package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestClassificationStandardHTTPRoundTrip(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	repo := store.New(pool)
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("standard-http-%d", time.Now().UnixNano())
	otherSlug := slug + "-other"
	ownerName, reviewerName := slug+"-owner", slug+"-reviewer"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{slug, otherSlug})
		must(err)
		_, err = pool.Exec(cleanup, "DELETE FROM plume.users WHERE username = ANY($1)", []string{ownerName, reviewerName})
		must(err)
	})
	must(repo.Bootstrap(ctx, ownerName, "unused", slug, "Standard HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, reviewerName, "unused"))
	must(repo.Bootstrap(ctx, reviewerName, "unused", otherSlug, "Other workspace"))
	owner, err := repo.FindUser(ctx, ownerName)
	must(err)
	reviewer, err := repo.FindUser(ctx, reviewerName)
	must(err)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	asset, err := repo.CreateAsset(ctx, workspace.ID, "Reviewed HTTP asset", "fixture", "Data")
	must(err)
	cookie := func(userID int64) *http.Cookie {
		t.Helper()
		token, err := sessionToken()
		must(err)
		must(repo.CreateSession(ctx, userID, sessionHash(token), time.Now().Add(time.Hour)))
		return &http.Cookie{Name: "__Host-scarlet-session", Value: token}
	}
	ownerCookie, reviewerCookie := cookie(owner.ID), cookie(reviewer.ID)
	const origin = "https://example.invalid"
	handler := New(repo, origin, true)
	do := func(method, path string, body any, cookie *http.Cookie, status int) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		must(err)
		request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, response.Code, status, response.Body)
		}
		return response
	}
	base := "/api/v1/workspaces/" + slug + "/classification"
	do("GET", base+"/standards", nil, nil, http.StatusUnauthorized)
	standards := do("GET", base+"/standards", nil, reviewerCookie, http.StatusOK)
	var catalog struct {
		Items             []classificationstandards.Standard `json:"items"`
		DefaultStandardID string                             `json:"defaultStandardId"`
	}
	must(json.Unmarshal(standards.Body.Bytes(), &catalog))
	if len(catalog.Items) == 0 || catalog.DefaultStandardID != classificationstandards.DefaultID {
		t.Fatalf("catalog or workspace default omitted: %+v", catalog)
	}
	do("PUT", base+"/standards/default", map[string]string{"standardId": "energy-grading-2026"}, reviewerCookie, http.StatusForbidden)
	do("PUT", base+"/standards/default", map[string]string{"standardId": "ydt-7344-2026"}, ownerCookie, http.StatusBadRequest)
	do("GET", "/api/v1/workspaces/"+otherSlug+"/classification/standards", nil, ownerCookie, http.StatusNotFound)

	path := fmt.Sprintf("%s/assets/%d", base, asset.ID)
	input := map[string]any{
		"revision": 0, "category": "business", "businessLevel": "internal", "statutoryCategory": "general",
		"basis": "Business scope and impact reviewed", "standardId": classificationstandards.DefaultID,
		"levelCode": "general", "ruleId": "ordinary-grading",
	}
	do("PUT", path, input, reviewerCookie, http.StatusForbidden)
	withoutRevision := map[string]string{"category": "business", "businessLevel": "internal", "statutoryCategory": "general", "basis": "Missing revision"}
	do("PUT", path, withoutRevision, ownerCookie, http.StatusBadRequest)
	response := do("PUT", path, input, ownerCookie, http.StatusOK)
	var saved store.AssetClassificationDetail
	must(json.Unmarshal(response.Body.Bytes(), &saved))
	expected, err := classificationstandards.Select(classificationstandards.DefaultID, "general", "ordinary-grading", "general")
	must(err)
	if saved.Revision != 1 || len(saved.History) != 1 || expected.RuleEvidence == "" ||
		!reflect.DeepEqual(saved.Standard, expected) || !reflect.DeepEqual(saved.History[0].Standard, expected) {
		t.Fatalf("HTTP response lost the selected version, rule or evidence requirements: %+v", saved)
	}
	do("PUT", path, input, ownerCookie, http.StatusConflict)
	reloaded := do("GET", path, nil, reviewerCookie, http.StatusOK)
	var stored store.AssetClassificationDetail
	must(json.Unmarshal(reloaded.Body.Bytes(), &stored))
	if !reflect.DeepEqual(stored, saved) {
		t.Fatalf("rejected stale write changed the saved determination or history: %+v", stored)
	}
}
