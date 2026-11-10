package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestAssetClassificationCandidatesAndHistory(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure test PostgreSQL database")
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
	slug := fmt.Sprintf("classify-%d", time.Now().UnixNano())
	ownerName, reviewerName, otherName := slug+"-owner", slug+"-reviewer", slug+"-other"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug = $1", slug); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(cleanup, "DELETE FROM plume.users WHERE username = ANY($1)", []string{ownerName, reviewerName, otherName}); err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "Classification"), nil)
	want(repo.ProvisionReviewer(ctx, slug, reviewerName, "unused"), nil)
	_, err = pool.Exec(ctx, "INSERT INTO plume.users (username,password_hash) VALUES ($1,'unused')", otherName)
	want(err, nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	reviewer, err := repo.FindUser(ctx, reviewerName)
	want(err, nil)
	other, err := repo.FindUser(ctx, otherName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)
	digest := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	legacy, err := repo.CreateAsset(ctx, workspace.ID, "Legacy metadata", "fixture", "Data")
	want(err, nil)
	_, err = pool.Exec(ctx, `
		UPDATE plume.assets SET category='business', business_level='internal',
			statutory_category='general', classification_basis='Imported determination' WHERE id=$1
	`, legacy.ID)
	want(err, nil)

	job, err := repo.CreateManagedPostgresJob(ctx, workspace.ID, owner.ID, "DB fixture", digest(slug+"-pg"), store.ManagedPostgresInput{
		Host: "sample-db", Port: 5432, Database: "sample", User: "reader", Schemas: []string{"inventory"},
		IntervalMinutes: 5, EncryptedPassword: []byte("fixture"),
	})
	want(err, nil)
	want(repo.QueueManagedPostgresJob(ctx, workspace.ID, owner.ID, job.ID), nil)
	claim, err := repo.ClaimManagedPostgresJob(ctx, slug+"-run", time.Minute)
	want(err, nil)
	if claim == nil || claim.JobID != job.ID {
		t.Fatalf("expected fixture claim: %+v", claim)
	}
	ref := collector.Asset{SourceRef: "inventory.customers", Name: "customers"}
	columns := []collector.PostgreSQLColumn{{Name: "national_id", Type: "text", Ordinal: 1, Sensitivity: "national_id"}}
	for i := range 25 {
		columns = append(columns, collector.PostgreSQLColumn{
			Name: fmt.Sprintf("contact_%02d_email", i), Type: "text", Ordinal: i + 2, Sensitivity: "email",
		})
	}
	_, err = repo.FinishManagedPostgresJob(ctx, *claim, store.AutomationDelivery{
		Digest: digest("collection"), ObservedAt: time.Now(), Assets: []store.CollectedAsset{{SourceRef: ref.SourceRef, Name: ref.Name}},
		PostgresTables: []collector.PostgreSQLTable{{Asset: ref, Schema: "inventory", Owner: "reader", Columns: columns}},
	})
	want(err, nil)

	const apiSource = "Fixture API"
	publish := func(revision, category, name string) store.APISecurityScan {
		t.Helper()
		result, err := repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, apiSource, digest(revision), apisecurity.Scan{
			Version: "3.1.0", Operations: []apisecurity.Operation{{
				Method: "GET", Path: "/members", Auth: "required",
				SensitiveFields: []apisecurity.SensitiveField{{Name: name, Location: "response", Category: category}},
			}},
		})
		want(err, nil)
		return result
	}
	oldScan := publish("old scan", "national_id", "national_id")
	scan := publish("current scan", "email", "billingEmail")
	if oldScan.Operations[0].AssetID != scan.Operations[0].AssetID {
		t.Fatal("expected both scans to identify the same asset")
	}
	apiAsset := scan.Operations[0].AssetID

	page, err := repo.ListAssetClassifications(ctx, workspace.ID, reviewer.ID, store.AssetClassificationFilter{})
	want(err, nil)
	if page.Counts.Total != 3 || page.Counts.Pending != 3 || page.Counts.Reviewed != 0 || len(page.Items) != 3 {
		t.Fatalf("unexpected initial classification counts: %+v", page.Counts)
	}
	for _, item := range page.Items {
		switch item.Asset.SourceProvider {
		case "postgresql":
			if item.CandidateCount != 26 || len(item.Candidates) != 20 || item.Candidates[0].Category != "national_id" ||
				item.Candidates[0].RunID != claim.RunID || item.Candidates[0].ObservedAt.IsZero() ||
				item.SuggestedCategory != "sensitive_personal" || item.SuggestedBusinessLevel != "confidential" ||
				item.Asset.StatutoryCategory != "unknown" {
				t.Fatalf("bad PostgreSQL provenance or unauthorized statutory inference: %+v", item)
			}
		case "openapi":
			if item.Asset.ID != apiAsset || item.CandidateCount != 1 || len(item.Candidates) != 1 ||
				item.Candidates[0].Name != "billingEmail" || item.Candidates[0].ScanID == nil || *item.Candidates[0].ScanID != scan.ID ||
				item.SuggestedCategory != "personal" || item.Asset.StatutoryCategory != "unknown" {
				t.Fatalf("bad OpenAPI provenance: %+v", item)
			}
		default:
			if item.Revision != 0 || item.Asset.StatutoryCategory != "general" || item.Candidates == nil {
				t.Fatalf("legacy values were overwritten or lost: %+v", item)
			}
		}
	}
	_, err = repo.GetAssetClassification(ctx, workspace.ID, other.ID, legacy.ID)
	want(err, store.ErrNotFound)
	input := store.AssetClassificationInput{
		Revision: 0, Category: "business", BusinessLevel: "confidential",
		StatutoryCategory: "important", Basis: "  Manually reviewed fixture scope  ",
	}
	_, err = repo.UpdateAssetClassification(ctx, workspace.ID, reviewer.ID, legacy.ID, input)
	want(err, store.ErrForbidden)
	invalid := input
	invalid.Basis = " "
	_, err = repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, legacy.ID, invalid)
	want(err, store.ErrInvalid)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, legacy.ID, input)
			errs <- err
		}()
	}
	first, second := <-errs, <-errs
	if first != nil {
		first, second = second, first
	}
	want(first, nil)
	want(second, store.ErrConflict)

	detail, err := repo.GetAssetClassification(ctx, workspace.ID, reviewer.ID, legacy.ID)
	want(err, nil)
	if detail.Revision != 1 || detail.Asset.StatutoryCategory != "important" || len(detail.History) != 1 ||
		detail.History[0].Actor != ownerName || detail.History[0].PreviousStatutoryCategory != "general" ||
		detail.History[0].PreviousBasis != "Imported determination" || detail.History[0].Basis != "Manually reviewed fixture scope" {
		t.Fatalf("unexpected persisted classification history: %+v", detail)
	}
	input.Revision, input.StatutoryCategory, input.Basis = 1, "core", "Approved replacement determination"
	next, err := repo.UpdateAssetClassification(ctx, workspace.ID, owner.ID, legacy.ID, input)
	want(err, nil)
	if next.Revision != 2 || len(next.History) != 2 || next.History[0].PreviousStatutoryCategory != "important" ||
		next.History[1] != detail.History[0] {
		t.Fatalf("next determination modified old history: %+v", next)
	}
	reviewed, err := repo.ListAssetClassifications(ctx, workspace.ID, reviewer.ID, store.AssetClassificationFilter{
		Status: "reviewed", StatutoryCategory: "core",
	})
	want(err, nil)
	if len(reviewed.Items) != 1 || reviewed.Items[0].Asset.ID != legacy.ID ||
		reviewed.Counts.Reviewed != 1 || reviewed.Counts.ImportantOrCore != 1 {
		t.Fatalf("reviewed filter or workspace counts incorrect: %+v", reviewed)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO plume.assets (workspace_id, name, system) SELECT $1, 'Fixture ' || n, 'fixture' FROM generate_series(1, 50) n
	`, workspace.ID)
	want(err, nil)
	pending, err := repo.ListAssetClassifications(ctx, workspace.ID, reviewer.ID, store.AssetClassificationFilter{Status: "pending"})
	want(err, nil)
	if len(pending.Items) != 50 || pending.NextCursor == nil || pending.Counts.Pending != 52 {
		t.Fatalf("first pending page incorrect: count=%d next=%v totals=%+v", len(pending.Items), pending.NextCursor, pending.Counts)
	}
	last, err := repo.ListAssetClassifications(ctx, workspace.ID, reviewer.ID, store.AssetClassificationFilter{Status: "pending", After: *pending.NextCursor})
	want(err, nil)
	if len(last.Items) != 2 || last.NextCursor != nil || last.Items[0].Asset.ID <= *pending.NextCursor {
		t.Fatalf("last pending page incorrect: %+v", last)
	}
}
