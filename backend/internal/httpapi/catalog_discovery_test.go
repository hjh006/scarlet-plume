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

func TestCatalogDiscoveryHTTPBoundaries(t *testing.T) {
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
	slug := fmt.Sprintf("catalog-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Catalog HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-viewer", "unused"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	viewer, e := repo.FindUser(ctx, slug+"-viewer")
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	_, e = pool.Exec(ctx, `UPDATE plume.memberships SET role='viewer' WHERE workspace_id=$1 AND user_id=$2`, ws.ID, viewer.ID)
	must(e)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE username=ANY($1)`, []string{slug, slug + "-viewer"})
	})
	session := func(id int64) string {
		v, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(v), time.Now().Add(time.Hour)))
		return v
	}
	ownerSession, viewerSession := session(owner.ID), session(viewer.ID)
	handler := New(repo, "https://example.invalid", true)
	base := "/api/v1/workspaces/" + slug
	call := func(method, path, body, cookie, bearer, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s returned%d want%d %.300s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	call("GET", base+"/catalog", "", "", "", "", 401)
	call("POST", base+"/discovery/sources", `{}`, ownerSession, "", "https://other.invalid", 403)
	source := `{"source":"mysql-local","provider":"mysql","system":"mysql","database":"approved","scopes":[],"service":"","intervalMinutes":15,"enabled":true,"revision":0}`
	call("POST", base+"/discovery/sources", source, viewerSession, "", "https://example.invalid", 403)
	response := call("POST", base+"/discovery/sources", source, ownerSession, "", "https://example.invalid", 200)
	var issued struct {
		Source store.DiscoverySource `json:"source"`
		Token  string                `json:"token"`
	}
	must(json.Unmarshal(response.Body.Bytes(), &issued))
	if len(issued.Token) != 43 {
		t.Fatal("no task credential")
	}
	list := call("GET", base+"/discovery/sources", "", viewerSession, "", "", 200)
	if strings.Contains(list.Body.String(), issued.Token) {
		t.Fatal("task credential disclosed")
	}
	call("GET", "/api/v1/workspaces/not-my-workspace/catalog", "", ownerSession, "", "", 404)
	claimed := call("POST", CatalogDiscoveryClaimPath, `{}`, "", issued.Token, "", 200)
	var result struct {
		Claim store.DiscoveryClaim `json:"claim"`
	}
	must(json.Unmarshal(claimed.Body.Bytes(), &result))
	if result.Claim.SourceID != issued.Source.ID {
		t.Fatal("wrong source claimed")
	}
	call("POST", CatalogDiscoveryRunPath, `{"rawValues":["prohibited"]}`, "", issued.Token, "", 400)
	submission := fmt.Sprintf(`{"sourceId":%d,"revision":1,"runRef":%q,"status":"success","errorCode":"","observedAt":%q,"result":{"tables":[],"coverage":{"metadata":"observed","content":"not_collected","permissions":"unknown","lineage":"unknown","rowLimit":200,"tableLimit":100,"warnings":[]}}}`, issued.Source.ID, result.Claim.RunRef, time.Now().UTC().Format(time.RFC3339Nano))
	call("POST", CatalogDiscoveryRunPath, submission, "", issued.Token, "", 200)
	call("POST", base+"/discovery/sources/"+fmt.Sprint(issued.Source.ID)+"/token", `{"revision":1}`, ownerSession, "", "https://example.invalid", 200)
	call("POST", CatalogDiscoveryClaimPath, `{}`, "", issued.Token, "", 404)
	t.Log("verified actual routes, CSRF boundary, unauthenticated/viewer/foreign-workspace denial, one-time credentials, bearer-only agent input, raw-field rejection and credential rotation")
}
