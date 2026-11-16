package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"github.com/jackc/pgx/v5/pgxpool"
)

func taskTestInput(t *testing.T, f assessmentSourceFixture, in GatewayAuthorizeInput) GatewayTaskInput {
	t.Helper()
	identity, e := f.repo.ResolveGateway(t.Context(), GatewayResolveInput{WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash})
	if e != nil {
		t.Fatal(e)
	}
	return GatewayTaskInput{ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, CredentialID: identity.CredentialID, Purpose: "support", CatalogDigest: in.CatalogDigest, ModelIDs: []string{"model"}, ToolIDs: []string{"tool"}, ToolScopes: []gatewaygovernance.TaskToolScope{{ToolID: "tool", Actions: []string{"close"}, Targets: []string{"ticket-17"}}}, DocumentIDs: []string{"doc"}, AllowedCategories: []string{"confidential", "email"}, MaxSteps: 100, BudgetMicros: 1000, MaxDelegationDepth: 3, ExpiresAt: time.Now().UTC().Add(time.Hour)}
}
func taskTestFact(t *testing.T, f assessmentSourceFixture, task GatewayTask, in GatewayAuthorizeInput, label string) gatewaygovernance.TaskCheckInput {
	t.Helper()
	view, e := f.repo.GatewayTaskLeaseView(t.Context(), f.ws.ID, f.user.ID, task.ID)
	if e != nil {
		t.Fatal(e)
	}
	return gatewaygovernance.TaskCheckInput{TaskContext: gatewaygovernance.TaskContext{TaskID: task.ID, TaskStepID: label, TaskStepDigest: gatewayDigest(label), LeaseChainDigest: view.LeaseChainDigest, LeaseProtocolVersion: view.LeaseProtocolVersion}, WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, CatalogDigest: in.CatalogDigest, Kind: "model", ModelID: in.ModelID}
}
func taskTestAuthorize(t *testing.T, f assessmentSourceFixture, in GatewayAuthorizeInput, fact gatewaygovernance.TaskCheckInput) GatewayAuthorization {
	t.Helper()
	if _, e := f.repo.AdmitGatewayTask(t.Context(), fact); e != nil {
		t.Fatal(e)
	}
	in.TaskContext = fact.TaskContext
	in.RequestID = fact.TaskStepID
	in.RequestDigest = fact.TaskStepDigest
	grant, e := f.repo.AuthorizeGateway(t.Context(), in)
	if e != nil {
		t.Fatal(e)
	}
	return grant
}

func taskTestSettlement(t *testing.T, f assessmentSourceFixture, in GatewayAuthorizeInput, g GatewayAuthorization, attempted bool, outcome string) GatewaySettlement {
	t.Helper()
	out := gatewayTestSettlement(in, g, attempted, outcome)
	if g.TaskID != "" {
		out.Audit.RuntimeProtocolVersion = aisecurity.RuntimeProtocolVersion
		out.Audit.InspectionVersion = aisecurity.InspectionVersion
		task, e := f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, f.user.ID, g.TaskID, 100)
		if e != nil {
			t.Fatal(e)
		}
		out.Audit.Decision.LeaseChainDigest = g.LeaseChainDigest
		out.Audit.Decision.LeaseProtocolVersion = g.LeaseProtocolVersion
		out.Audit.Decision.TaskID = g.TaskID
		out.Audit.Decision.TaskPurpose = task.Task.Purpose
		out.Audit.Decision.TaskScopeDigest = task.Task.ScopeDigest
	}
	return out
}

