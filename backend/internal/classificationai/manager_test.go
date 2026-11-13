package classificationai

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

const fixtureKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const fixtureAPIKey = "synthetic-fixture-api-key"

func testTLS(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Manager, Profile) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	manager, err := NewWithRootCAs(fixtureKey, strings.TrimPrefix(server.URL, "https://"), roots)
	if err != nil {
		t.Fatal(err)
	}
	return server, manager, Profile{BaseURL: server.URL + "/v1", Protocol: "chat_completions", Model: "user/model-manually-selected", APIKey: fixtureAPIKey}
}

func TestCredentialEncryptionBindsTenantAndNormalizedURL(t *testing.T) {
	manager, err := New(fixtureKey, "")
	if err != nil || !manager.ReadyEncryption() {
		t.Fatal("configured encryption unavailable")
	}
	one, err := manager.Encrypt(7, "https://API.EXAMPLE.test:443/v1/", fixtureAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	two, err := manager.Encrypt(7, "https://api.example.test/v1", fixtureAPIKey)
	if err != nil || string(one) == string(two) || strings.Contains(string(one), fixtureAPIKey) {
		t.Fatal("encryption lacked nonce randomness or exposed plaintext")
	}
	plain, err := manager.Decrypt(7, "https://api.example.test/v1", one)
	if err != nil || plain != fixtureAPIKey {
		t.Fatal("normalized URL binding failed")
	}
	for _, binding := range []struct {
		workspace int64
		url       string
	}{{8, "https://api.example.test/v1"}, {7, "https://api.example.test/other"}, {7, "https://other.example.test/v1"}} {
		if _, err := manager.Decrypt(binding.workspace, binding.url, one); !errors.Is(err, ErrCredential) {
			t.Fatal("credential could be transplanted to another tenant or endpoint")
		}
	}
	one[len(one)-1] ^= 1
	if _, err := manager.Decrypt(7, "https://api.example.test/v1", one); !errors.Is(err, ErrCredential) {
		t.Fatal("modified ciphertext accepted")
	}
	for _, bad := range []string{"garbage", strings.Repeat("00", 31), strings.Repeat("00", 33)} {
		if _, err := New(bad, ""); !errors.Is(err, ErrConfiguration) {
			t.Fatal("invalid key accepted")
		}
	}
	disabled, err := New("", "")
	if err != nil || disabled.ReadyEncryption() {
		t.Fatal("empty encryption key should leave local engine available")
	}
	if _, err := disabled.Encrypt(1, "https://example.test/v1", fixtureAPIKey); !errors.Is(err, ErrEncryptionUnavailable) {
		t.Fatal("credentials stored without configured encryption")
	}
}

func TestEndpointValidationAndPrivateHostPolicy(t *testing.T) {
	for _, bad := range []string{"http://example.test/v1", "https://user:secret@example.test/v1", "https://example.test/v1?secret=value", "https://example.test/v1#secret", "https://example.test/a/../v1", "https://example.test/%2fother", "https://example.test:99999/v1", "https://example.test\\@localhost/v1", "https://[fe80::1%25en0]/v1"} {
		if _, err := NormalizeBaseURL(bad); !errors.Is(err, ErrConfiguration) {
			t.Errorf("invalid endpoint accepted")
		}
	}
	for _, bad := range []string{"*.example.test", "https://example.test", "host.test/path", "host.test:0"} {
		if _, err := New("", bad); !errors.Is(err, ErrConfiguration) {
			t.Fatal("invalid host allowlist accepted")
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "172.16.1.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "192.0.2.1", "198.18.0.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2002:7f00:1::"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("special-use target accepted: %s", ip)
		}
	}
	if !publicIP(net.ParseIP("8.8.8.8")) || !publicIP(net.ParseIP("2606:4700:4700::1111")) {
		t.Fatal("public IPs incorrectly blocked")
	}
	called := false
	server, allowed, profile := testTLS(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`))
	})
	blocked, err := NewWithRootCAs("", "", allowed.roots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.Models(t.Context(), profile); !errors.Is(err, ErrRequest) || called {
		t.Fatal("private host reached without exact opt-in")
	}
	wrongPort, err := NewWithRootCAs("", "127.0.0.1", allowed.roots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongPort.Models(t.Context(), profile); !errors.Is(err, ErrRequest) || called {
		t.Fatal("bare allowlist host permitted arbitrary port")
	}
	if models, err := allowed.Models(t.Context(), profile); err != nil || !slices.Equal(models, []string{"fixture-model"}) {
		t.Fatal("verified exact private endpoint unavailable")
	}
	called = false
	untrusted, err := New("", strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := untrusted.Models(t.Context(), profile); !errors.Is(err, ErrRequest) || called {
		t.Fatal("untrusted TLS endpoint reached handler")
	}
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	wrongName, err := NewWithRootCAs("", "wrong.example.test:"+port, allowed.roots)
	if err != nil {
		t.Fatal(err)
	}
	wrongName.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	profile.BaseURL = "https://wrong.example.test:" + port + "/v1"
	if _, err := wrongName.Models(t.Context(), profile); !errors.Is(err, ErrRequest) || called {
		t.Fatal("TLS hostname verification was bypassed")
	}
}

func TestRequestsPinValidatedDNSAndDoNotUseProxyOrRedirect(t *testing.T) {
	proxyCalls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls++; w.WriteHeader(502) }))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	server, manager, profile := testTLS(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"pinned-model"}]}`))
	})
	// The test certificate includes example.com. Resolve it once to a validated
	// public address, then route that exact pinned address to the local TLS peer.
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	profile.BaseURL = "https://example.com:" + port + "/v1"
	lookups, dials := 0, 0
	manager.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		lookups++
		if lookups > 1 {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	manager.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dials++
		if address != net.JoinHostPort("8.8.8.8", port) {
			t.Fatal("dial did not use pinned validated IP")
		}
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "https://"))
	}
	if _, err := manager.Models(t.Context(), profile); err != nil {
		t.Fatalf("pinned verified request failed: %v", err)
	}
	if lookups != 1 || dials != 1 || proxyCalls != 0 {
		t.Fatal("request re-resolved DNS or used an environment proxy")
	}
	manager.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if _, err := manager.Models(t.Context(), profile); !errors.Is(err, ErrRequest) || dials != 1 {
		t.Fatal("mixed public/private DNS answer reached network")
	}
	destinationCalls := 0
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls++ }))
	defer destination.Close()
	_, redirect, redirectProfile := testTLS(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	if _, err := redirect.Models(t.Context(), redirectProfile); !errors.Is(err, ErrRequest) || destinationCalls != 0 {
		t.Fatal("redirect followed")
	}
}

