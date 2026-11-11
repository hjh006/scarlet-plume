package store

import (
	"errors"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

func TestControlApprovalSerializesDuplicateCanonicalTargets(t *testing.T) {
	f := newOperationsFixture(t)
	ctx, repo, ws := f.ctx, f.repo, f.workspace
	token := operationsTestDigest(f.slug)
	source, err := repo.CreateRuntimeSource(ctx, ws.ID, f.owner.ID, "Duplicate gateway", "Gateway app", "gateway", token)
	f.must(t, err)
	_, err = repo.DeliverRuntimeBatch(ctx, "gateway", token, operationsTestDigest("dedup-route"), []runtimesecurity.Observation{{Method: "GET", Route: "/contacts/{id}", Status: 200, ObservedAt: time.Now().UTC()}})
	f.must(t, err)
	input := ControlActionInput{Kind: "block_api", RuntimeSourceID: source.ID, Method: "GET", Route: "/contacts/{id}", Reason: "First limited block", DurationMinutes: 5}
	first, err := repo.CreateControlAction(ctx, ws.ID, f.owner.ID, input)
	f.must(t, err)
	input.Route = "/contacts/{customer_id}"
	second, err := repo.CreateControlAction(ctx, ws.ID, f.owner.ID, input)
	f.must(t, err)
	type result struct {
		id  int64
		err error
	}
	results := make(chan result, 2)
	for _, action := range []ControlAction{first, second} {
		go func(a ControlAction) {
			_, err := repo.ApproveControlAction(ctx, ws.ID, f.reviewer.ID, a.ID, a.Revision)
			results <- result{a.ID, err}
		}(action)
	}
	var approved, conflicted int64
	for range 2 {
		r := <-results
		if r.err == nil {
			approved = r.id
		} else if errors.Is(r.err, ErrConflict) {
			conflicted = r.id
		} else {
			f.must(t, r.err)
		}
	}
	if approved == 0 || conflicted == 0 {
		t.Fatal("concurrent duplicate approvals did not select exactly one active target")
	}
	_, err = f.pool.Exec(ctx, `UPDATE plume.control_actions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, approved)
	f.must(t, err)
	pending, err := repo.ControlAction(ctx, ws.ID, f.owner.ID, conflicted)
	f.must(t, err)
	_, err = repo.ApproveControlAction(ctx, ws.ID, f.reviewer.ID, pending.ID, pending.Revision)
	f.must(t, err)
}

func TestPostgresActiveControlBlocksDuplicateApprovalAndSourceRevocation(t *testing.T) {
	f := newOperationsFixture(t)
	ctx, repo, ws := f.ctx, f.repo, f.workspace
	job, _, asset, _ := operationsPostgresFixture(t, f)
	token := operationsTestDigest(f.slug)
	source, err := repo.CreateOperationsSource(ctx, ws.ID, f.owner.ID, OperationsSourceInput{Name: "Guarded control source", Kind: "postgres_control", OwnerTeam: "Security", JobID: job.ID}, token)
	f.must(t, err)
	input := ControlActionInput{Kind: "revoke_select", OperationsSourceID: source.ID, AssetID: asset.ID, Reason: "Scoped direct SELECT", DurationMinutes: 5}
	first, err := repo.CreateControlAction(ctx, ws.ID, f.owner.ID, input)
	f.must(t, err)
	second, err := repo.CreateControlAction(ctx, ws.ID, f.owner.ID, input)
	f.must(t, err)
	first, err = repo.ApproveControlAction(ctx, ws.ID, f.reviewer.ID, first.ID, first.Revision)
	f.must(t, err)
	_, err = repo.ApproveControlAction(ctx, ws.ID, f.reviewer.ID, second.ID, second.Revision)
	f.want(t, err, ErrConflict)
	f.want(t, repo.RevokeOperationsSource(ctx, ws.ID, f.owner.ID, source.ID), ErrConflict)
	command, err := repo.ClaimPostgresControl(ctx, token, "apply-lease")
	f.must(t, err)
	if command == nil {
		t.Fatal("missing approved command")
	}
	had, has := true, false
	f.must(t, repo.RecordControlReceipt(ctx, "postgres_control", token, ControlReceipt{ActionID: command.ID, Revision: command.Revision, Lease: command.Lease, Result: ControlResult{Phase: "apply", Success: true, Code: "select_revoked", HadSelect: &had, HasSelect: &has, ObservedAt: time.Now().UTC()}}))
	first, err = repo.ControlAction(ctx, ws.ID, f.owner.ID, first.ID)
	f.must(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE plume.control_actions SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, first.ID)
	f.must(t, err)
	_, err = repo.ApproveControlAction(ctx, ws.ID, f.reviewer.ID, second.ID, second.Revision)
	f.want(t, err, ErrConflict)
	f.want(t, repo.RevokeOperationsSource(ctx, ws.ID, f.owner.ID, source.ID), ErrConflict)
	first, err = repo.RollbackControlAction(ctx, ws.ID, f.owner.ID, first.ID, first.Revision, "Explicit rollback before removing source")
	f.must(t, err)
	f.want(t, repo.RevokeOperationsSource(ctx, ws.ID, f.owner.ID, source.ID), ErrConflict)
	command, err = repo.ClaimPostgresControl(ctx, token, "rollback-lease")
	f.must(t, err)
	if command == nil {
		t.Fatal("missing rollback command")
	}
	had, has = false, true
	f.must(t, repo.RecordControlReceipt(ctx, "postgres_control", token, ControlReceipt{ActionID: command.ID, Revision: command.Revision, Lease: command.Lease, Result: ControlResult{Phase: "rollback", Success: true, Code: "select_restored", HadSelect: &had, HasSelect: &has, ObservedAt: time.Now().UTC()}}))
	f.must(t, repo.RevokeOperationsSource(ctx, ws.ID, f.owner.ID, source.ID))
	_, err = repo.ApproveControlAction(ctx, ws.ID, f.reviewer.ID, second.ID, second.Revision)
	f.want(t, err, ErrConflict)
}
