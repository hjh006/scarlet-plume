package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func TestGatewayTaskIndependentApprovalUsesTrustedContextAndCancellation(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	catalog := aiTestConfig()
	catalog.Applications[0].RequireTask = true
	if _, e := f.repo.SaveAISecurityConfiguration(ctx, f.ws.ID, f.user.ID, 0, catalog); e != nil {
		t.Fatal(e)
	}
	config := gatewayTestConfig(1_000_000, 1000, 1_000_000)
	config.CatalogDigest = aisecurity.ConfigDigest(catalog)
	config.Applications[0].ID = "enterprise-app"
	config.Applications[0].PrincipalID = "branch-user"
	config.Applications[0].RequireTask = true
	config.Applications[0].ModelIDs = []string{"approved-model"}
	config.Applications[0].ToolIDs = []string{"approved-tool"}
	config.Providers[0].ModelID = "approved-model"
	if _, e := f.repo.SaveGatewayConfig(ctx, f.ws.ID, f.user.ID, 0, config); e != nil {
		t.Fatal(e)
	}
	credential, token, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "enterprise-app", "Task-bound operator")
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256([]byte(token))
	input := GatewayTaskInput{ApplicationID: "enterprise-app", PrincipalID: "branch-user", CredentialID: credential.ID, Purpose: "support", CatalogDigest: config.CatalogDigest, ModelIDs: []string{"approved-model"}, ToolIDs: []string{"approved-tool"}, ToolScopes: []gatewaygovernance.TaskToolScope{{ToolID: "approved-tool", Actions: []string{"update"}, Targets: []string{"crm-approved"}}}, DocumentIDs: []string{"approved-document"}, AllowedCategories: []string{}, MaxSteps: 10, BudgetMicros: 1000, MaxDelegationDepth: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	request := aisecurity.Request{TaskID: task.ID, RequestID: "task-independent-approval-001", Kind: "tool", ApplicationID: input.ApplicationID, PrincipalID: input.PrincipalID, ToolID: "approved-tool", Action: "update", Target: "crm-approved", Arguments: map[string]string{"record": "approved"}, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	proposal, e := f.repo.RequestAISecurityApproval(ctx, f.ws.ID, f.user.ID, 1, request)
	if e != nil || proposal.TaskID != task.ID {
		t.Fatal("task context blocked legitimate approval", proposal, e)
	}
	detail, e := f.repo.GatewayTaskDetail(ctx, f.ws.ID, f.user.ID, task.ID, 100)
	if e != nil || detail.Task.StepsUsed != 0 || detail.Task.ReservedMicros != 0 {
		t.Fatal("approval consumed an execution step", detail, e)
	}
	var reviewer int64
	name := fmt.Sprintf("task-reviewer-%d", time.Now().UnixNano())
	if e = f.pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, name).Scan(&reviewer); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'reviewer')`, f.ws.ID, reviewer); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM plume.memberships WHERE user_id=$1`, reviewer)
		_, _ = f.pool.Exec(ctx, `DELETE FROM plume.users WHERE id=$1`, reviewer)
	})
	if _, e = f.repo.ReviewAISecurityApproval(ctx, f.ws.ID, f.user.ID, proposal.ID, "approved", "self"); !errors.Is(e, ErrForbidden) {
		t.Fatal("self approval allowed", e)
	}
	approved, e := f.repo.ReviewAISecurityApproval(ctx, f.ws.ID, reviewer, proposal.ID, "approved", "Independent task and exact action verified")
	if e != nil || approved.Status != "approved" || approved.TaskID != task.ID {
		t.Fatal(approved, e)
	}
	resolved, e := f.repo.CheckGatewayTask(ctx, gatewaygovernance.TaskCheckInput{TaskContext: gatewaygovernance.TaskContext{TaskID: task.ID, TaskStepID: request.RequestID, TaskStepDigest: aisecurity.RequestDigest(request)}, WorkspaceSlug: f.ws.Slug, TokenHash: fmt.Sprintf("%x", sum), ApplicationID: input.ApplicationID, PrincipalID: input.PrincipalID, CatalogDigest: config.CatalogDigest, Kind: "tool", ToolID: request.ToolID, Action: request.Action, Target: request.Target})
	if e != nil || !resolved.Allowed {
		t.Fatal(e)
	}
	if _, e = f.repo.SetGatewayTaskStatus(ctx, f.ws.ID, f.user.ID, task.ID, GatewayTaskStatusInput{ExpectedRevision: 1, Status: "cancelled", Reason: "Stop task after approved action"}); e != nil {
		t.Fatal(e)
	}
	var status string
	if e = f.pool.QueryRow(ctx, `SELECT status FROM plume.ai_security_approvals WHERE workspace_id=$1 AND id=$2`, f.ws.ID, proposal.ID).Scan(&status); e != nil || status != "invalidated" {
		t.Fatal("cancel left task approval reusable", status, e)
	}
	if _, e = f.repo.RequestAISecurityApproval(ctx, f.ws.ID, f.user.ID, 1, request); !errors.Is(e, ErrForbidden) {
		t.Fatal("cancelled task got fresh approval", e)
	}
}
