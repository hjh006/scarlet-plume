package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCatalogGraphAndClassificationCheckLifecycle(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := t.Context()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("catalog-linked-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Catalog linked test"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	reviewer, e := repo.FindUser(ctx, slug+"-reviewer")
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE username=ANY($1)`, []string{slug, slug + "-reviewer"})
	})
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(slug)))
	source, e := repo.SaveDiscoverySource(ctx, ws.ID, owner.ID, 0, store.DiscoverySourceInput{Source: "shared-catalog", Provider: "mysql", System: "mysql", Database: "inventory", Scopes: []string{}, IntervalMinutes: 15, Enabled: true}, token)
	must(e)
	table := collector.DiscoveryTable{Database: "inventory", Schema: "inventory", Name: "customers", PermissionsStatus: "current_account_visible_grants", Fields: []collector.DiscoveryField{{Name: "email", Type: "varchar", Hits: []collector.DiscoveryHit{{RuleKey: "builtin.email", RuleVersion: 1, Category: "email", Method: "bounded_customer_sample", MatchCount: 1, ScannedCount: 1}}}}, ScannedRows: 1, Warnings: []string{}}
	counter := 0
	run := func(tables []collector.DiscoveryTable, status string) {
		t.Helper()
		must(repo.QueueDiscoverySource(ctx, ws.ID, owner.ID, source.ID, source.Revision, "collect"))
		counter++
		claim, e := repo.ClaimDiscovery(ctx, token, fmt.Sprintf("%032x", counter))
		must(e)
		if claim == nil {
			t.Fatal("no lease")
		}
		input := store.DiscoverySubmission{SourceID: source.ID, Revision: source.Revision, RunRef: claim.RunRef, Status: status, ObservedAt: time.Now().UTC(), Result: collector.DiscoveryResult{Tables: tables, Coverage: collector.DiscoveryCoverage{Metadata: "observed", Content: "bounded_sample", Permissions: "current_account_visible_grants", Lineage: "unknown"}}}
		if status == "error" {
			input.ErrorCode = "collection_failed"
		}
		_, e = repo.SubmitDiscovery(ctx, token, input)
		must(e)
	}
	run([]collector.DiscoveryTable{table}, "success")
	entries, e := repo.ListCatalogEntries(ctx, ws.ID, "table", "")
	must(e)
	if len(entries) != 1 {
		t.Fatal("no table")
	}
	assetID := entries[0].AssetID
	var checkID int64
	must(pool.QueryRow(ctx, `SELECT check_id FROM plume.catalog_check_bindings WHERE workspace_id=$1 AND asset_id=$2`, ws.ID, assetID).Scan(&checkID))
	detail, e := repo.CheckDetail(ctx, ws.ID, owner.ID, checkID)
	must(e)
	if len(detail.Evidence) != 1 || detail.Evidence[0].Catalog == nil || detail.CanSubmit {
		t.Fatal("automatic origin must await manual classification and a newer positive source run")
	}
	classify := func(category, level string) {
		t.Helper()
		classification, e := repo.GetAssetClassification(ctx, ws.ID, owner.ID, assetID)
		must(e)
		_, e = repo.UpdateAssetClassification(ctx, ws.ID, owner.ID, assetID, store.AssetClassificationInput{Revision: classification.Revision, Category: category, BusinessLevel: level, StatutoryCategory: "general", Basis: "confirmed using business purpose and retained source evidence"})
		must(e)
	}

	must(repo.QueueDiscoverySource(ctx, ws.ID, owner.ID, source.ID, source.Revision, "collect"))
	counter++
	beforeManual, e := repo.ClaimDiscovery(ctx, token, fmt.Sprintf("%032x", counter))
	must(e)
	classify("personal", "internal")
	if e = repo.SubmitCheck(ctx, ws.ID, owner.ID, checkID); !errors.Is(e, store.ErrEvidenceRequired) {
		t.Fatalf("old pre-manual source evidence accepted: %v", e)
	}

	_, e = repo.SubmitDiscovery(ctx, token, store.DiscoverySubmission{SourceID: source.ID, Revision: source.Revision, RunRef: beforeManual.RunRef, Status: "success", ObservedAt: time.Now().UTC(), Result: collector.DiscoveryResult{Tables: []collector.DiscoveryTable{table}, Coverage: collector.DiscoveryCoverage{Metadata: "observed", Content: "bounded_sample"}}})
	must(e)
	detail, e = repo.CheckDetail(ctx, ws.ID, owner.ID, checkID)
	must(e)
	if detail.CanSubmit {
		t.Fatal("collection leased before manual confirmation was accepted as fresh source evidence")
	}
	run([]collector.DiscoveryTable{table}, "success")
	detail, e = repo.CheckDetail(ctx, ws.ID, owner.ID, checkID)
	must(e)
	if !detail.CanSubmit {
		t.Fatal("new compatible positive source not submittable")
	}
	must(repo.SubmitCheck(ctx, ws.ID, owner.ID, checkID))
	detail, e = repo.CheckDetail(ctx, ws.ID, reviewer.ID, checkID)
	must(e)
	must(repo.ReviewCheck(ctx, ws.ID, reviewer.ID, checkID, detail.ReviewRevision, "changes", "refresh source after remediation"))
	stamp := time.Now().UTC()
	must(repo.AddEvidence(ctx, ws.ID, owner.ID, checkID, store.EvidenceInput{Kind: "remediation", SourceSystem: "classification governance", SourceRecord: "remediation-1", Summary: "confirmed classification and source access scope", ObservedAt: stamp}))
	must(repo.AddEvidence(ctx, ws.ID, owner.ID, checkID, store.EvidenceInput{Kind: "retest", SourceSystem: "manual", SourceRecord: "manual-retest", Summary: "cannot replace fresh collector evidence", ObservedAt: time.Now().UTC()}))
	if e = repo.SubmitCheck(ctx, ws.ID, owner.ID, checkID); !errors.Is(e, store.ErrEvidenceRequired) {
		t.Fatalf("manual retest bypassed source guard %v", e)
	}
	run(nil, "error")
	if e = repo.SubmitCheck(ctx, ws.ID, owner.ID, checkID); !errors.Is(e, store.ErrEvidenceRequired) {
		t.Fatalf("failed source accepted %v", e)
	}
	run([]collector.DiscoveryTable{table}, "success")
	must(repo.SubmitCheck(ctx, ws.ID, owner.ID, checkID))
	detail, e = repo.CheckDetail(ctx, ws.ID, reviewer.ID, checkID)
	must(e)
	if !detail.CanVerify {
		t.Fatal("fresh positive retest not reviewable")
	}
	must(repo.ReviewCheck(ctx, ws.ID, reviewer.ID, checkID, detail.ReviewRevision, "verified", "independent current source review"))
	sourceToken := fmt.Sprintf("%x", sha256.Sum256([]byte(slug+"-runtime")))
	runtimeSource, e := repo.CreateRuntimeSource(ctx, ws.ID, owner.ID, "OTLP fixture", "Catalog API", "otlp", sourceToken)
	must(e)
	now := time.Now().UTC()
	_, e = repo.DeliverRuntimeBatch(ctx, "otlp", sourceToken, fmt.Sprintf("%x", sha256.Sum256([]byte("cross-system-fixture"))), []runtimesecurity.Observation{{Method: "GET", Route: "/mysql", Status: 200, ObservedAt: now, DatabaseTargets: []runtimesecurity.DatabaseTarget{{System: "mysql", Source: source.Source, Database: "inventory", Table: "customers", Operation: "SELECT"}}}, {Method: "GET", Route: "/wrong-system", Status: 200, ObservedAt: now, DatabaseTargets: []runtimesecurity.DatabaseTarget{{System: "postgresql", Source: source.Source, Database: "inventory", Schema: "inventory", Table: "customers", Operation: "SELECT"}}}})
	must(e)
	_ = runtimeSource
	graph, e := repo.DataFlows(ctx, ws.ID, owner.ID, store.DataFlowFilter{})
	must(e)
	tableNode := "table:" + fmt.Sprint(assetID)
	edges := 0
	for _, edge := range graph.Edges {
		if edge.Relation == "database_target" && edge.To == tableNode {
			edges++
		}
	}
	if edges != 1 {
		t.Fatalf("cross-system or MySQL namespace canonicalization wrong edges=%d", edges)
	}
	investigation, e := repo.InvestigateDataFlows(ctx, ws.ID, owner.ID, store.DataFlowFilter{Focus: tableNode})
	must(e)
	if len(investigation.Items) != 1 || investigation.Items[0].Route != "/mysql" {
		t.Fatalf("table focus leaked wrong system: %+v", investigation.Items)
	}

	// A legacy managed-PG directory and a new native-PG source can share an alias.
	// Two different Asset IDs are ambiguous even when every table name matches.
	legacyConfig := store.ManagedPostgresInput{Host: "sample-db", Port: 5432, Database: "inventory", User: "reader", Schemas: []string{"inventory"}, IntervalMinutes: 15, Enabled: true, EncryptedPassword: []byte("fixture-ciphertext")}
	legacy, e := repo.CreateManagedPostgresJob(ctx, ws.ID, owner.ID, "pg-alias", fmt.Sprintf("%x", sha256.Sum256([]byte(slug+"-legacy"))), legacyConfig)
	must(e)
	must(repo.QueueManagedPostgresJob(ctx, ws.ID, owner.ID, legacy.ID))
	legacyClaim, e := repo.ClaimManagedPostgresJob(ctx, "catalog-pg-alias", time.Minute)
	must(e)
	if legacyClaim == nil || legacyClaim.JobID != legacy.ID {
		t.Fatal("legacy fixture lease mismatch")
	}
	_, e = repo.FinishManagedPostgresJob(ctx, *legacyClaim, store.AutomationDelivery{Digest: fmt.Sprintf("%x", sha256.Sum256([]byte("legacy-alias-facts"))), ObservedAt: time.Now().UTC(), Assets: []store.CollectedAsset{{SourceRef: "inventory.customers", Name: "customers"}}, PostgresTables: []collector.PostgreSQLTable{{Asset: collector.Asset{SourceRef: "inventory.customers", Name: "customers"}, Schema: "inventory", Columns: []collector.PostgreSQLColumn{{Name: "email", Type: "text", Ordinal: 1}}, Grants: []collector.PostgreSQLGrant{}, Warnings: []string{}}}})
	must(e)
	nativeToken := fmt.Sprintf("%x", sha256.Sum256([]byte(slug+"-native-pg")))
	native, e := repo.SaveDiscoverySource(ctx, ws.ID, owner.ID, 0, store.DiscoverySourceInput{Source: "pg-alias", Provider: "postgresql", System: "postgresql", Database: "inventory", Scopes: []string{"inventory"}, IntervalMinutes: 15, Enabled: true}, nativeToken)
	must(e)
	nativeClaim, e := repo.ClaimDiscovery(ctx, nativeToken, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	must(e)
	_, e = repo.SubmitDiscovery(ctx, nativeToken, store.DiscoverySubmission{SourceID: native.ID, Revision: native.Revision, RunRef: nativeClaim.RunRef, Status: "success", ObservedAt: time.Now().UTC(), Result: collector.DiscoveryResult{Tables: []collector.DiscoveryTable{table}, Coverage: collector.DiscoveryCoverage{Metadata: "observed", Content: "bounded_sample"}}})
	must(e)
	_, e = repo.DeliverRuntimeBatch(ctx, "otlp", sourceToken, fmt.Sprintf("%x", sha256.Sum256([]byte("ambiguous-pg-alias"))), []runtimesecurity.Observation{{Method: "GET", Route: "/ambiguous-pg", Status: 200, ObservedAt: time.Now().UTC(), DatabaseTargets: []runtimesecurity.DatabaseTarget{{System: "postgresql", Source: "pg-alias", Database: "inventory", Schema: "inventory", Table: "customers", Operation: "SELECT"}}}})
	must(e)
	graph, e = repo.DataFlows(ctx, ws.ID, owner.ID, store.DataFlowFilter{})
	must(e)
	ambiguous := false
	for _, warning := range graph.Warnings {
		if warning == "ambiguous_database_target" {
			ambiguous = true
		}
	}
	if !ambiguous {
		t.Fatal("PG legacy/native alias was not marked ambiguous")
	}
	for _, edge := range graph.Edges {
		if edge.Relation == "database_target" && edge.To != tableNode {
			t.Fatal("ambiguous PG alias produced a precise edge")
		}
	}
	report, e := repo.ComplianceReport(ctx, ws.ID, owner.ID, 0)
	must(e)
	if report.CatalogDiscovery == nil || len(report.CatalogDiscovery.Assessments) == 0 {
		t.Fatal("catalog assessment references missing from report")
	}
	table.Fields[0].Hits = append(table.Fields[0].Hits, collector.DiscoveryHit{RuleKey: "builtin.financial_account", RuleVersion: 1, Category: "financial_account", Method: "source_aggregate", MatchCount: 1, ScannedCount: 1})
	run([]collector.DiscoveryTable{table}, "success")
	var newCheck int64
	must(pool.QueryRow(ctx, `SELECT max(check_id) FROM plume.catalog_check_bindings WHERE workspace_id=$1 AND asset_id=$2`, ws.ID, assetID).Scan(&newCheck))
	if newCheck == checkID {
		t.Fatal("verified check did not reopen on higher positive conflict")
	}
	classification, e := repo.GetAssetClassification(ctx, ws.ID, owner.ID, assetID)
	must(e)
	if classification.Asset.Category != "personal" || classification.Origin != "manual" {
		t.Fatal("source overrode manual classification")
	}
	revised := source.DiscoverySourceInput
	revised.Provider = "openmetadata"
	revised.Service = "service"
	_, e = repo.SaveDiscoverySource(ctx, ws.ID, owner.ID, source.ID, revised, "")
	if !errors.Is(e, store.ErrConflict) {
		t.Fatalf("provider identity switch must be rejected: %v", e)
	}
	run([]collector.DiscoveryTable{}, "success")
	investigation, e = repo.InvestigateDataFlows(ctx, ws.ID, owner.ID, store.DataFlowFilter{Focus: tableNode})
	must(e)
	if len(investigation.Items) > 0 {
		t.Fatal("omitted table remained investigable")
	}
	report, e = repo.ComplianceReport(ctx, ws.ID, owner.ID, 0)
	must(e)
	for _, evidence := range report.CatalogDiscovery.Assessments {
		if evidence.Assessment.AssetID == assetID && evidence.Current {
			t.Fatal("omitted source table evidence remained current in report")
		}
	}
	t.Log("verified canonical cross-system graph/focus, source-backed G02 evidence, fresh manual confirmation, remediation source retest, independent review, report assessment references, verified conflict reopening and immutable provider identity")
}
