package httpapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestWorkloadBindingHTTPImportsPublicTrustAndExchangesSignedMachineAssertion(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	base := "/api/v1/workspaces/" + f.ws.Slug + "/ai-gateway"
	cfg := gatewayHTTPConfig()
	identityHTTPRequest(t, f.handler, "PUT", base+"/config", gatewayHTTPJSON(t, map[string]any{"expectedRevision": 0, "enabled": cfg.Enabled, "catalogDigest": cfg.CatalogDigest, "applications": cfg.Applications, "providers": cfg.Providers}), f.cookie, "", 200)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	keys, _ := json.Marshal(map[string]any{"keys": []any{map[string]string{"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig", "kid": "synthetic-http-key", "x": encode(pub)}}})
	in := store.GatewayWorkloadInput{Name: "Synthetic workload", ApplicationID: "app", Issuer: "https://issuer.example.invalid", Audience: "scarlet-workload:" + f.ws.Slug, Subject: "agent/support", JWKS: keys, MaxTTLSeconds: 60}
	identityHTTPRequest(t, f.handler, "POST", base+"/workloads", gatewayHTTPJSON(t, in), f.viewerCookie, "", 403)
	created := identityHTTPRequest(t, f.handler, "POST", base+"/workloads", gatewayHTTPJSON(t, in), f.cookie, "", 201)
	var binding store.GatewayWorkloadBinding
	if json.Unmarshal(created.Body.Bytes(), &binding) != nil || binding.Revision != 1 || !binding.Enabled {
		t.Fatal("workload binding create DTO")
	}
	path := base + "/workloads/" + strconv.FormatInt(binding.ID, 10)
	identityHTTPRequest(t, f.handler, "GET", base+"/workloads?limit=1", "", f.viewerCookie, "", 200)
	identityHTTPRequest(t, f.handler, "GET", base+"/workloads?limit=", "", f.cookie, "", 400)
	identityHTTPRequest(t, f.handler, "GET", base+"/workloads?before=invalid", "", f.cookie, "", 400)
	unsafe := in
	unsafe.JWKS = json.RawMessage(strings.Replace(string(keys), `"kty":"OKP"`, `"kty":"OKP","d":"synthetic-private-key"`, 1))
	identityHTTPRequest(t, f.handler, "POST", base+"/workloads", gatewayHTTPJSON(t, unsafe), f.cookie, "", 400)
	sign := func(jti string) string {
		now := time.Now().UTC()
		header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": "synthetic-http-key", "typ": "JWT"})
		body, _ := json.Marshal(map[string]any{"iss": in.Issuer, "aud": in.Audience, "sub": in.Subject, "iat": now.Unix(), "exp": now.Unix() + 120, "jti": jti, "roles": []string{"ignored-owner"}})
		raw := encode(header) + "." + encode(body)
		return raw + "." + encode(ed25519.Sign(key, []byte(raw)))
	}
	assertion := sign("http-one-time-assertion")
	request := gatewayHTTPJSON(t, map[string]int64{"bindingId": binding.ID})
	identityHTTPRequest(t, f.handler, "POST", base+"/workload-token", request, nil, "invalid", 401)
	issued := identityHTTPRequest(t, f.handler, "POST", base+"/workload-token", request, nil, assertion, 200)
	var token store.GatewayWorkloadToken
	if json.Unmarshal(issued.Body.Bytes(), &token) != nil || token.Credential.ExpiresAt == nil || token.Credential.WorkloadBindingID == nil || token.Token == "" || issued.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("workload credential one-time response")
	}
	identityHTTPRequest(t, f.handler, "POST", base+"/workload-token", request, nil, assertion, 409)
	identityHTTPRequest(t, f.handler, "POST", base+"/workload-token", request, f.cookie, sign("cookie-token-rejected"), 401)
	identityHTTPRequest(t, f.handler, "PUT", base+"/workload-token", request, nil, sign("wrong-method"), 403)
	view := identityHTTPRequest(t, f.handler, "GET", base, "", f.viewerCookie, "", 200)
	if strings.Contains(view.Body.String(), token.Token) || strings.Contains(view.Body.String(), assertion) || !strings.Contains(view.Body.String(), "expiresAt") || !strings.Contains(view.Body.String(), "workloadBindingId") {
		t.Fatal("credential inventory lost expiry/origin or leaked credentials")
	}
	update := map[string]any{"expectedRevision": 1, "enabled": true, "name": in.Name, "applicationId": in.ApplicationID, "issuer": in.Issuer, "audience": in.Audience, "subject": in.Subject, "jwks": in.JWKS, "maxTtlSeconds": in.MaxTTLSeconds}
	identityHTTPRequest(t, f.handler, "PUT", path, gatewayHTTPJSON(t, update), f.viewerCookie, "", 403)
	identityHTTPRequest(t, f.handler, "PUT", path, gatewayHTTPJSON(t, update), f.cookie, "", 200)
	identityHTTPRequest(t, f.handler, "PUT", path, gatewayHTTPJSON(t, update), f.cookie, "", 409)
	if _, err = f.repo.ResolveGateway(t.Context(), store.GatewayResolveInput{WorkspaceSlug: f.ws.Slug, TokenHash: sessionHash(token.Token)}); err == nil {
		t.Fatal("workload trust update retained previously issued token")
	}
	identityHTTPRequest(t, f.handler, "POST", path+"/revoke", `{"expectedRevision":2}`, f.cookie, "", 200)
	identityHTTPRequest(t, f.handler, "POST", base+"/workload-token", request, nil, sign("revoked-assertion"), 401)
}

func TestWorkloadMachineOriginExemptionCannotAuthorizeNeighborRoutesOrCookies(t *testing.T) {
	base := "/api/v1/workspaces/synthetic/ai-gateway/"
	for _, tc := range []struct {
		method, path, cookie, auth string
		want                       bool
	}{
		{"POST", base + "workload-token", "", "Bearer assertion", true},
		{"PUT", base + "workload-token", "", "Bearer assertion", false},
		{"POST", base + "workloads", "", "Bearer assertion", false},
		{"POST", base + "workload-token/extra", "", "Bearer assertion", false},
		{"POST", base + "workload-token", "session=synthetic", "Bearer assertion", false},
		{"POST", base + "workload-token", "", "", false},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Cookie", tc.cookie)
		r.Header.Set("Authorization", tc.auth)
		if got := gatewayWorkloadTokenOriginExempt(r); got != tc.want {
			t.Fatal("origin exemption widened", tc.method, tc.path)
		}
	}
}
