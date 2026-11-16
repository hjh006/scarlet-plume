package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAIRegressionPackValidationRejectsMislabelingAndTampering(t *testing.T) {
	baseline := BaselineAISecurityRegressionPack()
	if v, e := ValidateAISecurityRegressionPack(baseline, true); e != nil || v.CaseCount != len(baseline.Cases) || len(v.CoverageGaps) != 0 {
		t.Fatalf("baseline: %+v %v", v, e)
	}
	for _, mutate := range []func(*AISecurityRegressionPack){
		func(p *AISecurityRegressionPack) { p.Version = "changed-without-new-digest" },
		func(p *AISecurityRegressionPack) { p.Cases[0].Dimension = "model" },
		func(p *AISecurityRegressionPack) { p.Cases[0].Probe = "unregistered-arbitrary-prompt" },
		func(p *AISecurityRegressionPack) { p.Cases[1].ID = p.Cases[0].ID },
		func(p *AISecurityRegressionPack) { p.Cases[0].Name = strings.Repeat("x", 161) },
	} {
		raw, _ := json.Marshal(baseline)
		var p AISecurityRegressionPack
		_ = json.Unmarshal(raw, &p)
		mutate(&p)
		if _, e := ValidateAISecurityRegressionPack(p, true); !errors.Is(e, ErrInvalid) {
			t.Fatalf("invalid package accepted: %v", e)
		}
	}
	baseline.Digest = ""
	if _, e := ValidateAISecurityRegressionPack(baseline, true); !errors.Is(e, ErrInvalid) {
		t.Fatal("unverified import accepted")
	}
	if v, e := ValidateAISecurityRegressionPack(baseline, false); e != nil || v.Pack.Digest == "" {
		t.Fatal("dry-run did not compute digest")
	}
}
func TestAIRegressionRequiredProbesCannotBeClaimedOrOmitted(t *testing.T) {
	c := aiTestConfig()
	p := BaselineAISecurityRegressionPack()
	now := time.Now().UTC()
	base := aiEvaluateRegression(c, 1, p, aiPackSummary(1, p, now), now)
	if !base.GateEligible {
		t.Fatalf("valid baseline: %+v", base)
	}
	p.Cases = slices.DeleteFunc(p.Cases, func(v AISecurityRegressionCase) bool { return v.Probe == "rag.scope" })
	p.Digest = aiPackDigest(p)
	preview, e := ValidateAISecurityRegressionPack(p, true)
	if e != nil || len(preview.CoverageGaps) != 0 {
		t.Fatal("remaining negative probes should still declare the rag dimension")
	}
	result := aiEvaluateRegression(c, 1, p, aiPackSummary(2, p, now), now)
	if result.GateEligible || !slices.Contains(result.CoverageGaps, "missing_or_failed_probe:rag.scope") {
		t.Fatalf("coverage laundering: %+v", result)
	}
	c.Applications[0].Enabled = false
	p = BaselineAISecurityRegressionPack()
	result = aiEvaluateRegression(c, 2, p, aiPackSummary(1, p, now), now)
	if !result.GateEligible || !slices.Contains(result.RequiredDimensions, "deny_all") {
		t.Fatalf("deny-all scope: %+v", result)
	}
}

