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
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Overview configured sources"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=$1`, owner.ID)
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
	counts, e := repo.OverviewCounts(ctx, ws.ID)
	must(e)
	if counts.ActiveSources != 4 {
		t.Fatalf("configured source count=%d want4", counts.ActiveSources)
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
	}
	if len(kinds) != 4 {
		t.Fatalf("missing source families: %v", kinds)
	}
	operations, e := repo.OperationsOverview(ctx, ws.ID, owner.ID)
	must(e)
	if operations.Counts.Sources != 4 {
		t.Fatal("operations and workspace source count denominators differ")
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
	t.Log("configured paused discovery and unrevoked operations included; revoked sources excluded; four-family preview remains bounded to six")
}
