package aisecurity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func mcpFixture() (Config, Request) {
	c := fixture()
	c.Tools[0].Protocol = "mcp_http"
	c.Tools[0].TargetArgument = "resource"
	r := testRequest("tool")
	r.ToolID = "ticket"
	r.Action = "close"
	r.Target = "ticket-17"
	r.Arguments = map[string]string{"resolution": "fixed"}
	approve(&c, &r)
	return c, r
}
func TestMCPStatelessJSONCallUsesCatalogTargetAndActualProtocol(t *testing.T) {
	cfg, r := mcpFixture()
	var hits atomic.Int32
	events := []AuditRecord{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, in *http.Request) {
		hits.Add(1)
		if in.Method != "POST" || in.Header.Get("MCP-Protocol-Version") != MCPVersion || in.Header.Get("Mcp-Method") != "tools/call" || in.Header.Get("Mcp-Name") != "close" || !strings.Contains(in.Header.Get("Accept"), "application/json") || !strings.Contains(in.Header.Get("Accept"), "text/event-stream") || in.Header.Get("Mcp-Session-Id") != "" {
			t.Error("incorrect stateless MCP headers")
		}
		var req struct {
			JSONRPC, ID, Method string
			Params              struct {
				Name      string
				Arguments map[string]string
				Meta      map[string]any `json:"_meta"`
			}
		}
		if json.NewDecoder(in.Body).Decode(&req) != nil {
			t.Error("invalid RPC request")
		}
		if req.JSONRPC != "2.0" || req.Method != "tools/call" || req.ID != r.RequestID || req.Params.Name != "close" || req.Params.Arguments["resource"] != "ticket-17" || req.Params.Arguments["resolution"] != "fixed" || req.Params.Meta["io.modelcontextprotocol/protocolVersion"] != MCPVersion || req.Params.Meta["io.modelcontextprotocol/clientInfo"] == nil || req.Params.Meta["io.modelcontextprotocol/clientCapabilities"] == nil {
			t.Errorf("incorrect MCP body %#v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{"resultType":"complete","content":[{"type":"text","text":"closed"}],"isError":false}}`, req.ID)
	}))
	defer up.Close()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, func(_ context.Context, a AuditRecord) error { events = append(events, a); return nil }, nil)
	denied := r
	denied.ApprovalID = ""
	if out := callRuntime(t, h, aliceToken, wire(denied)); out.Code != 403 || hits.Load() != 0 {
		t.Fatal("unapproved MCP call reached upstream", out.Code)
	}
	override := r
	override.Arguments = map[string]string{"resolution": "fixed", "resource": "ticket-evil"}
	if out := callRuntime(t, h, aliceToken, wire(override)); out.Code != 403 || hits.Load() != 0 {
		t.Fatal("caller replaced trusted target", out.Code)
	}
	if out := callRuntime(t, h, bobToken, wire(r)); out.Code != 403 || hits.Load() != 0 {
		t.Fatal("cross-principal action reached MCP", out.Code)
	}
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 200 || hits.Load() != 1 {
		t.Fatal(out.Code, out.Body.String())
	}
	last := events[len(events)-1]
	if last.Stage != "completed" || last.Execution.Outcome != "succeeded" {
		t.Fatal(last)
	}
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 409 || hits.Load() != 1 {
		t.Fatal("MCP replay executed again")
	}
}
func TestMCPBoundedSSENotificationsAndMatchingFinalResponse(t *testing.T) {
	cfg, r := mcpFixture()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, in *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, ": keepalive\n\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"message\":\"progress only\"}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":%q,\"result\":{\"resultType\":\"complete\",\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\n\n", r.RequestID)
	}))
	defer up.Close()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, nil, nil)
	out := callRuntime(t, h, aliceToken, wire(r))
	if out.Code != 200 || strings.Contains(out.Body.String(), "progress only") || !json.Valid(out.Body.Bytes()) {
		t.Fatal(out.Code, out.Body.String())
	}
}
func TestMCPErrorsUnsupportedResultsAndOutputGuardsNeverClaimSuccess(t *testing.T) {
	cases := []string{"wrong_id", "rpc_error", "tool_error", "input_required", "image", "resource", "double_response", "legacy_session", "wrong_protocol", "only_notification", "oversize", "sensitive_text"}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			cfg, r := mcpFixture()
			events := []AuditRecord{}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, in *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				id := r.RequestID
				if mode == "wrong_id" {
					id = "wrong-request-123456"
				}
				if mode == "legacy_session" {
					w.Header().Set("Mcp-Session-Id", "legacy")
				}
				if mode == "wrong_protocol" {
					w.Header().Set("MCP-Protocol-Version", "2025-11-25")
				}
				payload := fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"resultType":"complete","content":[{"type":"text","text":"done"}]}}`, id)
				switch mode {
				case "rpc_error":
					payload = fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"error":{"code":-32602,"message":"bad"}}`, id)
				case "tool_error":
					payload = fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"resultType":"complete","isError":true,"content":[]}}`, id)
				case "input_required":
					payload = fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"resultType":"input_required","inputRequests":{}}}`, id)
				case "image":
					payload = fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"resultType":"complete","content":[{"type":"image","data":"AA=="}]}}`, id)
				case "resource":
					payload = fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"resultType":"complete","content":[{"type":"resource_link","uri":"file:///private"}]}}`, id)
				case "double_response":
					w.Header().Set("Content-Type", "text/event-stream")
					payload = "data: " + payload + "\n\ndata: " + payload + "\n\n"
				case "only_notification":
					w.Header().Set("Content-Type", "text/event-stream")
					payload = "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\n\n"
				case "oversize":
					payload = strings.Repeat("x", cfg.Policy.MaxOutputBytes+1)
				case "sensitive_text":
					payload = fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"resultType":"complete","content":[{"type":"text","text":"person@example.com"}]}}`, id)
				}
				io.WriteString(w, payload)
			}))
			defer up.Close()
			h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return cfg, nil }, up.URL, func(_ context.Context, a AuditRecord) error { events = append(events, a); return nil }, nil)
			out := callRuntime(t, h, aliceToken, wire(r))
			if out.Code < 400 || strings.Contains(out.Body.String(), "person@example.com") {
				t.Fatal(out.Code, out.Body.String())
			}
			if events[len(events)-1].Execution.Outcome == "succeeded" {
				t.Fatal("HTTP200 invalid MCP result counted as success")
			}
		})
	}
}
func TestMCPConfigurationTargetParameterCannotHaveTwoAuthorities(t *testing.T) {
	cfg, _ := mcpFixture()
	cfg.Tools[0].AllowedArguments["resource"] = []string{"ticket-17"}
	if ValidateConfig(cfg) == nil {
		t.Fatal("target argument in client schema accepted")
	}
	cfg, _ = mcpFixture()
	cfg.Tools[0].Protocol = "json"
	if ValidateConfig(cfg) == nil {
		t.Fatal("JSON protocol with MCP target injection accepted")
	}
}
