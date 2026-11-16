package enterpriseidentity

import (
	"context"
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
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func signedIdentityToken(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	body, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(unsigned))
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if e != nil {
		t.Fatal(e)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestOIDCSignatureIssuerAudienceNonceAndPKCE(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name               string
		change             func(map[string]any)
		nonce, verifier    string
		badSignature, want bool
	}{
		{name: "valid", nonce: "bound-nonce", verifier: "pkce-verifier", want: true},
		{name: "wrong nonce", nonce: "another-nonce", verifier: "pkce-verifier"},
		{name: "wrong PKCE", nonce: "bound-nonce", verifier: "wrong-verifier"},
		{name: "wrong issuer", nonce: "bound-nonce", verifier: "pkce-verifier", change: func(c map[string]any) { c["iss"] = "https://other.invalid" }},
		{name: "wrong audience", nonce: "bound-nonce", verifier: "pkce-verifier", change: func(c map[string]any) { c["aud"] = "another-client" }},
		{name: "expired", nonce: "bound-nonce", verifier: "pkce-verifier", change: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }},
		{name: "future issued", nonce: "bound-nonce", verifier: "pkce-verifier", change: func(c map[string]any) { c["iat"] = time.Now().Add(time.Hour).Unix() }},
		{name: "wrong authorized party", nonce: "bound-nonce", verifier: "pkce-verifier", change: func(c map[string]any) { c["azp"] = "another-client" }},
		{name: "multiple audience without azp", nonce: "bound-nonce", verifier: "pkce-verifier", change: func(c map[string]any) { c["aud"] = []string{"client", "other"} }},
		{name: "bad signature", nonce: "bound-nonce", verifier: "pkce-verifier", badSignature: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					_ = json.NewEncoder(w).Encode(map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}, "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}})
				case "/keys":
					_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test-key", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
				case "/token":
					if e := r.ParseForm(); e != nil || r.Form.Get("code_verifier") != "pkce-verifier" {
						w.WriteHeader(400)
						_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
						return
					}
					claims := map[string]any{"iss": server.URL, "sub": "provisioned-subject", "aud": "client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": "bound-nonce"}
					if tc.change != nil {
						tc.change(claims)
					}
					token := signedIdentityToken(t, key, claims)
					if tc.badSignature {
						parts := strings.Split(token, ".")
						sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
						sig[0] ^= 1
						parts[2] = base64.RawURLEncoding.EncodeToString(sig)
						token = strings.Join(parts, ".")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "opaque-test-only", "token_type": "Bearer", "id_token": token})
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client, e := New(t.Context(), Config{Issuer: server.URL, ClientID: "client", ClientSecret: "test-client-secret", RedirectURL: "https://gateway.invalid/callback"}, server.Client())
			if e != nil {
				t.Fatal(e)
			}
			auth, e := url.Parse(client.AuthorizationURL("bound-state", "bound-nonce", "pkce-verifier"))
			if e != nil {
				t.Fatal(e)
			}
			q := auth.Query()
			if q.Get("state") != "bound-state" || q.Get("nonce") != "bound-nonce" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != oauth2.S256ChallengeFromVerifier("pkce-verifier") {
				t.Fatal("authorization flow omitted state, nonce or PKCE")
			}
			sub, e := client.Subject(t.Context(), "test-code", tc.nonce, tc.verifier)
			if (e == nil) != tc.want {
				t.Fatalf("verification success=%v, want=%v", e == nil, tc.want)
			}
			if tc.want && sub != "provisioned-subject" {
				t.Fatal("wrong subject")
			}
		})
	}
}

func TestOIDCRejectsInsecureDiscovery(t *testing.T) {
	for _, cfg := range []Config{{Issuer: "http://issuer.invalid", ClientID: "client", ClientSecret: "secret", RedirectURL: "https://app.invalid/callback"}, {Issuer: "https://issuer.invalid", ClientID: "client", ClientSecret: "secret", RedirectURL: "http://app.invalid/callback"}} {
		if _, e := New(context.Background(), cfg, nil); e == nil {
			t.Fatal("insecure configuration accepted")
		}
	}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": server.URL, "authorization_endpoint": "http://unsafe.invalid/auth", "token_endpoint": "http://unsafe.invalid/token", "jwks_uri": "http://unsafe.invalid/keys"})
	}))
	defer server.Close()
	if _, e := New(t.Context(), Config{Issuer: server.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "https://app.invalid/callback"}, server.Client()); e == nil {
		t.Fatal("insecure discovery endpoints accepted")
	}
}
