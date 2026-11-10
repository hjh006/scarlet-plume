package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
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

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func TestHTTPSProxyPreservesTrafficAndRetriesOnlyMetadata(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "private-payload" || r.Method != http.MethodPost ||
			r.URL.Path != "/v1/customers/private-id" || r.URL.Query().Get("code") != "private-query" ||
			r.Header.Get("Authorization") != "Bearer private-app-token" || r.Header.Get("Cookie") != "session=private-cookie" {
			t.Errorf("proxy did not preserve upstream business request")
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "private-upstream-response")
	}))
	defer upstream.Close()
	certFile, keyFile := writeTestTLSFiles(t, upstream.TLS.Certificates[0])
	listenerConfig, err := listenerTLSConfig(certFile, keyFile, "")
	if err != nil || listenerConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("listener TLS config: %v", err)
	}
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := directTransport()
	defer transport.CloseIdleConnections()
	if err := configureUpstreamTLS(transport, upstreamURL, certFile); err != nil {
		t.Fatal(err)
	}

	received := make(chan []byte, 4)
	var attempts atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != telemetryPath ||
			r.Header.Get("Authorization") != "Bearer receiver-fixture" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected telemetry request headers or route")
		}
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received <- payload
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	telemetry := newObserver(receiver.URL+telemetryPath, "receiver-fixture")
	defer telemetry.client.CloseIdleConnections()
	workerCtx, stop := context.WithCancel(t.Context())
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); telemetry.run(workerCtx) }()
	defer func() { stop(); <-workerDone }()
	proxy := httptest.NewUnstartedServer(proxyHandler(upstreamURL, transport, testRoutes(t, "/v1/customers/{id}"), telemetry))
	proxy.TLS = listenerConfig
	proxy.EnableHTTP2 = true
	proxy.StartTLS()
	defer proxy.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, proxy.URL+"/v1/customers/private-id?code=private-query", strings.NewReader("private-payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer private-app-token")
	req.Header.Set("Cookie", "session=private-cookie")
	res, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil || res.StatusCode != http.StatusCreated || res.ProtoMajor != 2 || string(body) != "private-upstream-response" {
		t.Fatalf("unexpected HTTPS response: status=%d protocol=%s body=%q err=%v", res.StatusCode, res.Proto, body, err)
	}

	first, retry := awaitPayload(t, received), awaitPayload(t, received)
	if !bytes.Equal(first, retry) {
		t.Fatal("retry changed payload or event identity")
	}
	if strings.Contains(string(first), "private-") || strings.Contains(string(first), "receiver-fixture") {
		t.Fatal("telemetry included raw request, response or credentials")
	}
	observations, err := runtimesecurity.ParseGateway(first)
	if err != nil || len(observations) != 1 {
		t.Fatalf("receiver parser rejected observer payload: %v", err)
	}
	observation := observations[0]
	if observation.Route != "/v1/customers/{id}" || observation.Method != http.MethodPost ||
		observation.Status != http.StatusCreated || observation.ObservedAt.IsZero() {
		t.Fatalf("unexpected observation: %+v", observation)
	}
	var events []observationEvent
	if err := json.Unmarshal(first, &events); err != nil || len(events) != 1 || len(events[0].EventID) != 32 {
		t.Fatalf("invalid event identity: %v", err)
	}
}

