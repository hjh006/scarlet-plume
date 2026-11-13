package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/endpointnative"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func clipboardResponderFixture(t *testing.T) (*clipboardResponder, *int, *endpointnative.ClipboardClearResult) {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	c := &endpointClient{device: strings.Repeat("a", 64), key: []byte(strings.Repeat("k", 32)), scope: "synthetic-session"}
	b := clipboardBinding{strings.Repeat("b", 64), strings.Repeat("c", 64)}
	approved := time.Now().Add(-time.Minute)
	p := store.EndpointPolicy{EndpointPolicyInput: store.EndpointPolicyInput{DeviceRef: c.device, Channel: "clipboard", Scope: "session_response", Mode: "clear_sensitive", Categories: []string{"email"}, TargetScope: "interactive_session_clipboard", TargetRefs: []string{b.sessionRef}, ConfigDigest: b.configDigest, Revision: 1}, ID: 1, Digest: strings.Repeat("d", 64), Status: "approved", Executor: clipboardSessionExecutor, ExpiresAt: time.Now().Add(time.Hour), ApprovedAt: &approved}
	calls := new(int)
	result := &endpointnative.ClipboardClearResult{Status: "cleared", Attempted: true, ReadbackEmpty: true, SequenceMatched: true, CurrentSequence: 123}
	r := &clipboardResponder{c: c, binding: b, cache: &clipboardPolicyCache{policy: &p, ready: true, loaded: true, deadline: time.Now().Add(time.Hour)}, queue: &clipboardOutbox{root: root, client: c}, stream: "synthetic-stream"}
	r.clear = func(ctx context.Context, m endpointnative.ClipboardMatch) (endpointnative.ClipboardClearResult, error) {
		facts := clipboardQueuedFacts(t, r.queue)
		if len(facts) < 1 || facts[len(facts)-1].Code != "session_clear_intent" {
			t.Fatal("clear preceded durable anonymous intent")
		}
		if ctx.Err() != nil {
			return endpointnative.ClipboardClearResult{}, ctx.Err()
		}
		*calls++
		return *result, nil
	}
	return r, calls, result
}
func clipboardQueuedFacts(t *testing.T, q *clipboardOutbox) []store.EndpointSessionEventInput {
	t.Helper()
	names, err := q.names()
	if err != nil {
		t.Fatal(err)
	}
	var facts []store.EndpointSessionEventInput
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(q.root.Name(), name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "synthetic@example.test") {
			t.Fatal("raw text persisted")
		}
		var e clipboardEnvelope
		if json.Unmarshal(raw, &e) != nil {
			t.Fatal("invalid metadata")
		}
		if e.Event != nil {
			facts = append(facts, *e.Event)
		}
	}
	return facts
}
func syntheticClipboardChange() endpointnative.ClipboardSessionEvent {
	return endpointnative.ClipboardSessionEvent{Kind: "content_changed", Status: "partial", Match: endpointnative.ClipboardMatch{Sequence: 123, Owner: 99}, Text: []byte("Office synthetic@example.test"), Complete: false, TextComplete: true, Stable: true, ClearEligible: true}
}
func TestClipboardPartialPositiveRequiresIntentWithoutClaimingPrevention(t *testing.T) {
	r, calls, _ := clipboardResponderFixture(t)
	if err := r.event(t.Context(), syntheticClipboardChange()); err != nil {
		t.Fatal(err)
	}
	facts := clipboardQueuedFacts(t, r.queue)
	if *calls != 1 || len(facts) != 2 || facts[0].Code != "session_clear_intent" || facts[1].Status != "cleared_after_change" || facts[1].Capture != "partial" || facts[0].OperationRef != facts[1].OperationRef || facts[1].SequenceRef != facts[1].CurrentSequenceRef {
		t.Fatal("partial positive/sequence/phase binding lost")
	}
	for _, e := range facts {
		if e.Status == "blocked" || strings.Contains(e.Code, "functional") {
			t.Fatal("reactive clear became prevention")
		}
	}
}
func TestClipboardIncompleteNoMatchAndTruncatedTokenStayUnknown(t *testing.T) {
	for _, text := range []string{"unclassified incomplete text", "synthetic@example.test"} {
		r, calls, _ := clipboardResponderFixture(t)
		o := syntheticClipboardChange()
		o.Text = []byte(text)
		o.TextComplete = false
		if err := r.event(t.Context(), o); err != nil {
			t.Fatal(err)
		}
		facts := clipboardQueuedFacts(t, r.queue)
		if *calls != 0 || len(facts) != 1 || facts[0].Status != "unknown" || len(facts[0].Categories) != 0 {
			t.Fatal("partial/truncated token became safe or positive")
		}
	}
}
func TestClipboardBusyUnavailableAndStaleCannotClaimSuccess(t *testing.T) {
	for _, status := range []string{"busy", "unavailable", "unstable"} {
		r, calls, _ := clipboardResponderFixture(t)
		o := syntheticClipboardChange()
		o.Status = status
		o.Stable = false
		if err := r.event(t.Context(), o); err != nil {
			t.Fatal(err)
		}
		facts := clipboardQueuedFacts(t, r.queue)
		if *calls != 0 || len(facts) != 1 || facts[0].ClipboardEmpty || facts[0].Status == "cleared_after_change" {
			t.Fatal("failed observation cleared or claimed success")
		}
	}
	r, calls, result := clipboardResponderFixture(t)
	*result = endpointnative.ClipboardClearResult{Status: "stale", Reason: "sequence_changed", CurrentSequence: 456}
	if err := r.event(t.Context(), syntheticClipboardChange()); err != nil {
		t.Fatal(err)
	}
	facts := clipboardQueuedFacts(t, r.queue)
	if *calls != 1 || facts[1].Status != "stale" || facts[1].ClearAttempted || facts[1].SequenceRef == facts[1].CurrentSequenceRef {
		t.Fatal("stale skip evidence lost")
	}
}
func TestClipboardExpiredStoppedMissingOrEndedApprovalCannotAct(t *testing.T) {
	r, calls, _ := clipboardResponderFixture(t)
	r.cache.policy.ExpiresAt = time.Now().Add(-time.Second)
	if r.cache.current(r.c, r.binding) != nil || r.event(t.Context(), syntheticClipboardChange()) != nil || *calls != 0 {
		t.Fatal("expired approval acted")
	}
	r, calls, _ = clipboardResponderFixture(t)
	if os.WriteFile(filepath.Join(r.queue.root.Name(), "stop.request"), []byte("stop"), 0600) != nil {
		t.Fatal("fixture")
	}
	if r.event(t.Context(), syntheticClipboardChange()) != nil || *calls != 0 {
		t.Fatal("stop marker ignored")
	}
	r, calls, _ = clipboardResponderFixture(t)
	p := *r.cache.policy
	p.EndedAt = &p.CreatedAt
	if sessionPolicyMatches(p, r.c, r.binding) {
		t.Fatal("ended approval resurrected")
	}
	p = *r.cache.policy
	p.TargetRefs = []string{strings.Repeat("e", 64)}
	if sessionPolicyMatches(p, r.c, r.binding) {
		t.Fatal("wrong logon adopted")
	}
	r.cache.policy = nil
	if r.event(t.Context(), syntheticClipboardChange()) != nil || *calls != 0 || len(clipboardQueuedFacts(t, r.queue)) != 0 {
		t.Fatal("missing approval acted")
	}
}
func TestClipboardEvidenceBudgetStopsBeforeIrreversibleAction(t *testing.T) {
	r, calls, _ := clipboardResponderFixture(t)
	for range 999 {
		f, err := os.CreateTemp(r.queue.root.Name(), "blocked-*.sjson")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if r.event(t.Context(), syntheticClipboardChange()) == nil || *calls != 0 {
		t.Fatal("missing durable audit allowed clear")
	}
}
func TestClipboardReplayIsOrderedMetadataOnly(t *testing.T) {
	r, calls, _ := clipboardResponderFixture(t)
	if r.event(t.Context(), syntheticClipboardChange()) != nil {
		t.Fatal("fixture")
	}
	var codes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var e store.EndpointSessionEventInput
		if json.NewDecoder(req.Body).Decode(&e) != nil {
			t.Error("invalid wire metadata")
		}
		codes = append(codes, e.Code)
		w.WriteHeader(201)
	}))
	defer server.Close()
	r.c.origin = server.URL
	r.c.http = server.Client()
	if r.queue.replay(t.Context()) != nil || r.queue.replay(t.Context()) != nil || *calls != 1 || strings.Join(codes, ",") != "session_clear_intent,session_cleared_after_change" {
		t.Fatal("replay reordered evidence or repeated action")
	}
}
func TestClipboardReadyAndUncertainCloseRemainDistinct(t *testing.T) {
	r, _, _ := clipboardResponderFixture(t)
	r.cache.ready = false
	r.cache.loaded = false
	if err := r.event(t.Context(), endpointnative.ClipboardSessionEvent{Kind: "listener_ready"}); err != nil || !r.cache.ready {
		t.Fatal("listener ready failed")
	}
	names, _ := r.queue.names()
	if len(names) != 1 {
		t.Fatal("missing loaded receipt")
	}
	r, _, _ = clipboardResponderFixture(t)
	r.clear = func(context.Context, endpointnative.ClipboardMatch) (endpointnative.ClipboardClearResult, error) {
		return endpointnative.ClipboardClearResult{Status: "cleared", Attempted: true, SequenceMatched: true, ReadbackEmpty: true}, errors.New("uncertain close")
	}
	if r.event(t.Context(), syntheticClipboardChange()) != nil {
		t.Fatal("fixture")
	}
	facts := clipboardQueuedFacts(t, r.queue)
	if facts[1].Status != "unknown" || facts[1].ClipboardEmpty {
		t.Fatal("native failure became success")
	}
}

