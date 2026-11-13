package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func createOperationsReportTx(ctx context.Context, tx pgx.Tx, ws, actor int64, input OperationsReportInput, scheduleID *int64, period, timezone string) (OperationsInvestigationReport, error) {
	out := OperationsInvestigationReport{Title: strings.TrimSpace(input.Title), CreatedBy: actor, Scope: input.Scope, Findings: []RuntimeFinding{}, Actions: []ControlAction{}, Evidence: []ScenarioCheckEvidence{}, Coverage: []string{}, MissingConditions: []string{}, Analysis: []ScenarioCheckFinding{}, ScheduleID: scheduleID, Period: period, Timezone: timezone}
	if !validCheckText(out.Title, 120) {
		return out, ErrInvalid
	}
	role, e := checkRole(ctx, tx, ws, actor, false)
	if e != nil {
		return out, e
	}
	if !checkEditor(role) {
		return out, ErrForbidden
	}
	out.Scope, e = normalizeRuntimeEvidenceScope(input.Scope)
	if e != nil {
		return out, e
	}
	out.Window, e = readRuntimeEvidenceWindow(ctx, tx, ws, out.Scope, 200)
	if e != nil {
		return out, e
	}
	if e = tx.QueryRow(ctx, `SELECT username,clock_timestamp() FROM plume.users WHERE id=$1`, actor).Scan(&out.Actor, &out.CreatedAt); e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT f.id,f.service,f.method,f.route,f.rule,f.source_id,f.source,f.baseline_scan_id,f.check_id,c.status,f.first_seen,f.last_positive_at,f.previous_check_id,f.retest_at,NULLIF(f.detection,'{}') FROM plume.runtime_findings f JOIN plume.checks c ON c.id=f.check_id AND c.workspace_id=f.workspace_id WHERE f.workspace_id=$1 AND ($2::bigint=0 OR COALESCE((f.detection->>'sourceId')::bigint,f.source_id)=$2) AND ($3='' OR f.service=$3) AND f.last_positive_at >= $4 AND f.first_seen <= $5 AND ($6='' OR f.method=$6) AND ($7='' OR f.route_key=$7) AND ($8='' OR f.detection->>'subjectRef'=$8) AND ($9='' OR f.detection#>>'{policy,clientIp}'=$9) ORDER BY f.last_positive_at DESC,f.id DESC LIMIT 201`, ws, out.Scope.SourceID, out.Scope.Service, out.Scope.From, out.Scope.To, out.Scope.Method, runtimeRoute(out.Scope.Route), out.Scope.SubjectRef, out.Scope.ClientIP)
	if e != nil {
		return out, e
	}
	out.Findings, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RuntimeFinding, error) {
		var f RuntimeFinding
		e := row.Scan(&f.ID, &f.Service, &f.Method, &f.Route, &f.Rule, &f.SourceID, &f.Source, &f.BaselineScanID, &f.CheckID, &f.CheckStatus, &f.FirstSeen, &f.LastSeen, &f.PreviousCheckID, &f.RetestAt, &f.Detection)
		return f, e
	})
	if e != nil {
		return out, e
	}
	if len(out.Findings) > 200 {
		out.Findings = out.Findings[:200]
		out.MissingConditions = append(out.MissingConditions, "风险记录匹配后超过200项，导出仅含节选。")
	}
	checkIDs := []int64{}
	for _, f := range out.Findings {
		checkIDs = append(checkIDs, f.CheckID)
	}
	rows, e = tx.Query(ctx, controlActionSelect+` WHERE a.workspace_id=$1 AND a.check_id=ANY($2) ORDER BY a.id DESC LIMIT 200`, ws, checkIDs)
	if e != nil {
		return out, e
	}
	out.Actions, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ControlAction, error) { return scanControlAction(row) })
	if e != nil {
		return out, e
	}
	scopeRun := ScenarioCheckRun{Scope: &out.Scope, CreatedAt: out.CreatedAt, CreatedBy: actor, Evidence: []ScenarioCheckEvidence{}, Findings: []ScenarioCheckFinding{}}
	if e = applyScenarioRuntimeEvidence(ctx, tx, ws, &scopeRun); e != nil {
		return out, e
	}
	out.Analysis = scopeRun.Findings
	out.Evidence = scopeRun.Evidence
	out.Coverage = append(out.Coverage, fmt.Sprintf("访问窗口%s至%s；仅保留台账内匹配记录%d项，本快照采用%d项。", out.Scope.From.Format(time.RFC3339), out.Scope.To.Format(time.RFC3339), out.Window.Total, len(out.Window.Items)), "主体标识按来源隔离；IP为来源报告的可观察连接地址，地域与接收方是客户显式标签，不通过公网IP猜测国家。", "风险记录按当前保留的发现周期及范围筛选，不表示已完整重建所有历史事件。执行回执与来源复测、独立复核分别记录。")
	if out.Window.Truncated {
		out.MissingConditions = append(out.MissingConditions, "匹配请求超过200项，窗口分析不完整；未命中不能作为符合结论。")
	}
	if out.Scope.From.Before(time.Now().Add(-RuntimeAuditRetentionDays * 24 * time.Hour)) {
		out.MissingConditions = append(out.MissingConditions, "所选周期超出当前访问明细保留期，缺失天数不按零请求补齐；请结合已保存的周期报告或外部受控归档。")
	}
	if len(out.Window.Items) == 0 {
		out.MissingConditions = append(out.MissingConditions, "没有该范围的当前保留访问观察；不等于没有访问或没有风险。")
	}
	out.MissingConditions = append(out.MissingConditions, "真实账号或员工身份映射、举报事实、业务授权与离岗结论需由组织核验；去标识主体或IP不能自动认定自然人。")
	if scheduleID != nil {
		var existing int64
		e = tx.QueryRow(ctx, `SELECT id FROM plume.operations_investigation_reports WHERE workspace_id=$1 AND schedule_id=$2 AND period_start=$3 AND period_end=$4`, ws, *scheduleID, out.Scope.From, out.Scope.To).Scan(&existing)
		if e == nil {
			return readOperationsReport(ctx, tx, ws, existing)
		}
		if e != pgx.ErrNoRows {
			return out, e
		}
	}
	if e = tx.QueryRow(ctx, `INSERT INTO plume.operations_investigation_reports(workspace_id,title,created_by,source_id,service,snapshot,snapshot_digest,schedule_id,period_start,period_end) VALUES($1,$2,$3,NULLIF($4,0),$5,'{}','',$6,$7,$8) RETURNING id`, ws, out.Title, actor, out.Scope.SourceID, out.Scope.Service, scheduleID, out.Scope.From, out.Scope.To).Scan(&out.ID); e != nil {
		return out, e
	}
	out.SnapshotDigest = OperationsReportSnapshotDigest(out)
	encoded, e := json.Marshal(out)
	if e != nil {
		return out, e
	}
	if len(encoded) > 8<<20 {
		return out, ErrInvalid
	}
	_, e = tx.Exec(ctx, `UPDATE plume.operations_investigation_reports SET snapshot=$3,snapshot_digest=$4 WHERE workspace_id=$1 AND id=$2`, ws, out.ID, encoded, out.SnapshotDigest)
	return out, e
}
func runtimeRoute(route string) string { return runtimesecurity.RouteKey(route) }

