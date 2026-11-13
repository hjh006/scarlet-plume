package store

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestScenarioAPIValidationUsesRealApprovedComparisonAndPreservesScope(t *testing.T) {
	sessionTestDigest := func(v string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(v))) }
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	name := f.ws.Slug + "-reviewer"
	must(f.repo.ProvisionReviewer(ctx, f.ws.Slug, name, "unused"))
	reviewer, e := f.repo.FindUser(ctx, name)
	must(e)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM plume.workspaces WHERE id=$1`, f.ws.ID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM plume.users WHERE id=$1`, reviewer.ID)
	})
	source, e := f.repo.CreateOperationsSource(ctx, f.ws.ID, f.user.ID, OperationsSourceInput{Name: "authorized validation", Kind: "security_events", OwnerTeam: "test"}, sessionTestDigest(f.ws.Slug))
	must(e)
	plan, e := f.repo.SaveAPIValidationPlan(ctx, f.ws.ID, f.user.ID, 0, APIValidationPlanInput{SourceID: source.ID, Name: "controlled fixture", Service: "comparison-app", Method: "GET", Route: "/objects/{id}", TargetOrigin: "http://127.0.0.1:8896", ManifestRef: "fixture", Rules: []string{"bola"}, IntervalMinutes: 5})
	must(e)
	_, e = f.repo.ActAPIValidationPlan(ctx, f.ws.ID, reviewer.ID, plan.ID, plan.Revision, "approve", "")
	must(e)
	execute := func(negative bool) {
		t.Helper()
		_, e = f.repo.ActAPIValidationPlan(ctx, f.ws.ID, f.user.ID, plan.ID, plan.Revision, "request", "validation")
		must(e)
		claim, e := f.repo.ClaimAPIValidation(ctx, sessionTestDigest(f.ws.Slug), source.ID)
		must(e)
		if claim == nil {
			t.Fatal("missing claim")
		}
		result := APIValidationRuleResult{Rule: "bola", Outcome: "vulnerable", OwnerStatus: 200, OtherOwnedStatus: 200, OtherStatus: 200, OwnerConfirmed: true, OtherAccountConfirmed: true, SameObject: true, ComparisonComplete: true, Requests: 3, Gaps: []string{}}
		if negative {
			result.Outcome = "not_observed"
			result.OtherStatus = 403
			result.SameObject = false
		}
		_, _, e = f.repo.SubmitAPIValidationResult(ctx, sessionTestDigest(f.ws.Slug), APIValidationResult{PlanID: plan.ID, Revision: plan.Revision, RunRef: claim.RunRef, Mode: "validation", ObservedAt: time.Now(), Results: []APIValidationRuleResult{result}})
		must(e)
	}
	run := func() ScenarioCheckRun {
		t.Helper()
		now := time.Now()
		scope := RuntimeEvidenceScope{Service: "comparison-app", Method: "GET", Route: "/objects/{id}", From: now.Add(-time.Hour), To: now}
		r, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, ScenarioCheckRunInput{Scenario: "personal", Scope: &scope})
		must(e)
		return r
	}
	finding := func(r ScenarioCheckRun) ScenarioCheckFinding {
		t.Helper()
		for _, v := range r.Findings {
			if strings.HasPrefix(v.ID, "api_validation_") {
				return v
			}
		}
		t.Fatal("approved API result not adapted without runtime audit")
		return ScenarioCheckFinding{}
	}
	execute(false)
	positive := finding(run())
	if positive.Outcome != "non_compliant" || positive.Remediation.Executable || !strings.Contains(positive.Remediation.Reason, "可信绑定") || len(positive.EvidenceRefs) != 1 {
		t.Fatal(positive)
	}
	if positive.CheckID == nil {
		t.Fatal("missing real native check")
	}
	must(f.repo.AddEvidence(ctx, f.ws.ID, f.user.ID, *positive.CheckID, EvidenceInput{Kind: "remediation", SourceSystem: "synthetic fixture", SourceRecord: "fixture:fixed", Summary: "synthetic authorization check corrected", ObservedAt: time.Now()}))
	execute(true)
	negative := finding(run())
	if negative.Outcome != "compliant" || negative.Remediation.Reason == "" {
		t.Fatal(negative)
	}
	_, e = f.pool.Exec(ctx, `UPDATE plume.memberships SET role='operator' WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, reviewer.ID)
	must(e)
	if finding(run()).Outcome != "needs_verification" {
		t.Fatal("demoted approver left trusted comparison")
	}
}
