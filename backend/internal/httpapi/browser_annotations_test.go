package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBrowserAnnotationHTTPBoundaries(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx := t.Context()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	repo := store.New(pool)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("browser-%d", time.Now().UnixNano())
	other := slug + "-other"
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Synthetic browser"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reader", "unused"))
	must(repo.Bootstrap(ctx, other, "unused", other, "Other workspace"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	reader, e := repo.FindUser(ctx, slug+"-reader")
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	outsider, e := repo.FindUser(ctx, other)
	must(e)
	otherWS, e := repo.Membership(ctx, outsider.ID, other)
	must(e)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, []string{slug, other})
		_, _ = pool.Exec(ctx, `DELETE FROM plume.users WHERE username=ANY($1)`, []string{slug, slug + "-reader", other})
	})
	asset, e := repo.CreateAsset(ctx, ws.ID, "Synthetic form", "business", "QA")
	must(e)
	foreign, e := repo.CreateAsset(ctx, otherWS.ID, "Foreign", "business", "QA")
	must(e)
	session := func(id int64) string {
		v, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(v), time.Now().Add(time.Hour)))
		return v
	}
	ownerToken, readerToken := session(owner.ID), session(reader.ID)
	handler := New(repo, "https://platform.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/browser-annotations"
	call := func(method, path, body, token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if method != "GET" {
			r.Header.Set("Origin", "https://platform.invalid")
		}
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got%d want%d %.300s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	body := fmt.Sprintf(`{"assetId":%d,"page":{"origin":"https://synthetic.invalid","path":"/business","title":"Synthetic"},"fields":[{"name":"revenue","label":"经营收入","elementKind":"input","locator":"input:visible-field(1)","category":"经营数据","targetField":"revenue","note":"人工关联"}]}`, asset.ID)
	call("GET", base, "", "", 401)
	call("POST", base, body, readerToken, 403)
	first := call("POST", base, body, ownerToken, 201)
	var saved store.BrowserAnnotation
	must(json.Unmarshal(first.Body.Bytes(), &saved))
	if saved.AssetID != asset.ID || len(saved.SnapshotDigest) != 64 || saved.Actor != slug {
		t.Fatalf("invalid persisted attribution: %+v", saved)
	}
	replay := call("POST", base, body, ownerToken, 201)
	var second store.BrowserAnnotation
	must(json.Unmarshal(replay.Body.Bytes(), &second))
	if !second.Replayed || second.ID != saved.ID {
		t.Fatal("same annotation did not replay immutable snapshot")
	}
	detail := call("GET", fmt.Sprintf("%s/%d", base, saved.ID), "", readerToken, 200)
	if !strings.Contains(detail.Body.String(), "经营收入") {
		t.Fatal("stored fields missing")
	}
	call("GET", base+"?assetId="+fmt.Sprint(asset.ID), "", readerToken, 200)
	call("GET", base+"?assetId="+fmt.Sprint(foreign.ID), "", ownerToken, 404)
	call("POST", base, strings.Replace(body, fmt.Sprintf(`"assetId":%d`, asset.ID), fmt.Sprintf(`"assetId":%d`, foreign.ID), 1), ownerToken, 404)
	call("POST", base, strings.Replace(body, `"name":"revenue"`, `"name":"revenue","value":"must-not-be-accepted"`, 1), ownerToken, 400)
	call("POST", base, strings.Replace(body, `"path":"/business"`, `"path":"/business?secret=excluded"`, 1), ownerToken, 400)
	call("POST", base, strings.Replace(body, `https://synthetic.invalid`, `https://user:secret@synthetic.invalid`, 1), ownerToken, 400)
	call("GET", "/api/v1/workspaces/"+other+"/browser-annotations/"+fmt.Sprint(saved.ID), "", ownerToken, 404)
	// Real imported contract fields, not a DOM-derived guess of an API relation.
	scan, e := repo.ImportAPISecurityScan(ctx, ws.ID, owner.ID, "synthetic-api", strings.Repeat("a", 64), apisecurity.Scan{Version: "3.0.3", Operations: []apisecurity.Operation{{Method: "GET", Path: "/business", Auth: "required", SensitiveFields: []apisecurity.SensitiveField{{Name: "email", Location: "response", Category: "email"}}}}})
	must(e)
	mapping := store.BrowserAnnotationInput{AssetID: scan.Operations[0].AssetID, Page: store.BrowserAnnotationPage{Origin: "https://synthetic.invalid", Path: "/business"}, Fields: []store.BrowserAnnotationField{{Name: "Email", Label: "电子邮箱", ElementKind: "input", Locator: "input:visible-field(1)"}}}
	encode := func(v any) string { b, e := json.Marshal(v); must(e); return string(b) }
	call("POST", base+"/match", encode(mapping), readerToken, 403)
	matched := call("POST", base+"/match", encode(mapping), ownerToken, 200)
	var candidates struct {
		Candidates []store.BrowserAnnotationCandidate `json:"candidates"`
	}
	must(json.Unmarshal(matched.Body.Bytes(), &candidates))
	if len(candidates.Candidates) != 1 || candidates.Candidates[0].ScanID != scan.ID || candidates.Candidates[0].Status != "candidate" {
		t.Fatalf("expected actual contract candidate: %.300s", matched.Body.String())
	}
	mapping.Fields[0].APICandidate = &candidates.Candidates[0]
	call("POST", base, encode(mapping), ownerToken, 201)
	mapping.Fields[0].Name = "unrelated"
	call("POST", base, encode(mapping), ownerToken, 400)
}
