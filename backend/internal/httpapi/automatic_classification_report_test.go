package httpapi

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestAutomaticClassificationReportExport(t *testing.T) {
	now := time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	const unsafeName = `<img src=x onerror="alert('asset')">&资产`
	const criterion = `依据格式 <敏感信息> & 实际用途判断`
	const evidenceRequirement = `保留源内聚合 <计数> & 复核依据`
	const missing = `仍需采证：影响程度 <重要性> & 去重人数`
	const reviewNote = `已核对聚合 & 记录 <复核意见>`
	count := int64(7)
	fact := classificationstandards.Fact{Kind: "format_match", Column: "value<column>", Category: "national_id", Source: "source_aggregate", Count: &count, Note: "匹配行数，不是人数"}
	rule := classificationstandards.Rule{ID: "industry-business-classification", Clause: "§5.1", Title: "分类依据", Criterion: criterion, Evidence: evidenceRequirement}
	standard := classificationstandards.Standard{ID: "gbt-43697-2024", Code: "GB/T 43697—2024", Title: "数据安全技术 数据分类分级规则", Status: "effective", URL: "https://example.invalid/standard", Rules: []classificationstandards.Rule{rule}}
	automatic := &store.AutomaticClassification{JobID: 8, RunID: "safe-run-reference", ObservedAt: now, ClassificationRevision: 1, Comparison: "matched", ComparisonReasons: []string{"观察属性与当前记录相容"}, Evaluation: classificationstandards.Evaluation{
		Version: "postgresql-rules-v1", Standard: standard, Status: "review_required", AutoApplicable: true, Category: "sensitive_personal", BusinessLevel: "confidential",
		Facts: []classificationstandards.Fact{fact}, MissingConditions: []string{missing},
		Rules: []classificationstandards.RuleEvaluation{{Rule: rule, Status: "detected", Facts: []classificationstandards.Fact{fact}, MissingConditions: []string{missing}}},
	}}
	classification := &store.AssetClassificationItem{Asset: store.Asset{ID: 3, Name: unsafeName, Category: "sensitive_personal", BusinessLevel: "confidential", StatutoryCategory: "unknown", ClassificationBasis: "有界格式聚合"}, Revision: 1, Origin: "automatic", Automatic: automatic,
		Standard: &classificationstandards.Reference{ID: standard.ID, Code: standard.Code, Title: standard.Title, URL: standard.URL, RuleID: rule.ID, RuleClause: rule.Clause, RuleTitle: rule.Title, RuleCriterion: criterion, RuleEvidence: evidenceRequirement}}
	observation := &store.PostgresObservation{Database: "source", SourceRef: "inventory.customers", ColumnCount: 1, SensitiveCandidateCount: 1, Assessment: "classification_matched", Scope: "有界源内聚合，不返回业务原值", Classification: automatic}
	review := store.CheckEvent{ID: 5, Action: "verified", Actor: "独立复核员<reviewer>", Note: reviewNote, CreatedAt: now}
	report := store.ComplianceReport{
		Workspace: store.ReportWorkspace{Slug: "export-test", Name: "工作区<workspace>&"}, GeneratedAt: now,
		Scope:          store.ReportScope{Basis: "报告仅覆盖列明检查", Source: &store.ReportSource{JobID: 8, Source: "本地样例", RunID: automatic.RunID, ObservedAt: now}},
		Counts:         store.ReportCounts{Total: 1, Verified: 1, Evidence: 1, Retests: 1, Reviews: 1},
		Identification: &store.ReportIdentification{Tables: 1, Columns: 1, SensitiveCandidates: 1, TablesWithCandidates: 1},
		Checks: []store.ReportCheck{{
			Check:          store.Check{ID: 9, AssetID: 3, AssetName: unsafeName, Status: "verified", CreatedAt: now, UpdatedAt: now, Requirement: store.Requirement{Code: "G02", Title: "分类分级检查"}},
			Classification: classification, AutomaticEvaluation: observation, LatestReview: &review, Events: []store.CheckEvent{review},
			Evidence: []store.Evidence{{ID: 4, Kind: "retest", SourceSystem: "PostgreSQL 自动采集", SourceRecord: "safe-source-reference", Summary: "源内统计完成", ObservedAt: now, CreatedAt: now, RecordedBy: "采集任务", Postgres: observation}},
		}},
	}

	t.Run("HTML", func(t *testing.T) {
		response := httptest.NewRecorder()
		writeReportExport(response, report, "html")
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/html; charset=utf-8" || !strings.Contains(response.Header().Get("Content-Disposition"), "scarlet-compliance-report.html") {
			t.Fatalf("unexpected HTML export response: %d, %v", response.Code, response.Header())
		}
		body := response.Body.String()
		for _, expected := range []string{standard.Code, "自动识别 · v1", "源内聚合", "格式校验命中", " · 7", "规则与取证要求", "定级采证缺口", "与当前分类相容", "复核通过", "疑似敏感 1 个", html.EscapeString(unsafeName), html.EscapeString(criterion), html.EscapeString(evidenceRequirement), html.EscapeString(missing), html.EscapeString(reviewNote)} {
			if !strings.Contains(body, expected) {
				t.Errorf("HTML export omitted %q", expected)
			}
		}
		for _, raw := range []string{unsafeName, criterion, evidenceRequirement, missing, reviewNote} {
			if strings.Contains(body, raw) {
				t.Errorf("HTML export rendered unescaped input %q", raw)
			}
		}
		if !strings.Contains(response.Header().Get("Content-Security-Policy"), "sandbox") {
			t.Error("HTML export lost its sandbox policy")
		}
	})
	t.Run("JSON", func(t *testing.T) {
		response := httptest.NewRecorder()
		writeReportExport(response, report, "json")
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") || !strings.Contains(response.Header().Get("Content-Disposition"), "scarlet-compliance-report.json") {
			t.Fatalf("unexpected JSON export response: %d, %v", response.Code, response.Header())
		}
		var decoded store.ComplianceReport
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, report) {
			t.Fatal("JSON export changed classification origin, standard/rule/evidence snapshot, scoped counts or independent review")
		}
	})
}
