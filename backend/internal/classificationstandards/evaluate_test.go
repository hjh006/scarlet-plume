package classificationstandards

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

func TestPostgreSQLAutomaticAttributesRequireObservedFormats(t *testing.T) {
	table := collector.PostgreSQLTable{MetadataStatus: "complete", Columns: []collector.PostgreSQLColumn{{Name: "value", Type: "text", Comment: "customer email address"}}}
	e, err := EvaluatePostgreSQL(table, DefaultID)
	if err != nil || e.Status != "review_required" || e.AutoApplicable || e.Category != "unknown" {
		t.Fatalf("metadata name/comment became confirmed classification: %+v %v", e, err)
	}
	analysis := &collector.PostgreSQLContentAnalysis{Status: "partial", Method: "bounded_prefix_aggregate", RowLimit: 200, RowsScanned: 200, Warnings: []string{"row_limit_reached"},
		Columns: []collector.PostgreSQLColumnStatistics{{Column: "value", Status: "complete", NonNullRows: 200, Matches: []collector.PostgreSQLPatternCount{{Category: "email", Count: 120}}}}}
	table.ContentAnalysis = analysis
	e, err = EvaluatePostgreSQL(table, DefaultID)
	if err != nil || !e.AutoApplicable || e.Status != "detected" || e.Category != "personal" || e.BusinessLevel != "internal" || e.LevelCode != "" || e.StatutoryCategory != "" {
		t.Fatalf("bounded positive aggregate did not yield scoped attributes: %+v %v", e, err)
	}
	if len(e.Rules) == 0 || e.Rules[0].Status != "detected" || e.Rules[0].Rule.Evidence == "" || len(e.Rules[0].MissingConditions) == 0 {
		t.Fatal("automatic rule lost its snapshot, proof or missing conditions")
	}
	analysis.Columns[0].Matches = []collector.PostgreSQLPatternCount{{Category: "national_id", Count: 1}}
	e, err = EvaluatePostgreSQL(table, "gbt-45574-2025")
	if err != nil || !e.AutoApplicable || e.Category != "sensitive_personal" || e.BusinessLevel != "confidential" || e.LevelCode != "" || e.StatutoryCategory != "" {
		t.Fatalf("sensitive format implied an unobserved statutory grade: %+v %v", e, err)
	}
	analysis.Warnings = []string{"columns_without_select"}
	e, _ = EvaluatePostgreSQL(table, DefaultID)
	if !e.AutoApplicable || e.Category != "sensitive_personal" || !strings.Contains(strings.Join(e.MissingConditions, ";"), "columns_without_select") {
		t.Fatal("unreadable columns discarded successfully observed positive formats or their scope gap")
	}
	analysis.Status = "unknown"
	analysis.Warnings = []string{"aggregate_timeout"}
	e, _ = EvaluatePostgreSQL(table, DefaultID)
	if e.AutoApplicable || e.Category != "unknown" || !strings.Contains(strings.Join(e.MissingConditions, ";"), "aggregate_timeout") {
		t.Fatal("failed aggregate retained a stale positive classification")
	}
}

func TestPostgreSQLRowSecurityRetainsOnlyObservedPositiveAttributes(t *testing.T) {
	table := collector.PostgreSQLTable{MetadataStatus: "complete", RowSecurity: &collector.PostgreSQLRowSecurity{Enabled: true, Active: true},
		Columns: []collector.PostgreSQLColumn{{Name: "value", Type: "text"}},
		ContentAnalysis: &collector.PostgreSQLContentAnalysis{Status: "partial", Method: "bounded_prefix_aggregate", RowLimit: 200, RowsScanned: 2, Warnings: []string{"row_security_filtered_scope"},
			Columns: []collector.PostgreSQLColumnStatistics{{Column: "value", Status: "complete", NonNullRows: 2, Matches: []collector.PostgreSQLPatternCount{{Category: "email", Count: 1}}}}}}
	e, err := EvaluatePostgreSQL(table, DefaultID)
	if err != nil || !e.AutoApplicable || e.Category != "personal" || e.BusinessLevel != "internal" || e.LevelCode != "" || e.StatutoryCategory != "" {
		t.Fatalf("RLS hid observed attributes or inferred an unsupported grade: %+v %v", e, err)
	}
	encoded, _ := json.Marshal(e)
	if !strings.Contains(string(encoded), "row_security_filtered_scope") || !strings.Contains(strings.Join(e.MissingConditions, ";"), "未覆盖数据仍未知") || !strings.Contains(string(encoded), "不是去重人数") {
		t.Fatal("RLS observation omitted its limited scope or misrepresented row counts")
	}
	table.ContentAnalysis.Columns[0].Matches = nil
	e, _ = EvaluatePostgreSQL(table, DefaultID)
	if e.AutoApplicable || e.Category != "unknown" || e.Status != "insufficient_data" {
		t.Fatal("RLS-limited negative observation became a clean or automatic result")
	}
}

