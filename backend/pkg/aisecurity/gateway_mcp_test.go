package aisecurity

import (
	"bytes"
	"context"
	"encoding/base64"
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

func mcpFacadeRequest(h http.Handler, token, method string, id any, p map[string]any, change func(*http.Request)) *httptest.ResponseRecorder {
	if p == nil {
		p = map[string]any{}
	}
	if _, ok := p["_meta"]; !ok {
		p["_meta"] = mcpMeta()
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": p})
	r := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("MCP-Protocol-Version", MCPVersion)
	r.Header.Set("Mcp-Method", method)
	r.Header.Set("Accept", "application/json, text/event-stream")
	if name, ok := p["name"].(string); ok {
		r.Header.Set("Mcp-Name", name)
	}
	if change != nil {
		change(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func mcpFacadeFixture(t *testing.T, c *Config, hits *atomic.Int32) http.Handler {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var rpc struct {
			ID     string `json:"id"`
			Params struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			} `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&rpc) != nil || !validID(rpc.ID) || len(rpc.ID) < 16 || rpc.Params.Name != "close" || rpc.Params.Arguments["ticketId"] != "ticket-17" {
			t.Error("facade did not preserve authorized execution contract")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("MCP-Protocol-Version", MCPVersion)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{"resultType": "complete", "content": []map[string]string{{"type": "text", "text": "synthetic tool answer"}}, "_meta": map[string]string{"untrusted": "upstream-metadata-discarded"}}})
	}))
	t.Cleanup(up.Close)
	c.Tools[0].Protocol = "mcp_http"
	c.Tools[0].ProtocolVersion = MCPVersion
	c.Tools[0].TargetArgument = "ticketId"
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return *c, nil }, up.URL, nil, nil)
	g, e := NewGateway(GatewayOptions{Runtime: runtime.(*runtimeHandler).options, Tools: []GatewayTool{{Name: "close-ticket", ToolID: "ticket", Action: "close"}}})
	if e != nil {
		t.Fatal(e)
	}
	return g
}

func TestMCPFacadeDiscoveryScopeAndStableToolSchema(t *testing.T) {
	c := fixture()
	var hits atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits)
	for _, method := range []string{"server/discover", "tools/list"} {
		out := mcpFacadeRequest(g, aliceToken, method, 1, nil, nil)
		if out.Code != 200 || !strings.Contains(out.Body.String(), `"resultType":"complete"`) || !strings.Contains(out.Body.String(), `"id":1`) {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	out := mcpFacadeRequest(g, aliceToken, "tools/list", "discovery", nil, nil)
	if !strings.Contains(out.Body.String(), `"close-ticket"`) || !strings.Contains(out.Body.String(), `"additionalProperties":false`) || !strings.Contains(out.Body.String(), `"ticketId"`) {
		t.Fatal("trusted tool schema missing", out.Body.String())
	}
	out = mcpFacadeRequest(g, bobToken, "tools/list", 2, nil, nil)
	if out.Code != 200 || strings.Contains(out.Body.String(), "close-ticket") {
		t.Fatal("tool metadata crossed principal scope", out.Body.String())
	}
	c.Principals[0].Enabled = false
	if out = mcpFacadeRequest(g, aliceToken, "tools/list", 3, nil, nil); out.Code != 403 {
		t.Fatal("disabled principal discovered tools")
	}
	if hits.Load() != 0 {
		t.Fatal("discovery performed an upstream action")
	}
}

func TestMCPFacadeToolExecutionSeparateRPCIDNonceAndReplay(t *testing.T) {
	c := fixture()
	c.Tools[0].HighImpact = false
	var hits atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits)
	meta := mcpMeta()
	meta[gatewayMCPControlMeta] = map[string]any{"requestId": "mcp-execution-nonce-1234"}
	p := map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}, "_meta": meta}
	out := mcpFacadeRequest(g, aliceToken, "tools/call", -4, p, nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"id":-4`) || !strings.Contains(out.Body.String(), "synthetic tool answer") || strings.Contains(out.Body.String(), "upstream-metadata-discarded") || out.Header().Get("X-Scarlet-Request-ID") != "mcp-execution-nonce-1234" {
		t.Fatal(out.Code, out.Body.String())
	}
	out = mcpFacadeRequest(g, aliceToken, "tools/call", "different-jsonrpc-id", p, nil)
	if out.Code != 409 || hits.Load() != 1 || !strings.Contains(out.Body.String(), `"id":"different-jsonrpc-id"`) {
		t.Fatal("JSON-RPC ID change bypassed nonce", out.Code, hits.Load())
	}
}

func TestMCPFacadeHighImpactApprovalBindsExactArguments(t *testing.T) {
	c := fixture()
	var hits atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits)
	now := time.Now().UTC()
	r := Request{RequestID: "mcp-independent-approval-1234", Kind: "tool", ApplicationID: "app", PrincipalID: "alice", ToolID: "ticket", Action: "close", Target: "ticket-17", Arguments: map[string]string{"resolution": "fixed"}, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	meta := mcpMeta()
	meta[gatewayMCPControlMeta] = gatewayMCPControl{RequestID: r.RequestID, IssuedAt: r.IssuedAt, ExpiresAt: r.ExpiresAt}
	p := map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}, "_meta": meta}
	if out := mcpFacadeRequest(g, aliceToken, "tools/call", 1, p, nil); out.Code != 403 || hits.Load() != 0 {
		t.Fatal("high-impact action executed without independent approval")
	}
	approve(&c, &r)
	meta[gatewayMCPControlMeta] = gatewayMCPControl{RequestID: r.RequestID, IssuedAt: r.IssuedAt, ExpiresAt: r.ExpiresAt, ApprovalID: r.ApprovalID}
	p["arguments"] = map[string]string{"ticketId": "ticket-17", "resolution": "duplicate"}
	if out := mcpFacadeRequest(g, aliceToken, "tools/call", 2, p, nil); out.Code != 403 || hits.Load() != 0 {
		t.Fatal("approval accepted changed arguments")
	}
	p["arguments"] = map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}
	if out := mcpFacadeRequest(g, aliceToken, "tools/call", 3, p, nil); out.Code != 200 || hits.Load() != 1 {
		t.Fatal(out.Code, out.Body.String(), hits.Load())
	}
}

func TestMCPFacadeHeaderVersionCapabilityAndUnsupportedProfiles(t *testing.T) {
	c := fixture()
	c.Tools[0].HighImpact = false
	var hits atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits)
	for _, tc := range []struct {
		name, method string
		id           any
		params       map[string]any
		change       func(*http.Request)
		want         int
	}{
		{name: "null ID", method: "tools/list", id: nil, want: 400},
		{name: "fractional ID", method: "tools/list", id: 1.25, want: 400},
		{name: "browser origin", method: "tools/list", id: 1, change: func(r *http.Request) { r.Header.Set("Origin", "https://untrusted.invalid") }, want: 403},
		{name: "legacy initialize", method: "initialize", id: 1, want: 404},
		{name: "unadvertised MRTR", method: "subscriptions/listen", id: 1, want: 404},
		{name: "method mismatch", method: "tools/list", id: 1, change: func(r *http.Request) { r.Header.Set("Mcp-Method", "tools/call") }, want: 400},
		{name: "missing HTTP protocol", method: "tools/list", id: 1, change: func(r *http.Request) { r.Header.Del("MCP-Protocol-Version") }, want: 400},
		{name: "missing capabilities", method: "tools/list", id: 1, params: map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": MCPVersion}}, want: 400},
		{name: "old protocol", method: "tools/list", id: 1, params: map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2025-11-25", "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}, want: 400},
		{name: "accept JSON only", method: "tools/list", id: 1, change: func(r *http.Request) { r.Header.Set("Accept", "application/json") }, want: 406},
		{name: "list cursor unsupported", method: "tools/list", id: 1, params: map[string]any{"cursor": "unknown"}, want: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := mcpFacadeRequest(g, aliceToken, tc.method, tc.id, tc.params, tc.change)
			if out.Code != tc.want || !strings.Contains(out.Body.String(), `"jsonrpc":"2.0"`) {
				t.Fatal(out.Code, out.Body.String())
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatal("invalid metadata performed an action")
	}
}

func TestMCPFacadeMirroredArgumentsAndNonceCannotOverrideIdentity(t *testing.T) {
	c := fixture()
	c.Tools[0].HighImpact = false
	var hits atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits)
	p := map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}}
	for _, h := range []string{"different", "=?base64?" + base64.StdEncoding.EncodeToString([]byte("different")) + "?="} {
		out := mcpFacadeRequest(g, aliceToken, "tools/call", 1, p, func(r *http.Request) { r.Header.Set("Mcp-Param-Ticketid", h) })
		if out.Code != 400 {
			t.Fatal("mirrored target mismatch accepted")
		}
	}
	meta := mcpMeta()
	meta["applicationId"] = "app"
	meta["principalId"] = "alice"
	p["_meta"] = meta
	if out := mcpFacadeRequest(g, bobToken, "tools/call", 2, p, nil); out.Code != 403 || hits.Load() != 0 {
		t.Fatal("self-reported principal bypassed scopes")
	}
	meta[gatewayMCPControlMeta] = map[string]any{"requestId": "stable-nonce-123456789"}
	if out := mcpFacadeRequest(g, aliceToken, "tools/call", 3, p, func(r *http.Request) { r.Header.Set("Idempotency-Key", "different-nonce-123456789") }); out.Code != 400 || hits.Load() != 0 {
		t.Fatal("two nonce sources disagreed without rejection")
	}
	delete(meta, gatewayMCPControlMeta)
	out := mcpFacadeRequest(g, aliceToken, "tools/call", 4, p, func(r *http.Request) {
		r.Header.Set("Mcp-Name", "=?base64?"+base64.StdEncoding.EncodeToString([]byte("close-ticket"))+"?=")
		r.Header.Set("Mcp-Param-Ticketid", "ticket-17")
	})
	if out.Code != 200 || hits.Load() != 1 {
		t.Fatal(fmt.Sprint(out.Code), out.Body.String())
	}
}

