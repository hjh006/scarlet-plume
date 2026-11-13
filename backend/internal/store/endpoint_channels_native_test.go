package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationml"
)

func endpointNativeFixture(t *testing.T) (assessmentSourceFixture, OperationsSource, string, EndpointHeartbeatInput, int64) {
	t.Helper()
	f := assessmentSourceTestStore(t)
	source, digest, h := endpointTestSource(t, f)
	reviewer := endpointMember(t, f, "reviewer")
	for _, channel := range []string{"usb", "print", "clipboard"} {
		h.Capabilities = append(h.Capabilities, EndpointCapability{Channel: channel, Scope: "native_control", Supported: true, Modes: []string{"block_all"}, Executor: endpointNativeExecutor(channel), TargetScope: endpointNativeTarget(channel), TargetRefs: []string{endpointHash("target-" + channel)}, ConfigDigest: endpointHash("config-" + channel), Privilege: "administrator"})
	}
	h.ObservedAt = endpointNow(t, f)
	if _, err := f.repo.EndpointHeartbeat(t.Context(), digest, h); err != nil {
		t.Fatal(err)
	}
	return f, source, digest, h, reviewer
}
func endpointNativeProposal(source OperationsSource, h EndpointHeartbeatInput, channel string) EndpointPolicyInput {
	in := endpointProposal(source, h, channel)
	in.Scope = "native_control"
	in.Mode = "block_all"
	in.Categories = []string{}
	for _, c := range h.Capabilities {
		if c.Channel == channel && c.Scope == "native_control" {
			in.TargetScope = c.TargetScope
			in.TargetRefs = append([]string{}, c.TargetRefs...)
			in.ConfigDigest = c.ConfigDigest
		}
	}
	return in
}
func endpointNativeApproved(t *testing.T, f assessmentSourceFixture, source OperationsSource, h EndpointHeartbeatInput, reviewer int64, channel string) EndpointPolicy {
	t.Helper()
	p, err := f.repo.CreateEndpointPolicy(t.Context(), f.ws.ID, f.user.ID, endpointNativeProposal(source, h, channel))
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.repo.DecideEndpointPolicy(t.Context(), f.ws.ID, reviewer, p.ID, p.Revision, "approve", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.NativeLease == nil || p.NativeLease.State != "issued" {
		t.Fatal("approval lacks stable native lease", p)
	}
	return p
}
func endpointNativeReceipt(t *testing.T, f assessmentSourceFixture, p EndpointPolicy, phase string) EndpointReceiptInput {
	t.Helper()
	in := EndpointReceiptInput{ReceiptRef: endpointHash(fmt.Sprint(p.ID) + phase + fmt.Sprint(time.Now().UnixNano())), DeviceRef: p.DeviceRef, PolicyID: p.ID, PolicyRevision: p.Revision, PolicyDigest: p.Digest, Channel: p.Channel, Scope: p.Scope, Executor: p.Executor, Status: "applied", ObservedAt: endpointNow(t, f), Phase: phase, LeaseRef: p.NativeLease.LeaseRef, OperationRef: p.NativeLease.ApplyOperationRef, Action: "apply", TargetScope: p.TargetScope, TargetRefs: append([]string{}, p.TargetRefs...), ConfigDigest: p.ConfigDigest, PriorStateDigest: endpointHash("prior"), StateDigest: endpointHash("applied")}
	switch phase {
	case "prepared":
		in.Code = "native_prepared"
		in.StateDigest = endpointHash("applied")
	case "configured":
		in.Code = "native_configured"
	case "readback":
		in.Code = "native_readback_match"
	case "functional_verified":
		in.Code = "native_functional_blocked"
		in.VerificationMethod = map[string]string{"usb": "removable_write_probe", "print": "configured_queue_job_retained", "clipboard": "rdp_clipboard_probe"}[p.Channel]
		in.VerificationRef = endpointHash("synthetic-verification")
	case "rolled_back":
		in.Code = "native_rolled_back"
		in.StateDigest = in.PriorStateDigest
		in.Action = "rollback"
		in.OperationRef = p.NativeLease.RollbackOperationRef
	case "conflict":
		in.Status = "failed"
		in.Code = "native_conflict"
		in.Action = "rollback"
		in.OperationRef = p.NativeLease.RollbackOperationRef
		in.StateDigest = endpointHash("external-change")
	case "failed":
		in.Status = "failed"
		in.Code = "permission_denied"
	}
	return in
}
func TestEndpointNativeThreeTargetsPhasesAndFrozenExport(t *testing.T) {
	for _, channel := range []string{"usb", "print", "clipboard"} {
		t.Run(channel, func(t *testing.T) {
			f, source, digest, h, reviewer := endpointNativeFixture(t)
			ctx := t.Context()
			p := endpointNativeApproved(t, f, source, h, reviewer, channel)
			bundle, err := f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
			if err != nil || len(bundle.NativeCommands) != 1 {
				t.Fatal("native command missing", bundle, err)
			}
			command := bundle.NativeCommands[0]
			if command.Action != "apply" || command.OperationRef != p.NativeLease.ApplyOperationRef || command.Policy.ConfigDigest != p.ConfigDigest {
				t.Fatal("command lost binding", command)
			}
			again, err := f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
			if err != nil || again.NativeCommands[0].LeaseRef != command.LeaseRef || again.NativeCommands[0].OperationRef != command.OperationRef {
				t.Fatal("lease changed on polling", err)
			}
			// Configuration without the durably prepared original state is rejected.
			if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, endpointNativeReceipt(t, f, p, "configured")); !errors.Is(err, ErrEvidenceRequired) {
				t.Fatal("write had no persisted original", err)
			}
			for _, phase := range []string{"prepared", "configured", "readback"} {
				in := endpointNativeReceipt(t, f, p, phase)
				receipt, replayed, err := f.repo.ReceiveEndpointReceipt(ctx, digest, in)
				if err != nil || replayed {
					t.Fatal(phase, err)
				}
				replay, replayed, err := f.repo.ReceiveEndpointReceipt(ctx, digest, in)
				if err != nil || !replayed || replay.ID != receipt.ID {
					t.Fatal("receipt replay duplicated", phase, err)
				}
				detail, err := f.repo.EndpointPolicyDetail(ctx, f.ws.ID, reviewer, p.ID)
				if err != nil || detail.DeploymentState != phase {
					t.Fatal("wrong phase projection", phase, detail.DeploymentState, err)
				}
				if detail.NativeLease == nil || detail.NativeLease.PriorStateDigest != in.PriorStateDigest {
					t.Fatal("original state digest not frozen")
				}
				if phase == "prepared" && detail.NativeLease.AppliedStateDigest != "" {
					t.Fatal("prepared claimed actual write")
				}
				if phase == "prepared" {
					wrong := endpointNativeReceipt(t, f, p, "configured")
					wrong.StateDigest = endpointHash("not-planned-state")
					if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, wrong); !errors.Is(err, ErrConflict) {
						t.Fatal("unplanned actual configuration reported as success", err)
					}
					if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, endpointNativeReceipt(t, f, p, "readback")); !errors.Is(err, ErrEvidenceRequired) {
						t.Fatal("readback without configured state", err)
					}
				}
				if phase == "configured" {
					if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, endpointNativeReceipt(t, f, p, "functional_verified")); !errors.Is(err, ErrEvidenceRequired) {
						t.Fatal("functional claim skipped readback", err)
					}
				}
				if detail.DeploymentState == "functional_verified" {
					t.Fatal("readback inferred functional verification")
				}
			}
			exported, err := f.repo.CreateEndpointEvidenceExport(ctx, f.ws.ID, reviewer, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if exported.Snapshot.Version != 2 || len(exported.Snapshot.Receipts) != 3 || exported.Snapshot.Policy.NativeLease == nil || exported.SnapshotDigest != classificationml.Digest(exported.Snapshot) {
				t.Fatal("native evidence freeze incomplete", exported)
			}
			raw, _ := json.Marshal(exported)
			if strings.Contains(string(raw), digest) || strings.Contains(string(raw), "HKLM") {
				t.Fatal("native export leaked source secret/path")
			}
			// A functional claim requires an explicit method and evidence reference.
			proof := endpointNativeReceipt(t, f, p, "functional_verified")
			proof.VerificationRef = ""
			if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, proof); !errors.Is(err, ErrInvalid) {
				t.Fatal("readback promoted to functional", err)
			}
			proof = endpointNativeReceipt(t, f, p, "functional_verified")
			if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, proof); err != nil {
				t.Fatal("synthetic explicit probe receipt", err)
			}
			_, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, f.user.ID, p.ID, p.Revision, "revoke", "Test restore original state")
			if err != nil {
				t.Fatal(err)
			}
			bundle, err = f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
			if err != nil || len(bundle.NativeCommands) != 1 || bundle.NativeCommands[0].Action != "rollback" || bundle.NativeCommands[0].OperationRef != p.NativeLease.RollbackOperationRef {
				t.Fatal("revoke lacks bounded rollback", bundle, err)
			}
			conflict := endpointNativeReceipt(t, f, p, "conflict")
			conflict.StateDigest = "" // A failed CAS may leave current state unobservable; never invent a digest.
			if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, conflict); err != nil {
				t.Fatal(err)
			}
			detail, err := f.repo.EndpointPolicyDetail(ctx, f.ws.ID, reviewer, p.ID)
			if err != nil || detail.DeploymentState != "conflict" {
				t.Fatal("conflict claimed restored", detail, err)
			}
			rollback := endpointNativeReceipt(t, f, p, "rolled_back")
			rollback.StateDigest = endpointHash("wrong-restored-state")
			if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, rollback); !errors.Is(err, ErrInvalid) {
				t.Fatal("mismatched original claimed restored", err)
			}
			rollback = endpointNativeReceipt(t, f, p, "rolled_back")
			if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, rollback); err != nil {
				t.Fatal(err)
			}
			bundle, err = f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
			if err != nil || len(bundle.NativeCommands) != 0 {
				t.Fatal("restored lease still commands", bundle, err)
			}
			frozen, err := f.repo.EndpointEvidenceExport(ctx, f.ws.ID, reviewer, exported.ID)
			if err != nil || frozen.Snapshot.Policy.NativeLease.State != "readback" || classificationml.Digest(frozen.Snapshot) != exported.SnapshotDigest {
				t.Fatal("live stages rewrote frozen report", err)
			}
			if _, err = f.pool.Exec(ctx, `UPDATE plume.endpoint_native_leases SET prior_state_digest=$2 WHERE policy_id=$1`, p.ID, endpointHash("changed")); err == nil {
				t.Fatal("original state mutable")
			}
		})
	}
}
func TestEndpointNativeBindingSupersessionAndNoopRollback(t *testing.T) {
	f, source, digest, h, reviewer := endpointNativeFixture(t)
	ctx := t.Context()
	p := endpointNativeApproved(t, f, source, h, reviewer, "usb")
	for _, field := range []string{"lease", "operation", "target", "config", "prior"} {
		in := endpointNativeReceipt(t, f, p, "prepared")
		switch field {
		case "lease":
			in.LeaseRef = endpointHash("wrong")
		case "operation":
			in.OperationRef = p.NativeLease.RollbackOperationRef
		case "target":
			in.TargetRefs = []string{endpointHash("wrong")}
		case "config":
			in.ConfigDigest = endpointHash("wrong")
		}
		if field == "prior" {
			valid := endpointNativeReceipt(t, f, p, "prepared")
			if _, _, err := f.repo.ReceiveEndpointReceipt(ctx, digest, valid); err != nil {
				t.Fatal(err)
			}
			in.PriorStateDigest = endpointHash("changed-prior")
		}
		if _, _, err := f.repo.ReceiveEndpointReceipt(ctx, digest, in); !errors.Is(err, ErrConflict) {
			t.Fatal("binding ignored", field, err)
		}
	}
	in := endpointNativeProposal(source, h, "usb")
	in.Revision = p.Revision
	next, err := f.repo.CreateEndpointPolicy(ctx, f.ws.ID, f.user.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	next, err = f.repo.DecideEndpointPolicy(ctx, f.ws.ID, reviewer, next.ID, next.Revision, "approve", "")
	if err != nil {
		t.Fatal(err)
	}
	if next.DeploymentState != "awaiting_rollback" {
		t.Fatal("new lease may overwrite original", next)
	}
	bundle, err := f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
	if err != nil || len(bundle.NativeCommands) != 1 || bundle.NativeCommands[0].Policy.ID != p.ID || bundle.NativeCommands[0].Action != "rollback" {
		t.Fatal("supersede apply before restore", bundle, err)
	}
	if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, endpointNativeReceipt(t, f, next, "prepared")); !errors.Is(err, ErrEvidenceRequired) {
		t.Fatal("new lease bypassed rollback gate", err)
	}
	noop := endpointNativeReceipt(t, f, p, "rolled_back")
	noop.Code = "native_not_applied"
	if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, noop); err != nil {
		t.Fatal("prepared but not applied lease cannot finish", err)
	}
	bundle, err = f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
	if err != nil || len(bundle.NativeCommands) != 1 || bundle.NativeCommands[0].Policy.ID != next.ID || bundle.NativeCommands[0].Action != "apply" {
		t.Fatal("new lease did not resume after restore", bundle, err)
	}
	foreign := assessmentSourceTestStore(t)
	if _, err = f.repo.EndpointPolicyDetail(ctx, foreign.ws.ID, foreign.user.ID, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign native policy visible", err)
	}
	// A changed target configuration invalidates current execution, returning
	// only the old frozen target rollback, never the newly reported target.
	for i := range h.Capabilities {
		if h.Capabilities[i].Scope == "native_control" && h.Capabilities[i].Channel == "usb" {
			h.Capabilities[i].ConfigDigest = endpointHash("changed-local-config")
		}
	}
	h.ObservedAt = endpointNow(t, f)
	if _, err = f.repo.EndpointHeartbeat(ctx, digest, h); err != nil {
		t.Fatal(err)
	}
	bundle, err = f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
	if err != nil || len(bundle.NativeCommands) != 1 || bundle.NativeCommands[0].Action != "rollback" || bundle.NativeCommands[0].Policy.ConfigDigest != next.ConfigDigest {
		t.Fatal("configuration change retargeted lease", bundle, err)
	}
}
func TestEndpointNativePrivilegeModesAndConfiguredAfterEnd(t *testing.T) {
	f, source, digest, h, reviewer := endpointNativeFixture(t)
	ctx := t.Context()
	for _, change := range []string{"os", "privilege", "mode", "raw-target"} {
		hb := h
		hb.Capabilities = append([]EndpointCapability{}, h.Capabilities...)
		hb.ObservedAt = endpointNow(t, f)
		for i := range hb.Capabilities {
			if hb.Capabilities[i].Scope == "native_control" {
				switch change {
				case "os":
					hb.OS = "darwin"
				case "privilege":
					hb.Capabilities[i].Privilege = "not_elevated"
				case "mode":
					hb.Capabilities[i].Modes = []string{"block_sensitive"}
				case "raw-target":
					hb.Capabilities[i].TargetRefs = []string{"real printer name"}
				}
			}
		}
		if _, err := f.repo.EndpointHeartbeat(ctx, digest, hb); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsupported native capability", change, err)
		}
	}
	p := endpointNativeApproved(t, f, source, h, reviewer, "print")
	prepared := endpointNativeReceipt(t, f, p, "prepared")
	if _, _, err := f.repo.ReceiveEndpointReceipt(ctx, digest, prepared); err != nil {
		t.Fatal(err)
	}
	_, err := f.repo.DecideEndpointPolicy(ctx, f.ws.ID, f.user.ID, p.ID, p.Revision, "revoke", "Stop during local write")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"configured", "readback"} {
		if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, endpointNativeReceipt(t, f, p, phase)); err != nil {
			t.Fatal("late real write/readback lost after prepared", phase, err)
		}
	}
	detail, err := f.repo.EndpointPolicyDetail(ctx, f.ws.ID, reviewer, p.ID)
	if err != nil || detail.Current || detail.DeploymentState != "rollback_pending" {
		t.Fatal("late configuration restored ended authorization", detail, err)
	}
	// Source token ownership is rechecked even for lease restore receipts.
	if err = f.repo.RevokeOperationsSource(ctx, f.ws.ID, f.user.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, endpointNativeReceipt(t, f, p, "rolled_back")); !errors.Is(err, ErrAutomationUnauthorized) {
		t.Fatal("revoked source submitted receipt", err)
	}
}

