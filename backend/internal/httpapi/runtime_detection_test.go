package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeDetectionHTTPVersionAndWorkspaceBoundaries(t *testing.T) {
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
	slug := fmt.Sprintf("detection-http-%d", time.Now().UnixNano())
	ownerName, readerName, foreignName := slug+"-owner", slug+"-reader", slug+"-foreign"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, []string{slug, slug + "-foreign"})
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, `DELETE FROM plume.users WHERE username=ANY($1)`, []string{ownerName, readerName, foreignName})
		if err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, ownerName, "unused", slug, "Detection policies HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, readerName, "unused"))
	must(repo.Bootstrap(ctx, foreignName, "unused", slug+"-foreign", "Foreign policies"))
	owner, err := repo.FindUser(ctx, ownerName)
	must(err)
	reader, err := repo.FindUser(ctx, readerName)
	must(err)
	foreign, err := repo.FindUser(ctx, foreignName)
	must(err)
	workspace, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	session := func(id int64) string {
		t.Helper()
		token, err := sessionToken()
		must(err)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	ownerToken, readerToken, foreignToken := session(owner.ID), session(reader.ID), session(foreign.ID)
	handler := New(repo, "https://example.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/api-security/runtime/policies"
	call := func(method, path, token, body, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: token})
		}
		if method != "GET" {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: %d want%d body=%.300s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	call("GET", base, "", "", "", 401)
	call("GET", base, foreignToken, "", "", 404)
	call("GET", base+"/history?rule=unknown", readerToken, "", "", 400)
	var page struct {
		Items []store.RuntimeDetectionPolicy `json:"items"`
	}
	must(json.Unmarshal(call("GET", base, readerToken, "", "", 200).Body.Bytes(), &page))
	if len(page.Items) != 9 {
		t.Fatal("default policies missing")
	}
	p := page.Items[0]
	raw, _ := json.Marshal(p)
	call("PUT", base, ownerToken, string(raw), "https://evil.invalid", 403)
	call("PUT", base, readerToken, string(raw), "https://example.invalid", 403)
	_, err = pool.Exec(ctx, `UPDATE plume.memberships SET role='operator' WHERE workspace_id=$1 AND user_id=$2`, workspace.ID, reader.ID)
	must(err)
	call("PUT", base, readerToken, string(raw), "https://example.invalid", 403)
	call("PUT", base, ownerToken, `{"raw":"private-business-value"}`, "https://example.invalid", 400)
	call("PUT", base, ownerToken, `{"rule":"`+strings.Repeat("x", 9000)+`"}`, "https://example.invalid", 413)
	invalid := p
	invalid.WindowMinutes = 61
	bad, _ := json.Marshal(invalid)
	call("PUT", base, ownerToken, string(bad), "https://example.invalid", 400)
	var first store.RuntimeDetectionPolicy
	must(json.Unmarshal(call("PUT", base, ownerToken, string(raw), "https://example.invalid", 200).Body.Bytes(), &first))
	if first.Revision != 1 || first.ID == 0 {
		t.Fatal("version missing")
	}
	call("PUT", base, ownerToken, string(raw), "https://example.invalid", 409)
	first.Enabled = false
	raw, _ = json.Marshal(first)
	var second store.RuntimeDetectionPolicy
	must(json.Unmarshal(call("PUT", base, ownerToken, string(raw), "https://example.invalid", 200).Body.Bytes(), &second))
	if second.Revision != 2 || second.Enabled {
		t.Fatal("updated version missing")
	}
	var history struct {
		Items []store.RuntimeDetectionPolicy `json:"items"`
	}
	must(json.Unmarshal(call("GET", base+"/history?rule="+p.Rule, readerToken, "", "", 200).Body.Bytes(), &history))
	if len(history.Items) != 2 || history.Items[0].Enabled || !history.Items[1].Enabled {
		t.Fatal("version history rewritten or unordered")
	}
	call("GET", "/api/v1/workspaces/"+slug+"-foreign/api-security/runtime/policies/history?rule="+p.Rule, ownerToken, "", "", 404)
	var foreignPage struct {
		Items []store.RuntimeDetectionPolicy `json:"items"`
	}
	must(json.Unmarshal(call("GET", "/api/v1/workspaces/"+slug+"-foreign/api-security/runtime/policies", foreignToken, "", "", 200).Body.Bytes(), &foreignPage))
	if foreignPage.Items[0].Revision != 0 {
		t.Fatal("policy leaked to another workspace")
	}
}
