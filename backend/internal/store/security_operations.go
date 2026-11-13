package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5"
)

var operationsKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

const operationsSourceFields = `id,name,kind,owner_team,scope,created_at,last_seen,revoked_at`

func scanOperationsSource(row pgx.Row) (OperationsSource, error) {
	var source OperationsSource
	err := row.Scan(&source.ID, &source.Name, &source.Kind, &source.OwnerTeam, &source.Scope, &source.CreatedAt, &source.LastSeen, &source.RevokedAt)
	return source, storeError(err)
}

func (s *Store) ListOperationsSources(ctx context.Context, workspaceID, actorID int64) ([]OperationsSource, error) {
	var result []OperationsSource
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+operationsSourceFields+" FROM plume.operations_sources WHERE workspace_id=$1 ORDER BY id DESC LIMIT 100", workspaceID)
		if err != nil {
			return err
		}
		result, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperationsSource, error) { return scanOperationsSource(row) })
		return err
	})
	if result == nil {
		result = []OperationsSource{}
	}
	return result, err
}

func (s *Store) CreateOperationsSource(ctx context.Context, workspaceID, actorID int64, input OperationsSourceInput, digest string) (OperationsSource, error) {
	if !validCheckText(input.Name, 80) || !validCheckText(input.OwnerTeam, 80) || !digestPattern.MatchString(digest) || !slices.Contains([]string{"security_events", "postgres_control"}, input.Kind) {
		return OperationsSource{}, ErrInvalid
	}
	var source OperationsSource
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		var count int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM plume.operations_sources WHERE workspace_id=$1 AND revoked_at IS NULL", workspaceID).Scan(&count); err != nil {
			return err
		}
		if count >= 50 {
			return ErrInvalid
		}
		scope := OperationsScope{}
		if input.Kind == "postgres_control" {
			if input.JobID < 1 {
				return ErrInvalid
			}
			err := tx.QueryRow(ctx, `SELECT j.id,j.source,p.host,p.port,p.database_name,p.schemas FROM plume.automation_jobs j JOIN plume.managed_postgres_jobs p ON p.job_id=j.id WHERE j.workspace_id=$1 AND j.id=$2 AND j.revoked_at IS NULL`, workspaceID, input.JobID).Scan(&scope.JobID, &scope.Source, &scope.Host, &scope.Port, &scope.Database, &scope.Schemas)
			if err != nil {
				return storeError(err)
			}
		} else if input.JobID != 0 {
			return ErrInvalid
		}
		source, err = scanOperationsSource(tx.QueryRow(ctx, `INSERT INTO plume.operations_sources(workspace_id,actor_id,name,kind,owner_team,scope,token_digest) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+operationsSourceFields, workspaceID, actorID, input.Name, input.Kind, input.OwnerTeam, scope, digest))
		return err
	})
	return source, storeError(err)
}

func (s *Store) RevokeOperationsSource(ctx context.Context, workspaceID, actorID, id int64) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM plume.operations_sources WHERE workspace_id=$1 AND id=$2 FOR NO KEY UPDATE`, workspaceID, id).Scan(&kind); err != nil {
			return storeError(err)
		}
		if kind == "postgres_control" {
			var active bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.control_actions WHERE workspace_id=$1 AND operations_source_id=$2 AND (status IN('applied','rollback_requested') OR status='approved' AND expires_at>clock_timestamp()))`, workspaceID, id).Scan(&active); err != nil {
				return err
			}
			if active {
				return ErrConflict
			}
		}
		result, err := tx.Exec(ctx, "UPDATE plume.operations_sources SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE workspace_id=$1 AND id=$2", workspaceID, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func operationsSourceActor(ctx context.Context, tx pgx.Tx, kind, digest string) (OperationsSource, int64, int64, error) {
	if !digestPattern.MatchString(digest) {
		return OperationsSource{}, 0, 0, ErrAutomationUnauthorized
	}
	var source OperationsSource
	var workspaceID, actorID int64
	err := tx.QueryRow(ctx, "SELECT "+operationsSourceFields+`,workspace_id,actor_id FROM plume.operations_sources WHERE token_digest=$1 AND kind=$2 AND revoked_at IS NULL FOR NO KEY UPDATE`, digest, kind).Scan(&source.ID, &source.Name, &source.Kind, &source.OwnerTeam, &source.Scope, &source.CreatedAt, &source.LastSeen, &source.RevokedAt, &workspaceID, &actorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return source, 0, 0, ErrAutomationUnauthorized
	}
	if err != nil {
		return source, 0, 0, err
	}
	role, err := checkRole(ctx, tx, workspaceID, actorID, true)
	if err != nil || role != "owner" {
		return source, 0, 0, ErrAutomationUnauthorized
	}
	return source, workspaceID, actorID, nil
}

func (s *Store) AuthorizeOperationsSource(ctx context.Context, kind, digest string) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error { _, _, _, err := operationsSourceActor(ctx, tx, kind, digest); return err })
}

func validSecurityEvent(input *SecurityEventInput) bool {
	if input.DataContext != nil && input.DataContext.Validate() != nil {
		return false
	}
	if input.DataContext != nil && input.DataContext.Categories == nil {
		input.DataContext.Categories = []string{}
	}
	if input.Outcome == "" {
		input.Outcome = "observed"
	}
	input.ObservedAt = input.ObservedAt.UTC().Truncate(time.Microsecond)
	if !operationsKey.MatchString(input.EventID) || !operationsKey.MatchString(input.Rule) || !slices.Contains([]string{"observed", "cleared", "activity"}, input.Outcome) || !slices.Contains([]string{"api", "data_leak", "access", "config", "malware", "backup", "key", "custom"}, input.Category) || !slices.Contains([]string{"low", "medium", "high", "critical"}, input.Severity) || input.Count < 0 || input.Count > 1000000000 || input.WindowSeconds < 1 || input.WindowSeconds > 86400 || input.ObservedAt.Before(time.Now().Add(-7*24*time.Hour)) || input.ObservedAt.After(time.Now().Add(5*time.Minute)) {
		return false
	}
	if input.Outcome == "activity" && (input.DataContext == nil || !slices.Contains([]string{"observed", "allowed"}, input.DataContext.Enforcement) || input.Count <= 0) {
		return false
	}
	if input.Outcome == "observed" && input.Count == 0 || input.Outcome == "cleared" && input.Count != 0 {
		return false
	}
	if input.Service != "" && (!validCheckText(input.Service, 80) || strings.IndexFunc(input.Service, unicode.IsControl) >= 0) {
		return false
	}
	if input.Method != "" || input.Route != "" {
		o, err := runtimesecurity.NormalizeObservation(runtimesecurity.Observation{Method: input.Method, Route: input.Route, Status: 200, ObservedAt: input.ObservedAt})
		if err != nil {
			return false
		}
		input.Method, input.Route = o.Method, o.Route
	}
	return input.AssetID >= 0
}

func (s *Store) ReceiveSecurityEvent(ctx context.Context, tokenDigest string, input SecurityEventInput) (OperationsSecurityEvent, bool, error) {
	if !validSecurityEvent(&input) {
		return OperationsSecurityEvent{}, false, ErrInvalid
	}
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	scopeRaw, _ := json.Marshal([]string{input.Category, input.Service, input.Method, runtimesecurity.RouteKey(input.Route)})
	if d := input.DataContext; d != nil {
		// A clear from another subject/device/file/channel must not clear this event's check.
		scopeRaw, _ = json.Marshal([]string{input.Category, input.Service, input.Method, runtimesecurity.RouteKey(input.Route), d.Kind, d.Channel, d.SubjectRef, d.DeviceRef, d.FileDigest, d.DestinationDomain, d.DatabaseRef, d.ObjectRef, d.Operation})
	}
	scopeSum := sha256.Sum256(scopeRaw)
	scopeKey := hex.EncodeToString(scopeSum[:])
	var result OperationsSecurityEvent
	var replayed bool
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		source, workspaceID, actorID, err := operationsSourceActor(ctx, tx, "security_events", tokenDigest)
		if err != nil {
			return err
		}
		var existingDigest string
		var replayAssetID int64
		err = tx.QueryRow(ctx, `SELECT e.id,e.asset_id,COALESCE(e.check_id,0),e.digest,e.metadata,COALESCE(c.status,'') FROM plume.operations_security_events e LEFT JOIN plume.checks c ON c.id=e.check_id AND c.workspace_id=e.workspace_id WHERE e.source_id=$1 AND e.event_id=$2`, source.ID, input.EventID).Scan(&result.ID, &replayAssetID, &result.CheckID, &existingDigest, &result.SecurityEventInput, &result.CheckStatus)
		if err == nil {
			if existingDigest != digest {
				return ErrConflict
			}
			replayed = true
			result.AssetID = replayAssetID
			result.SourceID, result.Source = source.ID, source.Name
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := validateEndpointSecurityEvent(ctx, tx, workspaceID, source.ID, input); err != nil {
			return err
		}
		assetID := input.AssetID
		if assetID > 0 {
			var found bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM plume.assets WHERE workspace_id=$1 AND id=$2)", workspaceID, assetID).Scan(&found); err != nil {
				return err
			}
			if !found {
				return ErrNotFound
			}
		} else {
			ref := fmt.Sprint(source.ID)
			err = tx.QueryRow(ctx, `INSERT INTO plume.assets(workspace_id,name,system,owner_team,source_provider,source,source_ref) VALUES($1,$2,'安全事件来源',$3,'security-events',$4,$5) ON CONFLICT(workspace_id,source_provider,source,source_ref) DO UPDATE SET name=plume.assets.name RETURNING id`, workspaceID, "安全信号 · "+source.Name, source.OwnerTeam, source.Name, ref).Scan(&assetID)
			if err != nil {
				return err
			}
		}
		var checkID int64
		var status string
		if input.Outcome != "activity" {
			var latestPositive time.Time
			err = tx.QueryRow(ctx, `SELECT c.id,c.status FROM plume.operations_security_events e JOIN plume.checks c ON c.id=e.check_id AND c.workspace_id=e.workspace_id WHERE e.source_id=$1 AND e.asset_id=$2 AND e.rule=$3 AND e.scope_key=$4 AND e.metadata->>'outcome'='observed' ORDER BY e.id DESC LIMIT 1 FOR NO KEY UPDATE OF c`, source.ID, assetID, input.Rule, scopeKey).Scan(&checkID, &status)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if checkID > 0 {
				if err := tx.QueryRow(ctx, `SELECT max(observed_at) FROM plume.operations_security_events WHERE check_id=$1 AND metadata->>'outcome'='observed'`, checkID).Scan(&latestPositive); err != nil {
					return err
				}
			}
			if input.Outcome == "cleared" && (checkID == 0 || !input.ObservedAt.After(latestPositive)) {
				return ErrEvidenceRequired
			}
			newCycle := checkID == 0
			if input.Outcome == "observed" && status == "verified" {
				var reviewed time.Time
				if err := tx.QueryRow(ctx, `SELECT created_at FROM plume.check_events WHERE check_id=$1 AND action='verified' ORDER BY id DESC LIMIT 1`, checkID).Scan(&reviewed); err != nil {
					return err
				}
				var latestClear *time.Time
				if err := tx.QueryRow(ctx, `SELECT max(observed_at) FROM plume.operations_security_events WHERE check_id=$1 AND metadata->>'outcome'='cleared'`, checkID).Scan(&latestClear); err != nil {
					return err
				}
				newCycle = input.ObservedAt.After(reviewed) && (latestClear == nil || input.ObservedAt.After(*latestClear))
			}
			if input.Outcome == "observed" && newCycle {
				if err := tx.QueryRow(ctx, `INSERT INTO plume.checks(workspace_id,asset_id,requirement_code,owner_team,note,created_by) VALUES($1,$2,'G03',$3,$4,$5) RETURNING id`, workspaceID, assetID, source.OwnerTeam, "外部安全信号："+input.Rule+"。该结果为已授权来源的声明，需结合范围和证据复核。", actorID).Scan(&checkID); err != nil {
					return err
				}
				if err := recordCheckEvent(ctx, tx, checkID, actorID, "created", "外部安全信号自动生成检查"); err != nil {
					return err
				}
				status = "draft"
			}
			if input.Outcome == "observed" && status == "in_review" {
				var latestClear *time.Time
				if err := tx.QueryRow(ctx, `SELECT max(observed_at) FROM plume.operations_security_events WHERE check_id=$1 AND metadata->>'outcome'='cleared'`, checkID).Scan(&latestClear); err != nil {
					return err
				}
				if latestClear == nil || !input.ObservedAt.Before(*latestClear) {
					if _, err := tx.Exec(ctx, `UPDATE plume.checks SET status='needs_work',updated_at=clock_timestamp() WHERE id=$1`, checkID); err != nil {
						return err
					}
					if err := recordCheckEvent(ctx, tx, checkID, actorID, "changes", "自动采集发现新的正信号，提交快照失效"); err != nil {
						return err
					}
					status = "needs_work"
				}
			}
		}
		result = OperationsSecurityEvent{SecurityEventInput: input, SourceID: source.ID, Source: source.Name, CheckID: checkID, CheckStatus: status}
		result.AssetID = assetID
		if err := tx.QueryRow(ctx, `INSERT INTO plume.operations_security_events(workspace_id,source_id,event_id,digest,observed_at,category,rule,scope_key,severity,asset_id,check_id,metadata) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11::bigint,0),$12) RETURNING id`, workspaceID, source.ID, input.EventID, digest, input.ObservedAt, input.Category, input.Rule, scopeKey, input.Severity, assetID, checkID, input).Scan(&result.ID); err != nil {
			return err
		}
		if status == "draft" || status == "needs_work" {
			check, err := scanCheck(tx.QueryRow(ctx, checkSelect+" WHERE c.workspace_id=$1 AND c.id=$2", workspaceID, checkID))
			if err != nil {
				return err
			}
			kind := "observation"
			if input.Outcome == "cleared" {
				remediation, err := operationsRemediationAt(ctx, tx, checkID)
				if err != nil {
					return err
				}
				if remediation.IsZero() || !input.ObservedAt.After(remediation) {
					return ErrEvidenceRequired
				}
				kind = "retest"
			}
			summary := fmt.Sprintf("来源声明 %s · 规则 %s · 类别 %s · %d 次 / %d 秒 · %s。原始日志未上传。", source.Name, input.Rule, input.Category, input.Count, input.WindowSeconds, input.Outcome)
			if _, err := addEvidenceTx(ctx, tx, check, actorID, EvidenceInput{Kind: kind, SourceSystem: "安全事件集成", SourceRecord: fmt.Sprintf("operations-event:%d", result.ID), Summary: summary, ObservedAt: input.ObservedAt}, true); err != nil {
				return err
			}
		}
		if err := enqueueSecurityEventDeliveries(ctx, tx, workspaceID, actorID, result); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE plume.operations_sources SET last_seen=clock_timestamp() WHERE id=$1", source.ID)
		return err
	})
	return result, replayed, storeError(err)
}

func (s *Store) ListOperationsSecurityEvents(ctx context.Context, workspaceID, actorID int64) ([]OperationsSecurityEvent, error) {
	var events []OperationsSecurityEvent
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT e.id,e.source_id,s.name,COALESCE(e.check_id,0),COALESCE(c.status,''),e.asset_id,e.metadata FROM plume.operations_security_events e JOIN plume.operations_sources s ON s.id=e.source_id LEFT JOIN plume.checks c ON c.id=e.check_id AND c.workspace_id=e.workspace_id WHERE e.workspace_id=$1 ORDER BY e.id DESC LIMIT 500`, workspaceID)
		if err != nil {
			return err
		}
		events, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperationsSecurityEvent, error) {
			var e OperationsSecurityEvent
			var assetID int64
			err := row.Scan(&e.ID, &e.SourceID, &e.Source, &e.CheckID, &e.CheckStatus, &assetID, &e.SecurityEventInput)
			e.AssetID = assetID
			return e, err
		})
		return err
	})
	if events == nil {
		events = []OperationsSecurityEvent{}
	}
	return events, err
}

