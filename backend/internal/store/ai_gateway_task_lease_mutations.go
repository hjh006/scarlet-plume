package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func leaseNote(s string) bool {
	return strings.TrimSpace(s) == s && gatewayText(s, 600) && !strings.ContainsAny(s, "\r\n\t")
}
func leaseInputValid(id, operation, digest string, expected []TaskLeaseExpected, changes []TaskLeaseTarget) bool {
	if !gatewayRuntimeID.MatchString(id) || !gatewayRuntimeID.MatchString(operation) || !gatewayHash.MatchString(digest) || len(expected) < 1 || len(expected) > 5 || len(changes) < 1 || len(changes) > 5 {
		return false
	}
	seen := map[string]bool{}
	for _, e := range expected {
		if !gatewayRuntimeID.MatchString(e.TaskID) || seen[e.TaskID] || e.TaskRevision < 1 || e.LeaseRevision < 1 || e.CredentialID < 1 {
			return false
		}
		seen[e.TaskID] = true
	}
	changed := map[string]bool{}
	for _, c := range changes {
		if !seen[c.TaskID] || changed[c.TaskID] || c.NewCredentialID < 1 {
			return false
		}
		changed[c.TaskID] = true
	}
	return true
}

// Lock order is shared with workload exchange/revocation. No proposal row is
// locked until credentials, configuration and task CAS have been locked. A
// revoke therefore either wins before validation or waits until after cutover.
func lockTaskLeaseChange(ctx context.Context, tx pgx.Tx, ws int64, id string, expected []TaskLeaseExpected, changes []TaskLeaseTarget) (chain []GatewayTask, creds map[int64]leaseCredential, err error) {
	if err = gatewayWorkloadLock(ctx, tx, ws); err != nil {
		return
	}
	ids := []int64{}
	for _, e := range expected {
		ids = append(ids, e.CredentialID)
	}
	for _, c := range changes {
		ids = append(ids, c.NewCredentialID)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	creds = map[int64]leaseCredential{}
	for _, cred := range ids {
		c, e := readTaskLeaseCredential(ctx, tx, ws, cred, true)
		if e != nil {
			return nil, nil, e
		}
		creds[cred] = c
	}
	if _, err = gatewayConfig(ctx, tx, ws, " FOR SHARE"); err != nil {
		return
	}
	// Publishing or editing current authority cannot race the atomic review.
	var dummy int64
	if err = tx.QueryRow(ctx, `SELECT revision FROM plume.ai_security_configurations WHERE workspace_id=$1 FOR SHARE`, ws).Scan(&dummy); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, leaseError("current_catalog_unverified", "未取得当前AI授权目录，不能切换执行凭据。", ErrForbidden)
		}
		return nil, nil, storeError(err)
	}
	if err = gatewayTaskLock(ctx, tx, ws); err != nil {
		return
	}
	chain, err = gatewayTaskChain(ctx, tx, ws, id)
	return
}
func validateTaskLeaseChange(ctx context.Context, tx pgx.Tx, ws int64, chain []GatewayTask, creds map[int64]leaseCredential, expectedDigest string, expected []TaskLeaseExpected, changes []TaskLeaseTarget, machine bool, now time.Time) (plan []TaskLeaseCredentialPlan, expiry time.Time, err error) {
	if !leaseExpectedMatches(chain, expectedDigest, expected) {
		return nil, expiry, leaseError("lease_conflict", "任务或祖先链修订已变化，请重新读取CAS。", ErrConflict)
	}
	if _, err = leaseAuthority(ctx, tx, ws, chain, now); err != nil {
		return
	}
	expiry = now.Add(15 * time.Minute)
	byTask := map[string]int64{}
	for _, c := range changes {
		byTask[c.TaskID] = c.NewCredentialID
	}
	plan = []TaskLeaseCredentialPlan{}
	for _, t := range chain {
		if t.ExpiresAt.Before(expiry) {
			expiry = t.ExpiresAt
		}
		old := creds[t.ExecutionLease.CredentialID]
		old.Enabled = old.live(now)
		if !machine && old.ExpiresAt != nil && old.ExpiresAt.Before(expiry) {
			expiry = *old.ExpiresAt
		}
		newID, change := byTask[t.ID]
		if old.ID < 1 || old.ApplicationID != t.ApplicationID {
			return nil, expiry, ErrForbidden
		}
		if !change {
			if !old.live(now) {
				return nil, expiry, leaseError("ancestor_credential_inactive", "未替换祖先的当前凭据不可执行，须先用相应身份恢复祖先。", ErrForbidden, TaskLeaseIssue{Code: "ancestor_credential_inactive", TaskID: t.ID, State: "blocked", Message: "不允许叶任务跳过祖先当前凭据。"})
			}
			if old.Binding != nil {
				if _, err = old.identity(); err != nil {
					return
				}
			}
			continue
		}
		fresh := creds[newID]
		fresh.Enabled = fresh.live(now)
		if fresh.ID == old.ID || fresh.ApplicationID != t.ApplicationID || !fresh.live(now) {
			return nil, expiry, leaseError("target_credential_inactive", "目标须为同应用的另一个当前有效凭据。", ErrForbidden)
		}
		if fresh.Binding != nil {
			if _, err = fresh.identity(); err != nil {
				return
			}
			if fresh.ExpiresAt == nil {
				return nil, expiry, ErrForbidden
			}
		}
		if old.RevokedAt != nil {
			return nil, expiry, leaseError("source_credential_revoked", "来源凭据已有明确撤销事实，不可恢复。", ErrForbidden)
		}
		if machine {
			a, e := old.identity()
			if e != nil {
				return nil, expiry, e
			}
			b, e := fresh.identity()
			if e != nil || a != b {
				return nil, expiry, leaseError("identity_mismatch", "机器续期只接受同绑定、签发修订及issuer/audience/subject身份。", ErrForbidden)
			}
			if old.ExpiresAt == nil || fresh.ExpiresAt == nil || !fresh.ExpiresAt.After(*old.ExpiresAt) || !fresh.CreatedAt.After(old.CreatedAt) || (!old.StoredEnabled && (old.ExpiryDisabledAt == nil || old.ExpiresAt.After(now))) {
				return nil, expiry, leaseError("source_credential_unknown", "来源不是可确认自然到期或仍有效的较早短期凭据。", ErrForbidden)
			}
		} else {
			if !old.live(now) {
				return nil, expiry, leaseError("source_credential_inactive", "受控换绑要求来源当前有效；不能救活到期、撤销或未知停用任务。", ErrForbidden)
			}
			if old.Binding != nil {
				if _, err = old.identity(); err != nil {
					return
				}
				if fresh.Binding == nil {
					return nil, expiry, leaseError("credential_downgrade", "工作负载短期凭据不能降级为静态密钥。", ErrForbidden)
				}
			}
		}
		if fresh.ExpiresAt != nil && fresh.ExpiresAt.Before(expiry) {
			expiry = *fresh.ExpiresAt
		}
		plan = append(plan, TaskLeaseCredentialPlan{TaskID: t.ID, Source: old.GatewayCredential, Target: fresh.GatewayCredential, SourceIdentity: old.identitySnapshot(), TargetIdentity: fresh.identitySnapshot()})
	}
	if machine {
		if busy, e := taskLeaseBusy(ctx, tx, ws, changes, true); e != nil {
			return nil, expiry, e
		} else if len(busy) > 0 {
			return nil, expiry, leaseError("terminal_evidence_required", "历史未封存步骤缺少原执行epoch证明，不能以当前租约回填。", ErrConflict, busy...)
		}
	}
	if !expiry.After(now) {
		return nil, expiry, ErrConflict
	}
	return
}