func TestGatewayTaskRequiredBindingAndExactToolScopes(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	cfg := gatewayTestConfig(1_000_000, 10000, 1_000_000)
	cfg.Applications[0].RequireTask = true
	if _, e := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 1, cfg); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.AuthorizeGateway(t.Context(), in); !errors.Is(e, ErrForbidden) {
		t.Fatal("required task bypass", e)
	}
	input := taskTestInput(t, f, in)
	task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, task, in, "task-bound-request-0001")
	if g, e := f.repo.CheckGatewayTask(t.Context(), fact); e != nil || !g.Allowed || g.Purpose != "support" || len(g.ToolScopes) != 1 {
		t.Fatal(g, e)
	}
	for _, mutate := range []func(*gatewaygovernance.TaskCheckInput){func(v *gatewaygovernance.TaskCheckInput) { v.TokenHash = strings.Repeat("e", 64) }, func(v *gatewaygovernance.TaskCheckInput) { v.PrincipalID = "other" }, func(v *gatewaygovernance.TaskCheckInput) { v.ModelID = "other" }, func(v *gatewaygovernance.TaskCheckInput) { v.DocumentIDs = []string{"other"} }, func(v *gatewaygovernance.TaskCheckInput) { v.Categories = []string{"credential"} }, func(v *gatewaygovernance.TaskCheckInput) { v.TaskID = ""; v.TaskStepID = ""; v.TaskStepDigest = "" }} {
		bad := fact
		mutate(&bad)
		if _, e = f.repo.CheckGatewayTask(t.Context(), bad); e == nil {
			t.Fatal("task binding/scope bypass")
		}
	}
	fact.Kind = "tool"
	fact.ModelID = ""
	fact.ToolID = "tool"
	fact.Action = "close"
	fact.Target = "ticket-17"
	if _, e = f.repo.CheckGatewayTask(t.Context(), fact); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*gatewaygovernance.TaskCheckInput){func(v *gatewaygovernance.TaskCheckInput) { v.Action = "delete" }, func(v *gatewaygovernance.TaskCheckInput) { v.Target = "ticket-18" }} {
		bad := fact
		mutate(&bad)
		if _, e = f.repo.CheckGatewayTask(t.Context(), bad); !errors.Is(e, ErrForbidden) {
			t.Fatal("exact action/target bypass", e)
		}
	}
	input.ToolScopes = nil
	if _, e = f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input); !errors.Is(e, ErrInvalid) {
		t.Fatal("missing scopes became wildcard", e)
	}
}

