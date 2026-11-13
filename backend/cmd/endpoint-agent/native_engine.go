package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	native "github.com/hjh006/scarlet-plume/backend/internal/endpointnativecontrol"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

type nativeDriver interface {
	Capture(context.Context, string) (native.Snapshot, error)
	Expected(native.Snapshot) (native.Snapshot, error)
	Apply(context.Context, native.Snapshot) (native.Readback, error)
	Readback(context.Context, string) (native.Readback, error)
	Restore(context.Context, native.Snapshot, native.Snapshot) (native.Readback, error)
}
type nativeJournal struct {
	Version        int                                   `json:"version"`
	Command        store.EndpointNativeCommand           `json:"command"`
	State          string                                `json:"state"`
	Before         []native.Snapshot                     `json:"before"`
	Planned        []native.Snapshot                     `json:"planned"`
	Applied        []native.Snapshot                     `json:"applied"`
	Receipts       map[string]store.EndpointReceiptInput `json:"receipts"`
	Acknowledged   map[string]bool                       `json:"acknowledged"`
	RequiredAction string                                `json:"requiredAction,omitempty"`
	DeadlineLocal  time.Time                             `json:"deadlineLocal"`
}
type nativeRuntime struct {
	mu       sync.Mutex
	journal  nativeJournal
	deadline time.Time
}
type nativeEngine struct {
	mu      sync.Mutex
	entries map[string]*nativeRuntime
	driver  nativeDriver
	client  *endpointClient
	save    func(nativeJournal) error
	report  func(context.Context, store.EndpointReceiptInput) error
}

