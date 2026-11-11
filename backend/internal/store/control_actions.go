package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5"
)

const controlActionSelect = `SELECT a.id,a.kind,u.username,COALESCE(p.username,''),a.runtime_source_id,a.operations_source_id,a.asset_id,a.check_id,a.spec,a.reason,a.status,a.revision,a.result,a.expires_at,a.created_at,a.updated_at FROM plume.control_actions a JOIN plume.users u ON u.id=a.actor_id LEFT JOIN plume.users p ON p.id=a.approved_by`

func scanControlAction(row pgx.Row) (ControlAction, error) {
	var a ControlAction
	err := row.Scan(&a.ID, &a.Kind, &a.Actor, &a.ApprovedBy, &a.RuntimeSourceID, &a.OperationsSourceID, &a.AssetID, &a.CheckID, &a.Spec, &a.Reason, &a.Status, &a.Revision, &a.Result, &a.ExpiresAt, &a.CreatedAt, &a.UpdatedAt)
	return a, storeError(err)
}
func controlOptionalID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}
func recordControlEvent(ctx context.Context, tx pgx.Tx, workspaceID, actionID, actorID int64, action, note string, automated bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO plume.control_action_events(workspace_id,action_id,actor_id,action,note,automated) VALUES($1,$2,$3,$4,$5,$6)`, workspaceID, actionID, actorID, action, note, automated)
	return err
}

func currentOperationsScope(ctx context.Context, tx pgx.Tx, workspaceID int64, scope OperationsScope, lock bool) (bool, error) {
	var current OperationsScope
	query := `SELECT j.source,p.host,p.port,p.database_name,p.schemas FROM plume.automation_jobs j JOIN plume.managed_postgres_jobs p ON p.job_id=j.id WHERE j.workspace_id=$1 AND j.id=$2 AND j.revoked_at IS NULL`
	if lock {
		query += " FOR SHARE OF j,p"
	}
	err := tx.QueryRow(ctx, query, workspaceID, scope.JobID).Scan(&current.Source, &current.Host, &current.Port, &current.Database, &current.Schemas)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return current.Source == scope.Source && current.Host == scope.Host && current.Port == scope.Port && current.Database == scope.Database && slices.Equal(current.Schemas, scope.Schemas), nil
}
func (s *Store) ListControlActions(ctx context.Context, workspaceID, actorID int64) ([]ControlAction, error) {
	var result []ControlAction
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, controlActionSelect+" WHERE a.workspace_id=$1 ORDER BY a.id DESC LIMIT 200", workspaceID)
		if err != nil {
			return err
		}
		result, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ControlAction, error) { return scanControlAction(row) })
		return err
	})
	if result == nil {
		result = []ControlAction{}
	}
	return result, err
}
func (s *Store) ControlAction(ctx context.Context, workspaceID, actorID, id int64) (ControlAction, error) {
	var a ControlAction
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		var err error
		a, err = scanControlAction(tx.QueryRow(ctx, controlActionSelect+" WHERE a.workspace_id=$1 AND a.id=$2", workspaceID, id))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT e.action,u.username,e.note,e.automated,e.created_at FROM plume.control_action_events e JOIN plume.users u ON u.id=e.actor_id WHERE e.workspace_id=$1 AND e.action_id=$2 ORDER BY e.id`, workspaceID, id)
		if err != nil {
			return err
		}
		a.Events, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ControlEvent, error) {
			var e ControlEvent
			err := row.Scan(&e.Action, &e.Actor, &e.Note, &e.Automated, &e.CreatedAt)
			return e, err
		})
		return err
	})
	return a, err
}
func (s *Store) CreateControlAction(ctx context.Context, workspaceID, actorID int64, input ControlActionInput) (ControlAction, error) {
	if !validCheckText(input.Reason, 400) || input.DurationMinutes < 1 || input.DurationMinutes > 1440 || input.AssetID < 0 || input.CheckID < 0 {
		return ControlAction{}, ErrInvalid
	}
	var id int64
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		var spec ControlSpec
		switch input.Kind {
		case "block_api":
			if input.RuntimeSourceID < 1 || input.OperationsSourceID != 0 || input.Role != "" {
				return ErrInvalid
			}
			err := tx.QueryRow(ctx, `SELECT s.service FROM plume.runtime_security_sources s WHERE s.workspace_id=$1 AND s.id=$2 AND s.kind='gateway' AND s.revoked_at IS NULL`, workspaceID, input.RuntimeSourceID).Scan(&spec.Service)
			if err != nil {
				return storeError(err)
			}
			o, err := runtimesecurity.NormalizeObservation(runtimesecurity.Observation{Method: input.Method, Route: input.Route, Status: 403, ObservedAt: time.Now()})
			if err != nil {
				return ErrInvalid
			}
			var observed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.runtime_security_routes WHERE source_id=$1 AND method=$2 AND route_key=$3)`, input.RuntimeSourceID, o.Method, runtimesecurity.RouteKey(o.Route)).Scan(&observed); err != nil {
				return err
			}
			if !observed {
				return ErrEvidenceRequired
			}
			spec.Method, spec.Route = o.Method, o.Route
		case "revoke_select":
			if input.OperationsSourceID < 1 || input.RuntimeSourceID != 0 || input.AssetID < 1 || input.Method != "" || input.Route != "" {
				return ErrInvalid
			}
			var scope OperationsScope
			if err := tx.QueryRow(ctx, `SELECT scope FROM plume.operations_sources WHERE workspace_id=$1 AND id=$2 AND kind='postgres_control' AND revoked_at IS NULL`, workspaceID, input.OperationsSourceID).Scan(&scope); err != nil {
				return storeError(err)
			}
			var table collector.PostgreSQLTable
			err := tx.QueryRow(ctx, `SELECT i.inspection FROM plume.postgres_asset_inspections i JOIN plume.assets a ON a.id=i.asset_id AND a.workspace_id=$1 JOIN plume.automation_jobs j ON j.id=i.job_id AND j.workspace_id=$1 AND j.revoked_at IS NULL JOIN plume.managed_postgres_jobs p ON p.job_id=j.id JOIN LATERAL(SELECT id,status FROM plume.automation_runs WHERE job_id=j.id ORDER BY id DESC LIMIT 1) latest ON true WHERE i.asset_id=$2 AND j.id=$3 AND latest.status='success' AND latest.id=i.automation_run_id AND (i.source_host,i.source_port,i.source_database)=(p.host,p.port,p.database_name) AND (p.host,p.port,p.database_name)=($4,$5,$6)`, workspaceID, input.AssetID, scope.JobID, scope.Host, scope.Port, scope.Database).Scan(&table)
			if err != nil {
				return storeError(err)
			}
			roleName := input.Role
			if roleName == "" {
				roleName = "PUBLIC"
			}
			if !validCheckText(roleName, 63) {
				return ErrInvalid
			}
			allowed := false
			for _, grant := range table.Grants {
				if grant.Column == "" && grant.Privilege == "SELECT" && !grant.Grantable && (roleName == "PUBLIC" && grant.Public || roleName != "PUBLIC" && grant.Grantee == roleName && !grant.Public) {
					allowed = true
				}
			}
			if !allowed || !slices.Contains(scope.Schemas, table.Schema) {
				return ErrEvidenceRequired
			}
			spec = ControlSpec{Host: scope.Host, Port: scope.Port, Database: scope.Database, Schema: table.Schema, Table: table.Name, Role: roleName, ExpectedSelect: true}
		default:
			return ErrInvalid
		}
		if input.AssetID > 0 {
			var found bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM plume.assets WHERE workspace_id=$1 AND id=$2)", workspaceID, input.AssetID).Scan(&found); err != nil {
				return err
			}
			if !found {
				return ErrNotFound
			}
		}
		if input.CheckID > 0 {
			var checkAsset int64
			var status string
			if err := tx.QueryRow(ctx, `SELECT asset_id,status FROM plume.checks WHERE workspace_id=$1 AND id=$2`, workspaceID, input.CheckID).Scan(&checkAsset, &status); err != nil {
				return storeError(err)
			}
			if status != "draft" && status != "needs_work" || input.AssetID > 0 && input.AssetID != checkAsset {
				return ErrConflict
			}
			input.AssetID = checkAsset
			if input.Kind == "block_api" {
				var matches bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.runtime_findings WHERE workspace_id=$1 AND check_id=$2 AND service=$3 AND method=$4 AND route_key=$5)`, workspaceID, input.CheckID, spec.Service, spec.Method, runtimesecurity.RouteKey(spec.Route)).Scan(&matches); err != nil {
					return err
				}
				if !matches {
					return ErrInvalid
				}
			} else {
				var code string
				var scope OperationsScope
				if err := tx.QueryRow(ctx, `SELECT requirement_code FROM plume.checks WHERE workspace_id=$1 AND id=$2`, workspaceID, input.CheckID).Scan(&code); err != nil {
					return err
				}
				if code != "G03" {
					return ErrInvalid
				}
				if err := tx.QueryRow(ctx, `SELECT scope FROM plume.operations_sources WHERE workspace_id=$1 AND id=$2`, workspaceID, input.OperationsSourceID).Scan(&scope); err != nil {
					return err
				}
				var conflict bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.postgres_check_bindings WHERE check_id=$1 AND (job_id,source_host,source_port,source_database)<>($2,$3,$4,$5))`, input.CheckID, scope.JobID, scope.Host, scope.Port, scope.Database).Scan(&conflict); err != nil {
					return err
				}
				if conflict {
					return ErrConflict
				}
				if _, err := tx.Exec(ctx, `INSERT INTO plume.postgres_check_bindings(check_id,job_id,source_host,source_port,source_database) VALUES($1,$2,$3,$4,$5) ON CONFLICT(check_id) DO NOTHING`, input.CheckID, scope.JobID, scope.Host, scope.Port, scope.Database); err != nil {
					return err
				}
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO plume.control_actions(workspace_id,actor_id,kind,runtime_source_id,operations_source_id,asset_id,check_id,spec,reason,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,clock_timestamp()+make_interval(mins=>$10)) RETURNING id`, workspaceID, actorID, input.Kind, controlOptionalID(input.RuntimeSourceID), controlOptionalID(input.OperationsSourceID), controlOptionalID(input.AssetID), controlOptionalID(input.CheckID), spec, input.Reason, input.DurationMinutes).Scan(&id); err != nil {
			return err
		}
		return recordControlEvent(ctx, tx, workspaceID, id, actorID, "proposed", input.Reason, false)
	})
	if err != nil {
		return ControlAction{}, storeError(err)
	}
	return s.ControlAction(ctx, workspaceID, actorID, id)
}
func (s *Store) ApproveControlAction(ctx context.Context, workspaceID, actorID, id, revision int64) (ControlAction, error) {
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" && role != "reviewer" {
			return ErrForbidden
		}
		// Match receipt/revocation lock order: serialize the source before locking
		// an individual action, so two proposals cannot both approve one target.
		var kind string
		var runtimeSource, operationsSource *int64
		var spec ControlSpec
		if err := tx.QueryRow(ctx, `SELECT kind,runtime_source_id,operations_source_id,spec FROM plume.control_actions WHERE workspace_id=$1 AND id=$2`, workspaceID, id).Scan(&kind, &runtimeSource, &operationsSource, &spec); err != nil {
			return storeError(err)
		}
		var sourceID int64
		if kind == "block_api" && runtimeSource != nil {
			if err := tx.QueryRow(ctx, `SELECT id FROM plume.runtime_security_sources WHERE workspace_id=$1 AND id=$2 AND revoked_at IS NULL FOR NO KEY UPDATE`, workspaceID, *runtimeSource).Scan(&sourceID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrConflict
				}
				return err
			}
		} else if kind == "revoke_select" && operationsSource != nil {
			if err := tx.QueryRow(ctx, `SELECT id FROM plume.operations_sources WHERE workspace_id=$1 AND id=$2 AND revoked_at IS NULL FOR NO KEY UPDATE`, workspaceID, *operationsSource).Scan(&sourceID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrConflict
				}
				return err
			}
		} else {
			return ErrInvalid
		}
		var creator, stored int64
		var status string
		var checkID *int64
		var expiry time.Time
		if err := tx.QueryRow(ctx, `SELECT actor_id,revision,status,check_id,expires_at FROM plume.control_actions WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspaceID, id).Scan(&creator, &stored, &status, &checkID, &expiry); err != nil {
			return storeError(err)
		}
		if creator == actorID {
			return ErrForbidden
		}
		if stored != revision || status != "proposed" || !time.Now().Before(expiry) {
			return ErrConflict
		}
		if checkID != nil {
			var contributed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.check_evidence WHERE check_id=$1 AND created_by=$2)`, *checkID, actorID).Scan(&contributed); err != nil {
				return err
			}
			if contributed {
				return ErrForbidden
			}
		}
		var duplicate bool
		if kind == "block_api" {
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.control_actions WHERE workspace_id=$1 AND runtime_source_id=$2 AND id<>$3 AND status IN('approved','applied','rollback_requested') AND expires_at>clock_timestamp() AND spec->>'method'=$4 AND regexp_replace(spec->>'route','\{[^{}]+\}','{}','g')=$5)`, workspaceID, sourceID, id, spec.Method, runtimesecurity.RouteKey(spec.Route)).Scan(&duplicate); err != nil {
				return err
			}
		} else {
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.control_actions WHERE workspace_id=$1 AND operations_source_id=$2 AND id<>$3 AND (status IN('applied','rollback_requested') OR status='approved' AND expires_at>clock_timestamp()) AND (spec->>'host',spec->>'port',spec->>'database',spec->>'schema',spec->>'table',spec->>'role')=($4,$5,$6,$7,$8,$9))`, workspaceID, sourceID, id, spec.Host, fmt.Sprint(spec.Port), spec.Database, spec.Schema, spec.Table, spec.Role).Scan(&duplicate); err != nil {
				return err
			}
		}
		if duplicate {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE plume.control_actions SET approved_by=$3,status='approved',revision=revision+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, workspaceID, id, actorID); err != nil {
			return err
		}
		return recordControlEvent(ctx, tx, workspaceID, id, actorID, "approved", "批准限定目标、有效期及回滚范围", false)
	})
	if err != nil {
		return ControlAction{}, err
	}
	return s.ControlAction(ctx, workspaceID, actorID, id)
}
func (s *Store) RollbackControlAction(ctx context.Context, workspaceID, actorID, id, revision int64, reason string) (ControlAction, error) {
	if !validCheckText(reason, 400) {
		return ControlAction{}, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		var status string
		var stored int64
		if err := tx.QueryRow(ctx, "SELECT status,revision FROM plume.control_actions WHERE workspace_id=$1 AND id=$2 FOR UPDATE", workspaceID, id).Scan(&status, &stored); err != nil {
			return storeError(err)
		}
		if stored != revision || !slices.Contains([]string{"approved", "applied", "failed"}, status) {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE plume.control_actions SET status='rollback_requested',revision=revision+1,lease_id=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, workspaceID, id); err != nil {
			return err
		}
		return recordControlEvent(ctx, tx, workspaceID, id, actorID, "rollback_requested", reason, false)
	})
	if err != nil {
		return ControlAction{}, err
	}
	return s.ControlAction(ctx, workspaceID, actorID, id)
}