func TestGatewayTaskConcurrentStepAndAncestorProviderBudget(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	input := taskTestInput(t, f, in)
	input.MaxSteps = 3
	input.BudgetMicros = 20
	task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.New(t.Context(), os.Getenv("PLUME_STORE_TEST_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	nodes := []*Store{f.repo, New(pool)}
	var wg sync.WaitGroup
	results := make(chan gatewaygovernance.TaskCheckInput, 12)
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fact := taskTestFact(t, f, task, in, fmt.Sprintf("step-concurrent-%04d", i))
			if _, e := nodes[i%2].AdmitGatewayTask(t.Context(), fact); e == nil {
				results <- fact
			} else if !errors.Is(e, ErrGatewayTaskSteps) {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	accepted := []gatewaygovernance.TaskCheckInput{}
	for fact := range results {
		accepted = append(accepted, fact)
	}
	if len(accepted) != 3 {
		t.Fatal("logical step budget", len(accepted))
	}
	grants := []GatewayAuthorization{}
	for _, fact := range accepted {
		req := in
		req.TaskContext = fact.TaskContext
		req.RequestID = fact.TaskStepID
		req.RequestDigest = fact.TaskStepDigest
		g, e := f.repo.AuthorizeGateway(t.Context(), req)
		if e == nil {
			grants = append(grants, g)
		} else if !errors.Is(e, ErrGatewayBudget) {
			t.Fatal(e)
		}
	}
	if len(grants) != 2 {
		t.Fatal("task budget did not reserve separately", len(grants))
	}
	detail, e := f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, f.user.ID, task.ID, 100)
	if e != nil || detail.Task.StepsUsed != 3 || detail.Task.ReservedMicros != 20 {
		t.Fatal(detail, e)
	}
	for _, g := range grants {
		req := in
		req.TaskContext = g.TaskContext
		req.RequestID = g.RequestID
		req.RequestDigest = g.RequestDigest
		settled := taskTestSettlement(t, f, req, g, true, "succeeded")
		if e = f.repo.SettleGateway(t.Context(), settled); e != nil {
			t.Fatal(e)
		}
		if e = f.repo.SettleGateway(t.Context(), settled); e != nil {
			t.Fatal(e)
		}
	}
	detail, e = f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, f.user.ID, task.ID, 100)
	if e != nil || detail.Task.ReservedMicros != 0 || detail.Task.SpentMicros != 20 {
		t.Fatal(detail, e)
	}
}

func TestGatewayTaskDelegationAttenuatesAndCancellationCascades(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	input := taskTestInput(t, f, in)
	parent, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	childInput := input
	childInput.ParentTaskID = parent.ID
	childInput.MaxDelegationDepth = 1
	childInput.MaxSteps = 10
	childInput.BudgetMicros = 100
	for _, mutate := range []func(*GatewayTaskInput){func(v *GatewayTaskInput) { v.Purpose = "different" }, func(v *GatewayTaskInput) { v.ModelIDs = []string{"other"} }, func(v *GatewayTaskInput) { v.MaxDelegationDepth = 3 }, func(v *GatewayTaskInput) { v.ExpiresAt = parent.ExpiresAt.Add(time.Second) }, func(v *GatewayTaskInput) { v.BudgetMicros = parent.BudgetMicros + 1 }, func(v *GatewayTaskInput) {
		v.ToolScopes = []gatewaygovernance.TaskToolScope{{ToolID: "tool", Actions: []string{"delete"}, Targets: []string{"ticket-17"}}}
	}} {
		bad := childInput
		mutate(&bad)
		if _, e = f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, bad); e == nil {
			t.Fatal("delegation expanded scope")
		}
	}
	child, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, childInput)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, child, in, "child-request-0001")
	grant := taskTestAuthorize(t, f, in, fact)
	for _, id := range []string{parent.ID, child.ID} {
		d, e := f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, f.user.ID, id, 100)
		if e != nil || d.Task.StepsUsed != 1 || d.Task.ReservedMicros != 10 {
			t.Fatal(d, e)
		}
	}
	if _, e = f.repo.SetGatewayTaskStatus(t.Context(), f.ws.ID, f.user.ID, parent.ID, GatewayTaskStatusInput{ExpectedRevision: 1, Status: "cancelled", Reason: "Synthetic kill switch"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.CheckGatewayTask(t.Context(), fact); !errors.Is(e, ErrForbidden) {
		t.Fatal("cancel did not reach child", e)
	}
	d, e := f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, f.user.ID, child.ID, 100)
	if e != nil || d.Task.Status != "cancelled" || d.Task.ReservedMicros != 10 {
		t.Fatal("cancel fabricated refund", d, e)
	}
	if _, e = f.repo.SetGatewayTaskStatus(t.Context(), f.ws.ID, f.user.ID, parent.ID, GatewayTaskStatusInput{ExpectedRevision: 1, Status: "completed", Reason: "stale"}); !errors.Is(e, ErrConflict) {
		t.Fatal("stale revision changed task", e)
	}
	req := in
	req.TaskContext = grant.TaskContext
	req.RequestID = grant.RequestID
	req.RequestDigest = grant.RequestDigest
	unknown := taskTestSettlement(t, f, req, grant, true, "unknown")
	if e = f.repo.SettleGateway(t.Context(), unknown); e != nil {
		t.Fatal(e)
	}
	d, e = f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, f.user.ID, parent.ID, 100)
	if e != nil || d.Task.ReservedMicros != 10 {
		t.Fatal("unknown reservation released", d, e)
	}
}

