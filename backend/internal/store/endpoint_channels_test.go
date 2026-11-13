package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationml"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func endpointHash(v string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(v))) }
func endpointMember(t *testing.T, f assessmentSourceFixture, role string) int64 {
	t.Helper()
	var id int64
	name := fmt.Sprintf("%s-%s-%d", f.ws.Slug, role, time.Now().UnixNano())
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO plume.users(username,password_hash)VALUES($1,'unused')RETURNING id`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO plume.memberships(workspace_id,user_id,role)VALUES($1,$2,$3)`, f.ws.ID, id, role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(t.Context(), `DELETE FROM plume.users WHERE id=$1`, id) })
	return id
}
func endpointNow(t *testing.T, f assessmentSourceFixture) time.Time {
	t.Helper()
	var now time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now.UTC()
}
func endpointTestSource(t *testing.T, f assessmentSourceFixture) (OperationsSource, string, EndpointHeartbeatInput) {
	t.Helper()
	digest := endpointHash(f.ws.Slug + fmt.Sprint(time.Now().UnixNano()))
	source, err := f.repo.CreateOperationsSource(t.Context(), f.ws.ID, f.user.ID, OperationsSourceInput{Name: "Endpoint fixture " + digest[:8], Kind: "security_events", OwnerTeam: "Data"}, digest)
	if err != nil {
		t.Fatal(err)
	}
	in := EndpointHeartbeatInput{DeviceRef: endpointHash(digest + "device"), OS: "windows", AgentVersion: "fixture-v1", ObservedAt: endpointNow(t, f), Capabilities: []EndpointCapability{}}
	for _, channel := range []string{"usb", "print", "clipboard"} {
		in.Capabilities = append(in.Capabilities, EndpointCapability{Channel: channel, Scope: "controlled_operations", Supported: true, Modes: []string{"monitor", "block_sensitive", "block_all"}, Executor: endpointExecutor("windows", channel, "controlled_operations")}, EndpointCapability{Channel: channel, Scope: "observation", Supported: true, Modes: []string{"monitor"}, Executor: endpointExecutor("windows", channel, "observation")})
	}
	if _, err = f.repo.EndpointHeartbeat(t.Context(), digest, in); err != nil {
		t.Fatal(err)
	}
	return source, digest, in
}
func endpointProposal(source OperationsSource, h EndpointHeartbeatInput, channel string) EndpointPolicyInput {
	return EndpointPolicyInput{SourceID: source.ID, DeviceRef: h.DeviceRef, Channel: channel, Scope: "controlled_operations", Mode: "block_sensitive", Categories: []string{"phone"}, Reason: "Synthetic endpoint test", DurationMinutes: 60}
}
func endpointEvent(t *testing.T, f assessmentSourceFixture, p EndpointPolicy, ref, stage, enforcement string) SecurityEventInput {
	t.Helper()
	return SecurityEventInput{Outcome: "observed", EventID: "endpoint-" + ref, ObservedAt: endpointNow(t, f), Category: "data_leak", Rule: "endpoint.fixture", Severity: "high", Count: 1, WindowSeconds: 1, DataContext: &datasafety.Context{Kind: "endpoint", Channel: p.Channel, DeviceRef: p.DeviceRef, SubjectRef: endpointHash("subject"), FileDigest: endpointHash("file"), Operation: "copy", Enforcement: enforcement, Capture: "complete", Categories: []string{"phone"}, PolicyDigest: p.Digest, Endpoint: &datasafety.EndpointEvidence{Version: 1, PolicyID: p.ID, PolicyRevision: p.Revision, Scope: p.Scope, Stage: stage, Executor: p.Executor, OperationRef: endpointHash(ref)}}}
}
func endpointApplied(t *testing.T, f assessmentSourceFixture, digest string, p EndpointPolicy) EndpointReceipt {
	t.Helper()
	r, _, err := f.repo.ReceiveEndpointReceipt(t.Context(), digest, EndpointReceiptInput{ReceiptRef: endpointHash(fmt.Sprint(p.ID) + "receipt"), DeviceRef: p.DeviceRef, PolicyID: p.ID, PolicyRevision: p.Revision, PolicyDigest: p.Digest, Channel: p.Channel, Scope: p.Scope, Executor: p.Executor, Status: "applied", Code: "policy_loaded", ObservedAt: endpointNow(t, f)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestEndpointChannelsLifecycleBindingsAndFrozenEvidence(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	reviewer := endpointMember(t, f, "reviewer")
	reader := endpointMember(t, f, "viewer")
	operator := endpointMember(t, f, "operator")
	source, digest, h := endpointTestSource(t, f)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, operator, endpointProposal(source, h, "usb"))
	must(err)
	if p.Revision != 1 || p.Status != "draft" || p.Current || p.DeploymentState == "applied" {
		t.Fatal("proposal claimed deployment", p)
	}
	if _, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, operator, p.ID, p.Revision, "approve", ""); !errors.Is(err, ErrForbidden) {
		t.Fatal("author approved", err)
	}
	if _, err = f.repo.CreateEndpointPolicy(ctx, f.ws.ID, reader, endpointProposal(source, h, "print")); !errors.Is(err, ErrForbidden) {
		t.Fatal("reader proposed", err)
	}
	p, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, p.ID, p.Revision, "approve", "")
	must(err)
	if !p.Current || p.DeploymentState != "pending" || p.Approver == "" {
		t.Fatal("approval missing or claimed applied", p)
	}
	originalDigest := p.Digest
	event := endpointEvent(t, f, p, "before-receipt", "preflight", "blocked")
	if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, event); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatal("event without applied receipt", err)
	}
	receipt := endpointApplied(t, f, digest, p)
	replay, replayed, err := f.repo.ReceiveEndpointReceipt(ctx, digest, receipt.EndpointReceiptInput)
	must(err)
	if !replayed || replay.ID != receipt.ID {
		t.Fatal("receipt replay duplicated")
	}
	mutated := receipt.EndpointReceiptInput
	mutated.Code = "operation_supported"
	if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, mutated); !errors.Is(err, ErrConflict) {
		t.Fatal("receipt replay mutated", err)
	}
	event = endpointEvent(t, f, p, "blocked", "preflight", "blocked")
	saved, _, err := f.repo.ReceiveSecurityEvent(ctx, digest, event)
	must(err)
	if saved.CheckID == 0 {
		t.Fatal("blocked operation missing risk evidence")
	}
	detail, err := f.repo.EndpointPolicyDetail(ctx, f.ws.ID, reader, p.ID)
	must(err)
	if detail.DeploymentState != "applied" || len(detail.Receipts) != 1 {
		t.Fatal("matching receipt not linked", detail)
	}
	exported, err := f.repo.CreateEndpointEvidenceExport(ctx, f.ws.ID, reader, p.ID)
	must(err)
	if len(exported.Snapshot.Events) != 1 || exported.SnapshotDigest != classificationml.Digest(exported.Snapshot) {
		t.Fatal("missing frozen evidence/digest", exported)
	}
	raw, _ := json.Marshal(exported)
	if strings.Contains(string(raw), digest) || strings.Contains(string(raw), "rawValue") {
		t.Fatal("token/content leaked")
	}
	// A newer draft must not disable the independently approved version.
	next := endpointProposal(source, h, "usb")
	next.Revision = 1
	next.Mode = "monitor"
	second, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, operator, next)
	must(err)
	bundle, err := f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
	must(err)
	if len(bundle.Policies) != 1 || bundle.Policies[0].ID != p.ID {
		t.Fatal("draft disabled approved policy", bundle)
	}
	second, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, second.ID, second.Revision, "approve", "")
	must(err)
	prior, err := f.repo.EndpointPolicyDetail(ctx, f.ws.ID, reader, p.ID)
	must(err)
	if prior.Status != "superseded" || prior.Current || prior.Digest != originalDigest {
		t.Fatal("supersede rewrote content", prior)
	}
	frozen, err := f.repo.EndpointEvidenceExport(ctx, f.ws.ID, reader, exported.ID)
	must(err)
	if frozen.SnapshotDigest != exported.SnapshotDigest || classificationml.Digest(frozen.Snapshot) != exported.SnapshotDigest || frozen.Snapshot.Policy.Status != "approved" {
		t.Fatal("export changed with live state", frozen)
	}
	if _, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, operator, second.ID, second.Revision, "revoke", "stop"); !errors.Is(err, ErrForbidden) {
		t.Fatal("operator revoked", err)
	}
	_, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, f.user.ID, second.ID, second.Revision, "revoke", "stop")
	must(err)
	bundle, err = f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
	must(err)
	if len(bundle.Policies) != 0 {
		t.Fatal("revoked policy fetched")
	}
	// DB also preserves policy/receipt/export payloads against mutation.
	for _, q := range []string{`UPDATE plume.endpoint_channel_policies SET body='{}' WHERE id=$1`, `UPDATE plume.endpoint_channel_receipts SET receipt='{}' WHERE policy_id=$1`, `UPDATE plume.endpoint_channel_exports SET snapshot='{}' WHERE policy_id=$1`} {
		if _, err = f.pool.Exec(ctx, q, p.ID); err == nil {
			t.Fatal("immutable payload changed", q)
		}
	}
}
func TestEndpointChannelsDeviceCapabilitiesAndSourceAuthorization(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	source, digest, h := endpointTestSource(t, f)
	reviewer := endpointMember(t, f, "reviewer")
	changed := h
	changed.DeviceRef = endpointHash("different")
	changed.ObservedAt = endpointNow(t, f)
	if _, err := f.repo.EndpointHeartbeat(ctx, digest, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("device rebound", err)
	}
	changed = h
	changed.OS = "linux"
	changed.Capabilities = []EndpointCapability{{Channel: "usb", Scope: "controlled_operations", Supported: true, Modes: []string{"monitor"}, Executor: "usb_copy_guard"}}
	changed.ObservedAt = endpointNow(t, f)
	if _, err := f.repo.EndpointHeartbeat(ctx, digest, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("OS rebound", err)
	}
	changed = h
	changed.Capabilities = []EndpointCapability{{Channel: "usb", Scope: "native_control", Supported: true, Modes: []string{"block_all"}, Executor: "usb_copy_guard"}}
	if _, err := f.repo.EndpointHeartbeat(ctx, digest, changed); !errors.Is(err, ErrInvalid) {
		t.Fatal("native capability fabricated", err)
	}
	p, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, endpointProposal(source, h, "print"))
	if err != nil {
		t.Fatal(err)
	}
	changed = h
	changed.ObservedAt = endpointNow(t, f)
	changed.AgentVersion = "fixture-v2"
	if _, err = f.repo.EndpointHeartbeat(ctx, digest, changed); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, p.ID, p.Revision, "approve", ""); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatal("changed executor build approved", err)
	}
	foreign := assessmentSourceTestStore(t)
	if _, err = f.repo.EndpointPolicyDetail(ctx, foreign.ws.ID, foreign.user.ID, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign policy visible", err)
	}
	if _, err = f.repo.CreateEndpointPolicy(ctx, foreign.ws.ID, foreign.user.ID, endpointProposal(source, h, "usb")); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign source usable", err)
	}
	// Current ownership, not a historical source creator role, authorizes evidence.
	if _, err = f.pool.Exec(ctx, `UPDATE plume.memberships SET role='operator' WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	changed.ObservedAt = endpointNow(t, f)
	if _, err = f.repo.EndpointHeartbeat(ctx, digest, changed); !errors.Is(err, ErrAutomationUnauthorized) {
		t.Fatal("demoted source remained trusted", err)
	}
	devices, err := f.repo.ListEndpointDevices(ctx, f.ws.ID, reviewer)
	if err != nil || len(devices) != 1 || devices[0].Current {
		t.Fatal("source current stale", devices, err)
	}
}
func TestEndpointChannelsHistoricalCompletionAndObservation(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	source, digest, h := endpointTestSource(t, f)
	reviewer := endpointMember(t, f, "reviewer")
	p, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, endpointProposal(source, h, "clipboard"))
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, p.ID, p.Revision, "approve", "")
	if err != nil {
		t.Fatal(err)
	}
	endpointApplied(t, f, digest, p)
	begin := endpointEvent(t, f, p, "operation-a", "observed", "observed")
	begin.Outcome = "activity"
	begin.DataContext.Categories = []string{"email"}
	if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, begin); err != nil {
		t.Fatal(err)
	}
	// Freeze an in-window event locally, then transmit only after revocation.
	late := endpointEvent(t, f, p, "late-preflight", "preflight", "blocked")
	_, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, f.user.ID, p.ID, p.Revision, "revoke", "Synthetic stop")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, late); err != nil {
		t.Fatal("historical event discarded", err)
	}
	completed := begin
	completed.EventID = "operation-completed"
	completed.ObservedAt = endpointNow(t, f)
	copyContext := *begin.DataContext
	copyEndpoint := *copyContext.Endpoint
	copyContext.Endpoint = &copyEndpoint
	completed.DataContext = &copyContext
	copyEndpoint.Stage = "completed"
	copyContext.Enforcement = "allowed"
	result, _, err := f.repo.ReceiveSecurityEvent(ctx, digest, completed)
	if err != nil || result.CheckID != 0 {
		t.Fatal("completion of accepted start lost", result, err)
	}
	bad := completed
	bad.EventID = "unrelated-completion"
	badContext := *completed.DataContext
	badEndpoint := *badContext.Endpoint
	badContext.Endpoint = &badEndpoint
	bad.DataContext = &badContext
	badEndpoint.OperationRef = endpointHash("different")
	if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("unbound completion after revoke", err)
	}
	after := endpointEvent(t, f, p, "after-revoke", "preflight", "blocked")
	if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, after); !errors.Is(err, ErrConflict) {
		t.Fatal("new revoked preflight accepted", err)
	}
	observer := endpointEvent(t, f, p, "observer", "observed", "observed")
	observer.Outcome = "activity"
	observer.DataContext.PolicyDigest = ""
	observer.DataContext.Endpoint.PolicyID = 0
	observer.DataContext.Endpoint.PolicyRevision = 0
	observer.DataContext.Endpoint.Scope = "observation"
	observer.DataContext.Endpoint.Executor = "windows_clipboard_listener"
	if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, observer); err != nil {
		t.Fatal("native observation needs blocking policy", err)
	}
	observer.EventID = "fake-device"
	observer.DataContext.DeviceRef = endpointHash("other device")
	if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, observer); !errors.Is(err, ErrConflict) {
		t.Fatal("observer changed device", err)
	}
}

func TestEndpointChannelsNegativeProofAndLatestReceipt(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	source, digest, h := endpointTestSource(t, f)
	reviewer := endpointMember(t, f, "reviewer")
	p, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, endpointProposal(source, h, "usb"))
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, p.ID, p.Revision, "approve", "")
	if err != nil {
		t.Fatal(err)
	}
	receipt := endpointApplied(t, f, digest, p)
	for _, capture := range []string{"complete", "partial", "unknown"} {
		forged := endpointEvent(t, f, p, "allowed-"+capture, "completed", "allowed")
		forged.Outcome = "activity"
		forged.DataContext.Capture = capture
		if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, forged); !errors.Is(err, ErrInvalid) {
			t.Fatal("sensitive or incomplete operation claimed allowed", capture, err)
		}
	}
	failed := receipt.EndpointReceiptInput
	failed.ReceiptRef = endpointHash("failed" + fmt.Sprint(p.ID))
	failed.Status = "failed"
	failed.Code = "executor_unavailable"
	failed.ObservedAt = endpointNow(t, f)
	if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, failed); err != nil {
		t.Fatal(err)
	}
	event := endpointEvent(t, f, p, "after-failure", "preflight", "blocked")
	if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, event); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatal("older applied receipt overrode later failure", err)
	}
	detail, err := f.repo.EndpointPolicyDetail(ctx, f.ws.ID, f.user.ID, p.ID)
	if err != nil || detail.DeploymentState != "failed" {
		t.Fatal("failure not current deployment truth", detail, err)
	}
	// Late arrival of an older successful receipt must not erase the failure.
	delayed := receipt.EndpointReceiptInput
	delayed.ReceiptRef = endpointHash("late-applied" + fmt.Sprint(p.ID))
	if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, delayed); err != nil {
		t.Fatal(err)
	}
	detail, err = f.repo.EndpointPolicyDetail(ctx, f.ws.ID, f.user.ID, p.ID)
	if err != nil || detail.DeploymentState != "failed" {
		t.Fatal("late receipt changed newer observed state", detail, err)
	}
	if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, event); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatal("late applied permitted action", err)
	}
	// Native controls require a frozen supported target and block_all mode.
	native := endpointProposal(source, h, "print")
	native.Scope = "native_control"
	native.Mode = "block_all"
	native.Categories = []string{}
	native.TargetScope = "configured_print_queues"
	native.TargetRefs = []string{endpointHash("unsupported-queue")}
	native.ConfigDigest = endpointHash("unsupported-config")
	if _, err = f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, native); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatal("unadvertised native target accepted", err)
	}
	// Unsupported category has no negative detection coverage in the text guard.
	other := endpointProposal(source, h, "clipboard")
	other.Categories = []string{"business"}
	unsupported, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, other)
	if err != nil {
		t.Fatal(err)
	}
	unsupported, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, unsupported.ID, unsupported.Revision, "approve", "")
	if err != nil {
		t.Fatal(err)
	}
	endpointApplied(t, f, digest, unsupported)
	noHit := endpointEvent(t, f, unsupported, "unsupported-negative", "completed", "allowed")
	noHit.Outcome = "activity"
	noHit.DataContext.Categories = []string{}
	if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, noHit); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsupported category auto-allowed", err)
	}
}

func TestEndpointChannelsAllChannelsTTLAndHeartbeatReplay(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	source, digest, h := endpointTestSource(t, f)
	reviewer := endpointMember(t, f, "reviewer")
	for _, channel := range []string{"usb", "print", "clipboard"} {
		t.Run(channel, func(t *testing.T) {
			in := endpointProposal(source, h, channel)
			in.Mode = "monitor"
			in.DurationMinutes = 1
			p, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, in)
			if err != nil {
				t.Fatal(err)
			}
			p, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, p.ID, p.Revision, "approve", "")
			if err != nil {
				t.Fatal(err)
			}
			endpointApplied(t, f, digest, p)
			begin := endpointEvent(t, f, p, "monitor-"+channel, "observed", "observed")
			begin.Outcome = "activity"
			if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, begin); err != nil {
				t.Fatal("monitor start audit", err)
			}
			event := endpointEvent(t, f, p, "monitor-"+channel, "completed", "allowed")
			event.EventID += "-completed"
			event.Outcome = "activity"
			if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, event); err != nil {
				t.Fatal("monitor actual operation", err)
			}
			contradiction := endpointEvent(t, f, p, "monitor-block-"+channel, "preflight", "blocked")
			if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, contradiction); !errors.Is(err, ErrInvalid) {
				t.Fatal("monitor claimed block", err)
			}
			expired := endpointEvent(t, f, p, "expired-"+channel, "observed", "observed")
			expired.ObservedAt = p.ExpiresAt.Add(time.Microsecond)
			expired.Outcome = "activity"
			if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, expired); !errors.Is(err, ErrConflict) {
				t.Fatal("new operation outside TTL", err)
			}
		})
	}
	if _, err := f.pool.Exec(ctx, `UPDATE plume.endpoint_devices SET last_seen_at=clock_timestamp()-interval '6 minutes' WHERE source_id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	device, err := f.repo.EndpointHeartbeat(ctx, digest, h)
	if err != nil || device.Online || device.Current {
		t.Fatal("heartbeat replay extended liveness", device, err)
	}
	if _, err = f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatal("offline device fetched policy", err)
	}
	h.ObservedAt = endpointNow(t, f)
	device, err = f.repo.EndpointHeartbeat(ctx, digest, h)
	if err != nil || !device.Current {
		t.Fatal("new heartbeat not current", device, err)
	}
}

