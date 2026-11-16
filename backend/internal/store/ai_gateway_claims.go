package store

import (
	"context"
	"github.com/jackc/pgx/v5"
)

func gatewayClaimRequest(ctx context.Context, tx pgx.Tx, ws int64, in GatewayAuthorizeInput, source string) error {
	scope := in.ParentRequestID
	if scope == "" {
		scope = in.RequestID
	}
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::bigint::text||':'||$2,0))`, ws, scope); e != nil {
		return e
	}
	if in.ParentRequestID != "" {
		var terminal bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.ai_gateway_request_claims WHERE workspace_id=$1 AND parent_request_id=$2 AND source='cache')`, ws, in.ParentRequestID).Scan(&terminal); e != nil {
			return e
		}
		if terminal {
			return ErrConflict
		}
	}
	_, e := tx.Exec(ctx, `INSERT INTO plume.ai_gateway_request_claims(workspace_id,request_id,parent_request_id,attempt,application_id,principal_id,request_digest,source) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ws, in.RequestID, in.ParentRequestID, in.Attempt, in.ApplicationID, in.PrincipalID, in.RequestDigest, source)
	return storeError(e)
}
