package store

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

const AISecurityRegressionGate = "versioned-regression-v1"

type AISecurityRegressionCase struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Dimension       string   `json:"dimension"`
	RiskTags        []string `json:"riskTags"`
	Probe           string   `json:"probe"`
	ExpectedOutcome string   `json:"expectedOutcome"`
}
type AISecurityRegressionPack struct {
	PackID             string                     `json:"packId"`
	Version            string                     `json:"version"`
	Digest             string                     `json:"digest,omitempty"`
	RiskTags           []string                   `json:"riskTags"`
	RequiredDimensions []string                   `json:"requiredDimensions"`
	Cases              []AISecurityRegressionCase `json:"cases"`
}
type AISecurityRegressionPackSummary struct {
	ID                 int64     `json:"id"`
	PackID             string    `json:"packId"`
	Version            string    `json:"version"`
	Digest             string    `json:"digest"`
	RiskTags           []string  `json:"riskTags"`
	RequiredDimensions []string  `json:"requiredDimensions"`
	CaseCount          int       `json:"caseCount"`
	CreatedAt          time.Time `json:"createdAt"`
}
type AISecurityRegressionPreview struct {
	Pack               AISecurityRegressionPack `json:"pack"`
	CaseCount          int                      `json:"caseCount"`
	RequiredDimensions []string                 `json:"requiredDimensions"`
	CoveredDimensions  []string                 `json:"coveredDimensions"`
	CoverageGaps       []string                 `json:"coverageGaps"`
}
type aiRegressionProbe struct{ id, dimension, name string }