func TestGatewayTaskCompletionRequiresActualReceiptAndPersistsSourceTaint(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	input := taskTestInput(t, f, in)
	task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, task, in, "rag-source-step-0001")
	fact.Kind = "rag"
	fact.ModelID = ""
	fact.DocumentIDs = []string{"doc"}
	if _, e = f.repo.AdmitGatewayTask(t.Context(), fact); e != nil {
		t.Fatal(e)
	}
	req := in
	req.Kind = "rag"
	req.ModelID = ""
	req.TaskContext = fact.TaskContext
	req.RequestID = fact.TaskStepID
	req.RequestDigest = fact.TaskStepDigest
	grant, e := f.repo.AuthorizeGateway(t.Context(), req)
	if e != nil {
		t.Fatal(e)
	}
	settlement := taskTestSettlement(t, f, req, grant, true, "succeeded")
	settlement.Audit.RuntimeProtocolVersion = aisecurity.RuntimeProtocolVersion
	settlement.Audit.InspectionVersion = aisecurity.InspectionVersion
	settlement.Audit.Decision.Categories = []string{"confidential"}
	settlement.Audit.Decision.AllowedDocumentIDs = []string{"doc"}
	source := gatewaygovernance.TaskSource{DocumentID: "doc", Version: "v1", ACLVersion: "acl1", ContentDigest: strings.Repeat("d", 64)}
	settlement.Audit.Decision.SourceDigests = []string{gatewayDigest(source)}
	raw, _ := json.Marshal(settlement.Audit)
	complete := gatewaygovernance.TaskCompleteInput{TaskCheckInput: fact, Audit: raw, Sources: []gatewaygovernance.TaskSource{source}}
	if _, e = f.repo.CompleteGatewayTask(t.Context(), complete); !errors.Is(e, ErrForbidden) {
		t.Fatal("unexecuted source became trusted", e)
	}
	if e = f.repo.SettleGateway(t.Context(), settlement); e != nil {
		t.Fatal(e)
	}
	out, e := f.repo.CompleteGatewayTask(t.Context(), complete)
	if e != nil || len(out.Sources) != 1 || len(out.Categories) != 1 || out.Categories[0] != "confidential" {
		t.Fatal(out, e)
	}
	if g, e := f.repo.CompleteGatewayTask(t.Context(), complete); e != nil || !g.Replayed {
		t.Fatal("completion replay", g, e)
	}
	model := taskTestFact(t, f, task, in, "next-model-step-0001")
	g, e := f.repo.CheckGatewayTask(t.Context(), model)
	if e != nil || len(g.SourceDocumentIDs) != 1 || g.SourceDocumentIDs[0] != "doc" || g.ScopeDigest != task.ScopeDigest {
		t.Fatal("omitted docIDs erased source taint", g, e)
	}
	child := input
	child.ParentTaskID = task.ID
	child.MaxDelegationDepth = 1
	child.MaxSteps = 50
	withoutSource := child
	withoutSource.DocumentIDs = nil
	narrow, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, withoutSource)
	if e != nil || len(narrow.DocumentIDs) != 0 || len(narrow.Sources) != 1 || len(narrow.ObservedCategories) != 1 {
		t.Fatal("derived source constraints expanded direct read authority", narrow, e)
	}
	narrowFact := taskTestFact(t, f, narrow, in, "narrow-derived-model")
	if g, e := f.repo.CheckGatewayTask(t.Context(), narrowFact); e != nil || len(g.Sources) != 1 || len(g.SourceDocumentIDs) != 1 || len(g.Categories) != 1 {
		t.Fatal("narrow child washed historical source constraints", g, e)
	}
	narrowFact.Kind = "rag"
	narrowFact.ModelID = ""
	narrowFact.DocumentIDs = []string{"doc"}
	if _, e = f.repo.CheckGatewayTask(t.Context(), narrowFact); !errors.Is(e, ErrForbidden) {
		t.Fatal("history source metadata granted direct read", e)
	}
	delegated, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, child)
	if e != nil || len(delegated.ObservedCategories) != 1 || len(delegated.Sources) != 1 {
		t.Fatal("child laundered taint", delegated, e)
	}
	complete.Sources[0].ContentDigest = strings.Repeat("c", 64)
	if _, e = f.repo.CompleteGatewayTask(t.Context(), complete); !errors.Is(e, ErrConflict) {
		t.Fatal("changed source replay accepted", e)
	}
}

func TestGatewayTaskCredentialRevocationAndLogicalStepBinding(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	input := taskTestInput(t, f, in)
	task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, task, in, "bound-step-000001")
	if _, e = f.repo.AdmitGatewayTask(t.Context(), fact); e != nil {
		t.Fatal(e)
	}
	request := in
	request.TaskContext = fact.TaskContext
	request.RequestID = "different-wire-request"
	request.RequestDigest = fact.TaskStepDigest
	if _, e = f.repo.AuthorizeGateway(t.Context(), request); !errors.Is(e, ErrForbidden) {
		t.Fatal("caller-selected step bypassed logical nonce", e)
	}
	if e = f.repo.RevokeGatewayCredential(t.Context(), f.ws.ID, f.user.ID, input.CredentialID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.CheckGatewayTask(t.Context(), fact); !errors.Is(e, ErrForbidden) {
		t.Fatal("revoked task credential accepted", e)
	}
}

