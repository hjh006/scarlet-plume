package endpointnativecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func fixedTarget(executor string) Target {
	t := Target{Executor: executor, TargetRef: strings.Repeat("a", 64), ConfigDigest: strings.Repeat("b", 64)}
	if executor == PrintPause {
		t.PrinterName = "Synthetic local queue"
	}
	return t
}

func memoryController(t *testing.T, target Target, initial State) (*Manager, *State, *int) {
	t.Helper()
	m, err := New([]Target{target})
	if err != nil {
		t.Fatal(err)
	}
	state, count := initial, 0
	m.read = func(ctx context.Context, target Target) (State, error) { return state, ctx.Err() }
	m.change = func(ctx context.Context, target Target, previous, next State) (State, error) {
		if state != previous {
			return State{}, ErrConflict
		}
		count++
		if target.Executor != PrintPause {
			next.ChangeStamp = fmt.Sprintf("%016x", count+1)
		}
		state = next
		return next, nil
	}
	return m, &state, &count
}

func initialState(executor string) State {
	if executor == PrintPause {
		return State{Present: true, IdentityDigest: strings.Repeat("c", 64)}
	}
	return State{ChangeStamp: "0000000000000001"}
}

func TestFixedControlsCaptureApplySealedRecoveryAndIdempotentRestore(t *testing.T) {
	for _, executor := range []string{USBWrite, PrintPause, RDPClipboard} {
		t.Run(executor, func(t *testing.T) {
			target := fixedTarget(executor)
			m, state, changes := memoryController(t, target, initialState(executor))
			before, err := m.Capture(t.Context(), target.TargetRef)
			if err != nil {
				t.Fatal(err)
			}
			planned, err := Expected(before)
			if err != nil {
				t.Fatal(err)
			}
			if planned.Phase != "desired" || planned.StateDigest == before.StateDigest {
				t.Fatal("invalid pure desired plan")
			}
			applied, err := m.Apply(t.Context(), before)
			if err != nil {
				t.Fatal(err)
			}
			if !applied.Configured || applied.FunctionalVerified || applied.Snapshot.Phase != "applied" || applied.Snapshot.StateDigest != planned.StateDigest || *changes != 1 {
				t.Fatal("configuration readback promoted into functional prevention or lost state identity")
			}
			if applied.RequiredAction == "" || applied.Reason == "" {
				t.Fatal("missing business-readable follow-up")
			}
			// Simulate serialization into the caller's protected local lease and
			// reconstruction in a new process. No native APIs are called.
			raw, _ := json.Marshal([]Snapshot{before, applied.Snapshot})
			if strings.Contains(string(raw), target.PrinterName) && target.PrinterName != "" {
				t.Fatal("local queue name entered the anonymous snapshot")
			}
			var sealed []Snapshot
			if json.Unmarshal(raw, &sealed) != nil {
				t.Fatal("snapshot not persistable")
			}
			restarted, restartState, restartChanges := memoryController(t, target, *state)
			restored, err := restarted.Restore(t.Context(), sealed[0], sealed[1])
			if err != nil {
				t.Fatal(err)
			}
			if restored.Configured || restored.FunctionalVerified || restored.Snapshot.Phase != "restored" || restored.Snapshot.StateDigest != before.StateDigest || *restartChanges != 1 {
				t.Fatal("restore did not recover the exact prior value digest")
			}
			if executor != PrintPause && restartState.ChangeStamp == before.State.ChangeStamp {
				t.Fatal("restore pretended the old registry timestamp could be restored")
			}
			if _, err := restarted.Restore(t.Context(), sealed[0], sealed[1]); err != nil || *restartChanges != 1 {
				t.Fatal("idempotent recovery repeated a native write")
			}
		})
	}
}

func TestDesiredOrReadbackCannotBecomeCrashGapOwnership(t *testing.T) {
	for _, executor := range []string{USBWrite, PrintPause, RDPClipboard} {
		t.Run(executor, func(t *testing.T) {
			target := fixedTarget(executor)
			m, state, changes := memoryController(t, target, initialState(executor))
			before, _ := m.Capture(t.Context(), target.TargetRef)
			planned, _ := Expected(before)
			*state = planned.State // could have been another administrator's action
			if executor != PrintPause {
				state.ChangeStamp = "0000000000000002"
			}
			if _, err := m.Restore(t.Context(), before, planned); !errors.Is(err, ErrConflict) || *changes != 0 {
				t.Fatal("intent alone took over another administrator's state")
			}
			readback, err := m.Readback(t.Context(), target.TargetRef)
			if err != nil {
				t.Fatal(err)
			}
			if !readback.Configured || readback.FunctionalVerified {
				t.Fatal("readback does not describe configuration only")
			}
			if _, err := m.Restore(t.Context(), before, readback.Snapshot); !errors.Is(err, ErrConflict) || *changes != 0 {
				t.Fatal("unowned configuration readback granted rollback ownership")
			}
		})
	}
}

