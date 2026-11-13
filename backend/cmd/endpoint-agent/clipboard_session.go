package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hjh006/scarlet-plume/backend/internal/endpointnative"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

const clipboardSessionExecutor = "windows_clipboard_session_guard"

type clipboardBinding struct{ sessionRef, configDigest string }

func configureClipboardSession(c *endpointClient) (clipboardBinding, error) {
	identity, err := endpointnative.CurrentClipboardSession()
	if err != nil {
		return clipboardBinding{}, err
	}
	b := clipboardBinding{sessionRef: c.ref("clipboard-logon", identity.LocalKey())}
	b.configDigest = c.ref("clipboard-config", c.device+"\x00"+b.sessionRef+"\x00unicode-bounded-v1")
	c.subject = c.ref("subject", identity.UserSID)
	c.nativeService = true // Explicit full capabilities: never inherit the SYSTEM/other observers.
	c.nativeCapabilities = func() []channelCapability {
		return []channelCapability{
			{Channel: "clipboard", Scope: "session_response", Supported: true, Modes: []string{"monitor", "clear_sensitive", "clear_all"}, Executor: clipboardSessionExecutor, Reason: "interactive_change_listener_conditional_clear_not_pre_paste", TargetScope: "interactive_session_clipboard", TargetRefs: []string{b.sessionRef}, ConfigDigest: b.configDigest, Privilege: "interactive_user"},
			{Channel: "clipboard", Scope: "controlled_operations", Supported: true, Modes: []string{"monitor", "block_sensitive", "block_all"}, Executor: "windows_clipboard_write", Reason: "explicit_integrated_application_write_only"},
		}
	}
	return b, nil
}

type clipboardPolicyCache struct {
	mu            sync.RWMutex
	policy        *store.EndpointPolicy
	ready, loaded bool
	deadline      time.Time
	deadlines     map[string]time.Time
}

func (s *clipboardPolicyCache) current(c *endpointClient, b clipboardBinding) *store.EndpointPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.ready || !s.loaded || s.policy == nil || !s.deadline.After(time.Now()) || !sessionPolicyMatches(*s.policy, c, b) {
		return nil
	}
	p := *s.policy
	p.Categories = append([]string(nil), p.Categories...)
	return &p
}

func sessionPolicyMatches(p store.EndpointPolicy, c *endpointClient, b clipboardBinding) bool {
	return p.ID > 0 && p.Revision > 0 && p.DeviceRef == c.device && p.Scope == "session_response" && p.Channel == "clipboard" && p.Executor == clipboardSessionExecutor && p.Status == "approved" && p.EndedAt == nil && p.ApprovedAt != nil && !p.ApprovedAt.IsZero() && p.ExpiresAt.After(c.now()) && len(p.Digest) == 64 && p.TargetScope == "interactive_session_clipboard" && len(p.TargetRefs) == 1 && p.TargetRefs[0] == b.sessionRef && p.ConfigDigest == b.configDigest && slices.Contains([]string{"monitor", "clear_sensitive", "clear_all"}, p.Mode)
}
func sessionReceipt(c *endpointClient, p store.EndpointPolicy, status string) store.EndpointReceiptInput {
	code := map[string]string{"loaded": "session_guard_loaded", "stopped": "session_guard_stopped", "failed": "session_guard_failed"}[status]
	at := c.now()
	return store.EndpointReceiptInput{ReceiptRef: c.ref("session-receipt", p.Digest+"\x00"+status+"\x00"+at.Format(time.RFC3339Nano)), DeviceRef: c.device, PolicyID: p.ID, PolicyRevision: p.Revision, PolicyDigest: p.Digest, TargetScope: p.TargetScope, TargetRefs: append([]string(nil), p.TargetRefs...), ConfigDigest: p.ConfigDigest, Channel: "clipboard", Scope: "session_response", Executor: clipboardSessionExecutor, Status: status, Code: code, ObservedAt: at}
}
func (s *clipboardPolicyCache) queueLoaded(c *endpointClient, q *clipboardOutbox) error {
	if s.ready && !s.loaded && s.policy != nil {
		if err := q.receipt(sessionReceipt(c, *s.policy, "loaded")); err != nil {
			return err
		}
		s.loaded = true
	}
	return nil
}
func (s *clipboardPolicyCache) listenerReady(c *endpointClient, q *clipboardOutbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = true
	return s.queueLoaded(c, q)
}
func clipboardApprovalKey(p store.EndpointPolicy) string {
	return strconv.FormatInt(p.ID, 10) + ":" + p.Digest
}
func (s *clipboardPolicyCache) retire(p store.EndpointPolicy) {
	if s.deadlines == nil {
		s.deadlines = map[string]time.Time{}
	}
	s.deadlines[clipboardApprovalKey(p)] = time.Time{}
}

