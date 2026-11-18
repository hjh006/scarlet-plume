package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestAISecurityApprovalRespectsToolHandlingDeadline(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	var reviewer int64
	username := fmt.Sprintf("approval-authority-reviewer-%d", f.ws.ID)
	if err := f.pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, username).Scan(&reviewer); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = f.pool.Exec(cleanupCtx, `DELETE FROM plume.ai_security_approvals WHERE workspace_id=$1 AND reviewed_by=$2`, f.ws.ID, reviewer)
		_, _ = f.pool.Exec(cleanupCtx, `DELETE FROM plume.memberships WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, reviewer)
		_, _ = f.pool.Exec(cleanupCtx, `DELETE FROM plume.users WHERE id=$1`, reviewer)
	})
	if _, err := f.pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'reviewer')`, f.ws.ID, reviewer); err != nil {
		t.Fatal(err)
	}
	var now time.Time
	if err := f.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(3 * time.Second)
	catalog := aiTestConfig()
	catalog.Tools[0].Handling = &aisecurity.DataHandling{
		AuthorizationExpiresAt: deadline, TrainingUse: "prohibited", EvidenceRef: "synthetic-handling-contract",
	}
	if _, err := f.repo.SaveAISecurityConfiguration(ctx, f.ws.ID, f.user.ID, 0, catalog); err != nil {
		t.Fatal(err)
	}
	request := aisecurity.Request{
		RequestID: "synthetic-approval-deadline-01", Kind: "tool", ApplicationID: "enterprise-app",
		PrincipalID: "branch-user", ToolID: "approved-tool", Action: "update", Target: "crm-approved",
		Arguments: map[string]string{"record": "approved"}, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	proposal, err := f.repo.RequestAISecurityApproval(ctx, f.ws.ID, f.user.ID, 1, request)
	if err != nil || proposal.Status != "proposed" {
		t.Fatalf("proposal while the tool contract is current: %s, %v", proposal.Status, err)
	}
	if _, err := f.pool.Exec(ctx, `SELECT pg_sleep_until($1)`, deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.ReviewAISecurityApproval(ctx, f.ws.ID, reviewer, proposal.ID, "approved", "Reviewed the exact action"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("review must reject a grant after the handling deadline: %v", err)
	}
	request.RequestID = "synthetic-approval-deadline-02"
	if _, err := f.repo.RequestAISecurityApproval(ctx, f.ws.ID, f.user.ID, 1, request); !errors.Is(err, ErrForbidden) {
		t.Fatalf("proposal must reject the expired handling contract: %v", err)
	}
	rejected, err := f.repo.ReviewAISecurityApproval(ctx, f.ws.ID, reviewer, proposal.ID, "rejected", "Handling contract expired before review")
	if err != nil || rejected.Status != "rejected" {
		t.Fatalf("independent reviewer can still decline the proposal: %s, %v", rejected.Status, err)
	}
}
