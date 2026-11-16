package governanceops

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func instant(s string) *time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &v
}
func baseInput(kind string) Input {
	return Input{Kind: kind, Title: "Governance case", Owner: "privacy", CheckIDs: []int64{1}, EvidenceIDs: []int64{1, 2, 3}, RegulationIDs: []string{"cn-pipl-2021"}, Applicability: "applicable", ApplicabilityReason: "Reviewed scope and evidence"}
}

func TestStrictKindsAndSubjectProjection(t *testing.T) {
	now := time.Now()
	valid := baseInput("rights_request")
	valid.Details = Details{RequestType: "delete", SubjectRef: strings.Repeat("a", 64), ReceivedAt: instant("2026-01-01T01:00:00+08:00"), IdentityStatus: "pending"}
	b, _ := json.Marshal(valid)
	if _, err := Decode(b); err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{
		strings.Replace(string(b), `"details":{`, `"details":{"rawPrompt":"secret",`, 1),
		strings.Replace(string(b), `"details":{`, `"details":{"artifactDigest":"",`, 1),
		strings.Replace(string(b), strings.Repeat("a", 64), "someone@example.com", 1),
		strings.Replace(string(b), `2026-01-01T01:00:00+08:00`, `2026-01-01T01:00:00`, 1),
		string(b) + ` {}`,
	} {
		if _, err := Decode([]byte(replacement)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid input: %s %v", replacement, err)
		}
	}
	valid.Details.ReceivedAt = &now
	valid.Details.ContractRef = "wrong kind"
	if err := valid.Validate(now); !errors.Is(err, ErrInvalid) {
		t.Fatal("programmatic field bypass", err)
	}
}

func TestIncidentClockHasOperatorScopeAndNoWorkingDayGuess(t *testing.T) {
	now := *instant("2026-11-16T14:00:00+08:00")
	for typ, h := range map[string]int{"cii": 1, "central": 2, "other": 4} {
		in := baseInput("incident")
		in.RegulationIDs = []string{"cn-incident-2025"}
		in.Details = Details{Level: "significant", OperatorType: typ, DetectedAt: instant("2026-11-16T12:00:00+08:00"), ResponseEndedAt: instant("2026-11-16T13:00:00+08:00")}
		out := Deadlines(in, now)
		if len(out) != 2 || out[0].DueAt.Sub(*in.Details.DetectedAt) != time.Duration(h)*time.Hour || out[1].DueAt.Day() != 16 || out[1].DueAt.Month() != time.December {
			t.Fatalf("clock %s: %#v", typ, out)
		}
		in.Details.Level = "general"
		if len(Deadlines(in, now)) != 0 {
			t.Fatal("general event incorrectly inherits major reporting clock")
		}
		in.Details.Level = "unknown"
		if d := Deadlines(in, now); len(d) != 1 || d[0].DueAt != nil || d[0].Status != "pending_scope" {
			t.Fatal("unknown scope assumed", d)
		}
	}
	in := baseInput("audit")
	in.RegulationIDs = []string{"cn-network-risk-2026", "cn-miit-risk-2024"}
	in.Details = Details{Scope: "important-data risk assessment", CompletedAt: instant("2026-11-16T12:00:00+08:00")}
	out := Deadlines(in, now)
	if len(out) != 1 || out[0].DueAt != nil || out[0].Amount != 10 || out[0].Status != "pending_calendar" || out[0].RegulationID != "cn-miit-risk-2024" {
		t.Fatal("sector/calendar mismatch", out)
	}
}

func TestAuditThresholdsAndCalendarMonths(t *testing.T) {
	now := *instant("2026-11-16T14:00:00+08:00")
	for _, tt := range []struct {
		people int64
		minor  bool
		amount int
	}{{10_000_000, false, 0}, {10_000_001, false, 24}, {99_999, false, 60}, {100_000, false, 0}, {99_999, true, 0}} {
		in := baseInput("audit")
		in.RegulationIDs = []string{"cn-pi-audit-2025", "cn-small-pi-2026"}
		in.Details = Details{Scope: "PI audit", ProcessorPeople: &tt.people, ScaleEvidenceID: 1, CompletedAt: instant("2026-09-01T00:00:00+08:00"), MinorData: tt.minor}
		out := Deadlines(in, now)
		if len(out) != 1 || out[0].Amount != tt.amount {
			t.Fatalf("people %d minors %v: %#v", tt.people, tt.minor, out)
		}
	}
	d := addMonths(*instant("2024-02-29T12:00:00+08:00"), 24)
	if d.Format(time.RFC3339) != "2026-02-28T12:00:00+08:00" {
		t.Fatal("leap-day overshoot", d)
	}
}

