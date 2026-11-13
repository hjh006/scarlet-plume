package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrAPIValidationProjection = errors.New("validation result saved; security event projection pending")

const apiValidationRunSelect = `SELECT id,plan_id,plan_revision,run_ref,plan_snapshot,state,result,event_ids,check_ids,error_code,projection_state,mode,created_at,completed_at FROM plume.api_validation_runs`

func scanAPIValidationRun(row pgx.Row) (APIValidationRun, error) {
	var run APIValidationRun
	err := row.Scan(&run.ID, &run.PlanID, &run.Revision, &run.RunRef, &run.Plan, &run.State, &run.Result, &run.EventIDs, &run.CheckIDs, &run.ErrorCode, &run.ProjectionState, &run.Mode, &run.CreatedAt, &run.CompletedAt)
	return run, storeError(err)
}
func validationApproverCurrent(ctx context.Context, tx pgx.Tx, workspaceID int64, p APIValidationPlan) error {
	if p.State != "approved" || p.ApprovedBy == nil {
		return ErrConflict
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.memberships WHERE workspace_id=$1 AND user_id=$2 AND role IN ('owner','reviewer'))`, workspaceID, *p.ApprovedBy).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
func (s *Store) ClaimAPIValidation(ctx context.Context, digest string, sourceID int64) (*APIValidationClaim, error) {
	var result *APIValidationClaim
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		source, ws, _, err := operationsSourceActor(ctx, tx, "security_events", digest)
		if err != nil {
			return err
		}
		if source.ID != sourceID {
			return ErrAutomationUnauthorized
		}
		var id int64
		err = tx.QueryRow(ctx, `SELECT p.id FROM plume.api_validation_plans p JOIN plume.memberships m ON m.workspace_id=p.workspace_id AND m.user_id=p.approved_by AND m.role IN ('owner','reviewer') WHERE p.workspace_id=$1 AND p.source_id=$2 AND p.state='approved' AND p.next_run_at<=clock_timestamp() AND (p.lease_expires_at IS NULL OR p.lease_expires_at<=clock_timestamp()) ORDER BY p.next_run_at,p.id FOR UPDATE OF p SKIP LOCKED LIMIT 1`, ws, source.ID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := scanAPIValidationPlan(tx.QueryRow(ctx, apiValidationPlanSelect+` WHERE id=$1`, id))
		if err != nil {
			return err
		}
		if err := validationApproverCurrent(ctx, tx, ws, p); err != nil {
			return err
		}
		var mode string
		if err := tx.QueryRow(ctx, `SELECT CASE WHEN run_requested THEN request_mode ELSE 'validation' END FROM plume.api_validation_plans WHERE id=$1`, id).Scan(&mode); err != nil {
			return err
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		ref := hex.EncodeToString(raw)
		until := time.Now().UTC().Add(2 * time.Minute)
		if _, err := tx.Exec(ctx, `UPDATE plume.api_validation_runs SET state='failed',error_code='lease_expired',completed_at=clock_timestamp()WHERE plan_id=$1 AND state='leased'`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plume.api_validation_runs(workspace_id,plan_id,source_id,plan_revision,run_ref,plan_snapshot,mode,state)VALUES($1,$2,$3,$4,$5,$6,$7,'leased')`, ws, id, source.ID, p.Revision, ref, p, mode); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE plume.api_validation_plans SET lease_ref=$2,lease_expires_at=$3,lease_revision=revision,run_requested=false,next_run_at=clock_timestamp()+make_interval(mins=>interval_minutes)WHERE id=$1`, id, ref, until)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM plume.api_validation_runs WHERE plan_id=$1 AND id IN(SELECT id FROM plume.api_validation_runs WHERE plan_id=$1 AND state<>'leased' ORDER BY id DESC OFFSET 200)`, p.ID); err != nil {
			return err
		}
		result = &APIValidationClaim{Plan: p, Mode: mode, RunRef: ref, LeaseExpiresAt: until}
		return nil
	})
	return result, storeError(err)
}
func validationRuleOutcome(r APIValidationRuleResult, mode string) string {
	if mode != "validation" || !r.ComparisonComplete || len(r.Gaps) > 0 {
		return "inconclusive"
	}
	owner := r.OwnerConfirmed && r.OwnerStatus >= 200 && r.OwnerStatus < 300
	if r.Rule == "bola" && owner && r.OtherAccountConfirmed && r.OtherOwnedStatus >= 200 && r.OtherOwnedStatus < 300 && r.Requests == 3 {
		if r.OtherStatus >= 200 && r.OtherStatus < 300 && r.SameObject {
			return "vulnerable"
		}
		if (r.OtherStatus == 403 || r.OtherStatus == 404) && !r.SameObject {
			return "not_observed"
		}
	}
	if r.PolicyConfirmed && r.PolicyDigest != "" && owner {
		switch r.Rule {
		case "auth_required":
			if r.Requests == 2 {
				if r.AnonymousStatus >= 200 && r.AnonymousStatus < 300 && r.AnonymousObjectConfirmed {
					return "vulnerable"
				}
				if (r.AnonymousStatus == 401 || r.AnonymousStatus == 403) && !r.AnonymousObjectConfirmed {
					return "not_observed"
				}
			}
		case "sensitive_exposure":
			if r.Requests == 2 && r.SensitiveChecked > 0 && r.OwnerSensitiveCount > 0 {
				if r.AnonymousStatus >= 200 && r.AnonymousStatus < 300 && r.AnonymousObjectConfirmed {
					if r.AnonymousSensitiveCount > 0 {
						return "vulnerable"
					}
					return "not_observed"
				}
				if (r.AnonymousStatus == 401 || r.AnonymousStatus == 403) && !r.AnonymousObjectConfirmed && r.AnonymousSensitiveCount == 0 {
					return "not_observed"
				}
			}
		case "response_headers":
			if r.Requests == 1 && r.HeaderChecked > 0 {
				if len(r.HeaderMismatches) > 0 {
					return "vulnerable"
				}
				return "not_observed"
			}
		}
	}
	if r.Rule == "weak_credential" && owner && r.CredentialAccepted && r.Requests == 1 {
		if r.WeakRuleMatched {
			return "vulnerable"
		}
		return "not_observed"
	}
	return "inconclusive"
}
func validateAPIValidationResult(p APIValidationPlan, in *APIValidationResult) bool {
	if in.PlanID != p.ID || in.Revision != p.Revision || !digestPattern.MatchString(in.RunRef) || !slices.Contains([]string{"validation", "connection"}, in.Mode) || len(in.Results) != len(p.Rules) {
		return false
	}
	in.ObservedAt = in.ObservedAt.UTC().Truncate(time.Microsecond)
	seen := map[string]bool{}
	for _, r := range in.Results {
		if !slices.Contains(p.Rules, r.Rule) || seen[r.Rule] || r.Requests < 0 || r.Requests > 4 || len(r.Gaps) > 8 || !slices.Contains([]string{"vulnerable", "not_observed", "inconclusive"}, r.Outcome) {
			return false
		}
		seen[r.Rule] = true
		for _, status := range []int{r.OwnerStatus, r.OtherStatus, r.OtherOwnedStatus} {
			if status != 0 && (status < 100 || status > 599) {
				return false
			}
		}
		for _, gap := range r.Gaps {
			if !slices.Contains([]string{"owner_unconfirmed", "other_account_unconfirmed", "comparison_missing", "cross_response_unexpected", "target_not_private", "manifest_mismatch", "credential_missing", "network_error", "invalid_json", "response_too_large", "connection_only", "probe_policy_mismatch", "anonymous_response_unexpected", "sensitive_control_missing", "field_shape_unknown"}, gap) {
				return false
			}
		}
		if !validExtendedValidationProof(p.ProbePolicy, r) || r.Outcome != validationRuleOutcome(r, in.Mode) || r.Outcome == "inconclusive" && len(r.Gaps) == 0 {
			return false
		}
	}
	return true
}
func (s *Store) SubmitAPIValidationResult(ctx context.Context, digest string, in APIValidationResult) (APIValidationRun, bool, error) {
	var run APIValidationRun
	replayed := false
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		source, ws, _, err := operationsSourceActor(ctx, tx, "security_events", digest)
		if err != nil {
			return err
		}
		run, err = scanAPIValidationRun(tx.QueryRow(ctx, apiValidationRunSelect+` WHERE workspace_id=$1 AND source_id=$2 AND run_ref=$3 FOR UPDATE`, ws, source.ID, in.RunRef))
		if err != nil {
			return err
		}
		if !validateAPIValidationResult(run.Plan, &in) {
			return ErrInvalid
		}
		resultDigest := assessmentDigest(in)
		if run.State == "complete" {
			var old string
			if err := tx.QueryRow(ctx, `SELECT result_digest FROM plume.api_validation_runs WHERE id=$1`, run.ID).Scan(&old); err != nil {
				return err
			}
			if old != resultDigest {
				return ErrConflict
			}
			replayed = true
			return nil
		}
		if run.State != "leased" || in.ObservedAt.Before(run.CreatedAt.Add(-5*time.Second)) || in.ObservedAt.Before(time.Now().Add(-5*time.Minute)) || in.ObservedAt.After(time.Now().Add(30*time.Second)) {
			return ErrConflict
		}
		p, err := scanAPIValidationPlan(tx.QueryRow(ctx, apiValidationPlanSelect+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, run.PlanID))
		if err != nil {
			return err
		}
		if p.Revision != in.Revision || p.State != "approved" || p.LeaseExpiresAt == nil || !p.LeaseExpiresAt.After(time.Now()) {
			return ErrConflict
		}
		if err := validationApproverCurrent(ctx, tx, ws, p); err != nil {
			return err
		}
		var lease string
		if err := tx.QueryRow(ctx, `SELECT lease_ref FROM plume.api_validation_plans WHERE id=$1`, p.ID).Scan(&lease); err != nil {
			return err
		}
		if lease != in.RunRef || in.Mode != run.Mode {
			return ErrConflict
		}

		if _, err := tx.Exec(ctx, `UPDATE plume.api_validation_runs SET result=$2,result_digest=$3,state='complete',completed_at=clock_timestamp()WHERE id=$1`, run.ID, in, resultDigest); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE plume.api_validation_plans SET lease_ref=NULL,lease_expires_at=NULL,lease_revision=NULL WHERE id=$1`, p.ID); err != nil {
			return err
		}
		run.Result = &in
		run.State = "complete"
		return nil
	})
	if err != nil {
		return run, replayed, storeError(err)
	}
	if err := s.projectAPIValidationRun(ctx, digest, &run); err != nil {
		return run, replayed, err
	}
	return run, replayed, nil
}
func validationEventRule(plan APIValidationPlan, rule string) string {
	return fmt.Sprintf("validated.plan%d.r%d.%s.v1", plan.ID, plan.Revision, rule)
}
func (s *Store) projectAPIValidationRun(ctx context.Context, digest string, run *APIValidationRun) error {
	if run.Result == nil {
		return ErrInvalid
	}
	ids, checks := []string{}, []int64{}
	for _, r := range run.Result.Results {
		if r.Outcome == "inconclusive" {
			continue
		}
		outcome, count := "observed", int64(1)
		if r.Outcome == "not_observed" {
			var exists bool
			err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.operations_security_events e JOIN plume.api_validation_runs r ON r.source_id=e.source_id WHERE r.id=$1 AND e.rule=$2 AND e.metadata->>'outcome'='observed')`, run.ID, validationEventRule(run.Plan, r.Rule)).Scan(&exists)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			outcome, count = "cleared", 0
		}
		id := "validation." + run.RunRef + "." + r.Rule
		event, _, err := s.ReceiveSecurityEvent(ctx, digest, SecurityEventInput{Outcome: outcome, EventID: id, ObservedAt: run.Result.ObservedAt, Category: "api", Rule: validationEventRule(run.Plan, r.Rule), Severity: "high", Service: run.Plan.Service, Method: run.Plan.Method, Route: run.Plan.Route, Count: count, WindowSeconds: 1})
		if err != nil {
			_, _ = s.pool.Exec(ctx, `UPDATE plume.api_validation_runs SET projection_state='error',error_code='projection_failed',event_ids=$2,check_ids=$3 WHERE id=$1`, run.ID, ids, checks)
			run.ProjectionState = "error"
			run.ErrorCode = "projection_failed"
			return ErrAPIValidationProjection
		}
		ids = append(ids, id)
		if !slices.Contains(checks, event.CheckID) {
			checks = append(checks, event.CheckID)
		}
	}
	_, err := s.pool.Exec(ctx, `UPDATE plume.api_validation_runs SET projection_state='complete',error_code='',event_ids=$2,check_ids=$3 WHERE id=$1`, run.ID, ids, checks)
	run.EventIDs, run.CheckIDs, run.ProjectionState = ids, checks, "complete"
	return err
}
func (s *Store) ListAPIValidationRuns(ctx context.Context, workspaceID, actorID, planID int64, limit int) ([]APIValidationRun, error) {
	if limit < 1 || limit > 1001 {
		return nil, ErrInvalid
	}
	items := []APIValidationRun{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		if _, err := scanAPIValidationPlan(tx.QueryRow(ctx, apiValidationPlanSelect+` WHERE workspace_id=$1 AND id=$2`, workspaceID, planID)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, apiValidationRunSelect+` WHERE workspace_id=$1 AND plan_id=$2 ORDER BY id DESC LIMIT $3`, workspaceID, planID, limit)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (APIValidationRun, error) { return scanAPIValidationRun(row) })
		return err
	})
	return items, storeError(err)
}

func (s *Store) RetryAPIValidationProjection(ctx context.Context, workspaceID, actorID, planID int64, runRef string) (APIValidationRun, error) {
	var run APIValidationRun
	var digest string
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, false)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		run, err = scanAPIValidationRun(tx.QueryRow(ctx, apiValidationRunSelect+` WHERE workspace_id=$1 AND plan_id=$2 AND run_ref=$3`, workspaceID, planID, runRef))
		if err != nil {
			return err
		}
		if run.State != "complete" || run.Result == nil {
			return ErrConflict
		}
		return tx.QueryRow(ctx, `SELECT s.token_digest FROM plume.operations_sources s JOIN plume.api_validation_runs r ON r.source_id=s.id AND r.workspace_id=s.workspace_id WHERE r.id=$1 AND s.workspace_id=$2 AND s.revoked_at IS NULL`, run.ID, workspaceID).Scan(&digest)
	})
	if err != nil {
		return run, storeError(err)
	}
	err = s.projectAPIValidationRun(ctx, digest, &run)
	return run, err
}
