package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func gatewayHTTPJSON(t *testing.T, v any) string {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}
func gatewayHTTPConfig() store.GatewayConfig {
	return store.GatewayConfig{Enabled: true, CatalogDigest: strings.Repeat("a", 64), Applications: []store.GatewayApplication{{ID: "app", Name: "App", Enabled: true, PrincipalID: "principal", AllowRag: true, ModelIDs: []string{"model"}, ToolIDs: []string{}, BudgetMicros: 100, RequestsPerMinute: 10, TokensPerMinute: 100}}, Providers: []store.GatewayProvider{{ID: "fixed", Name: "Fixed channel", Provider: "openai", ModelID: "model", UpstreamModel: "fixed-model", EndpointRef: "env:SCARLET_GATEWAY_BIFROST", SecretRef: "env:SCARLET_GATEWAY_API_KEY", Region: "us-east-1", ResponseFormat: "openai_chat", Enabled: true, InputMicrosPerMillion: 1_000_000, OutputMicrosPerMillion: 1_000_000}}}
}
func TestGatewayManagementAndMachineHTTP(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-gateway"
	machine := "/api/v1/internal/ai-gateway"
	cfg := gatewayHTTPConfig()
	configBody := gatewayHTTPJSON(t, map[string]any{"expectedRevision": 0, "enabled": cfg.Enabled, "catalogDigest": cfg.CatalogDigest, "applications": cfg.Applications, "providers": cfg.Providers})
	identityHTTPRequest(t, f.handler, "GET", base, "", nil, "", 401)
	identityHTTPRequest(t, f.handler, "GET", base, "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "PUT", base+"/config", configBody, f.viewerCookie, "", 403)
	identityHTTPRequest(t, f.handler, "PUT", base+"/config", configBody, f.cookie, "", 200)
	identityHTTPRequest(t, f.handler, "PUT", base+"/config", configBody, f.cookie, "", 409)
	identityHTTPRequest(t, f.handler, "POST", base+"/credentials", `{"applicationId":"app","name":"Denied"}`, f.viewerCookie, "", 403)
	created := identityHTTPRequest(t, f.handler, "POST", base+"/credentials", `{"applicationId":"app","name":"One time token"}`, f.cookie, "", 201)
	var credential struct {
		Credential store.GatewayCredential `json:"credential"`
		Token      string                  `json:"token"`
	}
	if e := json.Unmarshal(created.Body.Bytes(), &credential); e != nil || len(credential.Token) != 68 || created.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing one-time credential")
	}
	overview := identityHTTPRequest(t, f.handler, "GET", base, "", f.cookie, "", 200)
	if bytes.Contains(overview.Body.Bytes(), []byte(credential.Token)) || bytes.Contains(overview.Body.Bytes(), []byte(sessionHash(credential.Token))) {
		t.Fatal("credential recovered from overview")
	}
	exported := identityHTTPRequest(t, f.handler, "GET", base+"/config/export", "", f.cookie, "", 200)
	if exported.Header().Get("Content-Disposition") == "" {
		t.Fatal("missing config attachment")
	}
	controlToken := strings.Repeat("c", 40)
	t.Setenv("SCARLET_GATEWAY_CONTROL_TOKENS", gatewayHTTPJSON(t, map[string]string{f.ws.Slug: controlToken}))
	t.Setenv("SCARLET_GATEWAY_CONTROL_TOKEN", "")
	resolve := store.GatewayResolveInput{WorkspaceSlug: f.ws.Slug, TokenHash: sessionHash(credential.Token)}
	body := gatewayHTTPJSON(t, resolve)
	identityHTTPRequest(t, f.handler, "POST", machine+"/resolve", body, nil, "", 401)
	identityHTTPRequest(t, f.handler, "POST", machine+"/resolve", body, nil, strings.Repeat("x", 40), 401)
	identityHTTPRequest(t, f.handler, "POST", machine+"/resolve", body, nil, controlToken, 200)
	// The machine request reaches the real mux without browser Origin; control auth remains mandatory.
	foreign := resolve
	foreign.WorkspaceSlug = "other-workspace"
	identityHTTPRequest(t, f.handler, "POST", machine+"/resolve", gatewayHTTPJSON(t, foreign), nil, controlToken, 503)
	identityHTTPRequest(t, f.handler, "POST", machine+"/resolve", `{"workspaceSlug":"`+f.ws.Slug+`","token":"`+credential.Token+`"}`, nil, controlToken, 400)
	in := store.GatewayAuthorizeInput{WorkspaceSlug: f.ws.Slug, TokenHash: resolve.TokenHash, ApplicationID: "app", PrincipalID: "principal", Kind: "model", RequestID: "request-000000001", ModelID: "model", RequestDigest: strings.Repeat("b", 64), CatalogDigest: cfg.CatalogDigest, InputBytes: 2, MaxOutputTokens: 8}
	admitted := identityHTTPRequest(t, f.handler, "POST", machine+"/authorize", gatewayHTTPJSON(t, in), nil, controlToken, 200)
	var auth store.GatewayAuthorization
	if e := json.Unmarshal(admitted.Body.Bytes(), &auth); e != nil || auth.MaxCostMicros != 10 {
		t.Fatalf("authorization %+v err=%v", auth, e)
	}
	replayed := identityHTTPRequest(t, f.handler, "POST", machine+"/authorize", gatewayHTTPJSON(t, in), nil, controlToken, 200)
	var replay store.GatewayAuthorization
	_ = json.Unmarshal(replayed.Body.Bytes(), &replay)
	if !replay.Replayed || replay.ReservationID != auth.ReservationID {
		t.Fatal("replay not marked")
	}
	st := store.GatewaySettlement{WorkspaceSlug: f.ws.Slug, ReservationID: auth.ReservationID, RequestDigest: in.RequestDigest, Audit: &aisecurity.AuditRecord{AttemptID: "attempt-http-00001", EventID: "event-http-00001", RequestID: in.RequestID, Stage: "completed", ObservedAt: time.Now().UTC(), Decision: aisecurity.Decision{Kind: in.Kind, Outcome: "allow", CatalogDigest: in.CatalogDigest, RequestDigest: in.RequestDigest, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID}, Execution: aisecurity.Execution{Attempted: true, Outcome: "succeeded", HTTPStatus: 200}}}
	identityHTTPRequest(t, f.handler, "POST", machine+"/settle", gatewayHTTPJSON(t, st), nil, controlToken, 200)
	identityHTTPRequest(t, f.handler, "POST", machine+"/settle", gatewayHTTPJSON(t, st), nil, controlToken, 200)
	identityHTTPRequest(t, f.handler, "POST", machine+"/heartbeat", gatewayHTTPJSON(t, map[string]any{"workspaceSlug": f.ws.Slug, "id": "node-http", "status": "healthy", "failures": 0}), nil, controlToken, 200)
	audit := identityHTTPRequest(t, f.handler, "GET", base+"/audit/export?limit=2", "", f.viewerCookie, "", 200)
	var page store.GatewayAuditPage
	if e := json.Unmarshal(audit.Body.Bytes(), &page); e != nil || !page.Valid || len(page.Items) != 2 {
		t.Fatal("audit export invalid")
	}
	identityHTTPRequest(t, f.handler, "GET", base+"/audit?after=-1", "", f.cookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", base+"/reservations?limit=100", "", f.cookie, "", 200)
	machineExport := machine + "/audit/export?workspaceSlug=" + f.ws.Slug + "&limit=2"
	identityHTTPRequest(t, f.handler, "GET", machineExport, "", f.cookie, "", 401)
	exportedPage := identityHTTPRequest(t, f.handler, "GET", machineExport, "", nil, controlToken, 200)
	var exportedAudit store.GatewayAuditPage
	if e := json.Unmarshal(exportedPage.Body.Bytes(), &exportedAudit); e != nil || !exportedAudit.Valid || exportedAudit.WorkspaceID != f.ws.ID || len(exportedAudit.Items) != 2 {
		t.Fatal("machine audit export not bound to workspace")
	}
	identityHTTPRequest(t, f.handler, "GET", machine+"/audit/export?workspaceSlug=other-workspace", "", nil, controlToken, 503)
	identityHTTPRequest(t, f.handler, "GET", machineExport+"&after=-1", "", nil, controlToken, 400)

	identityHTTPRequest(t, f.handler, "DELETE", fmt.Sprintf("%s/credentials/%d", base, credential.Credential.ID), "", f.viewerCookie, "", 403)
	identityHTTPRequest(t, f.handler, "DELETE", fmt.Sprintf("%s/credentials/%d", base, credential.Credential.ID), "", f.cookie, "", 204)
	identityHTTPRequest(t, f.handler, "POST", machine+"/resolve", body, nil, controlToken, 403)
	// Prefix resemblance does not exempt arbitrary paths or methods from the browser Origin policy.
	r := httptest.NewRequest(http.MethodPut, machine+"/resolve", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+controlToken)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("PUT machine exemption: %d", w.Code)
	}
}
