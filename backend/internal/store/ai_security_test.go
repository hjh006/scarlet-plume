package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"strings"
	"testing"
	"time"
)

func aiTestConfig() aisecurity.Config {
	c := aisecurity.DefaultConfig()
	c.Policy.AllowedModelRegions = []string{"CN"}
	c.Applications = []aisecurity.Application{{ID: "enterprise-app", Name: "Approved application", Enabled: true, PrincipalIDs: []string{"branch-user"}, DocumentIDs: []string{"approved-document"}, ModelIDs: []string{"approved-model"}, ToolIDs: []string{"approved-tool"}}}
	c.Principals = []aisecurity.Principal{{ID: "branch-user", Enabled: true, ApplicationIDs: []string{"enterprise-app"}}}
	c.Documents = []aisecurity.Document{{ID: "approved-document", Version: "v1", ACLVersion: "acl-v1", KnowledgeBaseID: "knowledge", AllowedPrincipalIDs: []string{"branch-user"}, Enabled: true}}
	c.Models = []aisecurity.Model{{ID: "approved-model", EndpointRef: "model-endpoint", Region: "CN", Enabled: true, AllowedApplicationIDs: []string{"enterprise-app"}}}
	c.Tools = []aisecurity.Tool{{ID: "approved-tool", EndpointRef: "tool-endpoint", Enabled: true, AllowedApplicationIDs: []string{"enterprise-app"}, AllowedPrincipalIDs: []string{"branch-user"}, Actions: []string{"update"}, Targets: []string{"crm-approved"}, AllowedArguments: map[string][]string{"record": {"approved", "other-approved"}}, RequiredArguments: []string{"record"}, HighImpact: true}}
	return c
}
func TestAISecurityCurrentConfigEvaluationBoundaries(t *testing.T) {
	c := aiTestConfig()
	c.Policy.MaxRequestAgeSeconds = 5
	if e := aisecurity.ValidateConfig(c); e != nil {
		t.Fatal(e)
	}
	batch := aiEvaluateConfiguration(c, 4, time.Now().UTC())
	if batch.Status != "passed" {
		t.Fatalf("valid short TTL configuration failed: %+v", batch.Cases)
	}
	c.Documents = nil
	c.Applications[0].DocumentIDs = nil
	for i := 0; i < 513; i++ {
		id := fmt.Sprintf("doc-%04d", i)
		allowed := []string{}
		if i == 512 {
			allowed = []string{"branch-user"}
		}
		c.Documents = append(c.Documents, aisecurity.Document{ID: id, Version: "v1", ACLVersion: "acl-v1", KnowledgeBaseID: "knowledge", Enabled: true, AllowedPrincipalIDs: allowed})
		c.Applications[0].DocumentIDs = append(c.Applications[0].DocumentIDs, id)
	}
	batch = aiEvaluateConfiguration(c, 5, time.Now().UTC())
	if batch.Status != "passed" {
		t.Fatalf("late permitted ACL denied by candidate budget: %+v", batch.Cases)
	}
	c.Applications[0].Enabled = false
	batch = aiEvaluateConfiguration(c, 6, time.Now().UTC())
	if batch.Status != "passed" || !strings.Contains(strings.Join(batch.Scope, " "), "仅拒绝路径") {
		t.Fatal("explicit disable-all gate must remain a limited denial test")
	}
	batch = aiEvaluateConfiguration(aisecurity.DefaultConfig(), 0, time.Now().UTC())
	if batch.Status != "failed" {
		t.Fatal("empty initial catalog must not be publishable")
	}
}
func TestAISecurityPersistenceCASApprovalAndPublication(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	c := aiTestConfig()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	var reviewer int64
	name := fmt.Sprintf("ai-reviewer-%d", time.Now().UnixNano())
	must(f.pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, name).Scan(&reviewer))
	_, e := f.pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'reviewer')`, f.ws.ID, reviewer)
	must(e)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM plume.memberships WHERE user_id=$1`, reviewer)
		_, _ = f.pool.Exec(ctx, `DELETE FROM plume.users WHERE id=$1`, reviewer)
	})
	_, _, e = f.repo.AISecurityRuntimeConfig(ctx, f.ws.ID, f.user.ID)
	if !errors.Is(e, ErrConflict) {
		t.Fatal("unpublished config downloadable")
	}
	state, e := f.repo.SaveAISecurityConfiguration(ctx, f.ws.ID, f.user.ID, 0, c)
	must(e)
	if state.Revision != 1 {
		t.Fatal(state.Revision)
	}
	_, e = f.repo.SaveAISecurityConfiguration(ctx, f.ws.ID, f.user.ID, 0, c)
	if !errors.Is(e, ErrConflict) {
		t.Fatal("stale CAS accepted")
	}
	_, e = f.repo.SaveAISecurityConfiguration(ctx, f.ws.ID, reviewer, 1, c)
	if !errors.Is(e, ErrForbidden) {
		t.Fatal("reviewer edited config")
	}
	c.Approvals = []aisecurity.Approval{{ID: "forged"}}
	_, e = f.repo.SaveAISecurityConfiguration(ctx, f.ws.ID, f.user.ID, 1, c)
	if !errors.Is(e, ErrInvalid) {
		t.Fatal("client approval accepted")
	}
	c.Approvals = nil
	batch, e := f.repo.RunAISecurityEvaluations(ctx, f.ws.ID, f.user.ID)
	must(e)
	if batch.Status != "passed" {
		t.Fatalf("gate failed %+v", batch.Cases)
	}
	if batch.GateEligible {
		t.Fatal("legacy diagnostic eligible for new publication")
	}
	if _, e = f.repo.PublishAISecurity(ctx, f.ws.ID, reviewer, 1, batch.ID, "Legacy must not publish"); !errors.Is(e, ErrEvidenceRequired) {
		t.Fatalf("legacy publication accepted: %v", e)
	}
	pack, e := f.repo.ImportAISecurityRegressionPack(ctx, f.ws.ID, f.user.ID, "", BaselineAISecurityRegressionPack())
	must(e)
	batch, e = f.repo.RunAISecurityRegression(ctx, f.ws.ID, f.user.ID, pack.ID)
	must(e)
	if !batch.GateEligible {
		t.Fatalf("baseline coverage failed: %+v", batch)
	}
	_, e = f.repo.PublishAISecurity(ctx, f.ws.ID, f.user.ID, 1, batch.ID, "self")
	if !errors.Is(e, ErrForbidden) {
		t.Fatal("self published")
	}
	state, e = f.repo.PublishAISecurity(ctx, f.ws.ID, reviewer, 1, batch.ID, "Independent limited-scope review")
	must(e)
	if state.PublishedDigest != aisecurity.ConfigDigest(c) {
		t.Fatal("published digest differs")
	}
	newPack := BaselineAISecurityRegressionPack()
	newPack.Version = "2.0.0"
	newPack.Digest = aiPackDigest(newPack)
	if _, e = f.repo.ImportAISecurityRegressionPack(ctx, f.ws.ID, f.user.ID, "1.0.0", newPack); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.PublishAISecurity(ctx, f.ws.ID, reviewer, 1, batch.ID, "Superseded pack must not republish"); !errors.Is(e, ErrEvidenceRequired) {
		t.Fatalf("superseded pack gate reused: %v", e)
	}
	now := time.Now().UTC()
	request := aisecurity.Request{RequestID: "bounded-tool-request-001", Kind: "tool", ApplicationID: "enterprise-app", PrincipalID: "branch-user", ToolID: "approved-tool", Action: "update", Target: "crm-approved", Arguments: map[string]string{"record": "approved"}, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	proposed, e := f.repo.RequestAISecurityApproval(ctx, f.ws.ID, f.user.ID, 1, request)
	must(e)
	_, e = f.repo.ReviewAISecurityApproval(ctx, f.ws.ID, f.user.ID, proposed.ID, "approved", "self")
	if !errors.Is(e, ErrForbidden) {
		t.Fatal("self approved")
	}
	// More recent preview records must neither hide an explicit review target nor revoke valid grants.
	_, e = f.pool.Exec(ctx, `INSERT INTO plume.ai_security_approvals(workspace_id,configuration_revision,catalog_digest,request_digest,request_metadata,requested_by,status,expires_at) SELECT workspace_id,configuration_revision,catalog_digest,request_digest,request_metadata,requested_by,'rejected',expires_at FROM plume.ai_security_approvals,generate_series(1,101) WHERE id=$1`, proposed.ID)
	must(e)
	approved, e := f.repo.ReviewAISecurityApproval(ctx, f.ws.ID, reviewer, proposed.ID, "approved", "Bound target and arguments verified")
	must(e)
	if approved.Status != "approved" {
		t.Fatal(approved)
	}
	cfg, revision, e := f.repo.AISecurityRuntimeConfig(ctx, f.ws.ID, f.user.ID)
	must(e)
	if revision != 1 || len(cfg.Approvals) != 1 {
		t.Fatal("active authorization lost to preview budget")
	}
	request.ApprovalID = fmt.Sprint(approved.ID)
	decision, e := f.repo.EvaluateAISecurity(ctx, f.ws.ID, f.user.ID, request)
	must(e)
	if decision.Decision.Outcome != "allow" {
		t.Fatal(decision.Decision)
	}
	request.Arguments["record"] = "other-approved"
	decision, e = f.repo.EvaluateAISecurity(ctx, f.ws.ID, f.user.ID, request)
	must(e)
	if decision.Decision.Outcome != "deny" {
		t.Fatal("modified arguments retained approval")
	}
	request.Input = "raw-body-secret-that-must-not-persist"
	decision, e = f.repo.EvaluateAISecurity(ctx, f.ws.ID, f.user.ID, request)
	must(e)
	bundle, e := f.repo.AISecurityBundle(ctx, f.ws.ID, reviewer)
	must(e)
	raw, _ := json.Marshal(bundle)
	if strings.Contains(string(raw), request.Input) {
		t.Fatal("raw body persisted")
	}
	state, e = f.repo.SaveAISecurityConfiguration(ctx, f.ws.ID, f.user.ID, 1, c)
	must(e)
	_, e = f.repo.PublishAISecurity(ctx, f.ws.ID, reviewer, 2, batch.ID, "stale gate")
	if !errors.Is(e, ErrEvidenceRequired) {
		t.Fatal("old evaluation published a new revision")
	}
	cfg, _, e = f.repo.AISecurityRuntimeConfig(ctx, f.ws.ID, f.user.ID)
	must(e)
	if len(cfg.Approvals) != 0 {
		t.Fatal("config change did not invalidate prior approvals")
	}
	other := assessmentSourceTestStore(t)
	_, e = f.repo.AISecurityBundle(ctx, f.ws.ID, other.user.ID)
	if e == nil {
		t.Fatal("cross workspace read")
	}
	_, e = f.repo.ReviewAISecurityApproval(ctx, other.ws.ID, other.user.ID, proposed.ID, "approved", "wrong workspace")
	if e == nil {
		t.Fatal("cross workspace review")
	}
}
