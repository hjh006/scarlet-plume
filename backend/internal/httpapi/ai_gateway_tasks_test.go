package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func TestGatewayTaskManagementAndAuthenticatedMachineHTTP(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-gateway"
	machine := "/api/v1/internal/ai-gateway"
	cfg := gatewayHTTPConfig()
	cfg.Applications[0].RequireTask = true
	identityHTTPRequest(t, f.handler, "PUT", base+"/config", gatewayHTTPJSON(t, map[string]any{"expectedRevision": 0, "enabled": cfg.Enabled, "catalogDigest": cfg.CatalogDigest, "applications": cfg.Applications, "providers": cfg.Providers}), f.cookie, "", 200)
	response := identityHTTPRequest(t, f.handler, "POST", base+"/credentials", `{"applicationId":"app","name":"Task workload"}`, f.cookie, "", 201)
	var credential struct {
		Credential store.GatewayCredential `json:"credential"`
		Token      string                  `json:"token"`
	}
	if json.Unmarshal(response.Body.Bytes(), &credential) != nil {
		t.Fatal("credential JSON")
	}
	input := store.GatewayTaskInput{ApplicationID: "app", PrincipalID: "principal", CredentialID: credential.Credential.ID, Purpose: "support", CatalogDigest: cfg.CatalogDigest, ModelIDs: []string{"model"}, ToolIDs: []string{}, ToolScopes: []gatewaygovernance.TaskToolScope{}, DocumentIDs: []string{}, AllowedCategories: []string{}, MaxSteps: 3, BudgetMicros: 50, MaxDelegationDepth: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	identityHTTPRequest(t, f.handler, "GET", base+"/tasks", "", nil, "", 401)
	identityHTTPRequest(t, f.handler, "POST", base+"/tasks", gatewayHTTPJSON(t, input), f.viewerCookie, "", 403)
	created := identityHTTPRequest(t, f.handler, "POST", base+"/tasks", gatewayHTTPJSON(t, input), f.cookie, "", 201)
	var task store.GatewayTask
	if json.Unmarshal(created.Body.Bytes(), &task) != nil || task.ID == "" {
		t.Fatal("task create DTO")
	}
	identityHTTPRequest(t, f.handler, "GET", base+"/tasks?limit=1", "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "GET", base+"/tasks?limit=201", "", f.cookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", base+"/tasks/"+task.ID, "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "GET", base+"/tasks/missing", "", f.cookie, "", 404)
	control := strings.Repeat("task-control-token", 3)
	t.Setenv("SCARLET_GATEWAY_CONTROL_TOKENS", gatewayHTTPJSON(t, map[string]string{f.ws.Slug: control}))
	fact := gatewaygovernance.TaskCheckInput{TaskContext: gatewaygovernance.TaskContext{TaskID: task.ID, LeaseProtocolVersion: gatewaygovernance.TaskLeaseProtocolVersion, TaskStepID: "http-task-step-0001", TaskStepDigest: strings.Repeat("d", 64)}, WorkspaceSlug: f.ws.Slug, TokenHash: sessionHash(credential.Token), ApplicationID: "app", PrincipalID: "principal", CatalogDigest: cfg.CatalogDigest, Kind: "model", ModelID: "model"}
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/check", gatewayHTTPJSON(t, fact), nil, "", 401)
	checked := identityHTTPRequest(t, f.handler, "POST", machine+"/task/check", gatewayHTTPJSON(t, fact), nil, control, 200)
	var grant gatewaygovernance.TaskGrant
	if json.Unmarshal(checked.Body.Bytes(), &grant) != nil || grant.LeaseChainDigest == "" {
		t.Fatal("missing lease pin")
	}
	fact.LeaseChainDigest = grant.LeaseChainDigest
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/steps/admit", gatewayHTTPJSON(t, fact), nil, control, 200)
	identityHTTPRequest(t, f.handler, "GET", base+"/tasks/"+task.ID+"/steps?limit=1", "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "GET", base+"/tasks/"+task.ID+"/steps?before=invalid", "", f.cookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", base+"/tasks/"+task.ID+"/steps?limit=", "", f.cookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", base+"/tasks/missing/steps", "", f.cookie, "", 404)
	bad := fact
	bad.Categories = []string{"confidential"}
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/check", gatewayHTTPJSON(t, bad), nil, control, 403)
	// Task selection never authorizes caller-supplied identities or trusted labels.
	for _, extra := range []string{`"observedCategories":["safe"]`, `"sources":[]`} {
		body := strings.TrimSuffix(gatewayHTTPJSON(t, input), "}") + "," + extra + "}"
		identityHTTPRequest(t, f.handler, "POST", base+"/tasks", body, f.cookie, "", 400)
	}
	forgedPurpose := strings.TrimSuffix(gatewayHTTPJSON(t, fact), "}") + `,"purpose":"forged"}`
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/check", forgedPurpose, nil, control, 400)
	request := httptest.NewRequest(http.MethodPut, machine+"/task/check", strings.NewReader(gatewayHTTPJSON(t, fact)))
	request.Header.Set("Authorization", "Bearer "+control)
	request.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	f.handler.ServeHTTP(out, request)
	if out.Code != 403 {
		t.Fatal("task origin exemption widened", out.Code)
	}
	status := store.GatewayTaskStatusInput{ExpectedRevision: 1, Status: "cancelled", Reason: "Synthetic HTTP kill switch"}
	identityHTTPRequest(t, f.handler, "POST", base+"/tasks/"+task.ID+"/status", gatewayHTTPJSON(t, status), f.viewerCookie, "", 403)
	identityHTTPRequest(t, f.handler, "POST", base+"/tasks/"+task.ID+"/status", gatewayHTTPJSON(t, status), f.cookie, "", 200)
	identityHTTPRequest(t, f.handler, "POST", base+"/tasks/"+task.ID+"/status", gatewayHTTPJSON(t, status), f.cookie, "", 409)
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/check", gatewayHTTPJSON(t, fact), nil, control, 403)
}
