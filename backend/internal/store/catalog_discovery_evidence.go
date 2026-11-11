package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

type CatalogClassificationAssessment struct {
	CollectionStartedAt    time.Time                           `json:"collectionStartedAt"`
	ID                     int64                               `json:"id"`
	SourceID               int64                               `json:"sourceId"`
	SourceRevision         int64                               `json:"sourceRevision"`
	RunID                  int64                               `json:"runId"`
	AssetID                int64                               `json:"assetId"`
	Source                 string                              `json:"source"`
	Provider               string                              `json:"provider"`
	System                 string                              `json:"system"`
	Database               string                              `json:"database"`
	Schema                 string                              `json:"schema"`
	Table                  string                              `json:"table"`
	ObservedAt             time.Time                           `json:"observedAt"`
	ClassificationRevision int64                               `json:"classificationRevision"`
	Origin                 string                              `json:"origin"`
	Comparison             string                              `json:"comparison"`
	Reasons                []string                            `json:"reasons"`
	Eligible               bool                                `json:"eligible"`
	Evaluation             *classificationstandards.Evaluation `json:"evaluation,omitempty"`
	Fields                 []collector.DiscoveryField          `json:"fields"`
	AppliedRules           []collector.DiscoveryRule           `json:"appliedRules"`
	Coverage               collector.DiscoveryCoverage         `json:"coverage"`
	MissingConditions      []string                            `json:"missingConditions"`
	FactDigest             string                              `json:"factDigest"`
	PreviousCheckID        *int64                              `json:"previousCheckId,omitempty"`
}

