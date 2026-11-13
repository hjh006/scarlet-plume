package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/endpointguard"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func TestPlatformTimeAnchorsReceiptsEventsAndLocalTTLAcrossHostSkew(t *testing.T) {
	for _, offset := range []time.Duration{-4 * time.Minute, 0, 4 * time.Minute} {
		t.Run(offset.String(), func(t *testing.T) {
			issued := time.Now().UTC().Add(offset)
			// DB approval can be milliseconds after another platform timestamp.
			approved := issued.Add(25 * time.Millisecond)
			device := strings.Repeat("a", 64)
			p := activePolicy{ID: 7, Revision: 2, DeviceRef: device, Channel: "clipboard", Mode: "block_sensitive", Scope: "controlled_operations", Digest: strings.Repeat("b", 64), Categories: []string{"email"}, ExpiresAt: issued.Add(2 * time.Minute), ApprovedAt: &approved, Executor: "windows_clipboard_write"}
			receipts, events, heartbeats := 0, 0, 0
			var eventTimes []time.Time
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					_ = json.NewEncoder(w).Encode(policyBundle{DeviceRef: device, IssuedAt: issued, Policies: []activePolicy{p}})
					return
				}
				var at time.Time
				switch r.URL.Path {
				case "/api/v1/endpoint/heartbeat":
					var b struct {
						ObservedAt time.Time `json:"observedAt"`
					}
					if json.NewDecoder(r.Body).Decode(&b) != nil {
						t.Error("invalid heartbeat")
						w.WriteHeader(400)
						return
					}
					if delta := time.Since(b.ObservedAt); delta < -time.Second || delta > time.Second {
						t.Error("heartbeat no longer uses host time")
					}
					heartbeats++
					w.WriteHeader(201)
					return
				case "/api/v1/endpoint/receipts":
					var b struct {
						ObservedAt time.Time `json:"observedAt"`
					}
					if json.NewDecoder(r.Body).Decode(&b) != nil {
						t.Error("invalid receipt")
						w.WriteHeader(400)
						return
					}
					at = b.ObservedAt
					receipts++
				case "/api/v1/operations/security-events":
					var e store.SecurityEventInput
					if json.NewDecoder(r.Body).Decode(&e) != nil || e.DataContext == nil || e.DataContext.Validate() != nil {
						t.Error("invalid event")
						w.WriteHeader(400)
						return
					}
					at = e.ObservedAt
					eventTimes = append(eventTimes, at)
					events++
				default:
					w.WriteHeader(404)
					return
				}
				// Retain strict server approval/expiry gates; do not add skew here.
				if at.Before(approved) || !at.Before(p.ExpiresAt) {
					w.WriteHeader(409)
					return
				}
				w.WriteHeader(201)
			}))
			defer s.Close()
			c := &endpointClient{origin: s.URL, key: []byte(strings.Repeat("k", 32)), scope: "clock-test", device: device, http: s.Client()}
			loaded, err := c.policy(t.Context(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if now := c.now(); now.Before(approved) || now.After(approved.Add(time.Second)) {
				t.Fatal("platform anchor ignored approval floor or host skew")
			}
			if err := c.heartbeat(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := c.receipt(t.Context(), loaded); err != nil {
				t.Fatal("strict receipt gate rejected platform time:", err)
			}
			guard := c.guardPolicy(loaded)
			if remaining := time.Until(guard.ExpiresAt); remaining < 118*time.Second || remaining > 120*time.Second {
				t.Fatalf("server TTL did not become a local deadline: %v", remaining)
			}
			actions := 0
			_, err = endpointguard.Run(guard, endpointguard.Identity{DeviceRef: device, OperationRef: strings.Repeat("c", 64)}, "txt", []byte("public fixture"), func(f datasafety.Context) error {
				return c.request(t.Context(), "POST", "/api/v1/operations/security-events", c.event("clock-fixture", f), nil)
			}, func([]byte) error { actions++; return nil })
			if err != nil || actions != 1 || receipts != 1 || events != 2 || heartbeats != 1 {
				t.Fatalf("clock-corrected operation failed: %v; action=%d receipt=%d event=%d", err, actions, receipts, events)
			}
			if eventTimes[1].Before(eventTimes[0]) {
				t.Fatal("monotonic elapsed platform time moved backwards")
			}
		})
	}
}

