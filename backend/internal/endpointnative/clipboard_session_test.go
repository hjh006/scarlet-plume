package endpointnative

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeSessionClipboard struct {
	id                                          ClipboardSessionIdentity
	idErr                                       error
	seq                                         uint32
	owned, openedBy                             uintptr
	threadID                                    uint32
	locked                                      bool
	formatsValue                                []uint32
	formatsOK, textOK, openOK, closeOK, emptyOK bool
	textValue                                   []byte
	opens, reads, formatReads, empties          int
	onOpen, onText                              func()
	keepAfterEmpty                              bool
}

func fakeSession() *fakeSessionClipboard {
	return &fakeSessionClipboard{id: ClipboardSessionIdentity{UserSID: "S-1-5-21-111-222-333-1001", AuthenticationID: "00000000:12345678", SessionID: 2}, seq: 10, owned: 42, threadID: 7, formatsValue: []uint32{13}, formatsOK: true, textOK: true, openOK: true, closeOK: true, emptyOK: true, textValue: []byte("synthetic@example.invalid")}
}
func (f *fakeSessionClipboard) identity() (ClipboardSessionIdentity, error) { return f.id, f.idErr }
func (f *fakeSessionClipboard) thread() uint32                              { return f.threadID }
func (f *fakeSessionClipboard) open(window uintptr) bool {
	f.opens++
	if !f.openOK {
		return false
	}
	if f.locked {
		panic("clipboard lock retained across callback")
	}
	f.locked = true
	f.openedBy = window
	if f.onOpen != nil {
		f.onOpen()
	}
	return true
}
func (f *fakeSessionClipboard) close() bool {
	if f.closeOK {
		f.locked = false
	}
	return f.closeOK
}
func (f *fakeSessionClipboard) sequence() uint32 { return f.seq }
func (f *fakeSessionClipboard) owner() uintptr   { return f.owned }
func (f *fakeSessionClipboard) formats() ([]uint32, bool) {
	if !f.locked {
		panic("formats read without clipboard lock")
	}
	f.formatReads++
	return append([]uint32(nil), f.formatsValue...), f.formatsOK
}
func (f *fakeSessionClipboard) text() ([]byte, bool) {
	if !f.locked {
		panic("text read without clipboard lock")
	}
	f.reads++
	raw := append([]byte(nil), f.textValue...)
	if f.onText != nil {
		f.onText()
	}
	return raw, f.textOK
}
func (f *fakeSessionClipboard) empty() bool {
	if !f.locked {
		panic("clear without clipboard lock")
	}
	f.empties++
	if !f.emptyOK {
		return false
	}
	f.seq++
	f.owned = f.openedBy
	if !f.keepAfterEmpty {
		f.formatsValue = nil
	}
	return true
}

func TestClipboardSessionReadyAndUnapprovedChangesNeverReadExistingContent(t *testing.T) {
	f := fakeSession()
	capture := false
	var events []ClipboardSessionEvent
	l := clipboardSessionListener{ctx: t.Context(), api: f, window: 100, identity: f.id, shouldCapture: func() bool { return capture }, emit: func(e ClipboardSessionEvent) error {
		events = append(events, e)
		if f.locked {
			t.Fatal("callback held clipboard lock")
		}
		return nil
	}}
	if l.ready() != nil || len(events) != 1 || events[0].Kind != "listener_ready" || events[0].Status != "ready" || events[0].Match.ticket != nil {
		t.Fatal("listener readiness did not stay metadata-only")
	}
	if l.notify() != nil {
		t.Fatal("baseline notification failed")
	}
	f.seq++
	if l.notify() != nil {
		t.Fatal("unapproved notification failed")
	}
	capture = true
	if l.notify() != nil {
		t.Fatal("cache activation unexpectedly failed")
	}
	if f.opens != 0 || f.reads != 0 || f.formatReads != 0 || len(events) != 1 {
		t.Fatal("startup/disabled capture read pre-existing clipboard contents")
	}
	f.seq++
	if l.notify() != nil || f.reads != 1 || len(events) != 2 {
		t.Fatal("a genuine approved new change was not captured")
	}
	for _, b := range events[1].Text {
		if b != 0 {
			t.Fatal("callback-retained text buffer was not wiped")
		}
	}
}

