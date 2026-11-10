package store_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgreSQLAutomation(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure automation test database")
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	want := func(err, expected error) {
		t.Helper()
		if !errors.Is(err, expected) {
			t.Fatalf("got %v, want %v", err, expected)
		}
	}
	want(repo.Migrate(ctx), nil)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	slug, otherSlug := "automated-"+suffix, "other-automated-"+suffix
	ownerName, otherName, reviewerName := "automation-owner-"+suffix, "automation-other-"+suffix, "automation-review-"+suffix
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{slug, otherSlug}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", []string{ownerName, otherName, reviewerName}); err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "Automation"), nil)
	want(repo.Bootstrap(ctx, otherName, "unused", otherSlug, "Other automation"), nil)
	want(repo.ProvisionReviewer(ctx, slug, reviewerName, "unused"), nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	other, err := repo.FindUser(ctx, otherName)
	want(err, nil)
	reviewer, err := repo.FindUser(ctx, reviewerName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)
	foreign, err := repo.Membership(ctx, other.ID, otherSlug)
	want(err, nil)

	now := func() time.Time {
		t.Helper()
		var at time.Time
		want(pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&at), nil)
		return at
	}
	digest := func(ch string) string { return strings.Repeat(ch, 64) }
	assetToken := digest("a")
	assetInput := store.AutomationJobInput{Provider: "postgresql", Source: "Catalog"}
	_, err = repo.CreateAutomationJob(ctx, workspace.ID, reviewer.ID, assetInput, assetToken)
	want(err, store.ErrForbidden)
	job, err := repo.CreateAutomationJob(ctx, workspace.ID, owner.ID, assetInput, assetToken)
	want(err, nil)
	_, err = repo.CreateAutomationJob(ctx, workspace.ID, owner.ID, assetInput, digest("b"))
	want(err, store.ErrConflict)
	want(repo.RevokeAutomationJob(ctx, foreign.ID, other.ID, job.ID), store.ErrNotFound)
	_, err = repo.ListAutomationRuns(ctx, foreign.ID, other.ID, job.ID)
	want(err, store.ErrNotFound)
	assetRun := store.AutomationDelivery{RunID: "test-1", Digest: digest("0"), Provider: "postgresql", Source: "Catalog",
		ObservedAt: now(), Assets: []store.CollectedAsset{{SourceRef: "catalog.invoice", Name: "Invoice"}}}
	first, err := repo.DeliverAutomationRun(ctx, assetToken, assetRun)
	want(err, nil)
	if first.Status != "success" || first.CreatedCount != 1 || first.ExistingCount != 0 || first.ImportID == nil {
		t.Fatalf("unexpected import outcome: %+v", first)
	}
	replay, err := repo.DeliverAutomationRun(ctx, assetToken, assetRun)
	want(err, nil)
	if !replay.Replayed || replay.ID != first.ID {
		t.Fatalf("unexpected replay: %+v", replay)
	}
	assetRun.Digest = digest("1")
	_, err = repo.DeliverAutomationRun(ctx, assetToken, assetRun)
	want(err, store.ErrConflict)
	assetRun.RunID, assetRun.Provider = "test-2", "openmetadata"
	_, err = repo.DeliverAutomationRun(ctx, assetToken, assetRun)
	want(err, store.ErrInvalid)
	assetRun.Provider = "postgresql"
	assetRun.Source = "Wrong catalog"
	_, err = repo.DeliverAutomationRun(ctx, assetToken, assetRun)
	want(err, store.ErrInvalid)
	assetRun.Source = "Catalog"
	second, err := repo.DeliverAutomationRun(ctx, assetToken, assetRun)
	want(err, nil)
	if second.CreatedCount != 0 || second.ExistingCount != 1 {
		t.Fatalf("duplicate asset imported: %+v", second)
	}
	failure := store.AutomationDelivery{RunID: "timeout", Provider: "postgresql", ObservedAt: now(),
		Digest: digest("2"), FailureCode: "collection_timeout"}
	failed, err := repo.DeliverAutomationRun(ctx, assetToken, failure)
	want(err, nil)
	if failed.Status != "error" || failed.Code != "collection_timeout" {
		t.Fatalf("failure missing from run history: %+v", failed)
	}
	jobs, err := repo.ListAutomationJobs(ctx, workspace.ID, reviewer.ID)
	want(err, nil)
	if len(jobs) != 1 || jobs[0].LatestRun == nil || jobs[0].LatestRun.ID != failed.ID {
		t.Fatalf("latest result missing: %+v", jobs)
	}
	want(repo.RevokeAutomationJob(ctx, workspace.ID, owner.ID, job.ID), nil)
	_, err = repo.DeliverAutomationRun(ctx, assetToken, assetRun)
	want(err, store.ErrAutomationUnauthorized)
	_, err = repo.CreateAutomationJob(ctx, workspace.ID, owner.ID, assetInput, digest("b"))
	want(err, nil)

	asset, err := repo.CreateAsset(ctx, workspace.ID, "Feishu doc", "Collaboration", "Security")
	want(err, nil)
	target := digest("c")
	check, err := repo.CreateCheck(ctx, workspace.ID, owner.ID, store.CheckInput{AssetID: asset.ID,
		RequirementCode: "G03", OwnerTeam: "Security", Note: "Direct external permission", FeishuTargetDigest: target})
	want(err, nil)
	checkJob := store.AutomationJobInput{Provider: "feishu", CheckID: &check.ID}
	_, err = repo.CreateAutomationJob(ctx, foreign.ID, other.ID, checkJob, digest("d"))
	want(err, store.ErrNotFound)
	feishuToken := digest("d")
	job, err = repo.CreateAutomationJob(ctx, workspace.ID, owner.ID, checkJob, feishuToken)
	want(err, nil)
	feishuDelivery := func(key string, at time.Time, present bool) store.AutomationDelivery {
		permission := ""
		if present {
			permission = "view"
		}
		snapshot := digest(key)
		return store.AutomationDelivery{RunID: key, Provider: "feishu",
			ObservedAt: at, Digest: snapshot, Evidence: &store.EvidenceInput{ObservedAt: at,
				SourceSystem: "Feishu", SourceRecord: snapshot, Summary: "Observed ACL",
				Feishu: &store.FeishuObservation{TargetDigest: target, SnapshotDigest: snapshot,
					MemberPresent: present, Permission: permission}}}
	}
	deliver := func(key string, at time.Time, present bool) store.AutomationRun {
		t.Helper()
		run, err := repo.DeliverAutomationRun(ctx, feishuToken, feishuDelivery(key, at, present))
		want(err, nil)
		return run
	}
	initial := deliver("3", now(), true)
	if initial.EvidenceID == nil || initial.Status != "success" {
		t.Fatalf("missing automatic observation: %+v", initial)
	}
	same := deliver("4", now(), true)
	if same.Code != "unchanged" || same.EvidenceID != nil {
		t.Fatalf("did not suppress unchanged evidence: %+v", same)
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, check.ID), nil)
	detail, err := repo.CheckDetail(ctx, workspace.ID, reviewer.ID, check.ID)
	want(err, nil)
	if !detail.CanReview || detail.CanVerify || len(detail.Evidence) != 1 ||
		detail.Evidence[0].AutomationJobID == nil || *detail.Evidence[0].AutomationJobID != job.ID ||
		detail.Evidence[0].AutomationRunID != initial.RunID {
		t.Fatalf("automated source or review state missing: %+v", detail)
	}
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, check.ID, detail.ReviewRevision, "changes", "Remove direct access"), nil)
	early := deliver("5", now(), false)
	if early.Code != "awaiting_remediation" || early.EvidenceID != nil {
		t.Fatalf("accepted premature retest: %+v", early)
	}
	want(repo.AddEvidence(ctx, workspace.ID, owner.ID, check.ID, store.EvidenceInput{Kind: "remediation",
		SourceSystem: "Change requests", SourceRecord: "ACL-123", Summary: "Remove direct share", ObservedAt: now()}), nil)
	ret := deliver("6", now(), false)
	if ret.EvidenceID == nil || ret.Status != "success" {
		t.Fatalf("missing automatic retest: %+v", ret)
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, check.ID), nil)
	closed := deliver("7", now(), true)
	if closed.Code != "check_closed" || closed.MemberPresent == nil || !*closed.MemberPresent {
		t.Fatalf("sealed report lost new access observation: %+v", closed)
	}
	stale := deliver("8", ret.ObservedAt.Add(time.Microsecond), false)
	if stale.Code != "stale_observation" {
		t.Fatalf("late delivery of older observation was accepted: %+v", stale)
	}
	detail, err = repo.CheckDetail(ctx, workspace.ID, reviewer.ID, check.ID)
	want(err, nil)
	if detail.CanVerify || !detail.CanReview {
		t.Fatalf("newer positive observation must block verification: %+v", detail)
	}
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, check.ID, detail.ReviewRevision, "verified", "Stale proof"), store.ErrEvidenceRequired)
	if latest := deliver("9", now(), false); latest.Code != "check_closed" {
		t.Fatalf("unexpected re-observation status: %+v", latest)
	}
	// The check lock serializes an approval with a simultaneous new grant.
	type deliveryResult struct {
		run store.AutomationRun
		err error
	}
	start := make(chan struct{})
	delivered := make(chan deliveryResult, 1)
	reviewed := make(chan error, 1)
	reappeared := feishuDelivery("e", now(), true)
	go func() {
		<-start
		run, err := repo.DeliverAutomationRun(ctx, feishuToken, reappeared)
		delivered <- deliveryResult{run, err}
	}()
	go func() {
		<-start
		reviewed <- repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, check.ID, detail.ReviewRevision, "verified", "Access removed")
	}()
	close(start)
	concurrent, reviewErr := <-delivered, <-reviewed
	want(concurrent.err, nil)
	if reviewErr == nil {
		current, err := repo.CheckDetail(ctx, workspace.ID, reviewer.ID, check.ID)
		want(err, nil)
		if current.Events[0].Action != "verified" || concurrent.run.CreatedAt.Before(current.Events[0].CreatedAt) {
			t.Fatal("approved after an automated grant was already recorded")
		}
	} else {
		want(reviewErr, store.ErrEvidenceRequired)
		deliver("f", now(), false)
		want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, check.ID, detail.ReviewRevision, "verified", "Access removed"), nil)
	}
	report, err := repo.ComplianceReport(ctx, workspace.ID, reviewer.ID, 0)
	want(err, nil)
	if len(report.Checks) != 1 || len(report.Checks[0].Evidence) != 3 ||
		report.Checks[0].Evidence[0].AutomationRunID != ret.RunID {
		t.Fatalf("automated retest provenance absent from report: %+v", report)
	}
}
