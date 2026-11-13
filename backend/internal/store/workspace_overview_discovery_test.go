package store_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOverviewCountsConfiguredDiscoveryAndOperationsSources(t *testing.T) {
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
	slug := fmt.Sprintf("overview-sources-%d", time.Now().UnixNano())
	foreignSlug := slug + "-foreign"
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Overview configured sources"))
	must(repo.Bootstrap(ctx, foreignSlug, "unused", foreignSlug, "Foreign overview sources"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	foreignOwner, e := repo.FindUser(ctx, foreignSlug)
	must(e)
	foreignWorkspace, e := repo.Membership(ctx, foreignOwner.ID, foreignSlug)
	must(e)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=ANY($1)`, []int64{ws.ID, foreignWorkspace.ID})
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, foreignOwner.ID})
	})
	digest := func(v string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(slug+v))) }
	_, e = repo.CreateAutomationJob(ctx, ws.ID, owner.ID, store.AutomationJobInput{Provider: "postgresql", Source: "legacy"}, digest("legacy"))
	must(e)
	_, e = repo.CreateRuntimeSource(ctx, ws.ID, owner.ID, "runtime", "API", "gateway", digest("runtime"))
	must(e)
	createDiscovery := func(name string) {
		t.Helper()
		_, e := repo.SaveDiscoverySource(ctx, ws.ID, owner.ID, 0, store.DiscoverySourceInput{Source: name, Provider: "mysql", System: "mysql", Database: "discovery_sample", IntervalMinutes: 15, Enabled: false}, digest(name))
		must(e)
	}
	createDiscovery("paused-discovery")
	_, e = repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "security-events", Kind: "security_events", OwnerTeam: "security"}, digest("operations"))
	must(e)
	revoked, e := repo.CreateOperationsSource(ctx, ws.ID, owner.ID, store.OperationsSourceInput{Name: "revoked-events", Kind: "security_events", OwnerTeam: "security"}, digest("revoked"))
	must(e)
	must(repo.RevokeOperationsSource(ctx, ws.ID, owner.ID, revoked.ID))
	asset, e := repo.CreateAsset(ctx, ws.ID, "AI application", "support", "Data")
	must(e)
	application, e := repo.CreateApplicationSource(ctx, ws.ID, owner.ID, store.ApplicationSourceInput{Name: "application-api", AssetIDs: []int64{asset.ID}, MaxAgeMinutes: 60}, digest("application"))
	must(e)
	foreignAsset, e := repo.CreateAsset(ctx, foreignWorkspace.ID, "Foreign AI application", "support", "Data")
	must(e)
	foreignApplication, e := repo.CreateApplicationSource(ctx, foreignWorkspace.ID, foreignOwner.ID, store.ApplicationSourceInput{Name: "foreign-application-api", AssetIDs: []int64{foreignAsset.ID}}, digest("foreign-application"))
	must(e)
	counts, e := repo.OverviewCounts(ctx, ws.ID)
	must(e)
	if counts.ActiveSources != 5 {
		t.Fatalf("configured source count=%d want5", counts.ActiveSources)
	}
	sources, e := repo.OverviewSources(ctx, ws.ID)
	must(e)
	kinds := map[string]bool{}
	for _, source := range sources {
		kinds[source.Kind] = true
		if source.Name == "paused-discovery" && source.State != "paused" {
			t.Fatal("paused discovery state lost")
		}
		if source.Name == "revoked-events" {
			t.Fatal("revoked operations source leaked into preview")
		}
		if source.Kind == "application_api" && (source.ID != application.ID || source.Provider != "application_api" || source.State != "never_run") {
			t.Fatal("application source scope, family or initial status incorrect")
		}
	}
	if len(kinds) != 5 {
		t.Fatalf("missing source families: %v", kinds)
	}
	operations, e := repo.OperationsOverview(ctx, ws.ID, owner.ID)
	must(e)
	if operations.Counts.Sources != 5 {
		t.Fatal("operations and workspace source count denominators differ")
	}
	applicationState := func(want string) {
		t.Helper()
		preview, e := repo.OverviewSources(ctx, ws.ID)
		must(e)
		for _, source := range preview {
			if source.Kind == "application_api" && source.ID == application.ID {
				if source.Name != application.Name || source.State != want || source.LastObservedAt == nil || source.LastRunAt == nil {
					t.Fatalf("application preview got %+v want state %s", source, want)
				}
				return
			}
		}
		t.Fatal("application source missing from recent preview")
	}
	observed := time.Now().UTC()
	_, e = repo.ReceiveApplicationObservation(ctx, digest("application"), store.ApplicationSubmission{BatchID: "observed", Status: "success", ObservedAt: observed, Items: []store.ApplicationObservation{{AssetID: asset.ID, Facts: map[string]string{"application_model": "model-v1"}}}})
	must(e)
	applicationState("current")
	_, e = repo.ReceiveApplicationObservation(ctx, digest("application"), store.ApplicationSubmission{BatchID: "failed", Status: "error", ObservedAt: observed.Add(time.Second)})
	must(e)
	applicationState("failed")
	_, e = repo.ReceiveApplicationObservation(ctx, digest("application"), store.ApplicationSubmission{BatchID: "restored", Status: "success", ObservedAt: observed.Add(2 * time.Second), Items: []store.ApplicationObservation{{AssetID: asset.ID, Facts: map[string]string{"application_model": "model-v1"}}}})
	must(e)
	applicationState("current")
	_, e = pool.Exec(ctx, `UPDATE plume.application_batches SET observed_at=now()-interval '2 hours' WHERE source_id=$1`, application.ID)
	must(e)
	applicationState("stale")
	_, e = pool.Exec(ctx, `UPDATE plume.memberships SET role='reviewer' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, owner.ID)
	must(e)
	applicationState("revoked")
	_, e = pool.Exec(ctx, `UPDATE plume.memberships SET role='owner' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, owner.ID)
	must(e)
	must(repo.RevokeApplicationSource(ctx, ws.ID, owner.ID, application.ID))
	counts, e = repo.OverviewCounts(ctx, ws.ID)
	must(e)
	operations, e = repo.OperationsOverview(ctx, ws.ID, owner.ID)
	must(e)
	if counts.ActiveSources != 4 || operations.Counts.Sources != 4 {
		t.Fatal("revoked application source remained in configured source counts")
	}
	sources, e = repo.OverviewSources(ctx, ws.ID)
	must(e)
	for _, source := range sources {
		if source.Kind == "application_api" {
			t.Fatal("revoked or foreign application source remained in preview")
		}
	}
	foreignPreview, e := repo.OverviewSources(ctx, foreignWorkspace.ID)
	must(e)
	if len(foreignPreview) != 1 || foreignPreview[0].Kind != "application_api" || foreignPreview[0].ID != foreignApplication.ID {
		t.Fatal("application preview crossed workspace boundary")
	}
	for i := range 4 {
		createDiscovery(fmt.Sprintf("extra-%d", i))
	}
	sources, e = repo.OverviewSources(ctx, ws.ID)
	must(e)
	if len(sources) != 6 {
		t.Fatalf("preview must remain bounded to6 got%d", len(sources))
	}
	foreign, e := repo.OverviewSources(ctx, ws.ID+1000000)
	must(e)
	if len(foreign) != 0 {
		t.Fatal("cross-workspace source leak")
	}
	t.Log("five configured source families included; application freshness, failure, owner role and revocation preserved; tenant isolation and six-item preview retained")
}
