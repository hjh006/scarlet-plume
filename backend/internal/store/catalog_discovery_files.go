package store

import (
	"context"
	"encoding/json"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

func saveFileDiscoveryCatalog(ctx context.Context, tx pgx.Tx, ws int64, source DiscoverySource, runID int64, in DiscoverySubmission) error {
	var rules []collector.DiscoveryRule
	e := tx.QueryRow(ctx, `SELECT lease_rules FROM plume.discovery_sources WHERE id=$1 AND workspace_id=$2`, source.ID, ws).Scan(&rules)
	if e != nil {
		return e
	}
	for _, file := range in.Result.Files {
		ref := catalogRef("local_files", source.Database, file.Ref)
		allHits := []any{}
		for _, field := range file.Fields {
			for _, hit := range field.Hits {
				allHits = append(allHits, hit)
			}
		}
		asset, e := upsertDiscoveryEntry(ctx, tx, ws, source, runID, in, "file", ref, "", file.Name, "", "", "", map[string]any{"discoveryProvider": "local_files", "appliedRules": rules, "fileEvidence": file, "ruleHits": allHits, "coverage": in.Result.Coverage})
		if e != nil {
			return e
		}
		if e = saveFileFingerprints(ctx, tx, ws, source.ID, runID, asset, file); e != nil {
			return e
		}
		raw, _ := json.Marshal(file)
		if _, e = tx.Exec(ctx, `INSERT INTO plume.file_content_evidence(workspace_id,source_id,run_id,asset_id,file_ref,evidence) VALUES($1,$2,$3,$4,$5,$6)`, ws, source.ID, runID, asset, file.Ref, raw); e != nil {
			return e
		}
		for _, field := range file.Fields {
			for _, hit := range field.Hits {
				scope, _ := json.Marshal(map[string]any{"fileRef": file.Ref, "field": field.Name, "coverage": file.Coverage})
				_, e = tx.Exec(ctx, `INSERT INTO plume.discovery_rule_hits(workspace_id,run_id,asset_id,rule_key,rule_version,category,method,match_count,scanned_count,scope) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(run_id,asset_id,rule_key,rule_version,method) DO UPDATE SET match_count=plume.discovery_rule_hits.match_count+EXCLUDED.match_count,scanned_count=plume.discovery_rule_hits.scanned_count+EXCLUDED.scanned_count`, ws, runID, asset, hit.RuleKey, hit.RuleVersion, hit.Category, hit.Method, hit.MatchCount, hit.ScannedCount, scope)
				if e != nil {
					return e
				}
			}
		}
	}
	return nil
}
