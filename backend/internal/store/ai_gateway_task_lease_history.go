package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

const taskLeaseProposalColumns = `id,task_id,status,revision,operation_id,reason,note,proposed_by,reviewed_by,created_at,expires_at,applied_at,expected_digest,expected_chain,changes,proposal_digest,credential_plan`

func scanTaskLeaseProposal(row pgx.Row) (p TaskLeaseProposal, err error) {
	var chain, changes, plan []byte
	err = row.Scan(&p.ID, &p.TaskID, &p.Status, &p.Revision, &p.OperationID, &p.Reason, &p.Note, &p.ProposedBy, &p.ReviewedBy, &p.CreatedAt, &p.ExpiresAt, &p.AppliedAt, &p.ExpectedLeaseChainDigest, &chain, &changes, &p.ProposalDigest, &plan)
	if err != nil {
		return p, storeError(err)
	}
	if json.Unmarshal(chain, &p.ExpectedChain) != nil || json.Unmarshal(changes, &p.Changes) != nil || json.Unmarshal(plan, &p.CredentialPlan) != nil {
		return p, ErrInvalid
	}
	p.Blockers = []TaskLeaseIssue{}
	return
}
func taskLeaseProposal(ctx context.Context, tx pgx.Tx, ws int64, task string, id, actor int64, lock string) (p TaskLeaseProposal, err error) {
	p, err = scanTaskLeaseProposal(tx.QueryRow(ctx, `SELECT `+taskLeaseProposalColumns+` FROM plume.ai_gateway_task_lease_proposals WHERE workspace_id=$1 AND task_id=$2 AND id=$3`+lock, ws, task, id))
	if err != nil {
		return
	}
	role, e := checkRole(ctx, tx, ws, actor, false)
	if e != nil {
		return p, e
	}
	p.CanReview = (role == "owner" || role == "reviewer") && p.ProposedBy != actor && p.Status == "proposed" && p.ExpiresAt.After(time.Now())
	if p.Status == "proposed" && !p.ExpiresAt.After(time.Now()) {
		p.Blockers = append(p.Blockers, TaskLeaseIssue{Code: "proposal_expired", State: "blocked", Message: "申请有效期已到，请重新提出。"})
	}
	return
}
func taskLeaseProposalByOperation(ctx context.Context, tx pgx.Tx, ws int64, task, operation string, actor int64) (TaskLeaseProposal, error) {
	var id int64
	e := tx.QueryRow(ctx, `SELECT id FROM plume.ai_gateway_task_lease_proposals WHERE workspace_id=$1 AND task_id=$2 AND operation_id=$3`, ws, task, operation).Scan(&id)
	if e != nil {
		return TaskLeaseProposal{}, storeError(e)
	}
	return taskLeaseProposal(ctx, tx, ws, task, id, actor, "")
}

const taskLeaseChangeColumns = `id,operation_id,mode,task_id,changed_task_ids,actor_id,actor_credential_id,proposal_id,reason,before_digest,after_digest,changes,created_at`

