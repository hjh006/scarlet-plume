package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var errScenarioRemediationBusy = errors.New("scenario remediation proposal busy")

// ProposeScenarioCheckRemediation only creates the existing controlled action
// request. Execution, independent approval, TTL and rollback remain unchanged.
func (s *Store) ProposeScenarioCheckRemediation(ctx context.Context, ws, actor, runID int64, findingID, reason string, durationMinutes int) (ControlAction, error) {
	reason = strings.TrimSpace(reason)
	if runID < 1 || !validCheckText(reason, 300) || durationMinutes < 1 || durationMinutes > 1440 || len(findingID) > 160 {
		return ControlAction{}, ErrInvalid
	}
	if findingID != "pg_public_select" {
		return s.proposeScenarioRuntimeRemediation(ctx, ws, actor, runID, findingID, reason, durationMinutes)
	}
	// ponytail: existing transaction-owning workflows need two pool connections;
	// use shared transaction helpers if single-connection support is required.
	if s.pool.Config().MaxConns < 2 {
		return ControlAction{}, ErrConflict
	}
	var actionID int64
	var e error
	for attempt := 0; ; attempt++ {
		e = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			role, e := checkRole(ctx, tx, ws, actor, false)
			if e != nil {
				return e
			}
			if !checkEditor(role) {
				return ErrForbidden
			}
			// This transaction holds only a per-finding advisory lock while invoking the
			// existing action workflow, so its independent transactions do not wait on
			// asset/check row locks owned by this wrapper.
			var locked bool
			if e = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("scenario-remediation:%d:%d:%s", ws, runID, findingID)).Scan(&locked); e != nil {
				return e
			}
			if !locked {
				return errScenarioRemediationBusy
			}
			e = tx.QueryRow(ctx, `SELECT action_id FROM plume.scenario_check_remediations WHERE workspace_id=$1 AND run_id=$2 AND finding_id=$3`, ws, runID, findingID).Scan(&actionID)
			if e == nil {
				return nil
			}
			if !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			run, e := readScenarioCheckRun(ctx, tx, ws, runID)
			if e != nil {
				return e
			}
			var frozen *ScenarioCheckFinding
			for i := range run.Findings {
				if run.Findings[i].ID == findingID {
					frozen = &run.Findings[i]
				}
			}
			if frozen == nil || frozen.Outcome != "non_compliant" || !frozen.Remediation.Executable || frozen.Remediation.Target == nil {
				return ErrEvidenceRequired
			}
			current, e := scenarioClassification(ctx, tx, ws, run.AssetID)
			if e != nil {
				return e
			}
			if current.Item.Revision != run.AssetRevision {
				return ErrConflict
			}
			if run.ActivityID > 0 {
				var revision int64
				if e = tx.QueryRow(ctx, `SELECT revision FROM plume.activities WHERE workspace_id=$1 AND id=$2 AND asset_id=$3`, ws, run.ActivityID, run.AssetID).Scan(&revision); e != nil {
					return e
				}
				if revision != run.ActivityRevision {
					return ErrConflict
				}
			}
			p, e := readScenarioPostgresSource(ctx, tx, ws, run.AssetID)
			if e != nil {
				return e
			}
			target, e := scenarioCurrentControlTarget(ctx, tx, ws, run.AssetID, p)
			if e != nil {
				return e
			}
			var frozenSource *scenarioPostgresSource
			for _, evidence := range run.Evidence {
				if evidence.ID == "postgres_inspection" {
					var stored scenarioPostgresSource
					if e = json.Unmarshal(evidence.Snapshot, &stored); e != nil {
						return e
					}
					frozenSource = &stored
					break
				}
			}
			if !scenarioSamePostgresTarget(frozenSource, p) {
				return ErrConflict
			}
			old := frozen.Remediation.Target
			if target == nil || target.Kind != old.Kind || target.AssetID != old.AssetID || target.OperationsSourceID != old.OperationsSourceID || target.Role != old.Role {
				return ErrConflict
			}
			prefix := fmt.Sprintf("场景检查#%d/%s：", runID, findingID)
			// Recover an already committed proposal if an interrupted response prevented
			// its run link from being committed. The advisory lock prevents duplicates.
			e = tx.QueryRow(ctx, `SELECT a.id FROM plume.control_actions a JOIN plume.checks c ON c.id=a.check_id AND c.workspace_id=a.workspace_id JOIN plume.postgres_check_bindings b ON b.check_id=c.id WHERE a.workspace_id=$1 AND a.asset_id=$2 AND a.kind=$3 AND a.operations_source_id=$4 AND left(a.reason,char_length($5))=$5 AND a.spec->>'role'=$6 AND a.spec->>'host'=$7 AND (a.spec->>'port')::integer=$8 AND a.spec->>'database'=$9 AND a.spec->>'schema'=$10 AND a.spec->>'table'=$11 AND (a.spec->>'expectedSelect')::boolean=true AND c.asset_id=a.asset_id AND c.requirement_code='G03' AND (b.job_id,b.source_host,b.source_port,b.source_database)=($12,$7,$8,$9) AND ($13::bigint=0 OR c.activity_id IS NULL OR c.activity_id=$13) ORDER BY a.id LIMIT 1`, ws, target.AssetID, target.Kind, target.OperationsSourceID, prefix, target.Role, p.Scope.Host, p.Scope.Port, p.Scope.Database, p.Inspection.Table.Schema, p.Inspection.Table.Name, p.Scope.JobID, run.ActivityID).Scan(&actionID)
			if errors.Is(e, pgx.ErrNoRows) {
				var checkID int64
				e = tx.QueryRow(ctx, `SELECT c.id FROM plume.checks c LEFT JOIN plume.postgres_check_bindings b ON b.check_id=c.id WHERE c.workspace_id=$1 AND c.asset_id=$2 AND c.requirement_code='G03' AND c.status IN ('draft','needs_work') AND (b.check_id IS NULL OR (b.job_id,b.source_host,b.source_port,b.source_database)=($3,$4,$5,$6)) AND ($7::bigint=0 OR c.activity_id IS NULL OR c.activity_id=$7) ORDER BY c.id DESC LIMIT 1`, ws, run.AssetID, p.Scope.JobID, p.Scope.Host, p.Scope.Port, p.Scope.Database, run.ActivityID).Scan(&checkID)
				if errors.Is(e, pgx.ErrNoRows) {
					var activityID *int64
					if run.ActivityID > 0 {
						activityID = &run.ActivityID
					}
					check, e := s.CreateCheck(ctx, ws, actor, CheckInput{AssetID: run.AssetID, ActivityID: activityID, RequirementCode: "G03", OwnerTeam: "技术检查", Note: prefix + "PUBLIC直接授权基线发现，处置申请、来源复采和独立核验分别留痕。"})
					if e != nil {
						return e
					}
					checkID = check.ID
				} else if e != nil {
					return e
				}
				if e = s.BindCheckPostgres(ctx, ws, actor, checkID, p.Scope.JobID); e != nil {
					return e
				}
				// Lock only after the existing binder has committed (it owns the
				// worker's source lock order). Hold the selected source/asset and
				// executor stable across the existing action-creation transaction.
				if _, e = tx.Exec(ctx, `SELECT j.id FROM plume.automation_jobs j JOIN plume.managed_postgres_jobs mp ON mp.job_id=j.id JOIN plume.postgres_asset_inspections i ON i.job_id=j.id AND i.asset_id=$3 WHERE j.workspace_id=$1 AND j.id=$2 FOR SHARE OF j,mp,i`, ws, p.Scope.JobID, run.AssetID); e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `SELECT id FROM plume.operations_sources WHERE workspace_id=$1 AND id=$2 FOR SHARE`, ws, target.OperationsSourceID); e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `SELECT id FROM plume.assets WHERE workspace_id=$1 AND id=$2 FOR SHARE`, ws, run.AssetID); e != nil {
					return e
				}
				latest, e := readScenarioPostgresSource(ctx, tx, ws, run.AssetID)
				if e != nil {
					return e
				}
				if !scenarioSamePostgresTarget(frozenSource, latest) {
					return ErrConflict
				}
				latestTarget, e := scenarioCurrentControlTarget(ctx, tx, ws, run.AssetID, latest)
				if e != nil {
					return e
				}
				if latestTarget == nil || latestTarget.OperationsSourceID != target.OperationsSourceID || latestTarget.Role != target.Role {
					return ErrConflict
				}
				var classificationRevision int64
				if e = tx.QueryRow(ctx, `SELECT classification_revision FROM plume.assets WHERE workspace_id=$1 AND id=$2`, ws, run.AssetID).Scan(&classificationRevision); e != nil {
					return e
				}
				if classificationRevision != run.AssetRevision {
					return ErrConflict
				}
				target.CheckID = checkID
				target.Reason = prefix + reason
				target.DurationMinutes = durationMinutes
				action, e := s.CreateControlAction(ctx, ws, actor, *target)
				if e != nil {
					return e
				}
				actionID = action.ID
			} else if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `INSERT INTO plume.scenario_check_remediations(workspace_id,run_id,finding_id,action_id) VALUES($1,$2,$3,$4)`, ws, runID, findingID, actionID)
			return e
		})
		if !errors.Is(e, errScenarioRemediationBusy) {
			break
		}
		if attempt >= 99 {
			return ControlAction{}, ErrConflict
		}
		// Release the outer connection before retrying so a small pool still has
		// capacity for the existing binder/action transactions of the lock owner.
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ControlAction{}, ctx.Err()
		case <-timer.C:
		}
	}
	if e != nil {
		return ControlAction{}, storeError(e)
	}
	return s.ControlAction(ctx, ws, actor, actionID)
}

func scenarioSamePostgresTarget(frozen, current *scenarioPostgresSource) bool {
	if frozen == nil || current == nil || !current.Current || frozen.SourceRevision == nil || current.SourceRevision == nil {
		return false
	}
	return *frozen.SourceRevision == *current.SourceRevision && frozen.CurrentRevision == current.CurrentRevision && frozen.Scope.JobID == current.Scope.JobID && frozen.Scope.Source == current.Scope.Source && frozen.Scope.Host == current.Scope.Host && frozen.Scope.Port == current.Scope.Port && frozen.Scope.Database == current.Scope.Database && slices.Equal(frozen.Scope.Schemas, current.Scope.Schemas) && frozen.Inspection.Table.Schema == current.Inspection.Table.Schema && frozen.Inspection.Table.Name == current.Inspection.Table.Name && frozen.Inspection.Table.SourceRef == current.Inspection.Table.SourceRef
}
