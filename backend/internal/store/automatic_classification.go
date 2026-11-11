package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

type AutomaticClassification struct {
	JobID                  int64                              `json:"jobId"`
	RunID                  string                             `json:"runId"`
	ObservedAt             time.Time                          `json:"observedAt"`
	ClassificationRevision int64                              `json:"classificationRevision"`
	Comparison             string                             `json:"comparison"`
	ComparisonReasons      []string                           `json:"comparisonReasons"`
	Stale                  bool                               `json:"stale,omitempty"`
	Evaluation             classificationstandards.Evaluation `json:"evaluation"`
}

func classificationDefaultTx(ctx context.Context, tx pgx.Tx, workspaceID int64) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, "SELECT COALESCE(classification_standard_id, $2) FROM plume.workspaces WHERE id = $1",
		workspaceID, classificationstandards.DefaultID).Scan(&id); err != nil {
		return "", storeError(err)
	}
	active, err := classificationstandards.IsSelectable(id)
	if !active {
		id = classificationstandards.DefaultID
	}
	return id, err
}

func selectedClassificationStandard(item AssetClassificationItem, defaultID string) string {
	if item.Standard != nil {
		return item.Standard.ID
	}
	return defaultID
}

func persistAutomaticClassifications(ctx context.Context, tx pgx.Tx, job AutomationJob, run AutomationRun,
	tables []collector.PostgreSQLTable, assetIDs map[string]int64,
) error {
	defaultID, err := classificationDefaultTx(ctx, tx, job.workspaceID)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(assetIDs))
	byID := make(map[int64]collector.PostgreSQLTable, len(tables))
	for _, table := range tables {
		id := assetIDs[table.SourceRef]
		ids = append(ids, id)
		byID[id] = table
	}
	slices.Sort(ids)
	rows, err := tx.Query(ctx, "SELECT "+assetColumns+`, classification_revision, classification_standard, classification_origin
		FROM plume.assets WHERE workspace_id = $1 AND id = ANY($2) ORDER BY id FOR NO KEY UPDATE`, job.workspaceID, ids)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssetClassificationItem, error) {
		return scanClassificationAsset(row)
	})
	if err != nil {
		return err
	}
	for _, item := range items {
		standardID := selectedClassificationStandard(item, defaultID)
		evaluation, err := classificationstandards.EvaluatePostgreSQL(byID[item.Asset.ID], standardID)
		if errors.Is(err, classificationstandards.ErrSelection) {
			standard := classificationstandards.Standard{ID: standardID}
			if item.Standard != nil {
				standard.Code, standard.Title, standard.URL = item.Standard.Code, item.Standard.Title, item.Standard.URL
				standard.Industry, standard.Kind = item.Standard.Industry, item.Standard.Kind
			}
			evaluation = classificationstandards.Evaluation{
				Version: "unavailable", Standard: standard, Status: "insufficient_data", Category: "unknown", BusinessLevel: "unknown",
				Rules: []classificationstandards.RuleEvaluation{}, Facts: []classificationstandards.Fact{},
				MissingConditions: []string{"所采用的标准版本尚无可用自动规则，请核对正式版本及条款。"},
			}
		} else if err != nil {
			return err
		}
		if err := applyAutomaticClassification(ctx, tx, job, &item, evaluation); err != nil {
			return err
		}
		automatic := AutomaticClassification{
			JobID: job.ID, RunID: run.RunID, ObservedAt: run.ObservedAt, ClassificationRevision: item.Revision,
			Evaluation: evaluation,
		}
		automatic.Comparison, automatic.ComparisonReasons = compareAutomaticClassification(item, evaluation)
		if item.Origin == "manual" && item.Revision > 0 {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plume.asset_classification_history
				WHERE asset_id = $1 AND revision = $2 AND created_at >= $3)`, item.Asset.ID, item.Revision, run.ObservedAt).Scan(&automatic.Stale); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE plume.postgres_asset_inspections SET classification = $2 WHERE asset_id = $1 AND automation_run_id = $3",
			item.Asset.ID, automatic, run.ID); err != nil {
			return err
		}
	}
	return nil
}

func classificationCategoryRank(category string) int {
	switch category {
	case "personal":
		return 1
	case "sensitive_personal":
		return 2
	}
	return 0
}

func classificationBusinessRank(level string) int {
	switch level {
	case "internal":
		return 1
	case "confidential":
		return 2
	}
	return 0
}

func applyAutomaticClassification(ctx context.Context, tx pgx.Tx, job AutomationJob, item *AssetClassificationItem, evaluation classificationstandards.Evaluation) error {
	previous := item.Asset
	pristine := item.Revision == 0 && previous.Category == "unknown" && previous.BusinessLevel == "unknown" &&
		previous.StatutoryCategory == "unknown" && previous.ClassificationBasis == ""
	if !evaluation.AutoApplicable || !pristine && item.Origin != "automatic" {
		return nil
	}
	category, businessLevel := previous.Category, previous.BusinessLevel
	if classificationCategoryRank(evaluation.Category) > classificationCategoryRank(category) {
		category = evaluation.Category
	}
	if classificationBusinessRank(evaluation.BusinessLevel) > classificationBusinessRank(businessLevel) {
		businessLevel = evaluation.BusinessLevel
	}
	reference := &classificationstandards.Reference{
		ID: evaluation.Standard.ID, Code: evaluation.Standard.Code, Title: evaluation.Standard.Title,
		Industry: evaluation.Standard.Industry, Kind: evaluation.Standard.Kind, URL: evaluation.Standard.URL,
		LevelCode: evaluation.LevelCode,
	}
	for _, rule := range evaluation.Rules {
		if rule.Status == "detected" {
			reference.RuleID, reference.RuleClause, reference.RuleTitle = rule.Rule.ID, rule.Rule.Clause, rule.Rule.Title
			reference.RuleCriterion, reference.RuleEvidence = rule.Rule.Criterion, rule.Rule.Evidence
			break
		}
	}
	if category == previous.Category && businessLevel == previous.BusinessLevel && reflect.DeepEqual(reference, item.Standard) {
		return nil
	}
	basis := fmt.Sprintf("依据 %s 和 PostgreSQL 源内格式聚合自动识别数据属性。实际主体、业务用途及法定类别所需的影响评估继续采证。", evaluation.Standard.Code)
	if err := tx.QueryRow(ctx, `UPDATE plume.assets SET category = $2, business_level = $3,
		classification_revision = classification_revision + 1, classification_origin = 'automatic',
		classification_standard = $4, classification_basis = $5 WHERE id = $1 RETURNING classification_revision`,
		previous.ID, category, businessLevel, reference, basis).Scan(&item.Revision); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO plume.asset_classification_history
		(workspace_id, asset_id, revision, actor_id, category, business_level, statutory_category, basis,
		 previous_category, previous_business_level, previous_statutory_category, previous_basis, standard_reference, origin)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'automatic')`,
		job.workspaceID, previous.ID, item.Revision, job.actorID, category, businessLevel, previous.StatutoryCategory, basis,
		previous.Category, previous.BusinessLevel, previous.StatutoryCategory, previous.ClassificationBasis, reference)
	if err == nil {
		item.Asset.Category, item.Asset.BusinessLevel, item.Asset.ClassificationBasis = category, businessLevel, basis
		item.Standard, item.Origin = reference, "automatic"
	}
	return err
}