func scanTaskLeaseChange(row pgx.Row) (c TaskLeaseChange, err error) {
	var raw []byte
	err = row.Scan(&c.ID, &c.OperationID, &c.Mode, &c.TaskID, &c.ChangedTaskIDs, &c.ActorID, &c.ActorCredentialID, &c.ProposalID, &c.Reason, &c.BeforeDigest, &c.AfterDigest, &raw, &c.CreatedAt)
	if err != nil {
		return c, storeError(err)
	}
	if json.Unmarshal(raw, &c.Changes) != nil {
		return c, ErrInvalid
	}
	return
}
func taskLeaseChangeByOperation(ctx context.Context, tx pgx.Tx, ws int64, task, operation string) (c TaskLeaseChange, intent string, err error) {
	c, err = scanTaskLeaseChange(tx.QueryRow(ctx, `SELECT `+taskLeaseChangeColumns+` FROM plume.ai_gateway_task_lease_changes WHERE workspace_id=$1 AND task_id=$2 AND operation_id=$3`, ws, task, operation))
	if err != nil {
		return
	}
	err = tx.QueryRow(ctx, `SELECT intent_digest FROM plume.ai_gateway_task_lease_changes WHERE workspace_id=$1 AND id=$2`, ws, c.ID).Scan(&intent)
	return
}
func (s *Store) GatewayTaskLeaseProposal(ctx context.Context, ws, actor int64, task string, id int64, operation string) (out TaskLeaseProposal, err error) {
	if !gatewayRuntimeID.MatchString(task) || (id < 1 && !gatewayRuntimeID.MatchString(operation)) {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		if id > 0 {
			out, e = taskLeaseProposal(ctx, tx, ws, task, id, actor, "")
		} else {
			out, e = taskLeaseProposalByOperation(ctx, tx, ws, task, operation, actor)
		}
		return e
	})
	return out, storeError(err)
}
func (s *Store) GatewayTaskLeaseProposals(ctx context.Context, ws, actor int64, task, cursor string, limit int) (out TaskLeaseProposalPage, err error) {
	if !gatewayRuntimeID.MatchString(task) || limit < 1 || limit > 200 {
		return out, ErrInvalid
	}
	scope := aiOperationsScope("task-lease-proposals", ws, task)
	before, e := aiOperationsBefore(cursor, scope)
	if e != nil {
		return out, e
	}
	out.Items = []TaskLeaseProposal{}
	out.Limit = limit
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, false)
		if e != nil {
			return e
		}
		if _, e = gatewayTask(ctx, tx, ws, task); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT `+taskLeaseProposalColumns+` FROM plume.ai_gateway_task_lease_proposals WHERE workspace_id=$1 AND task_id=$2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT $4`, ws, task, before, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			p, e := scanTaskLeaseProposal(rows)
			if e != nil {
				return e
			}
			if len(out.Items) == limit {
				out.HasMore = true
				break
			}
			p.CanReview = (role == "owner" || role == "reviewer") && p.ProposedBy != actor && p.Status == "proposed" && p.ExpiresAt.After(time.Now())
			out.Items = append(out.Items, p)
		}
		return rows.Err()
	})
	if err == nil && out.HasMore {
		out.NextCursor = aiOperationsNext(out.Items[len(out.Items)-1].ID, scope)
	}
	return out, storeError(err)
}
func (s *Store) GatewayTaskLeaseHistory(ctx context.Context, ws, actor int64, task, cursor string, limit int) (out TaskLeaseChangePage, err error) {
	if !gatewayRuntimeID.MatchString(task) || limit < 1 || limit > 200 {
		return out, ErrInvalid
	}
	scope := aiOperationsScope("task-lease-history", ws, task)
	before, e := aiOperationsBefore(cursor, scope)
	if e != nil {
		return out, e
	}
	out.Items = []TaskLeaseChange{}
	out.Limit = limit
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		chain, e := gatewayTaskChain(ctx, tx, ws, task)
		if e != nil {
			return e
		}
		path := []string{}
		for _, t := range chain {
			path = append(path, t.ID)
		}
		rows, e := tx.Query(ctx, `SELECT `+taskLeaseChangeColumns+` FROM plume.ai_gateway_task_lease_changes WHERE workspace_id=$1 AND changed_task_ids && $2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT $4`, ws, path, before, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			p, e := scanTaskLeaseChange(rows)
			if e != nil {
				return e
			}
			if len(out.Items) == limit {
				out.HasMore = true
				break
			}
			out.Items = append(out.Items, p)
		}
		return rows.Err()
	})
	if err == nil && out.HasMore {
		out.NextCursor = aiOperationsNext(out.Items[len(out.Items)-1].ID, scope)
	}
	return out, storeError(err)
}

// A historical receipt can only be completed after a sequence of independently
// recorded same-identity renewals. A controlled identity switch is never treated
// as approval to finish an older identity's work.
func taskLeaseHistoricalCompletion(ctx context.Context, tx pgx.Tx, ws int64, chain []GatewayTask, task, step, oldDigest string) error {
	var pinned string
	var raw []byte
	if e := tx.QueryRow(ctx, `SELECT COALESCE(lease_chain_digest,''),lease_snapshot FROM plume.ai_gateway_task_steps WHERE workspace_id=$1 AND task_id=$2 AND step_id=$3`, ws, task, step).Scan(&pinned, &raw); e != nil {
		return ErrForbidden
	}
	if pinned != oldDigest || !gatewayHash.MatchString(pinned) {
		return ErrForbidden
	}
	var old []TaskLeaseExpected
	if json.Unmarshal(raw, &old) != nil || len(old) != len(chain) {
		return ErrForbidden
	}
	for i, t := range chain {
		start := old[i]
		if start.TaskID != t.ID || start.TaskRevision != t.Revision || start.LeaseRevision > t.ExecutionLease.Revision {
			return ErrForbidden
		}
		revision, credential := start.LeaseRevision, start.CredentialID
		if revision == t.ExecutionLease.Revision {
			if credential != t.ExecutionLease.CredentialID {
				return ErrForbidden
			}
			continue
		}
		rows, e := tx.Query(ctx, `SELECT c.mode,x.item FROM plume.ai_gateway_task_lease_changes c CROSS JOIN LATERAL jsonb_array_elements(c.changes) x(item) WHERE c.workspace_id=$1 AND x.item->>'taskId'=$2 AND (x.item->>'newLeaseRevision')::bigint>$3 AND (x.item->>'newLeaseRevision')::bigint<=$4 ORDER BY (x.item->>'newLeaseRevision')::bigint LIMIT 1001`, ws, t.ID, revision, t.ExecutionLease.Revision)
		if e != nil {
			return e
		}
		count := 0
		for rows.Next() {
			var mode string
			var r []byte
			if e = rows.Scan(&mode, &r); e != nil {
				rows.Close()
				return e
			}
			var c TaskLeaseApplied
			if json.Unmarshal(r, &c) != nil || mode != "renewal" || c.OldLeaseRevision != revision || c.OldCredentialID != credential || c.NewLeaseRevision != revision+1 {
				rows.Close()
				return ErrForbidden
			}
			revision = c.NewLeaseRevision
			credential = c.NewCredentialID
			count++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if count > 1000 || revision != t.ExecutionLease.Revision || credential != t.ExecutionLease.CredentialID {
			return ErrForbidden
		}
	}
	return nil
}

func taskLeasePendingPreviousEpoch(ctx context.Context, tx pgx.Tx, ws int64, chain []GatewayTask, exceptStep string) error {
	for _, t := range chain {
		var step string
		e := tx.QueryRow(ctx, `WITH RECURSIVE affected(id) AS (SELECT id FROM plume.ai_gateway_tasks WHERE workspace_id=$1 AND id=$2 UNION SELECT q.id FROM plume.ai_gateway_tasks q JOIN affected a ON q.parent_task_id=a.id WHERE q.workspace_id=$1) SELECT s.step_id FROM plume.ai_gateway_task_steps s JOIN affected a ON s.task_id=a.id WHERE s.workspace_id=$1 AND s.status='admitted' AND s.step_id<>$3 AND (s.lease_chain_digest IS NULL OR s.lease_snapshot IS NULL OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(s.lease_snapshot) x WHERE x->>'taskId'=$2) OR EXISTS(SELECT 1 FROM jsonb_array_elements(s.lease_snapshot) x WHERE x->>'taskId'=$2 AND (x->>'leaseRevision')::bigint<>$4)) ORDER BY s.id LIMIT 1`, ws, t.ID, exceptStep, t.ExecutionLease.Revision).Scan(&step)
		if e == nil {
			return leaseError("terminal_evidence_required", "旧epoch步骤尚未完成；请用新有效凭据提交原始终态回执及来源，费用未知须先核对。", ErrConflict, TaskLeaseIssue{TaskID: t.ID, StepID: step, Code: "terminal_evidence_required", State: "blocked", Message: "保留原步骤和预算；不会把未知执行伪造为成功。"})
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
	}
	return nil
}