func TestPostgreSQLEvaluationKeepsSelectedSectorAndUnknownConditions(t *testing.T) {
	count := int64(10000000)
	table := collector.PostgreSQLTable{MetadataStatus: "complete", EstimatedRows: &count, Columns: []collector.PostgreSQLColumn{{Name: "email", Type: "text"}},
		ContentAnalysis: &collector.PostgreSQLContentAnalysis{Status: "complete", Method: "bounded_prefix_aggregate", RowLimit: 200, RowsScanned: 2,
			Columns: []collector.PostgreSQLColumnStatistics{{Column: "email", Status: "complete", NonNullRows: 2, Matches: []collector.PostgreSQLPatternCount{{Category: "email", Count: 2}}}}}}
	e, err := EvaluatePostgreSQL(table, "education-jyt-0661-2025")
	if err != nil || e.Standard.ID != "education-jyt-0661-2025" || e.Status != "review_required" || !e.AutoApplicable || e.LevelCode != "" || e.StatutoryCategory != "" {
		t.Fatalf("sector evaluation borrowed a grade or lost observed attributes: %+v %v", e, err)
	}
	for _, rule := range e.Rules {
		if rule.Status != "review_required" || len(rule.MissingConditions) == 0 || rule.Rule.Evidence == "" {
			t.Fatal("unimplemented sector provision was silently omitted or passed")
		}
	}
	encoded, _ := json.Marshal(e)
	if !strings.Contains(string(encoded), "不是人数") || !strings.Contains(string(encoded), "去重人数") {
		t.Fatal("estimated rows were not distinguished from people")
	}
	again, _ := EvaluatePostgreSQL(table, "education-jyt-0661-2025")
	if !reflect.DeepEqual(e, again) {
		t.Fatal("identical observations produced unstable evidence")
	}
	if _, err := EvaluatePostgreSQL(table, "ydt-7344-2026"); !errors.Is(err, ErrSelection) {
		t.Fatal("future standard evaluated as current")
	}
	if _, err := EvaluatePostgreSQL(table, "jrt-0158-2018"); !errors.Is(err, ErrSelection) {
		t.Fatal("superseded standard evaluated as current")
	}
}

func TestPostgreSQLNoSignalNeedsCompleteObservation(t *testing.T) {
	table := collector.PostgreSQLTable{MetadataStatus: "complete", ContentAnalysis: &collector.PostgreSQLContentAnalysis{Status: "complete", Method: "bounded_prefix_aggregate", Columns: []collector.PostgreSQLColumnStatistics{}, Warnings: []string{}}}
	e, err := EvaluatePostgreSQL(table, DefaultID)
	if err != nil || e.Status != "no_signal" || e.AutoApplicable {
		t.Fatalf("complete negative scope: %+v %v", e, err)
	}
	table.ContentAnalysis.Status = "partial"
	e, _ = EvaluatePostgreSQL(table, DefaultID)
	if e.Status != "insufficient_data" || e.AutoApplicable {
		t.Fatal("limited negative sample treated as clean")
	}
	table.ContentAnalysis.Columns = []collector.PostgreSQLColumnStatistics{{Column: "unknown", Status: "complete", NonNullRows: 0, Matches: []collector.PostgreSQLPatternCount{{Category: "phone", Count: 1}}}}
	e, _ = EvaluatePostgreSQL(table, DefaultID)
	if e.AutoApplicable {
		t.Fatal("inconsistent aggregate yielded automatic classification")
	}
}