// A subtree can have siblings not in the supplied ancestor CAS. Their admitted
// work is fenced by an ancestor lease too, so controlled rebind checks all of
// them. Zero-price reservations count as unresolved execution as well.
func taskLeaseBusy(ctx context.Context, tx pgx.Tx, ws int64, changes []TaskLeaseTarget, legacyOnly bool) ([]TaskLeaseIssue, error) {
	ids := []string{}
	for _, c := range changes {
		ids = append(ids, c.TaskID)
	}
	rows, e := tx.Query(ctx, `WITH RECURSIVE affected(id) AS (SELECT id FROM plume.ai_gateway_tasks WHERE workspace_id=$1 AND id=ANY($2) UNION SELECT t.id FROM plume.ai_gateway_tasks t JOIN affected a ON t.parent_task_id=a.id WHERE t.workspace_id=$1) SELECT s.task_id,s.step_id,'',s.status,0::bigint FROM plume.ai_gateway_task_steps s JOIN affected a ON s.task_id=a.id WHERE s.workspace_id=$1 AND s.status='admitted' AND (NOT $3::bool OR s.lease_chain_digest IS NULL OR s.lease_snapshot IS NULL) UNION ALL SELECT r.authorized->>'taskId',r.authorized->>'taskStepId',r.id,r.state,r.reserved_micros FROM plume.ai_gateway_reservations r JOIN affected a ON r.authorized->>'taskId'=a.id WHERE r.workspace_id=$1 AND r.state IN ('reserved','unknown') AND NOT $3::bool LIMIT 201`, ws, ids, legacyOnly)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []TaskLeaseIssue{}
	for rows.Next() {
		var v TaskLeaseIssue
		var amount int64
		if e = rows.Scan(&v.TaskID, &v.StepID, &v.ReservationID, &v.State, &amount); e != nil {
			return nil, e
		}
		v.Code = "inflight_unresolved"
		v.Message = "受影响子树存在未封存步骤或未决费用；保持步骤及占额，不能换绑。"
		v.ReservedMicros = &amount
		out = append(out, v)
	}
	return out, rows.Err()
}
func applyTaskLease(ctx context.Context, tx pgx.Tx, ws int64, id, operation, intent, mode, reason string, actor, credential int64, proposal *int64, chain []GatewayTask, changes []TaskLeaseTarget) (out TaskLeaseChange, err error) {
	out = TaskLeaseChange{OperationID: operation, Mode: mode, TaskID: id, ChangedTaskIDs: []string{}, Changes: []TaskLeaseApplied{}, Reason: reason, BeforeDigest: gatewayTaskLeaseDigest(chain), ProposalID: proposal}
	if actor > 0 {
		out.ActorID = &actor
	}
	if credential > 0 {
		out.ActorCredentialID = &credential
	}
	for _, target := range changes {
		for i := range chain {
			t := &chain[i]
			if t.ID != target.TaskID {
				continue
			}
			out.ChangedTaskIDs = append(out.ChangedTaskIDs, t.ID)
			out.Changes = append(out.Changes, TaskLeaseApplied{TaskID: t.ID, OldCredentialID: t.ExecutionLease.CredentialID, NewCredentialID: target.NewCredentialID, OldLeaseRevision: t.ExecutionLease.Revision, NewLeaseRevision: t.ExecutionLease.Revision + 1})
			_, err = tx.Exec(ctx, `UPDATE plume.ai_gateway_task_execution_leases SET credential_id=$3,revision=revision+1,mode=$4,updated_at=clock_timestamp() WHERE workspace_id=$1 AND task_id=$2`, ws, t.ID, target.NewCredentialID, mode)
			if err != nil {
				return
			}
			t.ExecutionLease.CredentialID = target.NewCredentialID
			t.ExecutionLease.Revision++
		}
	}
	out.AfterDigest = gatewayTaskLeaseDigest(chain)
	raw, _ := json.Marshal(out.Changes)
	err = tx.QueryRow(ctx, `INSERT INTO plume.ai_gateway_task_lease_changes(workspace_id,task_id,operation_id,intent_digest,mode,changed_task_ids,actor_id,actor_credential_id,proposal_id,reason,before_digest,after_digest,changes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id,created_at`, ws, id, operation, intent, mode, out.ChangedTaskIDs, out.ActorID, out.ActorCredentialID, proposal, reason, out.BeforeDigest, out.AfterDigest, raw).Scan(&out.ID, &out.CreatedAt)
	if err != nil {
		return
	}
	_, err = tx.Exec(ctx, `UPDATE plume.ai_gateway_task_lease_proposals SET status='invalidated',revision=revision+1,note='执行租约已切换，原CAS失效。' WHERE workspace_id=$1 AND status='proposed' AND path_ids && $2`, ws, out.ChangedTaskIDs)
	if err != nil {
		return
	}
	_, err = tx.Exec(ctx, `WITH RECURSIVE affected(id) AS (SELECT id FROM plume.ai_gateway_tasks WHERE workspace_id=$1 AND id=ANY($2) UNION SELECT t.id FROM plume.ai_gateway_tasks t JOIN affected a ON t.parent_task_id=a.id WHERE t.workspace_id=$1) UPDATE plume.ai_security_approvals SET status='invalidated' WHERE workspace_id=$1 AND status IN ('proposed','approved') AND request_metadata->>'taskId' IN (SELECT id FROM affected)`, ws, out.ChangedTaskIDs)
	if err != nil {
		return
	}
	err = identityAudit(ctx, tx, ws, actor, "gateway.task_execution_lease_changed", id, out)
	return
}
func (s *Store) RenewGatewayTaskCredential(ctx context.Context, slug, hash, id string, in TaskLeaseRenewalInput) (out TaskLeaseRenewalResult, err error) {
	changes := []TaskLeaseTarget{}
	for _, v := range in.ReplaceTaskIDs {
		changes = append(changes, TaskLeaseTarget{TaskID: v, NewCredentialID: 1})
	}
	if !slices.Contains(in.ReplaceTaskIDs, id) || !leaseInputValid(id, in.OperationID, in.ExpectedLeaseChainDigest, in.ExpectedChain, changes) {
		return out, ErrInvalid
	}
	intent := gatewayDigest(in)
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		ws, fresh, e := machineLeaseCredential(ctx, tx, slug, hash)
		if e != nil {
			return e
		}
		for i := range changes {
			changes[i].NewCredentialID = fresh.ID
		}
		chain, creds, e := lockTaskLeaseChange(ctx, tx, ws, id, in.ExpectedChain, changes)
		if e != nil {
			return e
		}
		// The fresh key is read again under its SHARE lock, after all earlier writers.
		fresh = creds[fresh.ID]
		var now time.Time
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		if !fresh.live(now) {
			return ErrForbidden
		}
		if _, e = fresh.identity(); e != nil {
			return e
		}
		prior, oldIntent, e := taskLeaseChangeByOperation(ctx, tx, ws, id, in.OperationID)
		if e == nil {
			if oldIntent != intent || prior.Mode != "renewal" || prior.ActorCredentialID == nil || *prior.ActorCredentialID != fresh.ID {
				return ErrConflict
			}
			current := creds[chain[len(chain)-1].ExecutionLease.CredentialID]
			if current.ID == 0 {
				var readErr error
				current, readErr = readTaskLeaseCredential(ctx, tx, ws, chain[len(chain)-1].ExecutionLease.CredentialID, false)
				if readErr != nil {
					return ErrForbidden
				}
			}
			a, identityErr := current.identity()
			b, freshIdentityErr := fresh.identity()
			if identityErr != nil || freshIdentityErr != nil || a != b || current.ApplicationID != fresh.ApplicationID {
				return ErrForbidden
			}
			out.Change = prior
			out.Replayed = true
			out.Lease, e = taskLeaseViewTx(ctx, tx, ws, 0, id)
			if e == nil {
				scrubMachineTaskLeaseView(&out.Lease)
			}
			return e
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if _, _, e = validateTaskLeaseChange(ctx, tx, ws, chain, creds, in.ExpectedLeaseChainDigest, in.ExpectedChain, changes, true, now); e != nil {
			return e
		}
		// Same-identity renewal fences every old epoch. Pending work is not discarded,
		// refunded, or rewritten. Its original receipt can be reconciled with a fresh
		// token before a new step is admitted.
		out.Change, e = applyTaskLease(ctx, tx, ws, id, in.OperationID, intent, "renewal", "同工作负载身份显式续期", 0, fresh.ID, nil, chain, changes)
		if e != nil {
			return e
		}
		out.Lease, e = taskLeaseViewTx(ctx, tx, ws, 0, id)
		if e == nil {
			scrubMachineTaskLeaseView(&out.Lease)
		}
		return e
	})
	return out, storeError(err)
}
func (s *Store) ProposeGatewayTaskCredentialBinding(ctx context.Context, ws, actor int64, id string, in TaskLeaseProposalInput) (out TaskLeaseProposal, err error) {
	if !leaseInputValid(id, in.OperationID, in.ExpectedLeaseChainDigest, in.ExpectedChain, in.Changes) || !leaseNote(in.Reason) {
		return out, ErrInvalid
	}
	intent := gatewayDigest(in)
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := gatewayRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		chain, creds, e := lockTaskLeaseChange(ctx, tx, ws, id, in.ExpectedChain, in.Changes)
		if e != nil {
			return e
		}
		prior, e := taskLeaseProposalByOperation(ctx, tx, ws, id, in.OperationID, actor)
		if e == nil {
			var digest string
			if e = tx.QueryRow(ctx, `SELECT intent_digest FROM plume.ai_gateway_task_lease_proposals WHERE workspace_id=$1 AND id=$2`, ws, prior.ID).Scan(&digest); e != nil {
				return e
			}
			if digest != intent {
				return ErrConflict
			}
			out = prior
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		var now time.Time
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		plan, expiry, e := validateTaskLeaseChange(ctx, tx, ws, chain, creds, in.ExpectedLeaseChainDigest, in.ExpectedChain, in.Changes, false, now)
		if e != nil {
			return e
		}
		if in.ExpiresAt != nil {
			if !in.ExpiresAt.After(now) || in.ExpiresAt.After(expiry) {
				return ErrInvalid
			}
			expiry = *in.ExpiresAt
		}
		if busy, e := taskLeaseBusy(ctx, tx, ws, in.Changes, false); e != nil {
			return e
		} else if len(busy) > 0 {
			return leaseError("inflight_unresolved", "受影响子树有未决执行，请先完成或凭真实终态回执核对。", ErrConflict, busy...)
		}
		expectedRaw, _ := json.Marshal(in.ExpectedChain)
		changesRaw, _ := json.Marshal(in.Changes)
		planRaw, _ := json.Marshal(plan)
		sources, targets := []int64{}, []int64{}
		paths := []string{}
		for _, t := range chain {
			paths = append(paths, t.ID)
			sources = append(sources, t.ExecutionLease.CredentialID)
		}
		for _, p := range plan {
			targets = append(targets, p.Target.ID)
		}
		digest := gatewayDigest(struct {
			Intent  string
			Plan    []TaskLeaseCredentialPlan
			Expires time.Time
		}{intent, plan, expiry})
		var proposal int64
		e = tx.QueryRow(ctx, `INSERT INTO plume.ai_gateway_task_lease_proposals(workspace_id,task_id,operation_id,intent_digest,expected_digest,expected_chain,changes,credential_plan,source_ids,target_ids,path_ids,reason,proposal_digest,proposed_by,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id`, ws, id, in.OperationID, intent, in.ExpectedLeaseChainDigest, expectedRaw, changesRaw, planRaw, sources, targets, paths, in.Reason, digest, actor, expiry).Scan(&proposal)
		if e != nil {
			return e
		}
		if e = identityAudit(ctx, tx, ws, actor, "gateway.task_credential_binding_proposed", id, map[string]any{"proposalId": proposal, "proposalDigest": digest, "credentialPlan": plan}); e != nil {
			return e
		}
		out, e = taskLeaseProposal(ctx, tx, ws, id, proposal, actor, "")
		return e
	})
	return out, storeError(err)
}
func (s *Store) ReviewGatewayTaskCredentialBinding(ctx context.Context, ws, actor int64, id string, proposal int64, in TaskLeaseReviewInput) (out TaskLeaseProposal, err error) {
	if !gatewayRuntimeID.MatchString(id) || proposal < 1 || in.ExpectedRevision < 1 || !slices.Contains([]string{"approve", "reject"}, in.Decision) || !leaseNote(in.Note) {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if role != "owner" && role != "reviewer" {
			return ErrForbidden
		}
		p, e := taskLeaseProposal(ctx, tx, ws, id, proposal, actor, "")
		if e != nil {
			return e
		}
		chain, creds, e := lockTaskLeaseChange(ctx, tx, ws, id, p.ExpectedChain, p.Changes)
		if e != nil {
			return e
		}
		p, e = taskLeaseProposal(ctx, tx, ws, id, proposal, actor, " FOR UPDATE")
		if e != nil {
			return e
		}
		var now time.Time
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		if p.ProposedBy == actor {
			return ErrForbidden
		}
		if p.Status != "proposed" || p.Revision != in.ExpectedRevision || !p.ExpiresAt.After(now) {
			return ErrConflict
		}
		if in.Decision == "reject" {
			_, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_task_lease_proposals SET status='rejected',revision=revision+1,reviewed_by=$3,note=$4 WHERE workspace_id=$1 AND id=$2`, ws, proposal, actor, in.Note)
			if e != nil {
				return e
			}
			out, e = taskLeaseProposal(ctx, tx, ws, id, proposal, actor, "")
			return e
		}
		plan, _, e := validateTaskLeaseChange(ctx, tx, ws, chain, creds, p.ExpectedLeaseChainDigest, p.ExpectedChain, p.Changes, false, now)
		if e != nil {
			return e
		}
		// Enabled/expiry are checked above against current facts. Frozen plan records
		// the actual reviewed names and identity, not a replacement for authority.
		for i := range plan {
			plan[i].Source.Enabled = p.CredentialPlan[i].Source.Enabled
			plan[i].Target.Enabled = p.CredentialPlan[i].Target.Enabled
		}
		if gatewayDigest(plan) != gatewayDigest(p.CredentialPlan) {
			return leaseError("credential_plan_changed", "来源或目标事实已变化，请重新提出换绑。", ErrConflict)
		}
		if busy, e := taskLeaseBusy(ctx, tx, ws, p.Changes, false); e != nil {
			return e
		} else if len(busy) > 0 {
			return leaseError("inflight_unresolved", "独立复核时仍有未决执行，未应用换绑。", ErrConflict, busy...)
		}
		// Mark this proposal applied before invalidating overlapping pending intents.
		_, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_task_lease_proposals SET status='applied',revision=revision+1,reviewed_by=$3,note=$4,applied_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, ws, proposal, actor, in.Note)
		if e != nil {
			return e
		}
		if _, e = applyTaskLease(ctx, tx, ws, id, p.OperationID, p.ProposalDigest, "rebind", p.Reason, actor, 0, &proposal, chain, p.Changes); e != nil {
			return e
		}
		out, e = taskLeaseProposal(ctx, tx, ws, id, proposal, actor, "")
		return e
	})
	return out, storeError(err)
}
