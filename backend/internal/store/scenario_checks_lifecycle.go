package store

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

func appendScenarioAPILifecycle(ctx context.Context, tx pgx.Tx, ws int64, run *ScenarioCheckRun, window RuntimeEvidenceWindow, basis ScenarioCheckBasis) error {
	type route struct {
		source                int64
		service, method, path string
		current               bool
		requests, success     int
		latest                time.Time
	}
	groups := map[string]*route{}
	for _, item := range window.Items {
		o := item.Observation
		k := fmt.Sprintf("%d:%s:%s", item.SourceID, o.Method, runtimeRoute(o.Route))
		g := groups[k]
		if g == nil {
			g = &route{source: item.SourceID, service: item.Service, method: o.Method, path: o.Route, current: true}
			groups[k] = g
		}
		g.current = g.current && item.SourceCurrent
		g.requests++
		if o.Status >= 200 && o.Status < 300 {
			g.success++
		}
		if o.ObservedAt.After(g.latest) {
			g.latest = o.ObservedAt
		}
	}
	for _, g := range groups {
		if len(run.Findings) >= 256 {
			run.Warnings = append(run.Warnings, "接口生命周期结果超出分析预算，未分析范围待核实。")
			break
		}
		var scan APISecurityScan
		var authorized bool
		e := tx.QueryRow(ctx, `SELECT `+apiSecuritySummaryColumns+`,operations,EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=a.workspace_id AND m.user_id=a.actor_id AND m.role IN ('owner','operator')) FROM plume.api_security_scans a WHERE workspace_id=$1 AND source=$2 ORDER BY id DESC LIMIT 1`, ws, g.service).Scan(append(apiSecuritySummaryFields(&scan.APISecurityScanSummary), &scan.Operations, &authorized)...)
		if e != nil && e != pgx.ErrNoRows {
			return e
		}
		exists := e == nil
		registered, deprecated := false, false
		for _, op := range scan.Operations {
			if op.Method == g.method && runtimeRoute(op.Path) == runtimeRoute(g.path) {
				registered = true
				deprecated = op.Deprecated
			}
		}
		f := scenarioFinding("api_lifecycle_"+flowID(fmt.Sprint(g.source), g.method, runtimeRoute(g.path)), "活跃接口与登记生命周期一致性", "G03", "已观察接口应在采用的契约基线登记，弃用接口不继续成功提供服务。", "只判断该服务契约基线与保留访问记录，不证明全网接口资产完整或法律合规。", basis)
		f.RuleVersion = fmt.Sprintf("existing-runtime-lifecycle-v1/scan%d", scan.ID)
		f.Observed = fmt.Sprintf("来源%d，%s %s，观察%d次/成功%d次，契约登记=%t/弃用=%t。", g.source, g.method, g.path, g.requests, g.success, registered, deprecated)
		f.EvidenceRefs = []string{"runtime_window"}
		if exists {
			f.EvidenceRefs = append(f.EvidenceRefs, scenarioEvidence(run, f.ID+"_baseline", "openapi", fmt.Sprintf("api-security-scan:%d", scan.ID), f.Coverage, &scan.CreatedAt, scan))
		}
		rule := "unregistered_success"
		if registered && deprecated {
			rule = "deprecated_success"
		}
		if exists && authorized && g.current && g.latest.After(time.Now().Add(-time.Hour)) && scan.UnresolvedReferences == 0 && g.success > 0 && (!registered || deprecated) {
			f.Outcome = "non_compliant"
			f.Remediation.Advice = "核对已发布接口登记与弃用计划，补齐真实契约或按审批临时阻断；变更后采集新访问记录。"
		} else if exists && authorized && g.current && !window.Truncated && g.latest.After(time.Now().Add(-time.Hour)) && scan.UnresolvedReferences == 0 && registered && !deprecated && scan.CreatedAt.Before(window.Scope.From) {
			f.Outcome = "compliant"
			f.Remediation = ScenarioCheckRemediation{Advice: "维持接口登记与实际发布同步，变更后重新采集检查。", Reason: "所列已观察接口与当前采用的登记基线相容，无需修复；未外推未观察接口。"}
		} else {
			f.MissingConditions = []string{"缺少有效完整契约基线、当前来源或窗口覆盖；未成功请求不能证明弃用已完成。"}
		}
		if f.Outcome == "non_compliant" {
			var check int64
			e = tx.QueryRow(ctx, `SELECT check_id FROM plume.runtime_findings f JOIN plume.checks c ON c.id=f.check_id WHERE f.workspace_id=$1 AND f.service=$2 AND f.method=$3 AND f.route_key=$4 AND f.rule=$5 AND f.source_id=$6 AND c.status IN ('draft','needs_work') ORDER BY f.id DESC LIMIT 1`, ws, g.service, g.method, runtimeRoute(g.path), rule, g.source).Scan(&check)
			if e != nil && e != pgx.ErrNoRows {
				return e
			}
			if e == nil {
				f.CheckID = &check
				if run.Scope.SubjectRef == "" && run.Scope.ClientIP == "" && run.Scope.ValueFingerprint == nil {
					target, e := runtimeCurrentBlockTarget(ctx, tx, ws, g.source, g.service, g.method, g.path)
					if e != nil {
						return e
					}
					if target != nil {
						f.Remediation = ScenarioCheckRemediation{Advice: f.Remediation.Advice, Executable: true, Kind: "block_api", Target: target, Reason: "可申请临时阻断整个接口，独立审批后执行，保留TTL与回滚。"}
					}
				}
			}
		}
		run.Findings = append(run.Findings, f)
	}
	return nil
}
