package httpapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
	"github.com/hjh006/scarlet-plume/backend/pkg/enterpriseidentity"
)

const scimUserSchema = "urn:ietf:params:scim:schemas:core:2.0:User"
const scimGroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
const scimPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"

func (s *server) identityRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/ai-gateway/identity"
	for _, v := range []struct {
		pattern string
		fn      workspaceAction
	}{
		{"GET " + base, s.getIdentitySettings}, {"PUT " + base, s.saveIdentitySettings},
		{"GET " + base + "/users", s.getIdentityUsers}, {"POST " + base + "/scim-token", s.rotateIdentityToken},
	} {
		mux.HandleFunc(v.pattern, s.workspace(false, func(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
			if ws.Role != "owner" {
				fail(w, 403, "仅工作区Owner可以管理企业身份。")
				return
			}
			v.fn(w, r, u, ws)
		}))
	}
	mux.HandleFunc("GET /api/v1/identity/{slug}/oidc/start", s.startOIDC)
	mux.HandleFunc("GET /api/v1/identity/{slug}/oidc/callback", s.finishOIDC)
	for _, kind := range []string{"Users", "Groups"} {
		mux.HandleFunc("GET /scim/v2/{slug}/"+kind, s.scimList)
		mux.HandleFunc("POST /scim/v2/{slug}/"+kind, s.scimWrite)
		mux.HandleFunc("GET /scim/v2/{slug}/"+kind+"/{identity}", s.scimGet)
		mux.HandleFunc("PUT /scim/v2/{slug}/"+kind+"/{identity}", s.scimWrite)
		mux.HandleFunc("PATCH /scim/v2/{slug}/"+kind+"/{identity}", s.scimWrite)
		mux.HandleFunc("DELETE /scim/v2/{slug}/"+kind+"/{identity}", s.scimDelete)
	}
	mux.HandleFunc("GET /scim/v2/{slug}/ServiceProviderConfig", s.scimServiceConfig)
}

func identityFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrInvalid) {
		fail(w, 400, "企业身份字段无效，请核对HTTPS issuer、客户端和环境秘密引用。")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		fail(w, 409, "身份配置或账号版本已变化；已有账号时不能更换issuer或clientId。")
		return
	}
	storeFailure(w, err)
}
func (s *server) getIdentitySettings(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	out, e := s.store.IdentitySettings(r.Context(), ws.ID, u.ID)
	if e != nil {
		identityFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) saveIdentitySettings(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in struct {
		ExpectedRevision int64             `json:"expectedRevision"`
		Enabled          bool              `json:"enabled"`
		Issuer           string            `json:"issuer"`
		ClientID         string            `json:"clientId"`
		ClientSecretRef  string            `json:"clientSecretRef"`
		SCIMEnabled      bool              `json:"scimEnabled"`
		DefaultRole      string            `json:"defaultRole"`
		GroupRoles       map[string]string `json:"groupRoles"`
	}
	if !readIdentityJSON(w, r, &in) {
		return
	}
	out, e := s.store.SaveIdentitySettings(r.Context(), ws.ID, u.ID, store.IdentitySettings{Revision: in.ExpectedRevision, Enabled: in.Enabled, Issuer: in.Issuer, ClientID: in.ClientID, ClientSecretRef: in.ClientSecretRef, SCIMEnabled: in.SCIMEnabled, DefaultRole: in.DefaultRole, GroupRoles: in.GroupRoles})
	if e != nil {
		identityFailure(w, e)
		return
	}
	respond(w, 200, out)
}
func (s *server) getIdentityUsers(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	out, e := s.store.IdentityUsers(r.Context(), ws.ID, u.ID)
	if e != nil {
		identityFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": out, "limit": 1000, "truncated": len(out) == 1000})
}
func (s *server) rotateIdentityToken(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var empty struct{}
	if !readIdentityJSON(w, r, &empty) {
		return
	}
	token, e := sessionToken()
	if e != nil {
		identityFailure(w, e)
		return
	}
	if e = s.store.RotateIdentitySCIMToken(r.Context(), ws.ID, u.ID, sessionHash(token)); e != nil {
		identityFailure(w, e)
		return
	}
	c, e := s.store.IdentitySettings(r.Context(), ws.ID, u.ID)
	if e != nil {
		identityFailure(w, e)
		return
	}
	respond(w, 201, map[string]any{"token": token, "revision": c.Revision})
}

func readIdentityJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	reject := func(status int, detail string) {
		if strings.HasPrefix(r.URL.Path, "/scim/v2/") {
			scimFail(w, status, "invalidSyntax", detail)
		} else {
			fail(w, status, detail)
		}
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || (media != "application/json" && media != "application/scim+json") {
		reject(415, "JSON required.")
		return false
	}
	if r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity" {
		reject(415, "Compressed identity requests are unsupported.")
		return false
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 131072))
	if e != nil {
		reject(413, "Identity request exceeds 128KiB.")
		return false
	}
	facts, _, e := datasafety.InspectJSON(bytes.NewReader(raw))
	if e != nil || facts.Capture != "complete" {
		reject(400, "Ambiguous or invalid identity JSON.")
		return false
	}
	if !identityJSONKeys(json.NewDecoder(bytes.NewReader(raw))) {
		reject(400, "Case-ambiguous identity fields.")
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(target); e != nil {
		reject(400, "Invalid or unsupported identity fields.")
		return false
	}
	return true
}

// encoding/json matches struct fields without case sensitivity. Reject the
// corresponding aliases too, including inside SCIM PATCH value objects.
func identityJSONKeys(d *json.Decoder) bool {
	t, e := d.Token()
	if e != nil {
		return false
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return true
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, e := d.Token()
			name, ok := key.(string)
			if e != nil || !ok {
				return false
			}
			fold := strings.Map(func(r rune) rune {
				min := r
				for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
					if next < min {
						min = next
					}
				}
				return min
			}, name)
			if seen[fold] {
				return false
			}
			seen[fold] = true
		}
		if !identityJSONKeys(d) {
			return false
		}
	}
	_, e = d.Token()
	return e == nil
}

