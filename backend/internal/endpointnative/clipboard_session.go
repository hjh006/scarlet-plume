package endpointnative

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

var ErrClipboardSession = errors.New("active interactive clipboard session unavailable")

// Identity is local input for a caller-owned anonymous session reference. It
// describes a Windows logon, not a verified natural person. AuthenticationID
// is stable across helper restarts in that logon. BootGeneration comes from a
// fixed user-owned volatile key and prevents normal reboot/LUID reuse from
// reviving the previous approval. This is not same-user tamper protection.
type ClipboardSessionIdentity struct {
	UserSID          string `json:"-"`
	AuthenticationID string `json:"-"`
	SessionID        uint32 `json:"-"`
	BootGeneration   string `json:"-"`
}

func (i ClipboardSessionIdentity) LocalKey() string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s", i.UserSID, i.AuthenticationID, i.SessionID, i.BootGeneration)
}

func allowedClipboardSession(i ClipboardSessionIdentity, visible, input bool) bool {
	return i.UserSID != "" && i.UserSID != "S-1-5-18" && i.UserSID != "S-1-5-19" && i.UserSID != "S-1-5-20" && i.AuthenticationID != "" && i.AuthenticationID != "00000000:00000000" && i.SessionID != 0 && visible && input
}

// Match cannot be reconstructed from HTTP/JSON. Its private ticket is bound to
// the callback thread, active watcher, original sequence/owner and short age.
type ClipboardMatch struct {
	Sequence uint32  `json:"-"`
	Owner    uintptr `json:"-"`
	ticket   *clipboardSessionTicket
}

type ClipboardSessionEvent struct {
	Kind            string                   `json:"kind"`
	Status          string                   `json:"status"`
	Reason          string                   `json:"reason"`
	Session         ClipboardSessionIdentity `json:"-"`
	Match           ClipboardMatch           `json:"-"`
	Text            []byte                   `json:"-"`
	Formats         []uint32                 `json:"-"`
	Complete        bool                     `json:"complete"`
	TextComplete    bool                     `json:"textComplete"`
	FormatsComplete bool                     `json:"formatsComplete"`
	Stable          bool                     `json:"stable"`
	ClearEligible   bool                     `json:"clearEligible"`
	CaptureMillis   int64                    `json:"captureMillis"`
}

type ClipboardClearResult struct {
	Status          string `json:"status"`
	Reason          string `json:"reason"`
	Attempted       bool   `json:"attempted"`
	ReadbackEmpty   bool   `json:"readbackEmpty"`
	SequenceMatched bool   `json:"sequenceMatched"`
	CurrentSequence uint32 `json:"-"` // before EmptyClipboard, never the resulting new sequence
}

const clipboardSessionClearMaxAge = 5 * time.Second

type clipboardSessionAPI interface {
	identity() (ClipboardSessionIdentity, error)
	thread() uint32
	open(uintptr) bool
	close() bool
	sequence() uint32
	owner() uintptr
	formats() ([]uint32, bool)
	text() ([]byte, bool)
	empty() bool
}

type clipboardSessionTicket struct {
	api                        clipboardSessionAPI
	ctx                        context.Context
	identity                   ClipboardSessionIdentity
	window                     uintptr
	thread                     uint32
	sequence                   uint32
	owner                      uintptr
	at                         time.Time
	active, eligible, consumed bool
}

type clipboardSessionListener struct {
	ctx           context.Context
	api           clipboardSessionAPI
	window        uintptr
	identity      ClipboardSessionIdentity
	sequence      uint32
	shouldCapture func() bool
	emit          func(ClipboardSessionEvent) error
}

func (l *clipboardSessionListener) ready() error {
	l.sequence = l.api.sequence() // metadata only; never read existing formats/text
	return l.emit(ClipboardSessionEvent{Kind: "listener_ready", Status: "ready", Reason: "listener_registered", Session: l.identity})
}

func (l *clipboardSessionListener) notify() error {
	if err := l.ctx.Err(); err != nil {
		return err
	}
	sequence := l.api.sequence()
	if sequence == l.sequence {
		return nil
	}
	if !l.shouldCapture() {
		l.sequence = sequence
		return nil
	}
	if sequence == 0 {
		return l.emit(ClipboardSessionEvent{Kind: "content_changed", Status: "unavailable", Reason: "sequence_unavailable", Session: l.identity})
	}
	e := captureClipboardSession(l.ctx, l.api, l.window, l.identity)
	if e.Reason == "close_failed" {
		clear(e.Text)
		return ErrClipboardSession
	}
	if e.Stable {
		l.sequence = e.Match.Sequence
	}
	return deliverClipboardSession(e, l.emit)
}

