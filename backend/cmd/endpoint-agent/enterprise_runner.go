package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	native "github.com/hjh006/scarlet-plume/backend/internal/endpointnativecontrol"
	"github.com/hjh006/scarlet-plume/backend/internal/endpointsecure"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

type nativeManagers map[string]*native.Manager

func (d nativeManagers) Capture(ctx context.Context, ref string) (native.Snapshot, error) {
	m := d[ref]
	if m == nil {
		return native.Snapshot{}, native.ErrTarget
	}
	return m.Capture(ctx, ref)
}
func (d nativeManagers) Expected(s native.Snapshot) (native.Snapshot, error) {
	return native.Expected(s)
}
func (d nativeManagers) Apply(ctx context.Context, s native.Snapshot) (native.Readback, error) {
	m := d[s.TargetRef]
	if m == nil {
		return native.Readback{}, native.ErrTarget
	}
	return m.Apply(ctx, s)
}
func (d nativeManagers) Readback(ctx context.Context, ref string) (native.Readback, error) {
	m := d[ref]
	if m == nil {
		return native.Readback{}, native.ErrTarget
	}
	return m.Readback(ctx, ref)
}
func (d nativeManagers) Restore(ctx context.Context, before, after native.Snapshot) (native.Readback, error) {
	m := d[before.TargetRef]
	if m == nil {
		return native.Readback{}, native.ErrTarget
	}
	return m.Restore(ctx, before, after)
}