func catalogAssessmentDigest(a CatalogClassificationAssessment) string {
	a.ID = 0
	a.RunID = 0
	a.ObservedAt = time.Time{}
	a.CollectionStartedAt = time.Time{}
	a.FactDigest = ""
	a.PreviousCheckID = nil
	return discoveryDigest(a)
}
func catalogContentAvailable(table collector.DiscoveryTable, coverage collector.DiscoveryCoverage) bool {
	if table.ScannedRows <= 0 || !slices.Contains([]string{"bounded_sample", "source_aggregate_and_bounded_customer_custom_rules", "source_aggregate_builtin_only"}, coverage.Content) || coverage.Metadata == "" {
		return false
	}
	for _, w := range append(slices.Clone(table.Warnings), coverage.Warnings...) {
		if strings.Contains(w, "permission") && w != "source_permissions_unknown" && w != "permissions_unknown" || slices.Contains([]string{"aggregate_unavailable", "aggregate_timeout", "sample_incomplete", "content_budget_exceeded", "custom_content_sample_unavailable", "custom_content_unavailable", "columns_without_select", "source_relation_changed"}, w) {
			return false
		}
	}
	return true
}
func persistCatalogAssessment(ctx context.Context, tx pgx.Tx, ws, actor, assetID, runID int64, source DiscoverySource, in DiscoverySubmission, table collector.DiscoveryTable, evaluation *classificationstandards.Evaluation) error {
	item, e := scanClassificationAsset(tx.QueryRow(ctx, "SELECT "+assetColumns+`,classification_revision,classification_standard,classification_origin FROM plume.assets WHERE workspace_id=$1 AND id=$2`, ws, assetID))
	if e != nil {
		return e
	}
	rules, e := readPublishedDiscoveryRules(ctx, tx, ws)
	if e != nil {
		return e
	}
	a := CatalogClassificationAssessment{SourceID: source.ID, SourceRevision: source.Revision, RunID: runID, AssetID: assetID, Source: source.Source, Provider: source.Provider, System: source.System, Database: source.Database, Schema: table.Schema, Table: table.Name, ObservedAt: in.ObservedAt, ClassificationRevision: item.Revision, Origin: item.Origin, Evaluation: evaluation, Fields: table.Fields, AppliedRules: rules, Coverage: in.Result.Coverage, MissingConditions: []string{}}
	if evaluation == nil {
		a.Comparison = "insufficient"
		a.Reasons = []string{"当前采用标准没有可用自动规则"}
		a.MissingConditions = append(a.MissingConditions, "adopted_standard_unavailable")
	} else {
		a.Comparison, a.Reasons = compareAutomaticClassification(item, *evaluation)
		a.MissingConditions = append(a.MissingConditions, evaluation.MissingConditions...)
		a.Eligible = evaluation.AutoApplicable && a.Comparison == "matched" && item.Origin == "manual" && catalogContentAvailable(table, in.Result.Coverage)
	}

	// The fixed two-minute task lease begins before the collector reads rows.
	// A manual edit during an in-flight collection requires a subsequent lease.
	if e = tx.QueryRow(ctx, `SELECT lease_until-interval '2 minutes' FROM plume.discovery_sources WHERE workspace_id=$1 AND id=$2`, ws, source.ID).Scan(&a.CollectionStartedAt); e != nil {
		return e
	}
	if item.Origin == "manual" {
		var confirmedAt time.Time
		e = tx.QueryRow(ctx, `SELECT created_at FROM plume.asset_classification_history WHERE workspace_id=$1 AND asset_id=$2 AND revision=$3 AND origin='manual'`, ws, assetID, item.Revision).Scan(&confirmedAt)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if errors.Is(e, pgx.ErrNoRows) || !a.CollectionStartedAt.After(confirmedAt) || !a.ObservedAt.After(confirmedAt) {
			a.Eligible = false
			a.MissingConditions = append(a.MissingConditions, "fresh_source_after_manual_confirmation_required")
		}
	}
	if item.Origin != "manual" {
		a.MissingConditions = append(a.MissingConditions, "manual_classification_confirmation_required")
	}
	if !catalogContentAvailable(table, in.Result.Coverage) {
		a.MissingConditions = append(a.MissingConditions, "current_positive_content_scope_required")
	}
	a.FactDigest = catalogAssessmentDigest(a)
	e = tx.QueryRow(ctx, `INSERT INTO plume.catalog_classification_assessments(workspace_id,source_id,source_revision,run_id,asset_id,classification_revision,fact_digest,assessment) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, ws, source.ID, source.Revision, runID, assetID, item.Revision, a.FactDigest, a).Scan(&a.ID)
	if e != nil {
		return e
	}
	return ensureCatalogAssessmentCheck(ctx, tx, ws, actor, a)
}
func ensureCatalogAssessmentCheck(ctx context.Context, tx pgx.Tx, ws, actor int64, a CatalogClassificationAssessment) error {
	var check Check
	var previous *int64
	e := tx.QueryRow(ctx, `SELECT c.id,c.status,b.previous_check_id FROM plume.catalog_check_bindings b JOIN plume.checks c ON c.id=b.check_id AND c.workspace_id=b.workspace_id WHERE b.workspace_id=$1 AND b.source_id=$2 AND b.asset_id=$3 ORDER BY c.id DESC LIMIT 1 FOR NO KEY UPDATE OF c`, ws, a.SourceID, a.AssetID).Scan(&check.ID, &check.Status, &previous)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if e == nil && check.Status == "verified" {
		if a.Comparison != "conflict" {
			return nil
		}
		p := check.ID
		previous = &p
		check = Check{}
	}
	if check.ID == 0 {
		e = tx.QueryRow(ctx, `INSERT INTO plume.checks(workspace_id,asset_id,requirement_code,owner_team,note,created_by) SELECT $1,id,'G02',COALESCE(NULLIF(left(owner_team,80),''),'自动采集'),'由多源目录采集创建。核对采用标准、客户规则、正向统计与人工分类；法定级别和完整影响条件独立取证。',$3 FROM plume.assets WHERE workspace_id=$1 AND id=$2 RETURNING id,status`, ws, a.AssetID, actor).Scan(&check.ID, &check.Status)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO plume.catalog_check_bindings(check_id,workspace_id,source_id,asset_id,source_revision,previous_check_id) VALUES($1,$2,$3,$4,$5,$6)`, check.ID, ws, a.SourceID, a.AssetID, a.SourceRevision, previous)
		if e != nil {
			return e
		}
		note := "由多源目录采集创建；来源、采用标准与分类修订固定到证据快照。"
		if previous != nil {
			note = fmt.Sprintf("已验证检查#%d之后出现新的分类冲突，创建新检查周期。", *previous)
		}
		if e = recordCheckEvent(ctx, tx, check.ID, actor, "created", note); e != nil {
			return e
		}
	}
	if !evidenceOpen(check) {
		return nil
	}
	kind := "observation"
	var cycle int64
	if check.Status == "needs_work" {
		change, e := latestCheckEvent(ctx, tx, check.ID, "changes")
		if e != nil {
			return e
		}
		remediated, e := checkRemediationAt(ctx, tx, check.ID, change)
		if e != nil {
			return e
		}
		if remediated.IsZero() || !a.ObservedAt.After(remediated) || !a.Eligible {
			return nil
		}
		kind = "retest"
		cycle = change.ID
	}
	var duplicate bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.check_evidence e JOIN plume.catalog_check_evidence ce ON ce.evidence_id=e.id WHERE e.check_id=$1 AND ce.cycle_id=$2 AND ce.fact_digest=$3)`, check.ID, cycle, a.FactDigest).Scan(&duplicate)
	if e != nil || duplicate {
		return e
	}
	code := "标准待核对"
	if a.Evaluation != nil {
		code = a.Evaluation.Standard.Code
	}
	summary := fmt.Sprintf("%s/%s.%s.%s；采用%s，分类修订%d，当前比对%s，规则%d项、字段%d项。候选和有界统计不表示法定级别或实际人数；人工确认及缺条件见不可变目录评估#%d。", a.System, a.Database, a.Schema, a.Table, code, a.ClassificationRevision, a.Comparison, len(a.AppliedRules), len(a.Fields), a.ID)
	if len([]rune(summary)) > 580 {
		summary = string([]rune(summary)[:580])
	}
	check.AssetID = a.AssetID
	check.Requirement.Code = "G02"
	evidenceID, e := addEvidenceTx(ctx, tx, check, actor, EvidenceInput{Kind: kind, SourceSystem: "多源目录自动分类", SourceRecord: fmt.Sprintf("来源#%d · 运行#%d · 评估#%d", a.SourceID, a.RunID, a.ID), Summary: summary, ObservedAt: a.ObservedAt}, true)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO plume.catalog_check_evidence(evidence_id,assessment_id,cycle_id,fact_digest) VALUES($1,$2,$3,$4)`, evidenceID, a.ID, cycle, a.FactDigest)
	return e
}

