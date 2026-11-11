package store

import (
	"context"
	"fmt"
	"slices"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationstandards"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

// The existing standards engine consumes bounded format counts. Convert only
// recognized built-in content rules; arbitrary customer labels remain candidates.
func persistCatalogClassification(ctx context.Context, tx pgx.Tx, ws, actor, assetID int64, source string, table collector.DiscoveryTable) (*classificationstandards.Evaluation, error) {
	item, e := scanClassificationAsset(tx.QueryRow(ctx, "SELECT "+assetColumns+`,classification_revision,classification_standard,classification_origin FROM plume.assets WHERE workspace_id=$1 AND id=$2 FOR NO KEY UPDATE`, ws, assetID))
	if e != nil {
		return nil, e
	}
	standard, e := classificationDefaultTx(ctx, tx, ws)
	if e != nil {
		return nil, e
	}
	standard = selectedClassificationStandard(item, standard)
	converted := collector.PostgreSQLTable{MetadataStatus: "partial", Warnings: []string{"catalog_adapter_scope_requires_review"}, Columns: []collector.PostgreSQLColumn{}, ContentAnalysis: &collector.PostgreSQLContentAnalysis{Status: "partial", Method: "bounded_customer_sample", RowsScanned: table.ScannedRows, RowLimit: 200, Columns: []collector.PostgreSQLColumnStatistics{}, Warnings: table.Warnings}}
	for _, field := range table.Fields {
		converted.Columns = append(converted.Columns, collector.PostgreSQLColumn{Name: field.Name, Type: field.Type, Comment: field.Comment, Sensitivity: field.Sensitivity})
		stats := collector.PostgreSQLColumnStatistics{Column: field.Name, Status: "complete", NonNullRows: table.ScannedRows, Matches: []collector.PostgreSQLPatternCount{}}
		for _, hit := range field.Hits {
			if hit.RuleKey == "builtin."+hit.Category && slices.Contains([]string{"email", "phone", "national_id", "financial_account"}, hit.Category) && hit.Method != "metadata" {
				stats.Matches = append(stats.Matches, collector.PostgreSQLPatternCount{Category: hit.Category, Count: hit.MatchCount})
			}
		}
		converted.ContentAnalysis.Columns = append(converted.ContentAnalysis.Columns, stats)
	}
	evaluation, e := classificationstandards.EvaluatePostgreSQL(converted, standard)
	if e != nil {
		return nil, nil
	}
	evaluation.Version = "catalog-bounded-rules-v1"
	for i := range evaluation.Facts {
		if evaluation.Facts[i].Source == "source_aggregate" {
			evaluation.Facts[i].Source = "customer_collector_aggregate"
		}
	}
	for i := range evaluation.Rules {
		for j := range evaluation.Rules[i].Facts {
			if evaluation.Rules[i].Facts[j].Source == "source_aggregate" {
				evaluation.Rules[i].Facts[j].Source = "customer_collector_aggregate"
			}
		}
	}
	previousRevision := item.Revision
	if e = applyAutomaticClassification(ctx, tx, AutomationJob{workspaceID: ws, actorID: actor}, &item, evaluation); e != nil {
		return nil, e
	}
	if item.Revision != previousRevision {
		basis := fmt.Sprintf("依据%s对来源%s的受限内容格式统计识别属性；采样与实际主体、用途、法定类别取证范围见目录运行批次。", evaluation.Standard.Code, source)
		if _, e = tx.Exec(ctx, `UPDATE plume.assets SET classification_basis=$2 WHERE id=$1 AND workspace_id=$3`, assetID, basis, ws); e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.asset_classification_history SET basis=$3 WHERE asset_id=$1 AND revision=$2 AND workspace_id=$4`, assetID, item.Revision, basis, ws); e != nil {
			return nil, e
		}
	}
	return &evaluation, nil
}
