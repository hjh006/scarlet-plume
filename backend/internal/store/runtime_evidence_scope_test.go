package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRuntimeScopedEvidenceSceneReportsAndSchedules(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	hash := func(v string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(v))) }
	source, e := f.repo.CreateRuntimeSource(ctx, f.ws.ID, f.user.ID, "scope fixture", "scope-app", "gateway", hash(f.ws.Slug))
	must(e)
	other, e := f.repo.CreateRuntimeSource(ctx, f.ws.ID, f.user.ID, "other fixture", "scope-app", "gateway", hash(f.ws.Slug+"other"))
	must(e)
	configure := func(rule, scope string) RuntimeDetectionPolicy {
		t.Helper()
		for _, p := range defaultRuntimeDetectionPolicies() {
			if p.Rule == rule {
				p.Enabled = true
				p.SourceID = source.ID
				p.ScopeKey = scope
				p.WindowMinutes = 1
				p.MinRequests = 1
				p.Threshold = 1
				if rule == "over_collection" {
					p.AllowlistConfigured = true
					p.Direction = "request"
					p.AllowedCategories = []string{"phone"}
				}
				saved, e := f.repo.SaveRuntimeDetectionPolicy(ctx, f.ws.ID, f.user.ID, p)
				must(e)
				_, e = f.pool.Exec(ctx, `UPDATE plume.runtime_detection_policies SET created_at=clock_timestamp()-interval '2 hours' WHERE id=$1`, saved.ID)
				must(e)
				return saved
			}
		}
		t.Fatal(rule)
		return RuntimeDetectionPolicy{}
	}
	policy := configure("over_collection", "privacy")
	configure("credential_cleartext_transport", "transport")
	subjectPolicy := configure("subject_multi_ip", "subject")
	subjectPolicy.Threshold = 3
	_, e = f.repo.SaveRuntimeDetectionPolicy(ctx, f.ws.ID, f.user.ID, subjectPolicy)
	must(e)
	var now time.Time
	must(f.pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now))
	obs := func(route, ip string) runtimesecurity.Observation {
		return runtimesecurity.Observation{Method: "POST", Route: route, Status: 200, ObservedAt: now, ClientIP: ip, Transport: "https", TransportHop: "proxy_upstream", InspectedLocations: []string{"request"}, AuthState: "anonymous"}
	}
	bad := obs("/bad", "192.0.2.11")
	bad.DataFields = []runtimesecurity.DataField{{Name: "email", Category: "email", Location: "request", Method: "format", Count: 1, Observed: 1}}
	token, e := valuefingerprint.Create(strings.Repeat("11", 32), "test-key", "test-domain", "fixture@example.test")
	must(e)
	bad.ValueFingerprints = []runtimesecurity.ValueFingerprint{{KeyID: token.KeyID, Scope: token.Scope, Version: token.Version, Digest: token.Digest, Locator: "email", Category: "email"}}
	safe := obs("/safe", "192.0.2.12")
	safe.DataFields = []runtimesecurity.DataField{{Name: "phone", Category: "phone", Location: "request", Method: "format", Count: 1, Observed: 1}}
	partial := obs("/partial", "192.0.2.13")
	partial.InspectedLocations = nil
	plain := obs("/plain", "192.0.2.14")
	plain.Transport = "http"
	plain.DataFields = []runtimesecurity.DataField{{Name: "password", Category: "credential", Location: "request", Method: "metadata", Count: 1, Observed: 1}}
	encrypted := plain
	encrypted.Route = "/https"
	encrypted.Transport = "https"
	unknown := plain
	unknown.Route = "/unknown-transport"
	unknown.Transport = ""
	events := []runtimesecurity.Observation{bad, safe, partial, plain, encrypted, unknown}
	for i := 0; i < 21; i++ {
		o := obs("/subjects", "192.0.2.20")
		o.SubjectRef = hash(fmt.Sprint(i))
		events = append(events, o)
	}
	receipt, e := f.repo.DeliverRuntimeBatch(ctx, "gateway", hash(f.ws.Slug), hash("batch"), events)
	must(e)
	if receipt.Accepted != len(events) {
		t.Fatal(receipt)
	}
	_, e = f.repo.DeliverRuntimeBatch(ctx, "gateway", hash(f.ws.Slug+"other"), hash("otherbatch"), []runtimesecurity.Observation{bad})
	must(e)
	scope := RuntimeEvidenceScope{SourceID: source.ID, Service: "scope-app", From: now.Add(-2 * time.Minute), To: now.Add(time.Microsecond)}
	run, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, ScenarioCheckRunInput{Scenario: "personal", Scope: &scope})
	must(e)
	find := func(route, rule string) ScenarioCheckFinding {
		t.Helper()
		for _, v := range run.Findings {
			if strings.HasPrefix(v.RuleVersion, rule+"/") && strings.Contains(v.Observed, route+"；") {
				return v
			}
		}
		t.Fatalf("missing %s %s", route, rule)
		return ScenarioCheckFinding{}
	}
	badFinding := find("/bad", "over_collection")
	if badFinding.Outcome != "non_compliant" || !badFinding.Remediation.Executable || badFinding.CheckID == nil {
		t.Fatalf("positive evidence/proposal missing %+v", badFinding)
	}
	if find("/safe", "over_collection").Outcome != "compliant" || find("/partial", "over_collection").Outcome != "needs_verification" {
		t.Fatal("complete/incomplete negatives conflated")
	}
	if find("/plain", "credential_cleartext_transport").Outcome != "non_compliant" || find("/https", "credential_cleartext_transport").Outcome != "compliant" || find("/unknown-transport", "credential_cleartext_transport").Outcome != "needs_verification" {
		t.Fatal("transport facts conflated")
	}
	if find("/safe", "missing_auth").Outcome != "needs_verification" {
		t.Fatal("unknown authentication requirement passed")
	}
	if !slices.ContainsFunc(run.Findings, func(v ScenarioCheckFinding) bool {
		return strings.HasPrefix(v.ID, "runtime_budget_subjects") && v.Outcome == "needs_verification"
	}) {
		t.Fatal("subject truncation silently lost coverage")
	}
	saved, e := f.repo.GetScenarioCheckRun(ctx, f.ws.ID, f.user.ID, run.ID)
	must(e)
	if ScenarioCheckRunSnapshotDigest(saved) != run.SnapshotDigest {
		t.Fatal("run digest changed on JSONB roundtrip")
	}
	action, e := f.repo.ProposeScenarioCheckRemediation(ctx, f.ws.ID, f.user.ID, run.ID, badFinding.ID, "确认临时停止该接口", 15)
	must(e)
	if action.Kind != "block_api" || action.Status != "proposed" || action.RuntimeSourceID == nil || *action.RuntimeSourceID != source.ID {
		t.Fatal(action)
	}
	same, e := f.repo.ProposeScenarioCheckRemediation(ctx, f.ws.ID, f.user.ID, run.ID, badFinding.ID, "重试申请", 15)
	must(e)
	if same.ID != action.ID {
		t.Fatal("proposal not idempotent")
	}
	scoped := scope
	scoped.ClientIP = "192.0.2.11"
	window, e := f.repo.RuntimeEvidence(ctx, f.ws.ID, f.user.ID, scoped)
	must(e)
	if window.Total != 1 || len(window.Items) != 1 {
		t.Fatal("IP/source filter applied after limit or ignored", window.Total)
	}
	matches, e := f.repo.SearchRuntimeValueFingerprint(ctx, f.ws.ID, f.user.ID, token, 20)
	must(e)
	if len(matches) != 2 {
		t.Fatal("fingerprint source projection missing", matches)
	}
	input := OperationsReportInput{Title: "fixture operational report", Scope: scoped}
	report, e := f.repo.CreateOperationsReport(ctx, f.ws.ID, f.user.ID, input)
	must(e)
	if report.Window.Total != 1 || len(report.Analysis) == 0 || len(report.Evidence) == 0 {
		t.Fatal("report is not actual frozen analysis")
	}
	reloaded, e := f.repo.GetOperationsReport(ctx, f.ws.ID, f.user.ID, report.ID)
	must(e)
	if reloaded.SnapshotDigest != OperationsReportSnapshotDigest(reloaded) {
		a, _ := json.Marshal(report)
		b, _ := json.Marshal(reloaded)
		t.Fatalf("report digest mismatch\n%s\n%s", a, b)
	}
	list, e := f.repo.ListOperationsReports(ctx, f.ws.ID, f.user.ID, scoped)
	must(e)
	if len(list) != 1 || list[0].Window.Items != nil || list[0].Analysis != nil {
		t.Fatal("report metadata list")
	}
	schedule, e := f.repo.SaveOperationsReportSchedule(ctx, f.ws.ID, f.user.ID, 0, OperationsReportScheduleInput{Title: "月度运营", Period: "monthly", Timezone: "Asia/Shanghai", Scope: scope, Enabled: true})
	must(e)
	scheduled, e := f.repo.RunOperationsReportSchedule(ctx, f.ws.ID, f.user.ID, schedule.ID)
	must(e)
	again, e := f.repo.RunOperationsReportSchedule(ctx, f.ws.ID, f.user.ID, schedule.ID)
	must(e)
	if again.ID != scheduled.ID || scheduled.ScheduleID == nil || scheduled.Scope.To.Sub(scheduled.Scope.From) < 27*24*time.Hour || len(scheduled.MissingConditions) < 2 {
		t.Fatal("calendar month/retention/idempotence missing")
	}
	_, e = f.pool.Exec(ctx, `UPDATE plume.operations_report_schedules SET next_run_at=date_trunc('day',clock_timestamp()) WHERE id=$1`, schedule.ID)
	must(e)
	count, e := f.repo.RunDueOperationsReportSchedules(ctx, 1)
	must(e)
	if count != 1 {
		t.Fatal("worker did not freeze due report")
	}
	count, e = f.repo.RunDueOperationsReportSchedules(ctx, 1)
	must(e)
	if count != 0 {
		t.Fatal("worker duplicated due report")
	}
	changed := policy
	changed.AllowedCategories = []string{"email"}
	_, e = f.repo.SaveRuntimeDetectionPolicy(ctx, f.ws.ID, f.user.ID, changed)
	must(e)
	_, e = f.repo.ProposeScenarioCheckRemediation(ctx, f.ws.ID, f.user.ID, run.ID, find("/plain", "over_collection").ID, "策略已变化", 15)
	if !errors.Is(e, ErrConflict) {
		t.Fatalf("changed policy proposal err=%v", e)
	}
	narrowScope := scope
	narrowScope.ValueFingerprint = &token
	narrowRun, e := f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, ScenarioCheckRunInput{Scenario: "personal", Scope: &narrowScope})
	must(e)
	for _, item := range narrowRun.Findings {
		if item.Remediation.Executable {
			t.Fatal("value-token subset widened into whole API repair")
		}
	}
	narrowPolicy := policy
	narrowPolicy.ID = 0
	narrowPolicy.Revision = 0
	narrowPolicy.ScopeKey = "privacy-ip"
	narrowPolicy.ClientIP = "192.0.2.11"
	_, e = f.repo.SaveRuntimeDetectionPolicy(ctx, f.ws.ID, f.user.ID, narrowPolicy)
	must(e)
	narrowRun, e = f.repo.CreateScenarioCheckRun(ctx, f.ws.ID, f.user.ID, ScenarioCheckRunInput{Scenario: "personal", Scope: &scope})
	must(e)
	seenSubset := false
	for _, item := range narrowRun.Findings {
		if strings.HasPrefix(item.RuleVersion, "over_collection/privacy-ip/") {
			seenSubset = true
			if item.Remediation.Executable {
				t.Fatal("policy IP subset widened into whole API repair")
			}
		}
	}
	if !seenSubset {
		t.Fatal("subset policy not evaluated")
	}
	_, e = f.pool.Exec(ctx, `UPDATE plume.memberships SET role='operator' WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, f.user.ID)
	must(e)
	inactive, e := f.repo.RuntimeEvidence(ctx, f.ws.ID, f.user.ID, scoped)
	must(e)
	if len(inactive.Items) != 1 || inactive.Items[0].SourceCurrent {
		t.Fatal("demoted source creator remained trusted")
	}
	_, e = f.repo.SaveOperationsReportSchedule(ctx, f.ws.ID, f.user.ID, 0, OperationsReportScheduleInput{Title: "not owner", Period: "daily", Scope: scope, Enabled: true})
	if !errors.Is(e, ErrForbidden) {
		t.Fatalf("operator modified schedule: %v", e)
	}
	_, e = f.pool.Exec(ctx, `UPDATE plume.operations_report_schedules SET next_run_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, schedule.ID)
	must(e)
	count, e = f.repo.RunDueOperationsReportSchedules(ctx, 1)
	must(e)
	if count != 0 {
		t.Fatal("schedule ran for demoted creator")
	}
	_ = other
}
func TestOperationsCalendarPeriods(t *testing.T) {
	loc, e := time.LoadLocation("Asia/Shanghai")
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2028, 3, 19, 12, 0, 0, 0, loc)
	month := operationsPeriodStart(at, "monthly", loc)
	previous := previousOperationsPeriod(month, "monthly")
	if previous.Day() != 1 || month.Sub(previous) != 29*24*time.Hour {
		t.Fatal("natural leap-year month not honored")
	}
	week := operationsPeriodStart(at, "weekly", loc)
	if week.Weekday() != time.Monday || week.Hour() != 0 {
		t.Fatal("week boundary")
	}
}
