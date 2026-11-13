package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAPIValidationApprovalLeaseProjectionAndFreshClear(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	want := func(err, target error) {
		t.Helper()
		if !errors.Is(err, target) {
			t.Fatalf("got %v want %v", err, target)
		}
	}
	must(repo.Migrate(ctx))
	// Use the database clock for causally ordered source observations. Evidence
	// created_at and workflow timestamps are also assigned by PostgreSQL.
	dbNow := func() time.Time {
		t.Helper()
		var at time.Time
		must(pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at))
		return at
	}

	slug := fmt.Sprintf("api-validation-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "API validation"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	must(repo.Bootstrap(ctx, slug+"-other", "unused", slug+"-other", "Other"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	reviewer, err := repo.FindUser(ctx, slug+"-reviewer")
	must(err)
	otherOwner, err := repo.FindUser(ctx, slug+"-other")
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	other, err := repo.Membership(ctx, otherOwner.ID, slug+"-other")
	must(err)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1)`, []int64{ws.ID, other.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE username=ANY($1)`, []string{slug, slug + "-reviewer", slug + "-other"})
	})
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(slug)))
	source, err := repo.CreateOperationsSource(ctx, ws.ID, owner.ID, OperationsSourceInput{Name: "Authorized probe", Kind: "security_events", OwnerTeam: "Governance"}, digest)
	must(err)
	in := APIValidationPlanInput{SourceID: source.ID, Name: "Synthetic object authorization", Service: "Validation app", Method: "GET", Route: "/objects/{id}", TargetOrigin: "http://127.0.0.1:8896", ManifestRef: "fixture-v1", Rules: []string{"bola"}, IntervalMinutes: 5}
	plan, err := repo.SaveAPIValidationPlan(ctx, ws.ID, owner.ID, 0, in)
	must(err)
	_, err = repo.ActAPIValidationPlan(ctx, ws.ID, owner.ID, plan.ID, plan.Revision, "approve", "")
	want(err, ErrForbidden)
	claim, err := repo.ClaimAPIValidation(ctx, digest, source.ID)
	must(err)
	if claim != nil {
		t.Fatal("unapproved plan leased")
	}
	plans, err := repo.ListAPIValidationPlans(ctx, ws.ID, reviewer.ID)
	must(err)
	if len(plans) != 1 || !plans[0].CanApprove {
		t.Fatal("independent reviewer capability missing")
	}
	_, err = repo.ListAPIValidationPlans(ctx, ws.ID, otherOwner.ID)
	want(err, ErrNotFound)
	_, err = repo.ActAPIValidationPlan(ctx, ws.ID, reviewer.ID, plan.ID, plan.Revision, "approve", "")
	must(err)
	_, err = repo.ClaimAPIValidation(ctx, digest, source.ID+99)
	want(err, ErrAutomationUnauthorized)
	claim, err = repo.ClaimAPIValidation(ctx, digest, source.ID)
	must(err)
	if claim == nil || claim.Plan.ID != plan.ID || claim.Mode != "validation" {
		t.Fatal("approved claim unavailable")
	}
	second, err := repo.ClaimAPIValidation(ctx, digest, source.ID)
	must(err)
	if second != nil {
		t.Fatal("active lease duplicated")
	}
	// A 200 alone cannot establish BOLA or a cleared condition.
	bad := APIValidationResult{PlanID: plan.ID, Revision: plan.Revision, RunRef: claim.RunRef, Mode: "validation", ObservedAt: dbNow(), Results: []APIValidationRuleResult{{Rule: "bola", Outcome: "vulnerable", OwnerStatus: 200, OtherStatus: 200, Requests: 3}}}
	_, _, err = repo.SubmitAPIValidationResult(ctx, digest, bad)
	want(err, ErrInvalid)
	bad.Results[0].Outcome = "inconclusive"
	bad.Results[0].Gaps = []string{"owner_unconfirmed"}
	unknown, _, err := repo.SubmitAPIValidationResult(ctx, digest, bad)
	must(err)
	if unknown.ProjectionState != "complete" || len(unknown.EventIDs) != 0 {
		t.Fatal("inconclusive result projected risk or clear")
	}
	request := func(mode string) *APIValidationClaim {
		t.Helper()
		_, e := repo.ActAPIValidationPlan(ctx, ws.ID, owner.ID, plan.ID, plan.Revision, "request", mode)
		must(e)
		c, e := repo.ClaimAPIValidation(ctx, digest, source.ID)
		must(e)
		if c == nil {
			t.Fatal("requested plan not claimed")
		}
		return c
	}
	claim = request("validation")
	positive := APIValidationResult{PlanID: plan.ID, Revision: plan.Revision, RunRef: claim.RunRef, Mode: "validation", ObservedAt: dbNow(), Results: []APIValidationRuleResult{{Rule: "bola", Outcome: "vulnerable", OwnerStatus: 200, OtherOwnedStatus: 200, OtherStatus: 200, OwnerConfirmed: true, OtherAccountConfirmed: true, SameObject: true, ComparisonComplete: true, Requests: 3, Gaps: []string{}}}}
	found, replayed, err := repo.SubmitAPIValidationResult(ctx, digest, positive)
	must(err)
	if replayed || len(found.CheckIDs) != 1 || found.ProjectionState != "complete" {
		t.Fatal("confirmed source result did not produce one native check")
	}
	checkID := found.CheckIDs[0]
	_, replayed, err = repo.SubmitAPIValidationResult(ctx, digest, positive)
	must(err)
	if !replayed {
		t.Fatal("identical receipt not replayed")
	}
	mutated := positive
	mutated.ObservedAt = positive.ObservedAt.Add(time.Microsecond)
	_, _, err = repo.SubmitAPIValidationResult(ctx, digest, mutated)
	want(err, ErrConflict)
	must(repo.AddEvidence(ctx, ws.ID, owner.ID, checkID, EvidenceInput{Kind: "remediation", SourceSystem: "Synthetic fix", SourceRecord: "fixture:fixed", Summary: "Object authorization now validates synthetic ownership", ObservedAt: dbNow()}))
	claim = request("connection")
	connection := positive
	connection.RunRef = claim.RunRef
	connection.Mode = "connection"
	connection.ObservedAt = dbNow()
	connection.Results = []APIValidationRuleResult{{Rule: "bola", Outcome: "inconclusive", OwnerStatus: 200, OtherOwnedStatus: 200, OwnerConfirmed: true, OtherAccountConfirmed: true, Requests: 2, Gaps: []string{"connection_only"}}}
	run, _, err := repo.SubmitAPIValidationResult(ctx, digest, connection)
	must(err)
	if len(run.EventIDs) != 0 {
		t.Fatal("connection-only run cleared risk")
	}
	claim = request("validation")
	negative := positive
	negative.RunRef = claim.RunRef
	negative.ObservedAt = dbNow()
	negative.Results = []APIValidationRuleResult{{Rule: "bola", Outcome: "not_observed", OwnerStatus: 200, OtherOwnedStatus: 200, OtherStatus: 403, OwnerConfirmed: true, OtherAccountConfirmed: true, ComparisonComplete: true, Requests: 3, Gaps: []string{}}}
	cleared, _, err := repo.SubmitAPIValidationResult(ctx, digest, negative)
	must(err)
	if len(cleared.CheckIDs) != 1 || cleared.CheckIDs[0] != checkID {
		t.Fatal("fresh same-plan negative did not retest original check")
	}
	detail, err := repo.CheckDetail(ctx, ws.ID, owner.ID, checkID)
	must(err)
	if detail.Status == "verified" {
		t.Fatal("source result automatically passed independent review")
	}
	must(repo.SubmitCheck(ctx, ws.ID, owner.ID, checkID))
	detail, err = repo.CheckDetail(ctx, ws.ID, reviewer.ID, checkID)
	must(err)
	if !detail.CanVerify {
		t.Fatalf("native clear evidence unavailable to independent reviewer: %s", checkFixtureTimeline(ctx, pool, checkID))
	}
	must(repo.ReviewCheck(ctx, ws.ID, reviewer.ID, checkID, detail.ReviewRevision, "verified", "Reviewed bounded live probe and remediation"))
	// Changing target scope resets approval and invalidates outstanding work.
	claim = request("validation")
	in.Revision = plan.Revision
	in.Route = "/other/{id}"
	changed, err := repo.SaveAPIValidationPlan(ctx, ws.ID, owner.ID, plan.ID, in)
	must(err)
	if changed.State != "draft" || changed.Revision != plan.Revision+1 {
		t.Fatal("scope change retained approval")
	}
	old := positive
	old.RunRef = claim.RunRef
	old.ObservedAt = dbNow()
	_, _, err = repo.SubmitAPIValidationResult(ctx, digest, old)
	want(err, ErrConflict)
	_, err = pool.Exec(ctx, `UPDATE plume.memberships SET role='operator' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, owner.ID)
	must(err)
	_, err = repo.ClaimAPIValidation(ctx, digest, source.ID)
	want(err, ErrAutomationUnauthorized)
}
