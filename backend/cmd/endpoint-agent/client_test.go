package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/endpointguard"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func TestEndpointMetadataAuditAndOutbox(t *testing.T) {
	var count atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "private@example.test") || strings.Contains(string(raw), "private-path") {
			t.Error("raw endpoint source entered projection")
		}
		count.Add(1)
		w.WriteHeader(201)
	}))
	defer s.Close()
	c := &endpointClient{origin: s.URL, key: []byte(strings.Repeat("k", 32)), scope: "test", device: strings.Repeat("a", 64), http: s.Client()}
	root, e := os.OpenRoot(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	p := endpointguard.Policy{ID: 1, Revision: 1, DeviceRef: c.device, Channel: "clipboard", Mode: "block_sensitive", Scope: "controlled_operations", Digest: strings.Repeat("b", 64), Categories: []string{"email"}, ExpiresAt: time.Now().Add(time.Hour)}
	actions := 0
	fact, e := endpointguard.Run(p, endpointguard.Identity{DeviceRef: c.device, OperationRef: strings.Repeat("c", 64)}, "txt", []byte("private@example.test"), func(f datasafety.Context) error { return deliver(t.Context(), c, root, c.event("attempt", f)) }, func([]byte) error { actions++; return nil })
	if e != endpointguard.ErrBlocked || actions != 0 || count.Load() != 1 || fact.Endpoint.Stage != "preflight" {
		t.Fatal("blocked action or audit failed")
	}
	c.origin = "http://127.0.0.1:1"
	c.http.Timeout = 100 * time.Millisecond
	event := c.event("offline", fact)
	if deliver(context.Background(), c, root, event) == nil {
		t.Fatal("offline audit pretended success")
	}
	// An explicit replay sends the exact stored timestamp and event ID; it
	// does not invoke any USB/clipboard/printer action.
	c.origin = s.URL
	if e := replay(t.Context(), c, root); e != nil {
		t.Fatal(e)
	}
	if count.Load() != 2 || actions != 0 {
		t.Fatal("replay changed operation count")
	}
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), "private@example.test") {
		t.Fatal("raw input in saved receipt")
	}
}

func TestEndpointClientRejectsRedirectAndUnsafeOrigins(t *testing.T) {
	t.Setenv("SCARLET_OPERATIONS_TOKEN", "synthetic-token")
	t.Setenv("SCARLET_ENDPOINT_KEY_HEX", strings.Repeat("ab", 32))
	t.Setenv("SCARLET_ENDPOINT_SCOPE", "test")
	t.Setenv("SCARLET_ENDPOINT_DEVICE_ID", "synthetic-device")
	var leaked atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer origin.Close()
	c, e := newEndpointClient(origin.URL, true)
	if e != nil {
		t.Fatal(e)
	}
	if c.request(t.Context(), "POST", "/api/v1/endpoint/heartbeat", map[string]string{"deviceRef": c.device}, nil) == nil || leaked.Load() != 0 {
		t.Fatal("redirect forwarded source credential or metadata")
	}
	for _, u := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/path", "https://example.com?q=x"} {
		if _, e := newEndpointClient(u, true); e == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
}
