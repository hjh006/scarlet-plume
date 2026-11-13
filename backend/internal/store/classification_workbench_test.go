package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClassificationWorkbenchReviewPublicationAndTaxonomy(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	want := func(got, expected error) {
		t.Helper()
		if !errors.Is(got, expected) {
			t.Fatalf("got %v want %v", got, expected)
		}
	}
	want(repo.Migrate(ctx), nil)
	slug := fmt.Sprintf("workbench-%d", time.Now().UnixNano())
	otherSlug := slug + "-other"
	names := []string{slug + "-owner", slug + "-operator", slug + "-reviewer", slug + "-viewer", slug + "-other"}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, e := pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, []string{slug, otherSlug})
		if e != nil {
			t.Error(e)
		}
		_, e = pool.Exec(cleanup, `DELETE FROM plume.users WHERE username=ANY($1)`, names)
		if e != nil {
			t.Error(e)
		}
	})
	want(repo.Bootstrap(ctx, names[0], "unused", slug, "Workbench"), nil)
	owner, e := repo.FindUser(ctx, names[0])
	want(e, nil)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	want(e, nil)
	member := func(name, role string) int64 {
		t.Helper()
		var id int64
		want(pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, name).Scan(&id), nil)
		_, e := pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,$3)`, ws.ID, id, role)
		want(e, nil)
		return id
	}
	operator, reviewer, viewer := member(names[1], "operator"), member(names[2], "reviewer"), member(names[3], "viewer")
	want(repo.Bootstrap(ctx, names[4], "unused", otherSlug, "Other"), nil)
	other, e := repo.FindUser(ctx, names[4])
	want(e, nil)
	otherWS, e := repo.Membership(ctx, other.ID, otherSlug)
	want(e, nil)
	a, e := repo.CreateAsset(ctx, ws.ID, "Customers", "CRM", "Data")
	want(e, nil)
	b, e := repo.CreateAsset(ctx, ws.ID, "Accounts", "Finance", "Data")
	want(e, nil)
	foreign, e := repo.CreateAsset(ctx, otherWS.ID, "Foreign", "CRM", "Data")
	want(e, nil)
	_, e = repo.ClassificationWorkbench(ctx, ws.ID, other.ID, store.ClassificationWorkbenchFilter{})
	want(e, store.ErrNotFound)
	_, e = repo.SaveClassificationTaxonomy(ctx, ws.ID, viewer, 0, store.ClassificationTaxonomyInput{Name: "Rejected", Enabled: true})
	want(e, store.ErrForbidden)
	parent, e := repo.SaveClassificationTaxonomy(ctx, ws.ID, owner.ID, 0, store.ClassificationTaxonomyInput{Name: "客户数据", Enabled: true})
	want(e, nil)
	child, e := repo.SaveClassificationTaxonomy(ctx, ws.ID, operator, 0, store.ClassificationTaxonomyInput{Name: "联系方式", ParentID: &parent.ID, Enabled: true})
	want(e, nil)
	if child.Path != "客户数据 / 联系方式" {
		t.Fatalf("bad taxonomy path %+v", child)
	}
	_, e = repo.SaveClassificationTaxonomy(ctx, ws.ID, owner.ID, parent.ID, store.ClassificationTaxonomyInput{Name: parent.Name, ParentID: &child.ID, Enabled: true, Revision: parent.Revision})
	want(e, store.ErrInvalid)
	_, e = repo.SaveClassificationTaxonomy(ctx, ws.ID, owner.ID, child.ID, store.ClassificationTaxonomyInput{Name: "stale", Enabled: true, Revision: 0})
	want(e, store.ErrConflict)
	_, e = repo.SaveClassificationTaxonomy(ctx, otherWS.ID, other.ID, 0, store.ClassificationTaxonomyInput{Name: "Cross tenant", ParentID: &parent.ID, Enabled: true})
	want(e, store.ErrInvalid)
	batch := store.ClassificationBatchReviewInput{Items: []store.ClassificationAssetRevision{{AssetID: b.ID, Revision: 0}, {AssetID: a.ID, Revision: 0}}, Category: "personal", BusinessLevel: "confidential", StatutoryCategory: "important", Basis: "Reviewed scope", StandardID: "education-jyt-0661-2025", RuleID: "education-l4-scale", LevelCode: "l4", TaxonomyID: &child.ID}
	_, e = repo.BatchReviewAssetClassifications(ctx, ws.ID, reviewer, batch)
	want(e, store.ErrForbidden)
	bad := batch
	bad.Items = []store.ClassificationAssetRevision{{AssetID: a.ID, Revision: 0}, {AssetID: b.ID, Revision: 9}}
	_, e = repo.BatchReviewAssetClassifications(ctx, ws.ID, operator, bad)
	want(e, store.ErrConflict)
	unchanged, e := repo.GetAssetClassification(ctx, ws.ID, owner.ID, a.ID)
	want(e, nil)
	if unchanged.Revision != 0 || len(unchanged.History) != 0 {
		t.Fatal("batch conflict partially committed")
	}
	bad = batch
	bad.Items = []store.ClassificationAssetRevision{{AssetID: a.ID, Revision: 0}, {AssetID: foreign.ID, Revision: 0}}
	_, e = repo.BatchReviewAssetClassifications(ctx, ws.ID, operator, bad)
	want(e, store.ErrNotFound)
	bad = batch
	bad.Items = []store.ClassificationAssetRevision{{AssetID: a.ID, Revision: 0}, {AssetID: a.ID, Revision: 0}}
	_, e = repo.BatchReviewAssetClassifications(ctx, ws.ID, operator, bad)
	want(e, store.ErrInvalid)
	details, e := repo.BatchReviewAssetClassifications(ctx, ws.ID, operator, batch)
	want(e, nil)
	if len(details) != 2 || details[0].Revision != 1 || details[0].Taxonomy == nil || details[0].History[0].Taxonomy.Path != child.Path {
		t.Fatalf("lost review snapshots %+v", details)
	}
	page, e := repo.ClassificationWorkbench(ctx, ws.ID, viewer, store.ClassificationWorkbenchFilter{Page: 1, PageSize: 1, System: "CRM", TaxonomyID: parent.ID, Status: "reviewed"})
	want(e, nil)
	if page.Total != 1 || len(page.Items) != 1 || page.Counts.Total != 2 || page.Counts.Reviewed != 2 || page.Counts.Unpublished != 2 || len(page.Facets.System) != 2 {
		t.Fatalf("wrong filtered/global totals %+v", page)
	}
	// Attach the reviewed table to an authorized, current collector run. No
	// source data is read; these are disposable ledger observations only.
	source, e := repo.SaveDiscoverySource(ctx, ws.ID, owner.ID, 0, store.DiscoverySourceInput{Source: "warehouse", Provider: "mysql", System: "mysql", Database: "fixture", IntervalMinutes: 15, Enabled: true}, fmt.Sprintf("%064x", b.ID))
	want(e, nil)
	var run1 int64
	want(pool.QueryRow(ctx, `INSERT INTO plume.discovery_runs(workspace_id,source_id,source_revision,run_ref,mode,status,observed_at,payload_digest) VALUES($1,$2,1,'first','collect','success',clock_timestamp(),'fixture') RETURNING id`, ws.ID, source.ID).Scan(&run1), nil)
	_, e = pool.Exec(ctx, `UPDATE plume.discovery_sources SET latest_run_id=$2 WHERE id=$1`, source.ID, run1)
	want(e, nil)
	_, e = pool.Exec(ctx, `INSERT INTO plume.catalog_entries(workspace_id,asset_id,kind,source,source_ref,source_id,run_id,source_revision,system,database_name,table_name,metadata) VALUES($1,$2,'table','warehouse','accounts',$3,$4,1,'Finance','fixture','accounts','{"comment":"same facts"}')`, ws.ID, b.ID, source.ID, run1)
	want(e, nil)
	baselineRules, _ := json.Marshal(collector.BuiltinDiscoveryRules())
	_, e = pool.Exec(ctx, `INSERT INTO plume.catalog_classification_assessments(workspace_id,source_id,source_revision,run_id,asset_id,classification_revision,fact_digest,assessment) VALUES($1,$2,1,$3,$4,1,'fixture',jsonb_build_object('appliedRules',$5::jsonb))`, ws.ID, source.ID, run1, b.ID, baselineRules)
	want(e, nil)
	req := store.ClassificationPublicationInput{Title: "Reviewed catalog v1", Note: "Frozen scope", Items: []store.ClassificationAssetRevision{{AssetID: a.ID, Revision: 1}, {AssetID: b.ID, Revision: 1}}}
	_, e = repo.CreateClassificationPublication(ctx, ws.ID, viewer, req)
	want(e, store.ErrForbidden)
	publication, e := repo.CreateClassificationPublication(ctx, ws.ID, owner.ID, req)
	want(e, nil)
	if publication.Status != "submitted" || len(publication.Items) != 2 || publication.Creator != names[0] || publication.SnapshotDigest == "" || publication.Items[0].StandardDefinition == nil {
		t.Fatalf("bad submission %+v", publication)
	}
	frozen, _ := json.Marshal(publication.Items)
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, owner.ID, publication.ID, store.ClassificationPublicationReviewInput{Action: "publish"})
	want(e, store.ErrForbidden)
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, operator, publication.ID, store.ClassificationPublicationReviewInput{Action: "publish"})
	want(e, store.ErrForbidden)
	_, e = repo.GetClassificationPublication(ctx, otherWS.ID, other.ID, publication.ID)
	want(e, store.ErrNotFound)
	published, e := repo.ReviewClassificationPublication(ctx, ws.ID, reviewer, publication.ID, store.ClassificationPublicationReviewInput{Action: "publish", Note: "Independent review"})
	want(e, nil)
	if published.Status != "published" || published.Reviewer != names[2] || published.ReviewedAt == nil {
		t.Fatalf("bad independent publication %+v", published)
	}
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, reviewer, publication.ID, store.ClassificationPublicationReviewInput{Action: "publish"})
	want(e, store.ErrConflict)
	page, e = repo.ClassificationWorkbench(ctx, ws.ID, viewer, store.ClassificationWorkbenchFilter{})
	want(e, nil)
	if page.Counts.Unpublished != 0 || page.Counts.Changed != 0 || page.Items[0].PublicationState != "published" {
		t.Fatalf("published counts wrong %+v", page)
	}
	// Identical polling may advance run IDs, catalog revisions and observation
	// times without changing an immutable classification result.
	var run2 int64
	want(pool.QueryRow(ctx, `INSERT INTO plume.discovery_runs(workspace_id,source_id,source_revision,run_ref,mode,status,observed_at,payload_digest) VALUES($1,$2,1,'second','collect','success',clock_timestamp(),'fixture') RETURNING id`, ws.ID, source.ID).Scan(&run2), nil)
	_, e = pool.Exec(ctx, `UPDATE plume.discovery_sources SET latest_run_id=$2 WHERE id=$1`, source.ID, run2)
	want(e, nil)
	_, e = pool.Exec(ctx, `UPDATE plume.catalog_entries SET run_id=$3,revision=revision+1,observed_at=clock_timestamp() WHERE workspace_id=$1 AND asset_id=$2`, ws.ID, b.ID, run2)
	want(e, nil)
	page, e = repo.ClassificationWorkbench(ctx, ws.ID, viewer, store.ClassificationWorkbenchFilter{Kind: "table"})
	want(e, nil)
	if page.Total != 1 || page.Items[0].PublicationState != "published" || page.Items[0].NeedsReview || page.Counts.Changed != 0 {
		t.Fatalf("identical poll invalidated publication %+v", page)
	}
	factSubmission, e := repo.CreateClassificationPublication(ctx, ws.ID, owner.ID, req)
	want(e, nil)
	_, e = pool.Exec(ctx, `UPDATE plume.catalog_entries SET metadata='{"comment":"changed source facts"}' WHERE workspace_id=$1 AND asset_id=$2`, ws.ID, b.ID)
	want(e, nil)
	page, e = repo.ClassificationWorkbench(ctx, ws.ID, viewer, store.ClassificationWorkbenchFilter{Status: "changed"})
	want(e, nil)
	if page.Total != 1 || page.Items[0].Asset.ID != b.ID || page.Counts.Changed != 1 {
		t.Fatalf("source fact change not detected %+v", page)
	}
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, reviewer, factSubmission.ID, store.ClassificationPublicationReviewInput{Action: "publish"})
	want(e, store.ErrConflict)
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, reviewer, factSubmission.ID, store.ClassificationPublicationReviewInput{Action: "reject"})
	want(e, nil)
	_, e = pool.Exec(ctx, `UPDATE plume.catalog_entries SET metadata='{"comment":"same facts"}' WHERE workspace_id=$1 AND asset_id=$2`, ws.ID, b.ID)
	want(e, nil)
	// Authorization and freshness are rechecked even for first submissions.
	assertSourceBlocked := func() {
		t.Helper()
		page, e := repo.ClassificationWorkbench(ctx, ws.ID, viewer, store.ClassificationWorkbenchFilter{Kind: "table"})
		want(e, nil)
		if len(page.Items) != 1 || !page.Items[0].NeedsReview || page.Items[0].Catalog.Current || page.Items[0].PublicationState != "changed" {
			t.Fatalf("untrusted source remained current %+v", page)
		}
		_, e = repo.CreateClassificationPublication(ctx, ws.ID, owner.ID, req)
		want(e, store.ErrConflict)
	}
	_, e = pool.Exec(ctx, `UPDATE plume.discovery_sources SET enabled=false WHERE id=$1`, source.ID)
	want(e, nil)
	assertSourceBlocked()
	_, e = pool.Exec(ctx, `UPDATE plume.discovery_sources SET enabled=true WHERE id=$1`, source.ID)
	want(e, nil)
	_, e = pool.Exec(ctx, `UPDATE plume.memberships SET role='operator' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, owner.ID)
	want(e, nil)
	assertSourceBlocked()
	_, e = pool.Exec(ctx, `UPDATE plume.memberships SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, owner.ID)
	want(e, nil)
	_, e = pool.Exec(ctx, `UPDATE plume.discovery_sources SET revision=revision+1 WHERE id=$1`, source.ID)
	want(e, nil)
	assertSourceBlocked()
	_, e = pool.Exec(ctx, `UPDATE plume.discovery_sources SET revision=1 WHERE id=$1`, source.ID)
	want(e, nil)
	_, e = pool.Exec(ctx, `UPDATE plume.discovery_runs SET status='error' WHERE id=$1`, run2)
	want(e, nil)
	assertSourceBlocked()
	_, e = pool.Exec(ctx, `UPDATE plume.discovery_runs SET status='success' WHERE id=$1`, run2)
	want(e, nil)
	_, e = repo.SaveDiscoveryRule(ctx, ws.ID, owner.ID, collector.DiscoveryRule{Key: "customer.new-version", Version: 1, Name: "New scope", Kind: "keyword", Target: "metadata", Category: "business", Expression: "department", Status: "published"})
	want(e, nil)
	page, e = repo.ClassificationWorkbench(ctx, ws.ID, viewer, store.ClassificationWorkbenchFilter{Kind: "table"})
	want(e, nil)
	if len(page.Items) != 1 || !page.Items[0].NeedsReview || page.Items[0].PublicationState != "changed" {
		t.Fatalf("new published rule reused old source observations %+v", page)
	}
	_, e = repo.CreateClassificationPublication(ctx, ws.ID, owner.ID, req)
	want(e, store.ErrConflict)
	_, e = pool.Exec(ctx, `UPDATE plume.discovery_rule_versions SET status='retired' WHERE workspace_id=$1 AND rule_key='customer.new-version'`, ws.ID)
	want(e, nil)
	submitted, e := repo.CreateClassificationPublication(ctx, ws.ID, owner.ID, req)
	want(e, nil)
	updated, e := repo.UpdateAssetClassification(ctx, ws.ID, operator, a.ID, store.AssetClassificationInput{Revision: 1, Category: batch.Category, BusinessLevel: "confidential", StatutoryCategory: batch.StatutoryCategory, Basis: "Revised scope"})
	want(e, nil)
	if updated.Taxonomy == nil || updated.Taxonomy.ID != child.ID {
		t.Fatal("omitted taxonomy did not preserve")
	}
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, reviewer, submitted.ID, store.ClassificationPublicationReviewInput{Action: "publish"})
	want(e, store.ErrConflict)
	rejected, e := repo.ReviewClassificationPublication(ctx, ws.ID, reviewer, submitted.ID, store.ClassificationPublicationReviewInput{Action: "reject", Note: "Needs fresh confirmation"})
	want(e, nil)
	if rejected.Status != "rejected" {
		t.Fatal("reject did not persist")
	}
	parent, e = repo.SaveClassificationTaxonomy(ctx, ws.ID, owner.ID, parent.ID, store.ClassificationTaxonomyInput{Name: "已改名客户数据", Enabled: true, Revision: parent.Revision})
	want(e, nil)
	page, e = repo.ClassificationWorkbench(ctx, ws.ID, viewer, store.ClassificationWorkbenchFilter{Status: "changed"})
	want(e, nil)
	if page.Total != 2 || page.Counts.Changed != 2 || !page.Items[0].TaxonomyStale || !page.Items[1].TaxonomyStale {
		t.Fatalf("ancestor rename did not stale snapshots %+v", page)
	}
	historical, e := repo.GetClassificationPublication(ctx, ws.ID, viewer, publication.ID)
	want(e, nil)
	after, _ := json.Marshal(historical.Items)
	if string(frozen) != string(after) {
		t.Fatal("publication changed after live edits")
	}
	oldB, e := repo.GetAssetClassification(ctx, ws.ID, viewer, b.ID)
	want(e, nil)
	if oldB.Taxonomy.Path != child.Path || !oldB.TaxonomyStale {
		t.Fatal("taxonomy snapshot was overwritten")
	}
	zero := int64(0)
	cleared, e := repo.UpdateAssetClassification(ctx, ws.ID, operator, b.ID, store.AssetClassificationInput{Revision: 1, Category: batch.Category, BusinessLevel: batch.BusinessLevel, StatutoryCategory: batch.StatutoryCategory, Basis: "Clear business category", TaxonomyID: &zero})
	want(e, nil)
	if cleared.Taxonomy != nil || cleared.History[0].Taxonomy != nil || cleared.History[1].Taxonomy == nil {
		t.Fatal("explicit taxonomy clear lost history")
	}
	_, e = pool.Exec(ctx, `UPDATE plume.memberships SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, operator)
	want(e, nil)
	// This independent-review fixture has no automatic evaluation; refresh its
	// revision binding to model the next collector observation after clearing.
	_, e = pool.Exec(ctx, `UPDATE plume.catalog_classification_assessments SET classification_revision=2 WHERE workspace_id=$1 AND asset_id=$2`, ws.ID, b.ID)
	want(e, nil)
	selfReq := store.ClassificationPublicationInput{Title: "Confirmers cannot approve", Items: []store.ClassificationAssetRevision{{AssetID: b.ID, Revision: 2}}}
	selfPub, e := repo.CreateClassificationPublication(ctx, ws.ID, owner.ID, selfReq)
	want(e, nil)
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, operator, selfPub.ID, store.ClassificationPublicationReviewInput{Action: "publish"})
	want(e, store.ErrForbidden)
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, operator, selfPub.ID, store.ClassificationPublicationReviewInput{Action: "reject"})
	want(e, store.ErrForbidden)
	deepest := child
	for i := 0; i < 3; i++ {
		deepest, e = repo.SaveClassificationTaxonomy(ctx, ws.ID, owner.ID, 0, store.ClassificationTaxonomyInput{Name: fmt.Sprintf("level-%d", i), ParentID: &deepest.ID, Enabled: true})
		want(e, nil)
	}
	_, e = repo.SaveClassificationTaxonomy(ctx, ws.ID, owner.ID, 0, store.ClassificationTaxonomyInput{Name: "too deep", ParentID: &deepest.ID, Enabled: true})
	want(e, store.ErrInvalid)
	_, e = pool.Exec(ctx, `INSERT INTO plume.classification_taxonomy(workspace_id,name,updated_by) SELECT $1,'category-'||n,$2 FROM generate_series(1,195) n`, ws.ID, owner.ID)
	want(e, nil)
	_, e = repo.SaveClassificationTaxonomy(ctx, ws.ID, owner.ID, 0, store.ClassificationTaxonomyInput{Name: "over limit", Enabled: true})
	want(e, store.ErrInvalid)
	list, e := repo.ListClassificationPublications(ctx, ws.ID, viewer)
	want(e, nil)
	if len(list) != 4 || list[0].Items != nil {
		t.Fatalf("publication list metadata contract wrong %+v", list)
	}
	// Historical publications outlive the operational asset ledger. Pending
	// submissions cannot be published after their live scope has disappeared.
	_, e = pool.Exec(ctx, `DELETE FROM plume.assets WHERE workspace_id=$1 AND id=ANY($2)`, ws.ID, []int64{a.ID, b.ID})
	want(e, nil)
	retained, e := repo.GetClassificationPublication(ctx, ws.ID, viewer, publication.ID)
	want(e, nil)
	retainedBytes, _ := json.Marshal(retained.Items)
	if len(retained.Items) != 2 || string(retainedBytes) != string(frozen) {
		t.Fatal("deleting live assets erased or rewrote the frozen manifest")
	}
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, reviewer, selfPub.ID, store.ClassificationPublicationReviewInput{Action: "publish"})
	want(e, store.ErrConflict)
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, operator, selfPub.ID, store.ClassificationPublicationReviewInput{Action: "reject"})
	want(e, store.ErrForbidden)
	_, e = repo.ReviewClassificationPublication(ctx, ws.ID, reviewer, selfPub.ID, store.ClassificationPublicationReviewInput{Action: "reject"})
	want(e, nil)
}