func TestMCPFacadeDiscoveryIntersectsLiveGovernanceScopeWithoutReservation(t *testing.T) {
	c := fixture()
	c.Tools[0].HighImpact = false
	var hits atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits).(*gatewayHandler)
	var allowed, unavailable atomic.Bool
	allowed.Store(true)
	var reservations atomic.Int32
	g.before = func(context.Context, GatewayAuthorization) error { reservations.Add(1); return nil }
	g.discoverTools = func(_ context.Context, id Identity, hash, digest string) ([]string, error) {
		if id != (Identity{ApplicationID: "app", PrincipalID: "alice"}) || hash != tokenHash(aliceToken) || digest != ConfigDigest(c) {
			t.Error("discovery trusted caller metadata or wrong catalog")
		}
		if unavailable.Load() {
			return nil, errors.New("controller unavailable")
		}
		if allowed.Load() {
			return []string{"ticket"}, nil
		}
		return []string{}, nil
	}
	out := mcpFacadeRequest(g, aliceToken, "tools/list", 1, nil, nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "close-ticket") {
		t.Fatal(out.Code, out.Body.String())
	}
	allowed.Store(false)
	out = mcpFacadeRequest(g, aliceToken, "tools/list", 2, nil, nil)
	if out.Code != 200 || strings.Contains(out.Body.String(), "close-ticket") || strings.Contains(out.Body.String(), "ticket-17") {
		t.Fatal("revoked tool metadata leaked", out.Body.String())
	}
	p := map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}}
	if out = mcpFacadeRequest(g, aliceToken, "tools/call", 3, p, nil); out.Code != 403 || hits.Load() != 0 {
		t.Fatal("revoked tool reached upstream")
	}
	unavailable.Store(true)
	if out = mcpFacadeRequest(g, aliceToken, "tools/list", 4, nil, nil); out.Code != 503 || strings.Contains(out.Body.String(), "close-ticket") {
		t.Fatal("discovery failure used a stale local allowlist")
	}
	if reservations.Load() != 0 {
		t.Fatal("tool discovery consumed an execution reservation")
	}
}
