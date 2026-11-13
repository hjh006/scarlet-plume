package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

// collectedAssessmentFacts reads only the selected asset in the caller's transaction.
// Catalog declarations stay separate from observed configuration and legal facts.
func collectedAssessmentFacts(ctx context.Context, tx pgx.Tx, workspaceID, assetID int64) ([]AssessmentFact, error) {
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM plume.assets WHERE workspace_id=$1 AND id=$2`, workspaceID, assetID).Scan(&id); err != nil {
		return nil, storeError(err)
	}
	facts, err := postgresAssessmentFacts(ctx, tx, workspaceID, assetID)
	if err != nil {
		return nil, err
	}
	catalog, err := catalogAssessmentFacts(ctx, tx, workspaceID, assetID)
	if err != nil {
		return nil, err
	}
	if len(facts) == 0 {
		facts = catalog
	} else {
		for _, fact := range catalog {
			if fact.Key == "catalog_business_metadata" {
				facts = append(facts, fact)
			}
		}
	}
	if len(facts) == 0 || !hasAssessmentFact(facts, "source_coverage") {
		facts = append(facts,
			AssessmentFact{Key: "source_observed_types", State: "unknown", Source: "source", Record: "not_collected_or_stale"},
			AssessmentFact{Key: "source_coverage", State: "unknown", Source: "source", Record: "not_collected_or_stale"},
		)
	}
	application, err := applicationAssessmentFacts(ctx, tx, workspaceID, assetID)
	if err != nil {
		return nil, err
	}
	return append(facts, application...), nil
}

func hasAssessmentFact(facts []AssessmentFact, key string) bool {
	for _, fact := range facts {
		if fact.Key == key {
			return true
		}
	}
	return false
}

func observedAssessmentFact(key, value, source, record string, at *time.Time) AssessmentFact {
	state := "known"
	if strings.TrimSpace(value) == "" || value == "unknown" {
		state, value = "unknown", ""
	}
	return AssessmentFact{Key: key, State: state, Value: value, Source: source, Record: record, ObservedAt: at}
}

func postgresAssessmentFacts(ctx context.Context, tx pgx.Tx, workspaceID, assetID int64) ([]AssessmentFact, error) {
	var table collector.PostgreSQLTable
	var run string
	var at time.Time
	err := tx.QueryRow(ctx, `SELECT i.inspection,r.run_id,r.observed_at FROM plume.postgres_asset_inspections i
 JOIN plume.assets a ON a.id=i.asset_id JOIN plume.automation_jobs j ON j.id=i.job_id AND j.workspace_id=a.workspace_id
 JOIN plume.managed_postgres_jobs p ON p.job_id=j.id
 JOIN plume.automation_runs r ON r.id=i.automation_run_id AND r.job_id=j.id AND r.status='success'
 WHERE a.workspace_id=$1 AND a.id=$2 AND j.revoked_at IS NULL
 AND EXISTS(SELECT 1 FROM plume.memberships member WHERE member.workspace_id=j.workspace_id AND member.user_id=j.actor_id AND member.role IN ('owner','operator'))
 AND (i.source_host,i.source_port,i.source_database)=(p.host,p.port,p.database_name)
 AND r.id=(SELECT latest.id FROM plume.automation_runs latest WHERE latest.job_id=i.job_id ORDER BY latest.id DESC LIMIT 1)`, workspaceID, assetID).Scan(&table, &run, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	types := map[string]bool{}
	for _, field := range postgresSensitiveFields(table) {
		types[field.Sensitivity] = true
	}
	scope := "metadata"
	if table.ContentAnalysis != nil {
		scope = table.ContentAnalysis.Status + fmt.Sprintf(" / sampled rows %d", table.ContentAnalysis.RowsScanned)
	}
	return []AssessmentFact{
		observedAssessmentFact("source_observed_types", strings.Join(sortedAssessmentKeys(types), ", "), "postgresql", run, &at),
		observedAssessmentFact("source_coverage", scope, "postgresql", run, &at),
	}, nil
}

func catalogAssessmentFacts(ctx context.Context, tx pgx.Tx, workspaceID, assetID int64) ([]AssessmentFact, error) {
	var entry CatalogEntry
	var sourceID, runID, sourceRevision *int64
	var provider string
	var current bool
	var coverage collector.DiscoveryCoverage
	err := tx.QueryRow(ctx, `SELECT ce.id,ce.kind,ce.source,ce.source_ref,ce.source_id,ce.run_id,ce.source_revision,
 ce.system,ce.database_name,ce.schema_name,ce.table_name,ce.field_name,ce.metadata,ce.confirmed_metadata,ce.conflicts,
 ce.revision,ce.observed_at,COALESCE(ds.provider,''),COALESCE(dr.coverage,'{}'),
 COALESCE(`+catalogCurrent+` AND dr.source_id=ds.id AND dr.source_revision=ds.revision
 AND ce.system=ds.system AND ce.database_name=ds.database_name AND ce.source=ds.source
 AND a.source_provider='discovery:'||ds.provider AND a.source=ce.source AND a.source_ref=ce.source_ref
 AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=ds.workspace_id AND m.user_id=ds.created_by AND m.role='owner'),false)
 FROM plume.catalog_entries ce JOIN plume.assets a ON a.id=ce.asset_id AND a.workspace_id=ce.workspace_id
 LEFT JOIN plume.discovery_sources ds ON ds.id=ce.source_id AND ds.workspace_id=ce.workspace_id
 LEFT JOIN plume.discovery_runs dr ON dr.id=ce.run_id AND dr.workspace_id=ce.workspace_id
 WHERE ce.workspace_id=$1 AND ce.asset_id=$2`, workspaceID, assetID).Scan(
		&entry.ID, &entry.Kind, &entry.Source, &entry.SourceRef, &sourceID, &runID, &sourceRevision,
		&entry.System, &entry.Database, &entry.Schema, &entry.Table, &entry.Field,
		&entry.Metadata, &entry.ConfirmedMetadata, &entry.Conflicts, &entry.Revision, &entry.ObservedAt, &provider, &coverage, &current,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record := fmt.Sprintf("catalog:%d/revision:%d/asset:%d", entry.ID, entry.Revision, assetID)
	facts := []AssessmentFact{}
	business := catalogBusinessAssessmentMetadata(entry)
	if business != nil {
		// Imported and manually confirmed declarations retain their own provenance,
		// even when technical collection is unavailable. They do not assert current ACLs.
		facts = append(facts, observedAssessmentFact("catalog_business_metadata", assessmentFactJSON(business), "catalog_declared", record, nil))
	}
	scope := map[string]any{"kind": entry.Kind, "system": entry.System, "database": entry.Database, "schema": entry.Schema, "table": entry.Table, "field": entry.Field}
	if sourceID == nil {
		facts = append(facts, observedAssessmentFact("source_metadata", assessmentFactJSON(scope), "catalog_declared", record, &entry.ObservedAt))
		return facts, nil
	}
	if !current || runID == nil || sourceRevision == nil {
		return facts, nil
	}
	record += fmt.Sprintf("/source:%d/revision:%d/run:%d", *sourceID, *sourceRevision, *runID)
	source := "discovery:" + provider
	var categories []string
	err = tx.QueryRow(ctx, `SELECT ARRAY(SELECT DISTINCT category FROM (
 SELECT h.category FROM plume.discovery_rule_hits h JOIN plume.catalog_entries f
 ON f.workspace_id=h.workspace_id AND f.asset_id=h.asset_id AND f.run_id=h.run_id
 WHERE f.workspace_id=$1 AND f.source_id=$2 AND f.run_id=$3 AND f.source_revision=$4 AND h.match_count>0
 AND (f.asset_id=$5 OR ($6='table' AND f.kind='field' AND f.parent_ref=$7))
 UNION SELECT f.metadata->>'sensitivityCandidate' FROM plume.catalog_entries f
 WHERE f.workspace_id=$1 AND f.source_id=$2 AND f.run_id=$3 AND f.source_revision=$4
 AND (f.asset_id=$5 OR ($6='table' AND f.kind='field' AND f.parent_ref=$7))
 ) candidates WHERE category IS NOT NULL AND category<>'' ORDER BY category)`, workspaceID, *sourceID, *runID, *sourceRevision, assetID, entry.Kind, entry.SourceRef).Scan(&categories)
	if err != nil {
		return nil, err
	}
	facts = append(facts,
		observedAssessmentFact("source_metadata", assessmentFactJSON(scope), source, record, &entry.ObservedAt),
		observedAssessmentFact("source_observed_types", strings.Join(categories, ", "), source, record, &entry.ObservedAt),
	)
	permissionStatus, _ := entry.Metadata["permissionsStatus"].(string)
	if permissionStatus == "" {
		permissionStatus = "unknown"
	}
	permissionValue := ""
	if slices.Contains([]string{"observed", "current_account_visible_grants"}, permissionStatus) {
		permissions, _ := entry.Metadata["permissions"].([]any)
		permissionValue = assessmentFactJSON(map[string]any{
			"status": permissionStatus, "permissions": permissions[:min(len(permissions), 50)],
			"omitted": max(len(permissions)-50, 0), "scope": scope,
		})
	}
	facts = append(facts, observedAssessmentFact("source_permissions", permissionValue, source, record, &entry.ObservedAt))
	facts = append(facts, observedAssessmentFact("source_coverage", assessmentFactJSON(map[string]any{
		"scope": scope, "coverage": coverage, "scannedRows": entry.Metadata["scannedRows"], "warnings": entry.Metadata["warnings"],
	}), source, record, &entry.ObservedAt))
	return facts, nil
}

func catalogBusinessAssessmentMetadata(entry CatalogEntry) map[string]any {
	observed, confirmed, conflicts := map[string]string{}, map[string]string{}, map[string]any{}
	for key := range catalogBusinessKeys {
		if value, ok := entry.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			observed[key] = value
		}
		if value, ok := entry.ConfirmedMetadata[key].(string); ok && strings.TrimSpace(value) != "" {
			confirmed[key] = value
		}
		if value, ok := entry.Conflicts[key]; ok {
			conflicts[key] = value
		}
	}
	if len(observed)+len(confirmed)+len(conflicts) == 0 {
		return nil
	}
	return map[string]any{"observed": observed, "confirmed": confirmed, "conflicts": conflicts}
}

func assessmentFactJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