func aiRegressionProbes() []aiRegressionProbe {
	out := []aiRegressionProbe{{"identity.unknown_application", "identity", "未知应用默认拒绝"}, {"deny_all.disabled", "deny_all", "已停用应用拒绝"}}
	out = append(out, aiRegressionProbe{"identity.application_grant", "identity", "已知应用撤销主体授权拒绝"}, aiRegressionProbe{"identity.principal_grant", "identity", "已知主体撤销应用授权拒绝"}, aiRegressionProbe{"rag.acl", "rag", "rag已知文档ACL撤销拒绝"}, aiRegressionProbe{"model.selector", "model", "model实际模型选择覆盖拒绝"}, aiRegressionProbe{"model.categories", "model", "model分类到固定目标限制拒绝"}, aiRegressionProbe{"tool.actions", "tool", "tool已知工具越权动作拒绝"}, aiRegressionProbe{"tool.targets", "tool", "tool已知工具越权目标拒绝"}, aiRegressionProbe{"tool.arguments", "tool", "tool已知工具越权参数拒绝"})
	for _, kind := range []string{"rag", "model", "tool"} {
		for _, p := range []struct{ id, name string }{{"scope", "当前配置可用范围/审批门槛"}, {"identity", "未知主体拒绝"}, {"expiry", "过期请求拒绝"}, {"target", "未知目标拒绝"}} {
			dimension := kind
			if p.id == "identity" {
				dimension = "identity"
			}
			if p.id == "expiry" {
				dimension = "request_lifetime"
			}
			out = append(out, aiRegressionProbe{kind + "." + p.id, dimension, kind + p.name})
		}
	}
	return out
}
func AISecurityRegressionDimensions() []string {
	return []string{"identity", "request_lifetime", "rag", "model", "tool", "deny_all"}
}
func aiPackDigest(p AISecurityRegressionPack) string {
	p.Digest = ""
	raw, _ := json.Marshal(p)
	return assessmentDigest(scenarioCanonicalJSON(raw))
}
func aiRegressionTags(tags []string) bool {
	if len(tags) == 0 || len(tags) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, s := range tags {
		if !securityText(s, 100, true) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}

// Validation accepts registered synthetic recipes only; no request bodies or credentials are stored.
func ValidateAISecurityRegressionPack(p AISecurityRegressionPack, requireDigest bool) (AISecurityRegressionPreview, error) {
	out := AISecurityRegressionPreview{RequiredDimensions: AISecurityRegressionDimensions(), CoveredDimensions: []string{}, CoverageGaps: []string{}}
	if !securityText(p.PackID, 80, true) || !securityText(p.Version, 80, true) || !aiRegressionTags(p.RiskTags) || len(p.Cases) == 0 || len(p.Cases) > 128 || len(p.RequiredDimensions) > 6 {
		return out, ErrInvalid
	}
	registry := map[string]aiRegressionProbe{}
	for _, probe := range aiRegressionProbes() {
		registry[probe.id] = probe
	}
	ids := map[string]bool{}
	dimensions := map[string]bool{}
	for _, c := range p.Cases {
		probe, ok := registry[c.Probe]
		if !ok || !securityText(c.ID, 80, true) || ids[c.ID] || !securityText(c.Name, 160, true) || !aiRegressionTags(c.RiskTags) || c.Dimension != probe.dimension || !slices.Contains([]string{"configured", "allow", "deny"}, c.ExpectedOutcome) {
			return out, ErrInvalid
		}
		ids[c.ID] = true
		dimensions[probe.dimension] = true
	}
	declared := map[string]bool{}
	for _, d := range p.RequiredDimensions {
		if !slices.Contains(out.RequiredDimensions, d) || declared[d] {
			return out, ErrInvalid
		}
		declared[d] = true
	}
	raw, _ := json.Marshal(p)
	if len(raw) > 262144 {
		return out, ErrInvalid
	}
	actual := aiPackDigest(p)
	if requireDigest && p.Digest == "" || p.Digest != "" && p.Digest != actual {
		return out, ErrInvalid
	}
	p.Digest = actual
	out.Pack = p
	out.CaseCount = len(p.Cases)
	for _, d := range out.RequiredDimensions {
		if dimensions[d] {
			out.CoveredDimensions = append(out.CoveredDimensions, d)
		} else {
			out.CoverageGaps = append(out.CoverageGaps, d)
		}
	}
	return out, nil
}
func BaselineAISecurityRegressionPack() AISecurityRegressionPack {
	p := AISecurityRegressionPack{PackID: "scarlet-authorization-baseline", Version: "1.0.0", RiskTags: []string{"authorization", "upgrade-regression"}, RequiredDimensions: AISecurityRegressionDimensions(), Cases: []AISecurityRegressionCase{}}
	for _, probe := range aiRegressionProbes() {
		p.Cases = append(p.Cases, AISecurityRegressionCase{ID: probe.id, Name: probe.name, Dimension: probe.dimension, RiskTags: []string{"authorization"}, Probe: probe.id, ExpectedOutcome: "configured"})
	}
	p.Digest = aiPackDigest(p)
	return p
}
func aiPackSummary(id int64, p AISecurityRegressionPack, at time.Time) AISecurityRegressionPackSummary {
	return AISecurityRegressionPackSummary{ID: id, PackID: p.PackID, Version: p.Version, Digest: p.Digest, RiskTags: p.RiskTags, RequiredDimensions: p.RequiredDimensions, CaseCount: len(p.Cases), CreatedAt: at}
}

func aiEvaluateRegression(cfg aisecurity.Config, revision int64, p AISecurityRegressionPack, summary AISecurityRegressionPackSummary, now time.Time) AISecurityEvaluation {
	base := aiEvaluateConfiguration(cfg, revision, now)
	out := base
	out.Pack = &summary
	out.GateVersion = AISecurityRegressionGate
	out.EngineVersion = aisecurity.RuntimeProtocolVersion
	out.InspectionVersion = aisecurity.InspectionVersion
	out.Diagnostics = base.Cases
	out.Cases = []AISecurityEvaluationCase{}
	out.CoverageGaps = []string{}
	out.CoveredDimensions = []string{}
	out.RequiredDimensions = []string{"identity"}
	out.Scope = append(out.Scope, "注册合成探针与当前配置门禁；不调用真实上游，也不验证实际工具定义、模型响应或外部身份源。")
	if base.Status != "passed" {
		out.CoverageGaps = append(out.CoverageGaps, "current_configuration_diagnostics_failed")
	}
	required := map[string]bool{"identity.unknown_application": true}
	disabled := len(cfg.Applications) > 0
	for _, a := range cfg.Applications {
		if a.Enabled {
			disabled = false
		}
	}
	if disabled {
		required["deny_all.disabled"] = true
		out.RequiredDimensions = append(out.RequiredDimensions, "deny_all")
	} else {
		kinds := map[string]bool{}
		for _, d := range cfg.Documents {
			kinds["rag"] = kinds["rag"] || d.Enabled
		}
		for _, m := range cfg.Models {
			kinds["model"] = kinds["model"] || m.Enabled
		}
		for _, t := range cfg.Tools {
			kinds["tool"] = kinds["tool"] || t.Enabled
		}
		for _, kind := range []string{"rag", "model", "tool"} {
			if kinds[kind] {
				out.RequiredDimensions = append(out.RequiredDimensions, kind)
				for _, suffix := range []string{"scope", "identity", "expiry", "target"} {
					required[kind+"."+suffix] = true
				}
			}
		}
		if len(out.RequiredDimensions) > 1 {
			out.RequiredDimensions = append(out.RequiredDimensions, "request_lifetime")
			required["identity.application_grant"] = true
			required["identity.principal_grant"] = true
		}
		if kinds["rag"] {
			required["rag.acl"] = true
		}
		if kinds["model"] {
			required["model.selector"] = true
			required["model.categories"] = true
		}
		if kinds["tool"] {
			required["tool.actions"] = true
			required["tool.targets"] = true
			required["tool.arguments"] = true
		}
	}
	registry := map[string]aiRegressionProbe{}
	for _, v := range aiRegressionProbes() {
		registry[v.id] = v
	}
	covered := map[string]bool{}
	dimensions := map[string]bool{}
	for _, c := range p.Cases {
		def, _ := json.Marshal(c)
		r := AISecurityEvaluationCase{ID: c.ID, Name: c.Name, Probe: c.Probe, Dimension: c.Dimension, DefinitionDigest: assessmentDigest(scenarioCanonicalJSON(def)), Expected: c.ExpectedOutcome, Actual: "not_applicable", Status: "not_applicable", Code: "probe_not_applicable"}
		matches := []AISecurityEvaluationCase{}
		for _, item := range base.Cases {
			if item.Name == registry[c.Probe].name {
				matches = append(matches, item)
			}
		}
		if len(matches) > 0 {
			r.Passed = true
			r.Status = "passed"
			r.Actual = matches[0].Actual
			r.Code = matches[0].Code
			r.CatalogScope = matches[0].CatalogScope
			for _, m := range matches {
				if !m.Passed || c.ExpectedOutcome != "configured" && m.Actual != c.ExpectedOutcome {
					r.Passed = false
				}
			}
			if r.Passed {
				covered[c.Probe] = true
				dimensions[c.Dimension] = true
			} else {
				r.Status = "failed"
				out.Status = "failed"
			}
		} else if required[c.Probe] {
			r.Status = "failed"
			out.Status = "failed"
		}
		out.Cases = append(out.Cases, r)
	}
	for probe := range required {
		if !covered[probe] {
			out.CoverageGaps = append(out.CoverageGaps, "missing_or_failed_probe:"+probe)
		}
	}
	for _, d := range AISecurityRegressionDimensions() {
		if dimensions[d] {
			out.CoveredDimensions = append(out.CoveredDimensions, d)
		}
	}
	slices.Sort(out.CoverageGaps)
	if len(out.CoverageGaps) > 0 {
		out.Status = "failed"
	}
	out.GateEligible = out.Status == "passed" && len(out.CoverageGaps) == 0
	return out
}

type AISecurityRegressionComparison struct {
	From           AISecurityEvaluation     `json:"from"`
	To             AISecurityEvaluation     `json:"to"`
	AddedCaseIDs   []string                 `json:"addedCaseIds"`
	RemovedCaseIDs []string                 `json:"removedCaseIds"`
	ChangedCases   []aiRegressionCaseChange `json:"changedCases"`
	Regressions    []aiRegressionCaseChange `json:"regressions"`
	Scope          []string                 `json:"scope"`
}
type aiRegressionCaseChange struct {
	ID                string                   `json:"id"`
	Before            AISecurityEvaluationCase `json:"before"`
	After             AISecurityEvaluationCase `json:"after"`
	DefinitionChanged bool                     `json:"definitionChanged"`
}

func aiCompareRegression(from, to AISecurityEvaluation) (AISecurityRegressionComparison, error) {
	out := AISecurityRegressionComparison{From: from, To: to, AddedCaseIDs: []string{}, RemovedCaseIDs: []string{}, ChangedCases: []aiRegressionCaseChange{}, Regressions: []aiRegressionCaseChange{}, Scope: []string{"比较冻结元数据；定义变化与同定义结果退化分别列示，不推导真实上游检测效果。"}}
	if from.GateVersion != AISecurityRegressionGate || to.GateVersion != AISecurityRegressionGate {
		return out, ErrInvalid
	}
	before := map[string]AISecurityEvaluationCase{}
	after := map[string]AISecurityEvaluationCase{}
	for _, c := range from.Cases {
		before[c.ID] = c
	}
	for _, c := range to.Cases {
		after[c.ID] = c
	}
	for _, c := range to.Cases {
		old, ok := before[c.ID]
		if !ok {
			out.AddedCaseIDs = append(out.AddedCaseIDs, c.ID)
			continue
		}
		changed := old.DefinitionDigest != c.DefinitionDigest
		change := aiRegressionCaseChange{c.ID, old, c, changed}
		if changed || old.Actual != c.Actual || old.Passed != c.Passed || old.Status != c.Status || old.Code != c.Code {
			out.ChangedCases = append(out.ChangedCases, change)
		}
		if !changed && old.Passed && !c.Passed && c.Status == "failed" {
			out.Regressions = append(out.Regressions, change)
		}
	}
	for _, c := range from.Cases {
		if _, ok := after[c.ID]; !ok {
			out.RemovedCaseIDs = append(out.RemovedCaseIDs, c.ID)
		}
	}
	return out, nil
}
