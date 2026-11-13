package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type assessmentSourceFixture struct {
	repo *Store
	pool *pgxpool.Pool
	ws   Workspace
	user User
}

func assessmentSourceTestStore(t *testing.T) assessmentSourceFixture {
	t.Helper()
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := New(pool)
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	slug := fmt.Sprintf("assessment-source-%d", time.Now().UnixNano())
	if err := repo.Bootstrap(ctx, slug, "unused", slug, "Assessment source fixture"); err != nil {
		t.Fatal(err)
	}
	user, err := repo.FindUser(ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := repo.Membership(ctx, user.ID, slug)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM plume.users WHERE id=$1`, user.ID)
	})
	return assessmentSourceFixture{repo, pool, ws, user}
}

func (f assessmentSourceFixture) facts(t *testing.T, workspaceID, assetID int64) ([]AssessmentFact, error) {
	t.Helper()
	var facts []AssessmentFact
	err := pgx.BeginTxFunc(t.Context(), f.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		facts, err = collectedAssessmentFacts(t.Context(), tx, workspaceID, assetID)
		return err
	})
	return facts, err
}

func assessmentSourceFact(t *testing.T, facts []AssessmentFact, key string) AssessmentFact {
	t.Helper()
	for _, fact := range facts {
		if fact.Key == key {
			return fact
		}
	}
	t.Fatalf("missing %s", key)
	return AssessmentFact{}
}

func (f assessmentSourceFixture) discovery(t *testing.T, provider string) (*DiscoverySource, func(string, string, []collector.DiscoveryTable) DiscoveryRun) {
	t.Helper()
	ctx := t.Context()
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d/%s", f.ws.ID, provider))))
	system := provider
	if provider == "openmetadata" {
		system = "postgresql"
	}
	source, err := f.repo.SaveDiscoverySource(ctx, f.ws.ID, f.user.ID, 0, DiscoverySourceInput{
		Source: "assessment-" + provider, Provider: provider, System: system, Database: "inventory",
		Service: "approved-service", IntervalMinutes: 15, Enabled: true,
	}, token)
	if err != nil {
		t.Fatal(err)
	}
	counter := 0
	return &source, func(mode, status string, tables []collector.DiscoveryTable) DiscoveryRun {
		t.Helper()
		if err := f.repo.QueueDiscoverySource(ctx, f.ws.ID, f.user.ID, source.ID, source.Revision, mode); err != nil {
			t.Fatal(err)
		}
		counter++
		claim, err := f.repo.ClaimDiscovery(ctx, token, fmt.Sprintf("%032x", counter))
		if err != nil || claim == nil {
			t.Fatalf("claim failed: %v", err)
		}
		coverage := collector.DiscoveryCoverage{Metadata: "observed", Content: "bounded_sample", Permissions: "unknown", Lineage: "unknown", RowLimit: 200, TableLimit: 10}
		if provider == "openmetadata" {
			coverage.Content = "not_collected"
		}
		in := DiscoverySubmission{SourceID: source.ID, Revision: source.Revision, RunRef: claim.RunRef, Status: status, ObservedAt: time.Now().UTC(), Result: collector.DiscoveryResult{Tables: tables, Coverage: coverage}}
		if status == "error" {
			in.ErrorCode = "collection_failed"
		}
		run, err := f.repo.SubmitDiscovery(ctx, token, in)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
}

func TestCollectedAssessmentFactsDiscoveryPauseAndToken(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	source, run := f.discovery(t, "mysql")
	tables := []collector.DiscoveryTable{assessmentSourceTable("customers", "email")}
	run("collect", "success", tables)
	entries, err := f.repo.ListCatalogEntries(ctx, f.ws.ID, "table", "customers")
	if err != nil || len(entries) != 1 {
		t.Fatalf("catalog missing: %v", err)
	}
	assertState := func(state string) {
		t.Helper()
		facts, err := f.facts(t, f.ws.ID, entries[0].AssetID)
		if err != nil {
			t.Fatal(err)
		}
		if fact := assessmentSourceFact(t, facts, "source_coverage"); fact.State != state {
			t.Fatalf("got %+v want %s", fact, state)
		}
	}
	assertState("known")
	changed := source.DiscoverySourceInput
	changed.Enabled = false
	*source, err = f.repo.SaveDiscoverySource(ctx, f.ws.ID, f.user.ID, source.ID, changed, "")
	if err != nil {
		t.Fatal(err)
	}
	assertState("unknown")
	changed = source.DiscoverySourceInput
	changed.Enabled = true
	*source, err = f.repo.SaveDiscoverySource(ctx, f.ws.ID, f.user.ID, source.ID, changed, "")
	if err != nil {
		t.Fatal(err)
	}
	assertState("unknown")
	run("collect", "success", tables)
	assertState("known")
	if err := f.repo.RotateDiscoveryToken(ctx, f.ws.ID, f.user.ID, source.ID, source.Revision, fmt.Sprintf("%x", sha256.Sum256([]byte("rotated")))); err != nil {
		t.Fatal(err)
	}
	assertState("unknown")
}

func TestCollectedAssessmentFactsManagedPostgres(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	config := ManagedPostgresInput{Host: "sample-db", Port: 5432, Database: "inventory", User: "reader", Schemas: []string{"inventory"}, IntervalMinutes: 15, Enabled: true, EncryptedPassword: []byte("ciphertext-fixture")}
	job, err := f.repo.CreateManagedPostgresJob(ctx, f.ws.ID, f.user.ID, "approved-pg", fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(f.ws.ID)))), config)
	if err != nil {
		t.Fatal(err)
	}
	table := collector.PostgreSQLTable{Asset: collector.Asset{SourceRef: "inventory.customers", Name: "customers"}, Schema: "inventory", Owner: "reader", Columns: []collector.PostgreSQLColumn{{Name: "email", Type: "text", Ordinal: 1, Sensitivity: "email"}}}
	run := func(runID, failure string, include bool) {
		t.Helper()
		if err := f.repo.QueueManagedPostgresJob(ctx, f.ws.ID, f.user.ID, job.ID); err != nil {
			t.Fatal(err)
		}
		claim, err := f.repo.ClaimManagedPostgresJob(ctx, runID, time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("PG claim failed: %v", err)
		}
		in := AutomationDelivery{Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(runID))), ObservedAt: time.Now().UTC(), FailureCode: failure}
		if failure == "" {
			in.Assets = []CollectedAsset{}
		}
		if include {
			in.Assets, in.PostgresTables = []CollectedAsset{{SourceRef: table.SourceRef, Name: table.Name}}, []collector.PostgreSQLTable{table}
		}
		if _, err := f.repo.FinishManagedPostgresJob(ctx, *claim, in); err != nil {
			t.Fatal(err)
		}
	}
	run("initial", "", true)
	assets, err := f.repo.ListAssets(ctx, f.ws.ID, 0, 10)
	if err != nil || len(assets) != 1 {
		t.Fatalf("PG asset missing: %v", err)
	}
	assertState := func(state string) {
		t.Helper()
		facts, err := f.facts(t, f.ws.ID, assets[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		fact := assessmentSourceFact(t, facts, "source_observed_types")
		if fact.State != state || state == "known" && (fact.Source != "postgresql" || fact.Value != "email") {
			t.Fatalf("unexpected PG facts: %+v", fact)
		}
	}
	assertState("known")
	run("failed", "collection_failed", false)
	assertState("unknown")
	run("restored", "", true)
	assertState("known")
	run("omitted", "", false)
	assertState("unknown")
	run("returned", "", true)
	assertState("known")
	config.Host = "different-target"
	if _, err := f.repo.UpdateManagedPostgresJob(ctx, f.ws.ID, f.user.ID, job.ID, job.Managed.Revision, config); err != nil {
		t.Fatal(err)
	}
	assertState("unknown")
	run("changed-target", "", true)
	assertState("known")
	if err := f.repo.RevokeAutomationJob(ctx, f.ws.ID, f.user.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	assertState("unknown")
}

func assessmentSourceTable(name, category string) collector.DiscoveryTable {
	return collector.DiscoveryTable{
		Database: "inventory", Schema: "inventory", Name: name, PermissionsStatus: "unknown", ScannedRows: 1,
		Fields: []collector.DiscoveryField{{Name: "contact", Type: "text", Sensitivity: category,
			Hits: []collector.DiscoveryHit{{RuleKey: "builtin." + category, RuleVersion: 1, Category: category, Method: "bounded_customer_sample", MatchCount: 1, ScannedCount: 1}},
		}},
	}
}

func TestCollectedAssessmentFactsDiscoverySources(t *testing.T) {
	for _, provider := range []string{"mysql", "mariadb", "mongodb", "openmetadata"} {
		t.Run(provider, func(t *testing.T) {
			f := assessmentSourceTestStore(t)
			ctx := t.Context()
			source, run := f.discovery(t, provider)
			table := assessmentSourceTable("customers", "email")
			if provider == "mysql" || provider == "mariadb" {
				table.PermissionsStatus = "current_account_visible_grants"
				table.Permissions = []string{"GRANT SELECT ON inventory.* TO current_account"}
			}
			if provider == "openmetadata" {
				// An OpenMetadata label is metadata, never proof of source content or ACLs.
				table.ScannedRows, table.Fields[0].Hits = 0, nil
			}
			other := assessmentSourceTable("unrelated", "phone")
			firstRun := run("collect", "success", []collector.DiscoveryTable{table, other})
			entries, err := f.repo.ListCatalogEntries(ctx, f.ws.ID, "table", "customers")
			if err != nil || len(entries) != 1 {
				t.Fatalf("expected one customers table: %v", err)
			}
			entry := entries[0]
			read := func() []AssessmentFact {
				t.Helper()
				facts, err := f.facts(t, f.ws.ID, entry.AssetID)
				if err != nil {
					t.Fatal(err)
				}
				return facts
			}
			assertCurrent := func() {
				t.Helper()
				facts := read()
				types := assessmentSourceFact(t, facts, "source_observed_types")
				if types.State != "known" || types.Value != "email" || types.Source != "discovery:"+provider || types.ObservedAt == nil || !strings.Contains(types.Record, fmt.Sprintf("/asset:%d/", entry.AssetID)) {
					t.Fatalf("incorrect scope or provenance: %+v", types)
				}
				permissions := assessmentSourceFact(t, facts, "source_permissions")
				if provider == "mysql" || provider == "mariadb" {
					if permissions.State != "known" || !strings.Contains(permissions.Value, "current_account_visible_grants") {
						t.Fatalf("visible account grants missing: %+v", permissions)
					}
				} else if permissions.State != "unknown" || permissions.Value != "" {
					t.Fatal("uncollected source ACLs became known")
				}
				if provider == "openmetadata" && !strings.Contains(assessmentSourceFact(t, facts, "source_coverage").Value, `"content":"not_collected"`) {
					t.Fatal("OpenMetadata metadata asserted source content")
				}
			}
			assertUnknown := func() {
				t.Helper()
				facts := read()
				for _, key := range []string{"source_observed_types", "source_coverage"} {
					if fact := assessmentSourceFact(t, facts, key); fact.State != "unknown" || fact.Value != "" {
						t.Fatalf("stale collection remained usable: %+v", fact)
					}
				}
			}
			assertCurrent()
			if _, err := f.facts(t, f.ws.ID+1000000, entry.AssetID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("foreign workspace accepted: %v", err)
			}
			manual, err := f.repo.CreateAsset(ctx, f.ws.ID, "Unrelated manual asset", "other", "Data")
			if err != nil {
				t.Fatal(err)
			}
			manualFacts, err := f.facts(t, f.ws.ID, manual.ID)
			if err != nil || assessmentSourceFact(t, manualFacts, "source_observed_types").State != "unknown" {
				t.Fatal("workspace-wide source facts leaked into unrelated asset")
			}
			fields, err := f.repo.ListCatalogEntries(ctx, f.ws.ID, "field", "customers")
			if err != nil || len(fields) != 1 {
				t.Fatalf("missing scoped field: %v", err)
			}
			fieldFacts, err := f.facts(t, f.ws.ID, fields[0].AssetID)
			if err != nil || assessmentSourceFact(t, fieldFacts, "source_observed_types").Value != "email" || assessmentSourceFact(t, fieldFacts, "source_permissions").State != "unknown" {
				t.Fatal("field borrowed sibling categories or table ACLs")
			}
			if _, err := f.pool.Exec(ctx, `UPDATE plume.discovery_runs SET observed_at=now()-interval '2 hours' WHERE id=$1`, firstRun.ID); err != nil {
				t.Fatal(err)
			}
			assertUnknown()
			run("collect", "success", []collector.DiscoveryTable{table, other})
			assertCurrent()
			run("test", "error", nil)
			assertUnknown()
			// A successful test does not replace a failed collection or populate catalog facts.
			run("test", "success", []collector.DiscoveryTable{table, other})
			assertUnknown()
			run("collect", "success", []collector.DiscoveryTable{table, other})
			assertCurrent()
			run("collect", "success", []collector.DiscoveryTable{other})
			assertUnknown()
			run("collect", "success", []collector.DiscoveryTable{table, other})
			assertCurrent()
			if _, err := f.pool.Exec(ctx, `UPDATE plume.memberships SET role='reviewer' WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, f.user.ID); err != nil {
				t.Fatal(err)
			}
			assertUnknown()
			if _, err := f.pool.Exec(ctx, `UPDATE plume.memberships SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, f.user.ID); err != nil {
				t.Fatal(err)
			}
			assertCurrent()
			changed := source.DiscoverySourceInput
			changed.Database = "changed_target"
			if _, err := f.repo.SaveDiscoverySource(ctx, f.ws.ID, f.user.ID, source.ID, changed, ""); err != nil {
				t.Fatal(err)
			}
			assertUnknown()
		})
	}
}

func TestCollectedAssessmentFactsCatalogDeclarationsAndUnknowns(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	_, run := f.discovery(t, "mysql")
	run("collect", "success", []collector.DiscoveryTable{assessmentSourceTable("customers", "email")})
	entries, err := f.repo.ListCatalogEntries(ctx, f.ws.ID, "table", "customers")
	if err != nil || len(entries) != 1 {
		t.Fatalf("catalog missing: %v", err)
	}
	entry := entries[0]
	if err := f.repo.ConfirmCatalogMetadata(ctx, f.ws.ID, f.user.ID, entry.ID, entry.Revision, map[string]string{"ownerTeam": "confirmed-team"}); err != nil {
		t.Fatal(err)
	}
	_, err = f.repo.ImportCMDB(ctx, f.ws.ID, f.user.ID, CMDBImport{Version: 1, Source: "approved-cmdb", BatchID: "first", Records: []CMDBRecord{{
		AssetID: entry.AssetID, SourceRef: "customers", Kind: "table", Name: "customers", System: "mysql",
		Metadata: map[string]string{"ownerTeam": "incoming-team", "application": "customer-service", "region": "overseas", "retention": "30 days"},
	}, {
		SourceRef: "support-application", Kind: "application", Name: "Support application", System: "support",
		Metadata: map[string]string{"application": "support", "ownerTeam": "support-team"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := f.facts(t, f.ws.ID, entry.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	declaration := assessmentSourceFact(t, facts, "catalog_business_metadata")
	var values struct {
		Observed  map[string]string `json:"observed"`
		Confirmed map[string]string `json:"confirmed"`
		Conflicts map[string]any    `json:"conflicts"`
	}
	if err := json.Unmarshal([]byte(declaration.Value), &values); err != nil {
		t.Fatal(err)
	}
	if declaration.Source != "catalog_declared" || declaration.ObservedAt != nil || values.Confirmed["ownerTeam"] != "confirmed-team" || values.Observed["application"] != "customer-service" || values.Conflicts["ownerTeam"] == nil {
		t.Fatalf("catalog declaration provenance lost: %+v", declaration)
	}
	var assessmentFactsResult []AssessmentFact
	err = pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		var err error
		assessmentFactsResult, err = assessmentFacts(ctx, tx, f.ws.ID, Activity{ID: 1, ActivityInput: ActivityInput{AssetID: entry.AssetID, Kind: "ai", Purpose: "approved synthetic use"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"deduplicated_subject_count", "cii_status", "cross_border_exception", "public_ai_service", "automated_decision", "lawful_basis", "notice_consent"} {
		if fact := assessmentSourceFact(t, assessmentFactsResult, key); fact.State != "unknown" || fact.Value != "" {
			t.Fatalf("CMDB region/counts inferred business fact: %+v", fact)
		}
	}
	applications, err := f.repo.ListCatalogEntries(ctx, f.ws.ID, "application", "")
	if err != nil || len(applications) != 1 {
		t.Fatalf("CMDB application missing: %v", err)
	}
	application, err := f.facts(t, f.ws.ID, applications[0].AssetID)
	if err != nil || assessmentSourceFact(t, application, "catalog_business_metadata").Source != "catalog_declared" || assessmentSourceFact(t, application, "source_coverage").State != "unknown" {
		t.Fatal("CMDB declaration became observed source collection")
	}
}

func TestCollectedAssessmentFactsPreserveSubmittedSnapshot(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	_, run := f.discovery(t, "mongodb")
	run("collect", "success", []collector.DiscoveryTable{assessmentSourceTable("customers", "email")})
	entries, err := f.repo.ListCatalogEntries(ctx, f.ws.ID, "table", "customers")
	if err != nil || len(entries) != 1 {
		t.Fatalf("catalog missing: %v", err)
	}
	activity, err := f.repo.CreateActivity(ctx, f.ws.ID, f.user.ID, ActivityInput{AssetID: entries[0].AssetID, Name: "AI synthetic activity", Kind: "ai", Purpose: "approved synthetic processing"})
	if err != nil {
		t.Fatal(err)
	}
	templates, err := f.repo.ListAssessmentTemplates(ctx, f.ws.ID, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	for _, template := range templates {
		if template.Kind == "ai" {
			revision = template.ID
		}
	}
	assessment, err := f.repo.CreateAssessment(ctx, f.ws.ID, f.user.ID, AssessmentInput{ActivityID: activity.ID, TemplateRevisionID: revision, Title: "AI source snapshot", DueAt: time.Now().Add(24 * time.Hour), RefreshMinutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := f.repo.ReviewAssessment(ctx, f.ws.ID, f.user.ID, assessment.ID, AssessmentReviewInput{Revision: assessment.Revision, Action: "submit", Note: "Submit with explicit missing business facts"})
	if err != nil {
		t.Fatal(err)
	}
	old := assessmentSourceFact(t, submitted.Facts, "source_observed_types")
	run("collect", "success", []collector.DiscoveryTable{assessmentSourceTable("customers", "phone")})
	if _, err := f.pool.Exec(ctx, `UPDATE plume.assessments SET next_refresh_at=now()-interval '1 minute' WHERE id=$1`, assessment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.RefreshDueAssessments(ctx, 25); err != nil {
		t.Fatal(err)
	}
	current, err := f.repo.Assessment(ctx, f.ws.ID, f.user.ID, assessment.ID)
	if err != nil {
		t.Fatal(err)
	}
	retained := assessmentSourceFact(t, current.Facts, "source_observed_types")
	if !current.Stale || retained.Value != old.Value || retained.Record != old.Record || retained.Value != "email" {
		t.Fatalf("submitted source snapshot mutated: old=%+v retained=%+v", old, retained)
	}
}