func (s *clipboardPolicyCache) replace(c *endpointClient, p *store.EndpointPolicy, q *clipboardOutbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.policy
	if old != nil && p != nil && old.ID == p.ID && old.Digest == p.Digest {
		return s.queueLoaded(c, q)
	}
	s.policy = nil
	s.loaded = false
	if old != nil {
		s.retire(*old)
		if err := q.receipt(sessionReceipt(c, *old, "stopped")); err != nil {
			return err
		}
	}
	if p != nil {
		if s.deadlines == nil {
			s.deadlines = map[string]time.Time{}
		}
		key := clipboardApprovalKey(*p)
		deadline, seen := s.deadlines[key]
		if seen && !deadline.After(time.Now()) {
			return nil
		}
		if !seen {
			if len(s.deadlines) >= 1000 {
				return errors.New("clipboard approval history budget reached; no new response adopted")
			}
			deadline = time.Now().Add(p.ExpiresAt.Sub(c.now()))
			s.deadlines[key] = deadline
		}
		copy := *p
		copy.TargetRefs = append([]string(nil), p.TargetRefs...)
		copy.Categories = append([]string(nil), p.Categories...)
		s.policy = &copy
		s.deadline = deadline
		if err := s.queueLoaded(c, q); err != nil {
			s.policy = nil
			s.loaded = false
			return err
		}
	}
	return nil
}
func (s *clipboardPolicyCache) expired(c *endpointClient, q *clipboardOutbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policy != nil && (!s.deadline.After(time.Now()) || !s.policy.ExpiresAt.After(c.now())) {
		p := *s.policy
		s.retire(p)
		s.policy = nil
		s.loaded = false
		return q.receipt(sessionReceipt(c, p, "stopped"))
	}
	return nil
}
func (s *clipboardPolicyCache) finish(c *endpointClient, q *clipboardOutbox, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.policy
	if p != nil {
		s.retire(*p)
	}
	s.policy = nil
	s.ready = false
	s.loaded = false
	if p != nil {
		return q.receipt(sessionReceipt(c, *p, status))
	}
	return nil
}

func fetchClipboardPolicy(ctx context.Context, c *endpointClient, b clipboardBinding) (*store.EndpointPolicy, error) {
	var bundle struct {
		DeviceRef string                 `json:"deviceRef"`
		IssuedAt  time.Time              `json:"issuedAt"`
		Policies  []store.EndpointPolicy `json:"policies"`
	}
	if err := c.request(ctx, "GET", "/api/v1/endpoint/policy?deviceRef="+c.device, nil, &bundle); err != nil {
		return nil, err
	}
	received := time.Now()
	lower, upper := received.Add(-5*time.Minute), received.Add(5*time.Minute)
	if bundle.DeviceRef != c.device || bundle.IssuedAt.Before(lower) || bundle.IssuedAt.After(upper) || len(bundle.Policies) > 100 {
		return nil, errors.New("fresh bounded clipboard policy bundle required")
	}
	base := bundle.IssuedAt
	for _, p := range bundle.Policies {
		if p.ApprovedAt != nil && p.ApprovedAt.After(base) {
			base = *p.ApprovedAt
		}
	}
	if base.After(upper) {
		return nil, errors.New("clipboard policy time exceeds budget")
	}
	c.clockMu.Lock()
	c.platformTime, c.receivedAt = base.UTC(), received
	c.clockMu.Unlock()
	var chosen *store.EndpointPolicy
	for _, p := range bundle.Policies {
		if sessionPolicyMatches(p, c, b) {
			if chosen != nil {
				return nil, errors.New("ambiguous session policies; no clipboard response adopted")
			}
			copy := p
			chosen = &copy
		}
	}
	return chosen, nil
}

// clipboardOutbox contains metadata only. Intent and result have separate stable
// IDs; replay performs HTTP delivery and can never read or clear a clipboard.
type clipboardOutbox struct {
	root   *os.Root
	client *endpointClient
	mu     sync.Mutex
}
type clipboardEnvelope struct {
	Version int                              `json:"version"`
	Event   *store.EndpointSessionEventInput `json:"event,omitempty"`
	Receipt *store.EndpointReceiptInput      `json:"receipt,omitempty"`
}

