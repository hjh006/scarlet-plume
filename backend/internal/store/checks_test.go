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

func TestPostgreSQLChecks(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_isolation"] = "repeatable read"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("could not configure test database")
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)

	want := func(t *testing.T, err, target error) {
		t.Helper()
		if !errors.Is(err, target) {
			t.Fatalf("got %v, want %v", err, target)
		}
	}
	want(t, repo.Migrate(ctx), nil)
	requirements, err := repo.ListRequirements(ctx)
	want(t, err, nil)
	if len(requirements) != 14 || requirements[2].Code != "G03" {
		t.Fatalf("expected 14 Django catalog entries, got %+v", requirements)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	slug, otherSlug := "checks-"+suffix, "other-checks-"+suffix
	usernames := []string{"owner-" + suffix, "other-" + suffix, "operator-" + suffix, "reviewer-" + suffix, "viewer-" + suffix}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{slug, otherSlug}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", usernames); err != nil {
			t.Error(err)
		}
	})
	want(t, repo.Bootstrap(ctx, usernames[0], "unused", slug, "Checks"), nil)
	want(t, repo.Bootstrap(ctx, usernames[1], "unused", otherSlug, "Other checks"), nil)
	owner, err := repo.FindUser(ctx, usernames[0])
	want(t, err, nil)
	other, err := repo.FindUser(ctx, usernames[1])
	want(t, err, nil)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	want(t, err, nil)
	foreign, err := repo.Membership(ctx, other.ID, otherSlug)
	want(t, err, nil)

	addMember := func(username, role string) int64 {
		t.Helper()
		var id int64
		want(t, pool.QueryRow(ctx, "INSERT INTO plume.users (username,password_hash) VALUES ($1,'unused') RETURNING id", username).Scan(&id), nil)
		_, err := pool.Exec(ctx, "INSERT INTO plume.memberships (workspace_id,user_id,role) VALUES ($1,$2,$3)", ws.ID, id, role)
		want(t, err, nil)
		return id
	}
	operator := addMember(usernames[2], "operator")
	reviewer := addMember(usernames[3], "reviewer")
	viewer := addMember(usernames[4], "viewer")

	asset, err := repo.CreateAsset(ctx, ws.ID, "Document", "Drive", "Security")
	want(t, err, nil)
	foreignAsset, err := repo.CreateAsset(ctx, foreign.ID, "Other", "Drive", "Security")
	want(t, err, nil)
	input := store.CheckInput{AssetID: asset.ID, RequirementCode: "G03", OwnerTeam: "Security", Note: "Check the scoped permission"}
	foreignInput := input
	foreignInput.AssetID = foreignAsset.ID
	_, err = repo.CreateCheck(ctx, ws.ID, owner.ID, foreignInput)
	want(t, err, store.ErrNotFound)
	_, err = repo.CreateCheck(ctx, ws.ID, viewer, input)
	want(t, err, store.ErrForbidden)

	manual := func(kind string, observed time.Time) store.EvidenceInput {
		return store.EvidenceInput{Kind: kind, SourceSystem: "Ticket", SourceRecord: "CASE-1", Summary: "Redacted evidence", ObservedAt: observed}
	}
	detail := func(t *testing.T, actor, checkID int64) store.CheckDetail {
		t.Helper()
		result, err := repo.CheckDetail(ctx, ws.ID, actor, checkID)
		want(t, err, nil)
		return result
	}

	t.Run("independent workflow and concurrent review", func(t *testing.T) {
		check, err := repo.CreateCheck(ctx, ws.ID, operator, input)
		want(t, err, nil)
		_, err = repo.CheckDetail(ctx, ws.ID, other.ID, check.ID)
		want(t, err, store.ErrNotFound)
		_, err = repo.CheckDetail(ctx, foreign.ID, other.ID, check.ID)
		want(t, err, store.ErrNotFound)
		if view := detail(t, viewer, check.ID); view.CanAddEvidence || view.CanReview || len(view.Events) != 1 {
			t.Fatalf("invalid viewer capabilities: %+v", view)
		}
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), store.ErrEvidenceRequired)
		want(t, repo.AddEvidence(ctx, ws.ID, viewer, check.ID, manual("observation", time.Now())), store.ErrForbidden)
		want(t, repo.AddEvidence(ctx, ws.ID, owner.ID, check.ID, manual("observation", time.Now().Add(time.Hour))), store.ErrInvalid)
		want(t, repo.AddEvidence(ctx, ws.ID, owner.ID, check.ID, manual("observation", time.Now())), nil)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), nil)
		review := detail(t, reviewer, check.ID)
		if !review.CanReview || !review.CanVerify || review.ReviewRevision < 1 || detail(t, owner.ID, check.ID).CanReview {
			t.Fatalf("invalid reviewer capabilities: %+v", review)
		}
		want(t, repo.ReviewCheck(ctx, ws.ID, owner.ID, check.ID, review.ReviewRevision, "verified", "Own evidence"), store.ErrForbidden)
		want(t, repo.ReviewCheck(ctx, ws.ID, reviewer, check.ID, review.ReviewRevision, "changes", "Record corrected permissions"), nil)
		changesAt := detail(t, operator, check.ID).Events[0].CreatedAt
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, manual("retest", changesAt.Add(-time.Second))), nil)
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, manual("remediation", changesAt.Add(time.Second))), nil)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), store.ErrEvidenceRequired)
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, manual("retest", changesAt.Add(2*time.Second))), nil)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), nil)
		want(t, repo.ReviewCheck(ctx, ws.ID, reviewer, check.ID, review.ReviewRevision, "verified", "Stale form"), store.ErrConflict)

		revision := detail(t, reviewer, check.ID).ReviewRevision
		outcomes := make(chan error, 2)
		for range 2 {
			go func() {
				outcomes <- repo.ReviewCheck(ctx, ws.ID, reviewer, check.ID, revision, "verified", "Retest accepted")
			}()
		}
		var approvals, conflicts int
		for range 2 {
			err := <-outcomes
			switch {
			case err == nil:
				approvals++
			case errors.Is(err, store.ErrConflict):
				conflicts++
			default:
				t.Fatal(err)
			}
		}
		final := detail(t, viewer, check.ID)
		if approvals != 1 || conflicts != 1 || final.Status != "verified" ||
			final.Events[0].Actor != usernames[3] || final.Events[0].Action != "verified" || len(final.Evidence) != 4 {
			t.Fatalf("unexpected audited result: %+v", final)
		}
	})

	t.Run("activity scoped sharing, cross-border, and AI checks", func(t *testing.T) {
		registered, err := repo.Asset(ctx, ws.ID, asset.ID)
		want(t, err, nil)
		if registered.Category != "unknown" || registered.BusinessLevel != "unknown" || registered.StatutoryCategory != "unknown" {
			t.Fatalf("unexpected default asset classification: %+v", registered)
		}
		_, err = repo.Asset(ctx, foreign.ID, asset.ID)
		want(t, err, store.ErrNotFound)

		activityInput := store.ActivityInput{AssetID: asset.ID, Name: "Activity", Kind: "internal", Purpose: "Service operation"}
		_, err = repo.CreateActivity(ctx, ws.ID, viewer, activityInput)
		want(t, err, store.ErrForbidden)
		foreignInput := activityInput
		foreignInput.AssetID = foreignAsset.ID
		_, err = repo.CreateActivity(ctx, ws.ID, owner.ID, foreignInput)
		want(t, err, store.ErrNotFound)
		missingDestination := activityInput
		missingDestination.Kind = "cross_border"
		_, err = repo.CreateActivity(ctx, ws.ID, operator, missingDestination)
		want(t, err, store.ErrInvalid)
		internal, err := repo.CreateActivity(ctx, ws.ID, operator, activityInput)
		want(t, err, nil)

		activityInput.Recipient = "Approved test supplier"
		activityInput.Region = "Test destination"
		activityInput.RetentionPolicy = "Retain only during testing"
		activityInput.TransferPath = "Pending business review"
		activityInput.Safeguards = "Scoped access"
		for _, scenario := range []struct{ kind, code string }{{"sharing", "T01"}, {"cross_border", "T03"}, {"ai", "X01"}} {
			t.Run(scenario.kind, func(t *testing.T) {
				activityInput.Kind = scenario.kind
				created, err := repo.CreateActivity(ctx, ws.ID, operator, activityInput)
				want(t, err, nil)
				fetched, err := repo.Activity(ctx, ws.ID, created.ID)
				want(t, err, nil)
				if fetched.ActivityInput != activityInput || fetched.AssetName != asset.Name {
					t.Fatalf("activity fields were not retained: %+v", fetched)
				}
				_, err = repo.Activity(ctx, foreign.ID, created.ID)
				want(t, err, store.ErrNotFound)
				_, err = repo.ListActivityChecks(ctx, foreign.ID, created.ID, "", 0, 10)
				want(t, err, store.ErrNotFound)

				otherAsset, err := repo.CreateAsset(ctx, ws.ID, "Mismatched "+scenario.kind, "Test", "")
				want(t, err, nil)
				linked := input
				linked.RequirementCode = scenario.code
				linked.ActivityID = &created.ID
				linked.AssetID = otherAsset.ID
				_, err = repo.CreateCheck(ctx, ws.ID, operator, linked)
				want(t, err, store.ErrNotFound)
				linked.AssetID = foreignAsset.ID
				_, err = repo.CreateCheck(ctx, foreign.ID, other.ID, linked)
				want(t, err, store.ErrNotFound)

				linked.AssetID = asset.ID
				check, err := repo.CreateCheck(ctx, ws.ID, operator, linked)
				want(t, err, nil)
				if check.Activity == nil || check.Activity.ID != created.ID || check.Activity.Kind != scenario.kind {
					t.Fatalf("missing check activity: %+v", check)
				}
				want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, manual("observation", time.Now())), nil)
				want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), nil)
				review := detail(t, reviewer, check.ID)
				want(t, repo.ReviewCheck(ctx, ws.ID, reviewer, check.ID, review.ReviewRevision, "changes", "Correct test control"), nil)
				changed := detail(t, operator, check.ID).Events[0].CreatedAt
				want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, manual("remediation", changed.Add(time.Second))), nil)
				want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, manual("retest", changed.Add(2*time.Second))), nil)
				want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), nil)
				review = detail(t, reviewer, check.ID)
				want(t, repo.ReviewCheck(ctx, ws.ID, reviewer, check.ID, review.ReviewRevision, "verified", "Retest accepted"), nil)
				checks, err := repo.ListActivityChecks(ctx, ws.ID, created.ID, "verified", 0, 10)
				want(t, err, nil)
				if len(checks) != 1 || checks[0].ID != check.ID {
					t.Fatalf("unexpected activity checks: %+v", checks)
				}
				checks, err = repo.ListActivityChecks(ctx, ws.ID, internal.ID, "", 0, 10)
				want(t, err, nil)
				if len(checks) != 0 {
					t.Fatalf("unexpected checks on unrelated activity: %+v", checks)
				}
			})
		}
		activities, err := repo.ListActivities(ctx, ws.ID, "cross_border", asset.ID, 0, 1)
		want(t, err, nil)
		if len(activities) != 1 || activities[0].Kind != "cross_border" {
			t.Fatalf("unexpected activity filter: %+v", activities)
		}
		activities, err = repo.ListActivities(ctx, ws.ID, "cross_border", asset.ID, activities[0].ID, 1)
		want(t, err, nil)
		if len(activities) != 0 {
			t.Fatalf("unexpected activity cursor results: %+v", activities)
		}
	})

	t.Run("Feishu scoped evidence and fresh absence", func(t *testing.T) {
		input := input
		target := strings.Repeat("a", 64)
		input.FeishuTargetDigest = target
		check, err := repo.CreateCheck(ctx, ws.ID, owner.ID, input)
		want(t, err, nil)
		evidence := func(digest string, present bool, observedAt time.Time) store.EvidenceInput {
			entry := manual("remediation", observedAt)
			entry.Feishu = &store.FeishuObservation{
				TargetDigest: target, SnapshotDigest: strings.Repeat(digest, 64), MemberPresent: present,
			}
			if present {
				entry.Feishu.Permission = "edit"
			}
			return entry
		}

		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, manual("observation", time.Now())), nil)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), store.ErrEvidenceRequired)
		initial := evidence("b", true, time.Now().Add(-time.Minute))
		wrong := initial
		wrongTarget := *initial.Feishu
		wrongTarget.TargetDigest = strings.Repeat("c", 64)
		wrong.Feishu = &wrongTarget
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, wrong), store.ErrInvalid)
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, initial), nil)
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, initial), store.ErrConflict)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), nil)

		review := detail(t, reviewer, check.ID)
		if !review.CanReview || review.CanVerify || review.Evidence[0].Kind != "observation" ||
			review.Evidence[0].Feishu.Permission != "edit" {
			t.Fatalf("positive grant should block verification: %+v", review)
		}
		want(t, repo.ReviewCheck(ctx, ws.ID, owner.ID, check.ID, review.ReviewRevision, "changes", "Own check"), store.ErrForbidden)
		want(t, repo.ReviewCheck(ctx, ws.ID, reviewer, check.ID, review.ReviewRevision, "verified", "Still present"), store.ErrEvidenceRequired)
		want(t, repo.ReviewCheck(ctx, ws.ID, reviewer, check.ID, review.ReviewRevision, "changes", "Remove the direct grant"), nil)
		changed := detail(t, operator, check.ID).Events[0].CreatedAt

		observedNow := func() time.Time {
			t.Helper()
			var now time.Time
			want(t, pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now), nil)
			return now
		}
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, evidence("c", false, changed)), store.ErrEvidenceRequired)

		// The absent ACL result alone cannot substitute for an explicit remediation record.
		preRemediation := observedNow()
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, evidence("d", false, preRemediation)), nil)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), store.ErrEvidenceRequired)
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, manual("remediation", changed.Add(-time.Second))), nil)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), store.ErrEvidenceRequired)

		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, manual("remediation", observedNow())), nil)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), store.ErrEvidenceRequired)
		// Uploading an old observation after the remediation still does not qualify.
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, evidence("e", false, preRemediation)), nil)
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, manual("retest", observedNow())), nil)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), store.ErrEvidenceRequired)

		earlier := observedNow()
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, evidence("f", true, observedNow())), nil)
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, evidence("1", false, earlier)), nil)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), store.ErrEvidenceRequired)
		want(t, repo.AddEvidence(ctx, ws.ID, operator, check.ID, evidence("2", false, observedNow())), nil)
		want(t, repo.SubmitCheck(ctx, ws.ID, operator, check.ID), nil)

		review = detail(t, reviewer, check.ID)
		if !review.CanVerify || review.Evidence[0].Kind != "retest" || review.Evidence[0].Feishu.MemberPresent {
			t.Fatalf("fresh same-target absence should allow review: %+v", review)
		}
		want(t, repo.ReviewCheck(ctx, ws.ID, reviewer, check.ID, review.ReviewRevision, "verified", "Exact direct grant absent"), nil)
		if final := detail(t, viewer, check.ID); final.Status != "verified" || final.CanAddEvidence || final.CanReview {
			t.Fatalf("unexpected closed check: %+v", final)
		}
	})
}
