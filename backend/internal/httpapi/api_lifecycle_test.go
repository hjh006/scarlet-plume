package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestAPILifecycleReadAndExportRoutes(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
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
	slug := fmt.Sprintf("lifecycle-http-%d", time.Now().UnixNano())
	foreignSlug := slug + "-foreign"
	ownerName, viewerName, foreignName := slug+"-owner", slug+"-viewer", slug+"-foreign-owner"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug=ANY($1)", []string{slug, foreignSlug})
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, "DELETE FROM plume.users WHERE username=ANY($1)", []string{ownerName, viewerName, foreignName})
		if err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, ownerName, "unused", slug, "Lifecycle HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, viewerName, "unused"))
	must(repo.Bootstrap(ctx, foreignName, "unused", foreignSlug, "Foreign lifecycle HTTP"))
	owner, err := repo.FindUser(ctx, ownerName)
	must(err)
	viewer, err := repo.FindUser(ctx, viewerName)
	must(err)
	foreign, err := repo.FindUser(ctx, foreignName)
	must(err)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	foreignWorkspace, err := repo.Membership(ctx, foreign.ID, foreignSlug)
	must(err)
	_, err = pool.Exec(ctx, "UPDATE plume.memberships SET role='viewer' WHERE workspace_id=$1 AND user_id=$2", workspace.ID, viewer.ID)
	must(err)
	const service = "Orders <service>"
	analysis, err := apisecurity.Analyze([]byte(`{"openapi":"3.1.0","paths":{"/orders":{"get":{"security":[],"responses":{"200":{"content":{"application/json":{"schema":{"properties":{"email":{"type":"string","example":"business-value-never-export@example.test"}}}}}}}}}}}`))
	must(err)
	scan, err := repo.ImportAPISecurityScan(ctx, workspace.ID, owner.ID, service, sessionHash("lifecycle-http-contract"), analysis)
	must(err)
	source, err := repo.CreateRuntimeSource(ctx, workspace.ID, owner.ID, "Gateway", service, "gateway", sessionHash(slug+"-source"))
	must(err)
	foreignSource, err := repo.CreateRuntimeSource(ctx, foreignWorkspace.ID, foreign.ID, "Foreign source", "Other", "gateway", sessionHash(slug+"-foreign-source"))
	must(err)
	token, err := sessionToken()
	must(err)
	must(repo.CreateSession(ctx, viewer.ID, sessionHash(token), time.Now().Add(time.Hour)))
	handler := New(repo, "https://example.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/api-security/lifecycle"
	get := func(path string, authenticated bool, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s returned %d, want %d: %.400s", path, w.Code, status, w.Body.String())
		}
		return w
	}
	get(base, false, http.StatusUnauthorized)
	get(base+"/export", false, http.StatusUnauthorized)
	get(base+"?sourceId=0", true, http.StatusBadRequest)
	get(base+"?sourceId=invalid", true, http.StatusBadRequest)
	get(base+"?service="+strings.Repeat("a", 81), true, http.StatusBadRequest)
	get(fmt.Sprintf("%s?sourceId=%d", base, foreignSource.ID), true, http.StatusNotFound)
	get(fmt.Sprintf("%s?sourceId=%d&service=Other", base, source.ID), true, http.StatusBadRequest)
	get("/api/v1/workspaces/"+foreignSlug+"/api-security/lifecycle", true, http.StatusNotFound)
	query := "?service=" + url.QueryEscape(service)
	view := get(base+query, true, http.StatusOK)
	export := get(base+"/export"+query, true, http.StatusOK)
	if export.Header().Get("Content-Disposition") != `attachment; filename="scarlet-api-lifecycle.json"` || !strings.HasPrefix(export.Header().Get("Content-Type"), "application/json") {
		t.Fatal("lifecycle export did not return a JSON attachment")
	}
	var before, after store.APILifecycle
	must(json.Unmarshal(view.Body.Bytes(), &before))
	must(json.Unmarshal(export.Body.Bytes(), &after))
	if len(after.Items) != 1 || after.Items[0].State != "unobserved" || after.Items[0].ScanID != scan.ID || len(after.Items[0].AssetIDs) != 1 || len(after.Items[0].CheckIDs) != 1 || !reflect.DeepEqual(before.Items, after.Items) || !reflect.DeepEqual(before.Comparisons, after.Comparisons) {
		t.Fatal("viewer export lost the filtered directory, linked records or contract history")
	}
	if strings.Contains(export.Body.String(), "business-value-never-export") || strings.Contains(export.Body.String(), foreignSource.Name) {
		t.Fatal("lifecycle export included raw examples or another workspace")
	}
	empty := get(base+"?service=NoSuchService", true, http.StatusOK)
	var emptyPage map[string]json.RawMessage
	must(json.Unmarshal(empty.Body.Bytes(), &emptyPage))
	for _, field := range []string{"items", "comparisons", "services"} {
		if string(emptyPage[field]) != "[]" {
			t.Errorf("empty %s was not an array: %s", field, emptyPage[field])
		}
	}
}
