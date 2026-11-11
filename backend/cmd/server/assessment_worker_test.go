package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAssessmentWorkerRefreshesDueFacts(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 16*time.Second)
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
	slug := fmt.Sprintf("assessment-worker-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Worker fixture"))
	user, err := repo.FindUser(ctx, slug)
	must(err)
	ws, err := repo.Membership(ctx, user.ID, slug)
	must(err)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=$1`, user.ID)
	})
	asset, err := repo.CreateAsset(ctx, ws.ID, "Worker data", "fixture", "Data")
	must(err)
	activity, err := repo.CreateActivity(ctx, ws.ID, user.ID, store.ActivityInput{AssetID: asset.ID, Name: "AI worker activity", Kind: "ai", Purpose: "Approved synthetic input"})
	must(err)
	templates, err := repo.ListAssessmentTemplates(ctx, ws.ID, user.ID)
	must(err)
	var revision int64
	for _, v := range templates {
		if v.Kind == "ai" {
			revision = v.ID
		}
	}
	assessment, err := repo.CreateAssessment(ctx, ws.ID, user.ID, store.AssessmentInput{ActivityID: activity.ID, TemplateRevisionID: revision, Title: "Periodic fixture", DueAt: time.Now().Add(time.Hour), RefreshMinutes: 5})
	must(err)
	_, err = pool.Exec(ctx, `UPDATE plume.assessments SET next_refresh_at=clock_timestamp()-interval '1 second',facts_refreshed_at=clock_timestamp()-interval '1 day' WHERE id=$1`, assessment.ID)
	must(err)
	started := time.Now()
	workerCtx, stop := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() { defer close(finished); runAssessmentWorker(workerCtx, repo) }()
	defer stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	refreshed := false
	for !refreshed {
		select {
		case <-ctx.Done():
			t.Fatal("worker did not refresh due assessment")
		case <-ticker.C:
			var at time.Time
			must(pool.QueryRow(ctx, `SELECT facts_refreshed_at FROM plume.assessments WHERE id=$1`, assessment.ID).Scan(&at))
			refreshed = at.After(started)
		}
	}
	stop()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop on cancellation")
	}
	var next time.Time
	must(pool.QueryRow(ctx, `SELECT next_refresh_at FROM plume.assessments WHERE id=$1`, assessment.ID).Scan(&next))
	if !next.After(time.Now().Add(4 * time.Minute)) {
		t.Fatal("refresh did not move due time")
	}
}
