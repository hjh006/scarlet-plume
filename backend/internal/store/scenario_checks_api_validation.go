package store

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

func appendScenarioAPIValidation(ctx context.Context, tx pgx.Tx, ws int64, run *ScenarioCheckRun, window RuntimeEvidenceWindow, basis ScenarioCheckBasis) error {
	if run.Scope == nil || run.Scope.Service == "" {
		return nil
	}
	rows, e := tx.Query(ctx, `SELECT DISTINCT ON(v.plan_id) v.id,v.plan_id,v.plan_revision,v.run_ref,v.plan_snapshot,v.state,v.result,v.event_ids,v.check_ids,v.error_code,v.projection_state,v.mode,v.created_at,v.completed_at,p.revision,p.state,COALESCE(s.revoked_at IS NULL AND EXISTS(SELECT 1 FROM plume.memberships ap WHERE ap.workspace_id=p.workspace_id AND ap.user_id=p.approved_by AND ap.role IN ('owner','reviewer')) AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.actor_id AND m.role='owner'),false) FROM plume.api_validation_runs v JOIN plume.api_validation_plans p ON p.id=v.plan_id AND p.workspace_id=v.workspace_id JOIN plume.operations_sources s ON s.id=p.source_id AND s.workspace_id=p.workspace_id WHERE v.workspace_id=$1 AND p.service=$2 AND ($3='' OR p.method=$3) AND ($4='' OR p.route=$4) ORDER BY v.plan_id,v.id DESC LIMIT 50`, ws, run.Scope.Service, run.Scope.Method, run.Scope.Route)
	if e != nil {
		return e
	}
	type sample struct {
		run      APIValidationRun
		revision int64
		state    string
		current  bool
	}
	samples, e := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sample, error) {
		var a sample
		r := &a.run
		e := row.Scan(&r.ID, &r.PlanID, &r.Revision, &r.RunRef, &r.Plan, &r.State, &r.Result, &r.EventIDs, &r.CheckIDs, &r.ErrorCode, &r.ProjectionState, &r.Mode, &r.CreatedAt, &r.CompletedAt, &a.revision, &a.state, &a.current)
		return a, e
	})
	if e != nil {
		return e
	}
	for _, s := range samples {
		v := s.run
		if v.Result == nil {
			continue
		}
		for _, result := range v.Result.Results {
			f := scenarioFinding(fmt.Sprintf("api_validation_%d_%s", v.PlanID, result.Rule), "受控API验证："+apiValidationRuleTitle(result.Rule), "G03", "仅在已批准目标、账户/对象对照与完整比较范围内判断该测试用例。", "受控验证结果不代表所有对象、账号或整个接口授权均符合。", basis)
			f.RuleVersion = fmt.Sprintf("api-validator-v1/plan%d/revision%d", v.PlanID, v.Revision)
			f.Observed = fmt.Sprintf("验证运行%d，模式%s，结果%s；请求%d，比较完整=%t。", v.ID, v.Mode, validationRuleOutcome(result, v.Mode), result.Requests, result.ComparisonComplete)
			if result.PolicyConfirmed {
				f.Observed += fmt.Sprintf(" 批准基线摘要%s；匿名HTTP%d/同对象=%t；字段检查%d、A格式命中%d、匿名命中%d；响应头检查%d、不符%s。", result.PolicyDigest, result.AnonymousStatus, result.AnonymousObjectConfirmed, result.SensitiveChecked, result.OwnerSensitiveCount, result.AnonymousSensitiveCount, result.HeaderChecked, strings.Join(result.HeaderMismatches, ","))
			}
			f.Expected = apiValidationExpected(result.Rule)
			f.EvidenceRefs = []string{scenarioEvidence(run, f.ID+"_evidence", "api_validation", fmt.Sprintf("api-validation:%d", v.ID), f.Coverage, &v.Result.ObservedAt, v)}
			current := run.Scope.SourceID == 0 && s.current && s.state == "approved" && v.Revision == s.revision && v.State == "complete" && v.ProjectionState == "complete" && v.Result.ObservedAt.After(run.Scope.From) && !v.Result.ObservedAt.After(run.Scope.To) && run.Scope.SubjectRef == "" && run.Scope.ClientIP == "" && v.Result.ObservedAt.After(time.Now().Add(-time.Duration(max(60, 3*v.Plan.IntervalMinutes))*time.Minute))
			if current {
				switch validationRuleOutcome(result, v.Mode) {
				case "vulnerable":
					f.Outcome = "non_compliant"
				case "not_observed":
					f.Outcome = "compliant"
					f.Remediation = ScenarioCheckRemediation{Advice: "维持本用例的已批准检测基线，变更后重新执行同范围受控验证。", Reason: "本次完整对照未发现该测试用例问题，无需发起修复；不能外推其他账号或对象。"}
				}
			}
			if f.Outcome == "needs_verification" {
				if len(result.Gaps) > 0 {
					f.MissingConditions = append(f.MissingConditions, "源端缺口："+strings.Join(result.Gaps, ", "))
				}
				f.MissingConditions = append(f.MissingConditions, "批准范围、来源绑定、状态、修订、时间、主体范围或比较证据不足；指定运行时来源时不借用另一个执行器的同名服务证据，连接测试不产生漏洞通过结论。")
			}
			if len(v.CheckIDs) > 0 {
				id := v.CheckIDs[0]
				f.CheckID = &id
			}
			if f.Outcome == "non_compliant" {
				f.Remediation.Advice = apiValidationRemediation(result.Rule)
				f.Remediation.Executable = false
				f.Remediation.Reason = "批准验证目标与网关上游尚无显式可信绑定，不能仅凭同名服务把任意代理作为修复目标。"
			}
			run.Findings = append(run.Findings, f)
		}
	}
	return nil
}

func apiValidationRuleTitle(rule string) string {
	return map[string]string{"bola": "对象级授权对照", "weak_credential": "已提供测试凭据", "auth_required": "明确认证要求与匿名访问对照", "sensitive_exposure": "指定敏感字段匿名暴露对照", "response_headers": "明确响应头基线"}[rule]
}
func apiValidationExpected(rule string) string {
	switch rule {
	case "auth_required":
		return "已确认A自有对象后，匿名访问同一已批准路径应明确401/403；普通200或其他对象不能替代证明。"
	case "sensitive_exposure":
		return "A对象有指定格式的正向对照；匿名应明确拒绝，或在同对象完整响应中不返回已批准禁止的敏感格式。"
	case "response_headers":
		return "已确认A对象的响应应满足独立批准的指定响应头及明确值；此基线不外推所有API。"
	case "weak_credential":
		return "所提供测试凭据成功认证后，不命中本地明确采用的弱凭据规则。"
	default:
		return "两个不同测试账号各自对象确认后，交叉请求不得取得相同的A对象。"
	}
}
func apiValidationRemediation(rule string) string {
	switch rule {
	case "auth_required":
		return "修复此路径的身份认证要求，确认A正向访问仍可用，再对匿名同目标重新验证并独立复核。"
	case "sensitive_exposure":
		return "修复此对象的敏感字段返回或匿名授权边界，保留A正向格式控制，再按相同字段基线复测。"
	case "response_headers":
		return "按已独立批准的响应头和值修复此响应，保留对象正向证明并取得相同基线的新运行。"
	default:
		return "修复对象级授权或测试凭据问题，重新取得同范围受控验证及独立复核。"
	}
}