func runtimeControlActor(ctx context.Context, tx pgx.Tx, digest string) (sourceID, workspaceID, actorID int64, err error) {
	if !validRuntimeToken("gateway", digest) {
		err = ErrAutomationUnauthorized
		return
	}
	err = tx.QueryRow(ctx, `SELECT id,workspace_id,actor_id FROM plume.runtime_security_sources WHERE token_digest=$1 AND kind='gateway' AND revoked_at IS NULL FOR KEY SHARE`, digest).Scan(&sourceID, &workspaceID, &actorID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrAutomationUnauthorized
	}
	if err != nil {
		return
	}
	var role string
	role, err = checkRole(ctx, tx, workspaceID, actorID, false)
	if err != nil || role != "owner" {
		err = ErrAutomationUnauthorized
	}
	return
}
func (s *Store) RuntimeControlCommands(ctx context.Context, digest string) ([]ControlCommand, error) {
	result := []ControlCommand{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		sourceID, workspaceID, _, err := runtimeControlActor(ctx, tx, digest)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,revision,status,spec,expires_at FROM plume.control_actions WHERE workspace_id=$1 AND runtime_source_id=$2 AND status IN ('approved','applied','rollback_requested') ORDER BY id LIMIT 201`, workspaceID, sourceID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cmd ControlCommand
			if err := rows.Scan(&cmd.ID, &cmd.Revision, &cmd.Status, &cmd.Spec, &cmd.ExpiresAt); err != nil {
				rows.Close()
				return err
			}
			cmd.Phase = "apply"
			if cmd.Status == "rollback_requested" || !time.Now().Before(cmd.ExpiresAt) {
				cmd.Phase = "rollback"
			}
			result = append(result, cmd)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(result) > 200 {
			return ErrRuntimeLimit
		}
		return nil
	})
	return result, err
}

func (s *Store) RecordControlReceipt(ctx context.Context, kind, digest string, input ControlReceipt) error {
	if input.ActionID < 1 || input.Revision < 1 || !slices.Contains([]string{"apply", "rollback"}, input.Result.Phase) || !slices.Contains([]string{"route_blocked", "route_released", "select_revoked", "select_restored", "scope_changed", "database_unavailable", "permission_denied", "execution_failed", "expired", "configuration_mismatch"}, input.Result.Code) || input.Result.ObservedAt.IsZero() || input.Result.ObservedAt.Before(time.Now().Add(-24*time.Hour)) || input.Result.ObservedAt.After(time.Now().Add(5*time.Minute)) {
		return ErrInvalid
	}
	input.Result.ObservedAt = input.Result.ObservedAt.UTC().Truncate(time.Microsecond)
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	receiptDigest := hex.EncodeToString(sum[:])
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var sourceID, workspaceID, actorID int64
		var err error
		if kind == "gateway" {
			sourceID, workspaceID, actorID, err = runtimeControlActor(ctx, tx, digest)
		} else if kind == "postgres_control" {
			var source OperationsSource
			source, workspaceID, actorID, err = operationsSourceActor(ctx, tx, kind, digest)
			sourceID = source.ID
		} else {
			return ErrInvalid
		}
		if err != nil {
			return err
		}
		if kind == "postgres_control" {
			var scope OperationsScope
			if err := tx.QueryRow(ctx, `SELECT scope FROM plume.operations_sources WHERE id=$1 AND workspace_id=$2`, sourceID, workspaceID).Scan(&scope); err != nil {
				return err
			}
			current, err := currentOperationsScope(ctx, tx, workspaceID, scope, true)
			if err != nil {
				return err
			}
			if !current {
				return ErrConflict
			}
		}
		var actionKind, status string
		var storedRevision int64
		var runtimeSource, operationsSource, checkID *int64
		var storedReceipt *string
		var spec ControlSpec
		var expiry, created, updated time.Time
		var lease *string
		if err := tx.QueryRow(ctx, `SELECT kind,status,revision,runtime_source_id,operations_source_id,check_id,receipt_digest,spec,expires_at,created_at,updated_at,lease_id FROM plume.control_actions WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspaceID, input.ActionID).Scan(&actionKind, &status, &storedRevision, &runtimeSource, &operationsSource, &checkID, &storedReceipt, &spec, &expiry, &created, &updated, &lease); err != nil {
			return storeError(err)
		}
		if kind == "gateway" && (runtimeSource == nil || *runtimeSource != sourceID) || kind == "postgres_control" && (operationsSource == nil || *operationsSource != sourceID) {
			return ErrForbidden
		}
		if storedReceipt != nil && *storedReceipt == receiptDigest {
			return nil
		}
		if storedRevision != input.Revision || input.Result.ObservedAt.Before(created) {
			return ErrConflict
		}
		if input.Result.ObservedAt.Before(updated.Truncate(time.Microsecond)) {
			return ErrConflict
		}
		if kind == "postgres_control" && (lease == nil || input.Lease != *lease) {
			return ErrConflict
		}
		phase := input.Result.Phase
		if phase == "apply" && (status != "approved" || !input.Result.ObservedAt.Before(expiry)) || phase == "rollback" && (status != "rollback_requested" && time.Now().Before(expiry)) {
			return ErrConflict
		}
		if input.Result.Success {
			if actionKind == "block_api" && (phase == "apply" && input.Result.Code != "route_blocked" || phase == "rollback" && input.Result.Code != "route_released") {
				return ErrInvalid
			}
			if actionKind == "revoke_select" {
				if input.Result.HadSelect == nil || input.Result.HasSelect == nil {
					return ErrInvalid
				}
				if phase == "apply" && (*input.Result.HasSelect || !*input.Result.HadSelect || input.Result.Code != "select_revoked") || phase == "rollback" && (*input.Result.HasSelect != spec.ExpectedSelect || input.Result.Code != "select_restored") {
					return ErrInvalid
				}
			}
		}
		next := "failed"
		if input.Result.Success {
			next = "applied"
			if phase == "rollback" {
				next = "rolled_back"
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE plume.control_actions SET status=$3,result=$4,receipt_digest=$5,revision=revision+1,lease_id=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, workspaceID, input.ActionID, next, input.Result, receiptDigest); err != nil {
			return err
		}
		if err := recordControlEvent(ctx, tx, workspaceID, input.ActionID, actorID, next, input.Result.Code, true); err != nil {
			return err
		}
		if next == "applied" && checkID != nil {
			check, err := scanCheck(tx.QueryRow(ctx, checkSelect+" WHERE c.workspace_id=$1 AND c.id=$2 FOR NO KEY UPDATE OF c", workspaceID, *checkID))
			if err != nil {
				return err
			}
			if evidenceOpen(check) {
				_, err = addEvidenceTx(ctx, tx, check, actorID, EvidenceInput{Kind: "remediation", SourceSystem: "受控处置执行器", SourceRecord: fmt.Sprintf("control:%d:%d", input.ActionID, input.Revision), Summary: "审批后的限定处置已收到来源回执；后续仍需新鲜采集复测与独立复核。结果：" + input.Result.Code, ObservedAt: input.Result.ObservedAt}, true)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Store) ClaimPostgresControl(ctx context.Context, digest, leaseID string) (*ControlCommand, error) {
	if !operationsKey.MatchString(leaseID) {
		return nil, ErrInvalid
	}
	var cmd *ControlCommand
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		source, workspaceID, _, err := operationsSourceActor(ctx, tx, "postgres_control", digest)
		if err != nil {
			return err
		}
		current, err := currentOperationsScope(ctx, tx, workspaceID, source.Scope, true)
		if err != nil {
			return err
		}
		if !current {
			return ErrConflict
		}
		var next ControlCommand
		var result *ControlResult
		err = tx.QueryRow(ctx, `SELECT id,revision,status,spec,expires_at,result FROM plume.control_actions WHERE workspace_id=$1 AND operations_source_id=$2 AND (status='rollback_requested' OR (status='approved' AND expires_at>clock_timestamp())) AND (lease_until IS NULL OR lease_until<=clock_timestamp()) ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`, workspaceID, source.ID).Scan(&next.ID, &next.Revision, &next.Status, &next.Spec, &next.ExpiresAt, &result)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if next.Spec.Host != source.Scope.Host || next.Spec.Port != source.Scope.Port || next.Spec.Database != source.Scope.Database || !slices.Contains(source.Scope.Schemas, next.Spec.Schema) {
			return ErrConflict
		}
		next.Phase = "apply"
		if next.Status == "rollback_requested" {
			next.Phase = "rollback"
		}
		next.Lease = leaseID
		next.Original = result
		if _, err := tx.Exec(ctx, "UPDATE plume.control_actions SET lease_id=$3,lease_until=clock_timestamp()+interval '45 seconds' WHERE workspace_id=$1 AND id=$2", workspaceID, next.ID, leaseID); err != nil {
			return err
		}
		cmd = &next
		_, err = tx.Exec(ctx, "UPDATE plume.operations_sources SET last_seen=clock_timestamp() WHERE id=$1", source.ID)
		return err
	})
	return cmd, err
}