func compareAutomaticClassification(item AssetClassificationItem, evaluation classificationstandards.Evaluation) (string, []string) {
	if !evaluation.AutoApplicable {
		return "insufficient", []string{"已记录当前可观测条件，完整分类所需条件见取证缺口。"}
	}
	if item.Revision == 0 {
		return "unconfirmed", []string{"当前已有分类尚未形成版本确认，自动结果已保留。"}
	}
	var reasons []string
	if classificationCategoryRank(evaluation.Category) > classificationCategoryRank(item.Asset.Category) {
		reasons = append(reasons, "源内格式统计识别的数据属性高于当前记录。")
	}
	if classificationBusinessRank(evaluation.BusinessLevel) > classificationBusinessRank(item.Asset.BusinessLevel) {
		reasons = append(reasons, "源内格式统计对应的敏感程度高于当前内部级别。")
	}
	if len(reasons) > 0 {
		return "conflict", reasons
	}
	return "matched", []string{"本次自动识别的数据属性与当前记录相容，其他定级条件按缺口继续核验。"}
}

func loadAutomaticClassifications(ctx context.Context, tx pgx.Tx, workspaceID int64, items []AssetClassificationItem) error {
	ids := make([]int64, 0, len(items))
	positions := make(map[int64]int, len(items))
	for i := range items {
		ids = append(ids, items[i].Asset.ID)
		positions[items[i].Asset.ID] = i
	}
	if len(ids) == 0 {
		return nil
	}
	defaultID, err := classificationDefaultTx(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT i.asset_id, i.classification,
		j.revoked_at IS NULL AND (i.source_host, i.source_port, i.source_database) = (p.host, p.port, p.database_name)
		AND i.automation_run_id = (SELECT latest.id FROM plume.automation_runs latest WHERE latest.job_id = j.id ORDER BY latest.id DESC LIMIT 1)
		AND (a.classification_origin = 'automatic' OR h.id IS NULL OR r.observed_at > h.created_at)
		FROM plume.postgres_asset_inspections i
		JOIN plume.automation_jobs j ON j.id = i.job_id
		JOIN plume.managed_postgres_jobs p ON p.job_id = i.job_id
		JOIN plume.assets a ON a.id = i.asset_id AND a.workspace_id = j.workspace_id
		JOIN plume.automation_runs r ON r.id = i.automation_run_id
		LEFT JOIN plume.asset_classification_history h ON h.asset_id = a.id AND h.revision = a.classification_revision
		WHERE j.workspace_id = $1 AND i.asset_id = ANY($2) AND i.classification IS NOT NULL`, workspaceID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var automatic AutomaticClassification
		var sameSource bool
		if err := rows.Scan(&id, &automatic, &sameSource); err != nil {
			return err
		}
		item := &items[positions[id]]
		automatic.Stale = !sameSource || automatic.ClassificationRevision != item.Revision ||
			automatic.Evaluation.Standard.ID != selectedClassificationStandard(*item, defaultID)
		item.Automatic = &automatic
	}
	return rows.Err()
}

// Source timestamps and run IDs change on every poll. Evidence deduplication
// compares observed facts, adopted rules and the classification version.
func automaticClassificationFacts(automatic *AutomaticClassification) json.RawMessage {
	if automatic == nil {
		return nil
	}
	facts := *automatic
	facts.JobID, facts.RunID, facts.ObservedAt = 0, "", time.Time{}
	encoded, _ := json.Marshal(facts)
	return encoded
}

func readClassifications(ctx context.Context, tx pgx.Tx, workspaceID int64, ids []int64) ([]AssetClassificationItem, error) {
	rows, err := tx.Query(ctx, "SELECT "+assetColumns+`, classification_revision, classification_standard, classification_origin
		FROM plume.assets WHERE workspace_id = $1 AND id = ANY($2) ORDER BY id`, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AssetClassificationItem, error) {
		return scanClassificationAsset(row)
	})
	if err != nil {
		return nil, err
	}
	return items, loadAutomaticClassifications(ctx, tx, workspaceID, items)
}

func lockClassificationAssetForCheck(ctx context.Context, tx pgx.Tx, workspaceID, checkID int64) error {
	var id int64
	err := tx.QueryRow(ctx, `SELECT a.id FROM plume.assets a JOIN plume.checks c ON c.asset_id = a.id AND c.workspace_id = a.workspace_id
		WHERE c.workspace_id = $1 AND c.id = $2 AND c.requirement_code = 'G02' FOR SHARE OF a`, workspaceID, checkID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// Automated classification evidence remains bound to its original PostgreSQL
// target. A manual retest cannot stand in for a subsequent source observation.
func automaticClassificationCheckAllowed(ctx context.Context, tx pgx.Tx, check Check, retest bool) (bool, bool, error) {
	if check.Requirement.Code != "G02" || check.FeishuTargetDigest != "" {
		return false, false, nil
	}
	var recorded *AutomaticClassification
	var kind string
	var observedAt, createdAt time.Time
	var workspaceID int64
	err := tx.QueryRow(ctx, `SELECT p.observation->'classification', e.kind, e.observed_at, e.created_at, c.workspace_id
		FROM plume.check_evidence e JOIN plume.postgres_check_evidence p ON p.evidence_id = e.id
		JOIN plume.checks c ON c.id = e.check_id
		JOIN plume.postgres_check_bindings b ON b.check_id = c.id
		JOIN plume.automation_runs r ON r.id = p.automation_run_id
		WHERE e.check_id = $1 AND p.observation->'classification' IS NOT NULL
		ORDER BY e.id DESC LIMIT 1`, check.ID).Scan(&recorded, &kind, &observedAt, &createdAt, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil || recorded == nil {
		return true, false, err
	}
	items, err := readClassifications(ctx, tx, workspaceID, []int64{check.AssetID})
	if err != nil || len(items) != 1 {
		return true, false, err
	}
	current := items[0].Automatic
	if current == nil || current.Stale || current.Comparison == "conflict" ||
		string(automaticClassificationFacts(current)) != string(automaticClassificationFacts(recorded)) {
		return true, false, nil
	}
	var sameTarget bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plume.postgres_check_bindings b
		JOIN plume.postgres_asset_inspections i ON i.asset_id = $2 AND i.job_id = b.job_id
		WHERE b.check_id = $1 AND (b.source_host, b.source_port, b.source_database) =
		(i.source_host, i.source_port, i.source_database))`, check.ID, check.AssetID).Scan(&sameTarget); err != nil {
		return true, false, err
	}
	if !sameTarget {
		return true, false, nil
	}
	if !retest {
		return true, true, nil
	}
	change, err := latestCheckEvent(ctx, tx, check.ID, "changes")
	if err != nil || change.ID == 0 || kind != "retest" {
		return true, false, err
	}
	remediatedAt, err := checkRemediationAt(ctx, tx, check.ID, change)
	return true, err == nil && !remediatedAt.IsZero() && observedAt.After(remediatedAt) && createdAt.After(remediatedAt), err
}