func (q *clipboardOutbox) event(e store.EndpointSessionEventInput) error {
	return q.enqueue(clipboardEnvelope{Version: 1, Event: &e}, e.EventRef, e.ResponseAt)
}
func (q *clipboardOutbox) receipt(r store.EndpointReceiptInput) error {
	return q.enqueue(clipboardEnvelope{Version: 1, Receipt: &r}, r.ReceiptRef, r.ObservedAt)
}
func (q *clipboardOutbox) enqueue(e clipboardEnvelope, ref string, at time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries, err := q.names()
	if err != nil || len(entries) >= 1000 || e.Event != nil && e.Event.Code == "session_clear_intent" && len(entries) >= 999 {
		return errors.New("clipboard evidence budget exhausted; response stopped")
	}
	raw, err := json.Marshal(e)
	if err != nil || len(raw) > 32<<10 || len(ref) != 64 {
		return errors.New("invalid clipboard evidence envelope")
	}
	name := strconv.FormatInt(at.UnixNano(), 10) + "-" + ref + ".sjson"
	f, err := q.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("clipboard evidence intent unavailable")
	}
	n, writeErr := f.Write(raw)
	if writeErr == nil && n == len(raw) {
		writeErr = f.Sync()
	} else {
		writeErr = errors.New("incomplete clipboard evidence")
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("clipboard evidence not durable; response stopped")
	}
	return nil
}
func (q *clipboardOutbox) names() ([]string, error) {
	f, err := q.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(2001)
	if err != nil && err != io.EOF || len(entries) > 2000 {
		return nil, errors.New("clipboard state exceeds directory budget")
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sjson") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}
func clipboardDeliveryPermanent(err error) bool {
	if err == nil || errors.Is(err, errEndpointUnavailable) || errors.Is(err, context.Canceled) {
		return false
	}
	var response endpointHTTPError
	if errors.As(err, &response) {
		return response.status >= 400 && response.status < 500 && response.status != 429
	}
	return true // Corrupt or inaccessible local audit is not a transient network loss.
}

func (q *clipboardOutbox) replay(ctx context.Context) error {
	// Only name enumeration shares the short writer lock; slow network requests
	// never hold it or block durable preparation of another response.
	q.mu.Lock()
	names, err := q.names()
	q.mu.Unlock()
	if err != nil {
		return err
	}
	for _, name := range names {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f, err := q.root.Open(name)
		if err != nil {
			return errors.New("clipboard evidence unavailable")
		}
		raw, err := io.ReadAll(io.LimitReader(f, 32<<10+1))
		f.Close()
		var e clipboardEnvelope
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		var extra any
		if err != nil || len(raw) > 32<<10 || d.Decode(&e) != nil || d.Decode(&extra) != io.EOF || e.Version != 1 || (e.Event == nil) == (e.Receipt == nil) {
			return errors.New("invalid protected clipboard metadata")
		}
		var path, ref string
		var body any
		var at time.Time
		if e.Event != nil {
			if e.Event.DeviceRef != q.client.device || e.Event.Scope != "session_response" || e.Event.Executor != clipboardSessionExecutor {
				return errors.New("clipboard outbox source binding invalid")
			}
			path, ref, body, at = "/api/v1/endpoint/session-events", e.Event.EventRef, e.Event, e.Event.ResponseAt
		} else {
			if e.Receipt.DeviceRef != q.client.device || e.Receipt.Scope != "session_response" || e.Receipt.Executor != clipboardSessionExecutor {
				return errors.New("clipboard receipt source binding invalid")
			}
			path, ref, body, at = "/api/v1/endpoint/receipts", e.Receipt.ReceiptRef, e.Receipt, e.Receipt.ObservedAt
		}
		if name != strconv.FormatInt(at.UnixNano(), 10)+"-"+ref+".sjson" {
			return errors.New("clipboard evidence filename binding invalid")
		}
		if err := q.client.request(ctx, "POST", path, body, nil); err != nil {
			return err
		}
		if q.root.Remove(name) != nil {
			return errors.New("accepted clipboard evidence cleanup pending")
		}
	}
	return nil
}

type clipboardResponder struct {
	c       *endpointClient
	binding clipboardBinding
	cache   *clipboardPolicyCache
	queue   *clipboardOutbox
	stream  string
	clear   func(context.Context, endpointnative.ClipboardMatch) (endpointnative.ClipboardClearResult, error)
}

func clipboardStopRequested(root *os.Root) bool {
	_, err := root.Stat("stop.request")
	return !errors.Is(err, os.ErrNotExist)
}

// Do not recognize a token whose apparent end was created by truncation.
func clipboardInspectionPrefix(raw []byte, complete bool) []byte {
	if complete {
		return raw
	}
	last := -1
	for i, ch := range string(raw) {
		if unicode.IsSpace(ch) {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	return raw[:last]
}

func (r *clipboardResponder) event(ctx context.Context, o endpointnative.ClipboardSessionEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.Kind == "listener_ready" {
		return r.cache.listenerReady(r.c, r.queue)
	}
	if clipboardStopRequested(r.queue.root) {
		return nil
	}
	p := r.cache.current(r.c, r.binding)
	if p == nil {
		return nil
	}
	at := r.c.now()
	op := r.c.ref("clipboard-session-operation", r.binding.sessionRef+"\x00"+r.stream+"\x00"+strconv.FormatUint(uint64(o.Match.Sequence), 10)+"\x00"+at.Format(time.RFC3339Nano))
	e := store.EndpointSessionEventInput{OperationRef: op, DeviceRef: r.c.device, SessionRef: r.binding.sessionRef, PolicyID: p.ID, PolicyRevision: p.Revision, PolicyDigest: p.Digest, Scope: "session_response", Executor: clipboardSessionExecutor, ConfigDigest: r.binding.configDigest, ObservedAt: at, ResponseAt: at, Capture: "unknown", Categories: []string{}}
	if o.Match.Sequence != 0 {
		e.SequenceRef = r.c.ref("clipboard-sequence", r.binding.sessionRef+":"+strconv.FormatUint(uint64(o.Match.Sequence), 10))
	}
	if len(o.Text) > 0 {
		fact, raw, err := datasafety.InspectText(bytes.NewReader(clipboardInspectionPrefix(o.Text, o.TextComplete)))
		clear(raw)
		if err == nil {
			e.Categories = append([]string{}, fact.Categories...)
			e.Capture = "partial"
			if o.Complete && fact.Capture == "complete" {
				e.Capture = "complete"
			}
		}
		e.ContentRef = r.c.ref("clipboard-content", string(o.Text))
	}
	e.Status, e.Code = "observed", "session_observed"
	shouldClear := p.Mode == "clear_all"
	if p.Mode == "clear_sensitive" {
		for _, category := range e.Categories {
			if slices.Contains(p.Categories, category) {
				shouldClear = true
			}
		}
		if !shouldClear {
			if e.Capture == "complete" {
				e.Status, e.Code = "not_required", "session_no_selected_match"
			} else {
				e.Status, e.Code = "unknown", "session_capture_unknown"
			}
		}
	}
	if p.Mode == "monitor" && e.Capture != "complete" {
		e.Status, e.Code = "unknown", "session_capture_unknown"
	}
	if o.Status == "busy" {
		e.Status, e.Code = "busy", "session_clipboard_busy"
		shouldClear = false
	} else if o.Status == "unavailable" {
		e.Status, e.Code = "unavailable", "session_unavailable"
		shouldClear = false
	} else if o.Status == "empty" {
		e.Status, e.Code = "observed", "session_observed"
		shouldClear = false
	} else if !o.Stable {
		e.Status, e.Code = "unknown", "session_sequence_unknown"
		shouldClear = false
	}
	if !shouldClear {
		return r.record(e, "observed")
	}
	if !o.ClearEligible {
		e.Status, e.Code = "unknown", "session_sequence_unknown"
		return r.record(e, "ineligible")
	}
	e.Status, e.Code = "unknown", "session_clear_intent"
	if err := r.record(e, "intent"); err != nil {
		return err
	}
	r.cache.mu.RLock()
	defer r.cache.mu.RUnlock()
	current := r.cache.policy
	if clipboardStopRequested(r.queue.root) || !r.cache.ready || !r.cache.loaded || !r.cache.deadline.After(time.Now()) || current == nil || current.Digest != p.Digest || !sessionPolicyMatches(*current, r.c, r.binding) {
		e.Status, e.Code = "unknown", "session_policy_inactive"
		e.ResponseAt = r.c.now()
		return r.record(e, "result")
	}
	deadline := r.cache.deadline
	serverDeadline := time.Now().Add(p.ExpiresAt.Sub(r.c.now()))
	if serverDeadline.Before(deadline) {
		deadline = serverDeadline
	}
	actionCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	result, err := r.clear(actionCtx, o.Match)
	e.ResponseAt = r.c.now()
	e.ClearAttempted = result.Attempted
	e.SequenceMatched = result.SequenceMatched
	e.ClipboardEmpty = result.ReadbackEmpty
	if result.CurrentSequence != 0 {
		e.CurrentSequenceRef = r.c.ref("clipboard-sequence", r.binding.sessionRef+":"+strconv.FormatUint(uint64(result.CurrentSequence), 10))
	}
	e.Status, e.Code = "unknown", "session_clear_failed"
	switch result.Status {
	case "cleared":
		e.Status, e.Code = "cleared_after_change", "session_cleared_after_change"
	case "stale":
		e.Status, e.Code = "stale", "session_changed"
		if result.Reason == "sequence_changed" {
			e.Code = "session_sequence_changed"
		}
	case "busy":
		e.Status, e.Code = "busy", "session_clipboard_busy"
	case "unsupported":
		e.Status, e.Code = "unsupported", "session_format_unsupported"
	case "session_unavailable":
		e.Status, e.Code = "unavailable", "session_unavailable"
	}
	if err != nil && result.Status == "cleared" {
		e.Status, e.Code = "unknown", "session_clear_failed"
		e.ClipboardEmpty = false
	}
	return r.record(e, "result")
}
func (r *clipboardResponder) record(e store.EndpointSessionEventInput, stage string) error {
	e.EventRef = r.c.ref("clipboard-session-event", e.OperationRef+":"+stage)
	return r.queue.event(e)
}

func runClipboardSession(ctx context.Context, c *endpointClient, root *os.Root, b clipboardBinding, mode string) error {
	q := &clipboardOutbox{root: root, client: c}
	if mode == "session-stop" {
		f, err := root.OpenFile("stop.request", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		if err != nil {
			return errors.New("clipboard stop request unavailable")
		}
		_, err = f.WriteString("stop-after-current-response\n")
		if err == nil {
			err = f.Sync()
		}
		closed := f.Close()
		if err != nil || closed != nil {
			return errors.New("clipboard stop request not durable")
		}
		return nil
	}
	release, err := lockClipboardSession(root)
	if err != nil {
		return err
	}
	defer release()
	if mode == "session-replay" {
		return q.replay(ctx)
	}
	if mode != "session-watch" {
		return errors.New("unsupported session runner mode")
	}
	// Deliberate start resumes this observer only; it never restores old content.
	for _, name := range []string{"stop.request", "running.marker"} {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("clipboard runtime markers unavailable")
		}
	}
	marker, err := root.OpenFile("running.marker", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("clipboard runtime marker unavailable")
	}
	_, err = marker.WriteString("interactive-observer-v1\n")
	if err == nil {
		err = marker.Sync()
	}
	closeErr := marker.Close()
	if err != nil || closeErr != nil {
		return errors.New("clipboard runtime marker not durable")
	}
	defer root.Remove("running.marker")
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cache := &clipboardPolicyCache{}
	responder := &clipboardResponder{c: c, binding: b, cache: cache, queue: q, stream: rand.Text(), clear: endpointnative.ClearClipboardIfCurrent}
	failure := make(chan error, 1)
	networkDone := make(chan struct{})
	go func() {
		defer close(networkDone)
		refresh := time.NewTicker(15 * time.Second)
		defer refresh.Stop()
		flush := time.NewTicker(2 * time.Second)
		defer flush.Stop()
		update := func() error {
			if err := c.heartbeat(watchCtx); err != nil {
				return err
			}
			p, err := fetchClipboardPolicy(watchCtx, c, b)
			if err != nil {
				return err
			}
			return cache.replace(c, p, q)
		}
		err := update()
		if sourceAuthorizationRejected(err) {
			_ = cache.replace(c, nil, q)
			failure <- err
			cancel()
			return
		}
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-refresh.C:
				err := update()
				if sourceAuthorizationRejected(err) {
					_ = cache.replace(c, nil, q)
					failure <- err
					cancel()
					return
				}
			case <-flush.C:
				if clipboardStopRequested(root) {
					_ = cache.replace(c, nil, q)
					cancel()
					return
				}
				if err := cache.expired(c, q); err != nil {
					failure <- err
					cancel()
					return
				}
				err := q.replay(watchCtx)
				if clipboardDeliveryPermanent(err) {
					_ = cache.replace(c, nil, q)
					failure <- err
					cancel()
					return
				}
			}
		}
	}()
	err = endpointnative.WatchClipboardSession(watchCtx, func() bool {
		return !clipboardStopRequested(root) && cache.current(c, b) != nil
	}, func(o endpointnative.ClipboardSessionEvent) error { return responder.event(watchCtx, o) })
	cancel()
	<-networkDone
	status := "stopped"
	if err != nil && !errors.Is(err, context.Canceled) {
		status = "failed"
	}
	_ = cache.finish(c, q, status)
	select {
	case e := <-failure:
		return e
	default:
		return err
	}
}