func TestConsentWithdrawalRequiresEverySystemAndOriginalScope(t *testing.T) {
	now := time.Now()
	in := baseInput("consent")
	in.Details = Details{SubjectRef: strings.Repeat("b", 64), Purpose: "inference", LawfulBasis: "consent", NoticeVersion: "v2", GrantedAt: &now, Systems: []string{"gateway", "archive"}}
	in.Receipts = []Receipt{{Purpose: "consent_grant", EvidenceID: 1}}
	if s, err := NextStatus(in, "in_progress", "submit", false, now); err != nil || s != "in_review" {
		t.Fatal(s, err)
	}
	if _, err := NextStatus(in, "withdrawal_pending", "submit", true, now); !errors.Is(err, ErrEvidence) {
		t.Fatal("withdrawal before receipt", err)
	}
	in.Details.WithdrawalAt = &now
	in.Receipts = append(in.Receipts, Receipt{Purpose: "withdrawal_propagation", EvidenceID: 2, System: "gateway"})
	if err := CompletionReady(in, true, now); !errors.Is(err, ErrEvidence) {
		t.Fatal("archive evidence missing", err)
	}
	in.Receipts = append(in.Receipts, Receipt{Purpose: "withdrawal_propagation", EvidenceID: 3, System: "archive"})
	if s, err := NextStatus(in, "in_review", "approve", true, now); err != nil || s != "withdrawn" {
		t.Fatal(s, err)
	}
	in.Applicability = "not_applicable"
	if _, err := NextStatus(in, "completed", "withdraw", false, now); !errors.Is(err, ErrTransition) {
		t.Fatal("not-applicable became source withdrawal", err)
	}
}

func TestCompletionRequiresIdentityAndBothReportReceiptsPerChannel(t *testing.T) {
	now := time.Now()
	in := baseInput("rights_request")
	in.Details = Details{IdentityStatus: "verified", IdentityEvidenceID: 1, Outcome: "delivered", Resolution: "fulfilled", RequestType: "copy"}
	in.Receipts = []Receipt{{Purpose: "identity_verification", EvidenceID: 2}, {Purpose: "response_delivery", EvidenceID: 3}}
	if err := CompletionReady(in, false, now); !errors.Is(err, ErrEvidence) {
		t.Fatal("mismatched identity evidence", err)
	}
	in.Receipts[0].EvidenceID = 1
	if err := CompletionReady(in, false, now); err != nil {
		t.Fatal(err)
	}
	in = baseInput("incident")
	in.Details = Details{Level: "major", OperatorType: "other", DetectedAt: &now, ResponseEndedAt: &now, Channels: []string{"cac", "industry"}}
	in.Receipts = []Receipt{{Purpose: "initial_report", EvidenceID: 1, System: "cac"}, {Purpose: "final_report", EvidenceID: 2, System: "cac"}}
	if err := CompletionReady(in, false, now); !errors.Is(err, ErrEvidence) {
		t.Fatal("parallel channel skipped", err)
	}
	in.Receipts = append(in.Receipts, Receipt{Purpose: "initial_report", EvidenceID: 1, System: "industry"}, Receipt{Purpose: "final_report", EvidenceID: 3, System: "industry"})
	if err := CompletionReady(in, false, now); err != nil {
		t.Fatal(err)
	}
}

func TestUnsupportedLabelingAndUnknownScopeStayUnverified(t *testing.T) {
	now := time.Now()
	in := baseInput("labeling")
	in.Details = Details{ServiceScope: "public", Modality: "image", ArtifactDigest: strings.Repeat("c", 64), StandardVersion: "GB 45438-2025", ExplicitStatus: "present", ImplicitStatus: "present", ExportStatus: "present", ProviderCode: "provider", ContentID: "content", VerificationMethod: "unsupported"}
	in.Receipts = []Receipt{{Purpose: "labeling_validation", EvidenceID: 1}}
	if err := CompletionReady(in, false, now); !errors.Is(err, ErrEvidence) {
		t.Fatal("unsupported marked complete", err)
	}
	in.Details.VerificationMethod = "metadata"
	if err := CompletionReady(in, false, now); err != nil {
		t.Fatal(err)
	}
	in.Applicability = "unknown"
	if err := CompletionReady(in, false, now); !errors.Is(err, ErrEvidence) {
		t.Fatal("unknown applicability completed", err)
	}
	in.Applicability = "not_applicable"
	in.EvidenceIDs = nil
	if err := CompletionReady(in, false, now); !errors.Is(err, ErrEvidence) {
		t.Fatal("unchecked exemption", err)
	}
}