func TestGatewayTaskDirectModelSourcePersistsIntoToolAndCannotBeForged(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	input := taskTestInput(t, f, in)
	task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, task, in, "direct-model-source-0001")
	fact.DocumentIDs = []string{"doc"}
	grant := taskTestAuthorize(t, f, in, fact)
	req := in
	req.TaskContext = fact.TaskContext
	req.RequestID = fact.TaskStepID
	req.RequestDigest = fact.TaskStepDigest
	source := gatewaygovernance.TaskSource{DocumentID: "doc", Version: "v1", ACLVersion: "acl1", ContentDigest: strings.Repeat("a", 64)}
	settlement := taskTestSettlement(t, f, req, grant, true, "succeeded")
	settlement.Audit.RuntimeProtocolVersion = aisecurity.RuntimeProtocolVersion
	settlement.Audit.InspectionVersion = aisecurity.InspectionVersion
	settlement.Audit.Decision.Categories = []string{"confidential"}
	settlement.Audit.Decision.AllowedDocumentIDs = []string{"doc"}
	settlement.Audit.Decision.SourceDigests = []string{gatewayDigest(source)}
	if e = f.repo.SettleGateway(t.Context(), settlement); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(settlement.Audit)
	complete := gatewaygovernance.TaskCompleteInput{TaskCheckInput: fact, Audit: raw}
	if _, e = f.repo.CompleteGatewayTask(t.Context(), complete); !errors.Is(e, ErrForbidden) {
		t.Fatal("model discarded the actual source", e)
	}
	complete.Sources = []gatewaygovernance.TaskSource{source}
	if _, e = f.repo.CompleteGatewayTask(t.Context(), complete); e != nil {
		t.Fatal(e)
	}
	tool := taskTestFact(t, f, task, in, "tool-after-model-source")
	tool.Kind = "tool"
	tool.ModelID = ""
	tool.ToolID = "tool"
	tool.Action = "close"
	tool.Target = "ticket-17"
	current, e := f.repo.CheckGatewayTask(t.Context(), tool)
	if e != nil || len(current.Sources) != 1 || current.Sources[0] != source || len(current.Categories) != 1 {
		t.Fatal("omitted document IDs washed model source", current, e)
	}
	if _, e = f.repo.AdmitGatewayTask(t.Context(), tool); e != nil {
		t.Fatal(e)
	}
	req.Kind = "tool"
	req.ModelID = ""
	req.ToolID = "tool"
	req.Action = "close"
	req.Target = "ticket-17"
	req.TaskContext = tool.TaskContext
	req.RequestID = tool.TaskStepID
	req.RequestDigest = tool.TaskStepDigest
	toolGrant, e := f.repo.AuthorizeGateway(t.Context(), req)
	if e != nil {
		t.Fatal(e)
	}
	toolReceipt := taskTestSettlement(t, f, req, toolGrant, true, "succeeded")
	toolReceipt.Audit.RuntimeProtocolVersion = aisecurity.RuntimeProtocolVersion
	toolReceipt.Audit.InspectionVersion = aisecurity.InspectionVersion
	toolReceipt.Audit.Decision.Categories = []string{"confidential"}
	toolReceipt.Audit.Decision.SourceDigests = []string{gatewayDigest(source)}
	if e = f.repo.SettleGateway(t.Context(), toolReceipt); e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(toolReceipt.Audit)
	toolComplete := gatewaygovernance.TaskCompleteInput{TaskCheckInput: tool, Audit: raw, Sources: []gatewaygovernance.TaskSource{source}}
	if _, e = f.repo.CompleteGatewayTask(t.Context(), toolComplete); e != nil {
		t.Fatal("known source metadata failed to carry", e)
	}
	forged := source
	forged.DocumentID = "other"
	toolComplete.Sources = append(toolComplete.Sources, forged)
	if _, e = f.repo.CompleteGatewayTask(t.Context(), toolComplete); e == nil {
		t.Fatal("tool fabricated source provenance")
	}
}

