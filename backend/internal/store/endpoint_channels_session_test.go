package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationml"
)

func endpointSessionFixture(t *testing.T) (assessmentSourceFixture, OperationsSource, string, EndpointHeartbeatInput, int64) {
	t.Helper()
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	reviewer := endpointMember(t, f, "reviewer")
	digest := endpointHash(f.ws.Slug)
	source, err := f.repo.CreateOperationsSource(ctx, f.ws.ID, f.user.ID, OperationsSourceInput{Name: "Synthetic interactive user", Kind: "security_events", OwnerTeam: "Fixture"}, digest)
	if err != nil {
		t.Fatal(err)
	}
	h := EndpointHeartbeatInput{DeviceRef: endpointHash("device" + f.ws.Slug), OS: "windows", AgentVersion: "session-fixture-v1", ObservedAt: endpointNow(t, f), Capabilities: []EndpointCapability{{Channel: "clipboard", Scope: "session_response", Supported: true, Modes: []string{"monitor", "clear_sensitive", "clear_all"}, Executor: endpointSessionExecutor, Reason: "fixture session", TargetScope: endpointSessionTarget, TargetRefs: []string{endpointHash("login" + f.ws.Slug)}, ConfigDigest: endpointHash("config" + f.ws.Slug), Privilege: "interactive_user"}, {Channel: "clipboard", Scope: "controlled_operations", Supported: true, Modes: []string{"monitor", "block_sensitive", "block_all"}, Executor: "windows_clipboard_write", Reason: "fixture classic application integration"}}}
	if _, err = f.repo.EndpointHeartbeat(ctx, digest, h); err != nil {
		t.Fatal(err)
	}
	return f, source, digest, h, reviewer
}
func endpointSessionProposal(source OperationsSource, h EndpointHeartbeatInput, mode string) EndpointPolicyInput {
	in := endpointProposal(source, h, "clipboard")
	in.Scope = "session_response"
	in.Mode = mode
	in.Categories = []string{}
	if mode == "clear_sensitive" {
		in.Categories = []string{"phone"}
	}
	for _, c := range h.Capabilities {
		if c.Scope == "session_response" {
			in.TargetScope = c.TargetScope
			in.TargetRefs = append([]string{}, c.TargetRefs...)
			in.ConfigDigest = c.ConfigDigest
		}
	}
	return in
}
func endpointSessionApproved(t *testing.T, f assessmentSourceFixture, source OperationsSource, h EndpointHeartbeatInput, reviewer int64, mode string) EndpointPolicy {
	t.Helper()
	p, err := f.repo.CreateEndpointPolicy(t.Context(), f.ws.ID, f.user.ID, endpointSessionProposal(source, h, mode))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.DecideEndpointPolicy(t.Context(), f.ws.ID, f.user.ID, p.ID, p.Revision, "approve", ""); !errors.Is(err, ErrForbidden) {
		t.Fatal("session author self-approved", err)
	}
	p, err = f.repo.DecideEndpointPolicy(t.Context(), f.ws.ID, reviewer, p.ID, p.Revision, "approve", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.NativeLease != nil || !p.Current {
		t.Fatal("session copied native lease or lacks current approval", p)
	}
	return p
}
func endpointSessionLoad(t *testing.T, f assessmentSourceFixture, digest string, p EndpointPolicy) EndpointReceiptInput {
	t.Helper()
	in := EndpointReceiptInput{ReceiptRef: endpointHash(fmt.Sprint(p.ID) + "session-loaded"), DeviceRef: p.DeviceRef, PolicyID: p.ID, PolicyRevision: p.Revision, PolicyDigest: p.Digest, Channel: "clipboard", Scope: p.Scope, Executor: p.Executor, TargetScope: p.TargetScope, TargetRefs: p.TargetRefs, ConfigDigest: p.ConfigDigest, Status: "loaded", Code: "session_guard_loaded", ObservedAt: endpointNow(t, f)}
	if _, _, err := f.repo.ReceiveEndpointReceipt(t.Context(), digest, in); err != nil {
		t.Fatal(err)
	}
	return in
}
func endpointSessionInput(t *testing.T, f assessmentSourceFixture, p EndpointPolicy, ref string) EndpointSessionEventInput {
	t.Helper()
	at := endpointNow(t, f)
	return EndpointSessionEventInput{EventRef: endpointHash(ref), OperationRef: endpointHash("operation:" + ref), DeviceRef: p.DeviceRef, SessionRef: p.TargetRefs[0], PolicyID: p.ID, PolicyRevision: p.Revision, PolicyDigest: p.Digest, Scope: p.Scope, Executor: p.Executor, ConfigDigest: p.ConfigDigest, Status: "unknown", Code: "session_clear_intent", ObservedAt: at, ResponseAt: at, Capture: "partial", Categories: []string{"phone"}, SequenceRef: endpointHash("sequence:" + ref), ContentRef: endpointHash("content:" + ref)}
}
func endpointSessionResult(t *testing.T, f assessmentSourceFixture, intent EndpointSessionEventInput) EndpointSessionEventInput {
	t.Helper()
	out := intent
	out.EventRef = endpointHash(intent.EventRef + "result")
	out.Status = "cleared_after_change"
	out.Code = "session_cleared_after_change"
	out.ResponseAt = endpointNow(t, f)
	out.CurrentSequenceRef = out.SequenceRef
	out.SequenceMatched = true
	out.ClearAttempted = true
	out.ClipboardEmpty = true
	return out
}
func TestEndpointSessionPartialPositiveIntentAndFrozenEvidence(t *testing.T) {
	f, source, digest, h, reviewer := endpointSessionFixture(t)
	ctx := t.Context()
	p := endpointSessionApproved(t, f, source, h, reviewer, "clear_sensitive")
	loaded := endpointSessionLoad(t, f, digest, p)
	intent := endpointSessionInput(t, f, p, "positive")
	result := endpointSessionResult(t, f, intent)
	if _, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, result); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatal("clear without durable intent", err)
	}
	first, replayed, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, intent)
	if err != nil || replayed || first.CurrentResponse {
		t.Fatal("intent became successful response", first, err)
	}
	detail, err := f.repo.EndpointPolicyDetail(ctx, f.ws.ID, reviewer, p.ID)
	if err != nil || detail.DeploymentState != "listening" || detail.SessionEventsTotal != 1 || !strings.Contains(strings.Join(detail.SessionCoverageMissing, " "), "仅有清除意图") {
		t.Fatal("missing intent uncertainty", detail, err)
	}
	result.ResponseAt = endpointNow(t, f)
	accepted, replayed, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, result)
	if err != nil || replayed || !accepted.CurrentResponse || accepted.Historical || accepted.Capture != "partial" {
		t.Fatal("definite positive partial text could not respond", accepted, err)
	}
	again, replayed, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, result)
	if err != nil || !replayed || again.ID != accepted.ID {
		t.Fatal("event replay duplicated", err)
	}
	changed := result
	changed.ContentRef = endpointHash("othercontent")
	if _, _, err = f.repo.ReceiveEndpointSessionEvent(ctx, digest, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("mutated replay accepted", err)
	}
	exported, err := f.repo.CreateEndpointEvidenceExport(ctx, f.ws.ID, f.user.ID, p.ID)
	if err != nil || exported.Snapshot.Version != 3 || exported.Snapshot.SessionEventsTotal != 2 || exported.SnapshotDigest != classificationml.Digest(exported.Snapshot) {
		t.Fatal("session evidence snapshot incomplete", err)
	}
	var checks, events int
	if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM plume.checks WHERE workspace_id=$1),(SELECT count(*) FROM plume.operations_security_events WHERE workspace_id=$1)`, f.ws.ID).Scan(&checks, &events); err != nil || checks != 0 || events != 0 {
		t.Fatal("session event created or cleared generic risk", checks, events, err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE plume.endpoint_session_events SET event='{}' WHERE id=$1`, accepted.ID); err == nil {
		t.Fatal("session evidence mutable")
	}
	_, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, f.user.ID, p.ID, p.Revision, "revoke", "Stop this response scope")
	if err != nil {
		t.Fatal(err)
	}
	stopped := loaded
	stopped.ReceiptRef = endpointHash("stopped")
	stopped.Status = "stopped"
	stopped.Code = "session_guard_stopped"
	stopped.ObservedAt = endpointNow(t, f)
	if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, stopped); err != nil {
		t.Fatal(err)
	}
	detail, err = f.repo.EndpointPolicyDetail(ctx, f.ws.ID, reviewer, p.ID)
	if err != nil || detail.Current || detail.DeploymentState != "stopped" {
		t.Fatal("stop was treated as body restoration or still active", detail, err)
	}
	frozen, err := f.repo.EndpointEvidenceExport(ctx, f.ws.ID, reviewer, exported.ID)
	if err != nil || classificationml.Digest(frozen.Snapshot) != exported.SnapshotDigest || frozen.Snapshot.Policy.Status != "approved" {
		t.Fatal("stop mutated frozen evidence", err)
	}
}
func TestEndpointSessionNoHitModesAndIntentBindings(t *testing.T) {
	f, source, digest, h, reviewer := endpointSessionFixture(t)
	ctx := t.Context()
	p := endpointSessionApproved(t, f, source, h, reviewer, "clear_sensitive")
	endpointSessionLoad(t, f, digest, p)
	missing := endpointSessionInput(t, f, p, "partial-no-hit")
	missing.Categories = []string{}
	missing.Status = "not_required"
	missing.Code = "session_no_selected_match"
	if _, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, missing); !errors.Is(err, ErrInvalid) {
		t.Fatal("partial no-hit claimed safe", err)
	}
	missing.Status = "unknown"
	missing.Code = "session_capture_unknown"
	if _, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, missing); err != nil {
		t.Fatal(err)
	}
	complete := missing
	complete.EventRef = endpointHash("complete-no-hit")
	complete.OperationRef = endpointHash("complete-op")
	complete.Capture = "complete"
	complete.Status = "not_required"
	complete.Code = "session_no_selected_match"
	if event, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, complete); err != nil || event.ClearAttempted {
		t.Fatal("complete no-hit cannot be recorded", err)
	}
	intent := endpointSessionInput(t, f, p, "bound")
	if _, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, intent); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"operation", "session", "content", "sequence", "category", "capture"} {
		result := endpointSessionResult(t, f, intent)
		result.EventRef = endpointHash("bad:" + field)
		switch field {
		case "operation":
			result.OperationRef = endpointHash("otherop")
		case "session":
			result.SessionRef = endpointHash("otherlogin")
		case "content":
			result.ContentRef = endpointHash("othercontent")
		case "sequence":
			result.SequenceRef = endpointHash("otherseq")
			result.CurrentSequenceRef = result.SequenceRef
		case "category":
			result.Categories = []string{"phone", "email"}
		case "capture":
			result.Capture = "complete"
		}
		if _, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, result); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrEvidenceRequired) {
			t.Fatal("result borrowed unrelated intent", field, err)
		}
	}
	stale := intent
	stale.EventRef = endpointHash("stale")
	stale.Code = "session_sequence_changed"
	stale.Status = "stale"
	stale.CurrentSequenceRef = endpointHash("new-seq")
	stale.SequenceMatched = false
	if _, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, stale); err != nil {
		t.Fatal("sequence race not retained", err)
	}
	foreign := assessmentSourceTestStore(t)
	if _, err := f.repo.EndpointPolicyDetail(ctx, foreign.ws.ID, foreign.user.ID, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign session evidence visible", err)
	}
}
func TestEndpointSessionPermanentInvalidationAndClassicCoexistence(t *testing.T) {
	for _, reason := range []string{"session", "config", "disabled", "source"} {
		t.Run(reason, func(t *testing.T) {
			f, source, digest, h, reviewer := endpointSessionFixture(t)
			ctx := t.Context()
			p := endpointSessionApproved(t, f, source, h, reviewer, "clear_all")
			endpointSessionLoad(t, f, digest, p)
			classic, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, endpointProposal(source, h, "clipboard"))
			if err != nil {
				t.Fatal(err)
			}
			classic, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, classic.ID, classic.Revision, "approve", "")
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
			if err != nil || len(bundle.Policies) != 2 {
				t.Fatal("scopes displaced each other", bundle, err)
			}
			original := h
			original.Capabilities = append([]EndpointCapability{}, h.Capabilities...)
			if reason == "source" {
				if err = f.repo.RevokeOperationsSource(ctx, f.ws.ID, f.user.ID, source.ID); err != nil {
					t.Fatal(err)
				}
				// Simulate an administrative source re-enable in this disposable DB;
				// the prior session approval must remain ended by the revocation trigger.
				if _, err = f.pool.Exec(ctx, `UPDATE plume.operations_sources SET revoked_at=NULL WHERE id=$1`, source.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				h.Capabilities = append([]EndpointCapability{}, h.Capabilities...)
				for i := range h.Capabilities {
					if h.Capabilities[i].Scope == "session_response" {
						switch reason {
						case "session":
							h.Capabilities[i].TargetRefs = []string{endpointHash("new-logon")}
						case "config":
							h.Capabilities[i].ConfigDigest = endpointHash("new-config")
						case "disabled":
							h.Capabilities[i].Supported = false
							h.Capabilities[i].Modes = []string{}
							h.Capabilities[i].Reason = "disabled"
						}
					}
				}
				h.ObservedAt = endpointNow(t, f)
				if _, err = f.repo.EndpointHeartbeat(ctx, digest, h); err != nil {
					t.Fatal(err)
				}
			}
			detail, err := f.repo.EndpointPolicyDetail(ctx, f.ws.ID, reviewer, p.ID)
			if err != nil || detail.Current || detail.EndedAt == nil || detail.DeploymentState != "awaiting_stop" {
				t.Fatal("scope did not permanently end", detail, err)
			}
			original.ObservedAt = endpointNow(t, f)
			if _, err = f.repo.EndpointHeartbeat(ctx, digest, original); err != nil {
				t.Fatal(err)
			}
			detail, err = f.repo.EndpointPolicyDetail(ctx, f.ws.ID, reviewer, p.ID)
			if err != nil || detail.Current || detail.EndedAt == nil {
				t.Fatal("old scope resurrected", detail, err)
			}
			cc, err := f.repo.EndpointPolicyDetail(ctx, f.ws.ID, reviewer, classic.ID)
			if err != nil || !cc.Current {
				t.Fatal("session invalidation affected classic scope", cc, err)
			}
			nextInput := endpointSessionProposal(source, original, "monitor")
			nextInput.Revision = p.Revision
			next, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, nextInput)
			if err != nil {
				t.Fatal(err)
			}
			next, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, next.ID, next.Revision, "approve", "")
			if err != nil || !next.Current {
				t.Fatal("independent new approval cannot replace ended scope", next, err)
			}
		})
	}
}
func TestEndpointSessionClearAllUnknownAndMonitorCannotClear(t *testing.T) {
	for _, mode := range []string{"clear_all", "monitor"} {
		t.Run(mode, func(t *testing.T) {
			f, source, digest, h, reviewer := endpointSessionFixture(t)
			ctx := t.Context()
			p := endpointSessionApproved(t, f, source, h, reviewer, mode)
			endpointSessionLoad(t, f, digest, p)
			intent := endpointSessionInput(t, f, p, "mode")
			intent.Capture = "unknown"
			intent.ContentRef = ""
			intent.Categories = []string{}
			if mode == "monitor" {
				if _, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, intent); !errors.Is(err, ErrInvalid) {
					t.Fatal("monitor intended clear", err)
				}
				intent.Status = "observed"
				intent.Code = "session_observed"
				if _, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, intent); err != nil {
					t.Fatal(err)
				}
				return
			}
			if _, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, intent); err != nil {
				t.Fatal(err)
			}
			result := endpointSessionResult(t, f, intent)
			if _, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, result); err != nil {
				t.Fatal("clear_all should not invent content recognition", err)
			}
		})
	}
}