func TestClipboardSessionClearComparesBeforeSequenceAndReadsBackWhileLocked(t *testing.T) {
	f := fakeSession()
	e := captureClipboardSession(t.Context(), f, 100, f.id)
	if !e.ClearEligible || !e.Complete || f.locked {
		t.Fatal("valid text capture did not release clipboard")
	}
	before := f.seq
	r, err := clearClipboardSession(t.Context(), e.Match)
	if err != nil || r.Status != "cleared" || !r.Attempted || !r.ReadbackEmpty || !r.SequenceMatched || r.CurrentSequence != before || f.seq == before || f.empties != 1 || f.locked {
		t.Fatal("conditional clear confused pre-clear sequence with resulting sequence")
	}
	if r, _ := clearClipboardSession(t.Context(), e.Match); r.Attempted || f.empties != 1 {
		t.Fatal("consumed clear ticket repeated an action")
	}
	clear(e.Text)
}

func TestClipboardSessionPartialTextAndNonTextCanBeClearedWithoutClaimingCompleteCoverage(t *testing.T) {
	for _, kind := range []string{"office_mixed", "partial_text", "non_text"} {
		t.Run(kind, func(t *testing.T) {
			f := fakeSession()
			switch kind {
			case "office_mixed":
				f.formatsValue = []uint32{13, 0xc001}
			case "partial_text":
				f.textOK = false
				f.formatsOK = false
			case "non_text":
				f.formatsValue = []uint32{8, 2}
			}
			e := captureClipboardSession(t.Context(), f, 100, f.id)
			defer clear(e.Text)
			if e.Complete || !e.Stable || !e.ClearEligible {
				t.Fatal("partial representation was promoted to complete or lost same-content eligibility")
			}
			if kind == "office_mixed" && !e.TextComplete {
				t.Fatal("complete Unicode representation lost to unrelated HTML format")
			}
			if kind == "partial_text" && !strings.Contains(string(e.Text), "@example.invalid") {
				t.Fatal("positive-detection prefix was discarded")
			}
			if kind == "non_text" && f.reads != 0 {
				t.Fatal("non-text formats triggered a Unicode read")
			}
			// The caller decides policy: definite-positive partial text or an
			// explicit clear_all may clear the unchanged instance, never proving
			// that other applications could not already paste it.
			r, err := clearClipboardSession(t.Context(), e.Match)
			if kind == "partial_text" {
				if err != nil || r.Status != "unknown" || !r.Attempted {
					t.Fatal("incomplete readback became functional success")
				}
			} else if err != nil || r.Status != "cleared" {
				t.Fatal("same-instance clear failed")
			}
		})
	}
}

func TestClipboardSessionNeverClearsAnotherSequenceOwnerSessionOrOldTicket(t *testing.T) {
	for _, kind := range []string{"sequence", "owner", "wrong_thread", "expired", "session", "session_during_open", "zero_owner", "zero_sequence", "mutated_match", "forged_match", "busy"} {
		t.Run(kind, func(t *testing.T) {
			f := fakeSession()
			if kind == "zero_owner" {
				f.owned = 0
			}
			if kind == "zero_sequence" {
				f.seq = 0
			}
			e := captureClipboardSession(t.Context(), f, 100, f.id)
			defer clear(e.Text)
			switch kind {
			case "sequence":
				f.onOpen = func() { f.seq++; f.textValue = []byte("new unrelated content") }
			case "owner":
				f.onOpen = func() { f.owned++ }
			case "wrong_thread":
				f.threadID++
			case "expired":
				e.Match.ticket.at = time.Now().Add(-6 * time.Second)
			case "session":
				f.id.SessionID++
			case "session_during_open":
				f.onOpen = func() { f.idErr = ErrClipboardSession }
			case "mutated_match":
				e.Match.Sequence++
			case "forged_match":
				e.Match.ticket = nil
			case "busy":
				f.openOK = false
			}
			r, _ := clearClipboardSession(t.Context(), e.Match)
			if r.Attempted || f.empties != 0 {
				t.Fatal("stale/foreign/unknown content was cleared")
			}
			if kind == "owner" && (!r.SequenceMatched || r.Reason != "owner_changed") {
				t.Fatal("owner mismatch lost its independent sequence evidence")
			}
		})
	}
}

