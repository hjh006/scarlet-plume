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
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestAPISecurityScanHistory(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure PostgreSQL test database")
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

	slug := fmt.Sprintf("api-security-%d", time.Now().UnixNano())
	ownerName, reviewerName := slug+"-owner", slug+"-reviewer"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = $1", slug); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", []string{ownerName, reviewerName}); err != nil {
			t.Error(err)
		}
	})
	want(repo.Bootstrap(ctx, ownerName, "unused", slug, "API security"), nil)
	want(repo.ProvisionReviewer(ctx, slug, reviewerName, "unused"), nil)
	owner, err := repo.FindUser(ctx, ownerName)
	want(err, nil)
	reviewer, err := repo.FindUser(ctx, reviewerName)
	want(err, nil)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	want(err, nil)

	openDocument := []byte(`{
		"openapi": "3.1.0",
		"paths": {"/clients": {"get": {"security": [], "responses": {"200": {"content": {
			"application/json": {"schema": {"properties": {"email": {"type": "string", "example": "keep-out-of-storage@example.org"}}}}
		}}}}}}
	}`)
	restrictedDocument := []byte(`{
		"openapi": "3.1.0",
		"components": {"securitySchemes": {"bearer": {"type": "http", "scheme": "bearer"}}},
		"paths": {"/clients": {"get": {"security": [{"bearer": []}], "responses": {"200": {"description": "ok"}}}}}
	}`)
	open, err := apisecurity.Analyze(openDocument)
	want(err, nil)
	restricted, err := apisecurity.Analyze(restrictedDocument)
	want(err, nil)
	openDigest := fmt.Sprintf("%x", sha256.Sum256(openDocument))
	restrictedDigest := fmt.Sprintf("%x", sha256.Sum256(restrictedDocument))
	const source = "Customer API"
	_, err = repo.ImportAPISecurityScan(ctx, workspace.ID, reviewer.ID, source, openDigest, open)
	want(err, store.ErrForbidden)

	type result struct {
		scan store.APISecurityScan
		err  error
	}
	imports := make(chan result, 2)
	for range 2 {
		go func() {
			scan, err := repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, source, openDigest, open)
			imports <- result{scan, err}
		}()
	}
	first, duplicate := <-imports, <-imports
	want(first.err, nil)
	want(duplicate.err, nil)
	if first.scan.ID != duplicate.scan.ID || first.scan.Replayed == duplicate.scan.Replayed ||
		first.scan.OperationCount != 1 || first.scan.SensitiveFieldCount != 1 ||
		first.scan.FindingCount != 1 || first.scan.CheckCount != 1 {
		t.Fatalf("unexpected initial and replayed scans: %+v / %+v", first.scan, duplicate.scan)
	}
	operation := first.scan.Operations[0]
	if operation.AssetID == 0 || operation.CheckID == 0 {
		t.Fatalf("missing asset or check link: %+v", operation)
	}
	var storedExample bool
	want(pool.QueryRow(ctx, `SELECT operations::text LIKE '%keep-out-of-storage%'
		FROM plume.api_security_scans WHERE id = $1`, first.scan.ID).Scan(&storedExample), nil)
	if storedExample {
		t.Fatal("an OpenAPI example was persisted")
	}
	detail, err := repo.APISecurityScan(ctx, workspace.ID, first.scan.ID)
	want(err, nil)
	if detail.Operations[0].CheckID != operation.CheckID || detail.Replayed {
		t.Fatalf("unexpected archived detail: %+v", detail)
	}
	_, err = repo.APISecurityScan(ctx, 0, first.scan.ID)
	want(err, store.ErrNotFound)

	second, err := repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, source, restrictedDigest, restricted)
	want(err, nil)
	third, err := repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, source, openDigest, open)
	want(err, nil)
	if second.FindingCount != 0 || third.Replayed || third.ID <= second.ID ||
		second.Operations[0].CheckID != operation.CheckID || third.Operations[0].AssetID != operation.AssetID {
		t.Fatalf("unexpected A to B to A scan history: %+v / %+v", second, third)
	}
	check, err := repo.CheckDetail(ctx, workspace.ID, owner.ID, operation.CheckID)
	want(err, nil)
	if check.Status != "draft" || check.Requirement.Code != "G03" || len(check.Evidence) != 3 {
		t.Fatalf("expected draft G03 with three changed observations: %+v", check)
	}
	want(repo.SubmitCheck(ctx, workspace.ID, owner.ID, operation.CheckID), nil)
	_, err = repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, source, restrictedDigest, restricted)
	want(err, nil)
	check, err = repo.CheckDetail(ctx, workspace.ID, owner.ID, operation.CheckID)
	want(err, nil)
	if check.Status != "in_review" || len(check.Evidence) != 3 {
		t.Fatalf("scan altered a check under independent review: %+v", check)
	}
	scans, err := repo.ListAPISecurityScans(ctx, workspace.ID)
	want(err, nil)
	if len(scans) != 4 || scans[0].ID <= third.ID {
		t.Fatalf("unexpected scan list: %+v", scans)
	}
}
