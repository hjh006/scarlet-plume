package aisecurity

import (
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"strings"
	"testing"
	"time"
)

func dataUseFixture() (Config, Request) {
	c := fixture()
	c.Documents[0].UsePolicy = &DataUsePolicy{AllowedPurposes: []string{"customer-support"}, MaxRetentionHours: 0}
	c.Models[0].Handling = &DataHandling{RetentionHours: 0, TrainingUse: "prohibited", EvidenceRef: "dpa-reviewed-v1", AuthorizationExpiresAt: time.Now().Add(time.Hour)}
	r := testRequest("model")
	r.ModelID, r.Input, r.TaskID = "enterprise", `{"prompt":"synthetic safe summary"}`, "task-data-use-01"
	r.taskContext = &TaskContext{TaskID: r.TaskID, Purpose: "customer-support", ScopeDigest: strings.Repeat("a", 64), LeaseChainDigest: strings.Repeat("b", 64), LeaseProtocolVersion: gatewaygovernance.TaskLeaseProtocolVersion, Categories: []string{"confidential"}, SourceDocumentIDs: []string{"doc"}, ExpiresAt: time.Now().Add(time.Hour)}
	return c, r
}

func TestSourcePurposeRetentionAndTrainingRemainAcrossSteps(t *testing.T) {
	c, r := dataUseFixture()
	if d := Evaluate(c, r, time.Now()); d.Outcome != "allow" || d.TaskID != r.TaskID || d.TaskPurpose != "customer-support" {
		t.Fatal("approved source contract rejected", d)
	}
	for _, variant := range []string{"purpose", "missing_handling", "expired_handling", "retention", "training", "revoked_source", "missing_pin", "context_required", "context_expired"} {
		t.Run(variant, func(t *testing.T) {
			cc, rr := dataUseFixture()
			switch variant {
			case "purpose":
				rr.taskContext.Purpose = "model-training"
			case "missing_handling":
				cc.Models[0].Handling = nil
			case "expired_handling":
				cc.Models[0].Handling.AuthorizationExpiresAt = time.Now().Add(-time.Second)
			case "retention":
				cc.Models[0].Handling.RetentionHours = 1
			case "training":
				cc.Models[0].Handling.TrainingUse = "permitted"
			case "revoked_source":
				cc.Documents[0].AllowedPrincipalIDs = []string{"bob"}
			case "missing_pin":
				cc.Policy.RequirePinnedSourceContent = true
			case "context_required":
				rr.taskContext = nil
			case "context_expired":
				rr.taskContext.ExpiresAt = time.Now().Add(-time.Second)
			}
			if d := Evaluate(cc, rr, time.Now()); d.Outcome != "deny" {
				t.Fatal("source restrictions disappeared after document IDs omitted", variant, d)
			}
		})
	}
	// A present restricted source cannot be hidden by leaving both task and
	// document IDs off a model call: existing app-level taint remains conservative.
	r.TaskID, r.taskContext = "", nil
	if d := Evaluate(c, r, time.Now()); d.Code != "source_purpose_denied" {
		t.Fatal(d)
	}
}

func TestTaskToolCannotLaunderSourceCategoriesOrArgumentPII(t *testing.T) {
	c, r := dataUseFixture()
	r.Kind, r.ModelID, r.Input = "tool", "", ""
	r.ToolID, r.Action, r.Target, r.Arguments = "ticket", "close", "ticket-17", map[string]string{"resolution": "fixed"}
	c.Tools[0].HighImpact = false
	c.Tools[0].Handling = c.Models[0].Handling
	c.Tools[0].AllowedInputCategories = []string{"confidential"}
	if d := Evaluate(c, r, time.Now()); d.Outcome != "allow" {
		t.Fatal(d)
	}
	c.Tools[0].AllowedInputCategories = []string{}
	if d := Evaluate(c, r, time.Now()); d.Code != "tool_input_category_denied" {
		t.Fatal("tool ignored task source taint", d)
	}
	c.Tools[0].AllowedInputCategories = []string{"confidential"}
	c.Tools[0].Handling.RetentionHours = 2
	if d := Evaluate(c, r, time.Now()); d.Code != "source_retention_denied" {
		t.Fatal("tool ignored destination retention", d)
	}
	c.Tools[0].Handling.RetentionHours = 0
	for _, value := range []string{"person@example.com", "13800138000"} {
		c.Tools[0].AllowedArguments["resolution"] = []string{value}
		r.Arguments["resolution"] = value
		if d := Evaluate(c, r, time.Now()); d.Code != "tool_input_category_denied" {
			t.Fatal("exact argument allowlist bypassed data policy", d)
		}
	}
}

func TestSourceOnlyRAGScopeAndDataHandlingValidation(t *testing.T) {
	c, r := dataUseFixture()
	c.Policy.RequireDataHandling = true
	r.Kind, r.ModelID, r.Input, r.DocumentIDs = "rag", "", "", []string{"doc"}
	if d := Evaluate(c, r, time.Now()); d.Outcome != "allow" || len(d.AllowedDocumentIDs) != 1 {
		t.Fatal("local RAG incorrectly required provider contract", d)
	}
	c.Documents[0].UsePolicy.AllowedPurposes = []string{}
	if d := Evaluate(c, r, time.Now()); d.Code != "source_purpose_denied" {
		t.Fatal("empty purpose list allowed source use", d)
	}
	for _, value := range []*DataHandling{
		{RetentionHours: -1, TrainingUse: "prohibited", EvidenceRef: "evidence", AuthorizationExpiresAt: time.Now()},
		{RetentionHours: 0, TrainingUse: "unknown", EvidenceRef: "evidence", AuthorizationExpiresAt: time.Now()},
		{RetentionHours: 0, TrainingUse: "prohibited", EvidenceRef: "arbitrary evidence text", AuthorizationExpiresAt: time.Now()},
		{RetentionHours: 0, TrainingUse: "prohibited", EvidenceRef: "evidence"},
	} {
		if validateDataHandling(value) {
			t.Fatal("invalid destination contract accepted", value)
		}
	}
	if validateDataUse(&DataUsePolicy{AllowedPurposes: []string{"support", "support"}}) || validateDataUse(&DataUsePolicy{MaxRetentionHours: -1}) {
		t.Fatal("invalid source policy accepted")
	}
}
