package aisecurity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func gatewayRequest(h http.Handler, path, token, body, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	if id != "" {
		r.Header.Set("Idempotency-Key", id)
	}
	r.Header.Set("x-bf-direct-key", "attacker-key")
	r.Header.Set("x-bf-cache-key", "attacker-cache")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGatewayChatActualForwardAndDeniedProtocols(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("x-bf-cache-key") != "" || r.Header.Get("x-bf-direct-key") != "" {
			t.Error("gateway forwarded caller routing/cache credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"approved answer"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	g, err := NewGateway(GatewayOptions{Runtime: runtime.(*runtimeHandler).options})
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"model":"enterprise","messages":[{"role":"user","content":"synthetic request"}],"max_tokens":20}`
	for _, body := range []string{strings.Replace(valid, `"max_tokens":20`, `"fallbacks":["other"]`, 1), strings.Replace(valid, `"content":"synthetic request"`, `"content":[{"type":"image_url"}]`, 1), strings.Replace(valid, "synthetic request", "person@example.com", 1)} {
		if out := gatewayRequest(g, "/v1/chat/completions", aliceToken, body, ""); out.Code < 400 || hits.Load() != 0 {
			t.Fatal(out.Code, out.Body.String(), hits.Load())
		}
	}
	out := gatewayRequest(g, "/v1/chat/completions", aliceToken, valid, "gateway-request-123456")
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"content":"approved answer"`) || strings.Contains(out.Body.String(), "usage") || hits.Load() != 1 {
		t.Fatal(out.Code, out.Body.String(), hits.Load())
	}
	if out := gatewayRequest(g, "/v1/chat/completions", aliceToken, valid, "gateway-request-123456"); out.Code != 409 || hits.Load() != 1 {
		t.Fatal("gateway replay executed", out.Code, hits.Load())
	}
}

func TestGatewayDynamicCredentialCreationRevocationAndNoStaticFallback(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"safe"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	var enabled atomic.Bool
	o := runtime.(*runtimeHandler).options
	o.Authenticate = func(context.Context, string) (Identity, error) {
		if !enabled.Load() {
			return Identity{}, errors.New("revoked")
		}
		return Identity{ApplicationID: "app", PrincipalID: "alice"}, nil
	}
	g, err := NewGateway(GatewayOptions{Runtime: o})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"enterprise","messages":[{"role":"user","content":"safe"}]}`
	if out := gatewayRequest(g, "/v1/chat/completions", aliceToken, body, ""); out.Code != 401 || hits.Load() != 0 {
		t.Fatal("static fallback bypassed resolver", out.Code)
	}
	enabled.Store(true)
	if out := gatewayRequest(g, "/v1/chat/completions", bobToken, body, ""); out.Code != 200 || hits.Load() != 1 {
		t.Fatal(out.Code, out.Body.String())
	}
	enabled.Store(false)
	if out := gatewayRequest(g, "/v1/chat/completions", bobToken, body, ""); out.Code != 401 || hits.Load() != 1 {
		t.Fatal("revoked credential reached upstream", out.Code)
	}
}

func TestGatewayRevocationDuringActualProviderCallSuppressesOutput(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"must remain private"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	var revoked atomic.Bool
	o := runtime.(*runtimeHandler).options
	o.Authenticate = func(context.Context, string) (Identity, error) {
		if revoked.Load() {
			return Identity{}, errors.New("revoked while upstream active")
		}
		return Identity{ApplicationID: "app", PrincipalID: "alice"}, nil
	}
	g, err := NewGateway(GatewayOptions{Runtime: o})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- gatewayRequest(g, "/v1/chat/completions", aliceToken, `{"model":"enterprise","messages":[{"role":"user","content":"safe"}]}`, "")
	}()
	<-started
	revoked.Store(true)
	close(release)
	out := <-result
	if out.Code != 403 || strings.Contains(out.Body.String(), "must remain private") {
		t.Fatal("revoked credential received output", out.Code, out.Body.String())
	}
}

func TestFixedBifrostProfileRejectsTargetAndFormatDrift(t *testing.T) {
	p := BifrostProfile{Provider: "openai", Region: "CN", EndpointRef: "env:SCARLET_GATEWAY_ENDPOINT", BaseURL: "https://provider.example/v1", APIKeyEnv: "SCARLET_GATEWAY_PROVIDER_KEY", Models: []string{"enterprise"}, Port: 8080}
	c := fixture()
	c.Models = c.Models[:1]
	c.Applications[0].ModelIDs = []string{"enterprise"}
	c.Models[0].Provider = "openai"
	c.Models[0].ResponseFormat = "openai_chat"
	targets := map[string]Target{"model": {URL: BifrostRuntimeURL(p)}}
	if _, err := BifrostConfig(p, false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBifrostCatalog(p, c, targets); err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"endpoint", "region", "provider", "selector", "response"} {
		cc := clone(c)
		tt := map[string]Target{"model": targets["model"]}
		switch which {
		case "endpoint":
			tt["model"] = Target{URL: "https://other.invalid"}
		case "region":
			cc.Models[0].Region = "US"
		case "provider":
			cc.Models[0].Provider = "other"
		case "selector":
			cc.Models[0].UpstreamModel = "unapproved"
		case "response":
			cc.Models[0].ResponseFormat = "json"
		}
		if ValidateBifrostCatalog(p, cc, tt) == nil {
			t.Fatal("fixed route drift accepted", which)
		}
	}
}

func TestGatewayGovernanceRevisionChangeDuringProviderSuppressesOutput(t *testing.T) {
	var revision atomic.Int64
	revision.Store(1)
	controllerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"credentialId":1,"applicationId":"app","principalId":"alice","configRevision":%d}`, revision.Load())
	}))
	defer controllerServer.Close()
	controller, err := NewGatewayController(GatewayControllerOptions{URL: controllerServer.URL, Token: strings.Repeat("x", 32), WorkspaceSlug: "workspace", Catalog: func(context.Context) (Config, error) { return fixture(), nil }, AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"scope changed private output"},"finish_reason":"stop"}]}`)
	}))
	defer provider.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, provider.URL, nil, nil)
	opts := runtime.(*runtimeHandler).options
	opts.Authenticate = controller.Authenticate
	g, err := NewGateway(GatewayOptions{Runtime: opts, BeforeExecute: func(ctx context.Context, fact GatewayAuthorization) error {
		callID, _ := ctx.Value(gatewayCallIDKey{}).(string)
		controller.mu.Lock()
		controller.reservations[callID] = gatewayReservation{ID: "reservation", Digest: fact.RequestDigest, Revision: 1}
		controller.mu.Unlock()
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- gatewayRequest(g, "/v1/chat/completions", aliceToken, `{"model":"enterprise","messages":[{"role":"user","content":"safe"}]}`, "")
	}()
	<-started
	revision.Store(2)
	close(release)
	out := <-result
	if out.Code != 403 || strings.Contains(out.Body.String(), "scope changed private output") {
		t.Fatal("changed governance scope released output", out.Code, out.Body.String())
	}
}

func TestBifrostKnownMetadataDoesNotRelaxGenericResponseSchema(t *testing.T) {
	m := Model{ID: "enterprise", ResponseFormat: "openai_chat"}
	raw := []byte(`{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"safe"},"finish_reason":"stop"}],"extra_fields":{"raw_response":"person@example.com"}}`)
	if _, err := normalizeModelOutput(raw, m); err == nil {
		t.Fatal("generic strict response accepted Bifrost extension")
	}
	clean, err := normalizeBifrostEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := normalizeModelOutput(clean, m)
	if err != nil || strings.Contains(string(out), "person@example.com") || strings.Contains(string(out), "extra_fields") {
		t.Fatal(err, string(out))
	}
	wrong := []byte(strings.Replace(string(raw), `"model":"enterprise"`, `"model":"unapproved"`, 1))
	clean, err = normalizeBifrostEnvelope(wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = normalizeModelOutput(clean, m); err == nil {
		t.Fatal("metadata adapter allowed wrong model")
	}
	duplicate := []byte(strings.Replace(string(raw), `"model":"enterprise"`, `"model":"enterprise","model":"enterprise"`, 1))
	if _, err = normalizeBifrostEnvelope(duplicate); err == nil {
		t.Fatal("metadata adapter accepted duplicate key")
	}
}

func TestControllerLiveDiscoveryAndScopedHeartbeat(t *testing.T) {
	var heartbeats atomic.Int32
	catalogDigest := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
			t.Error("heartbeat lacked node authentication")
		}
		switch r.URL.Path {
		case "/api/v1/internal/ai-gateway/resolve":
			fmt.Fprintf(w, `{"credentialId":1,"configRevision":3,"applicationId":"app","principalId":"alice","catalogDigest":%q,"toolIds":["ticket"]}`, catalogDigest)
		case "/api/v1/internal/ai-gateway/heartbeat":
			var body struct {
				WorkspaceSlug, ID, Status string
				Failures                  int
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.WorkspaceSlug != "workspace" || body.ID != "node-1" || body.Status != "healthy" || body.Failures != 0 {
				t.Error("incorrect scoped heartbeat", body)
			}
			heartbeats.Add(1)
			io.WriteString(w, `{"accepted":true}`)
		default:
			http.Error(w, "bad operation", 404)
		}
	}))
	defer server.Close()
	client, err := NewGatewayController(GatewayControllerOptions{URL: server.URL, Token: strings.Repeat("x", 32), WorkspaceSlug: "workspace", Catalog: func(context.Context) (Config, error) { return fixture(), nil }, AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	if identity, err := client.Authenticate(context.Background(), strings.Repeat("b", 64)); err != nil || identity.PrincipalID != "alice" {
		t.Fatal("new resolve fields incompatible", identity, err)
	}
	tools, err := client.DiscoverTools(context.Background(), Identity{"app", "alice"}, strings.Repeat("b", 64), catalogDigest)
	if err != nil || len(tools) != 1 || tools[0] != "ticket" {
		t.Fatal(tools, err)
	}
	if _, err := client.DiscoverTools(context.Background(), Identity{"app", "bob"}, strings.Repeat("b", 64), catalogDigest); err == nil {
		t.Fatal("cross-principal discovery allowed")
	}
	if _, err := client.DiscoverTools(context.Background(), Identity{"app", "alice"}, strings.Repeat("b", 64), strings.Repeat("c", 64)); err == nil {
		t.Fatal("stale catalog discovery allowed")
	}
	if err := client.Heartbeat(context.Background(), "node-1", "healthy", 0); err != nil || heartbeats.Load() != 1 {
		t.Fatal(err, heartbeats.Load())
	}
}

func TestGatewayControllerTerminalSettlementAndReplayIsolation(t *testing.T) {
	c := fixture()
	c.Models[0].Provider = "openai"
	c.Models[0].ResponseFormat = "openai_chat"
	profile := BifrostProfile{Provider: "openai", Region: "CN", EndpointRef: "env:SCARLET_GATEWAY_ENDPOINT", APIKeyEnv: "SCARLET_GATEWAY_PROVIDER_KEY"}
	var settled atomic.Int32
	var replay atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/internal/ai-gateway/authorize":
			var in GatewayAuthorization
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				t.Fatal("bad fact")
			}
			fmt.Fprintf(w, `{"configRevision":1,"reservationId":"reservation-1","applicationId":"app","principalId":"alice","modelId":"enterprise","providerId":"provider-1","provider":"openai","upstreamModel":"enterprise","endpointRef":"env:SCARLET_GATEWAY_ENDPOINT","secretRef":"env:SCARLET_GATEWAY_PROVIDER_KEY","region":"CN","responseFormat":"openai_chat","requestId":%q,"requestDigest":%q,"catalogDigest":%q,"kind":"model","replayed":%t}`, in.RequestID, in.RequestDigest, in.CatalogDigest, replay.Load())
		case "/api/v1/internal/ai-gateway/settle":
			settled.Add(1)
			io.WriteString(w, `{"ok":true}`)
		case "/api/v1/internal/ai-gateway/events":
			io.WriteString(w, `{"ok":true}`)
		default:
			http.Error(w, "bad path", 404)
		}
	}))
	defer server.Close()
	client, err := NewGatewayController(GatewayControllerOptions{URL: server.URL, Token: strings.Repeat("x", 32), WorkspaceSlug: "workspace", Profile: profile, Catalog: func(context.Context) (Config, error) { return c, nil }, AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"safe"}`
	d := Evaluate(c, r, r.IssuedAt.Add(1))
	fact := GatewayAuthorization{Identity: Identity{"app", "alice"}, Kind: "model", RequestID: r.RequestID, ModelID: r.ModelID, RequestDigest: RequestDigest(r), CatalogDigest: ConfigDigest(c)}
	ctx := context.WithValue(context.Background(), gatewayCallIDKey{}, "call-1")
	if err := client.BeforeExecute(ctx, fact); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"decision", "prepared"} {
		if err := client.Audit(ctx, AuditRecord{Stage: stage, Decision: d, Execution: Execution{Outcome: "not_attempted"}}); err != nil {
			t.Fatal(err)
		}
	}
	if settled.Load() != 0 {
		t.Fatal("nonterminal allow refunded reservation")
	}
	replay.Store(true)
	newCtx := context.WithValue(context.Background(), gatewayCallIDKey{}, "call-2")
	if err := client.BeforeExecute(newCtx, fact); err == nil {
		t.Fatal("replayed authorization allowed")
	}
	denied := d
	denied.Outcome = "deny"
	if err := client.Audit(newCtx, AuditRecord{Stage: "decision", Decision: denied}); err != nil || settled.Load() != 0 {
		t.Fatal("new replay denial settled old reservation", err)
	}
	if err := client.Audit(ctx, AuditRecord{Stage: "completed", Decision: d, Execution: Execution{Attempted: true, Outcome: "unknown"}}); err != nil || settled.Load() != 1 {
		t.Fatal(err, settled.Load())
	}
}

