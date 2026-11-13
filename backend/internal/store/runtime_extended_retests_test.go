package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

type extendedRetestFixture struct {
	assessmentSourceFixture
	policy                        RuntimeDetectionPolicy
	source                        RuntimeSource
	token                         string
	reviewer                      User
	checkID                       int64
	base, remediated, anchor, end time.Time
	good                          runtimesecurity.Observation
	sequence                      int
}

func runtimeExtendedFixture(t *testing.T, rule string) *extendedRetestFixture {
	t.Helper()
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	name := f.ws.Slug + "-reviewer"
	must(f.repo.ProvisionReviewer(ctx, f.ws.Slug, name, "unused"))
	reviewer, e := f.repo.FindUser(ctx, name)
	must(e)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = f.pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE id=$1`, f.ws.ID)
		_, _ = f.pool.Exec(cleanup, `DELETE FROM plume.users WHERE id=$1`, reviewer.ID)
	})
	token := fmt.Sprintf("%x", sha256.Sum256([]byte(f.ws.Slug)))
	source, e := f.repo.CreateRuntimeSource(ctx, f.ws.ID, f.user.ID, "extended fixture", "extended-app", "gateway", token)
	must(e)
	var p RuntimeDetectionPolicy
	for _, v := range defaultRuntimeDetectionPolicies() {
		if v.Rule == rule {
			p = v
		}
	}
	p.Enabled = true
	p.SourceID = source.ID
	p.ScopeKey = "automatic-retest"
	p.Method = "POST"
	p.Route = "/extended/" + rule
	p.WindowMinutes = 1
	p.MinRequests = 1
	p.Threshold = 1
	switch rule {
	case "over_collection":
		p.Direction = "request"
		p.AllowlistConfigured = true
		p.AllowedCategories = []string{"phone"}
	case "subject_multi_ip", "subject_multi_region", "subject_repeated_access":
		p.Threshold = 2
	case "client_region_mismatch":
		p.AllowedClientCountries = []string{"CN"}
	case "recipient_scope":
		p.AllowedRecipients = []string{"registered"}
	case "destination_scope":
		p.AllowedCountries = []string{"CN"}
		p.AllowedDomains = []string{"registered.example"}
	case "volume_limit":
		p.MaxVolume = 100
		p.VolumeUnit = "records"
	}
	p, e = f.repo.SaveRuntimeDetectionPolicy(ctx, f.ws.ID, f.user.ID, p)
	must(e)
	var now time.Time
	must(f.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now))
	base := now.Add(-5 * time.Minute).Truncate(time.Microsecond)
	_, e = f.pool.Exec(ctx, `UPDATE plume.runtime_detection_policies SET created_at=$2 WHERE id=$1`, p.ID, base.Add(-time.Minute))
	must(e)
	volume := int64(20)
	good := runtimesecurity.Observation{Method: "POST", Route: p.Route, Status: 200, ObservedAt: base, SubjectRef: strings.Repeat("a", 64), ClientIP: "192.0.2.1", Transport: "https", TransportHop: "proxy_upstream", InspectedLocations: []string{"request", "response"}, Context: &runtimesecurity.RuntimeContext{ClientCountry: "CN", DestinationCountry: "CN", DestinationDomain: "registered.example", Recipient: "registered", Volume: &volume, VolumeUnit: "records"}, DataFields: []runtimesecurity.DataField{{Name: "phone", Location: "request", Category: "phone", Method: "format", Count: 1, Observed: 1}}}
	bad := good
	bad.Context = runtimeRetestCloneContext(good.Context)
	positives := []runtimesecurity.Observation{bad}
	switch rule {
	case "over_collection":
		bad.DataFields = []runtimesecurity.DataField{{Name: "email", Location: "request", Category: "email", Method: "format", Count: 1, Observed: 1}}
	case "credential_cleartext_transport":
		bad.Transport = "http"
		bad.DataFields = []runtimesecurity.DataField{{Name: "password", Location: "request", Category: "credential", Method: "metadata", Count: 1, Observed: 1}}
		good.DataFields = append([]runtimesecurity.DataField(nil), bad.DataFields...)
	case "subject_multi_ip":
		other := bad
		other.ClientIP = "192.0.2.2"
		positives = append(positives, other)
	case "subject_multi_region":
		other := bad
		other.Context = runtimeRetestCloneContext(bad.Context)
		other.Context.ClientCountry = "US"
		positives = append(positives, other)
	case "subject_repeated_access":
		positives = append(positives, bad)
	case "client_region_mismatch":
		bad.Context.ClientCountry = "US"
	case "recipient_scope":
		bad.Context.Recipient = "unregistered"
	case "destination_scope":
		bad.Context.DestinationCountry = "US"
		bad.Context.DestinationDomain = "external.example"
	case "volume_limit":
		v := int64(200)
		bad.Context.Volume = &v
	}
	positives[0] = bad
	receipt, e := f.repo.DeliverRuntimeBatch(ctx, "gateway", token, fmt.Sprintf("%x", sha256.Sum256([]byte("opening"))), positives)
	must(e)
	if receipt.Accepted != len(positives) {
		t.Fatal(receipt)
	}
	findings, e := f.repo.ListRuntimeFindings(ctx, f.ws.ID)
	must(e)
	var checkID int64
	for _, item := range findings {
		if item.Rule == rule && item.SourceID == source.ID {
			checkID = item.CheckID
		}
	}
	if checkID == 0 {
		t.Fatalf("no positive %s", rule)
	}
	_, e = f.pool.Exec(ctx, `UPDATE plume.checks SET created_at=$2 WHERE id=$1`, checkID, base.Add(time.Second))
	must(e)
	return &extendedRetestFixture{assessmentSourceFixture: f, policy: p, source: source, token: token, reviewer: reviewer, checkID: checkID, base: base, remediated: base.Add(10 * time.Second), anchor: base.Add(70 * time.Second), end: base.Add(131 * time.Second), good: good}
}
func runtimeRetestCloneContext(c *runtimesecurity.RuntimeContext) *runtimesecurity.RuntimeContext {
	copy := *c
	if c.Volume != nil {
		n := *c.Volume
		copy.Volume = &n
	}
	return &copy
}
func (f *extendedRetestFixture) detail(t *testing.T, actor int64) CheckDetail {
	t.Helper()
	d, e := f.repo.CheckDetail(t.Context(), f.ws.ID, actor, f.checkID)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func (f *extendedRetestFixture) deliver(t *testing.T, at time.Time, events ...runtimesecurity.Observation) {
	t.Helper()
	f.sequence++
	for i := range events {
		events[i].ObservedAt = at
	}
	_, e := f.repo.DeliverRuntimeBatch(t.Context(), "gateway", f.token, fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(f.sequence)))), events)
	if e != nil {
		t.Fatal(e)
	}
}
func (f *extendedRetestFixture) remediate(t *testing.T) {
	t.Helper()
	e := f.repo.AddEvidence(t.Context(), f.ws.ID, f.user.ID, f.checkID, EvidenceInput{Kind: "remediation", SourceSystem: "synthetic fixture", SourceRecord: "fixture:fix", Summary: "Actual fixture correction registered", ObservedAt: f.remediated})
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.pool.Exec(t.Context(), `UPDATE plume.check_evidence SET created_at=$2 WHERE check_id=$1 AND kind='remediation'`, f.checkID, f.remediated)
	if e != nil {
		t.Fatal(e)
	}
}
func (f *extendedRetestFixture) pass(t *testing.T) {
	t.Helper()
	f.remediate(t)
	f.deliver(t, f.anchor, f.good)
	f.deliver(t, f.end, f.good)
	d := f.detail(t, f.reviewer.ID)
	if d.Status != "in_review" || d.RuntimeRetest == nil || d.RuntimeRetest.State != "passed" || !d.CanReview || !d.CanVerify {
		t.Fatalf("auto retest did not enter independent review: %+v", d)
	}
}
func TestExtendedRuntimeNineRulesDraftAutoRetestAndIndependentReview(t *testing.T) {
	for _, rule := range extendedRuntimeRules {
		t.Run(rule, func(t *testing.T) {
			f := runtimeExtendedFixture(t, rule)
			d := f.detail(t, f.user.ID)
			if d.Status != "draft" || d.CanSubmit || d.CanReview || d.RuntimeRetest == nil || d.RuntimeRetest.State != "awaiting_remediation" {
				t.Fatal("draft observation bypassed retest", d)
			}
			if e := f.repo.SubmitCheck(t.Context(), f.ws.ID, f.user.ID, f.checkID); !errors.Is(e, ErrEvidenceRequired) {
				t.Fatal("draft submitted without auto retest", e)
			}
			f.pass(t)
			owner := f.detail(t, f.user.ID)
			if owner.CanReview || owner.CanVerify {
				t.Fatal("creator gained independent review")
			}
			d = f.detail(t, f.reviewer.ID)
			if e := f.repo.ReviewCheck(t.Context(), f.ws.ID, f.reviewer.ID, f.checkID, d.ReviewRevision, "verified", "Reviewed complete scoped technical evidence"); e != nil {
				t.Fatal(e)
			}
			d = f.detail(t, f.reviewer.ID)
			if d.Status != "verified" || d.RuntimeRetest.State != "verified" {
				t.Fatal("independent result missing", d)
			}
		})
	}
}
func TestExtendedRuntimeRetestInvalidationAndUnknownEvidence(t *testing.T) {
	t.Run("new positive policy and source", func(t *testing.T) {
		f := runtimeExtendedFixture(t, "over_collection")
		f.pass(t)
		bad := f.good
		bad.DataFields = []runtimesecurity.DataField{{Name: "email", Location: "request", Category: "email", Method: "format", Count: 1, Observed: 1}}
		f.deliver(t, f.end.Add(time.Second), bad)
		d := f.detail(t, f.reviewer.ID)
		if d.CanVerify || d.CanReview || d.Status != "needs_work" {
			t.Fatal("new positive did not invalidate old proof", d)
		}
	})
	t.Run("policy revision", func(t *testing.T) {
		f := runtimeExtendedFixture(t, "recipient_scope")
		f.pass(t)
		p := f.policy
		p.Enabled = false
		if _, e := f.repo.SaveRuntimeDetectionPolicy(t.Context(), f.ws.ID, f.user.ID, p); e != nil {
			t.Fatal(e)
		}
		d := f.detail(t, f.reviewer.ID)
		if d.Status != "needs_work" || d.CanVerify || d.RuntimeRetest.State != "invalidated" {
			t.Fatal("paused policy retained review", d)
		}
	})
	t.Run("source revoked", func(t *testing.T) {
		f := runtimeExtendedFixture(t, "volume_limit")
		f.pass(t)
		if e := f.repo.RevokeRuntimeSource(t.Context(), f.ws.ID, f.user.ID, f.source.ID); e != nil {
			t.Fatal(e)
		}
		d := f.detail(t, f.reviewer.ID)
		if d.Status != "needs_work" || d.CanVerify || d.RuntimeRetest.State != "invalidated" {
			t.Fatal("revoked source retained review", d)
		}
	})
	t.Run("source creator demoted", func(t *testing.T) {
		f := runtimeExtendedFixture(t, "destination_scope")
		f.pass(t)
		if _, e := f.pool.Exec(t.Context(), `UPDATE plume.memberships SET role='operator' WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, f.user.ID); e != nil {
			t.Fatal(e)
		}
		d := f.detail(t, f.reviewer.ID)
		if d.CanVerify || d.CanReview || d.RuntimeRetest.State != "invalidated" {
			t.Fatal("demoted source retained review", d)
		}
	})
	t.Run("unknown capture and substitute identity", func(t *testing.T) {
		f := runtimeExtendedFixture(t, "over_collection")
		f.remediate(t)
		f.deliver(t, f.anchor, f.good)
		unknown := f.good
		unknown.InspectedLocations = nil
		f.deliver(t, f.end, unknown)
		d := f.detail(t, f.reviewer.ID)
		if d.CanVerify || d.Status != "draft" {
			t.Fatal("unknown capture passed", d)
		}
		other := f.good
		other.SubjectRef = strings.Repeat("b", 64)
		other.ClientIP = "192.0.2.2"
		f.deliver(t, f.end.Add(time.Second), other)
		d = f.detail(t, f.reviewer.ID)
		if d.CanVerify || d.Status != "draft" {
			t.Fatal("other identity cleared original finding", d)
		}
	})
}

