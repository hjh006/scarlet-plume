package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

func readPublishedDiscoveryRules(ctx context.Context, tx pgx.Tx, workspaceID int64) ([]collector.DiscoveryRule, error) {
	rules := collector.BuiltinDiscoveryRules()
	rows, e := tx.Query(ctx, `SELECT rule_key,version,name,kind,target,category,expression,dictionary,status FROM plume.discovery_rule_versions WHERE workspace_id=$1 AND status='published' ORDER BY rule_key LIMIT 101`, workspaceID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var r collector.DiscoveryRule
		var dictionary []byte
		if e = rows.Scan(&r.Key, &r.Version, &r.Name, &r.Kind, &r.Target, &r.Category, &r.Expression, &dictionary, &r.Status); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(dictionary, &r.Dictionary); e != nil {
			return nil, e
		}
		rules = append(rules, r)
	}
	if len(rules) > 103 {
		return nil, ErrInvalid
	}
	return rules, rows.Err()
}
func (s *Store) ListDiscoveryRules(ctx context.Context, workspaceID int64) ([]collector.DiscoveryRule, error) {
	rules := collector.BuiltinDiscoveryRules()
	rows, e := s.pool.Query(ctx, `SELECT rule_key,version,name,kind,target,category,expression,dictionary,status FROM plume.discovery_rule_versions WHERE workspace_id=$1 ORDER BY rule_key,version DESC LIMIT 500`, workspaceID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var r collector.DiscoveryRule
		var dictionary []byte
		if e = rows.Scan(&r.Key, &r.Version, &r.Name, &r.Kind, &r.Target, &r.Category, &r.Expression, &dictionary, &r.Status); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(dictionary, &r.Dictionary); e != nil {
			return nil, e
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}
func (s *Store) SaveDiscoveryRule(ctx context.Context, workspaceID, actorID int64, input collector.DiscoveryRule) (collector.DiscoveryRule, error) {
	if collector.ValidateDiscoveryRule(input) != nil || strings.HasPrefix(input.Key, "builtin.") || input.Status != "draft" && input.Status != "published" {
		return input, ErrInvalid
	}
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, workspaceID, actorID, true)
		if e != nil {
			return e
		}
		if role != "owner" && role != "operator" {
			return ErrForbidden
		}
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1,$2)`, int32(workspaceID), int32(0x444452)); e != nil {
			return e
		}
		var next int
		e = tx.QueryRow(ctx, `SELECT COALESCE(max(version),0)+1 FROM plume.discovery_rule_versions WHERE workspace_id=$1 AND rule_key=$2`, workspaceID, input.Key).Scan(&next)
		if e != nil {
			return e
		}
		if input.Version != next {
			return ErrConflict
		}
		if input.Status == "published" {
			_, e = tx.Exec(ctx, `UPDATE plume.discovery_rule_versions SET status='retired' WHERE workspace_id=$1 AND rule_key=$2 AND status='published'`, workspaceID, input.Key)
			if e != nil {
				return e
			}
		}
		dictionary, _ := json.Marshal(input.Dictionary)
		if input.Dictionary == nil {
			dictionary = []byte(`[]`)
		}
		_, e = tx.Exec(ctx, `INSERT INTO plume.discovery_rule_versions(workspace_id,rule_key,version,name,kind,target,category,expression,dictionary,status,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, workspaceID, input.Key, input.Version, input.Name, input.Kind, input.Target, input.Category, input.Expression, dictionary, input.Status, actorID)
		return e
	})
	return input, storeError(e)
}
func (s *Store) PublishDiscoveryRule(ctx context.Context, workspaceID, actorID int64, key string, version int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, workspaceID, actorID, true)
		if e != nil {
			return e
		}
		if role != "owner" && role != "operator" {
			return ErrForbidden
		}
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1,$2)`, int32(workspaceID), int32(0x444452)); e != nil {
			return e
		}
		var status string
		if e = tx.QueryRow(ctx, `SELECT status FROM plume.discovery_rule_versions WHERE workspace_id=$1 AND rule_key=$2 AND version=$3 FOR UPDATE`, workspaceID, key, version).Scan(&status); e != nil {
			return storeError(e)
		}
		if status != "draft" {
			return ErrConflict
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.discovery_rule_versions SET status='retired' WHERE workspace_id=$1 AND rule_key=$2 AND status='published'`, workspaceID, key); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE plume.discovery_rule_versions SET status='published' WHERE workspace_id=$1 AND rule_key=$2 AND version=$3`, workspaceID, key, version)
		return e
	})
}
