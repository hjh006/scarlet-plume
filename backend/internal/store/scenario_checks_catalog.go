package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

// applyScenarioCatalogClassification reuses the catalog's existing bounded
// classification evaluator and immutable collection evidence. It does not infer
// source ACLs, statutory grades or unobserved fields from a content signal.
func applyScenarioCatalogClassification(ctx context.Context, tx pgx.Tx, workspaceID int64, run *ScenarioCheckRun, classification *ScenarioClassification, finding *ScenarioCheckFinding) error {
	if classification == nil || !strings.HasPrefix(classification.Item.Asset.SourceProvider, "discovery:") {
		return nil
	}
	finding.Outcome = "needs_verification"
	finding.Expected = "当前已采集字段及有界样本中的正向属性信号，与同一资产已人工确认的分类相容；未采集范围仍未知。"
	finding.Remediation = ScenarioCheckRemediation{Advice: "核对来源、采用规则与人工分类；需要时重新采集后再检查。", Reason: "分类差异通过已有人工确认流程处理，不生成数据库撤权或法定类别变更。"}
	var kind, field, provider, runRef string
	var assessment CatalogClassificationAssessment
	err := tx.QueryRow(ctx, `
		SELECT ce.kind,ce.field_name,ds.provider,dr.run_ref,ca.assessment,ca.id
		FROM plume.catalog_entries ce
		JOIN plume.discovery_sources ds ON ds.id=ce.source_id AND ds.workspace_id=ce.workspace_id
		JOIN plume.discovery_runs dr ON dr.id=ce.run_id AND dr.workspace_id=ce.workspace_id AND dr.source_id=ds.id
		LEFT JOIN plume.catalog_entries parent ON ce.kind='field' AND parent.workspace_id=ce.workspace_id
		 AND parent.source_id=ce.source_id AND parent.source_ref=ce.parent_ref AND parent.kind='table' AND parent.run_id=ce.run_id
		JOIN plume.catalog_classification_assessments ca ON ca.workspace_id=ce.workspace_id
		 AND ca.source_id=ce.source_id AND ca.source_revision=ce.source_revision AND ca.run_id=ce.run_id
		 AND ca.asset_id=CASE WHEN ce.kind='table' THEN ce.asset_id ELSE parent.asset_id END
		WHERE ce.workspace_id=$1 AND ce.asset_id=$2 AND ce.kind IN('table','field')
	`, workspaceID, run.AssetID).Scan(&kind, &field, &provider, &runRef, &assessment, &assessment.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		finding.MissingConditions = append(finding.MissingConditions, "当前目录对象缺少同范围表或字段的不可变来源评估。")
		return nil
	}
	if err != nil {
		return err
	}
	facts, err := catalogAssessmentFacts(ctx, tx, workspaceID, run.AssetID)
	if err != nil {
		return err
	}
	current := scenarioKnownFact(facts, "source_coverage")
	rules, err := readPublishedDiscoveryRules(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	rulesCurrent := discoveryDigest(rules) == discoveryDigest(assessment.AppliedRules)
	fields := assessment.Fields
	if kind == "field" {
		fields = slices.DeleteFunc(slices.Clone(fields), func(candidate collector.DiscoveryField) bool { return candidate.Name != field })
	}
	var evaluation *classificationstandards.Evaluation
	comparison := "insufficient"
	automatic := classification.Item.Automatic
	if automatic != nil {
		evaluation = &automatic.Evaluation
		comparison = automatic.Comparison
	}
	// A field uses only its own persisted evaluator result. The parent assessment
	// supplies the same-run collection boundary, never another field's matches.
	fieldScoped := kind != "field" || len(fields) == 1
	if evaluation != nil && kind == "field" {
		for _, fact := range evaluation.Facts {
			if fact.Column != "" && fact.Column != field {
				fieldScoped = false
			}
		}
	}
	content := false
	// sampled_row_count is already an existing evaluator fact; no row values or
	// reconstructed person counts are introduced by this adapter.
	var scannedRows int64
	if evaluation != nil {
		for _, fact := range evaluation.Facts {
			if fact.Kind == "sampled_row_count" && fact.Count != nil {
				scannedRows = *fact.Count
			}
		}
		content = catalogContentAvailable(collector.DiscoveryTable{Fields: fields, ScannedRows: scannedRows, Warnings: evaluationScopeWarnings(evaluation)}, assessment.Coverage)
	}
	record := fmt.Sprintf("catalog-assessment:%d/source:%d/revision:%d/run:%d/%s", assessment.ID, assessment.SourceID, assessment.SourceRevision, assessment.RunID, runRef)
	scope := fmt.Sprintf("%s目录；%s.%s.%s；%d个已采集字段，有界观察%d行；仅判断正向格式属性相容性。", provider, assessment.Database, assessment.Schema, assessment.Table, len(fields), scannedRows)
	if kind == "field" {
		scope += " 限定字段：" + field + "。"
	}
	evidence := map[string]any{
		"assessmentId": assessment.ID, "assessmentDigest": assessment.FactDigest,
		"sourceId": assessment.SourceID, "sourceRevision": assessment.SourceRevision, "runId": assessment.RunID, "runRef": runRef,
		"provider": provider, "assetId": run.AssetID, "field": field, "classificationRevision": classification.Item.Revision,
		"collectionStartedAt": assessment.CollectionStartedAt, "current": current, "rulesCurrent": rulesCurrent,
		"fields": fields, "evaluation": evaluation, "comparison": comparison, "coverage": assessment.Coverage, "appliedRules": assessment.AppliedRules,
	}
	finding.EvidenceRefs = append(finding.EvidenceRefs, scenarioEvidence(run, "catalog_assessment", "discovery", record, scope, &assessment.ObservedAt, evidence))
	finding.Coverage = scope
	finding.Observed = fmt.Sprintf("来源%s，采集运行%s；来源当前=%t，规则版本相符=%t，人工确认=%t，分类比对=%s。", provider, runRef, current, rulesCurrent, classification.ManuallyConfirmed, comparison)
	if kind == "table" {
		finding.MissingConditions = append(finding.MissingConditions, assessment.MissingConditions...)
	}
	if evaluation != nil {
		finding.RuleVersion = evaluation.Version
		finding.MissingConditions = append(finding.MissingConditions, evaluation.MissingConditions...)
	}
	switch {
	case !current:
		finding.MissingConditions = append(finding.MissingConditions, "目录来源失效、配置/身份/权限或最新采集不匹配，或已超过观察时效。")
	case !rulesCurrent:
		finding.MissingConditions = append(finding.MissingConditions, "已发布识别规则与该采集批次采用的完整规则版本不同，需重新采集。")
	case !fieldScoped:
		finding.MissingConditions = append(finding.MissingConditions, "字段缺少自身同运行的识别结果，不能复用其他字段的信号。")
	case provider == "openmetadata" || !content:
		finding.MissingConditions = append(finding.MissingConditions, "未取得该范围可用的正向内容统计；目录标签或元数据候选不能作为内容确认。")
	case automatic == nil || automatic.Stale || evaluation == nil || !evaluation.AutoApplicable || !classification.ManuallyConfirmed:
		finding.MissingConditions = append(finding.MissingConditions, "缺少与当前人工分类修订匹配、人工确认后新采集的正向识别结果。")
	case comparison == "conflict":
		finding.Outcome = "non_compliant"
		finding.Remediation.Advice = "核对该字段/有界样本的正向属性信号与人工分类差异，补充业务事实后重新确认分类，再取得新的来源观察；不自动修改法定类别。"
	case comparison == "matched" && classification.Usable && (kind == "field" || assessment.Eligible):
		finding.Outcome = "compliant"
	}
	slices.Sort(finding.MissingConditions)
	finding.MissingConditions = slices.Compact(finding.MissingConditions)
	return nil
}

func evaluationScopeWarnings(evaluation *classificationstandards.Evaluation) []string {
	warnings := []string{}
	for _, fact := range evaluation.Facts {
		if fact.Kind != "content_scope" {
			continue
		}
		parts := strings.SplitN(fact.Note, ";", 3)
		if len(parts) == 3 && parts[2] != "" {
			warnings = append(warnings, strings.Split(parts[2], ",")...)
		}
	}
	return warnings
}
