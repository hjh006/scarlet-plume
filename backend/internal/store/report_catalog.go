package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type ReportCatalogSource struct {
	ID         int64      `json:"id"`
	Source     string     `json:"source"`
	Provider   string     `json:"provider"`
	System     string     `json:"system"`
	Revision   int64      `json:"revision"`
	RunID      *int64     `json:"runId,omitempty"`
	Status     string     `json:"status"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	Current    bool       `json:"current"`
}
type ReportCatalogAssessment struct {
	CheckID    int64                           `json:"checkId"`
	EvidenceID int64                           `json:"evidenceId"`
	Kind       string                          `json:"kind"`
	Current    bool                            `json:"current"`
	Assessment CatalogClassificationAssessment `json:"assessment"`
}
type ReportCatalogDiscovery struct {
	Sources     []ReportCatalogSource     `json:"sources"`
	Assessments []ReportCatalogAssessment `json:"assessments"`
	Tables      int                       `json:"tables"`
	Fields      int                       `json:"fields"`
	RuleHits    int                       `json:"ruleHits"`
	Truncated   bool                      `json:"truncated"`
	Basis       string                    `json:"basis"`
	Warnings    []string                  `json:"warnings"`
}

func readReportCatalogDiscovery(ctx context.Context, tx pgx.Tx, ws int64, report *ComplianceReport) error {
	result := ReportCatalogDiscovery{Sources: []ReportCatalogSource{}, Assessments: []ReportCatalogAssessment{}, Warnings: []string{"native_connectors_and_openmetadata_have_different_verified_scope", "unknown_source_acl_is_not_a_permission_audit", "classification_evidence_is_bounded_and_not_statutory_determination"}, Basis: "工作区目录来源状态与本报告列出的检查证据分别列示；采用同一数据库快照。每项评估固定来源修订、运行、标准与规则、人工分类版本和缺条件。已验证是独立复核的历史状态，当前来源失效另行标记；统计不代表全量业务或整体合规。"}
	rows, e := tx.Query(ctx, `SELECT ds.id,ds.source,ds.provider,ds.system,ds.revision,ds.latest_run_id,COALESCE(dr.status,'not_observed'),dr.observed_at,COALESCE(ds.enabled AND ds.revision=dr.source_revision AND dr.status='success' AND dr.mode='collect' AND dr.observed_at>now()-make_interval(mins=>greatest(ds.interval_minutes*3,60)),false) FROM plume.discovery_sources ds LEFT JOIN plume.discovery_runs dr ON dr.id=ds.latest_run_id WHERE ds.workspace_id=$1 ORDER BY ds.id LIMIT 101`, ws)
	if e != nil {
		return e
	}
	for rows.Next() {
		var v ReportCatalogSource
		if e = rows.Scan(&v.ID, &v.Source, &v.Provider, &v.System, &v.Revision, &v.RunID, &v.Status, &v.ObservedAt, &v.Current); e != nil {
			rows.Close()
			return e
		}
		result.Sources = append(result.Sources, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(result.Sources) > 100 {
		result.Sources = result.Sources[:100]
		result.Truncated = true
	}
	e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='table'),count(*) FILTER(WHERE kind='field'),(SELECT count(*) FROM plume.discovery_rule_hits h JOIN plume.discovery_sources ds ON ds.id=(SELECT source_id FROM plume.discovery_runs WHERE id=h.run_id) AND ds.latest_run_id=h.run_id AND ds.workspace_id=h.workspace_id WHERE h.workspace_id=$1) FROM plume.catalog_entries WHERE workspace_id=$1`, ws).Scan(&result.Tables, &result.Fields, &result.RuleHits)
	if e != nil {
		return e
	}
	ids := []int64{}
	for _, check := range report.Checks {
		ids = append(ids, check.Check.ID)
	}
	rules, e := readPublishedDiscoveryRules(ctx, tx, ws)
	if e != nil {
		return e
	}
	rows, e = tx.Query(ctx, `SELECT e.check_id,e.id,e.kind,a.assessment,a.id,COALESCE(ds.enabled AND ds.revision=a.source_revision AND ce.run_id=ds.latest_run_id AND ca.fact_digest=a.fact_digest AND ast.classification_revision=a.classification_revision AND dr.status='success' AND dr.mode='collect' AND dr.observed_at>now()-make_interval(mins=>greatest(ds.interval_minutes*3,60)),false) FROM plume.check_evidence e JOIN plume.catalog_check_evidence pe ON pe.evidence_id=e.id JOIN plume.catalog_classification_assessments a ON a.id=pe.assessment_id JOIN plume.discovery_sources ds ON ds.id=a.source_id LEFT JOIN plume.catalog_entries ce ON ce.asset_id=a.asset_id AND ce.workspace_id=a.workspace_id LEFT JOIN plume.discovery_runs dr ON dr.id=ds.latest_run_id LEFT JOIN plume.catalog_classification_assessments ca ON ca.run_id=ds.latest_run_id AND ca.asset_id=a.asset_id JOIN plume.assets ast ON ast.id=a.asset_id AND ast.workspace_id=a.workspace_id WHERE a.workspace_id=$1 AND e.check_id=ANY($2) ORDER BY e.check_id,e.id DESC LIMIT 501`, ws, ids)
	if e != nil {
		return e
	}
	for rows.Next() {
		var v ReportCatalogAssessment
		if e = rows.Scan(&v.CheckID, &v.EvidenceID, &v.Kind, &v.Assessment, &v.Assessment.ID, &v.Current); e != nil {
			rows.Close()
			return e
		}
		if discoveryDigest(rules) != discoveryDigest(v.Assessment.AppliedRules) {
			v.Current = false
		}
		result.Assessments = append(result.Assessments, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(result.Assessments) > 500 {
		result.Assessments = result.Assessments[:500]
		result.Truncated = true
	}
	report.CatalogDiscovery = &result
	return nil
}
func loadCatalogCheckDetail(ctx context.Context, tx pgx.Tx, detail *CheckDetail) error {
	for i := range detail.Evidence {
		var assessment CatalogClassificationAssessment
		e := tx.QueryRow(ctx, `SELECT a.assessment,a.id FROM plume.catalog_check_evidence e JOIN plume.catalog_classification_assessments a ON a.id=e.assessment_id JOIN plume.check_evidence ce ON ce.id=e.evidence_id WHERE ce.check_id=$1 AND e.evidence_id=$2`, detail.ID, detail.Evidence[i].ID).Scan(&assessment, &assessment.ID)
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		detail.Evidence[i].Catalog = &assessment
	}
	return nil
}