func TestEndpointSessionOfflineSlowCompletionOnlyArchivesHistory(t *testing.T) {
	f, source, digest, h, reviewer := endpointSessionFixture(t)
	ctx := t.Context()
	// Persist an immutable policy as it would exist after an offline restart.
	// Real source timestamps are retained; the test does not move system clocks.
	in := endpointSessionProposal(source, h, "clear_sensitive")
	in.DurationMinutes = 1
	in.Revision = 1
	p := EndpointPolicy{EndpointPolicyInput: in, OS: h.OS, AgentVersion: h.AgentVersion, Creator: f.user.Username, CreatorID: f.user.ID, SourceName: source.Name, Executor: endpointSessionExecutor}
	p.Capability, p.CapabilityDigest = endpointCapability(EndpointDevice{EndpointHeartbeatInput: h}, "clipboard", "session_response")
	p.CreatedAt = endpointNow(t, f).Add(-4 * time.Hour)
	p.ExpiresAt = p.CreatedAt.Add(time.Minute)
	approved := p.CreatedAt.Add(time.Second)
	if err := f.pool.QueryRow(ctx, `SELECT nextval('plume.endpoint_channel_policies_id_seq')`).Scan(&p.ID); err != nil {
		t.Fatal(err)
	}
	p.Digest = assessmentDigest(p)
	_, err := f.pool.Exec(ctx, `INSERT INTO plume.endpoint_channel_policies(id,workspace_id,source_id,channel,scope,revision,actor_id,body,digest,status,approved_by,approved_at,expires_at,created_at) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,'clipboard','session_response',1,$4,$5,$6,'approved',$7,$8,$9,$10)`, p.ID, f.ws.ID, source.ID, f.user.ID, p, p.Digest, reviewer, approved, p.ExpiresAt, p.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	p.ApprovedAt = &approved
	loaded := EndpointReceiptInput{ReceiptRef: endpointHash("offline-load"), DeviceRef: p.DeviceRef, PolicyID: p.ID, PolicyRevision: 1, PolicyDigest: p.Digest, Channel: "clipboard", Scope: p.Scope, Executor: p.Executor, TargetScope: p.TargetScope, TargetRefs: p.TargetRefs, ConfigDigest: p.ConfigDigest, Status: "loaded", Code: "session_guard_loaded", ObservedAt: approved.Add(time.Second)}
	if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, loaded); err != nil {
		t.Fatal(err)
	}
	intent := endpointSessionInput(t, f, p, "offline")
	intent.ObservedAt = loaded.ObservedAt.Add(time.Second)
	intent.ResponseAt = intent.ObservedAt
	if _, _, err = f.repo.ReceiveEndpointSessionEvent(ctx, digest, intent); err != nil {
		t.Fatal("offline intent was discarded", err)
	}
	result := endpointSessionResult(t, f, intent)
	result.ResponseAt = intent.ResponseAt.Add(2 * time.Hour)
	accepted, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, result)
	if err != nil || !accepted.Historical || accepted.CurrentResponse || !accepted.ResponseAt.Equal(result.ResponseAt) {
		t.Fatal("slow real completion was discarded or promoted to current", accepted, err)
	}
	tooLate := result
	tooLate.EventRef = endpointHash("more-than-24h")
	tooLate.ResponseAt = intent.ObservedAt.Add(25 * time.Hour)
	if _, _, err = f.repo.ReceiveEndpointSessionEvent(ctx, digest, tooLate); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded operation accepted", err)
	}
	// A historical successful clear never authorizes a new intent after expiry.
	newer := endpointSessionInput(t, f, p, "new-after-expiry")
	if _, _, err = f.repo.ReceiveEndpointSessionEvent(ctx, digest, newer); !errors.Is(err, ErrConflict) {
		t.Fatal("expired policy allowed new clear", err)
	}
}

