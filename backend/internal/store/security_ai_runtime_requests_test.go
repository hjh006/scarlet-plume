package store

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func runtimeChainFixture(stage string, id int64) AISecurityRuntimeEvent {
	r := aisecurity.AuditRecord{AttemptID: strings.Repeat("f", 32), EventID: strings.Repeat("a", 32), RequestID: "synthetic-request-0001", Stage: stage, ObservedAt: time.Date(2026, 11, 14, 1, 0, int(id), 0, time.UTC), RuntimeProtocolVersion: aisecurity.RuntimeProtocolVersion, InspectionVersion: aisecurity.InspectionVersion, Decision: aisecurity.Decision{Outcome: "allow", Code: "allowed", Kind: "model", ApplicationID: "app", PrincipalID: "person", CatalogDigest: strings.Repeat("b", 64), RequestDigest: strings.Repeat("c", 64)}, Execution: aisecurity.Execution{Outcome: "not_attempted"}}
	if stage == "completed" {
		r.Execution = aisecurity.Execution{Attempted: true, Outcome: "succeeded", HTTPStatus: 200}
	}
	return AISecurityRuntimeEvent{ID: id, SourceID: 1, Current: true, Record: r}
}

func TestAIRuntimePhaseClaimsRejectContradictoryExecution(t *testing.T) {
	for _, stage := range []string{"decision", "prepared", "completed"} {
		r := runtimeChainFixture(stage, 1).Record
		if !validAIRuntimePhase(r) {
			t.Fatalf("valid phase %s rejected", stage)
		}
		r.Execution.Attempted = !r.Execution.Attempted
		if validAIRuntimePhase(r) {
			t.Fatalf("contradictory phase %s accepted", stage)
		}
	}
	r := runtimeChainFixture("prepared", 1).Record
	r.Decision.Outcome = "deny"
	if validAIRuntimePhase(r) {
		t.Fatal("prepared denial accepted")
	}
	r = runtimeChainFixture("completed", 1).Record
	r.Execution.HTTPStatus = 500
	if validAIRuntimePhase(r) {
		t.Fatal("failed upstream promoted to success")
	}
	for _, outcome := range []string{"unknown", "failed", "blocked_output"} {
		r = runtimeChainFixture("completed", 1).Record
		r.Decision.Outcome = "deny"
		r.Execution.Outcome = outcome
		if !validAIRuntimePhase(r) {
			t.Fatalf("legitimate %s rejected", outcome)
		}
	}
}

func TestAIRuntimeRequestsRequireAllPhasesAndKeepSourcesSeparate(t *testing.T) {
	d, p, c := runtimeChainFixture("decision", 1), runtimeChainFixture("prepared", 2), runtimeChainFixture("completed", 3)
	for _, tc := range []struct {
		events  []AISecurityRuntimeEvent
		status  string
		missing int
	}{
		{[]AISecurityRuntimeEvent{c, p, d}, "reported_succeeded", 0},
		{[]AISecurityRuntimeEvent{c}, "incomplete", 2},
		{[]AISecurityRuntimeEvent{p, d}, "incomplete", 1},
	} {
		out := aiRuntimeRequests(tc.events)
		if len(out) != 1 || out[0].Status != tc.status || len(out[0].MissingStages) != tc.missing {
			t.Fatalf("wrong chain: %+v", out)
		}
	}
	c.SourceID = 2
	out := aiRuntimeRequests([]AISecurityRuntimeEvent{c, p, d})
	if len(out) != 2 || out[0].Status != "incomplete" || out[1].Status != "incomplete" {
		t.Fatalf("cross-source phases combined: %+v", out)
	}
	c = runtimeChainFixture("completed", 3)
	c.Record.Decision.RequestDigest = strings.Repeat("d", 64)
	if len(aiRuntimeRequests([]AISecurityRuntimeEvent{c, p, d})) != 2 {
		t.Fatal("different request digests combined")
	}
}

func TestAIRuntimeRequestsPreserveConflictsAndVersionGaps(t *testing.T) {
	d, p, c := runtimeChainFixture("decision", 1), runtimeChainFixture("prepared", 2), runtimeChainFixture("completed", 3)
	duplicate := c
	duplicate.ID = 4
	duplicate.Record.Decision.Outcome = "deny"
	duplicate.Record.Execution.Outcome = "unknown"
	out := aiRuntimeRequests([]AISecurityRuntimeEvent{duplicate, c, p, d})
	if out[0].Status != "inconsistent" || !slices.Contains(out[0].Issues, "phase_result_conflict") {
		t.Fatalf("conflict lost: %+v", out)
	}
	c.Record.InspectionVersion = "unknown-future-inspector"
	if aiRuntimeCompatibility(c.Record) != "unsupported" {
		t.Fatal("unknown inspector adopted")
	}
	out = aiRuntimeRequests([]AISecurityRuntimeEvent{c, p, d})
	if out[0].Status != "inconsistent" || !slices.Contains(out[0].Issues, "runtime_profile_conflict") {
		t.Fatalf("profile drift lost: %+v", out)
	}
	c.Record.RuntimeProtocolVersion = ""
	c.Record.InspectionVersion = ""
	if !validAIRuntimeVersions(c.Record) || aiRuntimeCompatibility(c.Record) != "unversioned" {
		t.Fatal("legacy evidence incorrectly upgraded or rejected")
	}
	c.Record.RuntimeProtocolVersion = aisecurity.RuntimeProtocolVersion
	if validAIRuntimeVersions(c.Record) {
		t.Fatal("partial version declaration accepted")
	}
	d.Record.Decision.Outcome = "deny"
	out = aiRuntimeRequests([]AISecurityRuntimeEvent{d})
	if out[0].Status != "policy_deny" || len(out[0].MissingStages) != 0 {
		t.Fatalf("standalone denial confused with execution: %+v", out)
	}
	d.Record.Decision.Outcome = "allow"
	c = runtimeChainFixture("completed", 3)
	c.Current = false
	out = aiRuntimeRequests([]AISecurityRuntimeEvent{c, p, d})
	if out[0].Current {
		t.Fatal("historical phase upgraded to current chain")
	}
}

func TestAIRuntimeRequestsDoNotMergeReplaysAndLegacyRepeatedDecisions(t *testing.T) {
	d, p, c := runtimeChainFixture("decision", 1), runtimeChainFixture("prepared", 2), runtimeChainFixture("completed", 3)
	retry := runtimeChainFixture("decision", 4)
	retry.Record.AttemptID = strings.Repeat("e", 32)
	out := aiRuntimeRequests([]AISecurityRuntimeEvent{retry, c, p, d})
	if len(out) != 2 || out[0].Status != "incomplete" || out[1].Status != "reported_succeeded" {
		t.Fatalf("normal retry corrupted original execution: %+v", out)
	}
	for _, record := range []*AISecurityRuntimeEvent{&retry, &c, &p, &d} {
		record.Record.AttemptID = ""
	}
	out = aiRuntimeRequests([]AISecurityRuntimeEvent{retry, c, p, d})
	if len(out) != 1 || out[0].Status != "reported_succeeded" {
		t.Fatalf("identical legacy decision time caused false conflict: %+v", out)
	}
	retry.Record.Decision.Outcome = "deny"
	retry.Record.Decision.Code = "request_expired"
	out = aiRuntimeRequests([]AISecurityRuntimeEvent{retry, c, p, d})
	if out[0].Status != "attempts_ambiguous" {
		t.Fatalf("legacy attempts incorrectly alleged contradictory execution: %+v", out)
	}
}