func TestGatewayTaskConcurrentConfigSettlementAndMutation(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	input := taskTestInput(t, f, in)
	task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, task, in, "lock-order-request-0001")
	grant := taskTestAuthorize(t, f, in, fact)
	req := in
	req.TaskContext = grant.TaskContext
	req.RequestID = grant.RequestID
	req.RequestDigest = grant.RequestDigest
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for op := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch op {
			case 0:
				errs <- f.repo.SettleGateway(ctx, taskTestSettlement(t, f, req, grant, true, "succeeded"))
			case 1:
				_, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, input)
				errs <- e
			case 2:
				_, e := f.repo.SaveGatewayConfig(ctx, f.ws.ID, f.user.ID, 1, gatewayTestConfig(1_000_000, 10000, 1_000_000))
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal("lock order regression", e)
		}
	}
}

func TestGatewayTaskCacheTerminalRequiresTaskStepAndNoProviderCharge(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	cfg := gatewayV3Config(t, f, false)
	cfg.Applications[0].RequireTask = true
	if _, e := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, cfg.Revision, cfg); e != nil {
		t.Fatal(e)
	}
	input := taskTestInput(t, f, in)
	input.BudgetMicros = 0
	input.MaxSteps = 1
	task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, task, in, "cache-task-step-00001")
	audit := aisecurity.AuditRecord{RuntimeProtocolVersion: aisecurity.RuntimeProtocolVersion, InspectionVersion: aisecurity.InspectionVersion, EventID: "cache-task-event-0001", AttemptID: "cache-task-attempt-01", RequestID: fact.TaskStepID, Stage: "completed", ObservedAt: time.Now().UTC(), Decision: aisecurity.Decision{Kind: "model", Outcome: "allow", Code: "allowed", CatalogVersion: "test-v1", ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, CatalogDigest: in.CatalogDigest, RequestDigest: fact.TaskStepDigest, TaskID: task.ID, TaskPurpose: task.Purpose, TaskScopeDigest: task.ScopeDigest, LeaseChainDigest: fact.LeaseChainDigest, LeaseProtocolVersion: fact.LeaseProtocolVersion}, Execution: aisecurity.Execution{Outcome: "not_attempted", CacheHit: true}, CacheEvidence: &aisecurity.CacheEvidence{ModelID: "model", InputBytes: 4, OutputBytes: 20, InputHash: strings.Repeat("b", 64), DocumentHash: strings.Repeat("c", 64), PolicyHash: in.CatalogDigest, OutputHash: strings.Repeat("d", 64), InspectionVersion: aisecurity.InspectionVersion, CacheHit: true}}
	overview, e := f.repo.GatewayOverview(t.Context(), f.ws.ID, f.user.ID)
	if e != nil {
		t.Fatal(e)
	}
	event := GatewayDeniedEvent{TaskContext: fact.TaskContext, WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash, ConfigRevision: overview.Config.Revision, ModelID: "model", Audit: &audit}
	if _, e = f.repo.RecordGatewayDenied(t.Context(), event); !errors.Is(e, ErrForbidden) {
		t.Fatal("cache bypassed task step admission", e)
	}
	if _, e = f.repo.AdmitGatewayTask(t.Context(), fact); e != nil {
		t.Fatal(e)
	}
	withoutTask := event
	empty := audit
	empty.Decision.TaskID = ""
	empty.Decision.TaskPurpose = ""
	empty.Decision.TaskScopeDigest = ""
	withoutTask.Audit = &empty
	withoutTask.TaskContext = gatewaygovernance.TaskContext{}
	if _, e = f.repo.RecordGatewayDenied(t.Context(), withoutTask); !errors.Is(e, ErrForbidden) {
		t.Fatal("required task bypassed via cache", e)
	}
	if receipt, e := f.repo.RecordGatewayDenied(t.Context(), event); e != nil || !receipt.Verified {
		t.Fatal(receipt, e)
	}
	raw, _ := json.Marshal(audit)
	if _, e = f.repo.CompleteGatewayTask(t.Context(), gatewaygovernance.TaskCompleteInput{TaskCheckInput: fact, Audit: raw}); e != nil {
		t.Fatal(e)
	}
	detail, e := f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, f.user.ID, task.ID, 100)
	if e != nil || detail.Task.StepsUsed != 1 || detail.Task.SpentMicros != 0 || detail.Task.ReservedMicros != 0 {
		t.Fatal("cache billed provider or skipped step", detail, e)
	}
	other := event
	copyAudit := audit
	copyAudit.EventID = "cache-other-node-event"
	other.Audit = &copyAudit
	if _, e = f.repo.RecordGatewayDenied(t.Context(), other); !errors.Is(e, ErrConflict) {
		t.Fatal("cache node replay", e)
	}
	second := taskTestFact(t, f, task, in, "cache-task-step-00002")
	if _, e = f.repo.AdmitGatewayTask(t.Context(), second); !errors.Is(e, ErrGatewayTaskSteps) {
		t.Fatal("cache evaded task lifetime step bound", e)
	}
}

