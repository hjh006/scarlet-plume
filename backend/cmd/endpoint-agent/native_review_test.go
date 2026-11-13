package main

import (
	"context"
	"strings"
	"testing"
	"time"

	native "github.com/hjh006/scarlet-plume/backend/internal/endpointnativecontrol"
)

type afterApplyDriver struct {
	nativeDriver
	after func()
}

func (d afterApplyDriver) Apply(ctx context.Context, s native.Snapshot) (native.Readback, error) {
	r, err := d.nativeDriver.Apply(ctx, s)
	d.after()
	return r, err
}

func TestNativeLeaseCannotBeginASecondTargetAfterTTL(t *testing.T) {
	e, d, cmd, _, _ := nativeFixture(t)
	second := strings.Repeat("8", 64)
	base := d.current[cmd.Policy.TargetRefs[0]]
	base.TargetRef = second
	d.current[second] = base
	cmd.Policy.TargetRefs = append(cmd.Policy.TargetRefs, second)
	e.driver = afterApplyDriver{nativeDriver: d, after: func() { e.entries[cmd.LeaseRef].deadline = time.Now().Add(-time.Second) }}
	if err := e.Handle(t.Context(), cmd); err != nil {
		t.Fatal(err)
	}
	if d.applied != 1 || d.restored != 1 || d.current[second].State.Paused || e.entries[cmd.LeaseRef].journal.State != "rolled_back" {
		t.Fatal("a later target started after expiry, or sealed prefix was not restored")
	}
}

func TestNativeStopWaitsForBusyLeaseThenRestores(t *testing.T) {
	e, d, cmd, _, _ := nativeFixture(t)
	if err := e.Handle(t.Context(), cmd); err != nil {
		t.Fatal(err)
	}
	r := e.entries[cmd.LeaseRef]
	r.mu.Lock()
	done := make(chan struct{})
	started := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	go func() { close(started); e.Expire(ctx, true); close(done) }()
	<-started
	select {
	case <-done:
		r.mu.Unlock()
		t.Fatal("Stop skipped an in-progress lease")
	case <-time.After(30 * time.Millisecond): // the operation is deliberately held in progress
	}
	r.mu.Unlock()
	<-done
	if d.restored != 1 || e.ensureRestored() != nil {
		t.Fatal("Stop failed to restore the previously busy known lease")
	}
}

func TestNativeMissingOriginalJournalNeverClaimsRemoteLeaseWasNotApplied(t *testing.T) {
	for _, state := range []string{"prepared", "configured", "readback", "functional_verified", "conflict", ""} {
		for _, action := range []string{"apply", "rollback"} {
			t.Run(state+"/"+action, func(t *testing.T) {
				e, d, cmd, _, receipts := nativeFixture(t)
				cmd.Policy.NativeLease.State = state
				cmd.Action = action
				if action == "rollback" {
					cmd.OperationRef = cmd.Policy.NativeLease.RollbackOperationRef
				}
				if err := e.Handle(t.Context(), cmd); err != nil {
					t.Fatal(err)
				}
				if d.applied != 0 || d.restored != 0 || len(*receipts) != 1 || (*receipts)[0].Phase != "conflict" || (*receipts)[0].Code == "native_not_applied" {
					t.Fatal("missing original journal guessed remote native ownership or no-effect")
				}
			})
		}
	}
	e, d, cmd, _, receipts := nativeFixture(t)
	cmd.Action = "rollback"
	cmd.OperationRef = cmd.Policy.NativeLease.RollbackOperationRef
	if e.Handle(t.Context(), cmd) != nil || d.applied != 0 || d.restored != 0 || len(*receipts) != 1 || (*receipts)[0].Code != "native_not_applied" {
		t.Fatal("genuine never-prepared issued lease lost no-effect rollback")
	}
}