func TestClipboardSessionDelayedRenderingRaceIsObservedButNotCleared(t *testing.T) {
	f := fakeSession()
	f.onText = func() { f.seq++; f.onText = nil }
	e := captureClipboardSession(t.Context(), f, 100, f.id)
	defer clear(e.Text)
	if e.Stable || e.ClearEligible || e.Reason != "sequence_changed" {
		t.Fatal("render-induced sequence change was silently treated as same instance")
	}
	if r, _ := clearClipboardSession(t.Context(), e.Match); r.Attempted {
		t.Fatal("unstable render was cleared")
	}
	stable := captureClipboardSession(t.Context(), f, 100, f.id)
	defer clear(stable.Text)
	if !stable.Stable || !stable.ClearEligible {
		t.Fatal("a later real notification cannot capture the rendered stable instance")
	}
}

func TestClipboardSessionCallbackFailureWipesAndInvalidatesTicket(t *testing.T) {
	f := fakeSession()
	e := captureClipboardSession(t.Context(), f, 100, f.id)
	want := errors.New("synthetic metadata failure")
	var retained []byte
	var match ClipboardMatch
	err := deliverClipboardSession(e, func(e ClipboardSessionEvent) error { retained = e.Text; match = e.Match; return want })
	if !errors.Is(err, want) {
		t.Fatal("callback failure suppressed")
	}
	for _, b := range retained {
		if b != 0 {
			t.Fatal("raw text survived callback failure")
		}
	}
	if r, _ := clearClipboardSession(t.Context(), match); r.Attempted {
		t.Fatal("retained post-callback match remained active")
	}
	raw, _ := json.Marshal(e)
	for _, private := range []string{f.id.UserSID, f.id.AuthenticationID, "synthetic@example.invalid"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("raw clipboard/session field entered JSON projection")
		}
	}
	var projected map[string]json.RawMessage
	if json.Unmarshal(raw, &projected) != nil {
		t.Fatal("invalid metadata JSON")
	}
	for _, key := range []string{"text", "formats", "match", "session", "owner", "sequence"} {
		if _, ok := projected[key]; ok {
			t.Fatal("raw field exposed in JSON")
		}
	}
}

func TestClipboardSessionIdentityGuardAndFailingAPIs(t *testing.T) {
	i := fakeSession().id
	if !allowedClipboardSession(i, true, true) {
		t.Fatal("stable interactive logon identity rejected")
	}
	for _, sid := range []string{"S-1-5-18", "S-1-5-19", "S-1-5-20", ""} {
		bad := i
		bad.UserSID = sid
		if allowedClipboardSession(bad, true, true) {
			t.Fatal("service/system identity accepted")
		}
	}
	zero := i
	zero.SessionID = 0
	if allowedClipboardSession(zero, true, true) || allowedClipboardSession(i, false, true) || allowedClipboardSession(i, true, false) {
		t.Fatal("session0 or non-input/non-visible desktop accepted")
	}
	f := fakeSession()
	f.closeOK = false
	l := clipboardSessionListener{ctx: t.Context(), api: f, window: 100, identity: f.id, sequence: f.seq - 1, shouldCapture: func() bool { return true }, emit: func(ClipboardSessionEvent) error { t.Fatal("callback ran despite clipboard-close failure"); return nil }}
	if !errors.Is(l.notify(), ErrClipboardSession) {
		t.Fatal("unreleased clipboard did not stop observation")
	}
	f = fakeSession()
	e := captureClipboardSession(t.Context(), f, 100, f.id)
	defer clear(e.Text)
	f.keepAfterEmpty = true
	r, _ := clearClipboardSession(t.Context(), e.Match)
	if r.Status != "unknown" || r.ReadbackEmpty || !r.Attempted {
		t.Fatal("non-empty readback became successful clear")
	}
	f = fakeSession()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stopped := captureClipboardSession(ctx, f, 100, f.id)
	if f.opens != 0 || f.reads != 0 || stopped.ClearEligible {
		t.Fatal("canceled observer read source content")
	}
}
