package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestGatewayTaskCredentialStatusHTTPIsReadOnlyScopedAndExplicitAboutUnknown(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-gateway"
	cfg := gatewayHTTPConfig()
	identityHTTPRequest(t, f.handler, "PUT", base+"/config", gatewayHTTPJSON(t, map[string]any{"expectedRevision": 0, "enabled": cfg.Enabled, "catalogDigest": cfg.CatalogDigest, "applications": cfg.Applications, "providers": cfg.Providers}), f.cookie, "", 200)
	var issued struct {
		Credential store.GatewayCredential `json:"credential"`
		Token      string                  `json:"token"`
	}
	if e := json.Unmarshal(identityHTTPRequest(t, f.handler, "POST", base+"/credentials", `{"applicationId":"app","name":"Task diagnostic fixture"}`, f.cookie, "", 201).Body.Bytes(), &issued); e != nil {
		t.Fatal(e)
	}
	input := store.GatewayTaskInput{ApplicationID: "app", PrincipalID: "principal", CredentialID: issued.Credential.ID, Purpose: "support", CatalogDigest: cfg.CatalogDigest, ModelIDs: []string{"model"}, ToolIDs: []string{}, DocumentIDs: []string{}, AllowedCategories: []string{}, MaxSteps: 10, BudgetMicros: 1000, MaxDelegationDepth: 0, ExpiresAt: time.Now().Add(time.Hour)}
	var task store.GatewayTask
	if e := json.Unmarshal(identityHTTPRequest(t, f.handler, "POST", base+"/tasks", gatewayHTTPJSON(t, input), f.cookie, "", 201).Body.Bytes(), &task); e != nil {
		t.Fatal(e)
	}
	path := base + "/tasks/" + task.ID + "/credential-status"
	identityHTTPRequest(t, f.handler, "GET", path, "", nil, "", 401)
	w := identityHTTPRequest(t, f.handler, "GET", path, "", f.viewerCookie, "", 200)
	var result store.GatewayTaskCredentialStatus
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil || result.RenewalSupported || result.CredentialChainStatus != "unknown" || result.Authorization.PrincipalStatus != "unknown" || len(result.AncestorPath) != 1 || result.AncestorPath[0].CredentialState != "active" || result.AncestorPath[0].ScopeDigest != task.ScopeDigest {
		t.Fatal("invalid diagnostic semantics", e, result)
	}
	if strings.Contains(w.Body.String(), issued.Token) || strings.Contains(w.Body.String(), sessionHash(issued.Token)) || strings.Contains(w.Body.String(), `"tokenHash"`) {
		t.Fatal("diagnostic exposed secret identity material")
	}
	foreign := newIdentityHTTPFixture(t)
	identityHTTPRequest(t, f.handler, "GET", path, "", foreign.cookie, "", 404)
	identityHTTPRequest(t, f.handler, "GET", "/api/v1/workspaces/"+foreign.ws.Slug+"/ai-gateway/tasks/"+task.ID+"/credential-status", "", foreign.cookie, "", 404)
	identityHTTPRequest(t, f.handler, "DELETE", fmt.Sprintf("%s/credentials/%d", base, issued.Credential.ID), "", f.viewerCookie, "", 403)
	identityHTTPRequest(t, f.handler, "DELETE", fmt.Sprintf("%s/credentials/%d", base, issued.Credential.ID), "", f.cookie, "", 204)
	w = identityHTTPRequest(t, f.handler, "GET", path, "", f.viewerCookie, "", 200)
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil || result.CredentialChainStatus != "blocked" || result.AncestorPath[0].CredentialState != "revoked" || result.AncestorPath[0].Credential.RevokedAt == nil || result.AncestorPath[0].Credential.RevocationReason != "manual" {
		t.Fatal("explicit revocation not visible", e, result)
	}
	page := identityHTTPRequest(t, f.handler, "GET", base+"/credentials?state=disabled", "", f.viewerCookie, "", 200)
	if !strings.Contains(page.Body.String(), `"revocationReason":"manual"`) || !strings.Contains(page.Body.String(), `"revokedAt"`) {
		t.Fatal("credential inventory did not project explicit revocation fact")
	}
	var detail store.GatewayTaskDetail
	if e := json.Unmarshal(identityHTTPRequest(t, f.handler, "GET", base+"/tasks/"+task.ID, "", f.viewerCookie, "", 200).Body.Bytes(), &detail); e != nil || detail.Task.ScopeDigest != task.ScopeDigest || detail.Task.Revision != task.Revision || detail.Task.StepsUsed != 0 || detail.Task.ReservedMicros != 0 || detail.Task.SpentMicros != 0 {
		t.Fatal("diagnosis/revocation changed frozen task authority", e)
	}
}
