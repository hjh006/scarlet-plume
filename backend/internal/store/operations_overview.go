package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type OperationsOverview struct {
	GeneratedAt time.Time         `json:"generatedAt"`
	WindowDays  int               `json:"windowDays"`
	Scope       string            `json:"scope"`
	Counts      OperationsCounts  `json:"counts"`
	Teams       []OperationsTeam  `json:"teams"`
	Trend       []OperationsTrend `json:"trend"`
}
type OperationsCounts struct {
	Sources          int64 `json:"sources"`
	Events           int64 `json:"events"`
	OpenEvents       int64 `json:"openEvents"`
	Controls         int64 `json:"controls"`
	PendingApprovals int64 `json:"pendingApprovals"`
	AppliedControls  int64 `json:"appliedControls"`
	OverdueRisks     int64 `json:"overdueRisks"`
}
type OperationsTeam struct {
	Name       string `json:"name"`
	Assets     int64  `json:"assets"`
	Checks     int64  `json:"checks"`
	OpenChecks int64  `json:"openChecks"`
	Overdue    int64  `json:"overdue"`
}
type OperationsTrend struct {
	Day      string `json:"day"`
	Events   int64  `json:"events"`
	Verified int64  `json:"verified"`
}

func (s *Store) OperationsOverview(ctx context.Context, workspaceID, actorID int64) (OperationsOverview, error) {
	result := OperationsOverview{GeneratedAt: time.Now().UTC(), WindowDays: 30, Scope: "事件和趋势覆盖最近30天；来源为当前配置，处置与检查为当前状态。仅统计已纳管来源，事件数按来源记录，不跨来源推断同一事件。", Teams: []OperationsTeam{}, Trend: []OperationsTrend{}}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM plume.automation_jobs WHERE workspace_id=$1 AND revoked_at IS NULL)+(SELECT count(*) FROM plume.runtime_security_sources WHERE workspace_id=$1 AND revoked_at IS NULL)+(SELECT count(*) FROM plume.discovery_sources WHERE workspace_id=$1)+(SELECT count(*) FROM plume.operations_sources WHERE workspace_id=$1 AND revoked_at IS NULL)+(SELECT count(*) FROM plume.application_sources WHERE workspace_id=$1 AND revoked_at IS NULL),
 (SELECT count(*) FROM plume.operations_security_events WHERE workspace_id=$1 AND observed_at>=clock_timestamp()-interval '30 days'),
 (SELECT count(DISTINCT e.check_id) FROM plume.operations_security_events e JOIN plume.checks c ON c.id=e.check_id AND c.workspace_id=e.workspace_id WHERE e.workspace_id=$1 AND c.status<>'verified'),
 count(*),count(*) FILTER(WHERE status='proposed'),count(*) FILTER(WHERE status='applied'),
 (SELECT count(*) FROM (SELECT DISTINCT ON(finding_id) finding_id,due_date FROM plume.runtime_risk_triage WHERE workspace_id=$1 ORDER BY finding_id,revision DESC) t JOIN plume.runtime_findings f ON f.id=t.finding_id JOIN plume.checks c ON c.id=f.check_id WHERE t.due_date<CURRENT_DATE AND c.status<>'verified')
 FROM plume.control_actions WHERE workspace_id=$1`, workspaceID).Scan(&result.Counts.Sources, &result.Counts.Events, &result.Counts.OpenEvents, &result.Counts.Controls, &result.Counts.PendingApprovals, &result.Counts.AppliedControls, &result.Counts.OverdueRisks); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `WITH teams AS(SELECT COALESCE(NULLIF(owner_team,''),'未分配') name FROM plume.assets WHERE workspace_id=$1 UNION SELECT owner_team FROM plume.checks WHERE workspace_id=$1)
 SELECT teams.name,(SELECT count(*) FROM plume.assets WHERE workspace_id=$1 AND COALESCE(NULLIF(owner_team,''),'未分配')=teams.name),
 (SELECT count(*) FROM plume.checks WHERE workspace_id=$1 AND owner_team=teams.name),(SELECT count(*) FROM plume.checks WHERE workspace_id=$1 AND owner_team=teams.name AND status<>'verified'),
 (SELECT count(*) FROM(SELECT DISTINCT ON(finding_id) finding_id,owner_team,due_date FROM plume.runtime_risk_triage WHERE workspace_id=$1 ORDER BY finding_id,revision DESC)t JOIN plume.runtime_findings f ON f.id=t.finding_id JOIN plume.checks c ON c.id=f.check_id WHERE t.owner_team=teams.name AND t.due_date<CURRENT_DATE AND c.status<>'verified')
 FROM teams ORDER BY teams.name LIMIT 100`, workspaceID)
		if err != nil {
			return err
		}
		result.Teams, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperationsTeam, error) {
			var t OperationsTeam
			err := row.Scan(&t.Name, &t.Assets, &t.Checks, &t.OpenChecks, &t.Overdue)
			return t, err
		})
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `WITH days AS(SELECT generate_series(CURRENT_DATE-29,CURRENT_DATE,interval '1 day') AS bucket_day)
 SELECT to_char(bucket_day,'YYYY-MM-DD'),(SELECT count(*) FROM plume.operations_security_events WHERE workspace_id=$1 AND observed_at>=bucket_day AND observed_at<bucket_day+interval '1 day'),
 (SELECT count(*) FROM plume.check_events e JOIN plume.checks c ON c.id=e.check_id WHERE c.workspace_id=$1 AND e.action='verified' AND e.created_at>=bucket_day AND e.created_at<bucket_day+interval '1 day') FROM days ORDER BY bucket_day`, workspaceID)
		if err != nil {
			return err
		}
		result.Trend, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperationsTrend, error) {
			var t OperationsTrend
			err := row.Scan(&t.Day, &t.Events, &t.Verified)
			return t, err
		})
		return err
	})
	return result, err
}
