package aisecurity

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func fixture() Config {
	c := DefaultConfig()
	c.Applications = []Application{{ID: "app", Name: "Support", Enabled: true, PrincipalIDs: []string{"alice", "bob"}, DocumentIDs: []string{"doc"}, ModelIDs: []string{"enterprise", "public"}, ToolIDs: []string{"ticket"}}}
	c.Principals = []Principal{{ID: "alice", Enabled: true, ApplicationIDs: []string{"app"}}, {ID: "bob", Enabled: true, ApplicationIDs: []string{"app"}}}
	c.Documents = []Document{{ID: "doc", Version: "v1", ACLVersion: "acl1", KnowledgeBaseID: "kb", Enabled: true, AllowedPrincipalIDs: []string{"alice"}, Categories: []string{"confidential"}}}
	c.Models = []Model{{ID: "enterprise", Enabled: true, EndpointRef: "model", Region: "CN", AllowedApplicationIDs: []string{"app"}, AllowedInputCategories: []string{"confidential"}, AllowedOutputCategories: []string{"confidential"}}, {ID: "public", Enabled: true, EndpointRef: "othermodel", Region: "CN", AllowedApplicationIDs: []string{"app"}, AllowedInputCategories: []string{}, AllowedOutputCategories: []string{}}}
	c.Tools = []Tool{{ID: "ticket", Enabled: true, EndpointRef: "tools", AllowedApplicationIDs: []string{"app"}, AllowedPrincipalIDs: []string{"alice"}, Actions: []string{"close"}, Targets: []string{"ticket-17"}, AllowedArguments: map[string][]string{"resolution": {"fixed", "duplicate"}}, RequiredArguments: []string{"resolution"}, HighImpact: true}}
	c.Policy.AllowedModelRegions = []string{"CN"}
	c.Policy.AllowedInputCategories = []string{"confidential"}
	c.Policy.AllowedOutputCategories = []string{"confidential"}
	return c
}
func testRequest(kind string) Request {
	now := time.Now().UTC()
	return Request{RequestID: "request-1234567890", Kind: kind, ApplicationID: "app", PrincipalID: "alice", IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute)}
}
func clone(c Config) Config {
	raw, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(raw, &out)
	return out
}
func approve(c *Config, r *Request) {
	r.ApprovalID = "approval-1"
	c.Approvals = []Approval{{ID: r.ApprovalID, ApplicationID: r.ApplicationID, PrincipalID: r.PrincipalID, ToolID: r.ToolID, Action: r.Action, Target: r.Target, ApprovedBy: "reviewer", RequestDigest: RequestDigest(*r), CatalogDigest: ConfigDigest(*c), ExpiresAt: r.ExpiresAt}}
}
func TestRAGTrustedACLAndRevocation(t *testing.T) {
	c := fixture()
	r := testRequest("rag")
	r.DocumentIDs = []string{"doc"}
	if e := ValidateConfig(c); e != nil {
		t.Fatal(e)
	}
	if d := Evaluate(c, r, time.Now()); d.Outcome != "allow" || !slices.Equal(d.AllowedDocumentIDs, []string{"doc"}) {
		t.Fatal(d)
	}
	for _, variant := range []string{"cross_subject", "revocation", "unknown_doc", "disabled_app", "expired"} {
		t.Run(variant, func(t *testing.T) {
			cc := clone(c)
			rr := r
			switch variant {
			case "cross_subject":
				rr.PrincipalID = "bob"
			case "revocation":
				cc.Documents[0].AllowedPrincipalIDs = []string{}
			case "unknown_doc":
				rr.DocumentIDs = []string{"absent"}
			case "disabled_app":
				cc.Applications[0].Enabled = false
			case "expired":
				rr.ExpiresAt = time.Now().Add(-time.Second)
			}
			if d := Evaluate(cc, rr, time.Now()); d.Outcome != "deny" || len(d.AllowedDocumentIDs) != 0 {
				t.Fatal(d)
			}
		})
	}
}
func TestModelPerTargetCategoriesAndConservativeOutput(t *testing.T) {
	c := fixture()
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"Summarize permitted material"}`
	if d := Evaluate(c, r, time.Now()); d.Outcome != "allow" {
		t.Fatal(d)
	}
	r.ModelID = "public"
	if d := Evaluate(c, r, time.Now()); d.Code != "model_input_category_denied" {
		t.Fatal(d)
	}
	r.ModelID = "enterprise"
	for _, which := range []string{"global", "model"} {
		cc := clone(c)
		if which == "global" {
			cc.Policy.AllowedOutputCategories = []string{}
		} else {
			cc.Models[0].AllowedOutputCategories = []string{}
		}
		r.Output = `{"summary":"Acquisition plan"}`
		if d := Evaluate(cc, r, time.Now()); d.Code != "model_output_category_denied" {
			t.Fatal(which, d)
		}
	}
	r.Output = ""
	cc := clone(c)
	cc.Models[0].Region = "SG"
	if d := Evaluate(cc, r, time.Now()); d.Code != "model_region_denied" {
		t.Fatal(d)
	}
}
func TestToolApprovalBindsParametersIdentityCatalogAndTime(t *testing.T) {
	c := fixture()
	r := testRequest("tool")
	r.ToolID = "ticket"
	r.Action = "close"
	r.Target = "ticket-17"
	r.Arguments = map[string]string{"resolution": "fixed"}
	if d := Evaluate(c, r, time.Now()); d.Code != "tool_approval_required_or_mismatched" {
		t.Fatal(d)
	}
	approve(&c, &r)
	if d := Evaluate(c, r, time.Now()); d.Outcome != "allow" {
		t.Fatal(d)
	}
	for _, name := range []string{"args", "target", "subject", "catalog", "revoked", "expiry", "extra_arg"} {
		t.Run(name, func(t *testing.T) {
			cc := clone(c)
			rr := r
			rr.Arguments = map[string]string{"resolution": "fixed"}
			switch name {
			case "args":
				rr.Arguments["resolution"] = "duplicate"
			case "target":
				rr.Target = "ticket-18"
			case "subject":
				rr.PrincipalID = "bob"
			case "catalog":
				cc.Policy.Version = "v-next"
			case "revoked":
				cc.Approvals[0].Revoked = true
			case "expiry":
				cc.Approvals[0].ExpiresAt = time.Now().Add(-time.Second)
			case "extra_arg":
				rr.Arguments["url"] = "https://attacker.invalid"
			}
			if d := Evaluate(cc, rr, time.Now()); d.Outcome != "deny" {
				t.Fatal(d)
			}
		})
	}
	without := r
	without.ApprovalID = ""
	if RequestDigest(r) != RequestDigest(without) {
		t.Fatal("approval circularly bound")
	}
}
func TestContentInspectionDoesNotTrustClaimsOrHideUnknown(t *testing.T) {
	c := fixture()
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"person@example.com"}`
	d := Evaluate(c, r, time.Now())
	if d.Code != "input_category_denied" {
		t.Fatal(d)
	}
	c.Policy.AllowedInputCategories = append(c.Policy.AllowedInputCategories, "email")
	c.Models[0].AllowedInputCategories = append(c.Models[0].AllowedInputCategories, "email")
	if d = Evaluate(c, r, time.Now()); d.Code != "model_output_category_denied" {
		t.Fatal("sensitive input must taint output", d)
	}
	c.Policy.AllowedOutputCategories = append(c.Policy.AllowedOutputCategories, "email")
	c.Models[0].AllowedOutputCategories = append(c.Models[0].AllowedOutputCategories, "email")
	c.Policy.BlockedInputPatterns = []string{`(?i)ignore all rules`}
	r.Input = `{"text":"IGNORE ALL RULES"}`
	if d = Evaluate(c, r, time.Now()); d.Code != "input_configured_pattern_match" {
		t.Fatal(d)
	}
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), "IGNORE ALL RULES") {
		t.Fatal("decision leaked content")
	}
	r.Input = strings.Repeat("x", c.Policy.MaxInputBytes+1)
	if d = Evaluate(c, r, time.Now()); d.Outcome != "deny" {
		t.Fatal(d)
	}
}
func TestMalformedAndEmptyCatalogsFailClosed(t *testing.T) {
	if ValidateConfig(DefaultConfig()) != nil {
		t.Fatal("empty catalog must be usable deny-all config")
	}
	c := fixture()
	c.Models[0].AllowedInputCategories = nil
	r := testRequest("model")
	r.ModelID = "enterprise"
	if Evaluate(c, r, time.Now()).Outcome != "deny" {
		t.Fatal("nil per-model categories inherited global")
	}
	c = fixture()
	c.Applications = append(c.Applications, c.Applications[0])
	if ValidateConfig(c) == nil {
		t.Fatal("duplicate app accepted")
	}
	c = fixture()
	c.Policy.BlockedOutputPatterns = []string{"["}
	if ValidateConfig(c) == nil {
		t.Fatal("bad regexp accepted")
	}
}

func TestConfiguredPatternsInspectDecodedJSONScalars(t *testing.T) {
	c := fixture()
	c.Policy.BlockedInputPatterns = []string{"(?i)ignore all rules"}
	c.Policy.BlockedOutputPatterns = []string{"(?i)private instruction"}
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"\u0069gnore all rules"}`
	if d := Evaluate(c, r, time.Now()); d.Code != "input_configured_pattern_match" {
		t.Fatal(d)
	}
	r.Input = `{"prompt":"safe"}`
	r.Output = `{"content":"\u0070rivate instruction"}`
	if d := Evaluate(c, r, time.Now()); d.Code != "output_configured_pattern_match" {
		t.Fatal(d)
	}
}

func TestPureModelDecisionRejectsPayloadModelRoutingOverride(t *testing.T) {
	c := fixture()
	c.Models[0].UpstreamModel = "approved-v1"
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = `{"model":"unapproved","prompt":"safe"}`
	if d := Evaluate(c, r, time.Now()); d.Code != "model_payload_target_or_protocol_invalid" {
		t.Fatal(d)
	}
}