func TestExtendedRuntimeAllTriggeringSubjectsAndDelayedScope(t *testing.T) {
	t.Run("same timestamp subjects before remediation", func(t *testing.T) {
		f := runtimeExtendedFixture(t, "subject_multi_ip")
		b := f.good
		b.SubjectRef = strings.Repeat("b", 64)
		b2 := b
		b2.ClientIP = "192.0.2.2"
		f.deliver(t, f.base, b, b2)
		f.remediate(t)
		f.deliver(t, f.anchor, f.good)
		f.deliver(t, f.end, f.good)
		if d := f.detail(t, f.reviewer.ID); d.Status != "draft" || d.CanVerify {
			t.Fatal("last/quiet subject cleared another triggering subject", d)
		}
		f.deliver(t, f.end.Add(time.Microsecond), b)
		if d := f.detail(t, f.reviewer.ID); d.Status != "in_review" || !d.CanVerify {
			t.Fatal("all triggering subjects did not qualify", d)
		}
	})
	for _, delta := range []time.Duration{0, -time.Second} {
		t.Run(fmt.Sprint("delayed subject ", delta), func(t *testing.T) {
			f := runtimeExtendedFixture(t, "subject_multi_ip")
			f.pass(t)
			old := f.detail(t, f.reviewer.ID)
			b := f.good
			b.SubjectRef = strings.Repeat("b", 64)
			b2 := b
			b2.ClientIP = "192.0.2.2"
			f.deliver(t, f.base.Add(delta), b, b2)
			d := f.detail(t, f.reviewer.ID)
			if d.Status != "needs_work" || d.CanVerify || d.RuntimeRetest.State != "invalidated" {
				t.Fatal("late new scope kept old proof valid", d)
			}
			if e := f.repo.ReviewCheck(t.Context(), f.ws.ID, f.reviewer.ID, f.checkID, old.ReviewRevision, "verified", "stale review"); !errors.Is(e, ErrConflict) {
				t.Fatal("stale old review accepted after new scope", e)
			}
		})
	}
}
func TestExtendedRuntimeRetestReplayAndResubmissionRevision(t *testing.T) {
	f := runtimeExtendedFixture(t, "over_collection")
	f.pass(t)
	first := f.detail(t, f.reviewer.ID)
	event := f.good
	event.ObservedAt = f.end
	batch := fmt.Sprintf("%x", sha256.Sum256([]byte("replayed-negative")))
	for range 2 {
		if _, e := f.repo.DeliverRuntimeBatch(t.Context(), "gateway", f.token, batch, []runtimesecurity.Observation{event}); e != nil {
			t.Fatal(e)
		}
	}
	var submissions int
	if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM plume.check_events WHERE check_id=$1 AND action='submitted'`, f.checkID).Scan(&submissions); e != nil {
		t.Fatal(e)
	}
	if submissions != 1 {
		t.Fatal("replay generated duplicate automatic submission", submissions)
	}
	if e := f.repo.ReviewCheck(t.Context(), f.ws.ID, f.reviewer.ID, f.checkID, first.ReviewRevision, "changes", "Need another correction"); e != nil {
		t.Fatal(e)
	}
	change := f.end.Add(5 * time.Second)
	if _, e := f.pool.Exec(t.Context(), `UPDATE plume.check_events SET created_at=$2 WHERE check_id=$1 AND action='changes'`, f.checkID, change); e != nil {
		t.Fatal(e)
	}
	f.remediated = change.Add(time.Second)
	f.anchor = f.remediated.Add(70 * time.Second)
	f.end = f.remediated.Add(131 * time.Second)
	f.pass(t)
	second := f.detail(t, f.reviewer.ID)
	if second.ReviewRevision <= first.ReviewRevision {
		t.Fatal("new remediation did not create new independent review revision")
	}
	if e := f.repo.ReviewCheck(t.Context(), f.ws.ID, f.reviewer.ID, f.checkID, first.ReviewRevision, "verified", "stale first revision"); !errors.Is(e, ErrConflict) {
		t.Fatal("old review revision accepted", e)
	}
}
func TestExtendedRuntimeControlledApplyStartsDraftRetest(t *testing.T) {
	f := runtimeExtendedFixture(t, "over_collection")
	d := f.detail(t, f.user.ID)
	action, e := f.repo.CreateControlAction(t.Context(), f.ws.ID, f.user.ID, ControlActionInput{Kind: "block_api", RuntimeSourceID: f.source.ID, AssetID: d.AssetID, CheckID: f.checkID, Method: f.good.Method, Route: f.good.Route, Reason: "Synthetic controlled application", DurationMinutes: 15})
	if e != nil {
		t.Fatal(e)
	}
	action, e = f.repo.ApproveControlAction(t.Context(), f.ws.ID, f.reviewer.ID, action.ID, action.Revision)
	if e != nil {
		t.Fatal(e)
	}
	var now time.Time
	if e = f.pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); e != nil {
		t.Fatal(e)
	}
	e = f.repo.RecordControlReceipt(t.Context(), "gateway", f.token, ControlReceipt{ActionID: action.ID, Revision: action.Revision, Result: ControlResult{Phase: "apply", Success: true, Code: "route_blocked", ObservedAt: now}})
	if e != nil {
		t.Fatal(e)
	}
	d = f.detail(t, f.user.ID)
	if d.Status != "draft" || d.RuntimeRetest.State != "awaiting_observations" || d.RuntimeRetest.NextEligibleAt == nil || !d.RuntimeRetest.NextEligibleAt.After(now) || d.CanSubmit {
		t.Fatal("apply receipt required manual submit/changes or prematurely passed", d)
	}
	changes := 0
	for _, event := range d.Events {
		if event.Action == "changes" {
			changes++
		}
	}
	if changes != 0 {
		t.Fatal("draft apply needed artificial review changes")
	}
}

func TestExtendedRuntimePolicyRevisionRecoveryAndScopeBoundary(t *testing.T) {
	f := runtimeExtendedFixture(t, "over_collection")
	f.pass(t)
	first := f.detail(t, f.reviewer.ID)
	next := f.policy
	next.AllowedCategories = []string{"phone", "email"}
	next, e := f.repo.SaveRuntimeDetectionPolicy(t.Context(), f.ws.ID, f.user.ID, next)
	if e != nil {
		t.Fatal(e)
	}
	if d := f.detail(t, f.reviewer.ID); d.CanVerify || d.RuntimeRetest.State != "invalidated" {
		t.Fatal("policy edit alone cleared old finding", d)
	}
	changed := f.end.Add(time.Second)
	if _, e = f.pool.Exec(t.Context(), `UPDATE plume.runtime_detection_policies SET created_at=$2 WHERE id=$1`, next.ID, changed); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE plume.check_events SET created_at=$2 WHERE check_id=$1 AND action='changes'`, f.checkID, changed); e != nil {
		t.Fatal(e)
	}
	f.remediated = changed.Add(time.Second)
	f.anchor = f.remediated.Add(70 * time.Second)
	f.end = f.remediated.Add(131 * time.Second)
	f.pass(t)
	d := f.detail(t, f.reviewer.ID)
	proof := d.RuntimeFinding.Detection.AutomaticRetest
	if proof == nil || proof.OriginalPolicy.ID != f.policy.ID || proof.AdoptedPolicy.ID != next.ID || proof.PolicyRevision != next.Revision || !strings.Contains(d.Evidence[0].Summary, "原命中策略") {
		t.Fatal("original/adopted baseline not frozen for reviewer", proof)
	}
	if e = f.repo.ReviewCheck(t.Context(), f.ws.ID, f.reviewer.ID, f.checkID, first.ReviewRevision, "verified", "old revision"); !errors.Is(e, ErrConflict) {
		t.Fatal("pre-policy review revision accepted", e)
	}
	narrowed := next
	narrowed.ClientIP = "192.0.2.2"
	if _, e = f.repo.SaveRuntimeDetectionPolicy(t.Context(), f.ws.ID, f.user.ID, narrowed); e != nil {
		t.Fatal(e)
	}
	if d = f.detail(t, f.reviewer.ID); d.CanVerify || d.RuntimeRetest.State != "invalidated" {
		t.Fatal("narrowed IP policy cleared original scope", d)
	}
}
func TestExtendedRuntimeMaskedResponseDoesNotEraseCategoryEvidence(t *testing.T) {
	f := runtimeExtendedFixture(t, "over_collection")
	f.remediate(t)
	request, requestGaps := runtimesecurity.InspectJSON([]byte(`{"phone":"13800138000"}`), "request")
	response, responseGaps := runtimesecurity.InspectJSON([]byte(`{"ok":true}`), "response")
	if len(requestGaps) != 0 || !slices.Contains(responseGaps, "json_dynamic_keys") {
		t.Fatal("fixture did not reproduce actual capture warnings")
	}
	good := f.good
	good.DataFields = append(request, response...)
	good.CaptureWarnings = responseGaps
	good.InspectedLocations = []string{"request"}
	f.deliver(t, f.anchor, good)
	f.deliver(t, f.end, good)
	d := f.detail(t, f.reviewer.ID)
	if d.Status != "in_review" || !d.CanVerify || d.RuntimeRetest.State != "passed" || !slices.Contains(d.RuntimeFinding.Detection.AutomaticRetest.Warnings, "json_dynamic_keys") {
		t.Fatal("masked response wrongly made complete request category unknown", d)
	}
	badFields, _ := runtimesecurity.InspectJSON([]byte(`{"email":"fixture@example.test"}`), "request")
	bad := good
	bad.DataFields = append(badFields, response...)
	matched, observed, _, handled := extendedRuntimePolicySignal(f.policy, bad)
	if !handled || matched == 0 || observed == 0 {
		t.Fatal("mask warning suppressed real forbidden category")
	}
	f.deliver(t, f.end.Add(time.Second), bad)
	if d = f.detail(t, f.reviewer.ID); d.CanVerify || d.Status != "needs_work" {
		t.Fatal("actual positive no longer invalidated old retest", d)
	}
}

