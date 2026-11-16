package httpapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func taskLeaseHTTPSetup(t *testing.T) (identityHTTPFixture, string, store.GatewayConfig, store.GatewayCredential, string, store.GatewayTask) {
	t.Helper()
	f := newIdentityHTTPFixture(t)
	ctx := t.Context()
	c := aisecurity.DefaultConfig()
	c.Policy.AllowedModelRegions = []string{"CN"}
	c.Applications = []aisecurity.Application{{ID: "app", Enabled: true, PrincipalIDs: []string{"principal"}, ModelIDs: []string{"model"}}}
	c.Principals = []aisecurity.Principal{{ID: "principal", Enabled: true, ApplicationIDs: []string{"app"}}}
	c.Models = []aisecurity.Model{{ID: "model", EndpointRef: "model-endpoint", Region: "CN", Enabled: true, AllowedApplicationIDs: []string{"app"}}}
	if e := aisecurity.ValidateConfig(c); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(c)
	digest := aisecurity.ConfigDigest(c)
	if _, e := f.pool.Exec(ctx, `INSERT INTO plume.ai_security_configurations(workspace_id,revision,configuration,updated_by,published_revision,published_configuration,published_digest,published_by,published_at)VALUES($1,1,$2,$3,1,$2,$4,$3,clock_timestamp())`, f.ws.ID, raw, f.owner.ID, digest); e != nil {
		t.Fatal(e)
	}
	cfg := gatewayHTTPConfig()
	cfg.CatalogDigest = digest
	cfg.Providers[0].Region = "CN"
	if _, e := f.repo.SaveGatewayConfig(ctx, f.ws.ID, f.owner.ID, 0, cfg); e != nil {
		t.Fatal(e)
	}
	credential, token, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.owner.ID, "app", "Original static")
	if e != nil {
		t.Fatal(e)
	}
	input := store.GatewayTaskInput{ApplicationID: "app", PrincipalID: "principal", CredentialID: credential.ID, Purpose: "support", CatalogDigest: digest, ModelIDs: []string{"model"}, ToolIDs: []string{}, ToolScopes: []gatewaygovernance.TaskToolScope{}, DocumentIDs: []string{}, AllowedCategories: []string{}, MaxSteps: 10, BudgetMicros: 50, MaxDelegationDepth: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.owner.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-gateway/tasks/" + task.ID
	return f, base, cfg, credential, token, task
}
func taskLeaseHTTPExpected(view store.TaskLeaseView) []store.TaskLeaseExpected {
	out := []store.TaskLeaseExpected{}
	for _, n := range view.Chain {
		out = append(out, store.TaskLeaseExpected{TaskID: n.TaskID, TaskRevision: n.TaskRevision, LeaseRevision: n.Lease.Revision, CredentialID: n.Lease.CredentialID})
	}
	return out
}
func TestTaskLeaseHTTPControlledRebindStrictContractAndRecoveryQueries(t *testing.T) {
	f, base, _, _, _, task := taskLeaseHTTPSetup(t)
	target, token, e := f.repo.CreateGatewayCredential(t.Context(), f.ws.ID, f.owner.ID, "app", "Visible rotation target")
	if e != nil {
		t.Fatal(e)
	}
	get := identityHTTPRequest(t, f.handler, "GET", base+"/credential-lease", "", f.viewerCookie, "", 200)
	var view store.TaskLeaseView
	if json.Unmarshal(get.Body.Bytes(), &view) != nil || !view.ObservedAt.Before(task.ExpiresAt) {
		t.Fatal("lease view")
	}
	pIn := store.TaskLeaseProposalInput{OperationID: "http-rebind-operation", ExpectedLeaseChainDigest: view.LeaseChainDigest, ExpectedChain: taskLeaseHTTPExpected(view), Changes: []store.TaskLeaseTarget{{TaskID: task.ID, NewCredentialID: target.ID}}, Reason: "计划轮换，保留历史授权。"}
	body := gatewayHTTPJSON(t, pIn)
	identityHTTPRequest(t, f.handler, "POST", base+"/credential-bindings", body, f.viewerCookie, "", 403)
	identityHTTPRequest(t, f.handler, "POST", base+"/credential-bindings", strings.TrimSuffix(body, "}")+`,"credentialToken":"forbidden"}`, f.cookie, "", 400)
	created := identityHTTPRequest(t, f.handler, "POST", base+"/credential-bindings", body, f.cookie, "", 201)
	var p store.TaskLeaseProposal
	if json.Unmarshal(created.Body.Bytes(), &p) != nil || p.ID < 1 || len(p.CredentialPlan) != 1 || p.CredentialPlan[0].Target.Name != target.Name {
		t.Fatal("readable frozen proposal missing")
	}
	if strings.Contains(created.Body.String(), token) || strings.Contains(created.Body.String(), sessionHash(token)) {
		t.Fatal("proposal leaked authentication material")
	}
	identityHTTPRequest(t, f.handler, "POST", base+"/credential-bindings", body, f.cookie, "", 201)
	pIn.Reason = "different"
	identityHTTPRequest(t, f.handler, "POST", base+"/credential-bindings", gatewayHTTPJSON(t, pIn), f.cookie, "", 409)
	identityHTTPRequest(t, f.handler, "GET", fmt.Sprintf("%s/credential-bindings/%d", base, p.ID), "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "GET", base+"/credential-bindings/by-operation/"+p.OperationID, "", f.viewerCookie, "", 200)
	reviewPath := fmt.Sprintf("%s/credential-bindings/%d/review", base, p.ID)
	review := `{"expectedRevision":1,"decision":"approve","note":"独立核定相同应用的轮换范围。"}`
	identityHTTPRequest(t, f.handler, "POST", reviewPath, review, f.cookie, "", 403)
	var reviewer int64
	if e = f.pool.QueryRow(t.Context(), `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, f.ws.Slug+"-reviewer").Scan(&reviewer); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(t.Context(), `INSERT INTO plume.memberships(workspace_id,user_id,role)VALUES($1,$2,'reviewer')`, f.ws.ID, reviewer); e != nil {
		t.Fatal(e)
	}
	session, e := sessionToken()
	if e != nil {
		t.Fatal(e)
	}
	if e = f.repo.CreateSession(t.Context(), reviewer, sessionHash(session), time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	cookie := &http.Cookie{Name: "__Host-scarlet-session", Value: session}
	applied := identityHTTPRequest(t, f.handler, "POST", reviewPath, review, cookie, "", 200)
	if !strings.Contains(applied.Body.String(), `"status":"applied"`) {
		t.Fatal("review did not atomically apply")
	}
	identityHTTPRequest(t, f.handler, "POST", reviewPath, review, cookie, "", 409)
	identityHTTPRequest(t, f.handler, "GET", base+"/credential-lease/history?limit=1", "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "GET", base+"/credential-bindings?limit=201", "", f.cookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", base+"/credential-bindings?before=bad", "", f.cookie, "", 400)
	// This action is authenticated by browser session and retains Origin checks.
	r := httptest.NewRequest("POST", reviewPath, strings.NewReader(review))
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://elsewhere.example.invalid")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("console Origin check bypass", w.Code)
	}
}
func TestTaskLeaseHTTPMachineRenewalAndExactHistoricalComplete(t *testing.T) {
	f, _, cfg, _, static, initial := taskLeaseHTTPSetup(t)
	ctx := t.Context()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	enc := base64.RawURLEncoding.EncodeToString
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]string{"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig", "kid": "renewal-http-key", "x": enc(pub)}}})
	input := store.GatewayWorkloadInput{Name: "Machine renewal fixture", ApplicationID: "app", Issuer: "https://issuer.example.invalid", Audience: "scarlet-workload:" + f.ws.Slug, Subject: "agent/support", JWKS: jwks, MaxTTLSeconds: 60}
	binding, e := f.repo.SaveGatewayWorkload(ctx, f.ws.ID, f.owner.ID, 0, 0, input, true)
	if e != nil {
		t.Fatal(e)
	}
	mint := func(jti string) store.GatewayWorkloadToken {
		t.Helper()
		now := time.Now().UTC()
		header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": "renewal-http-key", "typ": "JWT"})
		claims, _ := json.Marshal(map[string]any{"iss": input.Issuer, "aud": input.Audience, "sub": input.Subject, "iat": now.Unix(), "exp": now.Unix() + 120, "jti": jti})
		raw := enc(header) + "." + enc(claims)
		v, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, raw+"."+enc(ed25519.Sign(key, []byte(raw))))
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	old := mint("http-renew-old")
	taskIn := initial.GatewayTaskInput
	taskIn.CredentialID = old.Credential.ID
	task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.owner.ID, taskIn)
	if e != nil {
		t.Fatal(e)
	}
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-gateway/tasks/" + task.ID
	v, e := f.repo.GatewayTaskLeaseView(ctx, f.ws.ID, f.owner.ID, task.ID)
	if e != nil {
		t.Fatal(e)
	}
	fact := gatewaygovernance.TaskCheckInput{TaskContext: gatewaygovernance.TaskContext{TaskID: task.ID, TaskStepID: "machine-history-step", TaskStepDigest: strings.Repeat("b", 64), LeaseChainDigest: v.LeaseChainDigest, LeaseProtocolVersion: v.LeaseProtocolVersion}, WorkspaceSlug: f.ws.Slug, TokenHash: sessionHash(old.Token), ApplicationID: "app", PrincipalID: "principal", CatalogDigest: cfg.CatalogDigest, Kind: "model", ModelID: "model"}
	if _, e = f.repo.AdmitGatewayTask(ctx, fact); e != nil {
		t.Fatal(e)
	}
	authIn := store.GatewayAuthorizeInput{TaskContext: fact.TaskContext, WorkspaceSlug: f.ws.Slug, TokenHash: fact.TokenHash, ApplicationID: "app", PrincipalID: "principal", CatalogDigest: cfg.CatalogDigest, Kind: "model", ModelID: "model", RequestID: fact.TaskStepID, RequestDigest: fact.TaskStepDigest, InputBytes: 2, MaxOutputTokens: 8}
	g, e := f.repo.AuthorizeGateway(ctx, authIn)
	if e != nil {
		t.Fatal(e)
	}
	audit := aisecurity.AuditRecord{AttemptID: "http-renew-attempt", EventID: "http-renew-event", RequestID: fact.TaskStepID, Stage: "completed", RuntimeProtocolVersion: aisecurity.RuntimeProtocolVersion, InspectionVersion: aisecurity.InspectionVersion, ObservedAt: time.Now().UTC(), Decision: aisecurity.Decision{Outcome: "allow", Kind: "model", ApplicationID: "app", PrincipalID: "principal", CatalogDigest: cfg.CatalogDigest, RequestDigest: fact.TaskStepDigest, TaskID: task.ID, TaskPurpose: task.Purpose, TaskScopeDigest: task.ScopeDigest, LeaseChainDigest: v.LeaseChainDigest, LeaseProtocolVersion: v.LeaseProtocolVersion}, Execution: aisecurity.Execution{Attempted: true, Outcome: "succeeded", HTTPStatus: 200}}
	if e = f.repo.SettleGateway(ctx, store.GatewaySettlement{WorkspaceSlug: f.ws.Slug, ReservationID: g.ReservationID, RequestDigest: fact.TaskStepDigest, Audit: &audit}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, `UPDATE plume.ai_gateway_credentials SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, old.Credential.ID); e != nil {
		t.Fatal(e)
	}
	fresh := mint("http-renew-fresh")
	identityHTTPRequest(t, f.handler, "GET", base+"/credential-renewal", "", nil, static, 403)
	identityHTTPRequest(t, f.handler, "GET", base+"/credential-renewal", "", nil, old.Token, 403)
	identityHTTPRequest(t, f.handler, "GET", base+"/credential-renewal", "", f.cookie, fresh.Token, 401)
	preflight := identityHTTPRequest(t, f.handler, "GET", base+"/credential-renewal", "", nil, fresh.Token, 200)
	v = store.TaskLeaseView{}
	if json.Unmarshal(preflight.Body.Bytes(), &v) != nil || v.Chain[0].Identity != nil || v.Chain[0].Credential.Name != "" {
		t.Fatal("machine preflight not minimal")
	}
	renew := store.TaskLeaseRenewalInput{OperationID: "http-machine-renew", ExpectedLeaseChainDigest: v.LeaseChainDigest, ExpectedChain: taskLeaseHTTPExpected(v), ReplaceTaskIDs: []string{task.ID}}
	body := gatewayHTTPJSON(t, renew)
	identityHTTPRequest(t, f.handler, "POST", base+"/credential-renewal", strings.TrimSuffix(body, "}")+`,"newCredentialId":1}`, nil, fresh.Token, 400)
	renewed := identityHTTPRequest(t, f.handler, "POST", base+"/credential-renewal", body, nil, fresh.Token, 200)
	var result store.TaskLeaseRenewalResult
	if json.Unmarshal(renewed.Body.Bytes(), &result) != nil || result.Change.Mode != "renewal" {
		t.Fatal("renewal result")
	}
	identityHTTPRequest(t, f.handler, "POST", base+"/credential-renewal", body, nil, fresh.Token, 200)
	completion := map[string]any{"leaseChainDigest": result.Lease.LeaseChainDigest, "leaseProtocolVersion": result.Lease.LeaseProtocolVersion, "taskStepId": fact.TaskStepID, "taskStepDigest": fact.TaskStepDigest, "kind": "model", "modelId": "model", "sources": []gatewaygovernance.TaskSource{}, "audit": audit}
	identityHTTPRequest(t, f.handler, "POST", base+"/credential-renewal/complete-step", gatewayHTTPJSON(t, completion), nil, fresh.Token, 200)
	completion["applicationId"] = "evil"
	identityHTTPRequest(t, f.handler, "POST", base+"/credential-renewal/complete-step", gatewayHTTPJSON(t, completion), nil, fresh.Token, 400)
}
func TestTaskLeaseHTTPOriginExemptionIsExactAndRejectsDuplicateHeaders(t *testing.T) {
	base := "/api/v1/workspaces/synthetic/ai-gateway/tasks/task/credential-renewal"
	token := "Bearer sgw_" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		method, path, cookie string
		want                 bool
	}{{"POST", base, "", true}, {"POST", base + "/complete-step", "", true}, {"PUT", base, "", false}, {"POST", base + "/extra", "", false}, {"POST", strings.TrimSuffix(base, "credential-renewal") + "credential-bindings", "", false}, {"POST", base, "session=any", false}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", token)
		r.Header.Set("Cookie", tc.cookie)
		if got := gatewayTaskLeaseRenewalOriginExempt(r); got != tc.want {
			t.Fatal("origin exemption broadened", tc.path)
		}
	}
	r := httptest.NewRequest("POST", base, nil)
	r.Header.Add("Authorization", token)
	r.Header.Add("Authorization", token)
	if gatewayTaskLeaseRenewalOriginExempt(r) {
		t.Fatal("duplicate header accepted")
	}
	w := httptest.NewRecorder()
	if _, ok := taskLeaseBearer(w, r); ok || w.Code != 401 {
		t.Fatal("duplicate bearer not rejected")
	}
}