func TestGatewayMCPApprovalAndScopeCannotBeBypassed(t *testing.T) {
	c, r := mcpFixture()
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%q,"result":{"resultType":"complete","content":[{"type":"text","text":"closed"}]}}`, r.RequestID)
	}))
	defer up.Close()
	var mu sync.Mutex
	before := 0
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	g, err := NewGateway(GatewayOptions{Runtime: runtime.(*runtimeHandler).options, Tools: []GatewayTool{{Name: "ticket.close", ToolID: "ticket", Action: "close"}}, BeforeExecute: func(context.Context, GatewayAuthorization) error { mu.Lock(); defer mu.Unlock(); before++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	makeBody := func(approval string) string {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": r.RequestID, "method": "tools/call", "params": map[string]any{"name": "ticket.close", "arguments": map[string]string{"resource": r.Target, "resolution": "fixed"}, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": MCPVersion, "io.modelcontextprotocol/clientCapabilities": map[string]any{}, "scarlet": map[string]any{"issuedAt": r.IssuedAt, "expiresAt": r.ExpiresAt, "approvalId": approval}}}})
		return string(raw)
	}
	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "/v1/mcp/execute", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+aliceToken)
		request.Header.Set("MCP-Protocol-Version", MCPVersion)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, request)
		return w
	}
	if out := call(makeBody("")); out.Code != 403 || calls.Load() != 0 || before != 0 {
		t.Fatal("unapproved tool executed", out.Code)
	}
	if out := call(makeBody(r.ApprovalID)); out.Code != 200 || calls.Load() != 1 || before != 1 {
		t.Fatal(out.Code, out.Body.String(), calls.Load())
	}
}
