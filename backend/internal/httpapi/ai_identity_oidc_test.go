package httpapi

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"golang.org/x/oauth2"
)

func TestOIDCCompleteBrowserFlowAndCallbackReplay(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	var issuer *httptest.Server
	var auth atomic.Value
	var exchanges atomic.Int32
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "alg": "RS256", "kid": "test-key", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/token":
			exchanges.Add(1)
			if r.ParseForm() != nil {
				w.WriteHeader(400)
				return
			}
			params, _ := auth.Load().(url.Values)
			if r.Form.Get("code") != "test-code" || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != params.Get("code_challenge") {
				w.WriteHeader(400)
				return
			}
			header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key"})
			body, _ := json.Marshal(map[string]any{"iss": issuer.URL, "sub": "scim-subject", "aud": "test-client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": params.Get("nonce"), "email": f.owner.Username, "roles": []string{"owner"}})
			unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
			digest := sha256.Sum256([]byte(unsigned))
			sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
			if e != nil {
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "test-only-opaque", "token_type": "Bearer", "id_token": unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)})
		default:
			w.WriteHeader(404)
		}
	}))
	defer issuer.Close()
	t.Setenv("SCARLET_IDP_HTTP_TEST_SECRET", "test-client-secret")
	t.Setenv("SCARLET_OIDC_ALLOWED_ISSUERS", issuer.URL)
	c, e := f.repo.SaveIdentitySettings(t.Context(), f.ws.ID, f.owner.ID, store.IdentitySettings{Enabled: true, Issuer: issuer.URL, ClientID: "test-client", ClientSecretRef: "env:SCARLET_IDP_HTTP_TEST_SECRET", SCIMEnabled: true, DefaultRole: "viewer", GroupRoles: map[string]string{}})
	if e != nil {
		t.Fatal(e)
	}
	provisioning := sessionHash("test-only-provisioning-token")
	if e := f.repo.RotateIdentitySCIMToken(t.Context(), f.ws.ID, f.owner.ID, provisioning); e != nil {
		t.Fatal(e)
	}
	_, e = f.repo.SaveSCIMIdentityUser(t.Context(), f.ws.Slug, provisioning, 0, store.IdentityUser{ExternalID: "scim-subject", UserName: f.owner.Username, Active: true}, 0)
	if e != nil {
		t.Fatal(e)
	}
	c, e = f.repo.IdentitySettings(t.Context(), f.ws.ID, f.owner.ID)
	if e != nil {
		t.Fatal(e)
	}
	s := &server{store: f.repo, origin: "https://app.example.invalid", secure: true, loginSlots: make(chan struct{}, 4), identityHTTPClient: issuer.Client()}
	mux := http.NewServeMux()
	s.identityRoutes(mux)
	base := "/api/v1/identity/" + f.ws.Slug + "/oidc"
	w := identityHTTPRequest(t, mux, "GET", base+"/start", "", nil, "", 302)
	location, e := url.Parse(w.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	auth.Store(location.Query())
	var browser *http.Cookie
	for _, v := range w.Result().Cookies() {
		if v.Name == s.oidcCookieName(f.ws.Slug) {
			browser = v
		}
	}
	if browser == nil || !browser.HttpOnly || !browser.Secure {
		t.Fatal("missing bound browser state cookie")
	}
	callback := base + "/callback?state=" + location.Query().Get("state") + "&code=test-code"
	identityHTTPRequest(t, mux, "GET", callback, "", &http.Cookie{Name: browser.Name, Value: strings.Repeat("x", 43)}, "", 401)
	if exchanges.Load() != 0 {
		t.Fatal("invalid browser reached token exchange")
	}
	w = identityHTTPRequest(t, mux, "GET", callback, "", browser, "", 303)
	var session *http.Cookie
	for _, v := range w.Result().Cookies() {
		if v.Name == s.cookieName() {
			session = v
		}
	}
	if session == nil {
		t.Fatal("verified OIDC login did not establish session")
	}
	user, e := f.repo.SessionUser(t.Context(), sessionHash(session.Value))
	if e != nil {
		t.Fatal(e)
	}
	if user.ID == f.owner.ID {
		t.Fatal("provider email linked local owner")
	}
	ws, e := f.repo.Membership(t.Context(), user.ID, f.ws.Slug)
	if e != nil || ws.Role != "viewer" {
		t.Fatal("untrusted token roles granted elevated access")
	}
	identityHTTPRequest(t, mux, "GET", callback, "", browser, "", 401)
	if exchanges.Load() != 1 {
		t.Fatal("callback replay repeated token exchange")
	}
	c.Enabled = false
	if _, e := f.repo.SaveIdentitySettings(t.Context(), f.ws.ID, f.owner.ID, c); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.SessionUser(t.Context(), sessionHash(session.Value)); e == nil {
		t.Fatal("disabled federation retained browser session")
	}
}
