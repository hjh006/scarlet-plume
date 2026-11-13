package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/jackc/pgx/v5"
)

func runtimeScopeForRun(ctx context.Context, tx pgx.Tx, ws int64, run *ScenarioCheckRun) (*RuntimeEvidenceScope, error) {
	if run.Scope != nil {
		s, e := normalizeRuntimeEvidenceScope(*run.Scope)
		return &s, e
	}
	if run.AssetID < 1 {
		return nil, nil
	}
	var provider, service, ref string
	e := tx.QueryRow(ctx, `SELECT COALESCE(source_provider,''),COALESCE(source,''),COALESCE(source_ref,'') FROM plume.assets WHERE workspace_id=$1 AND id=$2`, ws, run.AssetID).Scan(&provider, &service, &ref)
	if e != nil {
		return nil, e
	}
	if provider != "runtime-api" && provider != "openapi" {
		return nil, nil
	}
	method, route, ok := strings.Cut(ref, " ")
	if !ok {
		return nil, nil
	}
	now := time.Now().UTC()
	scope, e := normalizeRuntimeEvidenceScope(RuntimeEvidenceScope{Service: service, Method: method, Route: route, From: now.Add(-time.Hour), To: now})
	if e != nil {
		return nil, e
	}
	return &scope, nil
}
func runtimeNegativeProof(p RuntimeDetectionPolicy, d RuntimeDetection, events []runtimesecurity.Observation, window RuntimeEvidenceWindow, current, required bool) bool {
	if !current || window.Truncated || len(events) == 0 || d.Requests < p.MinRequests || p.CreatedAt != nil && p.CreatedAt.After(d.WindowStart) || d.WindowStart.Before(window.Scope.From) || window.Scope.To.After(time.Now()) || d.WindowEnd.Before(time.Now().Add(-time.Hour)) {
		return false
	}
	covered := 0
	for _, o := range events {
		if !runtimeObservationInScope(p, o) || o.ObservedAt.Before(d.WindowStart) || o.ObservedAt.After(d.WindowEnd) {
			continue
		}
		if runtimeRuleUsesSubject(p.Rule) && (d.SubjectRef == "" || o.SubjectRef != d.SubjectRef) {
			continue
		}
		if incompleteRuntimeCapture(o) {
			return false
		}
		switch p.Rule {
		case "sensitive_response", "credential_response":
			if !slices.Contains(o.InspectedLocations, "response") {
				return false
			}
		case "over_collection":
			if !slices.Contains(o.InspectedLocations, p.Direction) || !p.AllowlistConfigured {
				return false
			}
		case "credential_cleartext_transport":
			if !classificationFilterValue(o.Transport, "http", "https") || len(o.InspectedLocations) == 0 {
				return false
			}
			m, n, _, _ := extendedRuntimePolicySignal(p, o)
			if n == 0 || m > 0 {
				return false
			}
		case "missing_auth":
			if !classificationFilterValue(o.AuthState, "authenticated", "anonymous") || o.AuthState == "anonymous" && o.Status >= 200 && o.Status < 300 && o.AuthRequired == nil && !required {
				return false
			}
		case "sql_injection_signal", "xss_signal", "path_traversal_signal":
			if !slices.Contains(o.InspectedLocations, "request") {
				return false
			}
			seen := false
			for _, s := range o.SecuritySignals {
				if s.Kind == strings.TrimSuffix(p.Rule, "_signal") {
					seen = true
				}
			}
			if !seen {
				return false
			}
		case "subject_multi_ip":
			if o.SubjectRef == "" || o.ClientIP == "" {
				return false
			}
		case "subject_multi_region":
			if o.SubjectRef == "" || o.Context == nil || o.Context.ClientCountry == "" {
				return false
			}
		case "subject_bulk_access", "subject_repeated_access":
			if o.SubjectRef == "" {
				return false
			}
		case "client_region_mismatch", "recipient_scope", "destination_scope", "volume_limit":
			_, n, _, _ := extendedRuntimePolicySignal(p, o)
			if n == 0 {
				return false
			}
		}
		covered++
	}
	return covered == d.Requests && covered > 0
}
func runtimeCurrentBlockTarget(ctx context.Context, tx pgx.Tx, ws int64, sourceID int64, service, method, route string) (*ControlActionInput, error) {
	rows, e := tx.Query(ctx, `SELECT s.id FROM plume.runtime_security_sources s JOIN plume.runtime_security_routes r ON r.source_id=s.id WHERE s.workspace_id=$1 AND s.kind='gateway' AND s.service=$2 AND s.revoked_at IS NULL AND s.last_seen>now()-interval '1 day' AND r.method=$3 AND r.route_key=$4 AND r.last_seen>now()-interval '1 day' AND ($5::bigint=0 OR s.id=$5) AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.actor_id AND m.role='owner') ORDER BY s.id`, ws, service, method, runtimesecurity.RouteKey(route), sourceID)
	if e != nil {
		return nil, e
	}
	ids, e := pgx.CollectRows(rows, func(row pgx.CollectableRow) (int64, error) { var id int64; e := row.Scan(&id); return id, e })
	if e != nil {
		return nil, e
	}
	if len(ids) != 1 {
		return nil, nil
	}
	return &ControlActionInput{Kind: "block_api", RuntimeSourceID: ids[0], Method: method, Route: route}, nil
}
func applyScenarioRuntimeEvidence(ctx context.Context, tx pgx.Tx, ws int64, run *ScenarioCheckRun) error {
	scope, e := runtimeScopeForRun(ctx, tx, ws, run)
	if e != nil || scope == nil {
		return e
	}
	run.Scope = scope
	window, e := readRuntimeEvidenceWindow(ctx, tx, ws, *scope, 200)
	if e != nil {
		return e
	}
	var observedAt *time.Time
	var recordID int64
	for _, item := range window.Items {
		if observedAt == nil || item.Observation.ObservedAt.After(*observedAt) {
			at := item.Observation.ObservedAt
			observedAt = &at
			recordID = item.ID
		}
	}
	evidenceRef := scenarioEvidence(run, "runtime_window", "runtime", fmt.Sprintf("runtime-audit:%d/source:%d", recordID, scope.SourceID), fmt.Sprintf("来源%d/服务%s；%s至%s，台账匹配%d项/采用%d项。", scope.SourceID, scope.Service, scope.From.Format(time.RFC3339), scope.To.Format(time.RFC3339), window.Total, len(window.Items)), observedAt, window)
	basis, e := scenarioCheckBasis(ctx, tx, "G03")
	if e != nil {
		return e
	}
	if len(window.Items) == 0 {
		f := scenarioFinding("runtime_source_coverage", "运行时来源与可观察范围", "G03", "在所选来源/服务/用户/IP/时间范围内取得实际、完整且有效的访问观察。", "只检查所选范围，不由无日志推定没有访问。", basis)
		f.EvidenceRefs = []string{evidenceRef}
		f.MissingConditions = []string{"未取得该范围的保留访问观察；历史记录只有原监测投影时也不能补造身份、内容或IP。"}
		run.Findings = append(run.Findings, f)
		return appendScenarioAPIValidation(ctx, tx, ws, run, window, basis)
	}
	type group struct {
		source                 int64
		service, method, route string
		current                bool
		events                 []runtimesecurity.Observation
	}
	groups := map[string]*group{}
	order := []string{}
	for _, item := range window.Items {
		key := fmt.Sprintf("%d:%s:%s", item.SourceID, item.Observation.Method, runtimesecurity.RouteKey(item.Observation.Route))
		g := groups[key]
		if g == nil {
			g = &group{source: item.SourceID, service: item.Service, method: item.Observation.Method, route: item.Observation.Route, current: true}
			groups[key] = g
			order = append(order, key)
		}
		g.current = g.current && item.SourceCurrent
		g.events = append(g.events, item.Observation)
	}
	slices.Sort(order)
	if len(order) > 20 {
		order = order[:20]
		run.Warnings = append(run.Warnings, "运行时接口匹配后超过20项，未分析部分保留未知。")
		appendRuntimeBudgetGap(run, "interfaces", basis)
	}
	policies, e := readRuntimeDetectionPolicies(ctx, tx, ws)
	if e != nil {
		return e
	}
	for _, groupID := range order {
		g := groups[groupID]
		required, e := runtimeDetectionRequired(ctx, tx, runtimeFindingBatch{workspaceID: ws, sourceID: g.source, service: g.service})
		if e != nil && e != pgx.ErrNoRows {
			return e
		}
		for _, p := range policies {
			if !p.Enabled || p.SourceID > 0 && p.SourceID != g.source || p.Method != "" && p.Method != g.method || p.Route != "" && runtimesecurity.RouteKey(p.Route) != runtimesecurity.RouteKey(g.route) {
				continue
			}
			subjects := []string{""}
			if runtimeRuleUsesSubject(p.Rule) {
				subjects = nil
				for _, o := range g.events {
					if o.SubjectRef != "" && runtimeObservationInScope(p, o) {
						subjects = append(subjects, o.SubjectRef)
					}
				}
				slices.Sort(subjects)
				subjects = slices.Compact(subjects)
				if len(subjects) == 0 {
					subjects = []string{""}
				}
				if len(subjects) > 20 {
					subjects = subjects[:20]
					run.Warnings = append(run.Warnings, "主体超过20项分析预算，未分析主体仍待核实。")
					appendRuntimeBudgetGap(run, "subjects_"+p.Rule+"_"+p.ScopeKey, basis)
				}
			}
			for _, subject := range subjects {
				if len(run.Findings) >= 256 {
					run.Warnings = append(run.Warnings, "技术规则结果达到256项预算，其他范围未知。")
					appendRuntimeBudgetGap(run, "rules", basis)
					return nil
				}
				key := runtimeRouteKey{method: g.method, route: runtimesecurity.RouteKey(g.route)}
				d := evaluateRuntimeDetection(p, g.events, key, scope.To, subject, required[key])
				d.SourceID = g.source
				f := scenarioFinding("runtime_"+flowID(fmt.Sprint(g.source), g.method, key.route, p.Rule, p.ScopeKey, subject), runtimeDetectionTitle(p.Rule), "G03", "按本次采用的版本化技术策略判断所选窗口内已观察请求。", "仅所列来源、接口及观察窗口；客户地域/接收方标签不等于已证实跨境路径。", basis)
				f.RuleVersion = fmt.Sprintf("%s/%s/v%d/id%d", p.Rule, p.ScopeKey, p.Revision, p.ID)
				f.EvidenceRefs = []string{evidenceRef}
				f.Observed = fmt.Sprintf("来源%d，%s %s；策略窗口%s至%s；请求%d，命中%d，可观察分母%d；阈值%d，最少请求%d。", g.source, g.method, g.route, d.WindowStart.Format(time.RFC3339), d.WindowEnd.Format(time.RFC3339), d.Requests, d.Matched, d.Observed, p.Threshold, p.MinRequests)
				if p.Rule == "credential_cleartext_transport" {
					hops := []string{}
					for _, o := range g.events {
						if o.Transport != "" {
							hops = append(hops, o.Transport+"/"+o.TransportHop)
						}
					}
					slices.Sort(hops)
					hops = slices.Compact(hops)
					f.Observed += " 实际采用传输段：" + strings.Join(hops, ", ") + "；只判断该观察段，不推定端到端链路。"
				}
				if p.Rule == "volume_limit" {
					f.Observed += fmt.Sprintf(" 数据量单位%s，配置上限%d；未观察到的量不补零。", p.VolumeUnit, p.MaxVolume)
				}
				ref := scenarioEvidence(run, f.ID+"_policy", "runtime_policy", fmt.Sprintf("runtime-policy:%d/scope:%s/revision:%d", p.ID, p.ScopeKey, p.Revision), f.Coverage, observedAt, d)
				f.EvidenceRefs = append(f.EvidenceRefs, ref)
				switch {
				case !g.current:
					f.MissingConditions = append(f.MissingConditions, "来源停用、授权或最新接收状态无效。")
				case runtimeDetectionPositive(d):
					f.Outcome = "non_compliant"
					f.Remediation.Advice = "核对命中请求、配置允许范围和业务必要性；修正字段、TLS、授权或登记基线后取得新的同范围来源观察。"
				case runtimeNegativeProof(p, d, g.events, window, g.current, required[key]):
					f.Outcome = "compliant"
					f.Remediation = ScenarioCheckRemediation{Advice: "维持本次已观察范围内的配置；来源、业务或策略变化后重新采集并检查。", Reason: "完整检查的保留请求未触发本项技术策略，无需发起修复；不外推未观察流量。"}
				default:
					f.MissingConditions = append(f.MissingConditions, "可观察分母、完整捕获、策略生效窗口、主体/IP或客户声明标签不足，不能把无告警当作符合。")
				}
				f.Remediation.Executable = false
				if f.Outcome != "compliant" {
					f.Remediation.Reason = "本项尚无适用的精确处置目标；未知不自动修复。"
				}
				if f.Outcome == "non_compliant" && scope.SubjectRef == "" && scope.ClientIP == "" && scope.ValueFingerprint == nil && p.SubjectRef == "" && p.ClientIP == "" && subject == "" && classificationFilterValue(p.Rule, "missing_auth", "over_collection", "credential_cleartext_transport") {
					target, e := runtimeCurrentBlockTarget(ctx, tx, ws, g.source, g.service, g.method, g.route)
					if e != nil {
						return e
					}
					if target != nil {
						f.Remediation.Target = target
						f.Remediation.Kind = "block_api"
						f.Remediation.Executable = true
						f.Remediation.Reason = "可对该来源的整个接口发起临时阻断申请，影响所有访问者；仍需独立审批、TTL、回滚及新来源复查。"
					}
				}
				var checkID int64
				e = tx.QueryRow(ctx, `SELECT check_id FROM plume.runtime_findings WHERE workspace_id=$1 AND service=$2 AND method=$3 AND route_key=$4 AND rule=$5 AND scope_key=$6 AND COALESCE((detection->>'sourceId')::bigint,source_id)=$7 ORDER BY id DESC LIMIT 1`, ws, g.service, g.method, key.route, p.Rule, p.ScopeKey, g.source).Scan(&checkID)
				if e != nil && e != pgx.ErrNoRows {
					return e
				}
				if e == nil {
					f.CheckID = &checkID
				} else if f.Remediation.Executable {
					f.Remediation.Executable = false
					f.Remediation.Target = nil
					f.Remediation.Reason = "尚无可绑定的真实G03发现记录，请先接收对应来源发现，再发起受控修复。"
				}
				run.Findings = append(run.Findings, f)
			}
		}
	}
	if e = appendScenarioAPILifecycle(ctx, tx, ws, run, window, basis); e != nil {
		return e
	}
	return appendScenarioAPIValidation(ctx, tx, ws, run, window, basis)
}

func appendRuntimeBudgetGap(run *ScenarioCheckRun, key string, basis ScenarioCheckBasis) {
	f := scenarioFinding("runtime_budget_"+key, "未完成分析的观察范围", "G03", "逐项覆盖所选可观察范围。", "预算外记录未分析，不据此给出符合结论。", basis)
	f.EvidenceRefs = []string{"runtime_window"}
	f.MissingConditions = []string{"匹配范围超过有界分析预算，请缩小服务、接口、主体或时间范围后重新运行。"}
	run.Findings = append(run.Findings, f)
}
