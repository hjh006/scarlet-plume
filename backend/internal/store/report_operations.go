package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type ReportOperations struct {
	CapturedAt     time.Time                 `json:"capturedAt"`
	Scope          string                    `json:"scope"`
	Controls       []ControlAction           `json:"controls"`
	SecurityEvents []OperationsSecurityEvent `json:"securityEvents"`
}

func readReportOperations(ctx context.Context, tx pgx.Tx, workspaceID int64, report *ComplianceReport) error {
	ids := make([]int64, 0, len(report.Checks))
	for _, entry := range report.Checks {
		ids = append(ids, entry.Check.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	result := ReportOperations{CapturedAt: report.GeneratedAt, Scope: "仅汇总本报告检查关联的处置及每项检查的最新安全信号。处置applied表示执行来源已回执，整体控制效果仍由列明的复测与独立复核判定。统计与回执随报告冻结。", Controls: []ControlAction{}, SecurityEvents: []OperationsSecurityEvent{}}
	rows, err := tx.Query(ctx, controlActionSelect+" WHERE a.workspace_id=$1 AND a.check_id=ANY($2) ORDER BY a.id DESC LIMIT 201", workspaceID, ids)
	if err != nil {
		return err
	}
	result.Controls, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ControlAction, error) { return scanControlAction(row) })
	if err != nil {
		return err
	}
	if len(result.Controls) > 200 {
		return ErrReportScopeTooLarge
	}
	controlIDs := make([]int64, 0, len(result.Controls))
	positions := map[int64]int{}
	for i := range result.Controls {
		controlIDs = append(controlIDs, result.Controls[i].ID)
		positions[result.Controls[i].ID] = i
		result.Controls[i].Events = []ControlEvent{}
	}
	remaining := ReportTimelineLimit
	for _, entry := range report.Checks {
		remaining -= len(entry.Events) + len(entry.Evidence)
	}
	if remaining < 0 {
		return ErrReportScopeTooLarge
	}
	if len(controlIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT e.action_id,e.action,u.username,e.note,e.automated,e.created_at FROM plume.control_action_events e JOIN plume.users u ON u.id=e.actor_id WHERE e.workspace_id=$1 AND e.action_id=ANY($2) ORDER BY e.id LIMIT $3`, workspaceID, controlIDs, remaining+1)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var id int64
			var event ControlEvent
			if err := rows.Scan(&id, &event.Action, &event.Actor, &event.Note, &event.Automated, &event.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			count++
			if count > remaining {
				rows.Close()
				return ErrReportScopeTooLarge
			}
			index := positions[id]
			result.Controls[index].Events = append(result.Controls[index].Events, event)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT ON(e.check_id) e.id,e.source_id,s.name,e.check_id,c.status,e.asset_id,e.metadata FROM plume.operations_security_events e JOIN plume.operations_sources s ON s.id=e.source_id AND s.workspace_id=e.workspace_id JOIN plume.checks c ON c.id=e.check_id AND c.workspace_id=e.workspace_id WHERE e.workspace_id=$1 AND e.check_id=ANY($2) ORDER BY e.check_id,e.observed_at DESC,(e.metadata->>'outcome'='observed') DESC,e.id DESC`, workspaceID, ids)
	if err != nil {
		return err
	}
	result.SecurityEvents, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperationsSecurityEvent, error) {
		var e OperationsSecurityEvent
		var assetID int64
		err := row.Scan(&e.ID, &e.SourceID, &e.Source, &e.CheckID, &e.CheckStatus, &assetID, &e.SecurityEventInput)
		e.AssetID = assetID
		return e, err
	})
	if err != nil {
		return err
	}
	if len(result.Controls) > 0 || len(result.SecurityEvents) > 0 {
		report.Operations = &result
	}
	return nil
}
