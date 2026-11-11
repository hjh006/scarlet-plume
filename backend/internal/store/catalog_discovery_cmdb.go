package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
)

type CMDBRecord struct {
	AssetID   int64             `json:"assetId,omitempty"`
	SourceRef string            `json:"sourceRef"`
	ParentRef string            `json:"parentRef"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	System    string            `json:"system"`
	Metadata  map[string]string `json:"metadata"`
}
type CMDBImport struct {
	Version int          `json:"version"`
	Source  string       `json:"source"`
	BatchID string       `json:"batchId"`
	Records []CMDBRecord `json:"records"`
}
type CMDBImportResult struct {
	Imported  int  `json:"imported"`
	Replayed  bool `json:"replayed"`
	Conflicts int  `json:"conflicts"`
}

var catalogBusinessKeys = map[string]bool{"business": true, "application": true, "ownerTeam": true, "businessOrganization": true, "region": true, "sharingRecipients": true, "retention": true, "description": true, "cmdbId": true}

func validCatalogBusinessMetadata(metadata map[string]string) bool {
	if len(metadata) > len(catalogBusinessKeys) {
		return false
	}
	for k, v := range metadata {
		if !catalogBusinessKeys[k] || len(v) > 1000 || strings.ContainsAny(v, "\r\x00") {
			return false
		}
	}
	return true
}
func (s *Store) ConfirmCatalogMetadata(ctx context.Context, ws, actor, id, revision int64, metadata map[string]string) error {
	if !validCatalogBusinessMetadata(metadata) {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if role != "owner" && role != "operator" {
			return ErrForbidden
		}
		b, _ := json.Marshal(metadata)
		tag, e := tx.Exec(ctx, `UPDATE plume.catalog_entries SET confirmed_metadata=confirmed_metadata||$4::jsonb,conflicts=conflicts-ARRAY(SELECT jsonb_object_keys($4::jsonb)),revision=revision+1 WHERE workspace_id=$1 AND id=$2 AND revision=$3`, ws, id, revision, b)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return recordCatalogHistory(ctx, tx, ws, id, revision+1, "manual", &actor)
	})
}
func (s *Store) ImportCMDB(ctx context.Context, ws, actor int64, input CMDBImport) (CMDBImportResult, error) {
	var result CMDBImportResult
	if input.Version != 1 || !validAutomationAssetSource(input.Source) || !validCheckText(input.BatchID, 100) || len(input.Records) > 1000 || len(input.Records) == 0 {
		return result, ErrInvalid
	}
	seen := map[string]bool{}
	for _, r := range input.Records {
		if !validCheckText(r.SourceRef, 500) || len(r.ParentRef) > 500 || strings.ContainsAny(r.ParentRef, "\r\n\x00") || !validCheckText(r.Name, 300) || !validCheckText(r.System, 128) || !validCatalogBusinessMetadata(r.Metadata) || seen[r.SourceRef] || r.AssetID < 0 {
			return result, ErrInvalid
		}
		switch r.Kind {
		case "business", "application", "database", "table", "field":
		default:
			return result, ErrInvalid
		}
		seen[r.SourceRef] = true
	}
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if role != "owner" && role != "operator" {
			return ErrForbidden
		}
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1,$2)`, int32(ws), int32(0x434d44)); e != nil {
			return e
		}
		var digest string
		var count int
		e = tx.QueryRow(ctx, `SELECT payload_digest,imported_count FROM plume.cmdb_imports WHERE workspace_id=$1 AND source=$2 AND batch_id=$3`, ws, input.Source, input.BatchID).Scan(&digest, &count)
		if e == nil {
			if digest != discoveryDigest(input) {
				return ErrConflict
			}
			result.Imported = count
			result.Replayed = true
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		for _, r := range input.Records {
			var entryID, revision, assetID int64
			var observed, confirmed []byte
			if r.AssetID > 0 {
				e = tx.QueryRow(ctx, `SELECT ce.id,ce.revision,ce.asset_id,ce.metadata,ce.confirmed_metadata FROM plume.catalog_entries ce WHERE ce.workspace_id=$1 AND ce.asset_id=$2 FOR UPDATE`, ws, r.AssetID).Scan(&entryID, &revision, &assetID, &observed, &confirmed)
				if e != nil {
					return storeError(e)
				}
			} else {
				e = tx.QueryRow(ctx, `INSERT INTO plume.assets(workspace_id,name,system,source_provider,source,source_ref) VALUES($1,$2,$3,'cmdb',$4,$5) ON CONFLICT(workspace_id,source_provider,source,source_ref) DO UPDATE SET name=EXCLUDED.name RETURNING id`, ws, r.Name, r.System, input.Source, r.SourceRef).Scan(&assetID)
				if e != nil {
					return e
				}
				e = tx.QueryRow(ctx, `INSERT INTO plume.catalog_entries(workspace_id,asset_id,kind,parent_ref,source,source_ref,system) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(workspace_id,asset_id) DO UPDATE SET parent_ref=EXCLUDED.parent_ref RETURNING id,revision,metadata,confirmed_metadata`, ws, assetID, r.Kind, r.ParentRef, "cmdb:"+input.Source, r.SourceRef, r.System).Scan(&entryID, &revision, &observed, &confirmed)
				if e != nil {
					return e
				}
			}
			var old, manual map[string]any
			if json.Unmarshal(observed, &old) != nil || json.Unmarshal(confirmed, &manual) != nil {
				return ErrInvalid
			}
			conflicts := map[string]any{}
			for key, value := range r.Metadata {
				if confirmedValue, ok := manual[key]; ok && !reflect.DeepEqual(confirmedValue, value) {
					conflicts[key] = map[string]any{"incoming": value, "confirmed": confirmedValue, "source": input.Source}
					result.Conflicts++
				} else {
					old[key] = value
				}
			}
			b, _ := json.Marshal(old)
			c, _ := json.Marshal(conflicts)
			_, e = tx.Exec(ctx, `UPDATE plume.catalog_entries SET metadata=$3,conflicts=conflicts||$4::jsonb,revision=revision+1,observed_at=now() WHERE workspace_id=$1 AND id=$2`, ws, entryID, b, c)
			if e != nil {
				return e
			}
			if e = recordCatalogHistory(ctx, tx, ws, entryID, revision+1, "cmdb:"+input.Source, &actor); e != nil {
				return e
			}
			result.Imported++
		}
		_, e = tx.Exec(ctx, `INSERT INTO plume.cmdb_imports(workspace_id,source,batch_id,payload_digest,imported_count) VALUES($1,$2,$3,$4,$5)`, ws, input.Source, input.BatchID, discoveryDigest(input), result.Imported)
		return e
	})
	return result, storeError(e)
}
