package aisecurity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMCPStrictToolResultRejectsAmbiguousSuccess(t *testing.T) {
	for name, result := range map[string]string{
		"missing_content":    `{"resultType":"complete"}`,
		"null_content":       `{"resultType":"complete","content":null}`,
		"missing_text":       `{"resultType":"complete","content":[{"type":"text"}]}`,
		"null_text":          `{"resultType":"complete","content":[{"type":"text","text":null}]}`,
		"null_error_flag":    `{"resultType":"complete","content":[],"isError":null}`,
		"error_case_alias":   `{"resultType":"complete","content":[],"isError":true,"IsError":false}`,
		"pending_case_alias": `{"resultType":"input_required","ResultType":"complete","content":[]}`,
		"content_case_alias": `{"resultType":"complete","Content":[{"type":"text","text":"done"}]}`,
		"text_case_alias":    `{"resultType":"complete","content":[{"type":"text","Text":"done"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, r := mcpFixture()
			var succeeded atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":%s}`, r.RequestID, result)
			}))
			defer up.Close()
			h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, func(_ context.Context, a AuditRecord) error {
				if a.Execution.Outcome == "succeeded" {
					succeeded.Add(1)
				}
				return nil
			}, nil)
			out := callRuntime(t, h, aliceToken, wire(r))
			if out.Code < 400 || succeeded.Load() != 0 {
				t.Fatalf("ambiguous MCP result was released/counted successful: status=%d successes=%d body=%s", out.Code, succeeded.Load(), out.Body.String())
			}
		})
	}
}

func TestMCPStrictResponseRejectsCaseAliasesAndAmbiguousHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		headers    http.Header
	}{
		{name: "id alias", body: `{"jsonrpc":"2.0","id":"other-request","ID":"expected-request","result":{"resultType":"complete","content":[]}}`},
		{name: "protocol alias", body: `{"jsonrpc":"1.0","JSONRPC":"2.0","id":"expected-request","result":{"resultType":"complete","content":[]}}`},
		{name: "empty session header", headers: http.Header{"Mcp-Session-Id": {""}}},
		{name: "duplicate session header", headers: http.Header{"Mcp-Session-Id": {"", "legacy-session"}}},
		{name: "duplicate protocol header", headers: http.Header{"Mcp-Protocol-Version": {MCPVersion, "2025-11-25"}}},
		{name: "empty protocol header", headers: http.Header{"Mcp-Protocol-Version": {""}}},
		{name: "duplicate content type", headers: http.Header{"Content-Type": {"application/json", "text/event-stream"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := tc.headers
			if headers == nil {
				headers = http.Header{}
			}
			if len(headers.Values("Content-Type")) == 0 {
				headers.Set("Content-Type", "application/json")
			}
			body := tc.body
			if body == "" {
				body = `{"jsonrpc":"2.0","id":"expected-request","result":{"resultType":"complete","content":[]}}`
			}
			if _, err := mcpResultBody([]byte(body), headers, "expected-request"); err == nil {
				t.Fatal("ambiguous protocol envelope/header accepted")
			}
		})
	}
}

