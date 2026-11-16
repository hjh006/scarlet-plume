package store

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func leaseTestSetup(t *testing.T) (assessmentSourceFixture, GatewayAuthorizeInput) {
	t.Helper()
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	in.CatalogDigest = taskCredentialPublishedFixture(t, f, nil)
	return f, in
}
func leaseTestView(t *testing.T, f assessmentSourceFixture, id string) TaskLeaseView {
	t.Helper()
	v, e := f.repo.GatewayTaskLeaseView(t.Context(), f.ws.ID, f.user.ID, id)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func leaseTestExpected(v TaskLeaseView) []TaskLeaseExpected {
	out := []TaskLeaseExpected{}
	for _, n := range v.Chain {
		out = append(out, TaskLeaseExpected{TaskID: n.TaskID, TaskRevision: n.TaskRevision, LeaseRevision: n.Lease.Revision, CredentialID: n.Lease.CredentialID})
	}
	return out
}
func leaseTestProposal(v TaskLeaseView, target int64, operation string) TaskLeaseProposalInput {
	return TaskLeaseProposalInput{OperationID: operation, ExpectedLeaseChainDigest: v.LeaseChainDigest, ExpectedChain: leaseTestExpected(v), Changes: []TaskLeaseTarget{{TaskID: v.TaskID, NewCredentialID: target}}, Reason: "计划轮换，保留原任务与账务。"}
}
func leaseTestRenewal(v TaskLeaseView, operation string, ids ...string) TaskLeaseRenewalInput {
	return TaskLeaseRenewalInput{OperationID: operation, ExpectedLeaseChainDigest: v.LeaseChainDigest, ExpectedChain: leaseTestExpected(v), ReplaceTaskIDs: ids}
}
func leaseTestExpire(t *testing.T, f assessmentSourceFixture, id int64) {
	t.Helper()
	if _, e := f.pool.Exec(t.Context(), `UPDATE plume.ai_gateway_credentials SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
}

func TestGatewayTaskLeaseStaticRebindIndependentCASAndRetirement(t *testing.T) {
	f, in := leaseTestSetup(t)
	ctx := t.Context()
	task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if e != nil {
		t.Fatal(e)
	}
	target, token, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "app", "Scheduled successor")
	if e != nil {
		t.Fatal(e)
	}
	view := leaseTestView(t, f, task.ID)
	proposalIn := leaseTestProposal(view, target.ID, "static-rebind-0001")
	p, e := f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, proposalIn)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.CredentialPlan) != 1 || p.CredentialPlan[0].Source.ID != task.CredentialID || p.CredentialPlan[0].Target.Name != target.Name || p.CredentialPlan[0].TargetIdentity != nil {
		t.Fatal("frozen review plan missing", p)
	}
	again, e := f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, proposalIn)
	if e != nil || again.ID != p.ID {
		t.Fatal("proposal not idempotent", e)
	}
	different := proposalIn
	different.Reason = "different"
	if _, e = f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, different); !errors.Is(e, ErrConflict) {
		t.Fatal("operation intent overwritten", e)
	}
	review := TaskLeaseReviewInput{ExpectedRevision: 1, Decision: "approve", Note: "独立确认相同应用身份及轮换范围。"}
	if _, e = f.repo.ReviewGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, p.ID, review); !errors.Is(e, ErrForbidden) {
		t.Fatal("self approval", e)
	}
	reviewer := gatewayTestMember(t, f, "reviewer")
	applied, e := f.repo.ReviewGatewayTaskCredentialBinding(ctx, f.ws.ID, reviewer, task.ID, p.ID, review)
	if e != nil || applied.Status != "applied" {
		t.Fatal(applied, e)
	}
	detail, e := f.repo.GatewayTaskDetail(ctx, f.ws.ID, f.user.ID, task.ID, 10)
	if e != nil || detail.Task.CredentialID != task.CredentialID || detail.Task.ScopeDigest != task.ScopeDigest || detail.Task.ExecutionLease.CredentialID != target.ID || detail.Task.ExecutionLease.Revision != 2 || detail.Task.Revision != 1 || !detail.Task.ExpiresAt.Equal(task.ExpiresAt) {
		t.Fatal("original authority rewritten", detail, e)
	}
	if e = f.repo.RevokeGatewayCredential(ctx, f.ws.ID, f.user.ID, task.CredentialID); e != nil {
		t.Fatal(e)
	}
	fresh := workloadAuthorizeInput(in, token)
	fact := taskTestFact(t, f, task, fresh, "rebound-step-0001")
	if _, e = f.repo.AdmitGatewayTask(ctx, fact); e != nil {
		t.Fatal("retiring old key revoked successor", e)
	}
	if _, e = f.repo.CheckGatewayTask(ctx, taskTestFact(t, f, task, in, "old-key-step-0001")); !errors.Is(e, ErrForbidden) {
		t.Fatal("old key executed new lease", e)
	}
	hist, e := f.repo.GatewayTaskLeaseHistory(ctx, f.ws.ID, f.user.ID, task.ID, "", 1)
	if e != nil || len(hist.Items) != 1 || hist.Items[0].Mode != "rebind" {
		t.Fatal(hist, e)
	}
	if _, e = f.repo.ReviewGatewayTaskCredentialBinding(ctx, f.ws.ID, reviewer, task.ID, p.ID, review); !errors.Is(e, ErrConflict) {
		t.Fatal("review replay CAS bypass", e)
	}
	if _, e = f.repo.GatewayTaskLeaseView(ctx, f.ws.ID+1, reviewer, task.ID); e == nil {
		t.Fatal("tenant scope bypass")
	}
}
func TestGatewayTaskLeaseNaturalExpiryRenewalAndHistoricalCompletion(t *testing.T) {
	f, original := leaseTestSetup(t)
	ctx := t.Context()
	binding, sign := workloadStoreFixture(t, f)
	old, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("lease-old-0001"))
	if e != nil {
		t.Fatal(e)
	}
	in := workloadAuthorizeInput(original, old.Token)
	task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, task, in, "renew-old-step-0001")
	auth := taskTestAuthorize(t, f, in, fact)
	in.TaskContext = fact.TaskContext
	in.RequestID = fact.TaskStepID
	in.RequestDigest = fact.TaskStepDigest
	settled := taskTestSettlement(t, f, in, auth, true, "succeeded")
	if e = f.repo.SettleGateway(ctx, settled); e != nil {
		t.Fatal(e)
	}
	before, _ := f.repo.GatewayTaskDetail(ctx, f.ws.ID, f.user.ID, task.ID, 10)
	leaseTestExpire(t, f, old.Credential.ID)
	fresh, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("lease-fresh-0002"))
	if e != nil {
		t.Fatal(e)
	}
	next := workloadAuthorizeInput(original, fresh.Token)
	preflight, e := f.repo.GatewayTaskLeasePreflight(ctx, f.ws.Slug, next.TokenHash, task.ID)
	if e != nil || len(preflight.Chain) != 1 || preflight.Chain[0].Credential.Name != "" || preflight.Chain[0].Identity != nil {
		t.Fatal("preflight leaked scope metadata", e)
	}
	renew := leaseTestRenewal(preflight, "renew-operation-0001", task.ID)
	result, e := f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, next.TokenHash, task.ID, renew)
	if e != nil || result.Change.Mode != "renewal" || result.Lease.LeaseChainDigest == preflight.LeaseChainDigest {
		t.Fatal(result, e)
	}
	replay, e := f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, next.TokenHash, task.ID, renew)
	if e != nil || !replay.Replayed || replay.Change.ID != result.Change.ID {
		t.Fatal("renewal replay", e)
	}
	if _, e = f.repo.AuthorizeGateway(ctx, in); !errors.Is(e, ErrForbidden) {
		t.Fatal("old expired admission replay", e)
	}
	nextFact := taskTestFact(t, f, task, next, "renew-new-step-0002")
	if _, e = f.repo.AdmitGatewayTask(ctx, nextFact); !errors.Is(e, ErrConflict) {
		t.Fatal("prior epoch success taint washed", e)
	}
	// A new pin cannot authorize or read the old admitted nonce.
	forged := in
	forged.TokenHash = next.TokenHash
	forged.LeaseChainDigest = result.Lease.LeaseChainDigest
	if _, e = f.repo.AuthorizeGateway(ctx, forged); !errors.Is(e, ErrForbidden) && !errors.Is(e, ErrConflict) {
		t.Fatal("new epoch borrowed old step", e)
	}
	raw, _ := json.Marshal(settled.Audit)
	completeFact := fact
	completeFact.TokenHash = next.TokenHash
	completeFact.LeaseChainDigest = result.Lease.LeaseChainDigest
	complete := gatewaygovernance.TaskCompleteInput{TaskCheckInput: completeFact, Audit: raw}
	if _, e = f.repo.CompleteRenewedGatewayTaskStep(ctx, f.ws.Slug, next.TokenHash, task.ID, complete); e != nil {
		t.Fatal("exact same-identity receipt cannot recover expired step", e)
	}
	if _, e = f.repo.AdmitGatewayTask(ctx, nextFact); e != nil {
		t.Fatal("renewed task remains stuck after proven completion", e)
	}
	after, _ := f.repo.GatewayTaskDetail(ctx, f.ws.ID, f.user.ID, task.ID, 10)
	if after.Task.SpentMicros != before.Task.SpentMicros || after.Task.ReservedMicros != before.Task.ReservedMicros || after.Task.StepsUsed != before.Task.StepsUsed+1 || after.Task.ScopeDigest != task.ScopeDigest || after.Task.CredentialID != old.Credential.ID {
		t.Fatal("renewal reset accounting or original identity")
	}
	// A later renewal still recognizes the exact old completion intent.
	leaseTestExpire(t, f, fresh.Credential.ID)
	third, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("lease-third-0003"))
	if e != nil {
		t.Fatal(e)
	}
	thirdIn := workloadAuthorizeInput(original, third.Token)
	v := leaseTestView(t, f, task.ID)
	r, e := f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, thirdIn.TokenHash, task.ID, leaseTestRenewal(v, "renew-operation-0002", task.ID))
	if e != nil {
		t.Fatal(e)
	}
	complete.TokenHash = thirdIn.TokenHash
	complete.LeaseChainDigest = r.Lease.LeaseChainDigest
	if g, e := f.repo.CompleteRenewedGatewayTaskStep(ctx, f.ws.Slug, thirdIn.TokenHash, task.ID, complete); e != nil || !g.Replayed {
		t.Fatal("historical completion intent not stable across safe renewals", e)
	}
}
func TestGatewayTaskLeaseAncestorsRequireExplicitMatchingRenewal(t *testing.T) {
	f, original := leaseTestSetup(t)
	ctx := t.Context()
	a, signA := workloadStoreFixture(t, f)
	aOld, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, a.ID, signA("ancestor-old-0001"))
	if e != nil {
		t.Fatal(e)
	}
	aIn := workloadAuthorizeInput(original, aOld.Token)
	input := taskTestInput(t, f, aIn)
	root, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	b, signB := workloadStoreFixture(t, f, "leaf-renewal")
	bOld, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, b.ID, signB("leaf-old-0001"))
	if e != nil {
		t.Fatal(e)
	}
	bIn := workloadAuthorizeInput(original, bOld.Token)
	childInput := taskTestInput(t, f, bIn)
	childInput.ParentTaskID = root.ID
	childInput.ExpiresAt = root.ExpiresAt.Add(-time.Minute)
	childInput.MaxDelegationDepth = 1
	child, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, childInput)
	if e != nil {
		t.Fatal(e)
	}
	leaseTestExpire(t, f, aOld.Credential.ID)
	leaseTestExpire(t, f, bOld.Credential.ID)
	bNew, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, b.ID, signB("leaf-new-0002"))
	if e != nil {
		t.Fatal(e)
	}
	bFresh := workloadAuthorizeInput(original, bNew.Token)
	v := leaseTestView(t, f, child.ID)
	if _, e = f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, bFresh.TokenHash, child.ID, leaseTestRenewal(v, "leaf-only-invalid", child.ID)); !errors.Is(e, ErrForbidden) {
		t.Fatal("expired ancestor bypass", e)
	}
	if _, e = f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, bFresh.TokenHash, child.ID, leaseTestRenewal(v, "cross-binding-invalid", root.ID, child.ID)); !errors.Is(e, ErrForbidden) {
		t.Fatal("ancestor different identity silently rebound", e)
	}
	aNew, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, a.ID, signA("ancestor-new-0002"))
	if e != nil {
		t.Fatal(e)
	}
	aFresh := workloadAuthorizeInput(original, aNew.Token)
	r, e := f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, aFresh.TokenHash, root.ID, leaseTestRenewal(leaseTestView(t, f, root.ID), "ancestor-renew-valid", root.ID))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, bFresh.TokenHash, child.ID, leaseTestRenewal(v, "stale-child-chain", child.ID)); !errors.Is(e, ErrConflict) {
		t.Fatal("ancestor CAS omitted", e)
	}
	if _, e = f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, bFresh.TokenHash, child.ID, leaseTestRenewal(leaseTestView(t, f, child.ID), "leaf-renew-valid", child.ID)); e != nil {
		t.Fatal(e)
	}
	if len(r.Lease.Chain) != 1 {
		t.Fatal("root renewal returned child scope")
	}
	fact := taskTestFact(t, f, child, bFresh, "child-current-step")
	if _, e = f.repo.AdmitGatewayTask(ctx, fact); e != nil {
		t.Fatal(e)
	}
}
func TestGatewayTaskLeaseRebindBlocksSiblingUnknownZeroCost(t *testing.T) {
	f, in := leaseTestSetup(t)
	ctx := t.Context()
	root, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if e != nil {
		t.Fatal(e)
	}
	childIn := taskTestInput(t, f, in)
	childIn.ParentTaskID = root.ID
	childIn.ExpiresAt = root.ExpiresAt.Add(-time.Minute)
	childIn.MaxDelegationDepth = 1
	child, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, childIn)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, child, in, "unknown-tool-step")
	fact.Kind = "tool"
	fact.ModelID = ""
	fact.ToolID = "tool"
	fact.Action = "close"
	fact.Target = "ticket-17"
	call := in
	call.Kind = "tool"
	call.ModelID = ""
	call.ToolID = "tool"
	call.Action = "close"
	call.Target = "ticket-17"
	call.MaxOutputTokens = 0
	g := taskTestAuthorize(t, f, call, fact)
	call.TaskContext = fact.TaskContext
	call.RequestID = fact.TaskStepID
	call.RequestDigest = fact.TaskStepDigest
	settlement := taskTestSettlement(t, f, call, g, true, "failed")
	e = f.repo.SettleGateway(ctx, settlement)
	var state string
	if e != nil {
		t.Fatal(e)
	}
	if e = f.pool.QueryRow(ctx, `SELECT state FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND id=$2`, f.ws.ID, g.ReservationID).Scan(&state); e != nil || state != "unknown" {
		t.Fatal(state, e)
	}
	target, _, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "app", "new static")
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, root.ID, leaseTestProposal(leaseTestView(t, f, root.ID), target.ID, "root-busy-rebind"))
	var le *TaskLeaseError
	if !errors.As(e, &le) || le.Code != "inflight_unresolved" {
		t.Fatal("zero-cost unresolved sibling not detected", e)
	}
	found := false
	for _, b := range le.Blockers {
		if b.ReservationID == g.ReservationID && b.State == "unknown" && b.ReservedMicros != nil && *b.ReservedMicros == 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("busy response hid zero-cost reservation")
	}
}
func TestGatewayTaskLeaseSourceRevocationAtomicWithReview(t *testing.T) {
	f, in := leaseTestSetup(t)
	ctx := t.Context()
	reviewer := gatewayTestMember(t, f, "reviewer")
	for n := 0; n < 4; n++ {
		task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
		if e != nil {
			t.Fatal(e)
		}
		target, _, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "app", fmt.Sprintf("race-target-%d", n))
		if e != nil {
			t.Fatal(e)
		}
		p, e := f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, leaseTestProposal(leaseTestView(t, f, task.ID), target.ID, fmt.Sprintf("race-rebind-%d", n)))
		if e != nil {
			t.Fatal(e)
		}
		var wg sync.WaitGroup
		var reviewErr, revokeErr error
		wg.Go(func() {
			_, reviewErr = f.repo.ReviewGatewayTaskCredentialBinding(ctx, f.ws.ID, reviewer, task.ID, p.ID, TaskLeaseReviewInput{ExpectedRevision: 1, Decision: "approve", Note: "独立轮换核验。"})
		})
		wg.Go(func() { revokeErr = f.repo.RevokeGatewayCredential(ctx, f.ws.ID, f.user.ID, task.CredentialID) })
		wg.Wait()
		if revokeErr != nil {
			t.Fatal(revokeErr)
		}
		d, e := f.repo.GatewayTaskDetail(ctx, f.ws.ID, f.user.ID, task.ID, 1)
		if e != nil {
			t.Fatal(e)
		}
		if reviewErr == nil {
			if d.Task.ExecutionLease.CredentialID != target.ID {
				t.Fatal("cutover lost")
			}
		} else if !errors.Is(reviewErr, ErrConflict) && !errors.Is(reviewErr, ErrForbidden) {
			t.Fatal(reviewErr)
		} else if d.Task.ExecutionLease.CredentialID != task.CredentialID {
			t.Fatal("revoked source revived")
		}
		// The next round uses a genuinely new live source; no revoked source is reset.
		_, token, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "app", fmt.Sprintf("race-source-%d", n))
		if e != nil {
			t.Fatal(e)
		}
		in = workloadAuthorizeInput(in, token)
	}
}
func TestGatewayTaskLeaseUnknownDisabledAndRevokedCannotRenew(t *testing.T) {
	for _, mode := range []string{"unknown-disabled", "explicit-revoke", "terminal-task", "binding-changed"} {
		t.Run(mode, func(t *testing.T) {
			f, original := leaseTestSetup(t)
			ctx := t.Context()
			binding, sign := workloadStoreFixture(t, f)
			old, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("negative-old-0001"))
			if e != nil {
				t.Fatal(e)
			}
			in := workloadAuthorizeInput(original, old.Token)
			task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
			if e != nil {
				t.Fatal(e)
			}
			if mode == "unknown-disabled" {
				if _, e = f.pool.Exec(ctx, `UPDATE plume.ai_gateway_credentials SET enabled=false WHERE id=$1`, old.Credential.ID); e != nil {
					t.Fatal(e)
				}
			}
			leaseTestExpire(t, f, old.Credential.ID)
			if mode == "explicit-revoke" {
				if e = f.repo.RevokeGatewayCredential(ctx, f.ws.ID, f.user.ID, old.Credential.ID); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "terminal-task" {
				if _, e = f.repo.SetGatewayTaskStatus(ctx, f.ws.ID, f.user.ID, task.ID, GatewayTaskStatusInput{ExpectedRevision: 1, Status: "cancelled", Reason: "停止任务"}); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "binding-changed" {
				if _, e = f.repo.SaveGatewayWorkload(ctx, f.ws.ID, f.user.ID, binding.ID, binding.Revision, binding.GatewayWorkloadInput, true); e != nil {
					t.Fatal(e)
				}
			}
			fresh, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("negative-fresh-0002"))
			if e != nil {
				t.Fatal(e)
			}
			next := workloadAuthorizeInput(original, fresh.Token)
			_, e = f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, next.TokenHash, task.ID, leaseTestRenewal(leaseTestView(t, f, task.ID), "negative-renew", task.ID))
			if !errors.Is(e, ErrForbidden) {
				t.Fatal("blocked identity renewed", e)
			}
			body, _ := json.Marshal(leaseTestView(t, f, task.ID))
			if strings.Contains(string(body), old.Token) || strings.Contains(string(body), next.TokenHash) {
				t.Fatal("lease view exposed authentication material")
			}
		})
	}
}

func TestGatewayTaskLeaseClosureNeedsStoredSettledTerminalReceipt(t *testing.T) {
	f, original := leaseTestSetup(t)
	ctx := t.Context()
	binding, sign := workloadStoreFixture(t, f)
	old, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("closure-old-token"))
	if e != nil {
		t.Fatal(e)
	}
	in := workloadAuthorizeInput(original, old.Token)
	task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, task, in, "closure-denied-step")
	auth := taskTestAuthorize(t, f, in, fact)
	in.TaskContext = fact.TaskContext
	in.RequestID = fact.TaskStepID
	in.RequestDigest = fact.TaskStepDigest
	terminal := taskTestSettlement(t, f, in, auth, false, "not_attempted")
	terminal.Audit.Stage = "decision"
	terminal.Audit.Decision.Outcome = "deny"
	if e = f.repo.SettleGateway(ctx, terminal); e != nil {
		t.Fatal(e)
	}
	leaseTestExpire(t, f, old.Credential.ID)
	fresh, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("closure-fresh-token"))
	if e != nil {
		t.Fatal(e)
	}
	next := workloadAuthorizeInput(original, fresh.Token)
	r, e := f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, next.TokenHash, task.ID, leaseTestRenewal(leaseTestView(t, f, task.ID), "closure-renew", task.ID))
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Lease.Blockers) == 0 || r.Lease.Blockers[0].Code != "terminal_evidence_required" {
		t.Fatal("missing recovery blocker", r.Lease.Blockers)
	}
	current := fact
	current.TokenHash = next.TokenHash
	current.LeaseChainDigest = r.Lease.LeaseChainDigest
	forged := *terminal.Audit
	forged.Decision.Code = "forged"
	raw, _ := json.Marshal(forged)
	if _, e = f.repo.CompleteRenewedGatewayTaskStep(ctx, f.ws.Slug, next.TokenHash, task.ID, gatewaygovernance.TaskCompleteInput{TaskCheckInput: current, Audit: raw}); !errors.Is(e, ErrForbidden) {
		t.Fatal("caller terminal claim accepted", e)
	}
	raw, _ = json.Marshal(terminal.Audit)
	if _, e = f.repo.CompleteRenewedGatewayTaskStep(ctx, f.ws.Slug, next.TokenHash, task.ID, gatewaygovernance.TaskCompleteInput{TaskCheckInput: current, Audit: raw}); e != nil {
		t.Fatal("exact settled terminal denied step stuck", e)
	}
	detail, e := f.repo.GatewayTaskDetail(ctx, f.ws.ID, f.user.ID, task.ID, 10)
	if e != nil || detail.Steps[0].Status != "closed" || detail.Task.StepsUsed != 1 || detail.Task.ReservedMicros != 0 || detail.Task.SpentMicros != 0 {
		t.Fatal("closure fabricated success or reset accounting", detail, e)
	}
	if _, e = f.repo.AdmitGatewayTask(ctx, taskTestFact(t, f, task, next, "closure-new-step")); e != nil {
		t.Fatal(e)
	}
}

func TestGatewayTaskLeasePendingProposalInvalidationAndHistoryPages(t *testing.T) {
	f, in := leaseTestSetup(t)
	ctx := t.Context()
	reviewer := gatewayTestMember(t, f, "reviewer")
	task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if e != nil {
		t.Fatal(e)
	}
	cursor := ""
	lastID := int64(0)
	for n := 0; n < 3; n++ {
		target, _, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "app", fmt.Sprintf("history-target-%d", n))
		if e != nil {
			t.Fatal(e)
		}
		p, e := f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, leaseTestProposal(leaseTestView(t, f, task.ID), target.ID, fmt.Sprintf("history-rebind-%d", n)))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.repo.ReviewGatewayTaskCredentialBinding(ctx, f.ws.ID, reviewer, task.ID, p.ID, TaskLeaseReviewInput{ExpectedRevision: 1, Decision: "approve", Note: "独立核定轮换。"}); e != nil {
			t.Fatal(e)
		}
	}
	seen := map[int64]bool{}
	for {
		page, e := f.repo.GatewayTaskLeaseHistory(ctx, f.ws.ID, f.user.ID, task.ID, cursor, 1)
		if e != nil || len(page.Items) != 1 {
			t.Fatal(page, e)
		}
		id := page.Items[0].ID
		if seen[id] || lastID != 0 && id >= lastID {
			t.Fatal("history cursor duplicate or unstable")
		}
		seen[id] = true
		lastID = id
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatal("lost history")
	}
	other, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.GatewayTaskLeaseHistory(ctx, f.ws.ID, f.user.ID, other.ID, cursor, 1); !errors.Is(e, ErrInvalid) {
		t.Fatal("cursor escaped task", e)
	}
	target, _, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "app", "revoked pending target")
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, leaseTestProposal(leaseTestView(t, f, task.ID), target.ID, "pending-invalidated"))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.repo.RevokeGatewayCredential(ctx, f.ws.ID, f.user.ID, target.ID); e != nil {
		t.Fatal(e)
	}
	p, e = f.repo.GatewayTaskLeaseProposal(ctx, f.ws.ID, reviewer, task.ID, p.ID, "")
	if e != nil || p.Status != "invalidated" || p.CanReview {
		t.Fatal("revoked target left reviewable proposal", p, e)
	}
}

func TestGatewayTaskLeaseConsoleCanChangeAncestorOnlyFromLeaf(t *testing.T) {
	f, in := leaseTestSetup(t)
	ctx := t.Context()
	root, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if e != nil {
		t.Fatal(e)
	}
	ci := taskTestInput(t, f, in)
	ci.ParentTaskID = root.ID
	ci.ExpiresAt = root.ExpiresAt.Add(-time.Minute)
	ci.MaxDelegationDepth = 1
	child, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, ci)
	if e != nil {
		t.Fatal(e)
	}
	target, _, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "app", "Ancestor successor")
	if e != nil {
		t.Fatal(e)
	}
	v := leaseTestView(t, f, child.ID)
	input := leaseTestProposal(v, target.ID, "ancestor-only-proposal")
	input.Changes = []TaskLeaseTarget{{TaskID: root.ID, NewCredentialID: target.ID}}
	p, e := f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, child.ID, input)
	if e != nil {
		t.Fatal("valid chain subset rejected", e)
	}
	reviewer := gatewayTestMember(t, f, "reviewer")
	if _, e = f.repo.ReviewGatewayTaskCredentialBinding(ctx, f.ws.ID, reviewer, child.ID, p.ID, TaskLeaseReviewInput{ExpectedRevision: 1, Decision: "approve", Note: "仅轮换所选祖先。"}); e != nil {
		t.Fatal(e)
	}
	after := leaseTestView(t, f, child.ID)
	if after.Chain[0].Lease.CredentialID != target.ID || after.Chain[1].Lease.CredentialID != child.CredentialID || after.Chain[1].Lease.Revision != 1 {
		t.Fatal("unselected leaf changed")
	}
	history, e := f.repo.GatewayTaskLeaseHistory(ctx, f.ws.ID, f.user.ID, root.ID, "", 10)
	if e != nil || len(history.Items) != 1 || history.Items[0].TaskID != child.ID {
		t.Fatal("ancestor change lost from ancestor history", history, e)
	}
}

func TestGatewayTaskLeaseCacheEpochScopeAndCompletedPut(t *testing.T) {
	f, in := leaseTestSetup(t)
	ctx := t.Context()
	t.Setenv("SCARLET_GATEWAY_TASK_LEASE_CACHE_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	overview, e := f.repo.GatewayOverview(ctx, f.ws.ID, f.user.ID)
	if e != nil {
		t.Fatal(e)
	}
	cfg := overview.Config
	cfg.Applications[0].CachePolicy = GatewayCachePolicy{Enabled: true, TTLSeconds: 300, MaxEntries: 10, MaxBytes: 1 << 20, KeyRef: "env:SCARLET_GATEWAY_TASK_LEASE_CACHE_KEY", KeyVersion: "key-v1"}
	if _, e = f.repo.SaveGatewayConfig(ctx, f.ws.ID, f.user.ID, cfg.Revision, cfg); e != nil {
		t.Fatal(e)
	}
	task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if e != nil {
		t.Fatal(e)
	}
	scope := GatewayCacheScope{WorkspaceSlug: f.ws.Slug, TokenHash: in.TokenHash, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, ModelID: "model", CatalogDigest: in.CatalogDigest, InspectionVersion: aisecurity.InspectionVersion, InputHash: gatewayDigest("exact input"), DocumentHash: gatewayDigest([]string{}), PolicyHash: in.CatalogDigest}
	finish := func(label string) (GatewayAuthorization, gatewaygovernance.TaskCheckInput) {
		t.Helper()
		fact := taskTestFact(t, f, task, in, label)
		auth := taskTestAuthorize(t, f, in, fact)
		call := in
		call.TaskContext = fact.TaskContext
		call.RequestID = fact.TaskStepID
		call.RequestDigest = fact.TaskStepDigest
		settlement := taskTestSettlement(t, f, call, auth, true, "succeeded")
		settlement.Audit.CacheEvidence = &aisecurity.CacheEvidence{ModelID: "model", InputBytes: 2, OutputBytes: int64(len("safe cached output")), InputHash: scope.InputHash, DocumentHash: scope.DocumentHash, PolicyHash: scope.PolicyHash, OutputHash: gatewayOutputHash("safe cached output"), InspectionVersion: scope.InspectionVersion}
		if e = f.repo.SettleGateway(ctx, settlement); e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(settlement.Audit)
		if _, e = f.repo.CompleteGatewayTask(ctx, gatewaygovernance.TaskCompleteInput{TaskCheckInput: fact, Audit: raw}); e != nil {
			t.Fatal(e)
		}
		return auth, fact
	}
	auth, fact := finish("cache-lease-step-one")
	scope.TaskContext = fact.TaskContext
	put := GatewayCachePut{CacheScope: scope, ReservationID: auth.ReservationID, Output: "safe cached output", OutputHash: gatewayOutputHash("safe cached output")}
	if e = f.repo.PutGatewayCache(ctx, put); e != nil {
		t.Fatal("runtime completes task before cache put", e)
	}
	second := taskTestFact(t, f, task, in, "cache-lease-step-two")
	if _, e = f.repo.AdmitGatewayTask(ctx, second); e != nil {
		t.Fatal(e)
	}
	read := scope
	read.TaskContext = second.TaskContext
	if hit, e := f.repo.GetGatewayCache(ctx, read); e != nil || !hit.Hit {
		t.Fatal("new nonce within same epoch lost cache", e)
	}
	// Close the genuine second step before controlled identity rotation.
	_, _ = finish("cache-lease-step-two")
	target, token, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "app", "Cache rotation successor")
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, leaseTestProposal(leaseTestView(t, f, task.ID), target.ID, "cache-epoch-rebind"))
	if e != nil {
		t.Fatal(e)
	}
	reviewer := gatewayTestMember(t, f, "reviewer")
	if _, e = f.repo.ReviewGatewayTaskCredentialBinding(ctx, f.ws.ID, reviewer, task.ID, p.ID, TaskLeaseReviewInput{ExpectedRevision: 1, Decision: "approve", Note: "独立确认轮换。"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.GetGatewayCache(ctx, read); !errors.Is(e, ErrForbidden) {
		t.Fatal("old epoch cache read", e)
	}
	in = workloadAuthorizeInput(in, token)
	current := taskTestFact(t, f, task, in, "cache-new-epoch-step")
	if _, e = f.repo.AdmitGatewayTask(ctx, current); e != nil {
		t.Fatal(e)
	}
	newRead := scope
	newRead.TokenHash = in.TokenHash
	newRead.TaskContext = current.TaskContext
	if hit, e := f.repo.GetGatewayCache(ctx, newRead); e != nil || hit.Hit {
		t.Fatal("old epoch ciphertext released as new", e)
	}
	repacked := put
	repacked.CacheScope = newRead
	if e = f.repo.PutGatewayCache(ctx, repacked); !errors.Is(e, ErrForbidden) {
		t.Fatal("old reservation repacked into new epoch", e)
	}
}

func gatewayOutputHash(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func TestGatewayTaskLeaseControlledTrustedWorkloadMigrationAndNoDowngrade(t *testing.T) {
	for _, source := range []string{"static", "workload"} {
		t.Run(source, func(t *testing.T) {
			f, in := leaseTestSetup(t)
			ctx := t.Context()
			var oldToken string
			if source == "workload" {
				binding, sign := workloadStoreFixture(t, f, "migration-source")
				old, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("migration-source-token"))
				if e != nil {
					t.Fatal(e)
				}
				oldToken = old.Token
				in = workloadAuthorizeInput(in, old.Token)
			}
			task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
			if e != nil {
				t.Fatal(e)
			}
			binding, sign := workloadStoreFixture(t, f, "migration-target")
			target, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("migration-target-token"))
			if e != nil {
				t.Fatal(e)
			}
			p, e := f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, leaseTestProposal(leaseTestView(t, f, task.ID), target.Credential.ID, "trusted-workload-migrate"))
			if e != nil {
				t.Fatal(e)
			}
			plan := p.CredentialPlan[0]
			if plan.TargetIdentity == nil || plan.TargetIdentity.Subject != binding.Subject || p.ExpiresAt.After(target.ExpiresAt) || source == "workload" && (plan.SourceIdentity == nil || plan.SourceIdentity.Subject == plan.TargetIdentity.Subject) {
				t.Fatal("review omitted migration identity or short expiry", p)
			}
			reviewer := gatewayTestMember(t, f, "reviewer")
			if _, e = f.repo.ReviewGatewayTaskCredentialBinding(ctx, f.ws.ID, reviewer, task.ID, p.ID, TaskLeaseReviewInput{ExpectedRevision: 1, Decision: "approve", Note: "独立核对issuer、audience、subject及任务范围。"}); e != nil {
				t.Fatal(e)
			}
			next := workloadAuthorizeInput(in, target.Token)
			if _, e = f.repo.CheckGatewayTask(ctx, taskTestFact(t, f, task, next, "trusted-migrated-step")); e != nil {
				t.Fatal(e)
			}
			if oldToken != "" {
				if _, e = f.repo.GatewayTaskLeasePreflight(ctx, f.ws.Slug, workloadAuthorizeInput(in, oldToken).TokenHash, task.ID); !errors.Is(e, ErrForbidden) {
					t.Fatal("old workload identity reads new identity lease", e)
				}
			}
			static, _, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "app", "downgrade denied")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, leaseTestProposal(leaseTestView(t, f, task.ID), static.ID, "forbidden-downgrade")); !errors.Is(e, ErrForbidden) {
				t.Fatal("short-token task downgraded", e)
			}
		})
	}
}
func TestGatewayTaskLeaseRenewalPreservesUnknownAndRejectsLegacyStepProof(t *testing.T) {
	for _, state := range []string{"unknown-fee", "legacy-step"} {
		t.Run(state, func(t *testing.T) {
			f, original := leaseTestSetup(t)
			ctx := t.Context()
			binding, sign := workloadStoreFixture(t, f)
			old, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("unresolved-old-token"))
			if e != nil {
				t.Fatal(e)
			}
			in := workloadAuthorizeInput(original, old.Token)
			task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
			if e != nil {
				t.Fatal(e)
			}
			fact := taskTestFact(t, f, task, in, "unresolved-original-step")
			g := taskTestAuthorize(t, f, in, fact)
			call := in
			call.TaskContext = fact.TaskContext
			call.RequestID = fact.TaskStepID
			call.RequestDigest = fact.TaskStepDigest
			unknown := taskTestSettlement(t, f, call, g, true, "failed")
			if e = f.repo.SettleGateway(ctx, unknown); e != nil {
				t.Fatal(e)
			}
			if state == "legacy-step" {
				if _, e = f.pool.Exec(ctx, `UPDATE plume.ai_gateway_task_steps SET lease_chain_digest=NULL,lease_snapshot=NULL WHERE workspace_id=$1 AND step_id=$2`, f.ws.ID, fact.TaskStepID); e != nil {
					t.Fatal(e)
				}
			}
			leaseTestExpire(t, f, old.Credential.ID)
			fresh, e := f.repo.ExchangeGatewayWorkloadToken(ctx, f.ws.Slug, binding.ID, sign("unresolved-fresh-token"))
			if e != nil {
				t.Fatal(e)
			}
			next := workloadAuthorizeInput(original, fresh.Token)
			v := leaseTestView(t, f, task.ID)
			r, e := f.repo.RenewGatewayTaskCredential(ctx, f.ws.Slug, next.TokenHash, task.ID, leaseTestRenewal(v, "unresolved-renew", task.ID))
			if state == "legacy-step" {
				if !errors.Is(e, ErrConflict) {
					t.Fatal("unknown legacy epoch was backfilled", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			detail, e := f.repo.GatewayTaskDetail(ctx, f.ws.ID, f.user.ID, task.ID, 10)
			if e != nil || detail.Task.ReservedMicros != g.MaxCostMicros || detail.Task.SpentMicros != 0 || detail.Task.StepsUsed != 1 {
				t.Fatal("renewal refunded unknown or reset budget", detail, e)
			}
			current := fact
			current.TokenHash = next.TokenHash
			current.LeaseChainDigest = r.Lease.LeaseChainDigest
			raw, _ := json.Marshal(unknown.Audit)
			if _, e = f.repo.CompleteRenewedGatewayTaskStep(ctx, f.ws.Slug, next.TokenHash, task.ID, gatewaygovernance.TaskCompleteInput{TaskCheckInput: current, Audit: raw}); !errors.Is(e, ErrForbidden) {
				t.Fatal("unknown fee treated as proven settled terminal", e)
			}
			if _, e = f.repo.AdmitGatewayTask(ctx, taskTestFact(t, f, task, next, "unknown-next-step")); !errors.Is(e, ErrConflict) {
				t.Fatal("new epoch omitted unresolved taint", e)
			}
		})
	}
}

func TestGatewayTaskLeaseApprovalPinFrozenAndNeverBackfilledAfterRebind(t *testing.T) {
	f, in := leaseTestSetup(t)
	ctx := t.Context()
	var catalog aisecurity.Config
	if e := f.pool.QueryRow(ctx, `SELECT configuration FROM plume.ai_security_configurations WHERE workspace_id=$1`, f.ws.ID).Scan(&catalog); e != nil {
		t.Fatal(e)
	}
	catalog.Tools[0].HighImpact = true
	raw, _ := json.Marshal(catalog)
	in.CatalogDigest = aisecurity.ConfigDigest(catalog)
	if _, e := f.pool.Exec(ctx, `UPDATE plume.ai_security_configurations SET configuration=$2,published_configuration=$2,published_digest=$3 WHERE workspace_id=$1`, f.ws.ID, raw, in.CatalogDigest); e != nil {
		t.Fatal(e)
	}
	overview, e := f.repo.GatewayOverview(ctx, f.ws.ID, f.user.ID)
	if e != nil {
		t.Fatal(e)
	}
	config := overview.Config
	config.CatalogDigest = in.CatalogDigest
	if _, e = f.repo.SaveGatewayConfig(ctx, f.ws.ID, f.user.ID, config.Revision, config); e != nil {
		t.Fatal(e)
	}
	task, e := f.repo.CreateGatewayTask(ctx, f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	request := aisecurity.Request{TaskID: task.ID, RequestID: "lease-frozen-approval", Kind: "tool", ApplicationID: "app", PrincipalID: "principal", ToolID: "tool", Action: "close", Target: "ticket-17", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	proposal, e := f.repo.RequestAISecurityApproval(ctx, f.ws.ID, f.user.ID, 1, request)
	if e != nil || proposal.LeaseChainDigest == "" || proposal.LeaseProtocolVersion != gatewaygovernance.TaskLeaseProtocolVersion {
		t.Fatal("approval missing server-derived immutable epoch", proposal, e)
	}
	reviewer := gatewayTestMember(t, f, "reviewer")
	approved, e := f.repo.ReviewAISecurityApproval(ctx, f.ws.ID, reviewer, proposal.ID, "approved", "独立核对任务、操作与执行epoch。")
	if e != nil {
		t.Fatal(e)
	}
	loaded, _, e := f.repo.AISecurityRuntimeConfig(ctx, f.ws.ID, f.user.ID)
	if e != nil || len(loaded.Approvals) != 1 || loaded.Approvals[0].LeaseChainDigest != approved.LeaseChainDigest {
		t.Fatal("runtime loader dropped frozen epoch", e)
	}
	target, token, e := f.repo.CreateGatewayCredential(ctx, f.ws.ID, f.user.ID, "app", "Approved key rotation")
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.repo.ProposeGatewayTaskCredentialBinding(ctx, f.ws.ID, f.user.ID, task.ID, leaseTestProposal(leaseTestView(t, f, task.ID), target.ID, "approval-rebind"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ReviewGatewayTaskCredentialBinding(ctx, f.ws.ID, reviewer, task.ID, p.ID, TaskLeaseReviewInput{ExpectedRevision: 1, Decision: "approve", Note: "独立核定轮换。"}); e != nil {
		t.Fatal(e)
	}
	var status, pin string
	if e = f.pool.QueryRow(ctx, `SELECT status,task_lease_digest FROM plume.ai_security_approvals WHERE workspace_id=$1 AND id=$2`, f.ws.ID, proposal.ID).Scan(&status, &pin); e != nil || status != "invalidated" || pin != approved.LeaseChainDigest {
		t.Fatal("old approval revived or rewritten", status, pin, e)
	}
	next := workloadAuthorizeInput(in, token)
	fact := taskTestFact(t, f, task, next, request.RequestID)
	fact.Kind = "tool"
	fact.ModelID = ""
	fact.ToolID = "tool"
	fact.Action = "close"
	fact.Target = "ticket-17"
	grant, e := f.repo.CheckGatewayTask(ctx, fact)
	if e != nil {
		t.Fatal(e)
	}
	current, e := aisecurity.ApplyTaskGrant(request, grant)
	if e != nil {
		t.Fatal(e)
	}
	current.ApprovalID = fmt.Sprint(approved.ID)
	decision := aisecurity.Evaluate(loaded, current, time.Now().UTC())
	if !decision.RequiresApproval || decision.Outcome == "allow" {
		t.Fatal("stale loaded approved snapshot reused after lease change", decision)
	}
}