func TestHTTPSUpstreamVerificationAndQueueOverflow(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	certFile, keyFile := writeTestTLSFiles(t, upstream.TLS.Certificates[0])
	if _, err := listenerTLSConfig(certFile, "", ""); err == nil {
		t.Fatal("expected partial TLS configuration rejection")
	}
	if _, err := listenerTLSConfig("", "", certFile); err == nil {
		t.Fatal("expected client CA without TLS rejection")
	}
	mutual, err := listenerTLSConfig(certFile, keyFile, certFile)
	if err != nil || mutual.ClientAuth != tls.RequireAndVerifyClientCert || mutual.ClientCAs == nil {
		t.Fatalf("invalid mutual TLS config: %v", err)
	}

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	validTarget := *target
	dialAddress := target.Host
	target.Host = net.JoinHostPort("incorrect.test.invalid", target.Port())
	transport := directTransport()
	defer transport.CloseIdleConnections()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, dialAddress)
	}
	if err := configureUpstreamTLS(transport, target, certFile); err != nil {
		t.Fatal(err)
	}
	telemetry := &observer{queue: make(chan observationEvent, 1)}
	routes := testRoutes(t, "/observed")
	invalidUpstream := proxyHandler(target, transport, routes, telemetry)
	failed := httptest.NewRecorder()
	invalidUpstream.ServeHTTP(failed, httptest.NewRequest(http.MethodGet, "/observed", nil))
	if failed.Code != http.StatusBadGateway || failed.Body.String() != "Bad Gateway\n" {
		t.Fatalf("incorrect upstream verification response: %d %q", failed.Code, failed.Body.String())
	}
	validUpstream := proxyHandler(&validTarget, transport, routes, telemetry)
	ok := httptest.NewRecorder()
	validUpstream.ServeHTTP(ok, httptest.NewRequest(http.MethodGet, "/observed", nil))
	if ok.Code != http.StatusOK || len(telemetry.queue) != 1 || telemetry.overflow.Load() != 1 {
		t.Fatal("full observation queue changed business response or exceeded its capacity")
	}
	event := <-telemetry.queue
	if event.Status != http.StatusBadGateway || event.Route != "/observed" {
		t.Fatalf("unexpected upstream transport observation: %+v", event)
	}
	ignored := httptest.NewRecorder()
	validUpstream.ServeHTTP(ignored, httptest.NewRequest(http.MethodGet, "/private-unmatched", nil))
	if ignored.Code != http.StatusOK || len(telemetry.queue) != 0 {
		t.Fatal("unmatched traffic should proxy without telemetry")
	}
}

func TestRouteTemplates(t *testing.T) {
	routes := testRoutes(t, "/", "/v1/customers/{id}", "/v1/customers/me", "/v1/{collection}/{id}")
	for _, sample := range []struct{ path, want string }{
		{"/", "/"},
		{"/v1/customers/123", "/v1/customers/{id}"},
		{"/v1/customers/me", "/v1/customers/me"},
		{"/v1/customers/%E5%BC%A0%E4%B8%89", "/v1/customers/{id}"},
		{"/v1/customers/id%2Fchild", ""},
		{"/v1/customers/id%252fchild", ""},
		{"/v1/customers/..", ""},
		{"/v1/customers/id/", ""},
		{"/v1/customers//", ""},
		{"/private", ""},
	} {
		if got := matchRoute(routes, sample.path); got != sample.want {
			t.Errorf("matchRoute(%q) = %q, want %q", sample.path, got, sample.want)
		}
	}
	for _, pattern := range []string{"/v1/customers/{user}", "/v1/{collection}/detail", "/v1/12345", "/v1/*", "/v1?key=value", "https://example.com"} {
		if err := addRoute(&routes, pattern); err == nil {
			t.Errorf("invalid or ambiguous route accepted: %q", pattern)
		}
	}
}

func TestReceiverOriginPolicy(t *testing.T) {
	for _, input := range []string{"http://example.com", "https://example.com/api", "https://user@example.com", "https://example.com?key=value"} {
		if _, err := receiverEndpoint(input, true); err == nil {
			t.Errorf("invalid receiver origin accepted: %s", input)
		}
	}
	if _, err := receiverEndpoint("http://app:8080", false); err == nil {
		t.Fatal("plaintext receiver accepted without local opt-in")
	}
	if actual, err := receiverEndpoint("http://app:8080", true); err != nil || actual != "http://app:8080"+telemetryPath {
		t.Fatalf("local compose origin rejected: %q %v", actual, err)
	}
}

func testRoutes(t *testing.T, patterns ...string) []routeTemplate {
	t.Helper()
	var routes []routeTemplate
	for _, pattern := range patterns {
		if err := addRoute(&routes, pattern); err != nil {
			t.Fatal(err)
		}
	}
	return routes
}

func awaitPayload(t *testing.T, received <-chan []byte) []byte {
	t.Helper()
	select {
	case data := <-received:
		return data
	case <-time.After(4 * time.Second):
		t.Fatal("observation did not arrive")
		return nil
	}
}

func writeTestTLSFiles(t *testing.T, pair tls.Certificate) (string, string) {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})
	privateKey, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
