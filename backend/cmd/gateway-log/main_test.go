package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestForwardSkipsInvalidLinesAndRetriesExactBytes(t *testing.T) {
	event := `{"method":"GET","route":"/orders/{id}","status":200,"observedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) +
		`","eventId":"0123456789abcdef0123456789abcdef"}`
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/runtime/gateway" ||
			r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("unexpected delivery request")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error("could not read request")
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		first := len(bodies) == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	endpoint, err := gatewayEndpoint(server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader("unparseable\n" + strings.Repeat("x", maxLineBytes*2) + "\n" +
		event + "," + strings.Replace(event, "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdee", 1) + "\n\n  " + event)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	rejected, err := forward(ctx, server.Client(), endpoint, "fixture-token", input)
	if err != nil || rejected != 3 {
		t.Fatalf("forward returned rejected=%d error=%v", rejected, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[0] != "["+event+"]" || bodies[0] != bodies[1] {
		t.Fatal("forwarder changed the validated body or accepted a malformed record")
	}
}

func TestGatewayEndpointRestrictsCleartext(t *testing.T) {
	for _, origin := range []string{"http://example.com", "https://user@example.com", "https://example.com/api"} {
		if _, err := gatewayEndpoint(origin, true); err == nil {
			t.Errorf("unexpected acceptance of %q", origin)
		}
	}
	if _, err := gatewayEndpoint("http://app:8080", false); err == nil {
		t.Fatal("cleartext was accepted without the local flag")
	}
	for _, origin := range []string{"https://example.com", "http://app:8080", "http://[::1]:8080"} {
		if _, err := gatewayEndpoint(origin, true); err != nil {
			t.Errorf("unexpected rejection of %q: %v", origin, err)
		}
	}
}
