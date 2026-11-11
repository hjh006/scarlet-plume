package httpapi

import (
	"encoding/json"
	"html"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func TestCatalogAndRuntimeReportFactsEscaped(t *testing.T) {
	now := time.Date(2026, 11, 11, 6, 7, 8, 0, time.UTC)
	marker := `<script>alert('source')</script>`
	a := store.CatalogClassificationAssessment{ID: 42, SourceID: 3, SourceRevision: 2, RunID: 8, AssetID: 6, Source: marker, Provider: "mysql", System: "mysql", Database: "inventory", Table: "customers", ObservedAt: now, ClassificationRevision: 4, Origin: "manual", Comparison: "matched", Eligible: true, AppliedRules: []collector.DiscoveryRule{{Key: "customer.department", Version: 2, Name: marker, Target: "content"}}, Fields: []collector.DiscoveryField{{Name: marker, Hits: []collector.DiscoveryHit{{RuleKey: "customer.department", RuleVersion: 2, Category: "business", Method: "bounded_customer_sample", MatchCount: 3, ScannedCount: 8}}}}, Coverage: collector.DiscoveryCoverage{Metadata: "observed", Content: "bounded_sample", Permissions: "unknown", Lineage: "unknown", Warnings: []string{marker}}, MissingConditions: []string{marker}, Evaluation: &classificationstandards.Evaluation{Standard: classificationstandards.Standard{Code: "TEST-STD", Title: "冻结标准", Rules: []classificationstandards.Rule{}}, Rules: []classificationstandards.RuleEvaluation{{Rule: classificationstandards.Rule{ID: "rule-1", Clause: "5.1", Title: marker}, Status: "detected"}}}}
	detection := &store.RuntimeDetection{SourceID: 7, Source: marker, Policy: store.RuntimeDetectionPolicy{ID: 9, Revision: 3, Rule: "error_spike", WindowMinutes: 5, Threshold: 2, MinRequests: 5, ErrorPercent: 40}, WindowStart: now.Add(-5 * time.Minute), WindowEnd: now, Requests: 10, Matched: 4, Observed: 10, Warnings: []string{marker, "absolute_window_threshold_not_historical_baseline"}}
	report := store.ComplianceReport{Workspace: store.ReportWorkspace{Slug: "facts", Name: "Report facts"}, GeneratedAt: now, Scope: store.ReportScope{Basis: "test scope"}, CatalogDiscovery: &store.ReportCatalogDiscovery{Sources: []store.ReportCatalogSource{{ID: 3, Source: marker, Provider: "mysql", System: "mysql", Revision: 2, ObservedAt: &now, Current: true}}, Assessments: []store.ReportCatalogAssessment{{CheckID: 1, EvidenceID: 2, Kind: "retest", Current: true, Assessment: a}}, Tables: 1, Fields: 1, RuleHits: 1, Basis: "bounded catalog scope"}, Checks: []store.ReportCheck{{Check: store.Check{ID: 1, AssetID: 6, Status: "verified", CreatedAt: now, UpdatedAt: now}, Evidence: []store.Evidence{{ID: 2, Kind: "retest", SourceRecord: "snapshot", ObservedAt: now, CreatedAt: now, Catalog: &a}}, RuntimeFinding: &store.RuntimeFinding{Rule: "error_spike", Service: "API", Route: "/records", Method: "GET", Detection: detection}}}}

	checkID := int64(1)
	report.Operations = &store.ReportOperations{CapturedAt: now, Scope: "source receipts are not independent verification", Controls: []store.ControlAction{{ID: 12, Kind: "block_api", CheckID: &checkID, Actor: marker, ApprovedBy: "reviewer", Status: "applied", Revision: 2, Reason: marker, ExpiresAt: now.Add(time.Hour), Spec: store.ControlSpec{Service: "API", Method: "GET", Route: "/records"}, Result: &store.ControlResult{Phase: "apply", Success: true, Code: "route_blocked", ObservedAt: now}, Events: []store.ControlEvent{{Action: "approved", Actor: marker, Note: marker, CreatedAt: now}}}}}
	report.AssessmentReferences = &store.ReportAssessmentReferences{Basis: "related activity references only", Items: []store.ReportAssessmentReference{{ID: 21, Kind: "pia", Title: marker, Status: "approved", ActivityID: 2, TemplateRevisionID: 4, Revision: 3, UpdatedAt: now, Stale: true}}}
	w := httptest.NewRecorder()
	writeReportExport(w, report, "html")
	if w.Code != 200 {
		t.Fatalf("report failed %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, value := range []string{"不可变目录评估 #42", "customer.department v2", "命中3 / 观察8", "TEST-STD", "实际统计分母", "请求10", "信号命中4 / 可观察分母10", "v3 #9", "处置#12", "来源回执执行成功", "source receipts are not independent verification", "评估#21", "已标记需要重评", html.EscapeString(marker)} {
		if !strings.Contains(body, value) {
			t.Errorf("report omitted %q", value)
		}
	}
	if strings.Contains(body, marker) {
		t.Fatal("source/rule values rendered as executable HTML")
	}
	w = httptest.NewRecorder()
	writeReportExport(w, report, "json")
	var decoded store.ComplianceReport
	if json.Unmarshal(w.Body.Bytes(), &decoded) != nil || decoded.CatalogDiscovery.Assessments[0].Assessment.ID != 42 || decoded.Checks[0].RuntimeFinding.Detection.Observed != 10 {
		t.Fatal("structured report references or runtime denominator lost")
	}
}
