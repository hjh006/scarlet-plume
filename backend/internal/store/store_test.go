package store_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgreSQLStore(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("could not configure test database")
	}
	// The store's transaction isolation must not depend on the server default.
	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = "repeatable read"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("could not configure test database")
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)

	// Two instances can apply the same migration concurrently.
	migrations := make(chan error, 2)
	for range 2 {
		go func() { migrations <- repo.Migrate(ctx) }()
	}
	for range 2 {
		if err := <-migrations; err != nil {
			t.Fatal(err)
		}
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	owner, outsider := "owner-"+suffix, "outsider-"+suffix
	slug, otherSlug := "workspace-"+suffix, "other-"+suffix
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{slug, otherSlug}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(cleanup, "DELETE FROM plume.users WHERE username = ANY($1)", []string{owner, outsider}); err != nil {
			t.Error(err)
		}
	})

	if err := repo.Bootstrap(ctx, owner, "original-hash", slug, "First workspace"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Bootstrap(ctx, owner, "new-hash", slug, "Renamed workspace"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Bootstrap(ctx, outsider, "other-hash", slug, "First workspace"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("bootstrap into existing workspace returned %v", err)
	}
	user, err := repo.FindUser(ctx, owner)
	if err != nil || user.PasswordHash != "original-hash" {
		t.Fatalf("bootstrap did not preserve the original credential: %v", err)
	}
	if _, err := repo.FindUser(ctx, outsider); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("conflicting bootstrap created an account: %v", err)
	}
	workspace, err := repo.Membership(ctx, user.ID, slug)
	if err != nil || workspace.Role != "owner" || workspace.Name != "First workspace" {
		t.Fatalf("unexpected workspace %+v: %v", workspace, err)
	}
	workspaces, err := repo.ListWorkspaces(ctx, user.ID)
	if err != nil || len(workspaces) != 1 || workspaces[0] != workspace {
		t.Fatalf("unexpected workspace list %+v: %v", workspaces, err)
	}
	if err := repo.Bootstrap(ctx, outsider, "other-hash", otherSlug, "Other workspace"); err != nil {
		t.Fatal(err)
	}
	other, err := repo.FindUser(ctx, outsider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Membership(ctx, other.ID, slug); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-workspace membership returned %v", err)
	}

	t.Run("sessions", func(t *testing.T) {
		token := "token-" + suffix
		if err := repo.CreateSession(ctx, user.ID, token, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if session, err := repo.SessionUser(ctx, token); err != nil || session.ID != user.ID || session.PasswordHash != "" {
			t.Fatalf("unexpected session identity: %v", err)
		}
		if err := repo.DeleteSession(ctx, token); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.SessionUser(ctx, token); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("revoked session returned %v", err)
		}
		expired := "expired-" + suffix
		if err := repo.CreateSession(ctx, user.ID, expired, time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.SessionUser(ctx, expired); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expired session returned %v", err)
		}
	})

	t.Run("assets and imports", func(t *testing.T) {
		manual, err := repo.CreateAsset(ctx, workspace.ID, "Manual", "CRM", "Security")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.CreateAsset(ctx, workspace.ID, "Manual", "CRM", "Another team"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate manual asset returned %v", err)
		}

		assets := []store.CollectedAsset{
			{SourceRef: "public.a", Name: "First label"},
			{SourceRef: "public.b", Name: "Second asset"},
			{SourceRef: "public.a", Name: "Ignored duplicate label"},
		}
		if _, err := repo.ImportAssets(ctx, workspace.ID, other.ID, "postgresql", "CRM", "foreign", "digest", assets); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("cross-workspace import returned %v", err)
		}
		run, err := repo.ImportAssets(ctx, workspace.ID, user.ID, "postgresql", "CRM", "batch-1", "digest", assets)
		if err != nil || run.Created != 2 || run.Existing != 1 {
			t.Fatalf("unexpected import %+v: %v", run, err)
		}
		replayed, err := repo.ImportAssets(ctx, workspace.ID, user.ID, "postgresql", "CRM", "batch-1", "digest", assets)
		if err != nil || !replayed.Replayed || replayed.ID != run.ID || replayed.Created != run.Created {
			t.Fatalf("expected existing run, got %+v: %v", replayed, err)
		}
		if _, err := repo.ImportAssets(ctx, workspace.ID, user.ID, "postgresql", "CRM", "batch-1", "different", assets); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("conflicting replay returned %v", err)
		}

		if _, err := pool.Exec(ctx, "UPDATE plume.assets SET name = 'Human label' WHERE workspace_id = $1 AND source_ref = 'public.a'", workspace.ID); err != nil {
			t.Fatal(err)
		}
		if next, err := repo.ImportAssets(ctx, workspace.ID, user.ID, "postgresql", "CRM", "batch-2", "digest", assets); err != nil || next.Created != 0 || next.Existing != 3 {
			t.Fatalf("unexpected reimport %+v: %v", next, err)
		}
		page, err := repo.ListAssets(ctx, workspace.ID, manual.ID, 1)
		if err != nil || len(page) != 1 || page[0].Name != "Human label" || page[0].SourceRef != "public.a" {
			t.Fatalf("unexpected page %+v: %v", page, err)
		}
		if count, err := repo.AssetCount(ctx, workspace.ID); err != nil || count != 3 {
			t.Fatalf("unexpected asset count %d: %v", count, err)
		}

		bad := []store.CollectedAsset{{SourceRef: "public.c", Name: "invalid\x00name"}}
		if _, err := repo.ImportAssets(ctx, workspace.ID, user.ID, "postgresql", "CRM", "batch-3", "failed-digest", bad); err == nil {
			t.Fatal("expected database to reject the invalid asset")
		}
		good := []store.CollectedAsset{{SourceRef: "public.c", Name: "Third asset"}}
		type result struct {
			run store.ImportRun
			err error
		}
		results := make(chan result, 2)
		for range 2 {
			go func() {
				run, err := repo.ImportAssets(ctx, workspace.ID, user.ID, "postgresql", "CRM", "batch-3", "retry-digest", good)
				results <- result{run: run, err: err}
			}()
		}
		first, second := <-results, <-results
		if first.err != nil || second.err != nil || first.run.Replayed == second.run.Replayed ||
			first.run.ID != second.run.ID || first.run.Created != 1 || second.run.Created != 1 {
			t.Fatalf("concurrent retries returned %+v and %+v", first, second)
		}
		runs, err := repo.ListImports(ctx, workspace.ID)
		if err != nil || len(runs) != 3 || runs[0].ID != first.run.ID {
			t.Fatalf("unexpected import history %+v: %v", runs, err)
		}
	})
}