func TestGatewayTaskReceiptMetadataRejectsPartialContextAndRawSources(t *testing.T) {
	for _, d := range []aisecurity.Decision{{TaskID: "task"}, {TaskPurpose: "support"}, {TaskScopeDigest: strings.Repeat("a", 64)}, {TaskID: "task", TaskPurpose: "raw email person@example.com", TaskScopeDigest: strings.Repeat("a", 64)}, {SourceDigests: []string{"raw document data"}}, {SourceDigests: []string{strings.Repeat("a", 64), strings.Repeat("a", 64)}}} {
		if validGatewayRuntimeEvidence(aisecurity.AuditRecord{Decision: d}) {
			t.Fatal("unbounded/partial task metadata persisted")
		}
	}
	d := aisecurity.Decision{TaskID: "task", TaskPurpose: "support", TaskScopeDigest: strings.Repeat("a", 64), SourceDigests: []string{strings.Repeat("b", 64)}}
	if !validGatewayRuntimeEvidence(aisecurity.AuditRecord{Decision: d}) {
		t.Fatal("valid metadata rejected")
	}
	record := aisecurity.AuditRecord{RuntimeProtocolVersion: aisecurity.RuntimeProtocolVersion, InspectionVersion: aisecurity.InspectionVersion, RequestID: "metadata-record", Stage: "completed", ObservedAt: time.Now().UTC(), Decision: d, Execution: aisecurity.Execution{Attempted: true, Outcome: "succeeded", HTTPStatus: 200}}
	items := aiRuntimeRequests([]AISecurityRuntimeEvent{{SourceID: 1, Current: true, Record: record}})
	if len(items) != 1 || items[0].TaskID != d.TaskID || items[0].TaskPurpose != d.TaskPurpose || items[0].TaskScopeDigest != d.TaskScopeDigest || len(items[0].SourceDigests) != 1 {
		t.Fatal("legacy typed receipt summary lost task source binding", items)
	}
}

