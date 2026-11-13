package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

type CatalogEntry struct {
	ID                int64          `json:"id"`
	AssetID           int64          `json:"assetId"`
	Name              string         `json:"name"`
	Kind              string         `json:"kind"`
	ParentRef         string         `json:"parentRef"`
	Source            string         `json:"source"`
	SourceRef         string         `json:"sourceRef"`
	System            string         `json:"system"`
	Database          string         `json:"database"`
	Schema            string         `json:"schema"`
	Table             string         `json:"table"`
	Field             string         `json:"field"`
	Metadata          map[string]any `json:"metadata"`
	ConfirmedMetadata map[string]any `json:"confirmedMetadata"`
	Conflicts         map[string]any `json:"conflicts"`
	Revision          int64          `json:"revision"`
	ObservedAt        time.Time      `json:"observedAt"`
	Current           bool           `json:"current"`
	Category          string         `json:"category"`
	BusinessLevel     string         `json:"businessLevel"`
	StatutoryCategory string         `json:"statutoryCategory"`
}
type CatalogHistory struct {
	Revision  int64          `json:"revision"`
	Origin    string         `json:"origin"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt time.Time      `json:"createdAt"`
}
type CatalogFlowTarget struct {
	AssetID    int64
	SourceID   int64
	RunID      int64
	Source     string
	System     string
	Database   string
	Schema     string
	Table      string
	Categories []string
	ObservedAt time.Time
}
type CatalogDiscoverySummary struct {
	Sources        int      `json:"sources"`
	CurrentSources int      `json:"currentSources"`
	Entries        int      `json:"entries"`
	Tables         int      `json:"tables"`
	Fields         int      `json:"fields"`
	RuleHits       int      `json:"ruleHits"`
	Confirmed      int      `json:"confirmed"`
	Warnings       []string `json:"warnings"`
}

func catalogRef(parts ...string) string { b, _ := json.Marshal(parts); return string(b) }
func saveDiscoveryCatalog(ctx context.Context, tx pgx.Tx, ws int64, source DiscoverySource, runID int64, in DiscoverySubmission) error {
	if source.Provider == "local_files" {
		return saveFileDiscoveryCatalog(ctx, tx, ws, source, runID, in)
	}
	dbref := catalogRef(source.System, source.Database)
	if _, e := upsertDiscoveryEntry(ctx, tx, ws, source, runID, in, "database", dbref, "", source.Database, "", "", "", map[string]any{"discoveryProvider": source.Provider, "coverage": in.Result.Coverage}); e != nil {
		return e
	}
	for _, table := range in.Result.Tables {
		ref := catalogRef(source.System, table.Database, table.Schema, table.Name)
		metadata := map[string]any{"comment": table.Comment, "permissionsStatus": table.PermissionsStatus, "permissions": table.Permissions, "constraints": table.Constraints, "lineage": table.Lineage, "warnings": table.Warnings, "scannedRows": table.ScannedRows}
		tableAssetID, e := upsertDiscoveryEntry(ctx, tx, ws, source, runID, in, "table", ref, dbref, table.Name, table.Schema, table.Name, "", metadata)
		if e != nil {
			return e
		}
		var actor int64
		if e = tx.QueryRow(ctx, `SELECT created_by FROM plume.discovery_sources WHERE id=$1`, source.ID).Scan(&actor); e != nil {
			return e
		}
		evaluation, e := persistCatalogClassification(ctx, tx, ws, actor, tableAssetID, source.Source, table)
		if e != nil {
			return e
		}
		if evaluation != nil {
			if _, e = tx.Exec(ctx, `UPDATE plume.catalog_entries SET metadata=metadata||jsonb_build_object('classification',$3::jsonb) WHERE workspace_id=$1 AND asset_id=$2`, ws, tableAssetID, evaluation); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE plume.catalog_history h SET metadata=jsonb_set(h.metadata,'{observed,classification}',$3::jsonb) FROM plume.catalog_entries ce WHERE ce.workspace_id=$1 AND ce.asset_id=$2 AND h.entry_id=ce.id AND h.revision=ce.revision`, ws, tableAssetID, evaluation); e != nil {
				return e
			}
		}
		if e = persistCatalogAssessment(ctx, tx, ws, actor, tableAssetID, runID, source, in, table, evaluation); e != nil {
			return e
		}
		for _, field := range table.Fields {
			fref := catalogRef(source.System, table.Database, table.Schema, table.Name, field.Name)
			meta := map[string]any{"type": field.Type, "comment": field.Comment, "nullable": field.Nullable, "sensitivityCandidate": field.Sensitivity, "ruleHits": field.Hits}
			assetID, e := upsertDiscoveryEntry(ctx, tx, ws, source, runID, in, "field", fref, ref, field.Name, table.Schema, table.Name, field.Name, meta)
			if e != nil {
				return e
			}
			if len(field.Hits) > 0 {
				one := table
				one.Fields = []collector.DiscoveryField{field}
				if _, e = persistCatalogClassification(ctx, tx, ws, actor, assetID, source.Source, one); e != nil {
					return e
				}
			}
			for _, hit := range field.Hits {
				scope, _ := json.Marshal(map[string]any{"database": table.Database, "schema": table.Schema, "table": table.Name, "field": field.Name, "coverage": in.Result.Coverage})
				_, e = tx.Exec(ctx, `INSERT INTO plume.discovery_rule_hits(workspace_id,run_id,asset_id,rule_key,rule_version,category,method,match_count,scanned_count,scope) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, ws, runID, assetID, hit.RuleKey, hit.RuleVersion, hit.Category, hit.Method, hit.MatchCount, hit.ScannedCount, scope)
				if e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func upsertDiscoveryEntry(ctx context.Context, tx pgx.Tx, ws int64, source DiscoverySource, runID int64, in DiscoverySubmission, kind, ref, parent, name, schema, table, field string, metadata map[string]any) (int64, error) {
	var assetID, entryID, revision int64
	e := tx.QueryRow(ctx, `INSERT INTO plume.assets(workspace_id,name,system,source_provider,source,source_ref) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,source_provider,source,source_ref) DO UPDATE SET name=EXCLUDED.name RETURNING id`, ws, name, source.System, "discovery:"+source.Provider, source.Source, ref).Scan(&assetID)
	if e != nil {
		return 0, e
	}
	meta, _ := json.Marshal(metadata)
	e = tx.QueryRow(ctx, `INSERT INTO plume.catalog_entries(workspace_id,asset_id,kind,parent_ref,source,source_ref,source_id,run_id,source_revision,system,database_name,schema_name,table_name,field_name,metadata,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) ON CONFLICT(workspace_id,source,source_ref) DO UPDATE SET run_id=EXCLUDED.run_id,source_id=EXCLUDED.source_id,source_revision=EXCLUDED.source_revision,metadata=(catalog_entries.metadata-'classification'-'classificationRevision')||EXCLUDED.metadata,revision=catalog_entries.revision+1,observed_at=EXCLUDED.observed_at RETURNING id,revision`, ws, assetID, kind, parent, source.Source, ref, source.ID, runID, source.Revision, source.System, source.Database, schema, table, field, meta, in.ObservedAt).Scan(&entryID, &revision)
	if e != nil {
		return 0, e
	}
	e = recordCatalogHistory(ctx, tx, ws, entryID, revision, "discovery", nil)
	return assetID, e
}
func recordCatalogHistory(ctx context.Context, tx pgx.Tx, ws, id, revision int64, origin string, actor *int64) error {
	_, e := tx.Exec(ctx, `INSERT INTO plume.catalog_history(workspace_id,entry_id,revision,origin,actor_id,metadata) SELECT workspace_id,id,revision,$3,$4,jsonb_build_object('observed',metadata,'confirmed',confirmed_metadata,'conflicts',conflicts) FROM plume.catalog_entries WHERE workspace_id=$1 AND id=$2`, ws, id, origin, actor)
	return e
}

const catalogCurrent = `(ds.enabled AND ds.revision=ce.source_revision AND ds.latest_run_id=ce.run_id AND dr.status='success' AND dr.mode='collect' AND dr.observed_at>now()-make_interval(mins=>greatest(ds.interval_minutes*3,60)))`

func (s *Store) ListCatalogEntries(ctx context.Context, ws int64, kind, query string) ([]CatalogEntry, error) {
	if len(query) > 160 {
		return nil, ErrInvalid
	}
	result := []CatalogEntry{}
	rows, e := s.pool.Query(ctx, `SELECT ce.id,ce.asset_id,a.name,ce.kind,ce.parent_ref,ce.source,ce.source_ref,ce.system,ce.database_name,ce.schema_name,ce.table_name,ce.field_name,ce.metadata,ce.confirmed_metadata,ce.conflicts,ce.revision,ce.observed_at,COALESCE(`+catalogCurrent+`,ce.source_id IS NULL),a.category,a.business_level,a.statutory_category FROM plume.catalog_entries ce JOIN plume.assets a ON a.id=ce.asset_id AND a.workspace_id=ce.workspace_id LEFT JOIN plume.discovery_sources ds ON ds.id=ce.source_id AND ds.workspace_id=ce.workspace_id LEFT JOIN plume.discovery_runs dr ON dr.id=ce.run_id AND dr.workspace_id=ce.workspace_id WHERE ce.workspace_id=$1 AND ($2='' OR ce.kind=$2) AND ($3='' OR strpos(lower(a.name||' '||ce.source||' '||ce.database_name||' '||ce.schema_name||' '||ce.table_name||' '||ce.field_name),lower($3))>0) ORDER BY ce.id DESC LIMIT 1000`, ws, kind, query)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var v CatalogEntry
		var meta, confirmed, conflicts []byte
		e = rows.Scan(&v.ID, &v.AssetID, &v.Name, &v.Kind, &v.ParentRef, &v.Source, &v.SourceRef, &v.System, &v.Database, &v.Schema, &v.Table, &v.Field, &meta, &confirmed, &conflicts, &v.Revision, &v.ObservedAt, &v.Current, &v.Category, &v.BusinessLevel, &v.StatutoryCategory)
		if e != nil {
			return nil, e
		}
		for _, item := range []struct {
			b []byte
			v *map[string]any
		}{{meta, &v.Metadata}, {confirmed, &v.ConfirmedMetadata}, {conflicts, &v.Conflicts}} {
			if e = json.Unmarshal(item.b, item.v); e != nil {
				return nil, e
			}
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Store) CatalogEntryHistory(ctx context.Context, ws, id int64) ([]CatalogHistory, error) {
	result := []CatalogHistory{}
	rows, e := s.pool.Query(ctx, `SELECT revision,origin,metadata,created_at FROM plume.catalog_history WHERE workspace_id=$1 AND entry_id=$2 ORDER BY revision DESC LIMIT 50`, ws, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var h CatalogHistory
		var b []byte
		if e = rows.Scan(&h.Revision, &h.Origin, &b, &h.CreatedAt); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &h.Metadata); e != nil {
			return nil, e
		}
		result = append(result, h)
	}
	return result, rows.Err()
}

// readCatalogFlowTargets runs in the caller's repeatable-read transaction.
func readCatalogFlowTargets(ctx context.Context, tx pgx.Tx, ws int64) ([]CatalogFlowTarget, error) {
	result := []CatalogFlowTarget{}
	rows, e := tx.Query(ctx, `SELECT ce.asset_id,ds.id,dr.id,ce.source,ce.system,ce.database_name,ce.schema_name,ce.table_name,dr.observed_at,ARRAY(SELECT DISTINCT h.category FROM plume.discovery_rule_hits h JOIN plume.catalog_entries f ON f.asset_id=h.asset_id AND f.workspace_id=h.workspace_id WHERE h.workspace_id=ce.workspace_id AND h.run_id=ce.run_id AND f.parent_ref=ce.source_ref AND f.source_id=ce.source_id ORDER BY h.category) FROM plume.catalog_entries ce JOIN plume.discovery_sources ds ON ds.id=ce.source_id AND ds.workspace_id=ce.workspace_id JOIN plume.discovery_runs dr ON dr.id=ce.run_id AND dr.workspace_id=ce.workspace_id WHERE ce.workspace_id=$1 AND ce.kind='table' AND `+catalogCurrent+` ORDER BY ce.asset_id LIMIT 1001`, ws)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var t CatalogFlowTarget
		if e = rows.Scan(&t.AssetID, &t.SourceID, &t.RunID, &t.Source, &t.System, &t.Database, &t.Schema, &t.Table, &t.ObservedAt, &t.Categories); e != nil {
			return nil, e
		}
		result = append(result, t)
	}
	if len(result) > 1000 {
		return nil, ErrDataFlowLimit
	}
	return result, rows.Err()
}
func (s *Store) CurrentCatalogFlowTargets(ctx context.Context, ws int64) ([]CatalogFlowTarget, error) {
	var result []CatalogFlowTarget
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error { var e error; result, e = readCatalogFlowTargets(ctx, tx, ws); return e })
	return result, e
}
func (s *Store) CatalogDiscoverySummary(ctx context.Context, ws int64) (CatalogDiscoverySummary, error) {
	var r CatalogDiscoverySummary
	r.Warnings = []string{"来源缺权限以unknown呈现；命中为候选，人工确认分类优先；负向采样不降级分类。"}
	e := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM plume.discovery_sources WHERE workspace_id=$1),(SELECT count(*) FROM plume.discovery_sources ds JOIN plume.discovery_runs dr ON dr.id=ds.latest_run_id WHERE ds.workspace_id=$1 AND ds.enabled AND ds.revision=dr.source_revision AND dr.status='success' AND dr.mode='collect' AND dr.observed_at>now()-make_interval(mins=>greatest(ds.interval_minutes*3,60))),count(*),count(*) FILTER(WHERE ce.kind='table'),count(*) FILTER(WHERE ce.kind='field'),(SELECT count(*) FROM plume.discovery_rule_hits h JOIN plume.discovery_sources s ON s.latest_run_id=h.run_id AND s.workspace_id=h.workspace_id WHERE h.workspace_id=$1 AND s.enabled),count(*) FILTER(WHERE ce.confirmed_metadata<>'{}') FROM plume.catalog_entries ce WHERE ce.workspace_id=$1`, ws).Scan(&r.Sources, &r.CurrentSources, &r.Entries, &r.Tables, &r.Fields, &r.RuleHits, &r.Confirmed)
	return r, e
}
