package store

import (
	"context"
	"errors"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/jackc/pgx/v5"
)

// Cache delivery has no provider charge, but consumes the same shared request
// and conservative byte-token rate limits as provider admission.
func gatewayCacheTerminalRate(ctx context.Context, tx pgx.Tx, ws int64, app GatewayApplication, evidence *aisecurity.CacheEvidence) error {
	var dummy int64
	if e := tx.QueryRow(ctx, `SELECT reserved_micros FROM plume.ai_gateway_accounts WHERE workspace_id=$1 AND application_id=$2 FOR UPDATE`, ws, app.ID).Scan(&dummy); e != nil {
		return e
	}
	if app.RequestsPerMinute == 0 || app.TokensPerMinute == 0 {
		return ErrGatewayRate
	}
	tokens := max(int64(1), evidence.InputBytes+evidence.OutputBytes)
	window, e := gatewayNow(ctx, tx)
	if e != nil {
		return e
	}
	var requests, priorTokens int64
	e = tx.QueryRow(ctx, `SELECT requests,tokens FROM plume.ai_gateway_windows WHERE workspace_id=$1 AND application_id=$2 AND window_start=$3`, ws, app.ID, window).Scan(&requests, &priorTokens)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if requests >= app.RequestsPerMinute || priorTokens > app.TokensPerMinute-tokens {
		return ErrGatewayRate
	}
	_, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_windows(workspace_id,application_id,window_start,requests,tokens) VALUES($1,$2,$3,1,$4) ON CONFLICT(workspace_id,application_id,window_start) DO UPDATE SET requests=ai_gateway_windows.requests+1,tokens=ai_gateway_windows.tokens+EXCLUDED.tokens`, ws, app.ID, window, tokens)
	return e
}
