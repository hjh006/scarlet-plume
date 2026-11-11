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

type operationsFixture struct {
	ctx                                           context.Context
	pool                                          *pgxpool.Pool
	repo                                          *Store
	workspace                                     Workspace
	owner, reviewer, contributor, viewer, foreign User
	slug                                          string
}

func newOperationsFixture(t *testing.T) *operationsFixture {
	t.Helper()
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := &operationsFixture{ctx: ctx, pool: pool, repo: New(pool), slug: fmt.Sprintf("operations-%d", time.Now().UnixNano())}
	f.must(t, f.repo.Migrate(ctx))
	names := []string{f.slug + "-owner", f.slug + "-reviewer", f.slug + "-contributor", f.slug + "-viewer", f.slug + "-foreign"}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, []string{f.slug, f.slug + "-foreign"})
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, `DELETE FROM plume.users WHERE username=ANY($1)`, names)
		if err != nil {
			t.Error(err)
		}
	})
	f.must(t, f.repo.Bootstrap(ctx, names[0], "unused", f.slug, "Operations tests"))
	for _, name := range names[1:4] {
		f.must(t, f.repo.ProvisionReviewer(ctx, f.slug, name, "unused"))
	}
	f.must(t, f.repo.Bootstrap(ctx, names[4], "unused", f.slug+"-foreign", "Foreign operations"))
	users := []*User{&f.owner, &f.reviewer, &f.contributor, &f.viewer, &f.foreign}
	for i, user := range users {
		*user, err = f.repo.FindUser(ctx, names[i])
		f.must(t, err)
	}
	f.workspace, err = f.repo.Membership(ctx, f.owner.ID, f.slug)
	f.must(t, err)
	_, err = pool.Exec(ctx, `UPDATE plume.memberships SET role=CASE WHEN user_id=$2 THEN 'owner' ELSE 'viewer' END WHERE workspace_id=$1 AND user_id=ANY($3)`, f.workspace.ID, f.contributor.ID, []int64{f.contributor.ID, f.viewer.ID})
	f.must(t, err)
	return f
}
func (f *operationsFixture) must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (f *operationsFixture) want(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got %v want %v", err, target)
	}
}
func operationsTestDigest(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

func TestOperationsSecurityEventsScopeReplayAndFreshClear(t *testing.T) {
	f := newOperationsFixture(t)
	ctx, repo, ws := f.ctx, f.repo, f.workspace
	token := operationsTestDigest(f.slug + "-events")
	source, err := repo.CreateOperationsSource(ctx, ws.ID, f.owner.ID, OperationsSourceInput{Name: "Authorized SIEM", Kind: "security_events", OwnerTeam: "Security"}, token)
	f.must(t, err)
	_, err = repo.CreateOperationsSource(ctx, ws.ID, f.viewer.ID, OperationsSourceInput{Name: "Viewer source", Kind: "security_events", OwnerTeam: "Security"}, operationsTestDigest("viewer"))
	f.want(t, err, ErrForbidden)
	_, err = repo.ListOperationsSources(ctx, ws.ID, f.foreign.ID)
	f.want(t, err, ErrNotFound)
	otherToken := operationsTestDigest(f.slug + "-other")
	_, err = repo.CreateOperationsSource(ctx, ws.ID, f.owner.ID, OperationsSourceInput{Name: "Other SIEM", Kind: "security_events", OwnerTeam: "Security"}, otherToken)
	f.must(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	input := SecurityEventInput{Outcome: "observed", EventID: "fixture-1", ObservedAt: now, Category: "data_leak", Rule: "fixture.response", Severity: "high", Count: 2, WindowSeconds: 60}
	event, replayed, err := repo.ReceiveSecurityEvent(ctx, token, input)
	f.must(t, err)
	if replayed || event.CheckID == 0 || event.AssetID == 0 || event.CheckStatus != "draft" {
		t.Fatalf("event lacks derived asset/check: %+v", event)
	}
	replay, replayed, err := repo.ReceiveSecurityEvent(ctx, token, input)
	f.must(t, err)
	if !replayed || replay.ID != event.ID || replay.AssetID != event.AssetID || replay.CheckStatus != "draft" {
		t.Fatalf("replay lost resolved projection: %+v replayed=%t", replay, replayed)
	}
	changed := input
	changed.Count = 3
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, changed)
	f.want(t, err, ErrConflict)
	scoped := input
	scoped.EventID = "route-alpha-1"
	scoped.Service = "Scoped API"
	scoped.Method = "GET"
	scoped.Route = "/alpha/{customer_id}"
	scopedEvent, _, err := repo.ReceiveSecurityEvent(ctx, token, scoped)
	f.must(t, err)
	if scopedEvent.CheckID == event.CheckID {
		t.Fatal("different route scope merged into source-wide cycle")
	}
	scoped.EventID = "route-alpha-2"
	scoped.Route = "/alpha/{id}"
	canonical, _, err := repo.ReceiveSecurityEvent(ctx, token, scoped)
	f.must(t, err)
	if canonical.CheckID != scopedEvent.CheckID {
		t.Fatal("equivalent canonical route opened duplicate source cycle")
	}
	wrongRoute := scoped
	wrongRoute.EventID = "wrong-route-clear"
	wrongRoute.Route = "/beta/{id}"
	wrongRoute.Outcome = "cleared"
	wrongRoute.Count = 0
	wrongRoute.ObservedAt = time.Now().UTC().Add(time.Millisecond)
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, wrongRoute)
	f.want(t, err, ErrEvidenceRequired)
	detail, err := repo.CheckDetail(ctx, ws.ID, f.owner.ID, event.CheckID)
	f.must(t, err)
	if len(detail.Evidence) != 1 || detail.Evidence[0].Kind != "observation" || detail.Evidence[0].SourceSystem != "安全事件集成" {
		t.Fatal("initial source evidence missing or replay duplicated")
	}
	wrongAsset := input
	wrongAsset.EventID = "foreign-asset"
	var foreignAsset int64
	foreignWS, err := repo.Membership(ctx, f.foreign.ID, f.slug+"-foreign")
	f.must(t, err)
	f.must(t, f.pool.QueryRow(ctx, `INSERT INTO plume.assets(workspace_id,name,system) VALUES($1,'Foreign asset','fixture') RETURNING id`, foreignWS.ID).Scan(&foreignAsset))
	wrongAsset.AssetID = foreignAsset
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, wrongAsset)
	f.want(t, err, ErrNotFound)
	f.must(t, repo.SubmitCheck(ctx, ws.ID, f.owner.ID, event.CheckID))
	detail, err = repo.CheckDetail(ctx, ws.ID, f.reviewer.ID, event.CheckID)
	f.must(t, err)
	f.want(t, repo.ReviewCheck(ctx, ws.ID, f.reviewer.ID, event.CheckID, detail.ReviewRevision, "verified", "No source retest"), ErrEvidenceRequired)
	f.must(t, repo.ReviewCheck(ctx, ws.ID, f.reviewer.ID, event.CheckID, detail.ReviewRevision, "changes", "Correct source findings"))
	f.must(t, repo.AddEvidence(ctx, ws.ID, f.owner.ID, event.CheckID, EvidenceInput{Kind: "remediation", SourceSystem: "fixture", SourceRecord: "release-1", Summary: "Corrected configured condition", ObservedAt: time.Now().UTC()}))
	manualErr := repo.AddEvidence(ctx, ws.ID, f.owner.ID, event.CheckID, EvidenceInput{Kind: "retest", SourceSystem: "fixture", SourceRecord: "manual-substitute", Summary: "Cannot replace authorized source clear", ObservedAt: time.Now().UTC()})
	if manualErr != nil {
		f.want(t, manualErr, ErrEvidenceRequired)
	}
	f.want(t, repo.SubmitCheck(ctx, ws.ID, f.owner.ID, event.CheckID), ErrEvidenceRequired)
	clear := input
	clear.EventID = "clear-1"
	clear.Outcome = "cleared"
	clear.Count = 0
	clear.ObservedAt = time.Now().UTC().Add(time.Millisecond)
	_, _, err = repo.ReceiveSecurityEvent(ctx, otherToken, clear)
	f.want(t, err, ErrEvidenceRequired)
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, clear)
	f.must(t, err)
	f.must(t, repo.SubmitCheck(ctx, ws.ID, f.owner.ID, event.CheckID))
	detail, err = repo.CheckDetail(ctx, ws.ID, f.reviewer.ID, event.CheckID)
	f.must(t, err)
	if !detail.CanVerify {
		t.Fatal("fresh same-source clear did not qualify for independent review")
	}
	submittedRevision := detail.ReviewRevision
	heartbeat := clear
	heartbeat.EventID = "healthy-after-submission"
	heartbeat.ObservedAt = clear.ObservedAt.Add(time.Microsecond)
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, heartbeat)
	f.must(t, err)
	detail, err = repo.CheckDetail(ctx, ws.ID, f.reviewer.ID, event.CheckID)
	f.must(t, err)
	if !detail.CanVerify || detail.ReviewRevision != submittedRevision {
		t.Fatal("healthy heartbeat invalidated a qualified submitted snapshot")
	}
	positive := input
	positive.EventID = "fresh-positive"
	positive.ObservedAt = clear.ObservedAt.Add(time.Millisecond)
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, positive)
	f.must(t, err)
	detail, err = repo.CheckDetail(ctx, ws.ID, f.reviewer.ID, event.CheckID)
	f.must(t, err)
	if detail.CanVerify {
		t.Fatal("new positive did not invalidate pending review")
	}
	if detail.Status != "needs_work" {
		t.Fatal("new positive did not invalidate submitted workflow")
	}
	f.want(t, repo.ReviewCheck(ctx, ws.ID, f.reviewer.ID, event.CheckID, submittedRevision, "verified", "Superseded source clear"), ErrConflict)
	late := input
	late.EventID = "late-older-positive"
	late.ObservedAt = now.Add(-time.Minute)
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, late)
	f.must(t, err)
	staleClear := clear
	staleClear.EventID = "clear-before-latest-positive"
	staleClear.ObservedAt = positive.ObservedAt.Add(-time.Microsecond)
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, staleClear)
	f.want(t, err, ErrEvidenceRequired)
	f.must(t, repo.AddEvidence(ctx, ws.ID, f.owner.ID, event.CheckID, EvidenceInput{Kind: "remediation", SourceSystem: "fixture", SourceRecord: "release-2", Summary: "Corrected recurrence before independent review", ObservedAt: time.Now().UTC()}))
	clear.EventID = "clear-current-cycle"
	clear.ObservedAt = time.Now().UTC().Add(time.Millisecond)
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, clear)
	f.must(t, err)
	f.must(t, repo.SubmitCheck(ctx, ws.ID, f.owner.ID, event.CheckID))
	detail, err = repo.CheckDetail(ctx, ws.ID, f.reviewer.ID, event.CheckID)
	f.must(t, err)
	if !detail.CanVerify {
		t.Fatal("new same-source clear did not restore review")
	}
	f.must(t, repo.ReviewCheck(ctx, ws.ID, f.reviewer.ID, event.CheckID, detail.ReviewRevision, "changes", "Require a new remediation scope"))
	f.must(t, repo.AddEvidence(ctx, ws.ID, f.owner.ID, event.CheckID, EvidenceInput{Kind: "remediation", SourceSystem: "fixture", SourceRecord: "release-3", Summary: "New remediation scope after earlier clear", ObservedAt: time.Now().UTC()}))
	manualErr = repo.AddEvidence(ctx, ws.ID, f.owner.ID, event.CheckID, EvidenceInput{Kind: "retest", SourceSystem: "fixture", SourceRecord: "old-clear-substitute", Summary: "Old source clear cannot substitute current round", ObservedAt: time.Now().UTC()})
	if manualErr != nil {
		f.want(t, manualErr, ErrEvidenceRequired)
	}
	f.want(t, repo.SubmitCheck(ctx, ws.ID, f.owner.ID, event.CheckID), ErrEvidenceRequired)
	clear.EventID = "clear-new-remediation-scope"
	// Intake permits bounded source clock skew. A late positive newer than the
	// server review time but older than the adopted source clear is not recurrence.
	clear.ObservedAt = time.Now().UTC().Add(time.Minute)
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, clear)
	f.must(t, err)
	f.must(t, repo.SubmitCheck(ctx, ws.ID, f.owner.ID, event.CheckID))
	detail, err = repo.CheckDetail(ctx, ws.ID, f.reviewer.ID, event.CheckID)
	f.must(t, err)
	f.must(t, repo.ReviewCheck(ctx, ws.ID, f.reviewer.ID, event.CheckID, detail.ReviewRevision, "verified", "Independent source review"))
	late.EventID = "late-after-review"
	lateEvent, _, err := repo.ReceiveSecurityEvent(ctx, token, late)
	f.must(t, err)
	if lateEvent.CheckID != event.CheckID || lateEvent.CheckStatus != "verified" {
		t.Fatal("late old signal reopened verified history")
	}
	clockSkew := input
	clockSkew.EventID = "newer-than-review-older-than-clear"
	clockSkew.ObservedAt = time.Now().UTC().Add(time.Second)
	clockEvent, _, err := repo.ReceiveSecurityEvent(ctx, token, clockSkew)
	f.must(t, err)
	if clockEvent.CheckID != event.CheckID || clockEvent.CheckStatus != "verified" {
		t.Fatal("source clock skew reopened history before the adopted clear")
	}
	positive.EventID = "verified-recurrence"
	positive.ObservedAt = clear.ObservedAt.Add(time.Second)
	recurrence, _, err := repo.ReceiveSecurityEvent(ctx, token, positive)
	f.must(t, err)
	if recurrence.CheckID == event.CheckID || recurrence.CheckStatus != "draft" {
		t.Fatal("new positive after verification did not create new cycle")
	}
	overview, err := repo.OperationsOverview(ctx, ws.ID, f.viewer.ID)
	f.must(t, err)
	if overview.Counts.Sources != 2 || len(overview.Trend) != 30 {
		t.Fatalf("overview not derived from current workspace sources: %+v", overview.Counts)
	}
	f.must(t, repo.RevokeOperationsSource(ctx, ws.ID, f.owner.ID, source.ID))
	overview, err = repo.OperationsOverview(ctx, ws.ID, f.viewer.ID)
	f.must(t, err)
	if overview.Counts.Sources != 1 {
		t.Fatal("source revocation did not update current overview counts")
	}
	input.EventID = "after-revoke"
	_, _, err = repo.ReceiveSecurityEvent(ctx, token, input)
	f.want(t, err, ErrAutomationUnauthorized)
	items, err := repo.ListOperationsSecurityEvents(ctx, ws.ID, f.viewer.ID)
	f.must(t, err)
	if len(items) < 4 {
		t.Fatal("events not available to workspace read-only member")
	}
	_, err = repo.ListOperationsSecurityEvents(ctx, ws.ID, f.foreign.ID)
	f.want(t, err, ErrNotFound)
}
