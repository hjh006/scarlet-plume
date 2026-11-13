package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

type DiscoverySourceInput struct {
	Source          string   `json:"source"`
	Provider        string   `json:"provider"`
	System          string   `json:"system"`
	Database        string   `json:"database"`
	Scopes          []string `json:"scopes"`
	Service         string   `json:"service"`
	IntervalMinutes int      `json:"intervalMinutes"`
	Enabled         bool     `json:"enabled"`
	Revision        int64    `json:"revision"`
}
type DiscoverySource struct {
	DiscoverySourceInput
	ID        int64         `json:"id"`
	NextRunAt time.Time     `json:"nextRunAt"`
	Queued    bool          `json:"queued"`
	Running   bool          `json:"running"`
	LatestRun *DiscoveryRun `json:"latestRun,omitempty"`
}
type DiscoveryRun struct {
	ID         int64                       `json:"id"`
	SourceID   int64                       `json:"sourceId"`
	Revision   int64                       `json:"revision"`
	RunRef     string                      `json:"runRef"`
	Mode       string                      `json:"mode"`
	Status     string                      `json:"status"`
	ErrorCode  string                      `json:"errorCode"`
	ObservedAt time.Time                   `json:"observedAt"`
	CreatedAt  time.Time                   `json:"createdAt"`
	Coverage   collector.DiscoveryCoverage `json:"coverage"`
}
type DiscoveryClaim struct {
	SourceID int64                     `json:"sourceId"`
	Source   string                    `json:"source"`
	Revision int64                     `json:"revision"`
	RunRef   string                    `json:"runRef"`
	Mode     string                    `json:"mode"`
	Config   collector.DiscoveryConfig `json:"config"`
}
type DiscoverySubmission struct {
	SourceID   int64                     `json:"sourceId"`
	Revision   int64                     `json:"revision"`
	RunRef     string                    `json:"runRef"`
	Status     string                    `json:"status"`
	ErrorCode  string                    `json:"errorCode"`
	ObservedAt time.Time                 `json:"observedAt"`
	Result     collector.DiscoveryResult `json:"result"`
}