// catalogClassificationCheckAllowed is called by both submission and independent verification.
func catalogClassificationCheckAllowed(ctx context.Context, tx pgx.Tx, check Check, retest bool) (bool, bool, error) {
	if check.Requirement.Code != "G02" {
		return false, false, nil
	}
	var ws, sourceID, assetID int64
	e := tx.QueryRow(ctx, `SELECT workspace_id,source_id,asset_id FROM plume.catalog_check_bindings WHERE check_id=$1`, check.ID).Scan(&ws, &sourceID, &assetID)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, false, nil
	}
	if e != nil {
		return true, false, e
	}
	var a CatalogClassificationAssessment
	var observed, created time.Time
	var kind string
	var sourceRevision, runID int64
	var enabled bool
	e = tx.QueryRow(ctx, `SELECT a.assessment,a.id,e.kind,e.observed_at,e.created_at,ds.revision,ds.latest_run_id,ds.enabled FROM plume.catalog_check_evidence ce JOIN plume.check_evidence e ON e.id=ce.evidence_id JOIN plume.catalog_classification_assessments a ON a.id=ce.assessment_id JOIN plume.discovery_sources ds ON ds.id=a.source_id AND ds.workspace_id=a.workspace_id WHERE e.check_id=$1 AND a.workspace_id=$2 ORDER BY e.id DESC LIMIT 1`, check.ID, ws).Scan(&a, &a.ID, &kind, &observed, &created, &sourceRevision, &runID, &enabled)
	if errors.Is(e, pgx.ErrNoRows) {
		return true, false, nil
	}
	if e != nil {
		return true, false, e
	}
	if !enabled || a.SourceID != sourceID || a.AssetID != check.AssetID || assetID != check.AssetID || a.SourceRevision != sourceRevision {
		return true, false, nil
	}
	var current CatalogClassificationAssessment
	var currentID int64
	var fresh bool
	e = tx.QueryRow(ctx, `SELECT a.assessment,a.id,dr.status='success' AND dr.mode='collect' AND dr.observed_at>now()-make_interval(mins=>greatest(ds.interval_minutes*3,60)) AND ce.run_id=a.run_id FROM plume.catalog_classification_assessments a JOIN plume.discovery_sources ds ON ds.id=a.source_id JOIN plume.discovery_runs dr ON dr.id=a.run_id JOIN plume.catalog_entries ce ON ce.asset_id=a.asset_id AND ce.workspace_id=a.workspace_id WHERE a.workspace_id=$1 AND a.source_id=$2 AND a.asset_id=$3 AND a.run_id=$4 AND a.source_revision=$5`, ws, sourceID, assetID, runID, sourceRevision).Scan(&current, &currentID, &fresh)
	if errors.Is(e, pgx.ErrNoRows) {
		return true, false, nil
	}
	if e != nil {
		return true, false, e
	}
	if !fresh || !current.Eligible || current.Comparison != "matched" || current.FactDigest != a.FactDigest {
		return true, false, nil
	}
	item, e := scanClassificationAsset(tx.QueryRow(ctx, "SELECT "+assetColumns+`,classification_revision,classification_standard,classification_origin FROM plume.assets WHERE workspace_id=$1 AND id=$2`, ws, assetID))
	if e != nil {
		return true, false, e
	}
	defaultID, e := classificationDefaultTx(ctx, tx, ws)
	if e != nil {
		return true, false, e
	}
	if item.Origin != "manual" || item.Revision != current.ClassificationRevision || current.Evaluation == nil || selectedClassificationStandard(item, defaultID) != current.Evaluation.Standard.ID {
		return true, false, nil
	}
	rules, e := readPublishedDiscoveryRules(ctx, tx, ws)
	if e != nil {
		return true, false, e
	}
	if discoveryDigest(rules) != discoveryDigest(current.AppliedRules) {
		return true, false, nil
	}
	if !retest {
		return true, true, nil
	}
	change, e := latestCheckEvent(ctx, tx, check.ID, "changes")
	if e != nil || change.ID == 0 || kind != "retest" {
		return true, false, e
	}
	remediated, e := checkRemediationAt(ctx, tx, check.ID, change)
	return true, e == nil && !remediated.IsZero() && observed.After(remediated) && created.After(remediated), e
}
