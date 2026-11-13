package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	native "github.com/hjh006/scarlet-plume/backend/internal/endpointnativecontrol"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

type fixtureNativeDriver struct {
	current           map[string]native.Snapshot
	applied, restored int
	failTarget        string
	trace             *[]string
}

func (d *fixtureNativeDriver) Capture(_ context.Context, ref string) (native.Snapshot, error) {
	s, ok := d.current[ref]
	if !ok {
		return s, native.ErrTarget
	}
	s.Phase = "captured"
	return s, nil
}
func (d *fixtureNativeDriver) Expected(before native.Snapshot) (native.Snapshot, error) {
	before.Phase = "desired"
	before.State.Paused = true
	before.StateDigest = strings.Repeat("e", 64)
	return before, nil
}
func (d *fixtureNativeDriver) Apply(_ context.Context, before native.Snapshot) (native.Readback, error) {
	d.applied++
	*d.trace = append(*d.trace, "native-apply")
	after, _ := d.Expected(before)
	after.Phase = "applied"
	d.current[before.TargetRef] = after
	if before.TargetRef == d.failTarget {
		return native.Readback{}, native.ErrReadback
	}
	return native.Readback{Snapshot: after, Configured: true, RequiredAction: "verify_queue_scheduling"}, nil
}
func (d *fixtureNativeDriver) Readback(_ context.Context, ref string) (native.Readback, error) {
	return native.Readback{Snapshot: d.current[ref], Configured: true}, nil
}
func (d *fixtureNativeDriver) Restore(_ context.Context, before, after native.Snapshot) (native.Readback, error) {
	d.restored++
	d.current[before.TargetRef] = before
	return native.Readback{Snapshot: before}, nil
}

func nativeFixture(t *testing.T) (*nativeEngine, *fixtureNativeDriver, store.EndpointNativeCommand, *[]string, *[]store.EndpointReceiptInput) {
	t.Helper()
	trace := []string{}
	receipts := []store.EndpointReceiptInput{}
	device := strings.Repeat("a", 64)
	ref := strings.Repeat("b", 64)
	config := strings.Repeat("c", 64)
	p := store.EndpointPolicy{EndpointPolicyInput: store.EndpointPolicyInput{DeviceRef: device, Channel: "print", Scope: "native_control", Mode: "block_all", TargetScope: "configured_print_queues", TargetRefs: []string{ref}, ConfigDigest: config, Revision: 1}, ID: 1, Status: "approved", Digest: strings.Repeat("d", 64), Executor: native.PrintPause, ExpiresAt: time.Now().Add(time.Hour), NativeLease: &store.EndpointNativeLease{State: "issued", LeaseRef: strings.Repeat("1", 64), ApplyOperationRef: strings.Repeat("2", 64), RollbackOperationRef: strings.Repeat("3", 64)}}
	cmd := store.EndpointNativeCommand{LeaseRef: p.NativeLease.LeaseRef, OperationRef: p.NativeLease.ApplyOperationRef, Action: "apply", Reason: "approved", Policy: p}
	driver := &fixtureNativeDriver{current: map[string]native.Snapshot{ref: {Version: 1, Executor: p.Executor, TargetRef: ref, ConfigDigest: config, Phase: "captured", StateDigest: strings.Repeat("f", 64), State: native.State{Present: true, IdentityDigest: strings.Repeat("9", 64)}}}, trace: &trace}
	c := &endpointClient{device: device, key: []byte(strings.Repeat("k", 32)), scope: "synthetic-native-engine"}
	e := &nativeEngine{driver: driver, client: c, save: func(j nativeJournal) error { trace = append(trace, "saved:"+j.State); return nil }, report: func(ctx context.Context, r store.EndpointReceiptInput) error {
		trace = append(trace, "report:"+r.Phase)
		receipts = append(receipts, r)
		return nil
	}}
	return e, driver, cmd, &trace, &receipts
}
func TestNativeLeasePreparedBeforeMutationAndReversibleExpiry(t *testing.T) {
	e, d, cmd, trace, receipts := nativeFixture(t)
	if err := e.Handle(t.Context(), cmd); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*trace, ",")
	if !strings.Contains(joined, "report:prepared,saved:captured,saved:applying,native-apply") {
		t.Fatalf("native operation preceded durable preparation/audit: %s", joined)
	}
	if d.applied != 1 || len(*receipts) != 3 {
		t.Fatal("missing prepared/configured/readback phases")
	}
	for _, r := range *receipts {
		if r.Phase == "functional_verified" {
			t.Fatal("configuration became functional proof")
		}
	}
	if e.Handle(t.Context(), cmd) != nil || d.applied != 1 {
		t.Fatal("repeated command repeated native mutation")
	}
	e.entries[cmd.LeaseRef].deadline = time.Now().Add(-time.Second)
	e.Expire(t.Context(), false)
	if d.restored != 1 || d.current[cmd.Policy.TargetRefs[0]].State.Paused {
		t.Fatal("expiry did not restore recorded original state")
	}
	last := (*receipts)[len(*receipts)-1]
	if last.Phase != "rolled_back" || last.StateDigest != last.PriorStateDigest || last.Action != "rollback" {
		t.Fatal("rollback did not retain exact original digest")
	}
}
func TestNativeLeasePreparationOfflineCannotWrite(t *testing.T) {
	e, d, cmd, _, _ := nativeFixture(t)
	e.report = func(context.Context, store.EndpointReceiptInput) error { return errors.New("offline") }
	if e.Handle(t.Context(), cmd) == nil || d.applied != 0 {
		t.Fatal("offline preparation allowed native mutation")
	}
}
func TestNativeLeaseCrashGapIsNotOwnershipProof(t *testing.T) {
	e, d, cmd, _, _ := nativeFixture(t)
	var saved nativeJournal
	e.save = func(j nativeJournal) error {
		raw, _ := json.Marshal(j)
		_ = json.Unmarshal(raw, &saved)
		if j.State == "applied" {
			return errors.New("disk failure")
		}
		return nil
	}
	if e.Handle(t.Context(), cmd) == nil || d.applied != 1 {
		t.Fatal("expected a post-action durable write failure")
	}
	// A crash could have retained only the earlier applying intent. Matching
	// current state must not authorize a guessed restore.
	saved.State = "applying"
	saved.Applied = nil
	restarted, _, _, _, receipts := nativeFixture(t)
	restarted.driver = d
	if restarted.load(saved) != nil {
		t.Fatal("valid journal did not load")
	}
	restarted.Expire(t.Context(), true)
	if d.restored != 0 || len(*receipts) != 1 || (*receipts)[0].Phase != "conflict" {
		t.Fatal("ambiguous intent claimed native ownership")
	}
}
func TestNativeLeasePartialFailureRestoresOnlySealedPrefix(t *testing.T) {
	e, d, cmd, _, _ := nativeFixture(t)
	second := strings.Repeat("8", 64)
	first := cmd.Policy.TargetRefs[0]
	base := d.current[first]
	base.TargetRef = second
	d.current[second] = base
	cmd.Policy.TargetRefs = append(cmd.Policy.TargetRefs, second)
	d.failTarget = second
	if e.Handle(t.Context(), cmd) == nil || d.applied != 2 {
		t.Fatal("partial operation fixture did not fail")
	}
	e.Expire(t.Context(), true)
	if d.restored != 1 || d.current[first].State.Paused || !d.current[second].State.Paused {
		t.Fatal("partial recovery guessed an unsealed target or missed owned prefix")
	}
}
