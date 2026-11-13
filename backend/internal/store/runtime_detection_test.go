package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeDetectionPoliciesSignalsAndIndependentRetests(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := New(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	want := func(err, target error) {
		t.Helper()
		if !errors.Is(err, target) {
			t.Fatalf("error=%v want=%v", err, target)
		}
	}
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("detection-%d", time.Now().UnixNano())
	ownerName, reviewerName, foreignName := slug+"-owner", slug+"-reviewer", slug+"-foreign"
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(cleanup, `DELETE FROM plume.workspaces WHERE slug=ANY($1)`, []string{slug, slug + "-foreign"})
		if err != nil {
			t.Error(err)
		}
		_, err = pool.Exec(cleanup, `DELETE FROM plume.users WHERE username=ANY($1)`, []string{ownerName, reviewerName, foreignName})
		if err != nil {
			t.Error(err)
		}
	})
	must(repo.Bootstrap(ctx, ownerName, "unused", slug, "Runtime detection"))
	must(repo.ProvisionReviewer(ctx, slug, reviewerName, "unused"))
	must(repo.Bootstrap(ctx, foreignName, "unused", slug+"-foreign", "Foreign"))
	owner, err := repo.FindUser(ctx, ownerName)
	must(err)
	reviewer, err := repo.FindUser(ctx, reviewerName)
	must(err)
	foreign, err := repo.FindUser(ctx, foreignName)
	must(err)
	ws, err := repo.Membership(ctx, owner.ID, slug)
	must(err)
	stamp := func() time.Time {
		t.Helper()
		var at time.Time
		must(pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&at))
		return at.UTC()
	}
	policies, err := repo.RuntimeDetectionPolicies(ctx, ws.ID, owner.ID)
	must(err)
	if len(policies) != 18 {
		t.Fatal("missing built-in policies")
	}
	_, err = repo.RuntimeDetectionPolicies(ctx, ws.ID, foreign.ID)
	if err == nil {
		t.Fatal("foreign actor read policies")
	}
	_, err = repo.SaveRuntimeDetectionPolicy(ctx, ws.ID, reviewer.ID, policies[0])
	want(err, ErrForbidden)
	now := time.Now().UTC().Truncate(time.Microsecond)
	policies = policies[:9]
	for i := range policies {
		p := policies[i]
		p.Enabled = true
		p.WindowMinutes = 1
		p.Threshold = 1
		p.MinRequests = 1
		p.TimezoneOffsetMinutes = 0
		p.StartHour = now.Hour()
		p.EndHour = (now.Hour() + 1) % 24
		if p.Rule == "subject_bulk_access" {
			p.Threshold = 3
		}
		if p.Rule == "error_spike" {
			p.Threshold = 2
			p.MinRequests = 3
		}
		if p.Rule == "off_hours" {
			p.Threshold = 2
		}
		policies[i], err = repo.SaveRuntimeDetectionPolicy(ctx, ws.ID, owner.ID, p)
		must(err)
		if policies[i].Revision != 1 || policies[i].ID == 0 {
			t.Fatal("policy revision was not persisted")
		}
		_, err = repo.SaveRuntimeDetectionPolicy(ctx, ws.ID, owner.ID, p)
		want(err, ErrConflict)
	}
	token, otherToken := digest(slug+"one"), digest(slug+"two")
	source, err := repo.CreateRuntimeSource(ctx, ws.ID, owner.ID, "Detection probe", "Detection app", "otlp", token)
	must(err)
	_, err = repo.CreateRuntimeSource(ctx, ws.ID, owner.ID, "Other detection probe", "Detection app", "otlp", otherToken)
	must(err)
	deliver := func(token, batch string, events ...runtimesecurity.Observation) {
		t.Helper()
		receipt, err := repo.DeliverRuntimeBatch(ctx, "otlp", token, digest(batch), events)
		must(err)
		if receipt.Accepted != len(events) {
			t.Fatalf("receipt=%+v", receipt)
		}
	}
	event := func(route string) runtimesecurity.Observation {
		return runtimesecurity.Observation{Method: "GET", Route: route, Status: 200, ObservedAt: now}
	}
	trueValue := true
	sensitive := event("/sensitive")
	sensitive.DataFields = []runtimesecurity.DataField{{Name: "email", Location: "response", Category: "email", Method: "format", Count: 2, Observed: 3}}
	credential := event("/credential")
	credential.DataFields = []runtimesecurity.DataField{{Name: "password", Location: "response", Category: "credential", Method: "metadata", Count: 1, Observed: 1}}
	anonymous := event("/auth")
	anonymous.AuthState = "anonymous"
	anonymous.AuthRequired = &trueValue
	hint := event("/hint")
	hint.DataFields = []runtimesecurity.DataField{{Name: "email", Location: "response", Category: "email", Method: "metadata", Count: 1, Observed: 1}}
	unknown := event("/unknown-auth")
	unknown.AuthRequired = &trueValue
	bulk := event("/bulk")
	bulk.SubjectRef = strings.Repeat("a", 64)
	otherSubject := bulk
	otherSubject.SubjectRef = strings.Repeat("b", 64)
	errorEvent := event("/errors")
	errorEvent.Status = 500
	goodEvent := event("/errors")
	attackEvents := []runtimesecurity.Observation{}
	for _, kind := range []string{"sql_injection", "xss", "path_traversal"} {
		o := event("/" + strings.ReplaceAll(kind, "_", "-"))
		o.SecuritySignals = []runtimesecurity.SecuritySignal{{Kind: kind, Location: "request", Count: 1, Observed: 1}}
		attackEvents = append(attackEvents, o)
	}
	events := []runtimesecurity.Observation{sensitive, credential, anonymous, hint, unknown, bulk, bulk, bulk, errorEvent, errorEvent, goodEvent, event("/hours"), event("/hours")}
	events = append(events, attackEvents...)
	deliver(token, "initial", events...)
	deliver(token, "initial", events...)
	// Two probes cannot combine their subject observations into a threshold hit.
	isolated := event("/split")
	isolated.SubjectRef = bulk.SubjectRef
	deliver(token, "split-one", isolated, otherSubject)
	deliver(otherToken, "split-two", isolated, isolated)
	findings, err := repo.ListRuntimeFindings(ctx, ws.ID)
	must(err)
	byRule := map[string]RuntimeFinding{}
	wantRoutes := map[string]string{"sensitive_response": "/sensitive", "credential_response": "/credential", "missing_auth": "/auth", "subject_bulk_access": "/bulk", "error_spike": "/errors", "off_hours": "/hours", "sql_injection_signal": "/sql-injection", "xss_signal": "/xss", "path_traversal_signal": "/path-traversal"}
	for _, f := range findings {
		if f.Route == "/hint" && f.Rule == "sensitive_response" || f.Route == "/unknown-auth" && f.Rule == "missing_auth" || f.Route == "/split" && f.Rule == "subject_bulk_access" {
			t.Fatalf("unsupported inferred risk: %+v", f)
		}
		if f.Route == wantRoutes[f.Rule] {
			byRule[f.Rule] = f
		}
	}
	if len(byRule) != 9 {
		t.Fatalf("expected every rule; got %v (all %+v)", byRule, findings)
	}
	var samples int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM plume.runtime_detection_samples WHERE workspace_id=$1`, ws.ID).Scan(&samples))
	if samples != len(events)+4 {
		t.Fatalf("replay changed samples: %d", samples)
	}
	for rule, f := range byRule {
		if f.SourceID != source.ID || f.Detection == nil || f.Detection.Policy.Revision != 1 || f.Detection.Matched < 1 || f.Detection.Observed < 1 || f.BaselineScanID != 0 {
			t.Fatalf("%s missing source/window/policy facts: %+v", rule, f)
		}
		detail, err := repo.CheckDetail(ctx, ws.ID, owner.ID, f.CheckID)
		must(err)
		if len(detail.Evidence) != 1 || !strings.Contains(detail.Evidence[0].Summary, "策略") {
			t.Fatal("automatic evidence missing")
		}
		must(repo.SubmitCheck(ctx, ws.ID, owner.ID, f.CheckID))
		detail, err = repo.CheckDetail(ctx, ws.ID, reviewer.ID, f.CheckID)
		must(err)
		want(repo.ReviewCheck(ctx, ws.ID, reviewer.ID, f.CheckID, detail.ReviewRevision, "verified", "Premature"), ErrEvidenceRequired)
		must(repo.ReviewCheck(ctx, ws.ID, reviewer.ID, f.CheckID, detail.ReviewRevision, "changes", "Correct observed signal"))
		must(repo.AddEvidence(ctx, ws.ID, owner.ID, f.CheckID, EvidenceInput{Kind: "remediation", SourceSystem: "fixture", SourceRecord: "release", Summary: "Corrected source behavior", ObservedAt: stamp()}))
		want(repo.AddEvidence(ctx, ws.ID, owner.ID, f.CheckID, EvidenceInput{Kind: "retest", SourceSystem: "fixture", SourceRecord: "manual", Summary: "Manual replacement", ObservedAt: stamp()}), ErrEvidenceRequired)
	}
	// A route cycle may aggregate different subjects. A low-count triggering
	// subject cannot clear another subject that remains over its own threshold.
	activeA := bulk
	activeA.ObservedAt = now.Add(10 * time.Second)
	deliver(token, "bulk-active-subject-a", activeA, activeA, activeA)
	oldB := otherSubject
	oldB.ObservedAt = now.Add(-40 * time.Second)
	recentB := otherSubject
	recentB.ObservedAt = now.Add(20 * time.Second)
	deliver(token, "bulk-trigger-subject-b", oldB, oldB, recentB)
	quietB := otherSubject
	quietB.ObservedAt = now.Add(65 * time.Second)
	deliver(token, "bulk-quiet-subject-b", quietB)
	bulkDetail, err := repo.CheckDetail(ctx, ws.ID, owner.ID, byRule["subject_bulk_access"].CheckID)
	must(err)
	if bulkDetail.RuntimeFinding.RetestAt != nil {
		t.Fatal("quiet subject cleared another still-positive subject")
	}
	// Unknown capture does not prove an inspected, clean response.
	uncaptured := event("/sensitive")
	uncaptured.ObservedAt = now.Add(2 * time.Minute)
	deliver(token, "unknown-retest", uncaptured)
	detail, err := repo.CheckDetail(ctx, ws.ID, owner.ID, byRule["sensitive_response"].CheckID)
	must(err)
	if detail.CanSubmit || detail.RuntimeFinding.RetestAt != nil {
		t.Fatal("uncaptured response qualified for retest")
	}
	uncaptured.InspectedLocations = []string{"response"}
	uncaptured.CaptureWarnings = []string{"json_field_limit"}
	deliver(token, "limited-retest", uncaptured)
	detail, err = repo.CheckDetail(ctx, ws.ID, owner.ID, byRule["sensitive_response"].CheckID)
	must(err)
	if detail.RuntimeFinding.RetestAt != nil {
		t.Fatal("truncated response qualified for clean retest")
	}
	for i := range policies {
		if policies[i].Rule == "off_hours" {
			policies[i].StartHour = (now.Hour() + 2) % 24
			policies[i].EndHour = (now.Hour() + 3) % 24
			policies[i], err = repo.SaveRuntimeDetectionPolicy(ctx, ws.ID, owner.ID, policies[i])
			must(err)
		}
	}
	clean := []runtimesecurity.Observation{}
	for rule, route := range wantRoutes {
		o := event(route)
		o.ObservedAt = now.Add(2*time.Minute + time.Second)
		switch rule {
		case "sensitive_response", "credential_response":
			o.InspectedLocations = []string{"response"}
		case "missing_auth":
			o.AuthState = "authenticated"
			o.AuthRequired = &trueValue
		case "subject_bulk_access":
			o.SubjectRef = otherSubject.SubjectRef
		case "sql_injection_signal", "xss_signal", "path_traversal_signal":
			o.SecuritySignals = []runtimesecurity.SecuritySignal{{Kind: strings.TrimSuffix(rule, "_signal"), Location: "request", Observed: 1}}
			o.InspectedLocations = []string{"request"}
		case "error_spike":
			clean = append(clean, o, o)
		}
		clean = append(clean, o)
	}
	wrongSourceClean := clean[0]
	wrongSourceClean.Route = "/sensitive"
	wrongSourceClean.InspectedLocations = []string{"response"}
	deliver(otherToken, "wrong-retest-source", wrongSourceClean)
	detail, err = repo.CheckDetail(ctx, ws.ID, owner.ID, byRule["sensitive_response"].CheckID)
	must(err)
	if detail.RuntimeFinding.RetestAt != nil {
		t.Fatal("another probe supplied remediation retest")
	}
	deliver(token, "clean-retests", clean...)
	for rule, f := range byRule {
		detail, err := repo.CheckDetail(ctx, ws.ID, owner.ID, f.CheckID)
		must(err)
		if !detail.CanSubmit || detail.RuntimeFinding.RetestAt == nil {
			t.Fatalf("%s missing fresh retest: %+v", rule, detail)
		}
		must(repo.SubmitCheck(ctx, ws.ID, owner.ID, f.CheckID))
		detail, err = repo.CheckDetail(ctx, ws.ID, reviewer.ID, f.CheckID)
		must(err)
		if !detail.CanVerify {
			t.Fatalf("%s did not qualify for independent review", rule)
		}
		if rule == "credential_response" {
			index := slices.IndexFunc(policies, func(p RuntimeDetectionPolicy) bool { return p.Rule == rule })
			policies[index], err = repo.SaveRuntimeDetectionPolicy(ctx, ws.ID, owner.ID, policies[index])
			must(err)
			detail, err = repo.CheckDetail(ctx, ws.ID, reviewer.ID, f.CheckID)
			must(err)
			if detail.CanVerify {
				t.Fatal("changed policy retained old retest approval")
			}
			want(repo.ReviewCheck(ctx, ws.ID, reviewer.ID, f.CheckID, detail.ReviewRevision, "verified", "Stale policy"), ErrEvidenceRequired)
			must(repo.ReviewCheck(ctx, ws.ID, reviewer.ID, f.CheckID, detail.ReviewRevision, "changes", "Retest under current policy"))
			must(repo.AddEvidence(ctx, ws.ID, owner.ID, f.CheckID, EvidenceInput{Kind: "remediation", SourceSystem: "fixture", SourceRecord: "updated-policy", Summary: "Revalidated current policy", ObservedAt: stamp()}))
			o := event("/credential")
			o.ObservedAt = now.Add(2*time.Minute + 2*time.Second)
			o.InspectedLocations = []string{"response"}
			deliver(token, "current-policy-retest", o)
			must(repo.SubmitCheck(ctx, ws.ID, owner.ID, f.CheckID))
			detail, err = repo.CheckDetail(ctx, ws.ID, reviewer.ID, f.CheckID)
			must(err)
			if !detail.CanVerify {
				t.Fatal("fresh observation could not restore current-policy review")
			}
			history, err := repo.RuntimeDetectionPolicyHistory(ctx, ws.ID, owner.ID, rule)
			must(err)
			if len(history) != 2 || history[0].Revision != 2 || history[1].Revision != 1 {
				t.Fatal("policy history not immutable/ordered")
			}
		}
		must(repo.ReviewCheck(ctx, ws.ID, reviewer.ID, f.CheckID, detail.ReviewRevision, "verified", "Independent source evidence review"))
	}
	// A later real positive opens a new cycle without rewriting verified history.
	sensitive.ObservedAt = now.Add(3 * time.Minute)
	deliver(token, "recurrence", sensitive)
	findings, err = repo.ListRuntimeFindings(ctx, ws.ID)
	must(err)
	index := slices.IndexFunc(findings, func(f RuntimeFinding) bool {
		return f.Rule == "sensitive_response" && f.PreviousCheckID != nil && *f.PreviousCheckID == byRule["sensitive_response"].CheckID
	})
	if index < 0 || findings[index].CheckStatus != "draft" {
		t.Fatal("verified recurrence did not create new actionable cycle")
	}
	detail, err = repo.CheckDetail(ctx, ws.ID, owner.ID, byRule["sensitive_response"].CheckID)
	must(err)
	if detail.Status != "verified" {
		t.Fatal("verified history changed")
	}
	audit, err := repo.ActionAudit(ctx, ws.ID, 24)
	must(err)
	policyActions := 0
	for _, entry := range audit.Items {
		if entry.Action == "runtime.policy_updated" {
			policyActions++
			if entry.Actor != ownerName || entry.TargetType != "runtime_policy" || entry.Automated {
				t.Fatal("policy audit lost actor or action projection")
			}
		}
	}
	if policyActions != 11 {
		t.Fatalf("policy version audit actions=%d want 11", policyActions)
	}
	// The retained detector projection has its own explicit bound and never rejects valid telemetry at that bound.
	_, err = pool.Exec(ctx, `INSERT INTO plume.runtime_detection_samples(workspace_id,source_id,batch_id,ordinal,observed_at,observation) SELECT $1,$2,$3,n,clock_timestamp(),jsonb_build_object('method','GET','route','/limit','status',200,'observedAt',clock_timestamp()) FROM generate_series(1,20001) n`, ws.ID, source.ID, digest("bounded-sample-fixture"))
	must(err)
	limitEvent := event("/limit")
	limitEvent.ObservedAt = now.Add(3 * time.Minute)
	deliver(token, "sample-limit-accepted", limitEvent)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM plume.runtime_detection_samples WHERE source_id=$1`, source.ID).Scan(&samples))
	if samples != 20000 {
		t.Fatalf("detector retention was not bounded: %d", samples)
	}
	var warning string
	must(pool.QueryRow(ctx, `SELECT findings_warning FROM plume.runtime_security_sources WHERE id=$1`, source.ID).Scan(&warning))
	if warning != "scope_limited" {
		t.Fatalf("detector truncation not exposed: %q", warning)
	}
}