func TestEndpointChannelsOwnerCanRevokeAfterSourceInvalidates(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	source, digest, h := endpointTestSource(t, f)
	reviewer := endpointMember(t, f, "reviewer")
	p, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, endpointProposal(source, h, "usb"))
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, p.ID, p.Revision, "approve", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.repo.RevokeOperationsSource(ctx, f.ws.ID, f.user.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	p, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, f.user.ID, p.ID, p.Revision, "revoke", "Record explicit withdrawal of revoked source policy")
	if err != nil || p.Status != "revoked" || p.Current {
		t.Fatal("owner unable to withdraw invalid source policy", p, err)
	}
	if _, err = f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef); !errors.Is(err, ErrAutomationUnauthorized) {
		t.Fatal("source revived by policy decision", err)
	}
}

func TestEndpointChannelsCompletionKeepsStartProofAfterDeploymentFailure(t *testing.T) {
	for _, status := range []string{"failed", "unsupported"} {
		t.Run(status, func(t *testing.T) {
			f := assessmentSourceTestStore(t)
			ctx := t.Context()
			source, digest, h := endpointTestSource(t, f)
			reviewer := endpointMember(t, f, "reviewer")
			p, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, endpointProposal(source, h, "print"))
			if err != nil {
				t.Fatal(err)
			}
			p, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, p.ID, p.Revision, "approve", "")
			if err != nil {
				t.Fatal(err)
			}
			receipt := endpointApplied(t, f, digest, p)
			begin := endpointEvent(t, f, p, "accepted-operation", "observed", "observed")
			begin.Outcome = "activity"
			begin.DataContext.Categories = []string{"email"}
			if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, begin); err != nil {
				t.Fatal(err)
			}
			failure := receipt.EndpointReceiptInput
			failure.ReceiptRef = endpointHash(status + fmt.Sprint(p.ID))
			failure.Status = status
			failure.Code = "executor_unavailable"
			failure.ObservedAt = endpointNow(t, f)
			if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, failure); err != nil {
				t.Fatal(err)
			}
			for _, stage := range []string{"completed", "post_action"} {
				completed := endpointEvent(t, f, p, "accepted-operation", stage, "allowed")
				completed.EventID = "accepted-operation-" + stage
				completed.DataContext.Categories = []string{"email"}
				if stage == "completed" {
					completed.Outcome = "activity"
				} else {
					completed.DataContext.Enforcement = "failed"
				}
				saved, replayed, err := f.repo.ReceiveSecurityEvent(ctx, digest, completed)
				if err != nil || replayed || saved.ID == 0 {
					t.Fatal("lost actual completion after loading failure", stage, err)
				}
				again, replayed, err := f.repo.ReceiveSecurityEvent(ctx, digest, completed)
				if err != nil || !replayed || again.ID != saved.ID {
					t.Fatal("completion replay duplicated", err)
				}
			}
			detail, err := f.repo.EndpointPolicyDetail(ctx, f.ws.ID, f.user.ID, p.ID)
			if err != nil || detail.DeploymentState != status {
				t.Fatal("historical completion restored deployment", detail.DeploymentState, err)
			}
			// A new operation still sees the latest failed deployment receipt.
			next := endpointEvent(t, f, p, "new-after-failure", "observed", "observed")
			next.Outcome = "activity"
			next.DataContext.Categories = []string{"email"}
			if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, next); !errors.Is(err, ErrEvidenceRequired) {
				t.Fatal("new operation reused old loading proof", err)
			}
			// Completing a different operation, subject, file or operation kind cannot
			// borrow the accepted start, even while the policy is still approved.
			for _, change := range []string{"operationRef", "subject", "file", "operation", "late"} {
				bad := endpointEvent(t, f, p, "accepted-operation", "completed", "allowed")
				bad.Outcome = "activity"
				bad.EventID = "unbound-" + change
				bad.DataContext.Categories = []string{"email"}
				switch change {
				case "operationRef":
					bad.DataContext.Endpoint.OperationRef = endpointHash("unrelated")
				case "subject":
					bad.DataContext.SubjectRef = endpointHash("other subject")
				case "file":
					bad.DataContext.FileDigest = endpointHash("other file")
				case "operation":
					bad.DataContext.Operation = "write"
				case "late":
					bad.ObservedAt = begin.ObservedAt.Add(10*time.Minute + time.Microsecond)
				}
				if change == "late" {
					// ReceiveSecurityEvent only accepts observations up to five minutes ahead;
					// move the stored start backwards inside this disposable fixture instead.
					bad.ObservedAt = endpointNow(t, f)
					if _, err = f.pool.Exec(ctx, `UPDATE plume.operations_security_events SET observed_at=$2 WHERE source_id=$1 AND event_id=$3`, source.ID, bad.ObservedAt.Add(-10*time.Minute-time.Microsecond), begin.EventID); err != nil {
						t.Fatal(err)
					}
				}
				if _, _, err = f.repo.ReceiveSecurityEvent(ctx, digest, bad); !errors.Is(err, ErrConflict) {
					t.Fatal("unbound completion borrowed another start", change, err)
				}
			}
		})
	}
}
