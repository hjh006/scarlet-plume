package store

import (
	"context"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"github.com/jackc/pgx/v5"
	"time"
)

type ValueSearchInput struct {
	valuefingerprint.Token
	SourceID int64 `json:"sourceId,omitempty"`
}
type ValueMatch struct {
	ID         int64     `json:"id"`
	SourceID   int64     `json:"sourceId"`
	Source     string    `json:"source"`
	RunID      int64     `json:"runId"`
	RunRef     string    `json:"runRef"`
	AssetID    int64     `json:"assetId"`
	ObservedAt time.Time `json:"observedAt"`
	Locator    string    `json:"locator"`
	Category   string    `json:"category"`
	valuefingerprint.Token
	Current bool   `json:"current"`
	Record  string `json:"record"`
}
type ValueSearchResult struct {
	Relations    []CryptoTransformRelation `json:"relations"`
	RuntimeItems []RuntimeValueMatch       `json:"runtimeItems"`
	Items        []ValueMatch              `json:"items"`
	Limit        int                       `json:"limit"`
	Truncated    bool                      `json:"truncated"`
	Coverage     string                    `json:"coverage"`
	Warnings     []string                  `json:"warnings"`
}

func saveFileFingerprints(ctx context.Context, tx pgx.Tx, ws, sourceID, runID, assetID int64, file collector.FileEvidence) error {
	for _, fp := range file.Fingerprints {
		_, e := tx.Exec(ctx, `INSERT INTO plume.value_fingerprints(workspace_id,source_id,run_id,asset_id,key_id,scope,version,digest,locator,category) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, ws, sourceID, runID, assetID, fp.KeyID, fp.Scope, fp.Version, fp.Digest, fp.Locator, fp.Category)
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) SearchValueFingerprints(ctx context.Context, ws, actor int64, in ValueSearchInput) (ValueSearchResult, error) {
	result := ValueSearchResult{Items: []ValueMatch{}, RuntimeItems: []RuntimeValueMatch{}, Relations: []CryptoTransformRelation{}, Limit: 200, Coverage: "recorded_fingerprints_only", Warnings: []string{"historical_logs_without_fingerprints_unknown", "equal_token_is_not_proof_of_lineage", "keys_remain_at_authorized_sources"}}
	if !valuefingerprint.Valid(in.Token) || in.SourceID < 0 {
		return result, ErrInvalid
	}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, false)
		if e != nil {
			return e
		}
		if role != "owner" && role != "reviewer" && role != "operator" {
			return ErrForbidden
		}
		rows, e := tx.Query(ctx, `SELECT fp.id,fp.source_id,ds.source,fp.run_id,dr.run_ref,fp.asset_id,dr.observed_at,fp.locator,fp.category,ds.enabled AND ds.revision=dr.source_revision AND ds.latest_run_id=dr.id AND dr.status='success' AND dr.mode='collect' AND dr.observed_at>now()-make_interval(mins=>greatest(ds.interval_minutes*3,60)) AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=ds.workspace_id AND m.user_id=ds.created_by AND m.role='owner') FROM plume.value_fingerprints fp JOIN plume.discovery_sources ds ON ds.id=fp.source_id AND ds.workspace_id=fp.workspace_id JOIN plume.discovery_runs dr ON dr.id=fp.run_id AND dr.workspace_id=fp.workspace_id WHERE fp.workspace_id=$1 AND fp.key_id=$2 AND fp.scope=$3 AND fp.version=$4 AND fp.digest=$5 AND ($6::bigint=0 OR fp.source_id=$6) ORDER BY fp.id DESC LIMIT 201`, ws, in.KeyID, in.Scope, in.Version, in.Digest, in.SourceID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v := ValueMatch{Token: in.Token}
			if e = rows.Scan(&v.ID, &v.SourceID, &v.Source, &v.RunID, &v.RunRef, &v.AssetID, &v.ObservedAt, &v.Locator, &v.Category, &v.Current); e != nil {
				return e
			}
			v.Record = fmt.Sprintf("discovery:%d/run:%d/asset:%d", v.SourceID, v.RunID, v.AssetID)
			result.Items = append(result.Items, v)
		}
		return rows.Err()
	})
	if e == nil && in.SourceID == 0 {
		result.RuntimeItems, e = s.SearchRuntimeValueFingerprint(ctx, ws, actor, in.Token, 200)
	}
	if e == nil && in.SourceID == 0 {
		result.Relations, e = s.SearchCryptoTransformRelations(ctx, ws, actor, in.Token)
	}
	if len(result.Items) > 200 {
		result.Truncated = true
		result.Items = result.Items[:200]
	}
	return result, e
}
