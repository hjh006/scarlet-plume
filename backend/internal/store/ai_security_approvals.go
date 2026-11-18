package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"github.com/jackc/pgx/v5"
)

func (s *Store) RequestAISecurityApproval(ctx context.Context, ws, actor, expected int64, r aisecurity.Request) (out AISecurityApproval, err error) {
	if r.Kind != "tool" || r.Input != "" || r.Output != "" || r.ApprovalID != "" {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := aiEditor(ctx, tx, ws, actor); e != nil {
			return e
		}
		v, e := aiConfiguration(ctx, tx, ws, true)
		if e != nil {
			return e
		}
		if v.Revision != expected || expected < 1 {
			return ErrConflict
		}
		digest := aisecurity.ConfigDigest(v.Configuration)
		r, e = gatewayApprovalTaskContext(ctx, tx, ws, r, digest, nil)
		if e != nil {
			return e
		}
		var now time.Time
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		if !r.ExpiresAt.After(now) || r.ExpiresAt.After(now.Add(15*time.Minute)) {
			return ErrInvalid
		}
		decision := aisecurity.Evaluate(v.Configuration, r, now)
		if !decision.RequiresApproval {
			return ErrInvalid
		}
		if _, e = gatewayApprovalTaskContext(ctx, tx, ws, r, digest, decision.Categories); e != nil {
			return e
		}
		raw, e := json.Marshal(r)
		if e != nil {
			return e
		}
		e = tx.QueryRow(ctx, `INSERT INTO plume.ai_security_approvals(workspace_id,configuration_revision,catalog_digest,request_digest,request_metadata,requested_by,expires_at,task_lease_digest) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,'')) RETURNING id`, ws, v.Revision, decision.CatalogDigest, decision.RequestDigest, raw, actor, r.ExpiresAt, aisecurity.TaskLeaseDigest(r)).Scan(&out.ID)
		if e != nil {
			return e
		}
		out, e = aiApproval(ctx, tx, ws, out.ID)
		return e
	})
	return
}
func (s *Store) ReviewAISecurityApproval(ctx context.Context, ws, actor, id int64, status, note string) (out AISecurityApproval, err error) {
	if id < 1 || (status != "approved" && status != "rejected") || len(strings.TrimSpace(note)) < 1 || len(note) > 1000 {
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
		v, e := aiConfiguration(ctx, tx, ws, true)
		if e != nil {
			return e
		}
		var revision, requester int64
		if e = gatewayTaskLock(ctx, tx, ws); e != nil {
			return e
		}
		var current, digest, requestDigest, leaseDigest string
		var expires, now time.Time
		var requestRaw []byte
		e = tx.QueryRow(ctx, `SELECT configuration_revision,requested_by,status,catalog_digest,request_digest,expires_at,COALESCE(task_lease_digest,''),request_metadata FROM plume.ai_security_approvals WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id).Scan(&revision, &requester, &current, &digest, &requestDigest, &expires, &leaseDigest, &requestRaw)
		if e != nil {
			return storeError(e)
		}
		if requester == actor || v.UpdatedBy == actor {
			return ErrForbidden
		}
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		if current != "proposed" || revision != v.Revision || digest != aisecurity.ConfigDigest(v.Configuration) || !expires.After(now) {
			return ErrConflict
		}
		var request aisecurity.Request
		if json.Unmarshal(requestRaw, &request) != nil || aisecurity.RequestDigest(request) != requestDigest || !request.ExpiresAt.Truncate(time.Microsecond).Equal(expires) {
			return ErrForbidden
		}
		// Declining an unbound request grants no authority. Approval, and any
		// review bound to an existing task, must validate the original context.
		if status == "approved" || request.TaskID != "" {
			request, e = gatewayApprovalTaskContext(ctx, tx, ws, request, digest, nil)
			if e != nil {
				return e
			}
			if aisecurity.TaskLeaseDigest(request) != leaseDigest {
				return ErrForbidden
			}
			decision := aisecurity.Evaluate(v.Configuration, request, now)
			if !decision.RequiresApproval {
				return ErrForbidden
			}
			if _, e = gatewayApprovalTaskContext(ctx, tx, ws, request, digest, decision.Categories); e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `UPDATE plume.ai_security_approvals SET status=$3,reviewed_by=$4,note=$5,reviewed_at=now() WHERE workspace_id=$1 AND id=$2`, ws, id, status, actor, strings.TrimSpace(note))
		if e != nil {
			return e
		}
		out, e = aiApproval(ctx, tx, ws, id)
		return e
	})
	return
}

// Editors can inspect an already-issued task when requesting approval. The
// credential hash is loaded inside the trusted transaction, never returned.
// This does not admit a step or bypass runtime credential authentication.
func gatewayApprovalTaskContext(ctx context.Context, tx pgx.Tx, ws int64, r aisecurity.Request, catalog string, categories []string) (aisecurity.Request, error) {
	if r.TaskID == "" {
		// Proposal and review already lock the catalog row. Use the same
		// authority rules as execution, including optional handling deadlines.
		_, err := gatewayCurrentCatalogPolicy(ctx, tx, ws, gatewaygovernance.TaskCheckInput{
			CatalogDigest: catalog, ApplicationID: r.ApplicationID, PrincipalID: r.PrincipalID,
			Kind: r.Kind, ToolID: r.ToolID, Action: r.Action, Target: r.Target,
		}, false)
		return r, err
	}
	if !gatewayRuntimeID.MatchString(r.TaskID) {
		return r, ErrInvalid
	}
	task, e := gatewayTask(ctx, tx, ws, r.TaskID)
	if e != nil {
		return r, e
	}
	var slug, hash string
	if e = tx.QueryRow(ctx, `SELECT w.slug,c.token_hash FROM plume.workspaces w JOIN plume.ai_gateway_credentials c ON c.workspace_id=w.id WHERE w.id=$1 AND c.id=$2 AND c.enabled AND (c.expires_at IS NULL OR c.expires_at>clock_timestamp())`, ws, task.ExecutionLease.CredentialID).Scan(&slug, &hash); e != nil {
		return r, ErrForbidden
	}
	fact := gatewaygovernance.TaskCheckInput{TaskContext: gatewaygovernance.TaskContext{TaskID: r.TaskID, LeaseProtocolVersion: gatewaygovernance.TaskLeaseProtocolVersion, LeaseChainDigest: aisecurity.TaskLeaseDigest(r), TaskStepID: r.RequestID, TaskStepDigest: aisecurity.RequestDigest(r)}, WorkspaceSlug: slug, TokenHash: hash, ApplicationID: r.ApplicationID, PrincipalID: r.PrincipalID, CatalogDigest: catalog, Kind: r.Kind, ToolID: r.ToolID, Action: r.Action, Target: r.Target, DocumentIDs: r.DocumentIDs, Categories: categories}
	_, _, grant, e := gatewayCheckTask(ctx, tx, fact)
	if e != nil {
		return r, e
	}
	return aisecurity.ApplyTaskGrant(r, grant)
}