func TestGatewayTaskFallbackOneLogicalStepAndIndependentUnknownReservations(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	gatewayV3Config(t, f, true)
	input := taskTestInput(t, f, in)
	input.ModelIDs = append(input.ModelIDs, "alternate-model")
	input.MaxSteps = 1
	input.BudgetMicros = 20
	task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, task, in, "task-fallback-root-001")
	if _, e = f.repo.AdmitGatewayTask(t.Context(), fact); e != nil {
		t.Fatal(e)
	}
	primary := in
	primary.TaskContext = fact.TaskContext
	primary.RequestID = fact.TaskStepID
	primary.RequestDigest = fact.TaskStepDigest
	primary.ParentRequestID = fact.TaskStepID
	primary.RootModelID = "model"
	first, e := f.repo.AuthorizeGateway(t.Context(), primary)
	if e != nil {
		t.Fatal(e)
	}
	failed := taskTestSettlement(t, f, primary, first, true, "failed")
	failed.Audit.Execution.HTTPStatus = 503
	if e = f.repo.SettleGateway(t.Context(), failed); e != nil {
		t.Fatal(e)
	}
	backup := primary
	backup.ModelID = "alternate-model"
	backup.RequestID = "actual-task-fallback-001"
	backup.RequestDigest = gatewayDigest("actual-fallback-request")
	backup.PreviousReservationID = first.ReservationID
	backup.Attempt = 1
	backupFact := fact
	backupFact.ModelID = "alternate-model"
	if g, e := f.repo.AdmitGatewayTask(t.Context(), backupFact); e != nil || !g.Replayed {
		t.Fatal("fallback consumed a second logical step", g, e)
	}
	second, e := f.repo.AuthorizeGateway(t.Context(), backup)
	if e != nil {
		t.Fatal(e)
	}
	completed := taskTestSettlement(t, f, backup, second, true, "succeeded")
	completed.Audit.RuntimeProtocolVersion = aisecurity.RuntimeProtocolVersion
	completed.Audit.InspectionVersion = aisecurity.InspectionVersion
	if e = f.repo.SettleGateway(t.Context(), completed); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(completed.Audit)
	if _, e = f.repo.CompleteGatewayTask(t.Context(), gatewaygovernance.TaskCompleteInput{TaskCheckInput: backupFact, Audit: raw}); e != nil {
		t.Fatal("fallback completion lost root binding", e)
	}
	detail, e := f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, f.user.ID, task.ID, 100)
	if e != nil || detail.Task.StepsUsed != 1 || detail.Task.ReservedMicros != 10 || detail.Task.SpentMicros != 10 {
		t.Fatal("fallback hid unknown charge or doubled logical steps", detail, e)
	}
}

func TestGatewayTaskSourceEnhancedWireQuoteAndReceiptPins(t *testing.T) {
	f, in := gatewayTestSetup(t, 10_000_000, 10000, 10_000_000)
	input := taskTestInput(t, f, in)
	input.BudgetMicros = 2_000_000
	task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, task, in, "source-wire-quote-0001")
	if _, e = f.repo.AdmitGatewayTask(t.Context(), fact); e != nil {
		t.Fatal(e)
	}
	in.TaskContext = fact.TaskContext
	in.RequestID = fact.TaskStepID
	in.RequestDigest = fact.TaskStepDigest
	in.InputBytes = 1_100_000
	if _, e = f.repo.AuthorizeGateway(t.Context(), in); !errors.Is(e, ErrInvalid) {
		t.Fatal("large uninspected input permitted", e)
	}
	in.SourceDigests = []string{"raw source body"}
	if _, e = f.repo.AuthorizeGateway(t.Context(), in); !errors.Is(e, ErrInvalid) {
		t.Fatal("raw source proof permitted", e)
	}
	in.SourceDigests = []string{strings.Repeat("a", 64)}
	grant, e := f.repo.AuthorizeGateway(t.Context(), in)
	if e != nil || grant.MaxCostMicros != 1_100_008 || len(grant.SourceDigests) != 1 {
		t.Fatal("actual source wire not in conservative quote", grant, e)
	}
	receipt := taskTestSettlement(t, f, in, grant, true, "succeeded")
	if e = f.repo.SettleGateway(t.Context(), receipt); !errors.Is(e, ErrForbidden) {
		t.Fatal("source pins disappeared at settlement", e)
	}
	receipt.Audit.Decision.SourceDigests = append([]string{}, in.SourceDigests...)
	validVersion := receipt.Audit.RuntimeProtocolVersion
	receipt.Audit.RuntimeProtocolVersion = "unknown-runtime-v999"
	if e = f.repo.SettleGateway(t.Context(), receipt); !errors.Is(e, ErrForbidden) {
		t.Fatal("unknown runtime version settled a task/source quote", e)
	}
	receipt.Audit.RuntimeProtocolVersion = validVersion
	if e = f.repo.SettleGateway(t.Context(), receipt); e != nil {
		t.Fatal(e)
	}
	detail, e := f.repo.GatewayTaskDetail(t.Context(), f.ws.ID, f.user.ID, task.ID, 100)
	if e != nil || detail.Task.SpentMicros != 1_100_008 || detail.Task.ReservedMicros != 0 {
		t.Fatal("task source budget quote mismatch", detail, e)
	}
}
