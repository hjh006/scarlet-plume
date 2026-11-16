package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReportArchiveRetentionGuardsRotationAndIntegrity(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := New(pool)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	want := func(e, target error) {
		t.Helper()
		if !errors.Is(e, target) {
			t.Fatalf("got %v want %v", e, target)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("archive-retention-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Archive retention"))
	owner, err := repo.FindUser(ctx, slug)
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	users := []string{slug}
	member := func(role string) int64 {
		var id int64
		name := slug + "-" + role
		users = append(users, name)
		must(pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash)VALUES($1,'unused')RETURNING id`, name).Scan(&id))
		_, e := pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role)VALUES($1,$2,$3)`, ws.ID, id, role)
		must(e)
		return id
	}
	reviewer := member("reviewer")
	operator := member("operator")
	viewer := member("viewer")
	otherSlug := slug + "-foreign"
	users = append(users, otherSlug)
	must(repo.Bootstrap(ctx, otherSlug, "unused", otherSlug, "Foreign"))
	foreignUser, err := repo.FindUser(ctx, otherSlug)
	must(err)
	foreignWS, err := repo.Membership(ctx, foreignUser.ID, otherSlug)
	must(err)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = pool.Exec(c, `UPDATE plume.automation_report_archives a SET retention_state='rotation_allowed',legal_hold=false,retain_until=NULL FROM plume.automation_runs r JOIN plume.automation_jobs j ON j.id=r.job_id WHERE a.automation_run_id=r.id AND j.workspace_id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.automation_report_archives a USING plume.automation_runs r,plume.automation_jobs j WHERE a.automation_run_id=r.id AND r.job_id=j.id AND j.workspace_id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1::bigint[])`, []int64{ws.ID, foreignWS.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE username=ANY($1::text[])`, users)
	})
	var job int64
	must(pool.QueryRow(ctx, `INSERT INTO plume.automation_jobs(workspace_id,actor_id,provider,source,token_digest)VALUES($1,$2,'postgresql','fixture',$3)RETURNING id`, ws.ID, owner.ID, fmt.Sprintf("%x", sha256.Sum256([]byte(slug)))).Scan(&job))
	ids := []int64{}
	for i := 0; i < 32; i++ {
		var run int64
		must(pool.QueryRow(ctx, `INSERT INTO plume.automation_runs(job_id,run_id,payload_digest,status,observed_at)VALUES($1,$2,$3,'success',clock_timestamp())RETURNING id`, job, fmt.Sprintf("retention-%d", i), strings.Repeat("b", 64)).Scan(&run))
		_, e := pool.Exec(ctx, `INSERT INTO plume.automation_report_archives(automation_run_id,generated_at,status,summary,report)VALUES($1,clock_timestamp(),'ready','{"counts":{},"identification":null}',jsonb_build_object('scope',jsonb_build_object('basis','fixture'),'ordinal',$2::int))`, run, i)
		must(e)
		ids = append(ids, run)
	}
	prune := func() {
		must(pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return pruneReportArchives(ctx, tx, job) }))
	}
	prune()
	page, err := repo.ListReportArchivePage(ctx, ws.ID, viewer, 0, 100)
	must(err)
	if len(page.Items) != 32 || page.Items[31].Retention.State != "review_required" {
		t.Fatal("unknown purpose reports were silently rotated", page)
	}
	old := page.Items[31]
	for _, q := range []string{`DELETE FROM plume.automation_report_archives WHERE automation_run_id=$1`, `DELETE FROM plume.automation_runs WHERE id=$1`} {
		_, e := pool.Exec(ctx, q, old.ID)
		want(archiveRetentionError(e), ErrReportArchiveProtected)
	}
	_, err = pool.Exec(ctx, `DELETE FROM plume.automation_jobs WHERE id=$1`, job)
	want(archiveRetentionError(err), ErrReportArchiveProtected)
	_, err = pool.Exec(ctx, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
	want(archiveRetentionError(err), ErrReportArchiveProtected)
	_, err = pool.Exec(ctx, `UPDATE plume.automation_report_archives SET report='{}' WHERE automation_run_id=$1`, old.ID)
	want(archiveRetentionError(err), ErrReportArchiveIntegrity)
	raw, verified, err := repo.ReportArchiveVerifiedBytes(ctx, ws.ID, viewer, old.ID)
	must(err)
	if verified.IntegrityStatus != "verified" || verified.ExternalStorageStatus != "unverified" || len(verified.ContentDigest) != 64 {
		t.Fatal(verified)
	}
	preparedID, err := repo.RecordReportArchiveExport(ctx, ws.ID, viewer, old.ID, verified.ContentDigest, "json", verified.ContentDigest, int64(len(raw)))
	must(err)
	prepared, err := repo.ReportArchiveRetentionHistory(ctx, ws.ID, viewer, old.ID, 0, 1)
	must(err)
	if len(prepared.Items) != 1 || prepared.Items[0].ID != preparedID || prepared.Items[0].Action != "export_prepared" || prepared.Items[0].Snapshot["deliveryStatus"] != "prepared" {
		t.Fatal("export claimed delivery or lost receipt", prepared)
	}
	_, _, err = repo.ReportArchiveVerifiedBytes(ctx, foreignWS.ID, foreignUser.ID, old.ID)
	want(err, ErrNotFound)
	asset, err := repo.CreateAsset(ctx, ws.ID, "Decision record", "fixture", "privacy")
	must(err)
	check, err := repo.CreateCheck(ctx, ws.ID, owner.ID, CheckInput{AssetID: asset.ID, RequirementCode: "G01", OwnerTeam: "privacy", Note: "Retirement review evidence"})
	must(err)
	addEvidence := func(actor int64, at time.Time) int64 {
		var id int64
		must(pool.QueryRow(ctx, `INSERT INTO plume.check_evidence(check_id,kind,source_system,source_record,summary,observed_at,created_by)VALUES($1,'observation','fixture','disposition-review','Human recorded retention disposition evidence',$2,$3)RETURNING id`, check.ID, at, actor).Scan(&id))
		return id
	}
	staleEvidence := addEvidence(owner.ID, time.Now().Add(-time.Hour))
	_, err = repo.SetReportArchiveRetention(ctx, ws.ID, viewer, old.ID, ReportArchiveRetentionInput{Revision: old.Retention.Revision, Action: "hold", Basis: "investigation hold"})
	want(err, ErrForbidden)
	old, err = repo.SetReportArchiveRetention(ctx, ws.ID, operator, old.ID, ReportArchiveRetentionInput{Revision: old.Retention.Revision, Action: "hold", Basis: "Preserve for active investigation"})
	must(err)
	_, err = repo.SetReportArchiveRetention(ctx, ws.ID, reviewer, old.ID, ReportArchiveRetentionInput{Revision: 1, Action: "release_hold", Basis: "stale", EvidenceID: staleEvidence})
	want(err, ErrConflict)
	_, err = repo.SetReportArchiveRetention(ctx, ws.ID, reviewer, old.ID, ReportArchiveRetentionInput{Revision: old.Retention.Revision, Action: "release_hold", Basis: "old receipt", EvidenceID: staleEvidence})
	want(err, ErrEvidenceRequired)
	freshEvidence := addEvidence(owner.ID, time.Now())
	_, err = repo.SetReportArchiveRetention(ctx, ws.ID, owner.ID, old.ID, ReportArchiveRetentionInput{Revision: old.Retention.Revision, Action: "release_hold", Basis: "self release", EvidenceID: freshEvidence})
	want(err, ErrForbidden)
	_, err = repo.SetReportArchiveRetention(ctx, ws.ID, reviewer, old.ID, ReportArchiveRetentionInput{Revision: old.Retention.Revision, Action: "allow_rotation", Basis: "still held", EvidenceID: freshEvidence})
	want(err, ErrReportArchiveProtected)
	old, err = repo.SetReportArchiveRetention(ctx, ws.ID, reviewer, old.ID, ReportArchiveRetentionInput{Revision: old.Retention.Revision, Action: "release_hold", Basis: "Independent review of actual release evidence", EvidenceID: freshEvidence})
	must(err)
	future := time.Now().Add(48 * time.Hour)
	old, err = repo.SetReportArchiveRetention(ctx, ws.ID, operator, old.ID, ReportArchiveRetentionInput{Revision: old.Retention.Revision, Action: "extend", Basis: "Specific report retention decision", RetainUntil: &future})
	must(err)
	shorter := future.Add(-time.Hour)
	_, err = repo.SetReportArchiveRetention(ctx, ws.ID, owner.ID, old.ID, ReportArchiveRetentionInput{Revision: old.Retention.Revision, Action: "extend", Basis: "attempt to shorten", RetainUntil: &shorter})
	want(err, ErrConflict)
	_, err = repo.SetReportArchiveRetention(ctx, ws.ID, reviewer, old.ID, ReportArchiveRetentionInput{Revision: old.Retention.Revision, Action: "allow_rotation", Basis: "not expired", EvidenceID: freshEvidence})
	want(err, ErrReportArchiveProtected)
	prune()
	if _, _, err = repo.ReportArchiveVerifiedBytes(ctx, ws.ID, viewer, old.ID); err != nil {
		t.Fatal("active minimum retention was rotated", err)
	}
	// A second archive has no asserted minimum duration and can be explicitly
	// released by an independent reviewer with real, tenant-bound evidence.
	var second ReportArchive
	for _, a := range page.Items {
		if a.ID == ids[1] {
			second = a
		}
	}
	second, err = repo.SetReportArchiveRetention(ctx, ws.ID, reviewer, second.ID, ReportArchiveRetentionInput{Revision: second.Retention.Revision, Action: "allow_rotation", Basis: "No ongoing purpose; assessed scope and retention decision", EvidenceID: freshEvidence})
	must(err)
	prune()
	_, _, err = repo.ReportArchiveVerifiedBytes(ctx, ws.ID, viewer, second.ID)
	want(err, ErrNotFound)
	history, err := repo.ReportArchiveRetentionHistory(ctx, ws.ID, viewer, second.ID, 0, 1)
	must(err)
	if len(history.Items) != 1 || history.Items[0].Action != "rotation_purged" || history.Items[0].Snapshot["contentDigest"] != second.ContentDigest || !history.HasMore {
		t.Fatal("missing deletion tombstone", history)
	}
	_, err = pool.Exec(ctx, `DELETE FROM plume.report_archive_retention_events WHERE archive_id=$1`, second.ID)
	if err == nil {
		t.Fatal("direct history deletion allowed")
	}
	_, err = repo.ReportArchiveRetentionHistory(ctx, foreignWS.ID, foreignUser.ID, second.ID, 0, 50)
	want(err, ErrNotFound)
	p1, err := repo.ListReportArchivePage(ctx, ws.ID, viewer, 0, 1)
	must(err)
	p2, err := repo.ListReportArchivePage(ctx, ws.ID, viewer, p1.NextCursor, 1)
	must(err)
	if !p1.HasMore || len(p2.Items) != 1 || p1.Items[0].ID == p2.Items[0].ID {
		t.Fatal("archive keyset continuation broken")
	}
	// Simulate damaged digest storage as the disposable database administrator.
	// The table lock spans disable/update/enable so concurrent package tests never
	// observe a disabled guard. The application must refuse the inconsistent row.
	corrupt := func(digest string) {
		must(pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `ALTER TABLE plume.automation_report_archives DISABLE TRIGGER report_archive_protection`); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `UPDATE plume.automation_report_archives SET content_digest=$2 WHERE automation_run_id=$1`, old.ID, digest); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `ALTER TABLE plume.automation_report_archives ENABLE TRIGGER report_archive_protection`)
			return e
		}))
	}
	corrupt(strings.Repeat("f", 64))
	_, _, err = repo.ReportArchiveVerifiedBytes(ctx, ws.ID, viewer, old.ID)
	want(err, ErrReportArchiveIntegrity)
	corrupt(verified.ContentDigest)
}