func TestMCPTextAndStructuredOutputRemainChecked(t *testing.T) {
	for name, result := range map[string]string{
		"text":              `{"resultType":"complete","content":[{"type":"text","text":"done"}]}`,
		"empty_text":        `{"resultType":"complete","content":[{"type":"text","text":""}]}`,
		"structured_object": `{"resultType":"complete","content":[],"structuredContent":{"count":2}}`,
		"structured_null":   `{"resultType":"complete","content":[],"structuredContent":null}`,
		"structured_array":  `{"resultType":"complete","content":[],"structuredContent":[1,2]}`,
	} {
		t.Run(name, func(t *testing.T) {
			headers := http.Header{"Content-Type": {"application/json"}}
			raw := []byte(`{"jsonrpc":"2.0","id":"expected-request","result":` + result + `}`)
			if _, err := mcpResultBody(raw, headers, "expected-request"); err != nil {
				t.Fatal(err)
			}
		})
	}
	cfg, r := mcpFixture()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{"resultType":"complete","content":[],"structuredContent":{"contact":"person@example.com"}}}`, r.RequestID)
	}))
	defer up.Close()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, nil, nil)
	out := callRuntime(t, h, aliceToken, wire(r))
	if out.Code < 400 || strings.Contains(out.Body.String(), "person@example.com") {
		t.Fatal("structured output escaped data checks", out.Code)
	}
}

func TestMCPFacadeRejectsResourceReadsAndUnregisteredMethodsBeforeExecution(t *testing.T) {
	c := fixture()
	c.Tools[0].HighImpact = false
	var hits atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits)
	for _, method := range []string{"resources/read", "resources/list", "resources/templates/list", "sampling/createMessage", "elicitation/create"} {
		out := mcpFacadeRequest(g, aliceToken, method, 1, nil, nil)
		if out.Code != 404 || hits.Load() != 0 {
			t.Fatal("unadvertised method performed work", method, out.Code, hits.Load())
		}
	}
	out := mcpFacadeRequest(g, aliceToken, "tools/call", 1, map[string]any{"name": "unregistered-tool", "arguments": map[string]string{"ticketId": "ticket-17"}}, nil)
	if out.Code != 403 || hits.Load() != 0 {
		t.Fatal("unregistered tool reached upstream", out.Code, hits.Load())
	}
}

func TestMCPFacadeRejectsAmbiguousParametersBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		params       map[string]any
		change       func(*http.Request)
		want         int
	}{
		{name: "null string argument", method: "tools/call", params: map[string]any{"name": "close-ticket", "arguments": map[string]any{"ticketId": "ticket-17", "resolution": nil}}, want: 400},
		{name: "name case alias", method: "tools/call", params: map[string]any{"Name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}}, change: func(r *http.Request) { r.Header.Set("Mcp-Name", "close-ticket") }, want: 400},
		{name: "arguments case alias", method: "tools/call", params: map[string]any{"name": "close-ticket", "Arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}}, want: 400},
		{name: "unexpected list arguments", method: "tools/list", params: map[string]any{"arguments": map[string]string{}}, want: 400},
		{name: "empty session selector", method: "tools/list", change: func(r *http.Request) { r.Header.Set("Mcp-Session-Id", "") }, want: 400},
		{name: "empty resumption selector", method: "tools/list", change: func(r *http.Request) { r.Header.Set("Last-Event-ID", "") }, want: 400},
		{name: "JSON excluded by accept", method: "tools/list", change: func(r *http.Request) { r.Header.Set("Accept", "application/json;q=0.0,text/event-stream") }, want: 406},
		{name: "invalid accept quality", method: "tools/list", change: func(r *http.Request) { r.Header.Set("Accept", "application/json;q=NaN,text/event-stream") }, want: 406},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture()
			c.Tools[0].HighImpact = false
			c.Tools[0].AllowedArguments["resolution"] = []string{"fixed", ""}
			var hits atomic.Int32
			g := mcpFacadeFixture(t, &c, &hits)
			out := mcpFacadeRequest(g, aliceToken, tc.method, 1, tc.params, tc.change)
			if out.Code != tc.want || hits.Load() != 0 {
				t.Fatal("ambiguous MCP request accepted", out.Code, hits.Load(), out.Body.String())
			}
		})
	}
}

func TestMCPFacadeDiscoveryRechecksBeforeReleasingToolDirectory(t *testing.T) {
	for _, mode := range []string{"catalog", "controller"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture()
			c.Tools[0].HighImpact = false
			var hits, queries atomic.Int32
			g := mcpFacadeFixture(t, &c, &hits).(*gatewayHandler)
			g.discoverTools = func(context.Context, Identity, string, string) ([]string, error) {
				if queries.Add(1) == 1 {
					if mode == "catalog" {
						c.Tools[0].Enabled = false
					}
					return []string{"ticket"}, nil
				}
				return []string{}, nil
			}
			out := mcpFacadeRequest(g, aliceToken, "tools/list", 1, nil, nil)
			if out.Code < 400 || strings.Contains(out.Body.String(), "close-ticket") || hits.Load() != 0 {
				t.Fatal("revoked discovery schema released", out.Code, hits.Load(), out.Body.String())
			}
		})
	}
}

// Two successful checks follow the processing audit: Runtime writes its held
// buffer, then Gateway copies that buffer. MCP's final wrapper must make its own
// third check before a checked result crosses the actual client boundary.
func TestMCPFacadeFinalReleasePreservesOriginalExecutionContext(t *testing.T) {
	for _, mode := range []string{"allowed", "credential_revoked", "ancestor_lease_changed"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture()
			c.Applications[0].RequireTask = true
			c.Tools[0].HighImpact = false
			var hits, completed, checks atomic.Int32
			g := mcpFacadeFixture(t, &c, &hits).(*gatewayHandler)
			tasks := newTestTaskController()
			g.runtime.options.TaskControl = tasks.call
			actualCall := ""
			g.before = func(ctx context.Context, _ GatewayAuthorization) error {
				actualCall, _ = ctx.Value(gatewayCallIDKey{}).(string)
				return nil
			}
			g.runtime.options.Audit = func(_ context.Context, a AuditRecord) error {
				if a.Stage == "completed" && a.Execution.Outcome == "succeeded" {
					completed.Add(1)
				}
				return nil
			}
			g.runtime.options.Authenticate = func(ctx context.Context, credential string) (Identity, error) {
				if credential != tokenHash(aliceToken) {
					return Identity{}, errors.New("unexpected credential")
				}
				if completed.Load() > 0 {
					if got, _ := ctx.Value(gatewayCallIDKey{}).(string); got == "" || got != actualCall {
						return Identity{}, errors.New("original execution context lost")
					}
					if checks.Add(1) >= 3 {
						if mode == "credential_revoked" {
							return Identity{}, errors.New("credential revoked at final wrapper")
						}
						if mode == "ancestor_lease_changed" {
							tasks.mu.Lock()
							tasks.grant.LeaseChainDigest = strings.Repeat("c", 64)
							tasks.mu.Unlock()
						}
					}
				}
				return Identity{ApplicationID: "app", PrincipalID: "alice"}, nil
			}
			p := map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}}
			out := mcpFacadeRequest(g, aliceToken, "tools/call", 1, p, func(r *http.Request) { r.Header.Set("X-Scarlet-Task-Id", "task-one") })
			if mode == "allowed" {
				if out.Code != 200 || !strings.Contains(out.Body.String(), "synthetic tool answer") {
					t.Fatal("valid authenticated task result rejected", out.Code, out.Body.String())
				}
			} else if out.Code != 403 || strings.Contains(out.Body.String(), "synthetic tool answer") {
				t.Fatal("stale MCP wrapper result released", out.Code, out.Body.String())
			}
			if hits.Load() != 1 || completed.Load() != 1 || checks.Load() < 3 {
				t.Fatal("executed work was retried/reclassified or final check absent", hits.Load(), completed.Load(), checks.Load())
			}
		})
	}
}

func TestMCPFacadeDiscoveryPinsOriginalTaskLease(t *testing.T) {
	c := fixture()
	c.Applications[0].RequireTask = true
	c.Tools[0].HighImpact = false
	var hits, queries atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits).(*gatewayHandler)
	tasks := newTestTaskController()
	g.runtime.options.TaskControl = tasks.call
	g.discoverTools = func(context.Context, Identity, string, string) ([]string, error) {
		if queries.Add(1) > 1 {
			tasks.mu.Lock()
			tasks.grant.LeaseChainDigest = strings.Repeat("c", 64)
			tasks.mu.Unlock()
		}
		return []string{"ticket"}, nil
	}
	out := mcpFacadeRequest(g, aliceToken, "tools/list", 1, nil, func(r *http.Request) { r.Header.Set("X-Scarlet-Task-Id", "task-one") })
	if out.Code != 403 || strings.Contains(out.Body.String(), "close-ticket") || hits.Load() != 0 {
		t.Fatal("new lease blessed stale tool directory", out.Code, hits.Load(), out.Body.String())
	}
}

func TestMCPFacadeExactControlMetadataRetainsIndependentApproval(t *testing.T) {
	c := fixture()
	var hits atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits)
	meta := mcpMeta()
	meta[gatewayMCPControlMeta] = json.RawMessage(`{"requestId":"original-nonce-123456","RequestID":"alias-nonce-123456"}`)
	p := map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}, "_meta": meta}
	if out := mcpFacadeRequest(g, aliceToken, "tools/call", 1, p, nil); out.Code != 400 || hits.Load() != 0 {
		t.Fatal("case alias changed execution reference", out.Code, hits.Load())
	}
	meta[gatewayMCPControlMeta] = gatewayMCPControl{RequestID: "unapproved-mcp-call-123", IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	if out := mcpFacadeRequest(g, aliceToken, "tools/call", 2, p, nil); out.Code != 403 || hits.Load() != 0 {
		t.Fatal("typed metadata bypassed independent approval", out.Code, hits.Load())
	}
}
