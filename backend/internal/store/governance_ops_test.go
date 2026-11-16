package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/governanceops"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGovernanceOpsTenantEvidenceIndependenceAndHistory(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
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
	slug := fmt.Sprintf("govops-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Governance operations"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	users := []string{slug}
	member := func(role string) int64 {
		t.Helper()
		name := slug + "-" + role
		users = append(users, name)
		var id int64
		must(pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash)VALUES($1,'unused')RETURNING id`, name).Scan(&id))
		_, e := pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role)VALUES($1,$2,$3)`, ws.ID, id, role)
		must(e)
		return id
	}
	operator := member("operator")
	reviewer := member("reviewer")
	viewer := member("viewer")
	otherSlug := slug + "-other"
	users = append(users, otherSlug)
	must(repo.Bootstrap(ctx, otherSlug, "unused", otherSlug, "Other tenant"))
	otherUser, err := repo.FindUser(ctx, otherSlug)
	must(err)
	other, err := repo.Membership(ctx, otherUser.ID, otherSlug)
	must(err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1::bigint[])`, []int64{ws.ID, other.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE username=ANY($1::text[])`, users)
	})
	var asset, foreignAsset int64
	must(pool.QueryRow(ctx, `INSERT INTO plume.assets(workspace_id,name,system)VALUES($1,'source','fixture')RETURNING id`, ws.ID).Scan(&asset))
	must(pool.QueryRow(ctx, `INSERT INTO plume.assets(workspace_id,name,system)VALUES($1,'foreign','fixture')RETURNING id`, other.ID).Scan(&foreignAsset))
	activity, err := repo.CreateActivity(ctx, ws.ID, owner.ID, ActivityInput{AssetID: asset, Name: "source activity", Kind: "internal", Purpose: "rights handling"})
	must(err)
	foreignActivity, err := repo.CreateActivity(ctx, other.ID, otherUser.ID, ActivityInput{AssetID: foreignAsset, Name: "foreign activity", Kind: "internal", Purpose: "isolation"})
	must(err)
	check, err := repo.CreateCheck(ctx, ws.ID, owner.ID, CheckInput{AssetID: asset, RequirementCode: "P01", OwnerTeam: "privacy", Note: "source evidence"})
	must(err)
	foreignCheck, err := repo.CreateCheck(ctx, other.ID, otherUser.ID, CheckInput{AssetID: foreignAsset, RequirementCode: "P01", OwnerTeam: "privacy", Note: "foreign evidence"})
	must(err)
	now := time.Now().UTC().Add(-time.Minute)
	addEvidence := func(checkID, actor int64, summary string) int64 {
		t.Helper()
		var id int64
		must(pool.QueryRow(ctx, `INSERT INTO plume.check_evidence(check_id,kind,source_system,source_record,summary,observed_at,created_by)VALUES($1,'observation','fixture','receipt-1',$2,$3,$4)RETURNING id`, checkID, summary, now, actor).Scan(&id))
		return id
	}
	evidence := addEvidence(check.ID, owner.ID, "original recorded receipt")
	foreignEvidence := addEvidence(foreignCheck.ID, otherUser.ID, "foreign recorded receipt")
	secondActivity, err := repo.CreateActivity(ctx, ws.ID, owner.ID, ActivityInput{AssetID: asset, Name: "unrelated processing", Kind: "internal", Purpose: "different processing"})
	must(err)
	secondCheck, err := repo.CreateCheck(ctx, ws.ID, owner.ID, CheckInput{AssetID: asset, ActivityID: &secondActivity.ID, RequirementCode: "P01", OwnerTeam: "privacy", Note: "other processing evidence"})
	must(err)
	secondEvidence := addEvidence(secondCheck.ID, owner.ID, "other processing receipt")
	in := governanceops.Input{Kind: "rights_request", Title: "Copy request", Owner: "privacy", ActivityID: activity.ID, CheckIDs: []int64{check.ID}, EvidenceIDs: []int64{evidence}, RegulationIDs: []string{"cn-pipl-2021"}, Applicability: "applicable", ApplicabilityReason: "Verified relevant processing", Details: governanceops.Details{RequestType: "copy", SubjectRef: strings.Repeat("a", 64), ReceivedAt: &now, IdentityStatus: "verified", IdentityEvidenceID: evidence, Outcome: "delivered by source", Resolution: "fulfilled"}, Receipts: []governanceops.Receipt{{Purpose: "identity_verification", EvidenceID: evidence}, {Purpose: "response_delivery", EvidenceID: evidence}}}
	bad := in
	bad.ActivityID = foreignActivity.ID
	_, err = repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, bad)
	want(err, ErrInvalid)
	bad = in
	bad.CheckIDs = []int64{foreignCheck.ID}
	_, err = repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, bad)
	want(err, ErrInvalid)
	bad = in
	bad.EvidenceIDs = []int64{foreignEvidence}
	bad.Details.IdentityEvidenceID = foreignEvidence
	bad.Receipts = nil
	_, err = repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, bad)
	want(err, ErrInvalid)
	bad = in
	bad.Details.IdentityEvidenceID = foreignEvidence
	_, err = repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, bad)
	want(err, ErrInvalid)
	bad = in
	bad.Receipts = []governanceops.Receipt{{Purpose: "identity_verification", EvidenceID: foreignEvidence}}
	_, err = repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, bad)
	want(err, ErrInvalid)
	bad = in
	bad.CheckIDs = []int64{secondCheck.ID}
	_, err = repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, bad)
	want(err, ErrInvalid)
	bad = in
	bad.CheckIDs = nil
	bad.EvidenceIDs = []int64{secondEvidence}
	bad.Details.IdentityEvidenceID = secondEvidence
	bad.Receipts = []governanceops.Receipt{{Purpose: "identity_verification", EvidenceID: secondEvidence}, {Purpose: "response_delivery", EvidenceID: secondEvidence}}
	_, err = repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, bad)
	want(err, ErrInvalid)
	_, err = repo.SaveGovernanceOps(ctx, ws.ID, viewer, 0, in)
	want(err, ErrForbidden)
	pendingPlan := governanceops.Input{Kind: "audit", Title: "Audit plan", Owner: "privacy", Applicability: "unknown", ApplicabilityReason: "Collecting scale and scope", Details: governanceops.Details{Scope: "PI compliance audit"}}
	plan, err := repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, pendingPlan)
	must(err)
	if plan.Status != "draft" || len(plan.Evidence) != 0 || len(plan.Regulations) != 0 {
		t.Fatal("pending plan did not preserve unknowns", plan)
	}
	lateRequest := in
	receivedLater := now.Add(30 * time.Second)
	lateRequest.Details.ReceivedAt = &receivedLater
	staleRecord, err := repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, lateRequest)
	must(err)
	if len(staleRecord.EvidenceIssues) == 0 {
		t.Fatal("old receipt silently accepted for new request")
	}
	staleRecord, err = repo.ActGovernanceOps(ctx, ws.ID, operator, staleRecord.ID, governanceops.Action{Revision: staleRecord.Revision, Action: "start", Note: "investigate stale evidence"})
	must(err)
	_, err = repo.ActGovernanceOps(ctx, ws.ID, operator, staleRecord.ID, governanceops.Action{Revision: staleRecord.Revision, Action: "submit", Note: "attempt old receipt reuse"})
	want(err, ErrEvidenceRequired)
	record, err := repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, in)
	must(err)
	if len(record.Evidence) != 1 || record.Evidence[0].SourceRecord != "receipt-1" || len(record.Evidence[0].SnapshotDigest) != 64 || len(record.Regulations) != 1 || len(record.History) != 1 {
		t.Fatalf("missing frozen evidence: %#v", record)
	}
	// The original evidence author is a contributor even though a different
	// operator created the case. Neither can independently approve it.
	if len(record.Contributors) != 2 {
		t.Fatal("evidence author not recorded", record.Contributors)
	}
	_, err = pool.Exec(ctx, `UPDATE plume.check_evidence SET summary='later source edit' WHERE id=$1`, evidence)
	must(err)
	frozen, err := repo.GovernanceOpsRecord(ctx, ws.ID, viewer, record.ID)
	must(err)
	if frozen.Evidence[0].Summary != "original recorded receipt" {
		t.Fatal("snapshot overwritten by source changes")
	}
	action := func(actor int64, name string) (GovernanceOpsRecord, error) {
		return repo.ActGovernanceOps(ctx, ws.ID, actor, record.ID, governanceops.Action{Revision: record.Revision, Action: name, Note: "independent case handling"})
	}
	record, err = action(operator, "start")
	must(err)
	_, err = repo.ActGovernanceOps(ctx, ws.ID, operator, record.ID, governanceops.Action{Revision: 1, Action: "submit", Note: "stale revision"})
	want(err, ErrConflict)
	record, err = action(operator, "submit")
	must(err)
	_, err = action(owner.ID, "approve")
	want(err, ErrForbidden)
	_, err = action(operator, "approve")
	want(err, ErrForbidden)
	_, err = action(viewer, "approve")
	want(err, ErrForbidden)
	record, err = action(reviewer, "approve")
	must(err)
	if record.Status != "completed" {
		t.Fatal(record.Status)
	}
	in.Revision = record.Revision
	_, err = repo.SaveGovernanceOps(ctx, ws.ID, operator, record.ID, in)
	want(err, ErrConflict)
	_, err = repo.GovernanceOpsRecord(ctx, other.ID, otherUser.ID, record.ID)
	want(err, ErrNotFound)
	_, err = repo.GovernanceOpsHistory(ctx, other.ID, otherUser.ID, record.ID, 0, 50)
	want(err, ErrNotFound)
	if _, err = pool.Exec(ctx, `UPDATE plume.governance_ops_events SET note='overwrite' WHERE record_id=$1`, record.ID); err == nil {
		t.Fatal("history update allowed")
	}
	// Lists and history expose true keyset continuation, not silently truncated
	// fixed windows. Interleave a new record before fetching the continuation.
	in.Revision = 0
	second, err := repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, in)
	must(err)
	firstPage, err := repo.ListGovernanceOps(ctx, ws.ID, viewer, "rights_request", "", 0, 1)
	must(err)
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != second.ID || !firstPage.HasMore || firstPage.NextCursor != second.ID {
		t.Fatal("invalid first page", firstPage)
	}
	_, err = repo.SaveGovernanceOps(ctx, ws.ID, operator, 0, in)
	must(err)
	page, err := repo.ListGovernanceOps(ctx, ws.ID, viewer, "rights_request", "", firstPage.NextCursor, 1)
	must(err)
	if len(page.Items) != 1 || page.Items[0].ID != record.ID || !page.HasMore {
		t.Fatal("keyset skipped or duplicated", page)
	}
	seen := map[int64]bool{}
	cursor := int64(0)
	for {
		p, e := repo.GovernanceOpsHistory(ctx, ws.ID, viewer, record.ID, cursor, 1)
		must(e)
		if len(p.Items) != 1 || seen[p.Items[0].ID] {
			t.Fatal("history duplicate/missing", p)
		}
		seen[p.Items[0].ID] = true
		if !p.HasMore {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != 4 {
		t.Fatal("missing history revisions", len(seen))
	}
	_, err = repo.ListGovernanceOps(ctx, ws.ID, viewer, "rights_request", "", 0, 101)
	want(err, ErrInvalid)
}