func TestExtendedRuntimeTwentyFourHourWindowKeepsStartEvidence(t *testing.T) {
	f := runtimeExtendedFixture(t, "subject_repeated_access")
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	// Recreate a causally ordered historic fixture; no wall-clock sleep or
	// production time gates are bypassed. The original positive proof is kept
	// while source sample cleanup discards observations older than its buffer.
	old := now.Add(-49 * time.Hour)
	policyAt := old.Add(-time.Hour)
	d := f.detail(t, f.user.ID)
	snapshot := *d.RuntimeFinding.Detection
	snapshot.Policy.WindowMinutes = 1440
	snapshot.Policy.CreatedAt = &policyAt
	snapshot.WindowEnd = old
	snapshot.WindowStart = old.Add(-24 * time.Hour)
	original := snapshot.Policy
	snapshot.RetestScope.OriginalPolicy = &original
	f.policy.WindowMinutes = 1440
	f.policy.CreatedAt = &policyAt
	if _, e := f.pool.Exec(ctx, `UPDATE plume.runtime_detection_policies SET config=$2,created_at=$3 WHERE id=$1`, f.policy.ID, f.policy, policyAt); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE plume.runtime_findings SET first_seen=$2,last_positive_at=$2,detection=$3 WHERE check_id=$1`, f.checkID, old, snapshot); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE plume.checks SET created_at=$2 WHERE id=$1`, f.checkID, old.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE plume.runtime_detection_samples SET observed_at=$2,observation=jsonb_set(observation,'{observedAt}',to_jsonb($2::timestamptz)) WHERE source_id=$1`, f.source.ID, old); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE plume.runtime_security_sources SET created_at=$2,last_seen=$3 WHERE id=$1`, f.source.ID, policyAt, old); e != nil {
		t.Fatal(e)
	}
	f.remediated = now.Add(-25*time.Hour - 30*time.Minute)
	f.anchor = now.Add(-24*time.Hour - 30*time.Minute)
	f.end = now.Add(-29 * time.Minute)
	f.pass(t)
	passed := f.detail(t, f.reviewer.ID)
	if passed.RuntimeFinding.Detection.AutomaticRetest.WindowEnd.Sub(passed.RuntimeFinding.Detection.AutomaticRetest.WindowStart) != 24*time.Hour {
		t.Fatal("24h rule was silently shortened")
	}
}
func TestExtendedRuntimeExpiredProofRefreshesReviewRevision(t *testing.T) {
	f := runtimeExtendedFixture(t, "over_collection")
	f.pass(t)
	ctx := t.Context()
	before := f.detail(t, f.reviewer.ID)
	snapshot := *before.RuntimeFinding.Detection
	shift := func(at *time.Time) {
		if at != nil {
			*at = at.Add(-25 * time.Hour)
		}
	}
	shift(snapshot.Policy.CreatedAt)
	shift(snapshot.RetestScope.OriginalPolicy.CreatedAt)
	proof := snapshot.AutomaticRetest
	shift(proof.OriginalPolicy.CreatedAt)
	shift(proof.AdoptedPolicy.CreatedAt)
	proof.WindowStart = proof.WindowStart.Add(-25 * time.Hour)
	proof.WindowEnd = proof.WindowEnd.Add(-25 * time.Hour)
	proof.ScopeDigest = assessmentDigest(snapshot.RetestScope)
	if _, e := f.pool.Exec(ctx, `UPDATE plume.runtime_detection_policies SET created_at=$2 WHERE id=$1`, f.policy.ID, *snapshot.Policy.CreatedAt); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE plume.runtime_findings SET first_seen=first_seen-interval '25 hours',last_positive_at=last_positive_at-interval '25 hours',retest_at=retest_at-interval '25 hours',detection=$2 WHERE check_id=$1`, f.checkID, snapshot); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE plume.checks SET created_at=created_at-interval '25 hours' WHERE id=$1`, f.checkID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE plume.check_evidence SET observed_at=observed_at-interval '25 hours',created_at=created_at-interval '25 hours' WHERE check_id=$1`, f.checkID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE plume.check_events SET created_at=created_at-interval '25 hours' WHERE check_id=$1`, f.checkID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pool.Exec(ctx, `UPDATE plume.runtime_detection_samples SET observed_at=observed_at-interval '25 hours',observation=jsonb_set(observation,'{observedAt}',to_jsonb(observed_at-interval '25 hours')) WHERE source_id=$1`, f.source.ID); e != nil {
		t.Fatal(e)
	}
	stale := f.detail(t, f.reviewer.ID)
	if stale.CanVerify || stale.RuntimeRetest.State != "invalidated" || !strings.Contains(stale.RuntimeRetest.Reason, "24小时") {
		t.Fatal("fresh source heartbeat reactivated expired proof", stale)
	}
	now := time.Now().UTC()
	f.deliver(t, now.Add(-2*time.Minute), f.good)
	f.deliver(t, now.Add(-time.Second), f.good)
	fresh := f.detail(t, f.reviewer.ID)
	if !fresh.CanVerify || fresh.RuntimeRetest.State != "passed" || fresh.ReviewRevision <= before.ReviewRevision {
		t.Fatal("fresh full window failed to replace expired proof/review revision", fresh)
	}
	if e := f.repo.ReviewCheck(ctx, f.ws.ID, f.reviewer.ID, f.checkID, before.ReviewRevision, "verified", "stale revision"); !errors.Is(e, ErrConflict) {
		t.Fatal("expired old revision accepted", e)
	}
}
