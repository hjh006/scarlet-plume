package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

type identityHTTPFixture struct {
	pool         *pgxpool.Pool
	repo         *store.Store
	owner        store.User
	ws           store.Workspace
	handler      http.Handler
	cookie       *http.Cookie
	viewerCookie *http.Cookie
}

func newIdentityHTTPFixture(t *testing.T) identityHTTPFixture {
	t.Helper()
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	pool, e := pgxpool.New(t.Context(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	repo := store.New(pool)
	if e := repo.Migrate(t.Context()); e != nil {
		t.Fatal(e)
	}
	slug := fmt.Sprintf("identity-http-%d", time.Now().UnixNano())
	if e := repo.Bootstrap(t.Context(), slug, "unused", slug, "Enterprise identity test"); e != nil {
		t.Fatal(e)
	}
	owner, e := repo.FindUser(t.Context(), slug)
	if e != nil {
		t.Fatal(e)
	}
	ws, e := repo.Membership(t.Context(), owner.ID, slug)
	if e != nil {
		t.Fatal(e)
	}
	var viewer int64
	if e := pool.QueryRow(t.Context(), `INSERT INTO plume.users(username,password_hash) VALUES($1,'unused') RETURNING id`, slug+"-viewer").Scan(&viewer); e != nil {
		t.Fatal(e)
	}
	if _, e := pool.Exec(t.Context(), `INSERT INTO plume.memberships(workspace_id,user_id,role) VALUES($1,$2,'viewer')`, ws.ID, viewer); e != nil {
		t.Fatal(e)
	}
	cookie := func(id int64) *http.Cookie {
		token, e := sessionToken()
		if e != nil {
			t.Fatal(e)
		}
		if e := repo.CreateSession(t.Context(), id, sessionHash(token), time.Now().Add(time.Hour)); e != nil {
			t.Fatal(e)
		}
		return &http.Cookie{Name: "__Host-scarlet-session", Value: token}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, `DELETE FROM plume.users WHERE id IN(SELECT user_id FROM plume.ai_identity_users WHERE workspace_id=$1)`, ws.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM plume.users WHERE id=ANY($1)`, []int64{owner.ID, viewer})
	})
	return identityHTTPFixture{pool, repo, owner, ws, New(repo, "https://app.example.invalid", true), cookie(owner.ID), cookie(viewer)}
}
func identityHTTPRequest(t *testing.T, h http.Handler, method, path, body string, cookie *http.Cookie, token string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
		r.Header.Set("Origin", "https://app.example.invalid")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func TestEnterpriseIdentityManagementAndSCIMHTTP(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-gateway/identity"
	identityHTTPRequest(t, f.handler, "GET", base, "", nil, "", 401)
	identityHTTPRequest(t, f.handler, "GET", base, "", f.viewerCookie, "", 403)
	identityHTTPRequest(t, f.handler, "PUT", base, `{"expectedRevision":0,"enabled":true,"issuer":"https://issuer.example.invalid","clientId":"client","clientSecretRef":"env:SCARLET_IDP_TEST_SECRET","scimEnabled":true,"defaultRole":"viewer","groupRoles":{"operators":"operator"}}`, f.cookie, "", 200)
	identityHTTPRequest(t, f.handler, "PUT", base, `{"expectedRevision":0,"enabled":false,"defaultRole":"viewer","groupRoles":{}}`, f.cookie, "", 409)
	w := identityHTTPRequest(t, f.handler, "POST", base+"/scim-token", `{}`, f.cookie, "", 201)
	var credential struct {
		Token    string `json:"token"`
		Revision int64  `json:"revision"`
	}
	if json.Unmarshal(w.Body.Bytes(), &credential) != nil || len(credential.Token) != 43 || credential.Revision != 2 {
		t.Fatal("missing one-time provisioning credential")
	}
	w = identityHTTPRequest(t, f.handler, "GET", base, "", f.cookie, "", 200)
	if bytes.Contains(w.Body.Bytes(), []byte(credential.Token)) || bytes.Contains(w.Body.Bytes(), []byte(sessionHash(credential.Token))) {
		t.Fatal("identity management exposed token or hash")
	}
	scim := "/scim/v2/" + f.ws.Slug
	identityHTTPRequest(t, f.handler, "GET", scim+"/ServiceProviderConfig", "", nil, credential.Token, 200)
	identityHTTPRequest(t, f.handler, "GET", scim+"/Users", "", nil, strings.Repeat("x", 43), 401)
	w = identityHTTPRequest(t, f.handler, "POST", scim+"/Users", `{"schemas":["`+scimUserSchema+`"],"externalId":"test-subject","userName":"employee@example.invalid","active":true}`, nil, credential.Token, 201)
	var user struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
	}
	if json.Unmarshal(w.Body.Bytes(), &user) != nil || user.ID == "" {
		t.Fatal("SCIM user missing")
	}
	userID, e := strconv.ParseInt(user.ID, 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	identityHTTPRequest(t, f.handler, "POST", scim+"/Groups", `{"schemas":["`+scimGroupSchema+`"],"externalId":"operators","displayName":"Operators","members":[{"value":"`+user.ID+`"}]}`, nil, credential.Token, 201)
	items, e := f.repo.IdentityUsers(t.Context(), f.ws.ID, f.owner.ID)
	if e != nil || len(items) != 1 || items[0].Role != "operator" {
		t.Fatal("SCIM group mapping not enforced")
	}
	config, e := f.repo.IdentitySettings(t.Context(), f.ws.ID, f.owner.ID)
	if e != nil {
		t.Fatal(e)
	}
	session := sessionHash("separate-identity-session")
	if _, e := f.repo.CompleteIdentityLogin(t.Context(), f.ws.ID, config.Revision, config.Issuer, "test-subject", session, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	identityHTTPRequest(t, f.handler, "PATCH", scim+"/Users/"+user.ID, `{"schemas":["`+scimPatchSchema+`"],"Operations":[{"op":"replace","path":"active","value":false}]}`, nil, credential.Token, 200)
	if _, e := f.repo.SessionUser(t.Context(), session); e == nil {
		t.Fatal("SCIM disable retained the active session")
	}
	identityHTTPRequest(t, f.handler, "GET", scim+"/Users?filter=userName%20eq%20%22EMPLOYEE%40EXAMPLE.INVALID%22", "", nil, credential.Token, 200)
	identityHTTPRequest(t, f.handler, "PATCH", scim+"/Users/"+user.ID, `{"schemas":["`+scimPatchSchema+`"],"Operations":[{"op":"replace","path":"role","value":"owner"}]}`, nil, credential.Token, 400)
	identityHTTPRequest(t, f.handler, "PATCH", scim+"/Users/"+user.ID, `{"schemas":["`+scimPatchSchema+`"],"Operations":[{"op":"replace","path":"","value":{"active":false,"Active":true}}]}`, nil, credential.Token, 400)
	identityHTTPRequest(t, f.handler, "PATCH", scim+"/Users/"+user.ID, `{"schemas":["`+scimPatchSchema+`"],"Operations":[{"op":"replace","path":"active","value":null}]}`, nil, credential.Token, 400)
	request := httptest.NewRequest("PATCH", scim+"/Users/"+user.ID, strings.NewReader(`{"schemas":["`+scimPatchSchema+`"],"Operations":[{"op":"replace","path":"active","value":true}]}`))
	request.Header.Set("Content-Type", "application/scim+json")
	request.Header.Set("Authorization", "Bearer "+credential.Token)
	request.Header.Set("If-Match", `W/"1"`)
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, request)
	if w.Code != 412 {
		t.Fatal("stale If-Match accepted")
	}
	identityHTTPRequest(t, f.handler, "DELETE", scim+"/Users/"+user.ID, "", nil, credential.Token, 204)
	identityHTTPRequest(t, f.handler, "GET", scim+"/Users/"+user.ID, "", nil, credential.Token, 404)
	if _, _, e = f.repo.SCIMIdentityUsers(t.Context(), f.ws.Slug, sessionHash(credential.Token), userID, "", "", 0, 1); e != nil {
		t.Fatal(e)
	}
	identityHTTPRequest(t, f.handler, "POST", base+"/scim-token", `{}`, f.cookie, "", 201)
	identityHTTPRequest(t, f.handler, "GET", scim+"/Users", "", nil, credential.Token, 401)
}

func TestIdentityJSONRejectsCaseAliasesAndUnsupportedSCIM(t *testing.T) {
	for _, body := range []string{`{"active":true,"Active":false}`, `{"x":{"active":true,"ACTIVE":false}}`, `{"active":true,"active":false}`, `{"active":true,"unknown":1}`} {
		r := httptest.NewRequest("POST", "/scim/v2/test/Users", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/scim+json")
		w := httptest.NewRecorder()
		var in struct {
			Active bool `json:"active"`
		}
		if readIdentityJSON(w, r, &in) || w.Code != 400 {
			t.Fatal("ambiguous or unsupported identity field accepted")
		}
	}
	if scimIsGroup(httptest.NewRequest("GET", "/scim/v2/Groups-company/Users", nil)) {
		t.Fatal("workspace slug changed the resource type")
	}
}
