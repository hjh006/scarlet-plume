package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestManagedPostgresInspectionEvidence(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure PostgreSQL test database")
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
	now := func() time.Time {
		t.Helper()
		var stamp time.Time
		want(pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&stamp), nil)
		return stamp
	}

	slug := fmt.Sprintf("pg-inspection-%d", time.Now().UnixNano())
	ownerName, reviewerName := slug+"-owner", slug+"-reviewer"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = $1", slug); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", []string{ownerName, reviewerName}); err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "PostgreSQL inspection"), nil)
	want(repo.ProvisionReviewer(ctx, slug, reviewerName, "unused"), nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	reviewer, err := repo.FindUser(ctx, reviewerName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)

	config := store.ManagedPostgresInput{Host: "sample-db", Port: 5432, Database: "source_one", User: "reader",
		Schemas: []string{"inventory"}, IntervalMinutes: 5, EncryptedPassword: []byte("ciphertext-fixture")}
	job, err := repo.CreateManagedPostgresJob(ctx, workspace.ID, owner.ID, "Catalog", fmt.Sprintf("%x", sha256.Sum256([]byte(slug))), config)
	want(err, nil)
	table := collector.PostgreSQLTable{
		Asset: collector.Asset{SourceRef: "inventory.customers", Name: "customers"}, Schema: "inventory", Owner: "source_admin",
		Columns: []collector.PostgreSQLColumn{{Name: "email", Type: "text", Ordinal: 1, Sensitivity: "email"}},
		Grants: []collector.PostgreSQLGrant{
			{Grantee: "source_admin", Grantor: "source_admin", Privilege: "SELECT", SchemaUsage: true},
			{Grantee: "PUBLIC", Grantor: "source_admin", Public: true, Privilege: "SELECT", Column: "email", SchemaUsage: true},
		},
		PublicSchemaUsage: true, PublicSelectColumns: []string{"email"},
	}
	runTables := func(runID string, tables []collector.PostgreSQLTable) store.AutomationRun {
		t.Helper()
		want(repo.QueueManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID), nil)
		claim, err := repo.ClaimManagedPostgresJob(ctx, runID, time.Minute)
		want(err, nil)
		if claim == nil || claim.JobID != job.ID {
			t.Fatalf("expected to claim own job: %+v", claim)
		}
		assets := make([]store.CollectedAsset, 0, len(tables))
		for _, table := range tables {
			assets = append(assets, store.CollectedAsset{SourceRef: table.SourceRef, Name: table.Name})
		}
		result, err := repo.FinishManagedPostgresJob(ctx, *claim, store.AutomationDelivery{
			Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(runID))), ObservedAt: now(),
			Assets: assets, PostgresTables: tables,
		})
		want(err, nil)
		return result
	}
	run := func(runID string) store.AutomationRun {
		t.Helper()
		return runTables(runID, []collector.PostgreSQLTable{table})
	}

	initial := run("first")
	if initial.CreatedCount != 1 {
		t.Fatalf("unexpected first import: %+v", initial)
	}
	assets, err := repo.ListAssets(ctx, workspace.ID, 0, 10)
	want(err, nil)
	if len(assets) != 1 || assets[0].SourceProvider != "postgresql" {
		t.Fatalf("unexpected source assets: %+v", assets)
	}
	asset := assets[0]
	inspection, err := repo.GetAssetPostgresInspection(ctx, workspace.ID, reviewer.ID, asset.ID)
	want(err, nil)
	if inspection.JobID != job.ID || inspection.RunID != initial.RunID || len(inspection.Table.Columns) != 1 {
		t.Fatalf("unexpected inspection: %+v", inspection)
	}
	_, err = repo.GetAssetPostgresInspection(ctx, 0, owner.ID, asset.ID)
	want(err, store.ErrNotFound)
	createCheck := func(code string) store.Check {
		t.Helper()
		check, err := repo.CreateCheck(ctx, workspace.ID, owner.ID, store.CheckInput{AssetID: asset.ID,
			RequirementCode: code, OwnerTeam: "Data", Note: "Inspect scoped facts"})
		want(err, nil)
		return check
	}
	permissions := createCheck("G03")
	classification := createCheck("G02")
	detail := func(actor, check int64) store.CheckDetail {
		t.Helper()
		result, err := repo.CheckDetail(ctx, workspace.ID, actor, check)
		want(err, nil)
		return result
	}
	if available := detail(owner.ID, permissions.ID).AvailablePostgresJobID; available == nil || *available != job.ID {
		t.Fatal("source not available to the editor")
	}
	want(repo.BindCheckPostgres(ctx, workspace.ID, reviewer.ID, permissions.ID, job.ID), store.ErrForbidden)
	for _, check := range []store.Check{permissions, classification} {
		want(repo.BindCheckPostgres(ctx, workspace.ID, owner.ID, check.ID, job.ID), nil)
		want(repo.BindCheckPostgres(ctx, workspace.ID, owner.ID, check.ID, job.ID), nil)
	}
	run("evidence")
	run("unchanged")
	for _, check := range []store.Check{permissions, classification} {
		result := detail(owner.ID, check.ID)
		if len(result.Evidence) != 1 || len(result.Events) != 3 || result.PostgresJobID == nil || result.AvailablePostgresJobID != nil {
			t.Fatalf("unexpected binding or duplicate evidence: %+v", result)
		}
		evidence := result.Evidence[0]
		if evidence.Postgres == nil || evidence.AutomationRunID != "evidence" || evidence.Postgres.PublicSelectColumnCount != 1 ||
			evidence.Postgres.SensitiveCandidateCount != 1 || len(evidence.Postgres.SelectGrants) != 1 {
			t.Fatalf("unexpected observation: %+v", evidence)
		}
	}

	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, permissions.ID), nil)
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, classification.ID), nil)
	review := detail(reviewer.ID, permissions.ID)
	want(repo.ReviewCheck(ctx, workspace.ID, reviewer.ID, permissions.ID, review.ReviewRevision, "changes", "Remove the public grant"), nil)
	table.Grants = table.Grants[:1]
	table.PublicSelectColumns = []string{}
	run("before-remediation")
	if len(detail(owner.ID, permissions.ID).Evidence) != 1 {
		t.Fatal("retake was recorded before remediation")
	}
	want(repo.AddEvidence(ctx, workspace.ID, owner.ID, permissions.ID, store.EvidenceInput{
		Kind: "remediation", SourceSystem: "PostgreSQL", SourceRecord: "scope-review", Summary: "Revoked sample grant", ObservedAt: now(),
	}), nil)
	run("retest")
	result := detail(owner.ID, permissions.ID)
	if len(result.Evidence) != 3 || result.Status != "needs_work" || result.Evidence[0].Kind != "retest" ||
		result.Evidence[0].Postgres == nil || result.Evidence[0].Postgres.PublicSelectColumnCount != 0 ||
		result.Evidence[0].Postgres.Assessment != "no_signal" ||
		!slices.Equal(result.Evidence[0].Postgres.AssessmentReasons, []string{"no_non_owner_select"}) {
		t.Fatalf("expected captured retest without state change: %+v", result)
	}
	if len(detail(owner.ID, classification.ID).Evidence) != 1 {
		t.Fatal("in-review check received new evidence")
	}

	config.Database = "source_two"
	_, err = repo.UpdateManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID, 1, config)
	want(err, nil)
	table.Grants = append(table.Grants, collector.PostgreSQLGrant{Grantee: "PUBLIC", Public: true, Privilege: "SELECT"})
	table.PublicSelect = true
	run("retargeted")
	want(repo.BindCheckPostgres(ctx, workspace.ID, owner.ID, permissions.ID, job.ID), store.ErrConflict)
	if len(detail(owner.ID, permissions.ID).Evidence) != 3 {
		t.Fatal("retargeted source wrote evidence into an existing check")
	}
	report, err := repo.ComplianceReport(ctx, workspace.ID, reviewer.ID, 0)
	want(err, nil)
	if len(report.Checks) != 2 || report.Counts.Evidence != 4 || report.Counts.Retests != 1 ||
		report.Checks[0].Evidence[0].Postgres == nil {
		t.Fatalf("report dropped collected evidence: %+v", report)
	}

	config.AutoChecks = true
	updated, err := repo.UpdateManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID, 2, config)
	want(err, nil)
	if !updated.Managed.AutoChecks || updated.Managed.AutoReports {
		t.Fatalf("unexpected automatic job configuration: %+v", updated.Managed)
	}
	run("automatic")
	run("automatic-unchanged")
	checks, err := repo.ListChecks(ctx, workspace.ID, "", 0, 10)
	want(err, nil)
	if len(checks) != 4 {
		t.Fatalf("expected two new checks for the new source identity, got %d total", len(checks))
	}
	automatic := map[string]int64{}
	for _, check := range checks {
		if check.ID == classification.ID || check.ID == permissions.ID {
			continue
		}
		result := detail(owner.ID, check.ID)
		automatic[check.Requirement.Code] = check.ID
		if result.Status != "draft" || result.PostgresJobID == nil || *result.PostgresJobID != job.ID ||
			len(result.Events) != 3 || len(result.Evidence) != 1 || result.Evidence[0].AutomationRunID != "automatic" {
			t.Fatalf("unexpected automated check after unchanged collection: %+v", result)
		}
	}
	assertAssessment := func(code, runID, assessment string, reasons []string) {
		t.Helper()
		result := detail(owner.ID, automatic[code])
		evidence := result.Evidence[0]
		if result.Status != "draft" || evidence.AutomationRunID != runID || evidence.Postgres == nil ||
			evidence.Postgres.Scope == "" || evidence.Postgres.Assessment != assessment ||
			!slices.Equal(evidence.Postgres.AssessmentReasons, reasons) {
			t.Fatalf("unexpected %s assessment: %+v", code, result)
		}
	}
	assertAssessment("G02", "automatic", "review_required", []string{"sensitive_candidate"})
	assertAssessment("G03", "automatic", "review_required", []string{"public_select"})

	table.Columns = []collector.PostgreSQLColumn{{Name: "category", Type: "text", Ordinal: 1}}
	table.Grants = append(table.Grants[:1], collector.PostgreSQLGrant{
		Grantee: "named_reader", Grantor: "source_admin", Privilege: "SELECT", SchemaUsage: true,
	})
	table.PublicSelect = false
	run("automatic-change")
	assertAssessment("G02", "automatic-change", "no_signal", []string{"no_sensitive_candidate"})
	assertAssessment("G03", "automatic-change", "review_required", []string{"named_select"})

	config.Database = "source_one"
	_, err = repo.UpdateManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID, updated.Managed.Revision, config)
	want(err, nil)
	run("automatic-reuse")
	checks, err = repo.ListChecks(ctx, workspace.ID, "", 0, 10)
	want(err, nil)
	if len(checks) != 4 {
		t.Fatalf("expected existing bound checks to be reused, got %d total", len(checks))
	}

	oversized := make([]collector.PostgreSQLTable, store.ManagedPostgresAutoCheckTableLimit+1)
	for i := range oversized {
		name := fmt.Sprintf("customer_%d", i)
		oversized[i] = collector.PostgreSQLTable{Asset: collector.Asset{SourceRef: "inventory." + name, Name: name}, Schema: "inventory"}
	}
	limited := runTables("oversized", oversized)
	if limited.Status != "error" || limited.Code != "automation_scope_limit" || limited.ImportID != nil {
		t.Fatalf("expected recorded scope error without partial import: %+v", limited)
	}
	assets, err = repo.ListAssets(ctx, workspace.ID, 0, 10)
	want(err, nil)
	if len(assets) != 1 {
		t.Fatalf("oversized scan updated assets: %+v", assets)
	}
}
