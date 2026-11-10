package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManifestValidation(t *testing.T) {
	base := manifest{Version: 1, Provider: "postgresql", Source: "CRM", BatchID: "batch-1", Assets: []store.CollectedAsset{
		{SourceRef: "public.b", Name: "B"}, {SourceRef: "public.a", Name: "A"},
	}}
	first, ok := base.digest()
	if !ok {
		t.Fatal("valid snapshot rejected")
	}
	base.Assets[0], base.Assets[1] = base.Assets[1], base.Assets[0]
	if reordered, ok := base.digest(); !ok || first != reordered {
		t.Fatal("identical snapshot with reordered rows has a different digest")
	}
	base.Assets = append(base.Assets, base.Assets[0])
	if _, ok := base.digest(); ok {
		t.Fatal("duplicate asset reference accepted")
	}
	base.Assets = nil
	if _, ok := base.digest(); ok {
		t.Fatal("missing asset array accepted")
	}
	base.Assets = []store.CollectedAsset{}
	if _, ok := base.digest(); !ok {
		t.Fatal("empty snapshot rejected")
	}
}

func TestUnauthenticatedAndCrossOrigin(t *testing.T) {
	handler := New(nil, "https://example.invalid", true)
	for _, tc := range []struct {
		method string
		origin string
		path   string
		want   int
	}{
		{"GET", "", "/api/v1/session", 401},
		{"POST", "https://other.invalid", "/api/v1/session", 403},
		{"DELETE", "", "/api/v1/session", 403},
		{"POST", "https://example.invalid", "/api/v1/workspaces/demo/imports", 401},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Origin", tc.origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s returned %d, expected %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

func TestPostgreSQLWorkflow(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not configure test database")
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	owner, other := "web-owner-"+suffix, "web-other-"+suffix
	a, b := "web-a-"+suffix, "web-b-"+suffix
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DELETE FROM plume.workspaces WHERE slug = ANY($1)", []string{a, b}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM plume.users WHERE username = ANY($1)", []string{owner, other}); err != nil {
			t.Error(err)
		}
	})
	password := "test-only-passphrase"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Bootstrap(ctx, owner, hash, a, "A"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Bootstrap(ctx, other, hash, b, "B"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://example.invalid"
	handler := New(repo, origin, true)
	do := func(method, path, body string, cookie *http.Cookie, expected int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, w.Code, expected, w.Body)
		}
		return w
	}
	session := "/api/v1/session"
	do("POST", session, fmt.Sprintf(`{"username":%q,"password":"bad"}`, owner), nil, 401)
	login := do("POST", session, fmt.Sprintf(`{"username":%q,"password":%q}`, owner, password), nil, 204)
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly ||
		cookies[0].Name != "__Host-scarlet-session" || cookies[0].Path != "/" {
		t.Fatal("invalid session cookie")
	}
	cookie := cookies[0]
	do("GET", session, "", cookie, 200)
	otherWorkspace := "/api/v1/workspaces/" + b
	do("GET", otherWorkspace+"/assets", "", cookie, 404)
	do("POST", otherWorkspace+"/imports", "{}", cookie, 404)

	workspace := "/api/v1/workspaces/" + a
	importPath := workspace + "/imports"
	first := `{"version":1,"provider":"postgresql","source":"CRM","batchId":"first","assets":[{"sourceRef":"public.orders","name":"orders"}]}`
	firstResult := do("POST", importPath, first, cookie, 200)
	retry := do("POST", importPath, first, cookie, 200)
	var original, replayed store.ImportRun
	if json.Unmarshal(firstResult.Body.Bytes(), &original) != nil ||
		json.Unmarshal(retry.Body.Bytes(), &replayed) != nil ||
		original.Replayed || !replayed.Replayed {
		t.Fatal("responses did not distinguish a replay")
	}
	original.Replayed = true
	if original != replayed {
		t.Fatal("same batch did not replay the original result")
	}
	do("POST", importPath, `{"version":1,"provider":"postgresql","source":"CRM","batchId":"first","assets":[{"sourceRef":"public.orders","name":"different"}]}`, cookie, 409)
	do("POST", importPath, `{"version":1,"provider":"postgresql","source":"CRM","batchId":"new","assets":[{"sourceRef":"public.orders","name":"orders","sample":"raw"}]}`, cookie, 400)

	rec := do("GET", workspace+"/assets", "", cookie, 200)
	var page struct{ Items []store.Asset }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil ||
		len(page.Items) != 1 || page.Items[0].SourceRef != "public.orders" {
		t.Fatalf("unexpected asset page: %s", rec.Body)
	}
	if _, err := pool.Exec(ctx, `UPDATE plume.memberships AS m SET role = 'viewer'
		FROM plume.workspaces AS w WHERE m.workspace_id = w.id AND w.slug = $1`, a); err != nil {
		t.Fatal(err)
	}
	do("GET", workspace+"/integrations", "", cookie, 200)
	do("POST", importPath, first, cookie, 403)
	do("POST", workspace+"/assets", `{"name":"record","system":"CRM","ownerTeam":""}`, cookie, 403)
	do("DELETE", session, "", cookie, 204)
	do("GET", session, "", cookie, 401)
}
