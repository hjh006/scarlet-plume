package store

import (
	"context"
	"fmt"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
)

const operationsScheduleColumns = `id,title,period,timezone,scope,enabled,revision,next_run_at,last_report_id,created_by,updated_at`

func scanOperationsSchedule(row pgx.Row) (OperationsReportSchedule, error) {
	var s OperationsReportSchedule
	e := row.Scan(&s.ID, &s.Title, &s.Period, &s.Timezone, &s.Scope, &s.Enabled, &s.Revision, &s.NextRunAt, &s.LastReportID, &s.CreatedBy, &s.UpdatedAt)
	return s, e
}
func operationsPeriodStart(at time.Time, period string, loc *time.Location) time.Time {
	local := at.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	switch period {
	case "weekly":
		start = start.AddDate(0, 0, -(int(start.Weekday())+6)%7)
	case "monthly":
		start = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
	}
	return start
}
func nextOperationsPeriod(start time.Time, period string) time.Time {
	switch period {
	case "weekly":
		return start.AddDate(0, 0, 7)
	case "monthly":
		return start.AddDate(0, 1, 0)
	}
	return start.AddDate(0, 0, 1)
}
func previousOperationsPeriod(start time.Time, period string) time.Time {
	switch period {
	case "weekly":
		return start.AddDate(0, 0, -7)
	case "monthly":
		return start.AddDate(0, -1, 0)
	}
	return start.AddDate(0, 0, -1)
}
func validateOperationsSchedule(input *OperationsReportScheduleInput) error {
	if !validCheckText(input.Title, 120) || !classificationFilterValue(input.Period, "daily", "weekly", "monthly") || input.Revision < 0 {
		return ErrInvalid
	}
	if input.Timezone == "" {
		input.Timezone = "Asia/Shanghai"
	}
	if len(input.Timezone) > 80 {
		return ErrInvalid
	}
	if _, e := time.LoadLocation(input.Timezone); e != nil {
		return ErrInvalid
	}
	input.Scope.From = time.Time{}
	input.Scope.To = time.Time{}
	normalized, e := normalizeRuntimeEvidenceScope(input.Scope)
	if e != nil {
		return e
	}
	normalized.From = time.Time{}
	normalized.To = time.Time{}
	input.Scope = normalized
	return nil
}
func (s *Store) SaveOperationsReportSchedule(ctx context.Context, ws, actor, id int64, input OperationsReportScheduleInput) (OperationsReportSchedule, error) {
	if id < 0 || validateOperationsSchedule(&input) != nil {
		return OperationsReportSchedule{}, ErrInvalid
	}
	var result OperationsReportSchedule
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if role != "owner" {
			return ErrForbidden
		}
		if input.Scope.SourceID > 0 {
			var sourceID int64
			if e = tx.QueryRow(ctx, `SELECT id FROM plume.runtime_security_sources WHERE workspace_id=$1 AND id=$2 AND revoked_at IS NULL`, ws, input.Scope.SourceID).Scan(&sourceID); e != nil {
				return e
			}
		}
		loc, _ := time.LoadLocation(input.Timezone)
		next := nextOperationsPeriod(operationsPeriodStart(time.Now(), input.Period, loc), input.Period).UTC()
		if id == 0 {
			if input.Revision != 0 {
				return ErrInvalid
			}
			var count int
			if e = tx.QueryRow(ctx, `SELECT count(*) FROM plume.operations_report_schedules WHERE workspace_id=$1`, ws).Scan(&count); e != nil {
				return e
			}
			if count >= 50 {
				return ErrInvalid
			}
			result, e = scanOperationsSchedule(tx.QueryRow(ctx, `INSERT INTO plume.operations_report_schedules(workspace_id,title,period,timezone,scope,enabled,created_by,updated_by,next_run_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7,$8) RETURNING `+operationsScheduleColumns, ws, input.Title, input.Period, input.Timezone, input.Scope, input.Enabled, actor, next))
			return e
		}
		old, e := scanOperationsSchedule(tx.QueryRow(ctx, `SELECT `+operationsScheduleColumns+` FROM plume.operations_report_schedules WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id))
		if e != nil {
			return e
		}
		if old.Revision != input.Revision {
			return ErrConflict
		}
		result, e = scanOperationsSchedule(tx.QueryRow(ctx, `UPDATE plume.operations_report_schedules SET title=$3,period=$4,timezone=$5,scope=$6,enabled=$7,revision=revision+1,updated_by=$8,updated_at=clock_timestamp(),next_run_at=$9 WHERE workspace_id=$1 AND id=$2 RETURNING `+operationsScheduleColumns, ws, id, input.Title, input.Period, input.Timezone, input.Scope, input.Enabled, actor, next))
		return e
	})
	return result, storeError(e)
}
func (s *Store) ListOperationsReportSchedules(ctx context.Context, ws, actor int64) ([]OperationsReportSchedule, error) {
	items := []OperationsReportSchedule{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT `+operationsScheduleColumns+` FROM plume.operations_report_schedules WHERE workspace_id=$1 ORDER BY id DESC LIMIT 50`, ws)
		if e != nil {
			return e
		}
		items, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperationsReportSchedule, error) { return scanOperationsSchedule(row) })
		return e
	})
	return items, storeError(e)
}
func createScheduledOperationsReport(ctx context.Context, tx pgx.Tx, ws, actor int64, schedule OperationsReportSchedule, end time.Time) (OperationsInvestigationReport, error) {
	loc, e := time.LoadLocation(schedule.Timezone)
	if e != nil {
		return OperationsInvestigationReport{}, ErrInvalid
	}
	end = operationsPeriodStart(end, schedule.Period, loc)
	start := previousOperationsPeriod(end, schedule.Period)
	scope := schedule.Scope
	scope.From = start.UTC()
	scope.To = end.UTC().Add(-time.Microsecond)
	title := fmt.Sprintf("%s · %s—%s", schedule.Title, start.Format("2006-01-02"), end.Add(-time.Nanosecond).Format("2006-01-02"))
	if len([]rune(title)) > 120 {
		title = string([]rune(title)[:120])
	}
	report, e := createOperationsReportTx(ctx, tx, ws, actor, OperationsReportInput{Title: title, Scope: scope}, &schedule.ID, schedule.Period, schedule.Timezone)
	if e != nil {
		return report, e
	}
	_, e = tx.Exec(ctx, `UPDATE plume.operations_report_schedules SET last_report_id=$3,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, ws, schedule.ID, report.ID)
	return report, e
}
func (s *Store) RunOperationsReportSchedule(ctx context.Context, ws, actor, id int64) (OperationsInvestigationReport, error) {
	var out OperationsInvestigationReport
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		schedule, e := scanOperationsSchedule(tx.QueryRow(ctx, `SELECT `+operationsScheduleColumns+` FROM plume.operations_report_schedules WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id))
		if e != nil {
			return e
		}
		out, e = createScheduledOperationsReport(ctx, tx, ws, actor, schedule, time.Now())
		return e
	})
	return out, storeError(e)
}
func (s *Store) RunDueOperationsReportSchedules(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 10 {
		return 0, ErrInvalid
	}
	completed := 0
	for range limit {
		found := false
		e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var ws, id, actor int64
			e := tx.QueryRow(ctx, `SELECT s.workspace_id,s.id,s.created_by FROM plume.operations_report_schedules s JOIN plume.memberships m ON m.workspace_id=s.workspace_id AND m.user_id=s.created_by AND m.role='owner' WHERE s.enabled AND s.next_run_at<=clock_timestamp() ORDER BY s.next_run_at,s.id FOR UPDATE OF s SKIP LOCKED LIMIT 1`).Scan(&ws, &id, &actor)
			if e == pgx.ErrNoRows {
				return nil
			}
			if e != nil {
				return e
			}
			found = true
			schedule, e := scanOperationsSchedule(tx.QueryRow(ctx, `SELECT `+operationsScheduleColumns+` FROM plume.operations_report_schedules WHERE workspace_id=$1 AND id=$2`, ws, id))
			if e != nil {
				return e
			}
			_, e = createScheduledOperationsReport(ctx, tx, ws, actor, schedule, schedule.NextRunAt)
			if e != nil {
				return e
			}
			loc, _ := time.LoadLocation(schedule.Timezone)
			next := nextOperationsPeriod(operationsPeriodStart(schedule.NextRunAt, schedule.Period, loc), schedule.Period).UTC()
			_, e = tx.Exec(ctx, `UPDATE plume.operations_report_schedules SET next_run_at=$3 WHERE workspace_id=$1 AND id=$2`, ws, id, next)
			return e
		})
		if e != nil {
			return completed, storeError(e)
		}
		if !found {
			break
		}
		completed++
	}
	return completed, nil
}
