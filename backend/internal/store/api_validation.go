package store

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5"
)

var validationRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)

const apiValidationPlanSelect = `SELECT id,source_id,name,service,method,route,target_origin,manifest_ref,rules,interval_minutes,revision,state,created_by,approved_by,approved_at,next_run_at,lease_expires_at,updated_at FROM plume.api_validation_plans`

func scanAPIValidationPlan(row pgx.Row) (APIValidationPlan, error) {
	var p APIValidationPlan
	err := row.Scan(&p.ID, &p.SourceID, &p.Name, &p.Service, &p.Method, &p.Route, &p.TargetOrigin, &p.ManifestRef, &p.Rules, &p.IntervalMinutes, &p.Revision, &p.State, &p.CreatedBy, &p.ApprovedBy, &p.ApprovedAt, &p.NextRunAt, &p.LeaseExpiresAt, &p.UpdatedAt)
	return p, storeError(err)
}
func validAPIValidationInput(in *APIValidationPlanInput) bool {
	in.Name = strings.TrimSpace(in.Name)
	in.Service = strings.TrimSpace(in.Service)
	if in.IntervalMinutes == 0 {
		in.IntervalMinutes = 60
	}
	if len(in.TargetOrigin) > 300 || in.SourceID < 1 || !validCheckText(in.Name, 100) || !collector.ValidSource(in.Service) || in.Method != "GET" || !validationRef.MatchString(in.ManifestRef) || in.IntervalMinutes < 5 || in.IntervalMinutes > 43200 || len(in.Rules) < 1 || len(in.Rules) > 2 {
		return false
	}
	probe, err := runtimesecurity.NormalizeObservation(runtimesecurity.Observation{Method: in.Method, Route: in.Route, Status: 200, ObservedAt: time.Now()})
	if err != nil || probe.Route != in.Route {
		return false
	}
	parsed, err := url.Parse(in.TargetOrigin)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(in.TargetOrigin, "\r\n\x00") {
		return false
	}
	in.TargetOrigin = strings.ToLower(parsed.Scheme + "://" + parsed.Host)
	seen := map[string]bool{}
	for _, rule := range in.Rules {
		if !slices.Contains([]string{"bola", "weak_credential"}, rule) || seen[rule] {
			return false
		}
		seen[rule] = true
	}
	in.Rules = slices.Clone(in.Rules)
	slices.Sort(in.Rules)
	return true
}
func validationSourceVisible(ctx context.Context, tx pgx.Tx, workspaceID, sourceID int64) error {
	var id int64
	return storeError(tx.QueryRow(ctx, `SELECT s.id FROM plume.operations_sources s JOIN plume.memberships m ON m.workspace_id=s.workspace_id AND m.user_id=s.actor_id AND m.role='owner' WHERE s.workspace_id=$1 AND s.id=$2 AND s.kind='security_events' AND s.revoked_at IS NULL`, workspaceID, sourceID).Scan(&id))
}
func (s *Store) ListAPIValidationPlans(ctx context.Context, workspaceID, actorID int64) ([]APIValidationPlan, error) {
	items := []APIValidationPlan{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, false)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, apiValidationPlanSelect+` WHERE workspace_id=$1 ORDER BY id DESC LIMIT 100`, workspaceID)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (APIValidationPlan, error) { return scanAPIValidationPlan(row) })
		if err != nil {
			return err
		}
		editedRows, err := tx.Query(ctx, `SELECT id FROM plume.api_validation_plans WHERE workspace_id=$1 AND $2::bigint=ANY(editor_ids)`, workspaceID, actorID)
		if err != nil {
			return err
		}
		edited, err := pgx.CollectRows(editedRows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		for i := range items {
			items[i].CanApprove = (role == "owner" || role == "reviewer") && items[i].State == "draft" && items[i].CreatedBy != actorID && !slices.Contains(edited, items[i].ID)
		}
		return err
	})
	return items, storeError(err)
}
func (s *Store) SaveAPIValidationPlan(ctx context.Context, workspaceID, actorID, id int64, in APIValidationPlanInput) (APIValidationPlan, error) {
	if !validAPIValidationInput(&in) {
		return APIValidationPlan{}, ErrInvalid
	}
	var result APIValidationPlan
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		if err := validationSourceVisible(ctx, tx, workspaceID, in.SourceID); err != nil {
			return err
		}
		if id == 0 {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.api_validation_plans WHERE workspace_id=$1`, workspaceID).Scan(&count); err != nil {
				return err
			}
			if count >= 100 {
				return ErrInvalid
			}
			err = tx.QueryRow(ctx, `INSERT INTO plume.api_validation_plans(workspace_id,source_id,created_by,editor_ids,name,service,method,route,target_origin,manifest_ref,rules,interval_minutes)VALUES($1,$2,$3,ARRAY[$3::bigint],$4,$5,$6,$7,$8,$9,$10,$11)RETURNING id`, workspaceID, in.SourceID, actorID, in.Name, in.Service, in.Method, in.Route, in.TargetOrigin, in.ManifestRef, in.Rules, in.IntervalMinutes).Scan(&id)
			if err != nil {
				return err
			}
		} else {
			old, err := scanAPIValidationPlan(tx.QueryRow(ctx, apiValidationPlanSelect+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspaceID, id))
			if err != nil {
				return err
			}
			if old.Revision != in.Revision {
				return ErrConflict
			}
			a, b := old.APIValidationPlanInput, in
			a.Revision, b.Revision = 0, 0
			if assessmentDigest(a) == assessmentDigest(b) {
				result = old
				return nil
			}
			_, err = tx.Exec(ctx, `UPDATE plume.api_validation_plans SET source_id=$3,name=$4,service=$5,route=$6,target_origin=$7,manifest_ref=$8,rules=$9,interval_minutes=$10,revision=revision+1,state='draft',approved_by=NULL,approved_at=NULL,lease_ref=NULL,lease_expires_at=NULL,lease_revision=NULL,run_requested=false,editor_ids=CASE WHEN $11::bigint=ANY(editor_ids) THEN editor_ids ELSE array_append(editor_ids,$11::bigint) END,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, workspaceID, id, in.SourceID, in.Name, in.Service, in.Route, in.TargetOrigin, in.ManifestRef, in.Rules, in.IntervalMinutes, actorID)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE plume.api_validation_runs SET state='failed',error_code='plan_changed',completed_at=clock_timestamp() WHERE plan_id=$1 AND state='leased'`, id); err != nil {
				return err
			}
		}
		result, err = scanAPIValidationPlan(tx.QueryRow(ctx, apiValidationPlanSelect+` WHERE workspace_id=$1 AND id=$2`, workspaceID, id))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO plume.api_validation_events(plan_id,actor_id,action,revision)VALUES($1,$2,'saved',$3)`, id, actorID, result.Revision)
		return err
	})
	return result, storeError(err)
}
func (s *Store) ActAPIValidationPlan(ctx context.Context, workspaceID, actorID, id, revision int64, action, mode string) (APIValidationPlan, error) {
	if !slices.Contains([]string{"approve", "request", "disable"}, action) || action == "request" && !slices.Contains([]string{"validation", "connection"}, mode) {
		return APIValidationPlan{}, ErrInvalid
	}
	var result APIValidationPlan
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		p, err := scanAPIValidationPlan(tx.QueryRow(ctx, apiValidationPlanSelect+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspaceID, id))
		if err != nil {
			return err
		}
		if p.Revision != revision {
			return ErrConflict
		}
		if err := validationSourceVisible(ctx, tx, workspaceID, p.SourceID); err != nil {
			return err
		}
		switch action {
		case "approve":
			if role != "owner" && role != "reviewer" {
				return ErrForbidden
			}
			var edited bool
			if err := tx.QueryRow(ctx, `SELECT $2::bigint=ANY(editor_ids) FROM plume.api_validation_plans WHERE id=$1`, id, actorID).Scan(&edited); err != nil {
				return err
			}
			if p.CreatedBy == actorID || edited {
				return ErrForbidden
			}
			if p.State != "draft" {
				return ErrConflict
			}
			_, err = tx.Exec(ctx, `UPDATE plume.api_validation_plans SET state='approved',approved_by=$2,approved_at=clock_timestamp(),next_run_at=clock_timestamp(),updated_at=clock_timestamp()WHERE id=$1`, id, actorID)
		case "request":
			if !checkEditor(role) {
				return ErrForbidden
			}
			if p.State != "approved" {
				return ErrConflict
			}
			if p.LeaseExpiresAt != nil && p.LeaseExpiresAt.After(time.Now()) {
				return ErrConflict
			}
			_, err = tx.Exec(ctx, `UPDATE plume.api_validation_plans SET run_requested=true,request_mode=$2,next_run_at=clock_timestamp(),updated_at=clock_timestamp()WHERE id=$1`, id, mode)
		case "disable":
			if !checkEditor(role) {
				return ErrForbidden
			}
			_, err = tx.Exec(ctx, `UPDATE plume.api_validation_plans SET state='disabled',lease_ref=NULL,lease_expires_at=NULL,lease_revision=NULL,run_requested=false,updated_at=clock_timestamp()WHERE id=$1`, id)
		}
		if err != nil {
			return err
		}
		if action == "disable" {
			if _, err := tx.Exec(ctx, `UPDATE plume.api_validation_runs SET state='failed',error_code='plan_disabled',completed_at=clock_timestamp() WHERE plan_id=$1 AND state='leased'`, id); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO plume.api_validation_events(plan_id,actor_id,action,revision)VALUES($1,$2,$3,$4)`, id, actorID, action, p.Revision); err != nil {
			return err
		}
		result, err = scanAPIValidationPlan(tx.QueryRow(ctx, apiValidationPlanSelect+` WHERE id=$1`, id))
		return err
	})
	return result, storeError(err)
}