func (s *server) oidcClient(r *http.Request, c store.IdentitySettings) (*enterpriseidentity.Client, error) {
	if !strings.HasPrefix(c.ClientSecretRef, "env:SCARLET_IDP_") {
		return nil, store.ErrInvalid
	}
	secret := os.Getenv(strings.TrimPrefix(c.ClientSecretRef, "env:"))
	if secret == "" {
		return nil, store.ErrInvalid
	}
	// Operators control the exact issuer allowlist as well as the secret's environment reference.
	allowed := false
	for _, issuer := range strings.Split(os.Getenv("SCARLET_OIDC_ALLOWED_ISSUERS"), ",") {
		if strings.TrimSpace(issuer) == c.Issuer {
			allowed = true
		}
	}
	if !allowed {
		return nil, store.ErrInvalid
	}
	return enterpriseidentity.New(r.Context(), enterpriseidentity.Config{Issuer: c.Issuer, ClientID: c.ClientID, ClientSecret: secret, RedirectURL: s.origin + "/api/v1/identity/" + r.PathValue("slug") + "/oidc/callback"}, s.identityHTTPClient)
}
func (s *server) oidcCookieName(slug string) string { return "scarlet-oidc-" + sessionHash(slug)[:12] }
func (s *server) oidcCookie(w http.ResponseWriter, slug, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: s.oidcCookieName(slug), Value: value, Path: "/api/v1/identity/" + slug + "/oidc/callback", MaxAge: maxAge, Secure: s.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}
func (s *server) startOIDC(w http.ResponseWriter, r *http.Request) {
	ws, c, e := s.store.IdentityLoginConfig(r.Context(), r.PathValue("slug"))
	if e != nil {
		identityFailure(w, e)
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	default:
		fail(w, 429, "企业登录请求过多。")
		return
	}
	client, e := s.oidcClient(r, c)
	if e != nil {
		fail(w, 503, "企业身份尚未就绪，请检查issuer允许列表、HTTPS回调和秘密引用。")
		return
	}
	values := make([]string, 4)
	for i := range values {
		values[i], e = sessionToken()
		if e != nil {
			identityFailure(w, e)
			return
		}
	}
	state, browser, nonce, verifier := values[0], values[1], values[2], values[3]
	e = s.store.BeginIdentityLogin(r.Context(), store.IdentityLoginState{WorkspaceID: ws, SettingsRevision: c.Revision, StateHash: sessionHash(state), BrowserHash: sessionHash(browser), Nonce: nonce, Verifier: verifier, ExpiresAt: time.Now().Add(5 * time.Minute)})
	if e != nil {
		identityFailure(w, e)
		return
	}
	s.oidcCookie(w, r.PathValue("slug"), browser, 300)
	http.Redirect(w, r, client.AuthorizationURL(state, nonce, verifier), http.StatusFound)
}
func (s *server) finishOIDC(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cookie, e := r.Cookie(s.oidcCookieName(r.PathValue("slug")))
	if e != nil || len(cookie.Value) != 43 || len(q["state"]) != 1 || len(q.Get("state")) != 43 || len(q["code"]) != 1 || len(q.Get("code")) == 0 || len(q.Get("code")) > 4096 || q.Get("error") != "" {
		fail(w, 401, "企业登录状态无效，请重新发起登录。")
		return
	}
	s.oidcCookie(w, r.PathValue("slug"), "", -1)
	ws, c, e := s.store.IdentityLoginConfig(r.Context(), r.PathValue("slug"))
	if e != nil {
		fail(w, 401, "企业登录已失效。")
		return
	}
	state, e := s.store.ConsumeIdentityLogin(r.Context(), ws, sessionHash(q.Get("state")), sessionHash(cookie.Value))
	if e != nil || state.SettingsRevision != c.Revision {
		fail(w, 401, "企业登录状态过期或已使用。")
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	default:
		fail(w, 429, "企业登录请求过多。")
		return
	}
	client, e := s.oidcClient(r, c)
	if e != nil {
		fail(w, 503, "企业身份暂时不可用。")
		return
	}
	subject, e := client.Subject(r.Context(), q.Get("code"), state.Nonce, state.Verifier)
	if e != nil {
		fail(w, 401, "企业身份验证未通过。")
		return
	}
	token, e := sessionToken()
	if e != nil {
		identityFailure(w, e)
		return
	}
	_, e = s.store.CompleteIdentityLogin(r.Context(), ws, c.Revision, c.Issuer, subject, sessionHash(token), time.Now().Add(sessionDuration))
	if e != nil {
		fail(w, 403, "此企业账号未开通、已停用或授权已变化。")
		return
	}
	if old, e := r.Cookie(s.cookieName()); e == nil && len(old.Value) == 43 {
		_ = s.store.DeleteSession(r.Context(), sessionHash(old.Value))
	}
	s.setSession(w, token, int(sessionDuration.Seconds()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func scimRespond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func scimFail(w http.ResponseWriter, status int, kind, detail string) {
	scimRespond(w, status, map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:Error"}, "status": strconv.Itoa(status), "scimType": kind, "detail": detail})
}
func scimFailure(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, store.ErrInvalid):
		scimFail(w, 400, "invalidValue", "Invalid identity fields or unsupported operation.")
	case errors.Is(e, store.ErrConflict):
		scimFail(w, 409, "uniqueness", "Identity already exists or its version changed.")
	case errors.Is(e, store.ErrNotFound):
		scimFail(w, 404, "noTarget", "Identity or referenced member was not found.")
	default:
		scimFail(w, 503, "", "Identity store unavailable.")
	}
}
func (s *server) scimAuth(w http.ResponseWriter, r *http.Request) (string, bool) {
	a := r.Header.Get("Authorization")
	if !strings.HasPrefix(a, "Bearer ") || len(a) != 50 {
		scimFail(w, 401, "", "Provisioning credential required.")
		return "", false
	}
	h := sessionHash(strings.TrimPrefix(a, "Bearer "))
	// Verify on every read/write; each mutation checks again under the settings row lock.
	if _, _, e := s.store.SCIMIdentityUsers(r.Context(), r.PathValue("slug"), h, 0, "", "", 0, 0); e != nil {
		if errors.Is(e, store.ErrNotFound) {
			scimFail(w, 401, "", "Provisioning credential rejected.")
		} else {
			scimFailure(w, e)
		}
		return "", false
	}
	return h, true
}
func scimIsGroup(r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return len(parts) > 3 && parts[3] == "Groups"
}
func scimID(r *http.Request) (int64, error) {
	id, e := strconv.ParseInt(r.PathValue("identity"), 10, 64)
	if e != nil || id < 1 {
		return 0, store.ErrInvalid
	}
	return id, nil
}
func scimVersion(revision int64) string { return fmt.Sprintf(`W/"%d"`, revision) }
func scimMatch(w http.ResponseWriter, r *http.Request, revision int64) bool {
	if v := r.Header.Get("If-Match"); v != "" && subtle.ConstantTimeCompare([]byte(v), []byte(scimVersion(revision))) != 1 {
		scimFail(w, 412, "", "Identity version changed.")
		return false
	}
	return true
}
func (s *server) scimLocation(r *http.Request, kind string, id int64) string {
	return s.origin + "/scim/v2/" + r.PathValue("slug") + "/" + kind + "/" + strconv.FormatInt(id, 10)
}
func (s *server) scimUser(r *http.Request, u store.IdentityUser) map[string]any {
	return map[string]any{"schemas": []string{scimUserSchema}, "id": strconv.FormatInt(u.ID, 10), "externalId": u.ExternalID, "userName": u.UserName, "displayName": u.DisplayName, "active": u.Active, "meta": map[string]any{"resourceType": "User", "version": scimVersion(u.Revision), "lastModified": u.UpdatedAt, "location": s.scimLocation(r, "Users", u.ID)}}
}
func (s *server) scimGroup(r *http.Request, g store.IdentityGroup) map[string]any {
	members := []map[string]string{}
	for _, id := range g.Members {
		members = append(members, map[string]string{"value": strconv.FormatInt(id, 10), "$ref": s.scimLocation(r, "Users", id)})
	}
	return map[string]any{"schemas": []string{scimGroupSchema}, "id": strconv.FormatInt(g.ID, 10), "externalId": g.ExternalID, "displayName": g.DisplayName, "members": members, "meta": map[string]any{"resourceType": "Group", "version": scimVersion(g.Revision), "lastModified": g.UpdatedAt, "location": s.scimLocation(r, "Groups", g.ID)}}
}
func (s *server) scimServiceConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.scimAuth(w, r); !ok {
		return
	}
	scimRespond(w, 200, map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"}, "patch": map[string]bool{"supported": true}, "bulk": map[string]bool{"supported": false}, "filter": map[string]any{"supported": true, "maxResults": 200}, "changePassword": map[string]bool{"supported": false}, "sort": map[string]bool{"supported": false}, "etag": map[string]bool{"supported": true}, "authenticationSchemes": []map[string]any{{"type": "oauthbearertoken", "name": "Bearer token", "description": "Workspace-scoped provisioning token", "primary": true}}})
}
func scimPage(r *http.Request) (start, count int, id int64, external, userName string, e error) {
	start, count = 0, 100
	q := r.URL.Query()
	for k, v := range q {
		if len(v) != 1 || (k != "startIndex" && k != "count" && k != "filter") {
			e = store.ErrInvalid
			return
		}
	}
	if v := q.Get("startIndex"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000000 {
			e = store.ErrInvalid
			return
		}
		start = n - 1
	}
	if v := q.Get("count"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 200 {
			e = store.ErrInvalid
			return
		}
		count = n
	}
	if v := q.Get("filter"); v != "" {
		parts := strings.SplitN(v, " eq ", 2)
		if len(parts) != 2 {
			e = store.ErrInvalid
			return
		}
		var value string
		if json.Unmarshal([]byte(parts[1]), &value) != nil || value == "" || len(value) > 1024 {
			e = store.ErrInvalid
			return
		}
		switch parts[0] {
		case "id":
			id, e = strconv.ParseInt(value, 10, 64)
			if id < 1 {
				e = store.ErrInvalid
			}
		case "externalId":
			external = value
		case "userName":
			if scimIsGroup(r) {
				e = store.ErrInvalid
			} else {
				userName = value
			}
		case "displayName":
			if scimIsGroup(r) {
				userName = value
			} else {
				e = store.ErrInvalid
			}
		default:
			e = store.ErrInvalid
		}
	}
	return
}
func (s *server) scimList(w http.ResponseWriter, r *http.Request) {
	h, ok := s.scimAuth(w, r)
	if !ok {
		return
	}
	start, count, id, external, name, e := scimPage(r)
	if e != nil {
		scimFail(w, 400, "invalidFilter", "Only exact id, externalId, userName or group displayName filters are supported.")
		return
	}
	resources := []map[string]any{}
	var total int
	if scimIsGroup(r) {
		var items []store.IdentityGroup
		items, total, e = s.store.SCIMIdentityGroups(r.Context(), r.PathValue("slug"), h, id, external, name, start, count)
		for _, v := range items {
			resources = append(resources, s.scimGroup(r, v))
		}
	} else {
		var items []store.IdentityUser
		items, total, e = s.store.SCIMIdentityUsers(r.Context(), r.PathValue("slug"), h, id, external, name, start, count)
		for _, v := range items {
			resources = append(resources, s.scimUser(r, v))
		}
	}
	if e != nil {
		scimFailure(w, e)
		return
	}
	scimRespond(w, 200, map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"}, "totalResults": total, "startIndex": start + 1, "itemsPerPage": len(resources), "Resources": resources})
}
func (s *server) scimGet(w http.ResponseWriter, r *http.Request) {
	h, ok := s.scimAuth(w, r)
	if !ok {
		return
	}
	id, e := scimID(r)
	if e != nil {
		scimFailure(w, e)
		return
	}
	if scimIsGroup(r) {
		items, _, e := s.store.SCIMIdentityGroups(r.Context(), r.PathValue("slug"), h, id, "", "", 0, 1)
		if e != nil {
			scimFailure(w, e)
			return
		}
		if len(items) == 0 {
			scimFailure(w, store.ErrNotFound)
			return
		}
		w.Header().Set("ETag", scimVersion(items[0].Revision))
		scimRespond(w, 200, s.scimGroup(r, items[0]))
	} else {
		items, _, e := s.store.SCIMIdentityUsers(r.Context(), r.PathValue("slug"), h, id, "", "", 0, 1)
		if e != nil {
			scimFailure(w, e)
			return
		}
		if len(items) == 0 {
			scimFailure(w, store.ErrNotFound)
			return
		}
		w.Header().Set("ETag", scimVersion(items[0].Revision))
		scimRespond(w, 200, s.scimUser(r, items[0]))
	}
}
func (s *server) scimDelete(w http.ResponseWriter, r *http.Request) {
	h, ok := s.scimAuth(w, r)
	if !ok {
		return
	}
	id, e := scimID(r)
	if e != nil {
		scimFailure(w, e)
		return
	}
	expected := int64(0)
	if r.Header.Get("If-Match") != "" {
		if scimIsGroup(r) {
			items, _, err := s.store.SCIMIdentityGroups(r.Context(), r.PathValue("slug"), h, id, "", "", 0, 1)
			if err != nil {
				scimFailure(w, err)
				return
			}
			if len(items) == 0 {
				scimFailure(w, store.ErrNotFound)
				return
			}
			expected = items[0].Revision
		} else {
			items, _, err := s.store.SCIMIdentityUsers(r.Context(), r.PathValue("slug"), h, id, "", "", 0, 1)
			if err != nil {
				scimFailure(w, err)
				return
			}
			if len(items) == 0 {
				scimFailure(w, store.ErrNotFound)
				return
			}
			expected = items[0].Revision
		}
		if !scimMatch(w, r, expected) {
			return
		}
	}
	// Deletion is serialized with provisioning and immediately removes sessions and grants.
	if scimIsGroup(r) {
		e = s.store.DeleteSCIMIdentityGroup(r.Context(), r.PathValue("slug"), h, id, expected)
	} else {
		e = s.store.DeleteSCIMIdentityUser(r.Context(), r.PathValue("slug"), h, id, expected)
	}
	if e != nil {
		if expected != 0 && errors.Is(e, store.ErrConflict) {
			scimFail(w, 412, "", "Identity version changed.")
			return
		}
		scimFailure(w, e)
		return
	}
	scimRespond(w, 204, nil)
}
