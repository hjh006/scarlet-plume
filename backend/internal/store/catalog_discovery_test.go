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
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCatalogDiscoveryLifecycle(t *testing.T) {
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
	slug := fmt.Sprintf("discovery-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Discovery test"))
	user, e := repo.FindUser(ctx, slug)
	must(e)
	ws, e := repo.Membership(ctx, user.ID, slug)
	must(e)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(cleanup, `DELETE FROM plume.users WHERE id=$1`, user.ID)
	})
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(slug)))
	source, e := repo.SaveDiscoverySource(ctx, ws.ID, user.ID, 0, store.DiscoverySourceInput{Source: "warehouse", Provider: "mysql", System: "mysql", Database: "approved", IntervalMinutes: 15, Enabled: true}, token)
	must(e)
	rule := collector.DiscoveryRule{Key: "customer.business", Version: 1, Name: "业务字段", Kind: "keyword", Target: "metadata", Category: "business", Expression: "department", Status: "draft"}
	_, e = repo.SaveDiscoveryRule(ctx, ws.ID, user.ID, rule)
	must(e)
	must(repo.PublishDiscoveryRule(ctx, ws.ID, user.ID, rule.Key, 1))
	claim, e := repo.ClaimDiscovery(ctx, token, "11111111111111111111111111111111")
	must(e)
	if claim == nil || len(claim.Config.Rules) != len(collector.BuiltinDiscoveryRules())+1 {
		t.Fatal("published rules not claimed")
	}
	second, e := repo.ClaimDiscovery(ctx, token, "22222222222222222222222222222222")
	must(e)
	if second != nil {
		t.Fatal("double lease")
	}
	table := collector.DiscoveryTable{Database: "approved", Schema: "approved", Name: "customers", Fields: []collector.DiscoveryField{{Name: "department", Type: "varchar", Hits: []collector.DiscoveryHit{{RuleKey: rule.Key, RuleVersion: 1, Category: "business", Method: "metadata", MatchCount: 1, ScannedCount: 1}}}}, PermissionsStatus: "unknown", ScannedRows: 2}
	table.Fields = append(table.Fields, collector.DiscoveryField{Name: "email", Type: "text", Hits: []collector.DiscoveryHit{{RuleKey: "builtin.email", RuleVersion: 1, Category: "email", Method: "bounded_customer_sample", MatchCount: 2, ScannedCount: 2}}})
	submission := store.DiscoverySubmission{SourceID: source.ID, Revision: source.Revision, RunRef: claim.RunRef, Status: "success", ObservedAt: time.Now(), Result: collector.DiscoveryResult{Tables: []collector.DiscoveryTable{table}, Coverage: collector.DiscoveryCoverage{Metadata: "observed", Content: "bounded_sample", Permissions: "unknown", Lineage: "unknown"}}}
	run, e := repo.SubmitDiscovery(ctx, token, submission)
	must(e)
	replay, e := repo.SubmitDiscovery(ctx, token, submission)
	must(e)
	if replay.ID != run.ID {
		t.Fatal("replay duplicate")
	}
	targets, e := repo.CurrentCatalogFlowTargets(ctx, ws.ID)
	must(e)
	if len(targets) != 1 || targets[0].Table != "customers" || len(targets[0].Categories) != 2 {
		t.Fatalf("no current precise target %+v", targets)
	}
	otherTargets, e := repo.CurrentCatalogFlowTargets(ctx, ws.ID+10000000)
	must(e)
	if len(otherTargets) != 0 {
		t.Fatal("cross tenant targets")
	}
	entries, e := repo.ListCatalogEntries(ctx, ws.ID, "table", "")
	must(e)
	if len(entries) != 1 {
		t.Fatal("table catalog missing")
	}
	entry := entries[0]
	if entry.Category != "personal" {
		t.Fatalf("standard-based positive classification not applied: %s", entry.Category)
	}
	classified, e := repo.GetAssetClassification(ctx, ws.ID, user.ID, entry.AssetID)
	must(e)
	_, e = repo.UpdateAssetClassification(ctx, ws.ID, user.ID, entry.AssetID, store.AssetClassificationInput{Revision: classified.Revision, Category: "sensitive_personal", BusinessLevel: "confidential", StatutoryCategory: "general", Basis: "test confirmed higher category"})
	must(e)
	must(repo.ConfirmCatalogMetadata(ctx, ws.ID, user.ID, entry.ID, entry.Revision, map[string]string{"ownerTeam": "confirmed-team"}))
	cmdb := store.CMDBImport{Version: 1, Source: "cmdb", BatchID: "b1", Records: []store.CMDBRecord{{AssetID: entry.AssetID, SourceRef: "external", Kind: "table", Name: "customers", System: "mysql", Metadata: map[string]string{"ownerTeam": "incoming-team", "region": "CN"}}}}
	imported, e := repo.ImportCMDB(ctx, ws.ID, user.ID, cmdb)
	must(e)
	if imported.Conflicts != 1 {
		t.Fatal("confirmed conflict not retained")
	}
	imported, e = repo.ImportCMDB(ctx, ws.ID, user.ID, cmdb)
	must(e)
	if !imported.Replayed {
		t.Fatal("CMDB replay not idempotent")
	}
	entries, e = repo.ListCatalogEntries(ctx, ws.ID, "table", "")
	must(e)
	if entries[0].ConfirmedMetadata["ownerTeam"] != "confirmed-team" || entries[0].Metadata["region"] != "CN" {
		t.Fatal("confirmed metadata overwritten")
	}
	cmdb.Records[0].Metadata["region"] = "other"
	_, e = repo.ImportCMDB(ctx, ws.ID, user.ID, cmdb)
	if !errors.Is(e, store.ErrConflict) {
		t.Fatalf("same batch mutation accepted %v", e)
	}
	must(repo.QueueDiscoverySource(ctx, ws.ID, user.ID, source.ID, source.Revision, "collect"))
	claim, e = repo.ClaimDiscovery(ctx, token, "33333333333333333333333333333333")
	must(e)
	submission.RunRef = claim.RunRef
	submission.ObservedAt = time.Now()
	submission.Result.Tables = []collector.DiscoveryTable{table}
	submission.Result.Tables[0].Fields[1].Hits = []collector.DiscoveryHit{}
	_, e = repo.SubmitDiscovery(ctx, token, submission)
	must(e)
	entries, e = repo.ListCatalogEntries(ctx, ws.ID, "table", "")
	must(e)
	if entries[0].Category != "sensitive_personal" || entries[0].Metadata["region"] != "CN" {
		t.Fatal("negative sampling downgraded manual classification or discarded CMDB enrichment")
	}
	must(repo.QueueDiscoverySource(ctx, ws.ID, user.ID, source.ID, source.Revision, "collect"))
	claim, e = repo.ClaimDiscovery(ctx, token, "44444444444444444444444444444444")
	must(e)
	submission.RunRef = claim.RunRef
	submission.ObservedAt = time.Now()
	submission.Result.Tables = []collector.DiscoveryTable{}
	_, e = repo.SubmitDiscovery(ctx, token, submission)
	must(e)
	targets, e = repo.CurrentCatalogFlowTargets(ctx, ws.ID)
	must(e)
	if len(targets) != 0 {
		t.Fatal("omitted old table remained current")
	}
	source.DiscoverySourceInput.Database = "changed"
	_, e = repo.SaveDiscoverySource(ctx, ws.ID, user.ID, source.ID, source.DiscoverySourceInput, "")
	must(e)
	targets, e = repo.CurrentCatalogFlowTargets(ctx, ws.ID)
	must(e)
	if len(targets) != 0 {
		t.Fatal("revised source remained current")
	}
	history, e := repo.CatalogEntryHistory(ctx, ws.ID, entry.ID)
	must(e)
	if len(history) < 3 {
		t.Fatal("catalog revision history missing")
	}
	t.Log("verified positive standard classification, manual classification and negative-sample protections, source leases, published rules, aggregate ingestion, replay, workspace isolation, precise targets, omitted-table and revision invalidation, confirmed CMDB conflicts, revision history")
}
