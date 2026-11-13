package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

type aiEgressFixture struct {
	guard         *aiEgress
	proxy         *httptest.Server
	plainProxy    http.Handler
	upstreamCalls atomic.Int64
	auditStatus   atomic.Int64
	forwarded     chan []byte
	audits        chan []byte
}

func newAIEgressFixture(t *testing.T, policy datasafety.Policy, responder ...http.HandlerFunc) *aiEgressFixture {
	t.Helper()
	f := &aiEgressFixture{forwarded: make(chan []byte, 16), audits: make(chan []byte, 32)}
	f.auditStatus.Store(200)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.upstreamCalls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer synthetic-upstream-key" {
			t.Error("upstream request changed")
		}
		f.forwarded <- body
		if len(responder) > 0 {
			responder[0](w, r)
			return
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"synthetic":true}`)
	}))
	t.Cleanup(upstream.Close)
	upstreamURL, _ := url.Parse(upstream.URL)
	_, port, _ := net.SplitHostPort(upstreamURL.Host)
	upstreamURL.Host = net.JoinHostPort("example.com", port)
	transport := directTransport()
	certPath, _ := writeTestTLSFiles(t, upstream.TLS.Certificates[0])
	if err := configureUpstreamTLS(transport, upstreamURL, certPath); err != nil {
		t.Fatal(err)
	}
	// Preserve real TLS hostname verification while keeping every socket on this fixture.
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != upstreamURL.Host {
			t.Error("request escaped the fixed fixture upstream")
		}
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != aiEventsPath || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer synthetic-operations-key" {
			t.Error("audit went to the wrong receiver or used wrong authorization")
		}
		raw, _ := io.ReadAll(r.Body)
		f.audits <- raw
		w.WriteHeader(int(f.auditStatus.Load()))
	}))
	t.Cleanup(receiver.Close)
	var err error
	f.guard, err = newAIEgress(upstreamURL, receiver.URL, "synthetic-operations-key", true, policy, testRoutes(t, "/v1/chat/completions"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.guard.client.CloseIdleConnections)
	telemetry := newObserver(receiver.URL+telemetryPath, "synthetic-runtime-key")
	t.Cleanup(telemetry.client.CloseIdleConnections)
	f.plainProxy = proxyHandler(upstreamURL, transport, f.guard.routes, telemetry)
	f.proxy = httptest.NewServer(f.guard.wrap(proxyHandlerWithResponsePolicy(upstreamURL, transport, f.guard.routes, telemetry, false, rejectAIEgressRedirect)))
	t.Cleanup(f.proxy.Close)
	return f
}

func (f *aiEgressFixture) post(t *testing.T, body string, encoding string, want int) {
	t.Helper()
	r, _ := http.NewRequestWithContext(t.Context(), "POST", f.proxy.URL+"/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer synthetic-upstream-key")
	if encoding != "" {
		r.Header.Set("Content-Encoding", encoding)
	}
	response, err := f.proxy.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != want || strings.Contains(string(raw), "fixture@example.test") || strings.Contains(string(raw), "synthetic-upstream-key") {
		t.Fatalf("AI egress status %d want %d or response exposed request data", response.StatusCode, want)
	}
}

func auditProjection(t *testing.T, f *aiEgressFixture) store.SecurityEventInput {
	t.Helper()
	raw := <-f.audits
	for _, forbidden := range []string{"fixture@example.test", `\u0066ixture`, "synthetic-upstream-key", "synthetic-operations-key", `"messages"`, `"body"`, `"prompt"`, `"fileDigest"`} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Error("AI audit contained source body, content digest or credentials")
		}
	}
	var event store.SecurityEventInput
	if json.Unmarshal(raw, &event) != nil || event.DataContext == nil || event.DataContext.Validate() != nil || event.DataContext.Kind != "ai" || event.DataContext.DestinationDomain != "example.com" || event.DataContext.PolicyDigest == "" || event.Route != "/v1/chat/completions" {
		t.Fatal("missing truthful bounded AI decision projection")
	}
	return event
}

func TestAIEgressActuallyBlocksAndPreservesAllowedHTTPSRequests(t *testing.T) {
	policy := datasafety.Policy{Version: "synthetic-ai-v1", ProtectedCategories: []string{"email"}, ApprovedAIDomains: []string{"example.com"}, BlockUnapprovedAI: true}
	f := newAIEgressFixture(t, policy)
	body := " {\n\"messages\":[{\"role\":\"user\",\"content\":\"\\u0066ixture@example.test\"}]}\n"
	f.post(t, body, "", 403)
	event := auditProjection(t, f)
	if f.upstreamCalls.Load() != 0 || event.Outcome != "observed" || event.DataContext.Enforcement != "blocked" || len(event.DataContext.Categories) != 1 || event.DataContext.Categories[0] != "email" {
		t.Fatal("protected prompt reached the upstream or was not classified")
	}
	f.guard.policy.AllowedChannels = []string{"ai_prompt"}
	f.post(t, body, "", 201)
	event = auditProjection(t, f)
	if f.upstreamCalls.Load() != 1 || string(<-f.forwarded) != body || event.Outcome != "activity" || event.DataContext.Enforcement != "allowed" {
		t.Fatal("approved request was not forwarded exactly once with original bytes")
	}
	f.guard.policy.ApprovedAIDomains = []string{"approved.example"}
	f.post(t, `{"messages":[{"content":"synthetic non-sensitive text"}]}`, "", 403)
	event = auditProjection(t, f)
	if f.upstreamCalls.Load() != 1 || event.Outcome != "observed" || event.DataContext.Enforcement != "blocked" {
		t.Fatal("unapproved fixed AI destination reached upstream")
	}
}

func TestAIEgressFailsClosedOnUnknownAndUnavailableAudit(t *testing.T) {
	f := newAIEgressFixture(t, datasafety.Policy{Version: "unknown-v1", ProtectedCategories: []string{"email"}, AllowedChannels: []string{"ai_prompt"}, ApprovedAIDomains: []string{"example.com"}, BlockUnapprovedAI: true})
	for _, tc := range []struct{ body, encoding string }{
		{`{"truncated":`, ""},
		{`{"content":"safe","content":"redacted"}`, ""},
		{`{"content":"fixture@example.test"}`, "gzip"},
		{`{"content":"fixture@example.test"}`, "br"},
		{`{"content":"` + strings.Repeat("x", (1<<20)+1) + `"}`, ""},
		{strings.Repeat("[", 18) + "null" + strings.Repeat("]", 18), ""},
	} {
		f.post(t, tc.body, tc.encoding, 422)
		if e := auditProjection(t, f); e.Outcome != "observed" || e.DataContext.Enforcement != "unknown" || e.DataContext.Capture != "unknown" {
			t.Fatal("incomplete JSON was represented as a safe decision")
		}
	}
	f.guard.policy.ProtectedCategories = []string{"credential"}
	f.post(t, `{"content":"synthetic unknown category"}`, "", 422)
	_ = auditProjection(t, f)
	f.guard.policy.ProtectedCategories = []string{"email"}
	f.auditStatus.Store(503)
	f.post(t, `{"content":"safe synthetic text"}`, "", 503)
	_ = auditProjection(t, f)
	f.guard.policy.AllowedChannels = nil
	f.post(t, `{"content":"fixture@example.test"}`, "", 403)
	_ = auditProjection(t, f)
	if f.upstreamCalls.Load() != 0 {
		t.Fatal("unknown, unaudited or blocked request reached upstream")
	}
}

func TestAIEgressRequiresExplicitScopeAndBoundedSourceConfiguration(t *testing.T) {
	f := newAIEgressFixture(t, datasafety.Policy{Version: "scope-v1", ApprovedAIDomains: []string{"example.com"}, BlockUnapprovedAI: true})
	for _, target := range []struct{ method, path string }{{"GET", "/v1/chat/completions"}, {"POST", "/unapproved"}, {"POST", "/v1/chat/completions?token=synthetic"}} {
		r, _ := http.NewRequestWithContext(t.Context(), target.method, f.proxy.URL+target.path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		response, err := f.proxy.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatal("unapproved request scope accepted")
		}
	}
	for range cap(f.guard.slots) {
		f.guard.slots <- struct{}{}
	}
	f.post(t, `{}`, "", 429)
	for range cap(f.guard.slots) {
		<-f.guard.slots
	}
	if f.upstreamCalls.Load() != 0 || len(f.audits) != 0 {
		t.Fatal("out-of-scope requests created an approved decision")
	}
	for _, origin := range []string{"http://example.com", "https://127.0.0.1"} {
		u, _ := url.Parse(origin)
		if _, err := newAIEgress(u, "https://platform.example", "synthetic", false, f.guard.policy, f.guard.routes); err == nil {
			t.Fatal("non-HTTPS or non-domain upstream accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	for _, raw := range []string{`{"version":"v1","unknown":true}`, `{"version":"v1"} {}`, strings.Repeat("x", (16<<10)+1)} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadDataPolicy(path); err == nil {
			t.Fatal("invalid source policy accepted")
		}
	}
}

func TestAIEgressRejectsTLSUpstreamRedirectWithoutReplayingBody(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var destinationCalls atomic.Int64
			destination := redirectTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/ready" {
					destinationCalls.Add(1)
				}
				w.WriteHeader(200)
			}))
			target, _ := url.Parse(destination.URL)
			_, port, _ := net.SplitHostPort(target.Host)
			target.Host = net.JoinHostPort("redirect.example.test", port)
			location := target.String() + "/replay"
			f := newAIEgressFixture(t, datasafety.Policy{Version: "redirect-v1", ProtectedCategories: []string{"email"}, AllowedChannels: []string{"ai_prompt"}, ApprovedAIDomains: []string{"example.com"}, BlockUnapprovedAI: true}, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(status)
			})
			transport := directTransport()
			roots := x509.NewCertPool()
			roots.AddCert(destination.Certificate())
			transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address == target.Host {
					address = destination.Listener.Addr().String()
				}
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			t.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			ready, err := client.Get(target.String() + "/ready")
			if err != nil {
				t.Fatal("redirect destination must be reachable with valid TLS", err)
			}
			_ = ready.Body.Close()
			if ready.StatusCode != 200 || ready.TLS == nil || len(ready.TLS.VerifiedChains) == 0 {
				t.Fatal("destination reachability depended on relaxed TLS")
			}
			body := " {\"messages\":[{\"content\":\"fixture@example.test\"}]}\n"
			req, _ := http.NewRequestWithContext(t.Context(), "POST", f.proxy.URL+"/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer synthetic-upstream-key")
			response, err := client.Do(req) // Ordinary SDK redirect behavior remains enabled.
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 502 || response.Header.Get("Location") != "" || destinationCalls.Load() != 0 || strings.Contains(string(raw), location) || strings.Contains(string(raw), "fixture@example.test") {
				t.Fatal("upstream redirect escaped AI egress or exposed redirect/body")
			}
			if f.upstreamCalls.Load() != 1 || string(<-f.forwarded) != body || auditProjection(t, f).Outcome != "activity" {
				t.Fatal("initial approved TLS hop did not retain the permitted bytes")
			}

			// The opt-in response policy must not alter ordinary gateway redirects.
			plain := httptest.NewServer(f.plainProxy)
			t.Cleanup(plain.Close)
			plainClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			plainReq, _ := http.NewRequestWithContext(t.Context(), "POST", plain.URL+"/v1/chat/completions", strings.NewReader(body))
			plainReq.Header.Set("Authorization", "Bearer synthetic-upstream-key")
			plainResponse, err := plainClient.Do(plainReq)
			if err != nil {
				t.Fatal(err)
			}
			_ = plainResponse.Body.Close()
			if plainResponse.StatusCode != status || plainResponse.Header.Get("Location") != location || string(<-f.forwarded) != body {
				t.Fatal("default non-AI gateway redirect behavior changed")
			}
		})
	}
}

func redirectTLSFixture(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"redirect.example.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func TestAIEgressPreservesStreamingResponse(t *testing.T) {
	resume := make(chan struct{})
	f := newAIEgressFixture(t, datasafety.Policy{Version: "stream-v1", ApprovedAIDomains: []string{"example.com"}, BlockUnapprovedAI: true}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-resume:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: second\n\n")
	})
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	body := `{"messages":[{"content":"synthetic stream"}],"stream":true}`
	req, _ := http.NewRequestWithContext(ctx, "POST", f.proxy.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer synthetic-upstream-key")
	response, err := f.proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil || first != "data: first\n" || response.StatusCode != 200 {
		t.Fatal("stream was buffered until completion or changed", err)
	}
	_, _ = reader.ReadString('\n')
	close(resume)
	rest, err := io.ReadAll(reader)
	if err != nil || string(rest) != "data: second\n\n" || string(<-f.forwarded) != body || auditProjection(t, f).Outcome != "activity" {
		t.Fatal("stream continuation or approved request bytes changed", err)
	}
}
