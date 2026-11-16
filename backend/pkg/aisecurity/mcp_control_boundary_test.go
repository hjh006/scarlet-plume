package aisecurity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMCPSSERequiresATerminatedFinalEventAndKeepsConsumedNonce(t *testing.T) {
	for name, ending := range map[string]string{
		"no_line_ending":                     "",
		"single_lf":                          "\n",
		"single_crlf":                        "\r\n",
		"single_cr":                          "\r",
		"complete_then_pending_notification": "\n\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, r := mcpFixture()
			var hits atomic.Int32
			var events []AuditRecord
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%q,\"result\":{\"resultType\":\"complete\",\"content\":[{\"type\":\"text\",\"text\":\"synthetic done\"}]}}%s", r.RequestID, ending)
			}))
			defer upstream.Close()
			handler, journal := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, upstream.URL, func(_ context.Context, event AuditRecord) error {
				events = append(events, event)
				return nil
			}, nil)
			first := callRuntime(t, handler, aliceToken, wire(r))
			last := events[len(events)-1]
			if first.Code != 502 || hits.Load() != 1 || strings.Contains(first.Body.String(), "synthetic done") || !last.Execution.Attempted || last.Execution.Outcome != "unknown" {
				t.Fatalf("unterminated SSE became success or erased side effect: status=%d hits=%d execution=%+v", first.Code, hits.Load(), last.Execution)
			}
			path := journal.file.Name()
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenReplayJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			options := handler.(*runtimeHandler).options
			options.Journal = reopened
			restarted, err := NewRuntime(options)
			if err != nil {
				t.Fatal(err)
			}
			if second := callRuntime(t, restarted, aliceToken, wire(r)); second.Code != 409 || hits.Load() != 1 {
				t.Fatal("unknown original result lost durable nonce protection", second.Code, hits.Load())
			}
		})
	}
	for name, ending := range map[string]string{"lf": "\n\n", "crlf": "\r\n\r\n", "cr": "\r\r", "mixed": "\r\n\r"} {
		t.Run("complete_"+name, func(t *testing.T) {
			raw := []byte(`data: {"jsonrpc":"2.0","id":"expected-request","result":{"resultType":"complete","content":[]}}` + ending)
			if _, err := mcpResultBody(raw, http.Header{"Content-Type": {"text/event-stream"}}, "expected-request"); err != nil {
				t.Fatal("complete SSE event rejected", err)
			}
		})
	}
}

func TestMCPFacadeOmittedControlRetainsDefaultsAndTypedControlRemainsValid(t *testing.T) {
	for _, mode := range []string{"omitted", "empty_object", "explicit_valid"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture()
			c.Tools[0].HighImpact = false
			var hits atomic.Int32
			g := mcpFacadeFixture(t, &c, &hits)
			meta := mcpMeta()
			if mode == "empty_object" {
				meta[gatewayMCPControlMeta] = map[string]any{}
			}
			if mode == "explicit_valid" {
				now := time.Now().UTC()
				meta[gatewayMCPControlMeta] = gatewayMCPControl{RequestID: "explicit-valid-nonce-1234", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
			}
			params := map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}, "_meta": meta}
			out := mcpFacadeRequest(g, aliceToken, "tools/call", 1, params, nil)
			nonce := out.Header().Get("X-Scarlet-Request-ID")
			if out.Code != 200 || hits.Load() != 1 || !validID(nonce) || len(nonce) < 16 || mode == "explicit_valid" && nonce != "explicit-valid-nonce-1234" {
				t.Fatal("valid optional/typed metadata rejected or changed", mode, out.Code, hits.Load(), nonce)
			}
		})
	}
}

func TestMCPFacadeExplicitInvalidControlNeverBecomesANewExecution(t *testing.T) {
	for name, raw := range map[string]string{
		"null_request_id":  `{"requestId":null}`,
		"empty_request_id": `{"requestId":""}`,
		"null_issued_at":   `{"issuedAt":null}`,
		"null_expires_at":  `{"expiresAt":null}`,
		"zero_issued_at":   `{"issuedAt":"0001-01-01T00:00:00Z"}`,
		"zero_expires_at":  `{"expiresAt":"0001-01-01T00:00:00Z"}`,
		"null_approval_id": `{"approvalId":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := fixture()
			c.Tools[0].HighImpact = false
			var hits atomic.Int32
			g := mcpFacadeFixture(t, &c, &hits)
			meta := mcpMeta()
			meta[gatewayMCPControlMeta] = json.RawMessage(raw)
			params := map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}, "_meta": meta}
			for attempt := 0; attempt < 2; attempt++ {
				out := mcpFacadeRequest(g, aliceToken, "tools/call", attempt+1, params, nil)
				if out.Code != 400 || hits.Load() != 0 || out.Header().Get("X-Scarlet-Request-ID") != "" {
					t.Errorf("invalid explicit metadata silently became executable defaults: status=%d calls=%d nonce=%q", out.Code, hits.Load(), out.Header().Get("X-Scarlet-Request-ID"))
				}
			}
		})
	}
}

func TestMCPFacadeIdempotencyHeaderMustBeUnambiguous(t *testing.T) {
	for name, values := range map[string][]string{
		"empty":               {""},
		"duplicate_same":      {"stable-mcp-nonce-1234", "stable-mcp-nonce-1234"},
		"duplicate_different": {"stable-mcp-nonce-1234", "another-mcp-nonce-123"},
		"empty_first":         {"", "stable-mcp-nonce-1234"},
		"empty_second":        {"stable-mcp-nonce-1234", ""},
	} {
		t.Run(name, func(t *testing.T) {
			c := fixture()
			c.Tools[0].HighImpact = false
			var hits atomic.Int32
			g := mcpFacadeFixture(t, &c, &hits)
			out := mcpFacadeRequest(g, aliceToken, "tools/call", 1, map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}}, func(r *http.Request) {
				r.Header["Idempotency-Key"] = values
			})
			if out.Code != 400 || hits.Load() != 0 {
				t.Fatalf("ambiguous nonce header executed a tool: status=%d calls=%d", out.Code, hits.Load())
			}
		})
	}
}

func TestMCPFacadeValidNoncePersistsAcrossGatewayRestart(t *testing.T) {
	c := fixture()
	c.Tools[0].HighImpact = false
	var hits atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits).(*gatewayHandler)
	params := map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}}
	nonce := strings.Repeat("m", 24)
	change := func(r *http.Request) { r.Header.Set("Idempotency-Key", nonce) }
	first := mcpFacadeRequest(g, aliceToken, "tools/call", 1, params, change)
	if first.Code != 200 || hits.Load() != 1 || first.Header().Get("X-Scarlet-Request-ID") != nonce {
		t.Fatal("valid header-only nonce failed", first.Code, hits.Load())
	}
	path := g.runtime.options.Journal.file.Name()
	if err := g.runtime.options.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenReplayJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	options := g.runtime.options
	options.Journal = journal
	restarted, err := NewGateway(GatewayOptions{Runtime: options, Tools: []GatewayTool{{Name: "close-ticket", ToolID: "ticket", Action: "close"}}})
	if err != nil {
		t.Fatal(err)
	}
	second := mcpFacadeRequest(restarted, aliceToken, "tools/call", "different-rpc-id", params, change)
	if second.Code != 409 || hits.Load() != 1 || !strings.Contains(second.Body.String(), "request_replayed") {
		t.Fatal("gateway restart or changed RPC ID bypassed persisted nonce", second.Code, hits.Load())
	}
}