func TestAIRegressionSentinelCannotCollideWithCustomerScope(t *testing.T) {
	c := aiTestConfig()
	sentinel := "unregistered-configuration-gate"
	c.Applications[0].ID = sentinel
	c.Applications[0].PrincipalIDs = []string{sentinel}
	c.Principals[0].ID = sentinel
	c.Principals[0].ApplicationIDs = []string{sentinel}
	c.Documents[0].AllowedPrincipalIDs = []string{sentinel}
	c.Models[0].AllowedApplicationIDs = []string{sentinel}
	c.Tools[0].AllowedApplicationIDs = []string{sentinel}
	c.Tools[0].AllowedPrincipalIDs = []string{sentinel}
	c.Tools[0].Targets = []string{sentinel}
	p := BaselineAISecurityRegressionPack()
	now := time.Now().UTC()
	if out := aiEvaluateRegression(c, 1, p, aiPackSummary(1, p, now), now); !out.GateEligible {
		t.Fatalf("legitimate customer names collide with a negative probe: %+v", out)
	}
}
func TestAIRegressionComparisonSeparatesDefinitionChangesAndRegressions(t *testing.T) {
	c := aiTestConfig()
	p := BaselineAISecurityRegressionPack()
	now := time.Now().UTC()
	a := aiEvaluateRegression(c, 1, p, aiPackSummary(1, p, now), now)
	for i := range p.Cases {
		if p.Cases[i].Probe == "rag.scope" {
			p.Cases[i].ExpectedOutcome = "deny"
		}
	}
	p.Version = "2.0.0"
	p.Digest = aiPackDigest(p)
	b := aiEvaluateRegression(c, 1, p, aiPackSummary(2, p, now), now)
	cmp, e := aiCompareRegression(a, b)
	if e != nil || b.GateEligible || len(cmp.ChangedCases) != 1 || len(cmp.Regressions) != 0 || !cmp.ChangedCases[0].DefinitionChanged {
		t.Fatalf("definition change confused with accuracy: %+v", cmp)
	}
	p = BaselineAISecurityRegressionPack()
	c.Policy.BlockedInputPatterns = []string{"configuration gate synthetic"}
	b = aiEvaluateRegression(c, 2, p, aiPackSummary(1, p, now), now)
	cmp, e = aiCompareRegression(a, b)
	if e != nil || b.GateEligible || len(cmp.Regressions) == 0 {
		t.Fatalf("real configured-scope regression lost: %+v", cmp)
	}
}
func TestAIRegressionPersistenceCASIsolationAndIntegrity(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	p := BaselineAISecurityRegressionPack()
	if _, e := f.repo.SaveAISecurityConfiguration(ctx, f.ws.ID, f.user.ID, 0, aiTestConfig()); e != nil {
		t.Fatal(e)
	}
	one, e := f.repo.ImportAISecurityRegressionPack(ctx, f.ws.ID, f.user.ID, "", p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ImportAISecurityRegressionPack(ctx, f.ws.ID, f.user.ID, "", p); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale import: %v", e)
	}
	p.Version = "2.0.0"
	p.Digest = aiPackDigest(p)
	if _, e = f.repo.ImportAISecurityRegressionPack(ctx, f.ws.ID, f.user.ID, "wrong", p); !errors.Is(e, ErrConflict) {
		t.Fatal("pack CAS bypass")
	}
	two, e := f.repo.ImportAISecurityRegressionPack(ctx, f.ws.ID, f.user.ID, "1.0.0", p)
	if e != nil {
		t.Fatal(e)
	}
	a, e := f.repo.RunAISecurityRegression(ctx, f.ws.ID, f.user.ID, one.ID)
	if e != nil || a.GateEligible || !slices.Contains(a.CoverageGaps, "test_pack_superseded") {
		t.Fatalf("superseded version became publishable: %+v %v", a, e)
	}
	b, e := f.repo.RunAISecurityRegression(ctx, f.ws.ID, f.user.ID, two.ID)
	if e != nil {
		t.Fatal(e)
	}
	cmp, e := f.repo.CompareAISecurityRegression(ctx, f.ws.ID, f.user.ID, a.ID, b.ID)
	if e != nil || len(cmp.Regressions) != 0 {
		t.Fatal("stable pack comparison", e)
	}
	if cmp.From.CreatedAt.IsZero() || cmp.To.CreatedAt.IsZero() {
		t.Fatal("comparison lost persisted evaluation timestamps")
	}
	foreign := assessmentSourceTestStore(t)
	if _, e = f.repo.AISecurityRegressionPack(ctx, foreign.ws.ID, foreign.user.ID, one.ID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("foreign pack leaked: %v", e)
	}
	if _, e = f.repo.CompareAISecurityRegression(ctx, foreign.ws.ID, foreign.user.ID, a.ID, b.ID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("foreign evaluations leaked: %v", e)
	}
	if _, e = f.pool.Exec(ctx, `UPDATE plume.ai_security_regression_packs SET pack=jsonb_set(pack,'{riskTags}','["tampered"]') WHERE id=$1`, one.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.RunAISecurityRegression(ctx, f.ws.ID, f.user.ID, one.ID); !errors.Is(e, ErrConflict) {
		t.Fatalf("tampered frozen pack adopted: %v", e)
	}
}
func TestAIRegressionPublicationRechecksExpiredAuthorization(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	c := aiTestConfig()
	expired := time.Now().UTC().Add(-time.Minute)
	c.Principals[0].AuthorizationExpiresAt = &expired
	if _, e := f.repo.SaveAISecurityConfiguration(ctx, f.ws.ID, f.user.ID, 0, c); e != nil {
		t.Fatal(e)
	}
	p := BaselineAISecurityRegressionPack()
	summary, e := f.repo.ImportAISecurityRegressionPack(ctx, f.ws.ID, f.user.ID, "", p)
	if e != nil {
		t.Fatal(e)
	}
	old := aiEvaluateRegression(c, 1, p, summary, expired.Add(-time.Minute))
	if !old.GateEligible {
		t.Fatalf("historically authorized baseline did not pass: %+v", old)
	}
	raw, _ := json.Marshal(old)
	if e = f.pool.QueryRow(ctx, `INSERT INTO plume.ai_security_evaluations(workspace_id,actor_id,configuration_revision,catalog_digest,status,result) VALUES($1,$2,1,$3,'passed',$4) RETURNING id`, f.ws.ID, f.user.ID, old.CatalogDigest, raw).Scan(&old.ID); e != nil {
		t.Fatal(e)
	}
	var reviewer int64
	if e = f.pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, fmt.Sprintf("regression-reviewer-%d", f.ws.ID)).Scan(&reviewer); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'reviewer')`, f.ws.ID, reviewer); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM plume.workspaces WHERE id=$1`, f.ws.ID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM plume.users WHERE id=$1`, reviewer)
	})
	if _, e = f.repo.PublishAISecurity(ctx, f.ws.ID, reviewer, 1, old.ID, "Recheck expired permissions"); !errors.Is(e, ErrEvidenceRequired) {
		t.Fatalf("expired cached gate published: %v", e)
	}
}
