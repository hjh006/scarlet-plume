package store_test

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

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManagedPostgresScheduling(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure automation test database")
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

	slug := fmt.Sprintf("managed-%d", time.Now().UnixNano())
	ownerName, viewerName := slug+"-owner", slug+"-viewer"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = $1", slug); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", []string{ownerName, viewerName}); err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "Managed automation"), nil)
	want(repo.ProvisionReviewer(ctx, slug, viewerName, "unused"), nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	viewer, err := repo.FindUser(ctx, viewerName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)

	token := fmt.Sprintf("%x", sha256.Sum256([]byte(slug)))
	config := store.ManagedPostgresInput{Host: "sample-db", Port: 5432, Database: "sample", User: "reader",
		Schemas: []string{"inventory"}, IntervalMinutes: 1, Enabled: true, EncryptedPassword: []byte("ciphertext-fixture")}
	_, err = repo.CreateManagedPostgresJob(ctx, workspace.ID, viewer.ID, "Managed catalog", token, config)
	want(err, store.ErrForbidden)
	job, err := repo.CreateManagedPostgresJob(ctx, workspace.ID, owner.ID, "Managed catalog", token, config)
	want(err, nil)
	_, err = repo.ManagedPostgresJob(ctx, workspace.ID, viewer.ID, job.ID)
	want(err, store.ErrForbidden)
	jobs, err := repo.ListAutomationJobs(ctx, workspace.ID, viewer.ID)
	want(err, nil)
	if len(jobs) != 1 || jobs[0].Managed == nil || jobs[0].Managed.Revision != 1 || len(jobs[0].Managed.EncryptedPassword) != 0 {
		t.Fatalf("expected safe managed configuration: %+v", jobs)
	}
	public, err := json.Marshal(job)
	want(err, nil)
	if strings.Contains(string(public), "ciphertext") {
		t.Fatal("managed configuration leaked its encrypted credential")
	}
	_, err = repo.DeliverAutomationRun(ctx, token, store.AutomationDelivery{})
	want(err, store.ErrAutomationUnauthorized)

	claim, err := repo.ClaimManagedPostgresJob(ctx, "run-original", time.Minute)
	want(err, nil)
	if claim == nil || claim.JobID != job.ID || string(claim.Config.EncryptedPassword) != "ciphertext-fixture" {
		t.Fatalf("initial schedule did not claim job: %+v", claim)
	}
	busy, err := repo.ClaimManagedPostgresJob(ctx, "run-busy", time.Minute)
	want(err, nil)
	if busy != nil {
		t.Fatal("leased job was claimed twice")
	}

	config.EncryptedPassword = nil
	changedHost := config
	changedHost.Host = "other-db"
	_, err = repo.UpdateManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID, 1, changedHost)
	want(err, store.ErrInvalid)
	config.IntervalMinutes, config.Enabled = 5, false
	changed, err := repo.UpdateManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID, 1, config)
	want(err, nil)
	if changed.Managed.Revision != 2 || changed.Managed.NextRunAt != nil || string(changed.Managed.EncryptedPassword) != "ciphertext-fixture" {
		t.Fatalf("revision did not preserve credentials and pause schedule: %+v", changed.Managed)
	}
	_, err = repo.UpdateManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID, 1, config)
	want(err, store.ErrConflict)

	input := store.AutomationDelivery{Digest: strings.Repeat("a", 64), ObservedAt: time.Now(),
		Assets: []store.CollectedAsset{{SourceRef: "inventory.orders", Name: "Orders"}}}
	_, err = repo.FinishManagedPostgresJob(ctx, *claim, input)
	want(err, store.ErrConflict)
	want(repo.QueueManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID), nil)
	claim, err = repo.ClaimManagedPostgresJob(ctx, "run-now", time.Minute)
	want(err, nil)
	if claim == nil || claim.Config.Revision != 2 {
		t.Fatal("paused schedule did not permit manual run")
	}
	want(repo.QueueManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID), nil)
	imported, err := repo.FinishManagedPostgresJob(ctx, *claim, input)
	want(err, nil)
	if imported.CreatedCount != 1 || imported.ImportID == nil {
		t.Fatalf("unexpected first outcome: %+v", imported)
	}
	replay, err := repo.FinishManagedPostgresJob(ctx, *claim, input)
	want(err, nil)
	if replay.ID != imported.ID || !replay.Replayed {
		t.Fatalf("unexpected replay outcome: %+v", replay)
	}

	claim, err = repo.ClaimManagedPostgresJob(ctx, "run-expired", time.Minute)
	want(err, nil)
	if claim == nil {
		t.Fatal("request queued during a run was lost")
	}
	_, err = pool.Exec(ctx, "UPDATE plume.managed_postgres_jobs SET lease_until = clock_timestamp() - interval '1 second' WHERE job_id = $1", job.ID)
	want(err, nil)
	recovered, err := repo.ClaimManagedPostgresJob(ctx, "run-recovered", time.Minute)
	want(err, nil)
	if recovered == nil || recovered.JobID != job.ID {
		t.Fatal("manual request did not survive worker lease expiry")
	}
	_, err = repo.FinishManagedPostgresJob(ctx, *claim, input)
	want(err, store.ErrConflict)
	imported, err = repo.FinishManagedPostgresJob(ctx, *recovered, input)
	want(err, nil)
	if imported.CreatedCount != 0 || imported.ExistingCount != 1 {
		t.Fatalf("recovered run created duplicates: %+v", imported)
	}
	want(repo.QueueManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID), nil)
	claim, err = repo.ClaimManagedPostgresJob(ctx, "run-schema-unavailable", time.Minute)
	want(err, nil)
	if claim == nil {
		t.Fatal("schema failure run was not claimed")
	}
	failed, err := repo.FinishManagedPostgresJob(ctx, *claim, store.AutomationDelivery{
		Digest: strings.Repeat("b", 64), ObservedAt: time.Now(),
		FailureCode: store.ManagedPostgresSchemaUnavailableCode,
	})
	want(err, nil)
	if failed.Status != "error" || failed.Code != store.ManagedPostgresSchemaUnavailableCode || failed.ImportID != nil {
		t.Fatalf("unexpected unavailable schema outcome: %+v", failed)
	}
	want(repo.QueueManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID), nil)
	claim, err = repo.ClaimManagedPostgresJob(ctx, "run-revoked", time.Minute)
	want(err, nil)
	if claim == nil {
		t.Fatal("manual request was not claimed")
	}
	want(repo.RevokeAutomationJob(ctx, workspace.ID, owner.ID, job.ID), nil)
	_, err = repo.FinishManagedPostgresJob(ctx, *claim, input)
	want(err, store.ErrConflict)
	want(repo.QueueManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID), store.ErrNotFound)
}
