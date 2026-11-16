package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAISecurityHTTPRolesGateExportAndNoRawBody(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal("test database unavailable")
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
	slug := fmt.Sprintf("ai-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "AI HTTP"))
	must(repo.ProvisionReviewer(ctx, slug, slug+"-reviewer", "unused"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	reviewer, e := repo.FindUser(ctx, slug+"-reviewer")
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	var viewer int64
	must(pool.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, slug+"-viewer").Scan(&viewer))
	_, e = pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'viewer')`, ws.ID, viewer)
	must(e)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, reviewer.ID, viewer})
	})
	session := func(id int64) string {
		token, e := sessionToken()
		must(e)
		must(repo.CreateSession(ctx, id, sessionHash(token), time.Now().Add(time.Hour)))
		return token
	}
	ownerCookie, reviewCookie, viewerCookie := session(owner.ID), session(reviewer.ID), session(viewer)
	handler := New(repo, "https://console.invalid", true)
	base := "/api/v1/workspaces/" + slug + "/ai-security"
	call := func(method, path string, body any, cookie, origin string, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, e := json.Marshal(body)
		must(e)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s status %d want%d: %.250s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	origin := "https://console.invalid"
	call("GET", base, nil, viewerCookie, origin, 200)
	call("GET", base+"/runtime-config", nil, ownerCookie, origin, 409)
	call("POST", base+"/evaluations", struct{}{}, viewerCookie, origin, 403)
	cfg := aisecurity.DefaultConfig()
	cfg.Policy.MaxRequestAgeSeconds = 5
	cfg.Applications = []aisecurity.Application{{ID: "application", Enabled: true, PrincipalIDs: []string{"principal"}, DocumentIDs: []string{"document"}}}
	cfg.Principals = []aisecurity.Principal{{ID: "principal", Enabled: true, ApplicationIDs: []string{"application"}}}
	cfg.Documents = []aisecurity.Document{{ID: "document", Version: "v1", ACLVersion: "acl-v1", KnowledgeBaseID: "kb", Enabled: true, AllowedPrincipalIDs: []string{"principal"}}}
	input := map[string]any{"expectedRevision": 0, "configuration": cfg}
	call("PUT", base+"/configuration", input, ownerCookie, "", 403)
	call("PUT", base+"/configuration", input, reviewCookie, origin, 403)
	call("PUT", base+"/configuration", input, ownerCookie, origin, 200)
	call("PUT", base+"/configuration", input, ownerCookie, origin, 409)
	var batch store.AISecurityEvaluation
	must(json.Unmarshal(call("POST", base+"/evaluations", struct{}{}, ownerCookie, origin, 201).Body.Bytes(), &batch))
	if batch.Status != "passed" {
		t.Fatalf("short TTL live config gate failed %+v", batch.Cases)
	}
	call("POST", base+"/publish", map[string]any{"expectedRevision": 1, "evaluationId": batch.ID, "note": "Legacy must not publish"}, reviewCookie, origin, 422)
	p := store.BaselineAISecurityRegressionPack()
	call("GET", base+"/regression-packs/baseline", nil, viewerCookie, origin, 200)
	call("POST", base+"/regression-packs/validate", map[string]any{"pack": p}, viewerCookie, origin, 403)
	call("POST", base+"/regression-packs/validate", map[string]any{"pack": p}, ownerCookie, origin, 200)
	call("POST", base+"/regression-packs", map[string]any{"expectedLatestVersion": "", "pack": p}, reviewCookie, origin, 403)
	var pack store.AISecurityRegressionPackSummary
	must(json.Unmarshal(call("POST", base+"/regression-packs", map[string]any{"expectedLatestVersion": "", "pack": p}, ownerCookie, origin, 201).Body.Bytes(), &pack))
	call("GET", base+"/regression-packs/"+fmt.Sprint(pack.ID), nil, viewerCookie, origin, 200)
	must(json.Unmarshal(call("POST", base+"/evaluations", map[string]any{"packId": pack.ID}, ownerCookie, origin, 201).Body.Bytes(), &batch))
	if !batch.GateEligible {
		t.Fatalf("versioned gate failed: %+v", batch)
	}
	publish := map[string]any{"expectedRevision": 1, "evaluationId": batch.ID, "note": "Independent HTTP review"}
	call("POST", base+"/publish", publish, ownerCookie, origin, 403)
	call("POST", base+"/publish", publish, reviewCookie, origin, 200)
	call("GET", base+"/runtime-config", nil, viewerCookie, origin, 200)
	now := time.Now().UTC()
	request := aisecurity.Request{RequestID: "http-raw-body-check-001", Kind: "rag", ApplicationID: "application", PrincipalID: "principal", DocumentIDs: []string{"document"}, Input: "raw-body-secret-must-not-return", IssuedAt: now, ExpiresAt: now.Add(4 * time.Second)}
	response := call("POST", base+"/evaluate", request, ownerCookie, origin, 201)
	if strings.Contains(response.Body.String(), request.Input) || !strings.Contains(response.Body.String(), `"mode":"evaluation"`) {
		t.Fatal("preview returned raw content or omitted its mode")
	}
	export := call("GET", base+"/export", nil, viewerCookie, origin, 200)
	if strings.Contains(export.Body.String(), request.Input) || !strings.Contains(export.Body.String(), "Independent HTTP review") {
		t.Fatal("export leaked raw body or lost independent publication evidence")
	}
	var obj map[string]any
	raw, _ := json.Marshal(request)
	must(json.Unmarshal(raw, &obj))
	obj["trustedAcl"] = []string{"all"}
	call("POST", base+"/evaluate", obj, ownerCookie, origin, 400)
	call("GET", "/api/v1/workspaces/not-a-workspace/ai-security", nil, ownerCookie, origin, 404)
}
