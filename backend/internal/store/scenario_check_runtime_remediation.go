package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

func (s *Store) proposeScenarioRuntimeRemediation(ctx context.Context, ws, actor, runID int64, findingID, reason string, duration int) (ControlAction, error) {
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
			if frozen == nil || frozen.Outcome != "non_compliant" || !frozen.Remediation.Executable || frozen.Remediation.Target == nil || frozen.Remediation.Target.Kind != "block_api" || frozen.CheckID == nil || run.Scope == nil || run.Scope.SubjectRef != "" || run.Scope.ClientIP != "" || run.Scope.ValueFingerprint != nil {
				return ErrEvidenceRequired
			}
			old := frozen.Remediation.Target
			if old.RuntimeSourceID < 1 || old.Method == "" || old.Route == "" {
				return ErrInvalid
			}
			// Source and latest rule versions must remain valid; evaluate actual recent
			// observations again, rather than acting on a historical positive alone.
			if _, e = tx.Exec(ctx, `SELECT id FROM plume.runtime_security_sources WHERE workspace_id=$1 AND id=$2 FOR SHARE`, ws, old.RuntimeSourceID); e != nil {
				return e
			}
			scope := *run.Scope
			now := time.Now().UTC()
			span := scope.To.Sub(scope.From)
			if span > time.Hour {
				span = time.Hour
			}
			scope.From = now.Add(-span)
			scope.To = now
			current := ScenarioCheckRun{Scope: &scope, Evidence: []ScenarioCheckEvidence{}, Findings: []ScenarioCheckFinding{}}
			if e = applyScenarioRuntimeEvidence(ctx, tx, ws, &current); e != nil {
				return e
			}
			var latest *ScenarioCheckFinding
			for i := range current.Findings {
				if current.Findings[i].ID == findingID {
					latest = &current.Findings[i]
				}
			}
			if latest == nil || latest.RuleVersion != frozen.RuleVersion || latest.Outcome != "non_compliant" || !latest.Remediation.Executable || latest.Remediation.Target == nil || latest.CheckID == nil || *latest.CheckID != *frozen.CheckID {
				return ErrConflict
			}
			target := latest.Remediation.Target
			if target.RuntimeSourceID != old.RuntimeSourceID || target.Method != old.Method || runtimeRoute(target.Route) != runtimeRoute(old.Route) {
				return ErrConflict
			}
			var checkAsset int64
			var service string
			e = tx.QueryRow(ctx, `SELECT c.asset_id,f.service FROM plume.runtime_findings f JOIN plume.checks c ON c.workspace_id=f.workspace_id AND c.id=f.check_id WHERE f.workspace_id=$1 AND f.check_id=$2 AND COALESCE((f.detection->>'sourceId')::bigint,f.source_id)=$3 AND f.method=$4 AND f.route_key=$5 AND c.status IN ('draft','needs_work') AND c.requirement_code='G03' ORDER BY f.id DESC LIMIT 1`, ws, *frozen.CheckID, target.RuntimeSourceID, target.Method, runtimeRoute(target.Route)).Scan(&checkAsset, &service)
			if e != nil {
				return e
			}
			prefix := fmt.Sprintf("场景检查#%d/%s：", runID, findingID)
			e = tx.QueryRow(ctx, `SELECT id FROM plume.control_actions WHERE workspace_id=$1 AND check_id=$2 AND asset_id=$3 AND kind='block_api' AND runtime_source_id=$4 AND spec->>'service'=$5 AND spec->>'method'=$6 AND spec->>'route'=$7 AND left(reason,char_length($8))=$8 ORDER BY id LIMIT 1`, ws, *frozen.CheckID, checkAsset, target.RuntimeSourceID, service, target.Method, target.Route, prefix).Scan(&actionID)
			if errors.Is(e, pgx.ErrNoRows) {
				target.AssetID = checkAsset
				target.CheckID = *frozen.CheckID
				target.Reason = prefix + reason
				target.DurationMinutes = duration
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
