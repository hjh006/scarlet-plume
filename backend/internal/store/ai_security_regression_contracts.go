package store

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

// These are derived engine contracts, never deployed configurations or source observations.
func aiDerivedRegression(cfg aisecurity.Config, r aisecurity.Request, unknown string, now time.Time, identity bool) []AISecurityEvaluationCase {
	out := []AISecurityEvaluationCase{}
	clone := func() aisecurity.Config {
		// ponytail: at most eight bounded catalog copies per evaluation; optimize only if measured.
		raw, _ := json.Marshal(cfg)
		var c aisecurity.Config
		_ = json.Unmarshal(raw, &c)
		c.Policy.BlockedInputPatterns = nil
		c.Policy.BlockedOutputPatterns = nil
		return c
	}
	check := func(name, code string, c aisecurity.Config, request aisecurity.Request) {
		d := aisecurity.Evaluate(c, request, now)
		out = append(out, AISecurityEvaluationCase{Name: name, Expected: "deny", Actual: d.Outcome, Code: d.Code, Passed: d.Outcome == "deny" && d.Code == code, CatalogScope: "derived_engine_contract"})
	}
	if identity {
		c := clone()
		for i := range c.Applications {
			if c.Applications[i].ID == r.ApplicationID {
				c.Applications[i].PrincipalIDs = nil
			}
		}
		check("已知应用撤销主体授权拒绝", "application_principal_not_allowed", c, r)
		c = clone()
		for i := range c.Principals {
			if c.Principals[i].ID == r.PrincipalID {
				c.Principals[i].ApplicationIDs = nil
			}
		}
		check("已知主体撤销应用授权拒绝", "application_principal_not_allowed", c, r)
	}
	switch r.Kind {
	case "rag":
		c := clone()
		for i := range c.Documents {
			if slices.Contains(r.DocumentIDs, c.Documents[i].ID) {
				c.Documents[i].AllowedPrincipalIDs = nil
			}
		}
		check("rag已知文档ACL撤销拒绝", "document_acl_denied", c, r)
	case "model":
		c := clone()
		changed := r
		var input map[string]any
		_ = json.Unmarshal([]byte(r.Input), &input)
		input["model"] = unknown
		raw, _ := json.Marshal(input)
		changed.Input = string(raw)
		check("model实际模型选择覆盖拒绝", "model_payload_target_or_protocol_invalid", c, changed)
		c = clone()
		for _, list := range []*[]string{&c.Policy.AllowedInputCategories, &c.Policy.AllowedOutputCategories} {
			if !slices.Contains(*list, "email") {
				*list = append(*list, "email")
			}
		}
		for i := range c.Models {
			if c.Models[i].ID == r.ModelID {
				c.Models[i].AllowedInputCategories = slices.DeleteFunc(c.Models[i].AllowedInputCategories, func(v string) bool { return v == "email" })
				if !slices.Contains(c.Models[i].AllowedOutputCategories, "email") {
					c.Models[i].AllowedOutputCategories = append(c.Models[i].AllowedOutputCategories, "email")
				}
			}
		}
		raw, _ = json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": "synthetic@example.com"}}})
		changed = r
		changed.Input = string(raw)
		check("model分类到固定目标限制拒绝", "model_input_category_denied", c, changed)
	case "tool":
		c := clone()
		for i := range c.Tools {
			if c.Tools[i].ID == r.ToolID {
				c.Tools[i].HighImpact = false
			}
		}
		changed := r
		changed.Action = unknown
		check("tool已知工具越权动作拒绝", "tool_action_or_target_denied", c, changed)
		changed = r
		changed.Target = unknown
		check("tool已知工具越权目标拒绝", "tool_action_or_target_denied", c, changed)
		changed = r
		changed.Arguments = map[string]string{}
		for k, v := range r.Arguments {
			changed.Arguments[k] = v
		}
		changed.Arguments[unknown] = "synthetic"
		check("tool已知工具越权参数拒绝", "tool_argument_denied", c, changed)
	}
	return out
}
