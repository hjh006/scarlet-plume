package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func canonicalCatalogSystem(system string) string {
	switch strings.ToLower(system) {
	case "postgres", "postgresql":
		return "postgresql"
	case "microsoft.sql_server", "sqlserver", "mssql", "azuresql":
		return "mssql"
	case "oracle.db", "oracle":
		return "oracle"
	case "ibm.db2", "db2":
		return "db2"
	}
	return strings.ToLower(system)
}
func catalogFlowTargetKey(system, source, database, schema, table string) string {
	canonical := canonicalCatalogSystem(system)
	if (canonical == "mysql" || canonical == "mariadb") && schema == database {
		schema = ""
	}
	return flowID(canonical, source, database, schema, table)
}
func addCatalogFlowNodes(ctx context.Context, tx pgx.Tx, ws int64, b *flowBuilder) error {
	targets, e := readCatalogFlowTargets(ctx, tx, ws)
	if e != nil {
		return e
	}
	for _, target := range targets {
		var category, business, statutory string
		var revision int64
		var meta, standard []byte
		e = tx.QueryRow(ctx, `SELECT a.category,a.business_level,a.statutory_category,a.classification_revision,ce.metadata,COALESCE(a.classification_standard,'{}') FROM plume.assets a JOIN plume.catalog_entries ce ON ce.asset_id=a.id AND ce.workspace_id=a.workspace_id WHERE a.workspace_id=$1 AND a.id=$2`, ws, target.AssetID).Scan(&category, &business, &statutory, &revision, &meta, &standard)
		if e != nil {
			return e
		}
		var metadata struct {
			Warnings          []string `json:"warnings"`
			PermissionsStatus string   `json:"permissionsStatus"`
			Classification    any      `json:"classification"`
		}
		if e = json.Unmarshal(meta, &metadata); e != nil {
			return e
		}
		warnings := flowCategories(metadata.Warnings, []string{"bounded_source_scope", "classification_candidates_not_statutory_determination"})
		if metadata.PermissionsStatus == "unknown" {
			warnings = flowCategories(warnings, []string{"source_permissions_unknown"})
		}
		evidence := DataFlowEvidence{Kind: "catalog_discovery", Source: target.Source, SourceID: target.SourceID, Record: fmt.Sprint(target.RunID), ObservedAt: target.ObservedAt, Warnings: warnings}
		evidence.Details = map[string]any{"system": target.System, "database": target.Database, "schema": target.Schema, "table": target.Table, "classificationStandard": json.RawMessage(standard), "evaluation": metadata.Classification}
		fields, fe := tx.Query(ctx, `SELECT ce.field_name,ce.metadata FROM plume.catalog_entries ce WHERE ce.workspace_id=$1 AND ce.source_id=$2 AND ce.run_id=$3 AND ce.parent_ref=$4 AND ce.kind='field' ORDER BY ce.field_name LIMIT 21`, ws, target.SourceID, target.RunID, catalogRef(target.System, target.Database, target.Schema, target.Table))
		if fe != nil {
			return fe
		}
		candidates := []map[string]any{}
		for fields.Next() {
			var name string
			var raw []byte
			if fe = fields.Scan(&name, &raw); fe != nil {
				fields.Close()
				return fe
			}
			var data map[string]any
			if fe = json.Unmarshal(raw, &data); fe != nil {
				fields.Close()
				return fe
			}
			sensitivity, _ := data["sensitivityCandidate"].(string)
			hits, _ := data["ruleHits"].([]any)
			if sensitivity != "" || len(hits) > 0 {
				if len(hits) > 20 {
					data["ruleHits"] = hits[:20]
					evidence.Warnings = flowCategories(evidence.Warnings, []string{"field_rules_truncated_20"})
				}
				candidates = append(candidates, map[string]any{"field": name, "facts": data})
			}
		}
		fe = fields.Err()
		fields.Close()
		if fe != nil {
			return fe
		}
		if len(candidates) > 20 {
			candidates = candidates[:20]
			evidence.Warnings = flowCategories(evidence.Warnings, []string{"field_candidates_truncated_20"})
		}
		evidence.Details["fieldCandidates"] = candidates
		id := "table:" + fmt.Sprint(target.AssetID)
		if revision == 0 {
			statutory = ""
		}
		b.node(DataFlowNode{ID: id, Kind: "table", Label: target.Database + "." + target.Schema + "." + target.Table, AssetID: target.AssetID, Category: category, BusinessLevel: business, StatutoryCategory: statutory, Categories: target.Categories, Evidence: []DataFlowEvidence{evidence}, Target: &DataFlowTableTarget{System: canonicalCatalogSystem(target.System), Source: target.Source, Database: target.Database, Schema: target.Schema, Table: target.Table}})
		key := catalogFlowTargetKey(target.System, target.Source, target.Database, target.Schema, target.Table)
		if old := b.targets[key]; old != "" && old != id {
			b.targets[key] = "ambiguous"
			b.graph.Warnings = flowCategories(b.graph.Warnings, []string{"ambiguous_database_target"})
		} else if old == "" {
			b.targets[key] = id
		}
	}
	return nil
}