func TestModelDiscoveryBoundsFailureAndManualModel(t *testing.T) {
	mode := "unavailable"
	var profile Profile
	_, manager, profile := testTLS(t, func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "unavailable":
			w.WriteHeader(404)
			_, _ = w.Write([]byte("raw-secret-upstream-body " + fixtureAPIKey))
		case "large":
			_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
		case "models":
			items := []map[string]string{}
			for index := range 250 {
				items = append(items, map[string]string{"id": fmt.Sprintf("fixture-model-%03d", index)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
		case "manual":
			var input struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.Model != profile.Model {
				t.Error("manually supplied model changed")
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"category\":\"business\",\"businessLevel\":\"internal\",\"rationale\":\"Metadata only.\"}"},"finish_reason":"stop"}]}`))
		}
	})
	if _, err := manager.Models(t.Context(), profile); !errors.Is(err, ErrRequest) || strings.Contains(err.Error(), fixtureAPIKey) || strings.Contains(err.Error(), "raw-secret") || strings.Contains(err.Error(), profile.BaseURL) {
		t.Fatal("model-list failure exposed upstream details")
	}
	mode = "large"
	if _, err := manager.Models(t.Context(), profile); !errors.Is(err, ErrResponse) {
		t.Fatal("oversized model response accepted")
	}
	mode = "models"
	if models, err := manager.Models(t.Context(), profile); err != nil || len(models) != 200 {
		t.Fatalf("model list bound failed: %v count=%d", err, len(models))
	}
	mode = "manual"
	if _, err := manager.Suggest(t.Context(), profile, Metadata{AssetName: "Synthetic inventory", Kind: "table", Fields: []MetadataField{{Name: "project", Type: "text"}}}); err != nil {
		t.Fatal("manual model selection incorrectly depended on discovery")
	}
}

func TestRequestCancellationAndVerifiedTLS(t *testing.T) {
	_, manager, profile := testTLS(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	if _, err := manager.Models(ctx, profile); !errors.Is(err, ErrRequest) || time.Since(start) > time.Second {
		t.Fatal("request ignored context cancellation")
	}
}