func (e *nativeEngine) digest(snapshots []native.Snapshot) string {
	// Only anonymized state hashes enter the source receipt; raw registry/queue
	// snapshots stay sealed in the machine-local journal.
	type projection struct{ Target, Config, State string }
	items := []projection{}
	for _, s := range snapshots {
		items = append(items, projection{s.TargetRef, s.ConfigDigest, s.StateDigest})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Target < items[j].Target })
	raw, _ := json.Marshal(items)
	return e.client.ref("native-state", string(raw))
}
func (e *nativeEngine) load(j nativeJournal) error {
	if j.Version != 1 || j.Command.Policy.DeviceRef != e.client.device || j.Command.LeaseRef == "" || j.Command.Policy.NativeLease == nil || j.Command.Policy.NativeLease.LeaseRef != j.Command.LeaseRef || len(j.Command.Policy.TargetRefs) < 1 || len(j.Command.Policy.TargetRefs) > 20 || len(j.Before) > len(j.Command.Policy.TargetRefs) || len(j.Before) != len(j.Planned) || len(j.Applied) > len(j.Before) {
		return errors.New("invalid protected native journal")
	}
	for i, before := range j.Before {
		if before.TargetRef != j.Command.Policy.TargetRefs[i] || before.ConfigDigest != j.Command.Policy.ConfigDigest || before.Executor != j.Command.Policy.Executor || before.Phase != "captured" {
			return errors.New("invalid protected native target binding")
		}
		planned, err := e.driver.Expected(before)
		if err != nil || planned.StateDigest != j.Planned[i].StateDigest || j.Planned[i].Phase != "desired" {
			return errors.New("invalid protected native desired snapshot")
		}
		if i < len(j.Applied) {
			applied := j.Applied[i]
			if applied.Phase != "applied" || applied.TargetRef != before.TargetRef || applied.ConfigDigest != before.ConfigDigest || applied.Executor != before.Executor || applied.StateDigest != planned.StateDigest {
				return errors.New("invalid protected native applied prefix")
			}
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.entries == nil {
		e.entries = map[string]*nativeRuntime{}
	}
	if _, exists := e.entries[j.Command.LeaseRef]; exists {
		return errors.New("duplicate native lease journal")
	}
	// On restart, a fresh authenticated bundle may refresh this deadline. Until
	// then an unknown clock/reboot cannot extend a prior native control lease.
	e.entries[j.Command.LeaseRef] = &nativeRuntime{journal: j, deadline: time.Now()}
	return nil
}
func (e *nativeEngine) persist(r *nativeRuntime) error { return e.save(r.journal) }
func (e *nativeEngine) receipt(ctx context.Context, r *nativeRuntime, phase, code string) error {
	j := &r.journal
	if j.Receipts == nil {
		j.Receipts = map[string]store.EndpointReceiptInput{}
	}
	if j.Acknowledged == nil {
		j.Acknowledged = map[string]bool{}
	}
	key := j.Command.Action + ":" + phase + ":" + code
	in, exists := j.Receipts[key]
	if !exists {
		p := j.Command.Policy
		action, op := "apply", p.NativeLease.ApplyOperationRef
		if phase == "rolled_back" || j.Command.Action == "rollback" {
			action = "rollback"
			op = p.NativeLease.RollbackOperationRef
		}
		status := "applied"
		if phase == "failed" || phase == "conflict" {
			status = "failed"
		}
		state := e.digest(j.Applied)
		if phase == "prepared" {
			state = e.digest(j.Planned)
		}
		if phase == "rolled_back" {
			state = e.digest(j.Before)
		}
		prior := ""
		if len(j.Before) > 0 {
			prior = e.digest(j.Before)
		}
		if phase == "rolled_back" && code == "native_not_applied" {
			prior = state
		}
		if len(j.Applied) == 0 && (phase == "failed" || phase == "conflict") {
			state = ""
		}
		in = store.EndpointReceiptInput{ReceiptRef: e.client.ref("native-receipt", p.Digest+":"+op+":"+key), DeviceRef: e.client.device, PolicyID: p.ID, PolicyRevision: p.Revision, PolicyDigest: p.Digest, Channel: p.Channel, Scope: p.Scope, Executor: p.Executor, Status: status, Code: code, ObservedAt: e.client.now(), Phase: phase, LeaseRef: j.Command.LeaseRef, OperationRef: op, Action: action, TargetScope: p.TargetScope, TargetRefs: p.TargetRefs, ConfigDigest: p.ConfigDigest, PriorStateDigest: prior, StateDigest: state}
		j.Receipts[key] = in
		if e.persist(r) != nil {
			return errors.New("native receipt was not durably recorded")
		}
	}
	if j.Acknowledged[key] {
		return nil
	}
	if e.report(ctx, in) != nil {
		return errors.New("native receipt delivery pending")
	}
	j.Acknowledged[key] = true
	return e.persist(r)
}
func (e *nativeEngine) Handle(ctx context.Context, cmd store.EndpointNativeCommand) error {
	p := cmd.Policy
	if p.NativeLease == nil || p.Scope != "native_control" || p.Mode != "block_all" || p.DeviceRef != e.client.device || p.NativeLease.LeaseRef != cmd.LeaseRef || len(p.TargetRefs) < 1 || len(p.TargetRefs) > 20 {
		return errors.New("approved device-bound native lease required")
	}
	if cmd.Action == "apply" && cmd.OperationRef != p.NativeLease.ApplyOperationRef || cmd.Action == "rollback" && cmd.OperationRef != p.NativeLease.RollbackOperationRef {
		return errors.New("native operation reference mismatch")
	}
	e.mu.Lock()
	if e.entries == nil {
		e.entries = map[string]*nativeRuntime{}
	}
	r := e.entries[cmd.LeaseRef]
	missingJournal := r == nil
	if r == nil {
		r = &nativeRuntime{journal: nativeJournal{Version: 1, Command: cmd, State: "new"}}
		e.entries[cmd.LeaseRef] = r
	}
	e.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if missingJournal && (p.NativeLease.State != "issued" || p.NativeLease.PreparedAt != nil || p.NativeLease.PriorStateDigest != "" || p.NativeLease.PlannedStateDigest != "" || p.NativeLease.AppliedStateDigest != "") {
		// A remote prepared/configured/readback lease cannot establish that a
		// machine with a missing original journal never applied it. Preserve the
		// uncertainty; do not recapture, restore guesses, or emit not_applied.
		r.journal.State = "conflict"
		r.journal.RequiredAction = "本机缺少原状态记录，但平台租约已有准备或应用记录，需管理员人工核对，不能认定从未执行。"
		if e.persist(r) != nil {
			return errors.New("missing native original journal requires administrator review")
		}
		return e.receipt(ctx, r, "conflict", "native_conflict")
	}
	if r.journal.Command.Policy.Digest != p.Digest {
		return errors.New("native lease policy changed")
	}
	if cmd.Action == "rollback" {
		r.journal.Command = cmd
		return e.rollback(ctx, r)
	}
	if cmd.Action != "apply" {
		return errors.New("unsupported native command")
	}
	if p.Status != "approved" || native.TargetScope(p.Executor) != p.TargetScope {
		return errors.New("current approved fixed native target scope required")
	}
	if !p.ExpiresAt.After(e.client.now()) {
		r.journal.Command.Action = "rollback"
		r.journal.Command.OperationRef = p.NativeLease.RollbackOperationRef
		return e.rollback(ctx, r)
	}
	r.deadline = time.Now().Add(p.ExpiresAt.Sub(e.client.now()))
	r.journal.DeadlineLocal = r.deadline
	if r.journal.State == "applying" || r.journal.State == "conflict" {
		r.journal.State = "conflict"
		_ = e.persist(r)
		return e.receipt(ctx, r, "conflict", "native_conflict")
	}
	if r.journal.State == "rolled_back" {
		return nil
	}
	if r.journal.State == "new" {
		for _, ref := range p.TargetRefs {
			before, err := e.driver.Capture(ctx, ref)
			if err != nil {
				return e.fail(ctx, r, err)
			}
			if before.Executor != p.Executor || before.ConfigDigest != p.ConfigDigest || before.TargetRef != ref {
				return e.fail(ctx, r, native.ErrTarget)
			}
			desired, err := e.driver.Expected(before)
			if err != nil {
				return e.fail(ctx, r, err)
			}
			r.journal.Before = append(r.journal.Before, before)
			r.journal.Planned = append(r.journal.Planned, desired)
		}
		r.journal.State = "captured"
		if e.persist(r) != nil {
			return errors.New("native original state not durably recorded")
		}
	}
	if r.journal.State == "captured" {
		if e.receipt(ctx, r, "prepared", "native_prepared") != nil {
			return errors.New("native apply not started: preparation audit unavailable")
		}
		if !r.deadline.After(time.Now()) {
			r.journal.Command.Action = "rollback"
			r.journal.Command.OperationRef = p.NativeLease.RollbackOperationRef
			return e.rollback(ctx, r)
		}
		r.journal.State = "applying"
		if e.persist(r) != nil {
			return errors.New("native apply intent unavailable")
		}
		for _, before := range r.journal.Before {
			// A preceding driver call or its durable save can consume the lease.
			// Do not start another target after TTL or shutdown. There is no
			// uncertain call in flight at this boundary: only the sealed prefix
			// must be restored; the remaining captured targets were never tried.
			if !r.deadline.After(time.Now()) || ctx.Err() != nil {
				r.journal.State = "rollback_pending"
				r.journal.Command.Action = "rollback"
				r.journal.Command.OperationRef = p.NativeLease.RollbackOperationRef
				if e.persist(r) != nil {
					return errors.New("native expiry rollback intent unavailable")
				}
				cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := e.rollback(cleanup, r)
				cancel()
				return err
			}
			applied, err := e.driver.Apply(ctx, before)
			if err != nil {
				return e.fail(ctx, r, err)
			}
			r.journal.Applied = append(r.journal.Applied, applied.Snapshot)
			r.journal.RequiredAction = applied.RequiredAction
			// Each success is sealed before attempting another target. A partial
			// crash never treats a desired snapshot as proof of ownership.
			if e.persist(r) != nil {
				return errors.New("native apply outcome uncertain; preserve journal for review")
			}
		}
		r.journal.State = "applied"
		if e.persist(r) != nil {
			return errors.New("native apply outcome uncertain")
		}
	}
	if r.journal.State == "applied" {
		if !r.deadline.After(time.Now()) {
			r.journal.Command.Action = "rollback"
			r.journal.Command.OperationRef = p.NativeLease.RollbackOperationRef
			return e.rollback(ctx, r)
		}
		if err := e.receipt(ctx, r, "configured", "native_configured"); err != nil {
			return err
		}
		for _, applied := range r.journal.Applied {
			read, err := e.driver.Readback(ctx, applied.TargetRef)
			if err != nil || read.Snapshot.State != applied.State {
				return e.fail(ctx, r, native.ErrConflict)
			}
		}
		return e.receipt(ctx, r, "readback", "native_readback_match")
	}
	return nil
}
func (e *nativeEngine) fail(ctx context.Context, r *nativeRuntime, cause error) error {
	phase, code := "failed", "execution_failed"
	if errors.Is(cause, native.ErrConflict) || r.journal.State == "applying" {
		phase, code = "conflict", "native_conflict"
	}
	r.journal.State = phase
	r.journal.RequiredAction = native.Reason(cause)
	if e.persist(r) != nil {
		return errors.New("native error journal unavailable")
	}
	_ = e.receipt(ctx, r, phase, code)
	return cause
}
func (e *nativeEngine) rollback(ctx context.Context, r *nativeRuntime) error {
	if r.journal.State == "applying" || r.journal.State == "conflict" {
		// Known completed prefix operations can still be restored. An uncertain
		// target without an applied snapshot is never inferred from a matching
		// paused bit or DWORD, and the overall lease remains a conflict.
		for i := len(r.journal.Applied) - 1; i >= 0; i-- {
			_, _ = e.driver.Restore(ctx, r.journal.Before[i], r.journal.Applied[i])
		}
		return e.receipt(ctx, r, "conflict", "native_conflict")
	}
	if r.journal.State == "rolled_back" {
		if len(r.journal.Applied) == 0 {
			return e.receipt(ctx, r, "rolled_back", "native_not_applied")
		}
		return e.receipt(ctx, r, "rolled_back", "native_rolled_back")
	}
	if len(r.journal.Applied) == 0 {
		// No local apply began in a new/captured record. Never use this path for
		// applying/conflict or a partially sealed native operation.
		r.journal.State = "rolled_back"
		if e.persist(r) != nil {
			return errors.New("native rollback journal unavailable")
		}
		return e.receipt(ctx, r, "rolled_back", "native_not_applied")
	}
	for i := len(r.journal.Applied) - 1; i >= 0; i-- {
		if _, err := e.driver.Restore(ctx, r.journal.Before[i], r.journal.Applied[i]); err != nil {
			return e.fail(ctx, r, err)
		}
	}
	r.journal.State = "rolled_back"
	if e.persist(r) != nil {
		return errors.New("native restore outcome journal unavailable")
	}
	return e.receipt(ctx, r, "rolled_back", "native_rolled_back")
}
func (e *nativeEngine) Expire(ctx context.Context, all bool) {
	e.mu.Lock()
	entries := make([]*nativeRuntime, 0, len(e.entries))
	for _, r := range e.entries {
		entries = append(entries, r)
	}
	e.mu.Unlock()
	for len(entries) > 0 {
		var busy []*nativeRuntime
		for _, r := range entries {
			if ctx.Err() != nil {
				return
			}
			if !r.mu.TryLock() {
				busy = append(busy, r)
				continue
			}
			if all || r.deadline.IsZero() || !r.deadline.After(time.Now()) {
				r.journal.Command.Action = "rollback"
				r.journal.Command.OperationRef = r.journal.Command.Policy.NativeLease.RollbackOperationRef
				_ = e.rollback(ctx, r)
			}
			r.mu.Unlock()
		}
		if !all || len(busy) == 0 {
			return
		}
		// Stop must give an in-flight handler a chance to seal its known outcome
		// and then restore it, rather than silently skipping the locked lease.
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		entries = busy
	}
}
