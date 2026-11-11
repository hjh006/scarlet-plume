package store

import (
	"errors"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

func TestPostgresG03ReappearanceUsesVerifiedPermissionSnapshot(t *testing.T) {
	f := newOperationsFixture(t)
	ctx, repo, ws := f.ctx, f.repo, f.workspace
	config := ManagedPostgresInput{Host: "sample-db", Port: 5432, Database: "recurrence_source", User: "reader", Schemas: []string{"inventory"}, IntervalMinutes: 5, AutoChecks: true, EncryptedPassword: []byte("fixture")}
	job, err := repo.CreateManagedPostgresJob(ctx, ws.ID, f.owner.ID, "Permission recurrence", operationsTestDigest(f.slug), config)
	f.must(t, err)
	table := collector.PostgreSQLTable{Asset: collector.Asset{SourceRef: "inventory.contacts", Name: "contacts"}, Schema: "inventory", Owner: "source_owner", MetadataStatus: "complete", Columns: []collector.PostgreSQLColumn{{Name: "value", Type: "text", BaseType: "text", CanSelect: true, Ordinal: 1}}, Grants: []collector.PostgreSQLGrant{{Grantee: "PUBLIC", Public: true, Privilege: "SELECT", SchemaUsage: true}}, PublicSelect: true, PublicSchemaUsage: true, PublicSelectColumns: []string{}, Constraints: []collector.PostgreSQLConstraint{}, Warnings: []string{}}
	var lastClaim ManagedPostgresClaim
	var lastInput AutomationDelivery
	collect := func(name string, at time.Time) {
		t.Helper()
		f.must(t, repo.QueueManagedPostgresJob(ctx, ws.ID, f.owner.ID, job.ID))
		claim, err := repo.ClaimManagedPostgresJob(ctx, name, time.Minute)
		f.must(t, err)
		if claim == nil || claim.JobID != job.ID {
			t.Fatal("could not claim own source")
		}
		input := AutomationDelivery{Digest: operationsTestDigest(name), ObservedAt: at, Assets: []CollectedAsset{{SourceRef: table.SourceRef, Name: table.Name}}, PostgresTables: []collector.PostgreSQLTable{table}}
		_, err = repo.FinishManagedPostgresJob(ctx, *claim, input)
		f.must(t, err)
		lastClaim, lastInput = *claim, input
	}
	checkIDs := func() []int64 {
		t.Helper()
		rows, err := f.pool.Query(ctx, `SELECT id FROM plume.checks WHERE workspace_id=$1 AND requirement_code='G03' ORDER BY id`, ws.ID)
		f.must(t, err)
		defer rows.Close()
		ids := []int64{}
		for rows.Next() {
			var id int64
			f.must(t, rows.Scan(&id))
			ids = append(ids, id)
		}
		f.must(t, rows.Err())
		return ids
	}
	verify := func(id int64) {
		t.Helper()
		f.must(t, repo.SubmitCheck(ctx, ws.ID, f.owner.ID, id))
		d, err := repo.CheckDetail(ctx, ws.ID, f.reviewer.ID, id)
		f.must(t, err)
		f.must(t, repo.ReviewCheck(ctx, ws.ID, f.reviewer.ID, id, d.ReviewRevision, "verified", "Independently reviewed current permission scope"))
	}
	collect("initial-public", time.Now().UTC())
	ids := checkIDs()
	if len(ids) != 1 {
		t.Fatal("initial G03 missing")
	}
	verify(ids[0])
	collect("same-approved-public", time.Now().UTC())
	if len(checkIDs()) != 1 {
		t.Fatal("unchanged approved PUBLIC reopened each poll")
	}
	table.PublicSelect = false
	table.Grants = []collector.PostgreSQLGrant{}
	collect("permission-reduced", time.Now().UTC())
	if len(checkIDs()) != 1 {
		t.Fatal("permission reduction created a positive recurrence")
	}
	table.PublicSelect = true
	table.Grants = []collector.PostgreSQLGrant{{Grantee: "PUBLIC", Public: true, Privilege: "SELECT", SchemaUsage: true}}
	collect("same-approved-capability", time.Now().UTC())
	if len(checkIDs()) != 1 {
		t.Fatal("same permission as adopted review reopened after an unreviewed transient reduction")
	}
	table.Grants = append(table.Grants, collector.PostgreSQLGrant{Grantee: "named_reader", Privilege: "SELECT", SchemaUsage: true})
	collect("new-named-grant", time.Now().UTC())
	ids = checkIDs()
	if len(ids) != 2 {
		t.Fatal("new non-owner direct SELECT did not create next G03")
	}
	collect("same-open-scope", time.Now().UTC())
	if len(checkIDs()) != 2 {
		t.Fatal("open G03 duplicated on repeated polling")
	}
	f.must(t, repo.SubmitCheck(ctx, ws.ID, f.owner.ID, ids[1]))
	d, err := repo.CheckDetail(ctx, ws.ID, f.reviewer.ID, ids[1])
	f.must(t, err)
	f.must(t, repo.ReviewCheck(ctx, ws.ID, f.reviewer.ID, ids[1], d.ReviewRevision, "changes", "Remove source permissions"))
	f.must(t, repo.AddEvidence(ctx, ws.ID, f.owner.ID, ids[1], EvidenceInput{Kind: "remediation", SourceSystem: "fixture", SourceRecord: "revoke", Summary: "Removed direct access", ObservedAt: time.Now().UTC()}))
	table.Grants = []collector.PostgreSQLGrant{}
	table.PublicSelect = false
	collect("fresh-clean-retest", time.Now().UTC())
	startedBeforeReview := time.Now().UTC()
	verify(ids[1])
	sealed, err := repo.CheckDetail(ctx, ws.ID, f.owner.ID, ids[1])
	f.must(t, err)
	if sealed.Evidence[0].Postgres == nil || sealed.Evidence[0].Postgres.PermissionBaseline == nil {
		t.Fatal("full immutable permission baseline was not adopted")
	}
	sealedCount := len(sealed.Evidence)
	table.Grants = []collector.PostgreSQLGrant{{Grantee: "PUBLIC", Public: true, Privilege: "SELECT", SchemaUsage: true}}
	table.PublicSelect = true
	collect("old-start-late-finish", startedBeforeReview)
	if len(checkIDs()) != 2 {
		t.Fatal("scan begun before verification was treated as post-review recurrence")
	}
	collect("explicit-public-restoration", time.Now().UTC())
	if len(checkIDs()) != 3 {
		t.Fatal("PUBLIC restoration after verified clean source did not reopen G03")
	}
	_, err = repo.FinishManagedPostgresJob(ctx, lastClaim, lastInput)
	if err != nil && !errors.Is(err, ErrConflict) {
		f.must(t, err)
	}
	collect("restored-repeat", time.Now().UTC())
	if len(checkIDs()) != 3 {
		t.Fatal("delivery replay or unchanged recurrence duplicated G03")
	}
	sealed, err = repo.CheckDetail(ctx, ws.ID, f.owner.ID, ids[1])
	f.must(t, err)
	if sealed.Status != "verified" || len(sealed.Evidence) != sealedCount || sealed.Evidence[0].Postgres.PublicSelect {
		t.Fatal("new source state rewrote verified authorization evidence")
	}
}

func TestPostgresPermissionIncreasePartialAndFullBaselines(t *testing.T) {
	old := PostgresObservation{PublicSelect: true, PublicSchemaUsage: true, SelectGrants: []PostgresSelectGrant{{Grantee: "PUBLIC", Public: true, SchemaUsage: true}}}
	current := collector.PostgreSQLTable{Owner: "owner", PublicSelect: true, PublicSchemaUsage: true, Grants: []collector.PostgreSQLGrant{{Grantee: "PUBLIC", Public: true, Privilege: "SELECT", SchemaUsage: true}}}
	if postgresPermissionIncreased(old, current) {
		t.Fatal("same legacy public grant is not recurrence")
	}
	old.SelectGrantsOmitted = 3
	current.Grants = append(current.Grants, collector.PostgreSQLGrant{Grantee: "not_in_short_preview", Privilege: "SELECT"})
	if postgresPermissionIncreased(old, current) {
		t.Fatal("truncated legacy ACL preview was treated as proof a named grant was absent")
	}
	old.PublicSelect = false
	old.PublicSelectColumnCount = 0
	if !postgresPermissionIncreased(old, current) {
		t.Fatal("known PUBLIC false to true was hidden by truncated named ACLs")
	}
	old = PostgresObservation{PermissionBaseline: &PostgresPermissionBaseline{SelectGrants: []PostgresSelectGrant{{Grantee: "reader", Grantable: true}, {Grantee: "reader"}}}}
	current = collector.PostgreSQLTable{Owner: "owner", Grants: []collector.PostgreSQLGrant{{Grantee: "reader", Privilege: "SELECT", Grantable: true}}}
	if postgresPermissionIncreased(old, current) {
		t.Fatal("grantor duplicate order fabricated a grant-option increase")
	}
}