func enterpriseNative(ctx context.Context, dir, mode string) error {
	private, err := endpointsecure.Open(dir)
	if err != nil {
		return err
	}
	defer private.Close()
	cfg, err := loadEnterprise(private)
	if err != nil {
		return err
	}
	client, err := cfg.client()
	if err != nil {
		return err
	}
	client.nativeService = true
	targets, err := cfg.targets(client)
	if err != nil {
		return err
	}
	drivers := nativeManagers{}
	for _, target := range targets {
		m, err := native.New([]native.Target{target})
		if err != nil {
			return err
		}
		drivers[target.TargetRef] = m
	}
	client.nativeCapabilities = func() []channelCapability { return nativeCapabilityProjection(ctx, targets, drivers) }
	if mode == "native-plan" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"deviceRef": client.device, "privilege": enterprisePrivilege(), "capabilities": client.nativeCapabilities(), "functionalVerified": false})
	}
	var privateMu sync.Mutex
	engine := &nativeEngine{client: client, driver: drivers, save: func(j nativeJournal) error {
		raw, err := json.Marshal(j)
		if err != nil {
			return errors.New("native journal encoding failed")
		}
		defer clear(raw)
		privateMu.Lock()
		defer privateMu.Unlock()
		return private.Write("lease-"+j.Command.LeaseRef+".bin", raw)
	}, report: func(ctx context.Context, r store.EndpointReceiptInput) error {
		return client.request(ctx, "POST", "/api/v1/endpoint/receipts", r, nil)
	}}
	names, err := private.Names()
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "lease-") {
			continue
		}
		raw, err := private.Read(name)
		if err != nil {
			return errors.New("protected native journal unavailable")
		}
		var j nativeJournal
		err = json.Unmarshal(raw, &j)
		clear(raw)
		if err != nil || name != "lease-"+j.Command.LeaseRef+".bin" {
			return errors.New("protected native journal binding invalid")
		}
		if err = engine.load(j); err != nil {
			return err
		}
	}
	if mode == "native-rollback" {
		cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		engine.Expire(cleanup, true)
		return engine.ensureRestored()
	}
	if mode == "native-replay" {
		return engine.replayReceipts(ctx)
	}
	if mode != "native-run" {
		return errors.New("unsupported enterprise runner mode")
	}
	// Network refresh never holds the lease map lock. Independent expiry ticks
	// can restore other completed leases while a request/driver is slow.
	bundles := make(chan policyBundle, 1)
	rejected := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			err := client.heartbeat(ctx)
			if err == nil {
				var b policyBundle
				b, err = client.nativeBundle(ctx)
				if err == nil {
					select {
					case bundles <- b:
					default:
					}
				}
			}
			if sourceAuthorizationRejected(err) {
				select {
				case rejected <- struct{}{}:
				default:
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	expiry := time.NewTicker(time.Second)
	defer expiry.Stop()
	startup := time.NewTimer(8 * time.Second)
	defer startup.Stop()
	ready := false
	var inflight sync.Map
	for {
		select {
		case <-ctx.Done():
			// Do not remove private originals on stop. A conflict remains visible
			// for administrator review and prevents destructive uninstall.
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			engine.Expire(cleanup, true)
			cancel()
			return engine.ensureRestored()
		case b := <-bundles:
			engine.refreshDeadlines(b.NativeCommands)
			ready = true
			for _, command := range b.NativeCommands {
				cmd := command
				if _, busy := inflight.LoadOrStore(cmd.LeaseRef, true); busy {
					continue
				}
				go func() { defer inflight.Delete(cmd.LeaseRef); _ = engine.Handle(ctx, cmd) }()
			}
		case <-rejected:
			// Explicit authorization loss is different from an offline network:
			// immediately restore owned state and retain any unsent receipts.
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			engine.Expire(cleanup, true)
			cancel()
			if err := engine.ensureRestored(); err != nil {
				return err
			}
			return errors.New("source authorization revoked; owned native state restored, protected evidence retained")
		case <-startup.C:
			ready = true // no authenticated refresh: restore loaded state
		case <-expiry.C:
			if ready {
				engine.Expire(ctx, false)
			}
		}
	}
}

func (e *nativeEngine) refreshDeadlines(commands []store.EndpointNativeCommand) {
	for _, cmd := range commands {
		if cmd.Action != "apply" || cmd.Policy.Status != "approved" || !cmd.Policy.ExpiresAt.After(e.client.now()) {
			continue
		}
		e.mu.Lock()
		r := e.entries[cmd.LeaseRef]
		e.mu.Unlock()
		if r == nil || !r.mu.TryLock() {
			continue
		}
		if r.journal.Command.Policy.Digest == cmd.Policy.Digest {
			r.deadline = time.Now().Add(cmd.Policy.ExpiresAt.Sub(e.client.now()))
			r.journal.DeadlineLocal = r.deadline
			_ = e.persist(r)
		}
		r.mu.Unlock()
	}
}
func (e *nativeEngine) replayReceipts(ctx context.Context) error {
	e.mu.Lock()
	entries := make([]*nativeRuntime, 0, len(e.entries))
	for _, r := range e.entries {
		entries = append(entries, r)
	}
	e.mu.Unlock()
	for _, r := range entries {
		r.mu.Lock()
		keys := make([]string, 0, len(r.journal.Receipts))
		for key := range r.journal.Receipts {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := r.journal.Receipts[keys[i]], r.journal.Receipts[keys[j]]
			if !a.ObservedAt.Equal(b.ObservedAt) {
				return a.ObservedAt.Before(b.ObservedAt)
			}
			rank := map[string]int{"prepared": 1, "configured": 2, "readback": 3, "functional_verified": 4, "failed": 5, "conflict": 6, "rolled_back": 7}
			return rank[a.Phase] < rank[b.Phase]
		})
		for _, key := range keys {
			receipt := r.journal.Receipts[key]
			if !r.journal.Acknowledged[key] {
				if e.report(ctx, receipt) != nil {
					r.mu.Unlock()
					return errors.New("protected native receipt delivery pending")
				}
				r.journal.Acknowledged[key] = true
				if e.persist(r) != nil {
					r.mu.Unlock()
					return errors.New("native receipt acknowledgement pending")
				}
			}
		}
		r.mu.Unlock()
	}
	return nil
}
func (e *nativeEngine) ensureRestored() error {
	e.mu.Lock()
	entries := make([]*nativeRuntime, 0, len(e.entries))
	for _, r := range e.entries {
		entries = append(entries, r)
	}
	e.mu.Unlock()
	for _, r := range entries {
		if !r.mu.TryLock() {
			return errors.New("native operation still in progress; preserve service and journal")
		}
		state := r.journal.State
		r.mu.Unlock()
		if state != "rolled_back" {
			return errors.New("native rollback incomplete or conflict; preserve protected originals for administrator review")
		}
	}
	return nil
}
func nativeCapabilityProjection(ctx context.Context, targets []native.Target, drivers nativeManagers) []channelCapability {
	groups := map[string][]native.Target{}
	for _, t := range targets {
		groups[t.Executor] = append(groups[t.Executor], t)
	}
	var caps []channelCapability
	privilege := enterprisePrivilege()
	for executor, items := range groups {
		channel := map[string]string{native.USBWrite: "usb", native.PrintPause: "print", native.RDPClipboard: "clipboard"}[executor]
		cap := channelCapability{Channel: channel, Scope: "native_control", Supported: privilege == "administrator" || privilege == "system", Modes: []string{"block_all"}, Executor: executor, Reason: "configuration_controller_only_not_functional_proof", TargetScope: native.TargetScope(executor), ConfigDigest: items[0].ConfigDigest, Privilege: privilege}
		for _, target := range items {
			cap.TargetRefs = append(cap.TargetRefs, target.TargetRef)
			if _, err := drivers[target.TargetRef].Capture(ctx, target.TargetRef); err != nil {
				cap.Supported = false
				cap.Reason = native.Reason(err)
			}
		}
		if !cap.Supported {
			cap.Modes = []string{}
		}
		sort.Strings(cap.TargetRefs)
		caps = append(caps, cap)
	}
	sort.Slice(caps, func(i, j int) bool { return caps[i].Channel < caps[j].Channel })
	return caps
}
func (c *endpointClient) nativeBundle(ctx context.Context) (policyBundle, error) {
	var b policyBundle
	if err := c.request(ctx, "GET", "/api/v1/endpoint/policy?deviceRef="+c.device, nil, &b); err != nil {
		return b, err
	}
	received := time.Now()
	lower, upper := received.Add(-5*time.Minute), received.Add(5*time.Minute)
	if b.DeviceRef != c.device || b.IssuedAt.Before(lower) || b.IssuedAt.After(upper) || len(b.NativeCommands) > 100 {
		return b, errors.New("invalid bounded native policy bundle")
	}
	base := b.IssuedAt
	for _, cmd := range b.NativeCommands {
		if cmd.Policy.ApprovedAt != nil && cmd.Policy.ApprovedAt.After(base) {
			base = *cmd.Policy.ApprovedAt
		}
	}
	if base.After(upper) {
		return b, errors.New("invalid native platform time")
	}
	c.clockMu.Lock()
	c.platformTime, c.receivedAt = base, received
	c.clockMu.Unlock()
	return b, nil
}
