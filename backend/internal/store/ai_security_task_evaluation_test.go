package store

import (
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestTaskRequiredCatalogCanPassPureEnginePublicationGate(t *testing.T) {
	c := aiTestConfig()
	c.Applications[0].RequireTask = true
	c.Documents[0].UsePolicy = &aisecurity.DataUsePolicy{AllowedPurposes: []string{"support"}, MaxRetentionHours: 0}
	c.Models[0].Handling = &aisecurity.DataHandling{RetentionHours: 0, TrainingUse: "prohibited", EvidenceRef: "approved-contract", AuthorizationExpiresAt: time.Now().Add(time.Hour)}
	c.Tools[0].Handling = c.Models[0].Handling
	c.Policy.RequireDataHandling = true
	now := time.Now()
	result := aiEvaluateConfiguration(c, 3, now)
	if result.Status != "passed" {
		t.Fatalf("task policy created circular publish prerequisite: %+v", result.Cases)
	}
	if !strings.Contains(strings.Join(result.Scope, " "), "合成可信上下文") {
		t.Fatal("synthetic context was reported as runtime admission")
	}
	pack := BaselineAISecurityRegressionPack()
	gate := aiEvaluateRegression(c, 3, pack, aiPackSummary(1, pack, now), now)
	if !gate.GateEligible {
		t.Fatalf("versioned task catalog gate failed: %+v", gate)
	}
	// The public preview never calls the synthetic fixture helper.
	r := aisecurity.Request{RequestID: "untrusted-task-preview", Kind: "model", ApplicationID: "enterprise-app", PrincipalID: "branch-user", TaskID: "configuration-gate-task", ModelID: "approved-model", Input: `{"prompt":"safe"}`, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	if d := aisecurity.Evaluate(c, r, now); d.Outcome != "deny" || d.Code != "task_context_required_or_invalid" {
		t.Fatal("preview gained synthetic execution authority", d)
	}
	c.Models[0].Handling.RetentionHours = 2
	if result := aiEvaluateConfiguration(c, 4, now); result.Status == "passed" {
		t.Fatal("gate ignored restricted source retention")
	}
}