func TestClipboardReadinessAndPolicyArrivalHandshake(t *testing.T) {
	for _, readyFirst := range []bool{true, false} {
		r, _, _ := clipboardResponderFixture(t)
		p := *r.cache.policy
		r.cache = &clipboardPolicyCache{}
		if readyFirst {
			if err := r.cache.listenerReady(r.c, r.queue); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.cache.replace(r.c, &p, r.queue); err != nil {
			t.Fatal(err)
		}
		if !readyFirst {
			if r.cache.current(r.c, r.binding) != nil {
				t.Fatal("policy adopted before a registered listener")
			}
			if err := r.cache.listenerReady(r.c, r.queue); err != nil {
				t.Fatal(err)
			}
		}
		if r.cache.current(r.c, r.binding) == nil {
			t.Fatal("registered listener and policy failed to adopt")
		}
		names, _ := r.queue.names()
		if len(names) != 1 {
			t.Fatal("handshake missed or duplicated durable loaded receipt")
		}
		if err := r.cache.replace(r.c, &p, r.queue); err != nil {
			t.Fatal(err)
		}
		again, _ := r.queue.names()
		if len(again) != 1 {
			t.Fatal("same bundle duplicated listener receipt")
		}
	}
}
func TestClipboardClockRefreshCannotExtendAdoptedMonotonicTTL(t *testing.T) {
	r, _, _ := clipboardResponderFixture(t)
	p := *r.cache.policy
	r.cache = &clipboardPolicyCache{ready: true}
	if err := r.cache.replace(r.c, &p, r.queue); err != nil {
		t.Fatal(err)
	}
	original := r.cache.deadline
	r.c.clockMu.Lock()
	r.c.platformTime = time.Now().Add(-5 * time.Minute)
	r.c.receivedAt = time.Now()
	r.c.clockMu.Unlock()
	if err := r.cache.replace(r.c, &p, r.queue); err != nil {
		t.Fatal(err)
	}
	if !r.cache.deadline.Equal(original) {
		t.Fatal("same policy refreshed and extended monotonic TTL")
	}
	r.cache.deadline = time.Now().Add(-time.Millisecond)
	if r.cache.current(r.c, r.binding) != nil {
		t.Fatal("slow server clock revived elapsed local TTL")
	}
	if err := r.cache.expired(r.c, r.queue); err != nil || r.cache.policy != nil {
		t.Fatal("expired local TTL remained active")
	}
}

func TestClipboardRetiredApprovalCannotReturnAfterClockRollback(t *testing.T) {
	r, _, _ := clipboardResponderFixture(t)
	p := *r.cache.policy
	r.cache = &clipboardPolicyCache{ready: true}
	if err := r.cache.replace(r.c, &p, r.queue); err != nil {
		t.Fatal(err)
	}
	r.cache.deadline = time.Now().Add(-time.Millisecond)
	r.c.clockMu.Lock()
	r.c.platformTime = time.Now().Add(-5 * time.Minute)
	r.c.receivedAt = time.Now()
	r.c.clockMu.Unlock()
	if err := r.cache.expired(r.c, r.queue); err != nil {
		t.Fatal(err)
	}
	if err := r.cache.replace(r.c, &p, r.queue); err != nil {
		t.Fatal(err)
	}
	if r.cache.policy != nil || r.cache.current(r.c, r.binding) != nil {
		t.Fatal("retired approval acquired a new TTL after clock rollback")
	}
	p.ID++
	p.Digest = strings.Repeat("f", 64)
	if err := r.cache.replace(r.c, &p, r.queue); err != nil || r.cache.current(r.c, r.binding) == nil {
		t.Fatal("new independent approval could not be adopted")
	}
}
