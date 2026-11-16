package store

import (
	"context"
	"encoding/json"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"slices"
	"strconv"
	"time"
)

// These tests execute the customer's current policy. They do not call a model or tool.
func aiEvaluateConfiguration(cfg aisecurity.Config, revision int64, now time.Time) AISecurityEvaluation {
	used := map[string]bool{}
	for _, v := range cfg.Applications {
		used[v.ID] = true
	}
	for _, v := range cfg.Principals {
		used[v.ID] = true
	}
	for _, v := range cfg.Documents {
		used[v.ID] = true
	}
	for _, v := range cfg.Models {
		used[v.ID] = true
	}
	for _, v := range cfg.Tools {
		used[v.ID] = true
		used[v.TargetArgument] = true
		for _, action := range v.Actions {
			used[action] = true
		}
		for key := range v.AllowedArguments {
			used[key] = true
		}
		for _, target := range v.Targets {
			used[target] = true
		}
	}
	unknownID := "unregistered-configuration-gate"
	for n := 1; used[unknownID]; n++ {
		unknownID = "unregistered-configuration-gate-" + strconv.Itoa(n)
	}
	out := AISecurityEvaluation{ConfigurationRevision: revision, CatalogDigest: aisecurity.ConfigDigest(cfg), Status: "passed", Scope: []string{"当前冻结配置的引擎契约检查；不调用真实模型、知识库或工具。", "每个已启用的资产族至少一个可构造正向/审批门槛用例；未知身份、目标和过期请求作否定用例。", "不证明全部客户业务、来源ACL完整性、供应链、真实执行或独立复测通过。"}, Cases: []AISecurityEvaluationCase{}}
	out.Scope = append(out.Scope, "任务策略采用显式合成可信上下文检查纯引擎契约，不创建真实任务、不占用步数或预算，也不证明客户节点任务准入成功。")
	add := func(name, expected string, r aisecurity.Request, approvalGate bool) {
		d := aisecurity.Evaluate(cfg, r, now)
		passed := d.Outcome == expected && (!approvalGate || d.RequiresApproval)
		out.Cases = append(out.Cases, AISecurityEvaluationCase{Name: name, Expected: expected, Actual: d.Outcome, Passed: passed, Code: d.Code})
		if !passed {
			out.Status = "failed"
		}
	}
	base := aisecurity.Request{RequestID: "configuration-gate", Kind: "rag", IssuedAt: now, ExpiresAt: now.Add(time.Duration(min(60, cfg.Policy.MaxRequestAgeSeconds)) * time.Second), ApplicationID: unknownID, PrincipalID: unknownID}
	add("未知应用默认拒绝", "deny", base, false)
	foundKinds := map[string]bool{}
	derivedIdentity := false
	required := map[string]bool{}
	attempts := 0
	for _, d := range cfg.Documents {
		required["rag"] = required["rag"] || d.Enabled
	}
	for _, m := range cfg.Models {
		required["model"] = required["model"] || m.Enabled
	}
	for _, tool := range cfg.Tools {
		required["tool"] = required["tool"] || tool.Enabled
	}
	for _, app := range cfg.Applications {
		if !app.Enabled {
			continue
		}
		for _, principal := range cfg.Principals {
			if !principal.Enabled || !slices.Contains(app.PrincipalIDs, principal.ID) || !slices.Contains(principal.ApplicationIDs, app.ID) {
				continue
			}
			r := base
			r.ApplicationID = app.ID
			r.PrincipalID = principal.ID
			for _, kind := range []string{"rag", "model", "tool"} {
				if foundKinds[kind] || !required[kind] {
					continue
				}
				r.Kind = kind
				r.DocumentIDs = nil
				r.ModelID = ""
				r.ToolID = ""
				r.Action = ""
				r.Target = ""
				r.Arguments = nil
				candidates := []aisecurity.Request{}
				if kind == "rag" {
					for _, id := range app.DocumentIDs {
						candidate := r
						permitted := false
						for _, doc := range cfg.Documents {
							if doc.ID == id && doc.Enabled && slices.Contains(doc.AllowedPrincipalIDs, principal.ID) {
								permitted = true
								break
							}
						}
						if !permitted {
							continue
						}
						candidate.DocumentIDs = []string{id}
						candidates = append(candidates, candidate)
					}
				}
				if kind == "model" {
					for _, id := range app.ModelIDs {
						candidate := r
						var model *aisecurity.Model
						for i := range cfg.Models {
							if cfg.Models[i].ID == id {
								model = &cfg.Models[i]
								break
							}
						}
						if model == nil || !model.Enabled || !slices.Contains(model.AllowedApplicationIDs, app.ID) {
							continue
						}
						candidate.ModelID = id
						upstream := model.UpstreamModel
						if upstream == "" {
							upstream = model.ID
						}
						body, _ := json.Marshal(map[string]any{"model": upstream, "messages": []map[string]string{{"role": "user", "content": "configuration gate synthetic"}}, "stream": false, "max_tokens": 16})
						candidate.Input = string(body)
						candidates = append(candidates, candidate)
					}
				}
				if kind == "tool" {
					for _, tool := range cfg.Tools {
						if !tool.Enabled || !slices.Contains(app.ToolIDs, tool.ID) || !slices.Contains(tool.AllowedPrincipalIDs, principal.ID) || !slices.Contains(tool.AllowedApplicationIDs, app.ID) || len(tool.Actions) == 0 || len(tool.Targets) == 0 {
							continue
						}
						candidate := r
						candidate.ToolID = tool.ID
						candidate.Action = tool.Actions[0]
						candidate.Target = tool.Targets[0]
						candidate.Arguments = map[string]string{}
						for _, key := range tool.RequiredArguments {
							if values := tool.AllowedArguments[key]; len(values) > 0 {
								candidate.Arguments[key] = values[0]
							}
						}
						candidates = append(candidates, candidate)
					}
				}
				for _, candidate := range candidates {
					candidate = aiEngineTaskCandidate(cfg, candidate, now)
					attempts++
					if attempts > 8192 {
						out.Scope = append(out.Scope, "配置候选评测达到8192次预算，范围未穷尽；门禁保持未通过。")
						out.Status = "failed"
						out.Cases = append(out.Cases, AISecurityEvaluationCase{Name: "评测预算", Expected: "完成所列范围", Actual: "预算截断", Passed: false, Code: "evaluation_budget_exhausted"})
						return out
					}
					d := aisecurity.Evaluate(cfg, candidate, now)
					gate := false
					if kind == "tool" {
						for _, t := range cfg.Tools {
							if t.ID == candidate.ToolID {
								gate = t.HighImpact
							}
						}
					}
					if d.Outcome != "allow" && !gate {
						continue
					}
					expected := "allow"
					if gate {
						expected = "deny"
					}
					add(kind+"当前配置可用范围/审批门槛", expected, candidate, gate)
					if candidate.TaskID != "" {
						unbound := candidate
						unbound.TaskID = "unbound-gate-task"
						add(kind+"任务引用不能替代可信上下文", "deny", unbound, false)
					}
					for _, contract := range aiDerivedRegression(cfg, candidate, unknownID, now, !derivedIdentity) {
						out.Cases = append(out.Cases, contract)
						if !contract.Passed {
							out.Status = "failed"
						}
					}
					derivedIdentity = true
					foundKinds[kind] = true
					unknown := candidate
					unknown.PrincipalID = unknownID
					add(kind+"未知主体拒绝", "deny", unknown, false)
					expired := candidate
					expired.IssuedAt = now.Add(-2 * time.Hour)
					expired.ExpiresAt = now.Add(-time.Hour)
					add(kind+"过期请求拒绝", "deny", expired, false)
					changed := candidate
					if kind == "rag" {
						changed.DocumentIDs = []string{unknownID}
					} else if kind == "model" {
						changed.ModelID = unknownID
					} else {
						changed.Target = unknownID
					}
					add(kind+"未知目标拒绝", "deny", changed, false)
					break
				}
			}
		}
	}
	count := 0
	for _, kind := range []string{"rag", "model", "tool"} {
		if required[kind] {
			count++
			if !foundKinds[kind] {
				out.Status = "failed"
				out.Cases = append(out.Cases, AISecurityEvaluationCase{Name: kind + "当前配置可用范围", Expected: "可构造正向或审批门槛用例", Actual: "未找到", Passed: false, Code: "no_usable_configured_scope"})
			}
		}
	}
	disabled := len(cfg.Applications) > 0
	for _, app := range cfg.Applications {
		if app.Enabled {
			disabled = false
		}
	}
	if disabled {
		out.Status = "passed"
		out.Scope = append(out.Scope, "明确停用模式：所有已登记应用均disabled，仅拒绝路径验证，不表示业务可用。")
		for _, app := range cfg.Applications {
			r := base
			r.ApplicationID = app.ID
			add("已停用应用拒绝", "deny", r, false)
		}
		return out
	}
	if count == 0 {
		out.Status = "failed"
		out.Cases = append(out.Cases, AISecurityEvaluationCase{Name: "客户配置范围", Expected: "至少一个已启用资产族与有效应用身份", Actual: "未配置", Passed: false, Code: "empty_catalog"})
	}
	return out
}
func (s *Store) RunAISecurityEvaluations(ctx context.Context, ws, actor int64) (AISecurityEvaluation, error) {
	return s.RunAISecurityRegression(ctx, ws, actor, 0)
}
