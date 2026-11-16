package store

import (
	"strings"
	"testing"
)

func TestScenarioReportMissingConditionsFollowAuditTask(t *testing.T) {
	f := assessmentSourceTestStore(t)
	asset, err := f.repo.CreateAsset(t.Context(), f.ws.ID, "Report scope", "CRM", "Data")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		scenario, task, key, required, excluded string
	}{
		{"personal", "audit", "personal_compliance_audit_report", "审计触发", "实际必要性"},
		{"domestic", "personal-audit", "personal_compliance_audit_report", "审计触发", "重要数据识别"},
		{"personal", "pia", "personal_impact_report", "实际必要性", "审计触发"},
		{"domestic", "important-data", "important_data_risk_report", "重要数据识别", "审计触发"},
	} {
		t.Run(tc.scenario+"/"+tc.task, func(t *testing.T) {
			run, err := f.repo.CreateScenarioCheckRun(t.Context(), f.ws.ID, f.user.ID, ScenarioCheckRunInput{Scenario: tc.scenario, Task: tc.task, AssetID: asset.ID})
			if err != nil {
				t.Fatal(err)
			}
			if len(run.ReportRequirements) != 2 {
				t.Fatalf("expected internal and conditional reports, got %d", len(run.ReportRequirements))
			}
			report := run.ReportRequirements[1]
			missing := strings.Join(report.MissingConditions, " ")
			if report.Key != tc.key || !strings.Contains(missing, tc.required) || strings.Contains(missing, tc.excluded) {
				t.Fatalf("incorrect task-specific report: %+v", report)
			}
			if report.Status != "missing" || report.Applicability != "conditional" {
				t.Fatalf("no-source report incorrectly promoted: %+v", report)
			}
		})
	}
}