func captureClipboardSession(ctx context.Context, api clipboardSessionAPI, window uintptr, identity ClipboardSessionIdentity) ClipboardSessionEvent {
	started := time.Now()
	e := ClipboardSessionEvent{Kind: "content_changed", Status: "unavailable", Reason: "session_unavailable", Session: identity}
	if ctx.Err() != nil {
		e.Reason = "context_canceled"
		return e
	}
	current, err := api.identity()
	if err != nil || current != identity {
		return e
	}
	if !api.open(window) {
		e.Status = "busy"
		e.Reason = "clipboard_busy"
		return e
	}
	// Close before returning: the caller may persist an intent or consult its
	// approved policy; no such work may retain the Windows clipboard lock.
	closed := false
	func() {
		defer func() { closed = api.close() }()
		sequence, owner := api.sequence(), api.owner()
		e.Match.Sequence, e.Match.Owner = sequence, owner
		if sequence == 0 {
			e.Status = "unavailable"
			e.Reason = "sequence_unavailable"
			return
		}
		e.Formats, e.FormatsComplete = api.formats()
		if slices.Contains(e.Formats, uint32(13)) {
			e.Text, e.TextComplete = api.text()
		}
		after, afterOwner := api.sequence(), api.owner()
		e.Stable = sequence != 0 && sequence == after && owner == afterOwner
		e.Status, e.Reason = "captured", "none"
		if sequence == 0 {
			e.Status, e.Reason = "unavailable", "sequence_unavailable"
		} else if sequence != after {
			e.Status, e.Reason = "unstable", "sequence_changed"
		} else if owner != afterOwner {
			e.Status, e.Reason = "unstable", "owner_changed"
		}
		onlyText := true
		for _, format := range e.Formats {
			if format != 1 && format != 7 && format != 13 && format != 16 {
				onlyText = false
			}
		}
		e.Complete = e.Stable && e.FormatsComplete && onlyText && e.TextComplete
		if e.FormatsComplete && len(e.Formats) == 0 {
			e.Status = "empty"
			e.Complete = e.Stable
		}
		if e.Status == "captured" && !e.Complete {
			e.Status = "partial"
			e.Reason = "representations_incomplete"
		}
	}()
	if !closed {
		clear(e.Text)
		e.Text = nil
		e.Complete = false
		e.TextComplete = false
		e.Stable = false
		e.Status = "unavailable"
		e.Reason = "close_failed"
		return e
	}
	current, err = api.identity()
	if err != nil || current != identity || ctx.Err() != nil {
		clear(e.Text)
		e.Text = nil
		e.Complete = false
		e.TextComplete = false
		e.Stable = false
		e.Status, e.Reason = "unavailable", "session_unavailable"
	}
	e.CaptureMillis = time.Since(started).Milliseconds()
	e.ClearEligible = e.Stable && e.Match.Owner != 0 && !(e.FormatsComplete && len(e.Formats) == 0) && ctx.Err() == nil
	e.Match.ticket = &clipboardSessionTicket{api: api, ctx: ctx, identity: identity, window: window, thread: api.thread(), sequence: e.Match.Sequence, owner: e.Match.Owner, at: time.Now(), active: true, eligible: e.ClearEligible}
	return e
}

// This shared implementation is exercised with synthetic APIs. The Windows
// public wrapper must run on the same locked callback thread as capture.
func clearClipboardSession(ctx context.Context, m ClipboardMatch) (r ClipboardClearResult, resultErr error) {
	r = ClipboardClearResult{Status: "stale", Reason: "capture_not_eligible"}
	t := m.ticket
	if t == nil {
		return r, nil
	}
	if t.api.thread() != t.thread {
		r.Reason = "wrong_thread"
		return r, nil
	}
	if !t.active || !t.eligible || t.consumed || m.Sequence != t.sequence || m.Owner != t.owner || m.Sequence == 0 || m.Owner == 0 {
		return r, nil
	}
	if time.Since(t.at) < 0 || time.Since(t.at) > clipboardSessionClearMaxAge {
		r.Reason = "stale_snapshot"
		return r, nil
	}
	if err := ctx.Err(); err != nil {
		r.Reason = "context_canceled"
		return r, err
	}
	if err := t.ctx.Err(); err != nil {
		r.Reason = "context_canceled"
		return r, err
	}
	identity, err := t.api.identity()
	if err != nil || identity != t.identity {
		r.Status = "session_unavailable"
		r.Reason = "session_changed"
		return r, nil
	}
	if !t.api.open(t.window) {
		r.Status = "busy"
		r.Reason = "clipboard_busy"
		return r, nil
	}
	defer func() {
		if !t.api.close() {
			r.Status = "unknown"
			r.Reason = "close_failed"
			resultErr = ErrClipboardSession
		}
	}()
	r.CurrentSequence = t.api.sequence()
	r.SequenceMatched = r.CurrentSequence != 0 && r.CurrentSequence == t.sequence
	if !r.SequenceMatched {
		r.Reason = "sequence_changed"
		return r, nil
	}
	if t.api.owner() != t.owner {
		r.Reason = "owner_changed"
		return r, nil
	}
	identity, err = t.api.identity() // active desktop and logon recheck while locked
	if err != nil || identity != t.identity {
		r.Status = "session_unavailable"
		r.Reason = "session_changed"
		return r, nil
	}
	if ctx.Err() != nil || t.ctx.Err() != nil || time.Since(t.at) > clipboardSessionClearMaxAge {
		r.Reason = "stale_snapshot"
		return r, nil
	}
	t.consumed = true
	r.Attempted = true
	if !t.api.empty() {
		r.Status = "failed"
		r.Reason = "empty_failed"
		return r, nil
	}
	formats, complete := t.api.formats()
	r.ReadbackEmpty = complete && len(formats) == 0
	if !complete {
		r.Status = "unknown"
		r.Reason = "readback_unavailable"
	} else if !r.ReadbackEmpty {
		r.Status = "unknown"
		r.Reason = "readback_not_empty"
	} else {
		r.Status = "cleared"
		r.Reason = "empty_readback"
	}
	return r, nil
}

func deliverClipboardSession(e ClipboardSessionEvent, emit func(ClipboardSessionEvent) error) error {
	defer clear(e.Text)
	if e.Match.ticket != nil {
		defer func() { e.Match.ticket.active = false }()
	}
	return emit(e)
}
