package httpapi

import (
	"bytes"
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
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSecurityPlatformHTTPIndependentReviewAndOrigin(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	if err = repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprint(time.Now().UnixNano())
	slugs := []string{"platform-http-a-" + suffix, "platform-http-r-" + suffix, "platform-http-b-" + suffix}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, slugs)
		_, _ = pool.Exec(ctx, `DELETE FROM plume.users WHERE username=ANY($1)`, slugs)
	})
	users := []store.User{}
	tokens := []string{strings.Repeat("a", 43), strings.Repeat("r", 43), strings.Repeat("b", 43)}
	for i, slug := range slugs {
		if err = repo.Bootstrap(ctx, slug, "unused", slug, slug); err != nil {
			t.Fatal(err)
		}
		user, e := repo.FindUser(ctx, slug)
		if e != nil {
			t.Fatal(e)
		}
		users = append(users, user)
		if err = repo.CreateSession(ctx, user.ID, sessionHash(tokens[i]), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := repo.Membership(ctx, users[0].ID, slugs[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'reviewer')`, ws.ID, users[1].ID); err != nil {
		t.Fatal(err)
	}
	const origin = "https://platform.example.invalid"
	handler := New(repo, origin, false)
	base := "/api/v1/workspaces/" + ws.Slug + "/security-platform"
	do := func(method, path, body, token, requestOrigin string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", requestOrigin)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "scarlet-session", Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	do("GET", base, "", "", "", 401)
	do("GET", base, "", tokens[2], origin, 404)
	do("POST", base+"/snapshots", "{}", tokens[0], "https://untrusted.invalid", 403)
	do("GET", base, "", tokens[1], origin, 200)
	binding := do("POST", base+"/network-bindings", `{"site":"Branch","circuit":"L01","vrf":"main","applicationRef":"ai-app","assetIds":[],"activityIds":[],"region":"CN","receiver":"HQ","purpose":"Enterprise knowledge","evidenceRef":"design-v1"}`, tokens[0], origin, 200)
	var b store.SecurityNetworkBinding
	if err = json.Unmarshal(binding.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	change := do("POST", base+"/network-changes", fmt.Sprintf(`{"bindingId":%d,"expectedRevision":%d,"proposedRegion":"SG","proposedApplicationRef":"ai-app-v2","reason":"Model change"}`, b.ID, b.Revision), tokens[0], origin, 201)
	var c store.SecurityNetworkChange
	if err = json.Unmarshal(change.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	reviewPath := fmt.Sprintf("%s/network-changes/%d/review", base, c.ID)
	do("POST", reviewPath, `{"decision":"accepted","note":"self"}`, tokens[0], origin, 403)
	do("POST", reviewPath, `{"decision":"accepted","note":"Scope reviewed; no network execution claimed"}`, tokens[1], origin, 200)
	do("POST", base+"/snapshots", "{}", tokens[1], origin, 403)
	do("POST", base+"/snapshots", "{}", tokens[0], origin, 201)
	do("POST", base+"/network-bindings", `{"site":"A","unexpected":"field"}`, tokens[0], origin, 400)
	if w := do("GET", base+"/export", "", tokens[0], origin, 200); !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("export missing attachment header")
	}

	aiBase := "/api/v1/workspaces/" + ws.Slug + "/ai-security"
	do("GET", aiBase+"/runtime-evidence", "", tokens[2], origin, 404)
	do("POST", aiBase+"/runtime-sources", `{"name":"Runtime","applicationId":"app-a","catalogDigest":"`+strings.Repeat("a", 64)+`"}`, tokens[1], origin, 403)
	config := aisecurity.DefaultConfig()
	config.Applications = []aisecurity.Application{{ID: "app-a", Name: "Approved enterprise app", Enabled: true, PrincipalIDs: []string{}, DocumentIDs: []string{}, ModelIDs: []string{}, ToolIDs: []string{}}}
	digest := aisecurity.ConfigDigest(config)
	configBody, _ := json.Marshal(config)
	if _, err = pool.Exec(ctx, `INSERT INTO plume.ai_security_configurations(workspace_id,revision,configuration,updated_by,published_revision,published_configuration,published_digest,published_by,published_at) VALUES($1,1,$2,$3,1,$2,$4,$3,now())`, ws.ID, configBody, users[0].ID, digest); err != nil {
		t.Fatal(err)
	}
	created := do("POST", aiBase+"/runtime-sources", fmt.Sprintf(`{"name":"Customer runtime","applicationId":"app-a","catalogDigest":%q}`, digest), tokens[0], origin, 201)
	var source struct {
		Source store.AISecurityRuntimeSource `json:"source"`
		Token  string                        `json:"token"`
	}
	if err = json.Unmarshal(created.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(source.Token, aiSecurityRuntimePrefix) {
		t.Fatal("independent source token not created")
	}
	event := aisecurity.AuditRecord{EventID: strings.Repeat("a", 32), RequestID: "runtime-http-fixture", Stage: "completed", ObservedAt: time.Now().UTC(), Decision: aisecurity.Decision{Outcome: "allow", Code: "allowed", CatalogVersion: config.Version, CatalogDigest: digest, RequestDigest: strings.Repeat("b", 64), Kind: "model", ApplicationID: "app-a", PrincipalID: "employee-a", Rules: []string{}, Categories: []string{}, Coverage: []string{}, AllowedDocumentIDs: []string{}}, Execution: aisecurity.Execution{Attempted: true, Outcome: "succeeded", HTTPStatus: 200}}
	eventBody, _ := json.Marshal(event)
	machine := func(body []byte, want int) {
		t.Helper()
		req := httptest.NewRequest("POST", aiSecurityRuntimePath, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+source.Token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("machine receipt HTTP %d want %d: %s", w.Code, want, w.Body)
		}
	}
	machine([]byte(`{"input":"must not persist model content"}`), 400)
	machine(eventBody, 200)
	machine(eventBody, 200)
	evidence := do("GET", aiBase+"/runtime-evidence", "", tokens[0], origin, 200)
	if strings.Contains(evidence.Body.String(), source.Token) || strings.Contains(evidence.Body.String(), "token_hash") {
		t.Fatal("source credential leaked into evidence")
	}
	do("POST", fmt.Sprintf("%s/runtime-sources/%d/revoke", aiBase, source.Source.ID), "{}", tokens[0], origin, 200)
	machine(eventBody, 401)
}