func TestNoExistingAdministratorStateIsTakenOver(t *testing.T) {
	for _, sample := range []struct {
		executor string
		state    State
	}{
		{USBWrite, State{Present: true, Value: 0}},
		{USBWrite, State{Present: true, Value: 1}},
		{RDPClipboard, State{Present: true, Value: 0}},
		{RDPClipboard, State{Present: true, Value: 1}},
		{PrintPause, State{Present: true, Paused: true, IdentityDigest: strings.Repeat("c", 64)}},
	} {
		m, _, changes := memoryController(t, fixedTarget(sample.executor), sample.state)
		before, err := m.Capture(t.Context(), strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Expected(before); !errors.Is(err, ErrConflict) {
			t.Fatal("existing explicit state was adopted")
		}
		if _, err := m.Apply(t.Context(), before); !errors.Is(err, ErrConflict) || *changes != 0 {
			t.Fatal("existing administrator state overwritten")
		}
	}
}

func TestExternalChangesStopApplyAndRestore(t *testing.T) {
	for _, kind := range []string{"registry value", "registry same-value write", "printer configuration", "stale capture"} {
		t.Run(kind, func(t *testing.T) {
			executor := USBWrite
			if kind == "printer configuration" {
				executor = PrintPause
			}
			target := fixedTarget(executor)
			m, state, changes := memoryController(t, target, initialState(executor))
			before, _ := m.Capture(t.Context(), target.TargetRef)
			if kind == "stale capture" {
				state.ChangeStamp = "0000000000000009"
				if _, err := m.Apply(t.Context(), before); !errors.Is(err, ErrConflict) || *changes != 0 {
					t.Fatal("changed capture was overwritten")
				}
				return
			}
			applied, err := m.Apply(t.Context(), before)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "registry value":
				state.Value = 0
			case "registry same-value write":
				state.ChangeStamp = "0000000000000009"
			case "printer configuration":
				state.IdentityDigest = strings.Repeat("d", 64)
			}
			outside := *state
			if _, err := m.Restore(t.Context(), before, applied.Snapshot); !errors.Is(err, ErrConflict) || *changes != 1 || *state != outside {
				t.Fatal("external administrator state overwritten during recovery")
			}
		})
	}
}

func TestUnsafeTargetsAndTamperedSnapshotsAreRejected(t *testing.T) {
	validTarget := fixedTarget(PrintPause)
	for _, name := range []string{`\\server\queue`, `Queue, Job 1`, "Queue/other", "Queue\nother", " Queue", "Queue\x00other"} {
		target := validTarget
		target.PrinterName = name
		if _, err := New([]Target{target}); !errors.Is(err, ErrTarget) {
			t.Fatal("unsafe printer target accepted")
		}
	}
	for _, targets := range [][]Target{nil, {fixedTarget("arbitrary_executor")}, {validTarget, validTarget}, {{Executor: USBWrite, TargetRef: strings.Repeat("A", 64), ConfigDigest: strings.Repeat("b", 64)}}, {{Executor: USBWrite, TargetRef: strings.Repeat("a", 64), ConfigDigest: strings.Repeat("b", 64), PrinterName: "not a registry path"}}} {
		if _, err := New(targets); !errors.Is(err, ErrTarget) {
			t.Fatal("unbounded or ambiguous target configuration accepted")
		}
	}
	m, _, changes := memoryController(t, fixedTarget(USBWrite), initialState(USBWrite))
	before, _ := m.Capture(t.Context(), strings.Repeat("a", 64))
	before.ConfigDigest = strings.Repeat("c", 64)
	if _, err := m.Apply(t.Context(), before); !errors.Is(err, ErrTarget) || *changes != 0 {
		t.Fatal("changed target binding accepted")
	}
}

func TestUncertainNativeResultAndCancellationAreNotClaimedAsApplied(t *testing.T) {
	target := fixedTarget(USBWrite)
	m, state, _ := memoryController(t, target, initialState(USBWrite))
	before, _ := m.Capture(t.Context(), target.TargetRef)
	m.change = func(context.Context, Target, State, State) (State, error) { return *state, nil }
	result, err := m.Apply(t.Context(), before)
	if !errors.Is(err, ErrReadback) || result.Configured || result.FunctionalVerified || result.Snapshot.Phase == "applied" {
		t.Fatal("unconfirmed mutation granted applied ownership")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.Capture(ctx, target.TargetRef); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled capture executed")
	}
	for _, err := range []error{ErrManaged, ErrPermission, ErrConflict, ErrUnsupported, ErrReadback, ErrUnavailable, ErrTarget} {
		if Reason(err) == "" {
			t.Fatal("missing actionable failure explanation")
		}
	}
}

func TestBlockedPrintProviderCannotBlockAnIndependentRegistryTarget(t *testing.T) {
	printer, usb := fixedTarget(PrintPause), fixedTarget(USBWrite)
	usb.TargetRef = strings.Repeat("d", 64)
	m, err := New([]Target{printer, usb})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	m.read = func(ctx context.Context, target Target) (State, error) {
		if target.Executor == PrintPause {
			close(entered)
			<-release
		}
		return initialState(target.Executor), nil
	}
	printDone := make(chan error, 1)
	go func() { _, err := m.Capture(t.Context(), printer.TargetRef); printDone <- err }()
	<-entered
	registryDone := make(chan error, 1)
	go func() { _, err := m.Capture(t.Context(), usb.TargetRef); registryDone <- err }()
	select {
	case err := <-registryDone:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("unrelated target was blocked by print provider")
	}
	close(release)
	if err := <-printDone; err != nil {
		t.Fatal(err)
	}
}