func OperationsReportSnapshotDigest(report OperationsInvestigationReport) string {
	report.SnapshotDigest = ""
	report.Evidence = append([]ScenarioCheckEvidence(nil), report.Evidence...)
	for i := range report.Evidence {
		report.Evidence[i].Snapshot = scenarioCanonicalJSON(report.Evidence[i].Snapshot)
	}
	return assessmentDigest(report)
}
func readOperationsReport(ctx context.Context, tx pgx.Tx, ws, id int64) (OperationsInvestigationReport, error) {
	var report OperationsInvestigationReport
	e := tx.QueryRow(ctx, `SELECT snapshot FROM plume.operations_investigation_reports WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&report)
	for i := range report.Evidence {
		report.Evidence[i].Snapshot = scenarioCanonicalJSON(report.Evidence[i].Snapshot)
	}
	return report, e
}
func (s *Store) CreateOperationsReport(ctx context.Context, ws, actor int64, input OperationsReportInput) (OperationsInvestigationReport, error) {
	var out OperationsInvestigationReport
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		var e error
		out, e = createOperationsReportTx(ctx, tx, ws, actor, input, nil, "", "")
		return e
	})
	return out, storeError(e)
}
func (s *Store) GetOperationsReport(ctx context.Context, ws, actor, id int64) (OperationsInvestigationReport, error) {
	var out OperationsInvestigationReport
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out, e = readOperationsReport(ctx, tx, ws, id)
		return e
	})
	return out, storeError(e)
}
func (s *Store) ListOperationsReports(ctx context.Context, ws, actor int64, scope RuntimeEvidenceScope) ([]OperationsInvestigationReport, error) {
	items := []OperationsInvestigationReport{}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		originalFrom, originalTo := scope.From, scope.To
		normalized, e := normalizeRuntimeEvidenceScope(scope)
		if e != nil {
			return e
		}
		scope.ClientIP = normalized.ClientIP
		var from, to any
		if !originalFrom.IsZero() {
			from = originalFrom
		}
		if !originalTo.IsZero() {
			to = originalTo
		}
		if scope.SourceID > 0 {
			var id int64
			if e = tx.QueryRow(ctx, `SELECT id FROM plume.runtime_security_sources WHERE workspace_id=$1 AND id=$2`, ws, scope.SourceID).Scan(&id); e != nil {
				return e
			}
		}
		rows, e := tx.Query(ctx, `SELECT snapshot FROM plume.operations_investigation_reports WHERE workspace_id=$1 AND ($2::bigint=0 OR source_id=$2) AND ($3='' OR service=$3) AND ($4='' OR snapshot#>>'{scope,method}'=$4) AND ($5='' OR snapshot#>>'{scope,route}'=$5) AND ($6='' OR snapshot#>>'{scope,subjectRef}'=$6) AND ($7='' OR snapshot#>>'{scope,clientIp}'=$7) AND ($8::timestamptz IS NULL OR period_end>=$8) AND ($9::timestamptz IS NULL OR period_start<=$9) ORDER BY id DESC LIMIT 50`, ws, scope.SourceID, scope.Service, scope.Method, scope.Route, scope.SubjectRef, scope.ClientIP, from, to)
		if e != nil {
			return e
		}
		items, e = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperationsInvestigationReport, error) {
			var r OperationsInvestigationReport
			e := row.Scan(&r)
			r.Window.Items = nil
			r.Findings = nil
			r.Actions = nil
			r.Evidence = nil
			r.Analysis = nil
			return r, e
		})
		return e
	})
	return items, storeError(e)
}
