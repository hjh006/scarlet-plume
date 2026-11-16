package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func TestGatewayTaskLeaseHTTPRejectsLegacyAdmissionAndReplay(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-gateway"
	machine := "/api/v1/internal/ai-gateway"
	cfg := gatewayHTTPConfig()
	cfg.Applications[0].RequireTask = true
	identityHTTPRequest(t, f.handler, "PUT", base+"/config", gatewayHTTPJSON(t, map[string]any{"expectedRevision": 0, "enabled": cfg.Enabled, "catalogDigest": cfg.CatalogDigest, "applications": cfg.Applications, "providers": cfg.Providers}), f.cookie, "", 200)
	createdCredential := identityHTTPRequest(t, f.handler, "POST", base+"/credentials", `{"applicationId":"app","name":"Lease protocol regression"}`, f.cookie, "", 201)
	var credential struct {
		Credential store.GatewayCredential `json:"credential"`
		Token      string                  `json:"token"`
	}
	if err := json.Unmarshal(createdCredential.Body.Bytes(), &credential); err != nil {
		t.Fatal(err)
	}
	input := store.GatewayTaskInput{ApplicationID: "app", PrincipalID: "principal", CredentialID: credential.Credential.ID, Purpose: "support", CatalogDigest: cfg.CatalogDigest, ModelIDs: []string{"model"}, ToolIDs: []string{}, ToolScopes: []gatewaygovernance.TaskToolScope{}, DocumentIDs: []string{}, AllowedCategories: []string{}, MaxSteps: 3, BudgetMicros: 50, MaxDelegationDepth: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	createdTask := identityHTTPRequest(t, f.handler, "POST", base+"/tasks", gatewayHTTPJSON(t, input), f.cookie, "", 201)
	var task store.GatewayTask
	if err := json.Unmarshal(createdTask.Body.Bytes(), &task); err != nil || task.ID == "" {
		t.Fatal("task creation", err)
	}
	control := strings.Repeat("lease-control-token", 3)
	t.Setenv("SCARLET_GATEWAY_CONTROL_TOKENS", gatewayHTTPJSON(t, map[string]string{f.ws.Slug: control}))
	fact := gatewaygovernance.TaskCheckInput{TaskContext: gatewaygovernance.TaskContext{TaskID: task.ID, TaskStepID: "lease-http-step-0001", TaskStepDigest: strings.Repeat("d", 64), LeaseProtocolVersion: gatewaygovernance.TaskLeaseProtocolVersion}, WorkspaceSlug: f.ws.Slug, TokenHash: sessionHash(credential.Token), ApplicationID: "app", PrincipalID: "principal", CatalogDigest: cfg.CatalogDigest, Kind: "model", ModelID: "model"}
	legacy := fact
	legacy.LeaseProtocolVersion = ""
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/check", gatewayHTTPJSON(t, legacy), nil, control, 403)
	legacy.LeaseProtocolVersion = "scarlet-task-lease-unknown"
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/check", gatewayHTTPJSON(t, legacy), nil, control, 403)
	checked := identityHTTPRequest(t, f.handler, "POST", machine+"/task/check", gatewayHTTPJSON(t, fact), nil, control, 200)
	var grant gatewaygovernance.TaskGrant
	if err := json.Unmarshal(checked.Body.Bytes(), &grant); err != nil || len(grant.LeaseChainDigest) != 64 || grant.LeaseProtocolVersion != gatewaygovernance.TaskLeaseProtocolVersion {
		t.Fatal("missing current lease grant", err)
	}
	// The first check can negotiate a pin. Neither admission nor authorization
	// may silently use the current pin on behalf of an old runtime.
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/steps/admit", gatewayHTTPJSON(t, fact), nil, control, 403)
	fact.LeaseChainDigest = grant.LeaseChainDigest
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/steps/admit", gatewayHTTPJSON(t, fact), nil, control, 200)
	authInput := store.GatewayAuthorizeInput{TaskContext: fact.TaskContext, WorkspaceSlug: f.ws.Slug, TokenHash: fact.TokenHash, ApplicationID: "app", PrincipalID: "principal", Kind: "model", RequestID: fact.TaskStepID, ModelID: "model", RequestDigest: fact.TaskStepDigest, CatalogDigest: cfg.CatalogDigest, InputBytes: 2, MaxOutputTokens: 8}
	missing := authInput
	missing.LeaseChainDigest = ""
	identityHTTPRequest(t, f.handler, "POST", machine+"/authorize", gatewayHTTPJSON(t, missing), nil, control, 403)
	wrong := authInput
	wrong.LeaseChainDigest = strings.Repeat("e", 64)
	identityHTTPRequest(t, f.handler, "POST", machine+"/authorize", gatewayHTTPJSON(t, wrong), nil, control, 403)
	authorized := identityHTTPRequest(t, f.handler, "POST", machine+"/authorize", gatewayHTTPJSON(t, authInput), nil, control, 200)
	var admission store.GatewayAuthorization
	if err := json.Unmarshal(authorized.Body.Bytes(), &admission); err != nil || admission.LeaseChainDigest != grant.LeaseChainDigest {
		t.Fatal("authorization lost lease pin", err)
	}
	// A real earlier reservation must not make a legacy replay bypass the gate.
	identityHTTPRequest(t, f.handler, "POST", machine+"/authorize", gatewayHTTPJSON(t, missing), nil, control, 403)
	audit := aisecurity.AuditRecord{AttemptID: "lease-http-attempt-0001", EventID: "lease-http-event-0001", RequestID: authInput.RequestID, Stage: "completed", RuntimeProtocolVersion: aisecurity.RuntimeProtocolVersion, InspectionVersion: aisecurity.InspectionVersion, ObservedAt: time.Now().UTC(), Decision: aisecurity.Decision{TaskID: task.ID, TaskPurpose: task.Purpose, TaskScopeDigest: task.ScopeDigest, LeaseChainDigest: grant.LeaseChainDigest, LeaseProtocolVersion: grant.LeaseProtocolVersion, Kind: "model", Outcome: "allow", CatalogDigest: cfg.CatalogDigest, RequestDigest: authInput.RequestDigest, ApplicationID: "app", PrincipalID: "principal", Categories: []string{}, AllowedDocumentIDs: []string{}}, Execution: aisecurity.Execution{Attempted: true, Outcome: "succeeded", HTTPStatus: 200}}
	settlement := store.GatewaySettlement{WorkspaceSlug: f.ws.Slug, ReservationID: admission.ReservationID, RequestDigest: authInput.RequestDigest, Audit: &audit}
	identityHTTPRequest(t, f.handler, "POST", machine+"/settle", gatewayHTTPJSON(t, settlement), nil, control, 200)
	rawAudit, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	completion := gatewaygovernance.TaskCompleteInput{TaskCheckInput: fact, Audit: rawAudit, Sources: []gatewaygovernance.TaskSource{}}
	legacyCompletion := completion
	legacyCompletion.LeaseChainDigest = ""
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/steps/complete", gatewayHTTPJSON(t, legacyCompletion), nil, control, 403)
	legacyCompletion = completion
	legacyCompletion.LeaseProtocolVersion = ""
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/steps/complete", gatewayHTTPJSON(t, legacyCompletion), nil, control, 403)
	identityHTTPRequest(t, f.handler, "POST", machine+"/task/steps/complete", gatewayHTTPJSON(t, completion), nil, control, 200)
}