func discoveryDigest(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func validDiscoverySource(i DiscoverySourceInput) bool {
	if !validAutomationAssetSource(i.Source) || !validCheckText(i.Database, 128) || i.IntervalMinutes < 1 || i.IntervalMinutes > 1440 || len(i.Scopes) > 100 {
		return false
	}
	for _, s := range i.Scopes {
		if !validCheckText(s, 128) {
			return false
		}
	}
	switch i.Provider {
	case "local_files":
		if i.System != "local_files" || !collector.ValidFileScopes(i.Scopes) {
			return false
		}
	case "postgresql":
		if i.System != "postgresql" || len(i.Scopes) == 0 {
			return false
		}
	case "mysql", "mariadb":
		if i.System != "mysql" && i.System != "mariadb" {
			return false
		}
	case "mongodb":
		if i.System != "mongodb" {
			return false
		}
	case "openmetadata":
		if !validCheckText(i.Service, 256) {
			return false
		}
		switch i.System {
		case "postgresql", "mysql", "mariadb", "mongodb", "mssql", "oracle", "db2", "hive", "clickhouse", "snowflake", "bigquery", "redshift", "trino", "presto", "spark", "databricks":
		default:
			return false
		}
	default:
		return false
	}
	return true
}
func (s *Store) SaveDiscoverySource(ctx context.Context, workspaceID, actorID, id int64, i DiscoverySourceInput, tokenHash string) (DiscoverySource, error) {
	if i.Scopes == nil {
		i.Scopes = []string{}
	}
	if !validDiscoverySource(i) || id == 0 && !digestPattern.MatchString(tokenHash) {
		return DiscoverySource{}, ErrInvalid
	}
	var result DiscoverySource
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, workspaceID, actorID, true)
		if e != nil {
			return e
		}
		if role != "owner" {
			return ErrForbidden
		}
		scope, _ := json.Marshal(i.Scopes)
		if id == 0 {
			e = tx.QueryRow(ctx, `INSERT INTO plume.discovery_sources(workspace_id,source,provider,system,database_name,scopes,service,interval_minutes,enabled,token_hash,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, workspaceID, i.Source, i.Provider, i.System, i.Database, scope, i.Service, i.IntervalMinutes, i.Enabled, tokenHash, actorID).Scan(&id)
		} else {
			tag, err := tx.Exec(ctx, `UPDATE plume.discovery_sources SET source=$4,provider=$5,system=$6,database_name=$7,scopes=$8,service=$9,interval_minutes=$10,enabled=$11,revision=revision+1,lease_id=NULL,lease_until=NULL,lease_revision=NULL,requested_mode=NULL,next_run_at=now() WHERE workspace_id=$1 AND id=$2 AND revision=$3 AND provider=$5`, workspaceID, id, i.Revision, i.Source, i.Provider, i.System, i.Database, scope, i.Service, i.IntervalMinutes, i.Enabled)
			e = err
			if e == nil && tag.RowsAffected() == 0 {
				e = ErrConflict
			}
		}
		if e != nil {
			return storeError(e)
		}
		result, e = readDiscoverySource(ctx, tx, workspaceID, id)
		if e != nil {
			return e
		}
		return captureDataFlowSnapshot(ctx, tx, workspaceID, actorID, "discovery_config")
	})
	return result, storeError(err)
}

const discoverySourceSelect = `SELECT id,source,provider,system,database_name,scopes,service,interval_minutes,enabled,revision,next_run_at,requested_mode IS NOT NULL,COALESCE(lease_until>now(),false),latest_run_id FROM plume.discovery_sources WHERE workspace_id=$1`

func scanDiscoverySource(row pgx.Row) (DiscoverySource, *int64, error) {
	var s DiscoverySource
	var run *int64
	var scope []byte
	e := row.Scan(&s.ID, &s.Source, &s.Provider, &s.System, &s.Database, &scope, &s.Service, &s.IntervalMinutes, &s.Enabled, &s.Revision, &s.NextRunAt, &s.Queued, &s.Running, &run)
	if e == nil {
		e = json.Unmarshal(scope, &s.Scopes)
		if s.Scopes == nil {
			s.Scopes = []string{}
		}
	}
	return s, run, e
}
func readDiscoverySource(ctx context.Context, tx pgx.Tx, workspaceID, id int64) (DiscoverySource, error) {
	s, run, e := scanDiscoverySource(tx.QueryRow(ctx, discoverySourceSelect+` AND id=$2`, workspaceID, id))
	if e == nil && run != nil {
		r, re := readDiscoveryRun(ctx, tx, workspaceID, *run)
		if re != nil {
			return s, re
		}
		s.LatestRun = &r
	}
	return s, storeError(e)
}
func readDiscoveryRun(ctx context.Context, tx pgx.Tx, workspaceID, id int64) (DiscoveryRun, error) {
	var r DiscoveryRun
	var b []byte
	e := tx.QueryRow(ctx, `SELECT id,source_id,source_revision,run_ref,mode,status,error_code,observed_at,created_at,coverage FROM plume.discovery_runs WHERE workspace_id=$1 AND id=$2`, workspaceID, id).Scan(&r.ID, &r.SourceID, &r.Revision, &r.RunRef, &r.Mode, &r.Status, &r.ErrorCode, &r.ObservedAt, &r.CreatedAt, &b)
	if e == nil {
		e = json.Unmarshal(b, &r.Coverage)
	}
	return r, storeError(e)
}
func (s *Store) ListDiscoverySources(ctx context.Context, workspaceID int64) ([]DiscoverySource, error) {
	result := []DiscoverySource{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, discoverySourceSelect+` ORDER BY id LIMIT 100`, workspaceID)
		if e != nil {
			return e
		}
		var ids []int64
		for rows.Next() {
			v, _, e := scanDiscoverySource(rows)
			if e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, v.ID)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range ids {
			v, e := readDiscoverySource(ctx, tx, workspaceID, id)
			if e != nil {
				return e
			}
			result = append(result, v)
		}
		return nil
	})
	return result, e
}
func (s *Store) QueueDiscoverySource(ctx context.Context, workspaceID, actorID, id, revision int64, mode string) error {
	if mode != "collect" && mode != "test" {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, workspaceID, actorID, true)
		if e != nil {
			return e
		}
		if role != "owner" {
			return ErrForbidden
		}
		tag, e := tx.Exec(ctx, `UPDATE plume.discovery_sources SET requested_mode=$5 WHERE workspace_id=$1 AND id=$2 AND revision=$3 AND enabled AND (lease_until IS NULL OR lease_until<now()) AND created_by IN(SELECT user_id FROM plume.memberships WHERE workspace_id=$1 AND role='owner') AND $4>0`, workspaceID, id, revision, actorID, mode)
		if e == nil && tag.RowsAffected() == 0 {
			return ErrConflict
		}
		return e
	})
}
func (s *Store) ClaimDiscovery(ctx context.Context, tokenHash, runRef string) (*DiscoveryClaim, error) {
	var result *DiscoveryClaim
	if !digestPattern.MatchString(tokenHash) || len(runRef) != 32 {
		return nil, ErrInvalid
	}
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ws, id int64
		e := tx.QueryRow(ctx, `SELECT workspace_id,id FROM plume.discovery_sources WHERE token_hash=$1 AND enabled AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=discovery_sources.workspace_id AND m.user_id=created_by AND m.role='owner') FOR UPDATE`, tokenHash).Scan(&ws, &id)
		if e != nil {
			return storeError(e)
		}
		source, e := readDiscoverySource(ctx, tx, ws, id)
		if e != nil {
			return e
		}
		var mode *string
		var lease *time.Time
		e = tx.QueryRow(ctx, `SELECT requested_mode,lease_until FROM plume.discovery_sources WHERE id=$1`, id).Scan(&mode, &lease)
		if e != nil {
			return e
		}
		if lease != nil && lease.After(time.Now()) || mode == nil && source.NextRunAt.After(time.Now()) {
			return nil
		}
		m := "collect"
		if mode != nil {
			m = *mode
		}
		claim := &DiscoveryClaim{SourceID: id, Source: source.Source, Revision: source.Revision, RunRef: runRef, Mode: m, Config: collector.DiscoveryConfig{Provider: source.Provider, System: source.System, Database: source.Database, Scopes: source.Scopes, Service: source.Service}}
		claim.Config.Rules, e = readPublishedDiscoveryRules(ctx, tx, ws)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE plume.discovery_sources SET lease_id=$2,lease_until=now()+interval '2 minutes',lease_revision=revision,lease_rules=$4,requested_mode=$3,next_run_at=now()+interval_minutes*interval '1 minute' WHERE id=$1`, id, runRef, m, claim.Config.Rules)
		result = claim
		return e
	})
	return result, e
}
func (s *Store) ListDiscoveryRuns(ctx context.Context, workspaceID, sourceID int64) ([]DiscoveryRun, error) {
	result := []DiscoveryRun{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id FROM plume.discovery_runs WHERE workspace_id=$1 AND source_id=$2 ORDER BY id DESC LIMIT 30`, workspaceID, sourceID)
		if e != nil {
			return e
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range ids {
			run, e := readDiscoveryRun(ctx, tx, workspaceID, id)
			if e != nil {
				return e
			}
			result = append(result, run)
		}
		return nil
	})
	return result, e
}
func (s *Store) SubmitDiscovery(ctx context.Context, tokenHash string, input DiscoverySubmission) (DiscoveryRun, error) {
	var result DiscoveryRun
	if input.Status != "success" && input.Status != "error" || len(input.RunRef) != 32 || input.ObservedAt.Before(time.Now().Add(-5*time.Minute)) || input.ObservedAt.After(time.Now().Add(time.Minute)) {
		return result, ErrInvalid
	}
	if input.Status == "error" && input.ErrorCode != "collection_failed" && input.ErrorCode != "collection_timeout" {
		return result, ErrInvalid
	}
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ws, id, revision, actorID int64
		var mode string
		var runRef *string
		var until *time.Time
		e := tx.QueryRow(ctx, `SELECT workspace_id,id,revision,COALESCE(requested_mode,'collect'),lease_id,lease_until,created_by FROM plume.discovery_sources WHERE token_hash=$1 AND id=$2 AND enabled AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=discovery_sources.workspace_id AND m.user_id=created_by AND m.role='owner') FOR UPDATE`, tokenHash, input.SourceID).Scan(&ws, &id, &revision, &mode, &runRef, &until, &actorID)
		if e != nil {
			return storeError(e)
		}
		var previous int64
		var previousDigest string
		e = tx.QueryRow(ctx, `SELECT id,payload_digest FROM plume.discovery_runs WHERE source_id=$1 AND run_ref=$2`, id, input.RunRef).Scan(&previous, &previousDigest)
		if e == nil {
			if previousDigest != discoveryDigest(input) {
				return ErrConflict
			}
			result, e = readDiscoveryRun(ctx, tx, ws, previous)
			return e
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if revision != input.Revision || runRef == nil || *runRef != input.RunRef || until == nil || until.Before(time.Now()) {
			return ErrConflict
		}
		source, e := readDiscoverySource(ctx, tx, ws, id)
		if e != nil {
			return e
		}
		rules, e := readPublishedDiscoveryRules(ctx, tx, ws)
		if e != nil {
			return e
		}
		var leaseRules []collector.DiscoveryRule
		if e = tx.QueryRow(ctx, `SELECT lease_rules FROM plume.discovery_sources WHERE id=$1`, id).Scan(&leaseRules); e != nil {
			return e
		}
		if input.Status == "success" && discoveryDigest(leaseRules) != discoveryDigest(rules) {
			return ErrConflict
		}
		cfg := collector.DiscoveryConfig{Provider: source.Provider, System: source.System, Database: source.Database, Scopes: source.Scopes, Service: source.Service, Rules: rules}
		if input.Status == "success" {
			if collector.ValidateDiscoveryResult(cfg, input.Result) != nil {
				return ErrInvalid
			}
			allowed := map[string]collector.DiscoveryRule{}
			for _, r := range rules {
				allowed[r.Key] = r
			}
			for _, table := range input.Result.Tables {
				for _, field := range table.Fields {
					for _, h := range field.Hits {
						r, ok := allowed[h.RuleKey]
						if !ok && (h.RuleKey == "builtin.national_id" && h.Category == "national_id" || h.RuleKey == "builtin.financial_account" && h.Category == "financial_account") && h.RuleVersion == 1 && h.Method == "source_aggregate" {
							continue
						}
						if !ok || r.Version != h.RuleVersion || r.Category != h.Category {
							return ErrConflict
						}
					}
				}
			}
		}
		coverage, _ := json.Marshal(input.Result.Coverage)
		var newID int64
		e = tx.QueryRow(ctx, `INSERT INTO plume.discovery_runs(workspace_id,source_id,source_revision,run_ref,mode,status,error_code,observed_at,coverage,payload_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, ws, id, revision, input.RunRef, mode, input.Status, input.ErrorCode, input.ObservedAt, coverage, discoveryDigest(input)).Scan(&newID)
		if e != nil {
			return e
		}
		if input.Status == "success" && mode == "collect" {
			if e = saveDiscoveryCatalog(ctx, tx, ws, source, newID, input); e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `UPDATE plume.discovery_sources SET latest_run_id=CASE WHEN $3='collect' OR $4='error' THEN $2 ELSE latest_run_id END,lease_id=NULL,lease_until=NULL,lease_revision=NULL,requested_mode=NULL WHERE id=$1`, id, newID, mode, input.Status)
		if e != nil {
			return e
		}
		result, e = readDiscoveryRun(ctx, tx, ws, newID)
		if e != nil {
			return e
		}
		if mode == "collect" || input.Status == "error" {
			return captureDataFlowSnapshot(ctx, tx, ws, actorID, "discovery")
		}
		return nil
	})
	return result, storeError(e)
}

// RotateDiscoveryToken invalidates the previous credential and any in-flight lease.
func (s *Store) RotateDiscoveryToken(ctx context.Context, ws, actor, id, revision int64, digest string) error {
	if !digestPattern.MatchString(digest) {
		return ErrInvalid
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if role != "owner" {
			return ErrForbidden
		}
		tag, e := tx.Exec(ctx, `UPDATE plume.discovery_sources SET token_hash=$4,revision=revision+1,lease_id=NULL,lease_until=NULL,lease_revision=NULL,requested_mode=NULL,next_run_at=now() WHERE workspace_id=$1 AND id=$2 AND revision=$3`, ws, id, revision, digest)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return captureDataFlowSnapshot(ctx, tx, ws, actor, "discovery_token")
	})
}
