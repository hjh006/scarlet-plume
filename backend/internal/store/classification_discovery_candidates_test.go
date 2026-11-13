package store_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDiscoveredClassificationCandidatesAndFreshness(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("discovery-candidates-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Discovery candidates"))
	user, err := repo.FindUser(ctx, slug)
	must(err)
	ws, err := repo.Membership(ctx, user.ID, slug)
	must(err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(cleanup, `DELETE FROM plume.users WHERE id=$1`, user.ID)
	})
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(slug)))
	source, err := repo.SaveDiscoverySource(ctx, ws.ID, user.ID, 0, store.DiscoverySourceInput{Source: "synthetic-warehouse", Provider: "mysql", System: "mysql", Database: "inventory", IntervalMinutes: 15, Enabled: true}, token)
	must(err)
	_, err = repo.SaveDiscoveryRule(ctx, ws.ID, user.ID, collector.DiscoveryRule{Key: "customer.project", Version: 1, Name: "合成项目词典", Kind: "dictionary", Target: "content", Category: "project_secret", Dictionary: []string{"synthetic-project"}, Status: "published"})
	must(err)
	fields := []collector.DiscoveryField{}
	for index := range 25 {
		fields = append(fields, collector.DiscoveryField{Name: fmt.Sprintf("contact_%02d_email", index), Type: "text", Sensitivity: "email", Hits: []collector.DiscoveryHit{{RuleKey: "builtin.email", RuleVersion: 1, Category: "email", Method: "bounded_customer_sample", MatchCount: 1, ScannedCount: 1}}})
	}
	fields = append(fields, collector.DiscoveryField{Name: "project_code", Type: "text", Hits: []collector.DiscoveryHit{{RuleKey: "customer.project", RuleVersion: 1, Category: "project_secret", Method: "bounded_customer_sample", MatchCount: 1, ScannedCount: 1}}})
	table := collector.DiscoveryTable{Database: "inventory", Schema: "inventory", Name: "customers", Fields: fields, PermissionsStatus: "unknown", ScannedRows: 1}
	counter := 0
	run := func(status string) store.DiscoveryRun {
		t.Helper()
		must(repo.QueueDiscoverySource(ctx, ws.ID, user.ID, source.ID, source.Revision, "collect"))
		counter++
		claim, err := repo.ClaimDiscovery(ctx, token, fmt.Sprintf("%032x", counter))
		must(err)
		if claim == nil {
			t.Fatal("missing synthetic lease")
		}
		input := store.DiscoverySubmission{SourceID: source.ID, Revision: source.Revision, RunRef: claim.RunRef, Status: status, ObservedAt: time.Now().UTC(), Result: collector.DiscoveryResult{Tables: []collector.DiscoveryTable{table}, Coverage: collector.DiscoveryCoverage{Metadata: "observed", Content: "bounded_sample", Permissions: "unknown", Lineage: "unknown"}}}
		if status == "error" {
			input.ErrorCode = "collection_failed"
		}
		result, err := repo.SubmitDiscovery(ctx, token, input)
		must(err)
		return result
	}
	firstRun := run("success")
	entries, err := repo.ListCatalogEntries(ctx, ws.ID, "table", "")
	must(err)
	if len(entries) != 1 {
		t.Fatal("synthetic table missing")
	}
	classification, err := repo.GetAssetClassification(ctx, ws.ID, user.ID, entries[0].AssetID)
	must(err)
	if classification.CandidateCount != 51 || len(classification.Candidates) != 20 || classification.SuggestedCategory != "personal" || classification.Automatic == nil || classification.Automatic.Stale || classification.Automatic.RunID != firstRun.RunRef {
		t.Fatalf("table candidates missing, capped incorrectly or stale: %+v", classification.AssetClassificationItem)
	}
	for _, candidate := range classification.Candidates {
		if candidate.Source != "discovery:mysql" || candidate.SourceID == nil || *candidate.SourceID != source.ID || candidate.Current == nil || !*candidate.Current || candidate.RunID != firstRun.RunRef {
			t.Fatalf("candidate lost source provenance: %+v", candidate)
		}
	}
	fieldEntries, err := repo.ListCatalogEntries(ctx, ws.ID, "field", "")
	must(err)
	var emailAsset, projectAsset int64
	for _, entry := range fieldEntries {
		if entry.Field == "contact_00_email" {
			emailAsset = entry.AssetID
		}
		if entry.Field == "project_code" {
			projectAsset = entry.AssetID
		}
	}
	field, err := repo.GetAssetClassification(ctx, ws.ID, user.ID, emailAsset)
	must(err)
	if field.CandidateCount != 2 || field.Automatic == nil || field.Automatic.Stale {
		t.Fatalf("individual field evidence missing: %+v", field.AssetClassificationItem)
	}
	foundRule := false
	for _, candidate := range field.Candidates {
		if candidate.RuleKey == "builtin.email" && candidate.RuleVersion == 1 && candidate.MatchCount != nil && *candidate.MatchCount == 1 && candidate.ScannedCount != nil && *candidate.ScannedCount == 1 {
			foundRule = true
		}
	}
	if !foundRule {
		t.Fatal("field rule/version/counts missing")
	}
	project, err := repo.GetAssetClassification(ctx, ws.ID, user.ID, projectAsset)
	must(err)
	if project.CandidateCount != 1 || project.Candidates[0].Category != "project_secret" || project.SuggestedCategory != "" || project.Asset.Category != "unknown" || project.Automatic != nil && project.Automatic.Evaluation.AutoApplicable {
		t.Fatalf("custom category was lost or automatically promoted: %+v", project.AssetClassificationItem)
	}
	field, err = repo.UpdateAssetClassification(ctx, ws.ID, user.ID, emailAsset, store.AssetClassificationInput{Revision: field.Revision, Category: "personal", BusinessLevel: "internal", StatutoryCategory: "general", Basis: "合成测试人工确认"})
	must(err)
	if field.Automatic == nil || !field.Automatic.Stale {
		t.Fatal("manual field revision reused old collection")
	}
	secondRun := run("success")
	field, err = repo.GetAssetClassification(ctx, ws.ID, user.ID, emailAsset)
	must(err)
	if field.Automatic == nil || field.Automatic.Stale || field.Automatic.Comparison != "matched" || field.Automatic.ClassificationRevision != field.Revision || field.Automatic.RunID != secondRun.RunRef {
		t.Fatalf("new poll did not refresh field revision: %+v", field.Automatic)
	}
	run("error")
	field, err = repo.GetAssetClassification(ctx, ws.ID, user.ID, emailAsset)
	must(err)
	if field.Automatic == nil || !field.Automatic.Stale {
		t.Fatal("failed latest collection retained fresh automatic classification")
	}
	for _, candidate := range field.Candidates {
		if candidate.Current == nil || *candidate.Current {
			t.Fatal("stale source candidate marked current")
		}
	}
	if field.SuggestedCategory != "" {
		t.Fatal("stale source produced a current suggestion")
	}
}
