package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/jackc/pgx/v5"
)

// loadCatalogClassificationCandidates supplements the PostgreSQL/OpenAPI loader
// with field-level evidence from discovered tables and individual field assets.
func loadCatalogClassificationCandidates(ctx context.Context, tx pgx.Tx, workspaceID int64, items []AssetClassificationItem) error {
	ids := []int64{}
	positions := map[int64]int{}
	for index, item := range items {
		if strings.HasPrefix(item.Asset.SourceProvider, "discovery:") {
			ids = append(ids, item.Asset.ID)
			positions[item.Asset.ID] = index
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		WITH target_fields AS (
			SELECT target.asset_id,ce.asset_id AS field_asset_id,CASE WHEN ce.kind='file' THEN 'text' ELSE ce.field_name END AS field_name,ce.metadata,
			       ce.observed_at,ce.source_id,ce.run_id,dr.run_ref,ds.provider,
			       COALESCE(`+catalogCurrent+`,false) AND target.run_id=ce.run_id AS current
			FROM plume.catalog_entries target
			JOIN plume.catalog_entries ce ON ce.workspace_id=target.workspace_id
			 AND ce.source_id=target.source_id AND ce.kind IN('field','file')
			 AND ((target.kind IN('field','file') AND ce.id=target.id) OR (target.kind='table' AND ce.parent_ref=target.source_ref))
			LEFT JOIN plume.discovery_sources ds ON ds.id=ce.source_id AND ds.workspace_id=ce.workspace_id
			LEFT JOIN plume.discovery_runs dr ON dr.id=ce.run_id AND dr.workspace_id=ce.workspace_id
			WHERE target.workspace_id=$1 AND target.asset_id=ANY($2)
		), candidates AS (
			SELECT asset_id,field_name,metadata->>'sensitivityCandidate' AS category,
			       'discovery:'||provider AS provider,'metadata'::text AS method,
			       observed_at,source_id,run_ref,current,''::text AS rule_key,0::integer AS rule_version,
			       NULL::bigint AS match_count,NULL::bigint AS scanned_count
			FROM target_fields WHERE COALESCE(metadata->>'sensitivityCandidate','')<>''
			UNION ALL
			SELECT f.asset_id,f.field_name,h.category,'discovery:'||f.provider,h.method,
			       f.observed_at,f.source_id,f.run_ref,f.current,h.rule_key,h.rule_version,h.match_count,h.scanned_count
			FROM target_fields f JOIN plume.discovery_rule_hits h
			 ON h.workspace_id=$1 AND h.asset_id=f.field_asset_id AND h.run_id=f.run_id
		), ranked AS (
			SELECT *,count(*) OVER(PARTITION BY asset_id) AS candidate_count,
			       bool_or(current AND category IN('national_id','financial_account')) OVER(PARTITION BY asset_id) AS sensitive,
			       bool_or(current AND category IN('email','phone','personal_name','address','ip_address')) OVER(PARTITION BY asset_id) AS personal,
			       bool_or(current AND category='credential') OVER(PARTITION BY asset_id) AS credential,
			       row_number() OVER(PARTITION BY asset_id ORDER BY current DESC,
			         (category IN('national_id','financial_account','credential')) DESC,field_name,category,method,rule_key,rule_version) AS position
			FROM candidates WHERE category<>'' AND char_length(field_name) BETWEEN 1 AND 256
		)
		SELECT asset_id,field_name,category,provider,method,observed_at,source_id,run_ref,current,
		       rule_key,rule_version,match_count,scanned_count,candidate_count,sensitive,personal,credential
		FROM ranked WHERE position<=$3 ORDER BY asset_id,position
	`, workspaceID, ids, classificationCandidateCap)
	if err != nil {
		return err
	}
	for rows.Next() {
		var candidate ClassificationCandidate
		var assetID int64
		var count int
		var sensitive, personal, credential bool
		if err := rows.Scan(&assetID, &candidate.Name, &candidate.Category, &candidate.Source, &candidate.Method,
			&candidate.ObservedAt, &candidate.SourceID, &candidate.RunID, &candidate.Current, &candidate.RuleKey,
			&candidate.RuleVersion, &candidate.MatchCount, &candidate.ScannedCount, &count, &sensitive, &personal, &credential); err != nil {
			rows.Close()
			return err
		}
		candidate.Location = "column"
		item := &items[positions[assetID]]
		item.Candidates = append(item.Candidates, candidate)
		item.CandidateCount = count
		switch {
		case sensitive:
			item.SuggestedCategory = "sensitive_personal"
		case personal:
			item.SuggestedCategory = "personal"
		}
		if sensitive || credential {
			item.SuggestedBusinessLevel = "confidential"
		} else if personal {
			item.SuggestedBusinessLevel = "internal"
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	return loadCatalogAutomaticClassifications(ctx, tx, workspaceID, ids, positions, items)
}

func loadCatalogAutomaticClassifications(ctx context.Context, tx pgx.Tx, workspaceID int64, ids []int64, positions map[int64]int, items []AssetClassificationItem) error {
	defaultID, err := classificationDefaultTx(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT ce.asset_id,ce.kind,ce.metadata,ca.assessment,dr.run_ref,ce.observed_at,
		       COALESCE(`+catalogCurrent+`,false),h.created_at
		FROM plume.catalog_entries ce
		JOIN plume.discovery_sources ds ON ds.id=ce.source_id AND ds.workspace_id=ce.workspace_id
		JOIN plume.discovery_runs dr ON dr.id=ce.run_id AND dr.workspace_id=ce.workspace_id
		LEFT JOIN plume.catalog_entries parent ON ce.kind='field' AND parent.workspace_id=ce.workspace_id
		 AND parent.source_id=ce.source_id AND parent.source_ref=ce.parent_ref AND parent.run_id=ce.run_id
		LEFT JOIN plume.catalog_classification_assessments ca ON ca.workspace_id=ce.workspace_id
		 AND ca.asset_id=CASE WHEN ce.kind='table' THEN ce.asset_id ELSE parent.asset_id END AND ca.run_id=ce.run_id
		LEFT JOIN plume.assets a ON a.workspace_id=ce.workspace_id AND a.id=ce.asset_id
		LEFT JOIN plume.asset_classification_history h ON h.workspace_id=ce.workspace_id
		 AND h.asset_id=ce.asset_id AND h.revision=a.classification_revision AND h.origin='manual'
		WHERE ce.workspace_id=$1 AND ce.asset_id=ANY($2) AND ce.kind IN('table','field')
	`, workspaceID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var assetID int64
		var kind, runRef string
		var metadata, assessment []byte
		var observedAt time.Time
		var current bool
		var manualAt *time.Time
		if err := rows.Scan(&assetID, &kind, &metadata, &assessment, &runRef, &observedAt, &current, &manualAt); err != nil {
			return err
		}
		item := &items[positions[assetID]]
		var snapshot CatalogClassificationAssessment
		if len(assessment) > 0 {
			if err := json.Unmarshal(assessment, &snapshot); err != nil {
				return err
			}
		}
		var automatic AutomaticClassification
		if kind == "table" {
			if snapshot.Evaluation == nil {
				continue
			}
			automatic.Evaluation = *snapshot.Evaluation
			automatic.ClassificationRevision = snapshot.ClassificationRevision
			automatic.Comparison, automatic.ComparisonReasons = snapshot.Comparison, snapshot.Reasons
		} else {
			var field struct {
				Evaluation *classificationstandards.Evaluation `json:"classification"`
				Revision   *int64                              `json:"classificationRevision"`
			}
			if err := json.Unmarshal(metadata, &field); err != nil {
				return err
			}
			if field.Evaluation == nil {
				continue
			}
			automatic.Evaluation = *field.Evaluation
			if field.Revision != nil {
				automatic.ClassificationRevision = *field.Revision
			} else {
				automatic.Stale = true
			}
			automatic.Comparison, automatic.ComparisonReasons = compareAutomaticClassification(*item, automatic.Evaluation)
		}
		automatic.RunID, automatic.ObservedAt = runRef, observedAt
		automatic.Stale = automatic.Stale || !current || automatic.ClassificationRevision != item.Revision ||
			automatic.Evaluation.Standard.ID != selectedClassificationStandard(*item, defaultID) ||
			(item.Origin == "manual" && (manualAt == nil || !snapshot.CollectionStartedAt.After(*manualAt) || !observedAt.After(*manualAt)))
		item.Automatic = &automatic
	}
	return rows.Err()
}