func TestEndpointSessionRevokedCachedApprovalOnlyArchivesActualResponse(t *testing.T) {
	f, source, digest, h, reviewer := endpointSessionFixture(t)
	ctx := t.Context()
	p := endpointSessionApproved(t, f, source, h, reviewer, "clear_sensitive")
	cached, err := f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
	if err != nil || len(cached.Policies) != 1 {
		t.Fatal("no original approved cache", err)
	}
	_, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, f.user.ID, p.ID, p.Revision, "revoke", "Server revocation while source offline")
	if err != nil {
		t.Fatal(err)
	}
	// The authorized source has not received revocation yet. Its asynchronous
	// loaded/intent/result reports retain real timestamps after server revocation.
	loaded := endpointSessionLoad(t, f, digest, p)
	intent := endpointSessionInput(t, f, p, "offline-cached-after-revoke")
	event, _, err := f.repo.ReceiveEndpointSessionEvent(ctx, digest, intent)
	if err != nil || !event.Historical || event.CurrentResponse {
		t.Fatal("cached source intent lost or reactivated", event, err)
	}
	result := endpointSessionResult(t, f, intent)
	event, _, err = f.repo.ReceiveEndpointSessionEvent(ctx, digest, result)
	if err != nil || !event.Historical || event.CurrentResponse {
		t.Fatal("cached actual clear lost or reactivated", event, err)
	}
	current, err := f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
	if err != nil || len(current.Policies) != 0 {
		t.Fatal("historical evidence reissued authorization", current, err)
	}
	stopped := loaded
	stopped.ReceiptRef = endpointHash("cache-stopped")
	stopped.Status = "stopped"
	stopped.Code = "session_guard_stopped"
	stopped.ObservedAt = endpointNow(t, f)
	if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, stopped); err != nil {
		t.Fatal(err)
	}
	afterStop := endpointSessionInput(t, f, p, "contradictory-new-after-stop")
	if _, _, err = f.repo.ReceiveEndpointSessionEvent(ctx, digest, afterStop); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatal("new clear ignored acknowledged stop", err)
	}
}
