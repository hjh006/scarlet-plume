package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func TestDataSecurityActivityAuditDoesNotCreateOrClearRisk(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	hash := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	source, e := f.repo.CreateOperationsSource(ctx, f.ws.ID, f.user.ID, OperationsSourceInput{Name: "activity fixture", Kind: "security_events", OwnerTeam: "Data"}, hash(f.ws.Slug))
	must(e)
	target, e := f.repo.SaveClassificationDeliveryTarget(ctx, f.ws.ID, f.user.ID, 0, ClassificationDeliveryTargetInput{Name: "synthetic receiver", Endpoint: "https://receiver.example/events", Enabled: true, AutoEvents: true, MinSeverity: "low"}, hash("target"+f.ws.Slug), false)
	must(e)
	var now time.Time
	must(f.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now))
	input := SecurityEventInput{Outcome: "activity", EventID: "normal-copy", ObservedAt: now, Category: "access", Rule: "guard.copy", Severity: "critical", Count: 1, WindowSeconds: 1, DataContext: &datasafety.Context{Kind: "endpoint", Channel: "file_copy", SubjectRef: strings.Repeat("a", 64), DeviceRef: strings.Repeat("b", 64), Operation: "copy", Enforcement: "allowed", Capture: "complete", Categories: []string{"phone"}}}
	activity, replayed, e := f.repo.ReceiveSecurityEvent(ctx, hash(f.ws.Slug), input)
	must(e)
	if replayed || activity.ID == 0 || activity.AssetID == 0 || activity.CheckID != 0 || activity.CheckStatus != "" || activity.DataContext == nil {
		t.Fatal("normal activity not retained without risk", activity)
	}
	var checks, events int
	must(f.pool.QueryRow(ctx, `SELECT count(*) FROM plume.checks WHERE workspace_id=$1`, f.ws.ID).Scan(&checks))
	must(f.pool.QueryRow(ctx, `SELECT count(*) FROM plume.operations_security_events WHERE workspace_id=$1`, f.ws.ID).Scan(&events))
	if checks != 0 || events != 1 {
		t.Fatal("activity created risk or missing audit", checks, events)
	}
	var nullCheck bool
	must(f.pool.QueryRow(ctx, `SELECT check_id IS NULL FROM plume.operations_security_events WHERE id=$1`, activity.ID).Scan(&nullCheck))
	if !nullCheck {
		t.Fatal("activity check is not NULL")
	}
	again, replayed, e := f.repo.ReceiveSecurityEvent(ctx, hash(f.ws.Slug), input)
	must(e)
	if !replayed || again.ID != activity.ID || again.AssetID != activity.AssetID || again.CheckID != 0 || again.CheckStatus != "" {
		t.Fatal("activity replay lost nullable check or resolved asset", again)
	}
	changed := input
	changed.Count = 2
	if _, _, e = f.repo.ReceiveSecurityEvent(ctx, hash(f.ws.Slug), changed); !errors.Is(e, ErrConflict) {
		t.Fatal("mutated activity replay accepted", e)
	}
	deliveries, e := f.repo.ClassificationDeliveries(ctx, f.ws.ID, f.user.ID, 0, 0)
	must(e)
	if len(deliveries) != 0 {
		t.Fatal("high severity activity triggered automatic risk delivery")
	}
	manual, e := f.repo.CreateSecurityEventDelivery(ctx, f.ws.ID, f.user.ID, activity.ID, target.ID)
	must(e)
	if manual.ResourceKind != "security_event" || manual.ResourceID != activity.ID {
		t.Fatal("manual activity evidence delivery missing")
	}
	overview, e := f.repo.OperationsOverview(ctx, f.ws.ID, f.user.ID)
	must(e)
	if overview.Counts.OpenEvents != 0 || overview.Counts.Events != 1 {
		t.Fatal("activity counted as open risk", overview.Counts)
	}
	page, e := f.repo.ListOperationsSecurityEvents(ctx, f.ws.ID, f.user.ID)
	must(e)
	if len(page) != 1 || page[0].CheckID != 0 || page[0].CheckStatus != "" {
		t.Fatal("nullable activity dropped by events list", page)
	}
	scope := DataSecurityScope{SourceID: source.ID, SubjectRef: input.DataContext.SubjectRef, From: now.Add(-time.Minute), To: now.Add(time.Second)}
	window, e := f.repo.DataSecurityEvents(ctx, f.ws.ID, f.user.ID, scope)
	must(e)
	if window.Total != 1 || len(window.Items) != 1 || window.Items[0].CheckID != 0 {
		t.Fatal("activity dropped by source window", window)
	}
	c, e := f.repo.CreateDataSecurityCase(ctx, f.ws.ID, f.user.ID, "normal audit evidence", scope)
	must(e)
	if len(c.Snapshot.Window.Items) != 1 || c.Snapshot.Window.Items[0].Outcome != "activity" || c.SnapshotDigest == "" {
		t.Fatal("case did not freeze activity evidence", c)
	}
	risk := input
	risk.Outcome = "observed"
	risk.EventID = "actual-risk"
	risk.ObservedAt = now.Add(time.Microsecond)
	risk.AssetID = activity.AssetID
	observed, _, e := f.repo.ReceiveSecurityEvent(ctx, hash(f.ws.Slug), risk)
	must(e)
	if observed.CheckID == 0 || observed.CheckStatus != "draft" {
		t.Fatal("old risk branch lost compatibility")
	}
	later := input
	later.EventID = "later-normal-copy"
	later.ObservedAt = now.Add(2 * time.Microsecond)
	later.AssetID = activity.AssetID
	_, _, e = f.repo.ReceiveSecurityEvent(ctx, hash(f.ws.Slug), later)
	must(e)
	detail, e := f.repo.CheckDetail(ctx, f.ws.ID, f.user.ID, observed.CheckID)
	must(e)
	if detail.Status != "draft" || len(detail.Evidence) != 1 || detail.Evidence[0].Kind != "observation" {
		t.Fatal("normal activity mutated risk evidence/state", detail)
	}
	cleared := risk
	cleared.Outcome = "cleared"
	cleared.Count = 0
	cleared.EventID = "unearned-clear"
	cleared.ObservedAt = now.Add(3 * time.Microsecond)
	if _, _, e = f.repo.ReceiveSecurityEvent(ctx, hash(f.ws.Slug), cleared); !errors.Is(e, ErrEvidenceRequired) {
		t.Fatal("normal activity substituted for remediation/clear", e)
	}
	overview, e = f.repo.OperationsOverview(ctx, f.ws.ID, f.user.ID)
	must(e)
	if overview.Counts.OpenEvents != 1 || overview.Counts.Events != 3 {
		t.Fatal("activity distorted risk queue", overview.Counts)
	}
	deliveries, e = f.repo.ClassificationDeliveries(ctx, f.ws.ID, f.user.ID, 0, 0)
	must(e)
	if len(deliveries) != 2 {
		t.Fatal("activity auto-delivered or observed risk not auto-delivered", len(deliveries))
	}
}
func TestDataSecurityActivityValidationAndTenantBoundaries(t *testing.T) {
	f := assessmentSourceTestStore(t)
	foreign := assessmentSourceTestStore(t)
	ctx := t.Context()
	hash := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	source, e := f.repo.CreateOperationsSource(ctx, f.ws.ID, f.user.ID, OperationsSourceInput{Name: "activity fixture", Kind: "security_events", OwnerTeam: "Data"}, hash(f.ws.Slug))
	if e != nil {
		t.Fatal(e)
	}
	foreignAsset, e := foreign.repo.CreateAsset(ctx, foreign.ws.ID, "foreign activity asset", "fixture", "Data")
	if e != nil {
		t.Fatal(e)
	}
	makeInput := func() SecurityEventInput {
		return SecurityEventInput{Outcome: "activity", EventID: "normal-audit", ObservedAt: time.Now(), Category: "config", Rule: "fim.change", Severity: "low", Count: 1, WindowSeconds: 1, DataContext: &datasafety.Context{Kind: "endpoint", Channel: "file_change", Operation: "change", Enforcement: "observed", Capture: "partial", Categories: []string{}}}
	}
	for _, mutate := range []func(*SecurityEventInput){func(i *SecurityEventInput) { i.DataContext = nil }, func(i *SecurityEventInput) { i.DataContext.Enforcement = "blocked" }, func(i *SecurityEventInput) { i.DataContext.Enforcement = "failed" }, func(i *SecurityEventInput) { i.DataContext.Enforcement = "unknown" }, func(i *SecurityEventInput) { i.Count = 0 }} {
		i := makeInput()
		mutate(&i)
		if _, _, e = f.repo.ReceiveSecurityEvent(ctx, hash(f.ws.Slug), i); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid activity accepted", e)
		}
	}
	input := makeInput()
	input.AssetID = foreignAsset.ID
	if _, _, e = f.repo.ReceiveSecurityEvent(ctx, hash(f.ws.Slug), input); !errors.Is(e, ErrNotFound) {
		t.Fatal("cross-tenant asset activity accepted", e)
	}
	input = makeInput()
	saved, _, e := f.repo.ReceiveSecurityEvent(ctx, hash(f.ws.Slug), input)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ListOperationsSecurityEvents(ctx, f.ws.ID, foreign.user.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("foreign actor read activity", e)
	}
	scope := DataSecurityScope{SourceID: source.ID, From: input.ObservedAt.Add(-time.Minute), To: time.Now().Add(time.Second)}
	if _, e = f.repo.DataSecurityEvents(ctx, f.ws.ID, foreign.user.ID, scope); !errors.Is(e, ErrNotFound) {
		t.Fatal("foreign actor read source window", e)
	}
	if _, e = f.repo.CreateDataSecurityCase(ctx, f.ws.ID, foreign.user.ID, "foreign case", scope); !errors.Is(e, ErrNotFound) {
		t.Fatal("foreign actor froze activity", e)
	}
	cleared := input
	cleared.Outcome = "cleared"
	cleared.Count = 0
	cleared.EventID = "normal-does-not-clear"
	cleared.ObservedAt = cleared.ObservedAt.Add(time.Microsecond)
	cleared.AssetID = saved.AssetID
	if _, _, e = f.repo.ReceiveSecurityEvent(ctx, hash(f.ws.Slug), cleared); !errors.Is(e, ErrEvidenceRequired) {
		t.Fatal("activity treated as observed risk for clear", e)
	}
}