func TestEndpointNativeExpiredLeaseStillDeliversFrozenRollback(t *testing.T) {
	f, source, digest, h, reviewer := endpointNativeFixture(t)
	ctx := t.Context()
	// Seed an immutable, previously approved policy as it would exist after a
	// platform restart. This tests expiry delivery without changing host clocks
	// or weakening the policy immutability trigger.
	in := endpointNativeProposal(source, h, "usb")
	in.DurationMinutes = 1
	in.Revision = 1
	p := EndpointPolicy{EndpointPolicyInput: in, OS: h.OS, AgentVersion: h.AgentVersion, Creator: f.user.Username, CreatorID: f.user.ID, SourceName: source.Name, Executor: endpointNativeExecutor("usb")}
	d, err := f.repo.ListEndpointDevices(ctx, f.ws.ID, f.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.Capability, p.CapabilityDigest = endpointCapability(d[0], "usb", "native_control")
	p.CreatedAt = endpointNow(t, f).Add(-3 * time.Minute)
	p.ExpiresAt = p.CreatedAt.Add(time.Minute)
	approved := p.CreatedAt.Add(time.Second)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT nextval('plume.endpoint_channel_policies_id_seq')`).Scan(&p.ID); err != nil {
		t.Fatal(err)
	}
	p.Digest = assessmentDigest(p)
	_, err = tx.Exec(ctx, `INSERT INTO plume.endpoint_channel_policies(id,workspace_id,source_id,channel,scope,revision,actor_id,body,digest,status,approved_by,approved_at,expires_at,created_at) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,'usb','native_control',1,$4,$5,$6,'approved',$7,$8,$9,$10)`, p.ID, f.ws.ID, source.ID, f.user.ID, p, p.Digest, reviewer, approved, p.ExpiresAt, p.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	p.ApprovedAt = &approved
	if err = createEndpointNativeLease(ctx, tx, f.ws.ID, p); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	bundle, err := f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
	if err != nil || len(bundle.Policies) != 0 || len(bundle.NativeCommands) != 1 || bundle.NativeCommands[0].Action != "rollback" || bundle.NativeCommands[0].Reason != "expired" {
		t.Fatal("expired policy lost restore command", bundle, err)
	}
	command := bundle.NativeCommands[0]
	p = command.Policy
	if p.TargetScope != in.TargetScope || !strings.EqualFold(p.ConfigDigest, in.ConfigDigest) || p.Current {
		t.Fatal("expiry changed frozen target", p)
	}
	noop := endpointNativeReceipt(t, f, p, "rolled_back")
	noop.Code = "native_not_applied"
	if _, _, err = f.repo.ReceiveEndpointReceipt(ctx, digest, noop); err != nil {
		t.Fatal("never-applied expired lease cannot close", err)
	}
	bundle, err = f.repo.EndpointPoliciesForAgent(ctx, digest, h.DeviceRef)
	if err != nil || len(bundle.NativeCommands) != 0 {
		t.Fatal("closed expired lease still pending", bundle, err)
	}
}
