package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/endpointguard"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func testEndpointPolicy(channel string) endpointguard.Policy {
	return endpointguard.Policy{ID: 1, Revision: 1, DeviceRef: strings.Repeat("a", 64), Channel: channel, Mode: "block_sensitive", Scope: "controlled_operations", Digest: strings.Repeat("b", 64), Categories: []string{"email"}, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestSourceFormatPreventsEscapedSensitiveDataOnEveryChannel(t *testing.T) {
	for _, channel := range []string{"usb", "print", "clipboard"} {
		for _, sample := range []struct {
			name, raw string
			want      error
		}{
			{"contact.JSON", `{"contact":"synthetic\u0040example.invalid"}`, endpointguard.ErrBlocked},
			{"contact.jsonl", `{"contact":"synthetic\u0040example.invalid"}` + "\n" + `{"public":true}`, endpointguard.ErrBlocked},
			{"contact.json", `{"contact":"public",}`, endpointguard.ErrUnknown},
			{"contact.bin", "public data", endpointguard.ErrUnknown},
		} {
			t.Run(channel+"/"+sample.name, func(t *testing.T) {
				p := testEndpointPolicy(channel)
				actions, audits := 0, 0
				fact, err := endpointguard.Run(p, endpointguard.Identity{DeviceRef: p.DeviceRef, OperationRef: strings.Repeat("c", 64)}, sourceFileFormat(sample.name), []byte(sample.raw), func(c datasafety.Context) error {
					audits++
					if c.Endpoint.Stage != "preflight" || c.Validate() != nil {
						t.Fatal("invalid denied preflight evidence")
					}
					return nil
				}, func([]byte) error { actions++; return nil })
				if !errors.Is(err, sample.want) || actions != 0 || audits != 1 {
					t.Fatalf("format-aware protection failed: action=%d audit=%d enforcement=%s err=%v", actions, audits, fact.Enforcement, err)
				}
			})
		}
	}
}

func TestNativeObservationIdentitiesAreUniqueAcrossStreamsAndTargets(t *testing.T) {
	c := &endpointClient{key: []byte(strings.Repeat("k", 32)), scope: "test"}
	first := c.observationRef("stream-one", "clipboard", "content_changed", "1", "clipboard")
	if first == c.observationRef("stream-two", "clipboard", "content_changed", "1", "clipboard") {
		t.Fatal("observer restart reused event identity")
	}
	printerA := c.observationRef("stream-one", "print", "job_observed", "1:20261113T120000.000", "printer A")
	printerB := c.observationRef("stream-one", "print", "job_observed", "1:20261113T120000.000", "printer B")
	if printerA == printerB {
		t.Fatal("different printer queues reused event identity")
	}
	if first != c.observationRef("stream-one", "clipboard", "content_changed", "1", "clipboard") {
		t.Fatal("one observation lost deterministic retry identity")
	}
}

func TestEndpointFinalAuditOutboxReplayCannotRepeatAction(t *testing.T) {
	var completionBodies []string
	rejectCompletion := true
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var event store.SecurityEventInput
		if json.Unmarshal(raw, &event) != nil || event.DataContext == nil || event.DataContext.Endpoint == nil {
			t.Error("invalid audit projection")
			w.WriteHeader(400)
			return
		}
		if strings.Contains(string(raw), "safe source fixture") {
			t.Error("source content entered audit or outbox")
		}
		if event.DataContext.Endpoint.Stage == "completed" {
			completionBodies = append(completionBodies, string(raw))
			if rejectCompletion {
				w.WriteHeader(503)
				return
			}
		}
		w.WriteHeader(201)
	}))
	defer s.Close()
	c := &endpointClient{origin: s.URL, key: []byte(strings.Repeat("k", 32)), scope: "test", device: strings.Repeat("a", 64), http: s.Client()}
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	p := testEndpointPolicy("print")
	actions := 0
	fact, err := endpointguard.Run(p, endpointguard.Identity{DeviceRef: c.device, OperationRef: strings.Repeat("c", 64)}, sourceFileFormat("safe.txt"), []byte("safe source fixture"), func(capture datasafety.Context) error {
		return deliver(t.Context(), c, root, c.event("one-attempt", capture))
	}, func([]byte) error { actions++; return nil })
	if !errors.Is(err, endpointguard.ErrAuditPending) || actions != 1 || fact.Enforcement != "allowed" {
		t.Fatalf("completion failure misclassified or action repeated: %v, actions=%d", err, actions)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatal("completion receipt not durably retained")
	}
	rejectCompletion = false
	if err := replay(t.Context(), c, root); err != nil {
		t.Fatal(err)
	}
	if actions != 1 || len(completionBodies) != 2 || completionBodies[0] != completionBodies[1] {
		t.Fatal("replay changed the receipt or repeated the action")
	}
	entries, err = os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("accepted outbox receipt not removed")
	}
}

func TestEndpointOfflinePreflightRemainsAnUnperformedAttemptAfterReplay(t *testing.T) {
	reject := true
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reject {
			w.WriteHeader(503)
			return
		}
		var e store.SecurityEventInput
		if json.NewDecoder(r.Body).Decode(&e) != nil || e.DataContext == nil || e.DataContext.Endpoint.Stage != "observed" {
			t.Error("preflight replay became a completed action")
		}
		w.WriteHeader(201)
	}))
	defer s.Close()
	c := &endpointClient{origin: s.URL, key: []byte(strings.Repeat("k", 32)), scope: "test", device: strings.Repeat("a", 64), http: s.Client()}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	p := testEndpointPolicy("clipboard")
	actions := 0
	_, err = endpointguard.Run(p, endpointguard.Identity{DeviceRef: c.device, OperationRef: strings.Repeat("c", 64)}, "txt", []byte("public fixture"), func(capture datasafety.Context) error {
		return deliver(t.Context(), c, root, c.event("offline-preflight", capture))
	}, func([]byte) error { actions++; return nil })
	if !errors.Is(err, endpointguard.ErrAudit) || actions != 0 {
		t.Fatal("offline preflight started an action")
	}
	reject = false
	if err := replay(t.Context(), c, root); err != nil || actions != 0 {
		t.Fatal("replay executed an unaudited action")
	}
}