func TestPlatformClockRejectsUnboundedOrExpiredBundles(t *testing.T) {
	for _, name := range []string{"old", "future", "future approval", "expired on platform", "missing approval", "wrong device"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC()
			approved := now.Add(-time.Second)
			p := activePolicy{ID: 1, DeviceRef: strings.Repeat("a", 64), Scope: "controlled_operations", ApprovedAt: &approved, ExpiresAt: now.Add(time.Minute)}
			b := policyBundle{DeviceRef: p.DeviceRef, IssuedAt: now, Policies: []activePolicy{p}}
			switch name {
			case "old":
				b.IssuedAt = now.Add(-6 * time.Minute)
			case "future":
				b.IssuedAt = now.Add(6 * time.Minute)
			case "future approval":
				future := now.Add(6 * time.Minute)
				b.Policies[0].ApprovedAt = &future
			case "expired on platform":
				b.IssuedAt = now.Add(4 * time.Minute)
				b.Policies[0].ExpiresAt = b.IssuedAt.Add(-time.Second)
			case "missing approval":
				b.Policies[0].ApprovedAt = nil
			case "wrong device":
				b.DeviceRef = strings.Repeat("d", 64)
			}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(b) }))
			defer s.Close()
			c := &endpointClient{origin: s.URL, device: p.DeviceRef, http: s.Client()}
			if _, err := c.policy(t.Context(), 1); err == nil {
				t.Fatal("invalid or expired platform policy accepted")
			}
			if !c.receivedAt.IsZero() {
				t.Fatal("rejected bundle changed the client clock")
			}
		})
	}
}

func TestSlowHostCannotStartActionAfterAuditCrossesPlatformTTL(t *testing.T) {
	issued := time.Now().UTC().Add(4 * time.Minute)
	approved := issued.Add(-time.Second)
	device := strings.Repeat("a", 64)
	p := activePolicy{ID: 1, Revision: 1, DeviceRef: device, Channel: "print", Mode: "monitor", Scope: "controlled_operations", Digest: strings.Repeat("b", 64), ExpiresAt: issued.Add(150 * time.Millisecond), ApprovedAt: &approved}
	preflights := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(policyBundle{DeviceRef: device, IssuedAt: issued, Policies: []activePolicy{p}})
			return
		}
		var event store.SecurityEventInput
		if json.NewDecoder(r.Body).Decode(&event) != nil || event.ObservedAt.Before(approved) || !event.ObservedAt.Before(p.ExpiresAt) {
			w.WriteHeader(409)
			return
		}
		preflights++
		// Deliberately cross a short TTL while the synchronous audit is pending.
		// This is the boundary being tested, not a delay to conceal clock races.
		timer := time.NewTimer(300 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		w.WriteHeader(201)
	}))
	defer s.Close()
	c := &endpointClient{origin: s.URL, key: []byte(strings.Repeat("k", 32)), scope: "ttl-test", device: device, http: s.Client()}
	loaded, err := c.policy(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	actions := 0
	_, err = endpointguard.Run(c.guardPolicy(loaded), endpointguard.Identity{DeviceRef: device, OperationRef: strings.Repeat("c", 64)}, "txt", []byte("public fixture"), func(f datasafety.Context) error {
		return c.request(t.Context(), "POST", "/api/v1/operations/security-events", c.event("ttl-fixture", f), nil)
	}, func([]byte) error { actions++; return nil })
	if !errors.Is(err, endpointguard.ErrPolicy) || actions != 0 || preflights != 1 {
		t.Fatalf("slow host extended platform TTL: err=%v action=%d preflight=%d", err, actions, preflights)
	}
}