func operationsRemediationAt(ctx context.Context, tx pgx.Tx, checkID int64) (time.Time, error) {
	change, err := latestCheckEvent(ctx, tx, checkID, "changes")
	if err != nil {
		return time.Time{}, err
	}
	var remediation time.Time
	err = tx.QueryRow(ctx, `SELECT GREATEST(observed_at,created_at) FROM plume.check_evidence WHERE check_id=$1 AND kind='remediation' AND observed_at>$2 AND created_at>$2 ORDER BY id DESC LIMIT 1`, checkID, change.CreatedAt).Scan(&remediation)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return remediation, err
}

func operationsVerificationAllowed(ctx context.Context, tx pgx.Tx, check Check) (bool, error) {
	var lastPositive *time.Time
	err := tx.QueryRow(ctx, `SELECT max(observed_at) FROM plume.operations_security_events WHERE check_id=$1 AND metadata->>'outcome'='observed'`, check.ID).Scan(&lastPositive)
	if err != nil {
		return false, err
	}
	if lastPositive == nil {
		return true, nil
	}
	remediation, err := operationsRemediationAt(ctx, tx, check.ID)
	if err != nil || remediation.IsZero() {
		return false, err
	}
	var submittedAt *time.Time
	if check.Status == "in_review" {
		submission, err := latestCheckEvent(ctx, tx, check.ID, "submitted")
		if err != nil {
			return false, err
		}
		if submission.ID == 0 {
			return false, nil
		}
		submittedAt = &submission.CreatedAt
	}
	var valid bool
	// A later healthy heartbeat need not mutate the submitted evidence set.
	// Use a qualifying source clear already represented in that frozen set;
	// a later positive still invalidates it by its observation timestamp.
	err = tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM plume.operations_security_events e JOIN plume.check_evidence proof
 ON proof.check_id=e.check_id AND proof.kind='retest' AND proof.source_system='安全事件集成'
 AND proof.source_record='operations-event:'||e.id::text AND proof.observed_at=e.observed_at
 WHERE e.check_id=$1 AND e.metadata->>'outcome'='cleared'
 AND e.observed_at>$2 AND e.observed_at>$3 AND e.created_at>$3 AND proof.created_at>$3
 AND ($4::timestamptz IS NULL OR proof.created_at<=$4))`, check.ID, *lastPositive, remediation, submittedAt).Scan(&valid)
	return valid, err
}
