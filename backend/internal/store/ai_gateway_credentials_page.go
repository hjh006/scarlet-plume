package store

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

const GatewayCredentialPageBoundary = "此列表展示凭据当前启用和到期状态，应用、主体、任务及运行时授权仍逐次核验。只返回凭据元数据，不返回令牌或令牌摘要；筛选页不是历史时间快照。"

type GatewayCredentialItem struct {
	GatewayCredential
	Status string `json:"status"`
}
type GatewayCredentialPage struct {
	Items      []GatewayCredentialItem `json:"items"`
	Limit      int                     `json:"limit"`
	HasMore    bool                    `json:"hasMore"`
	NextCursor string                  `json:"nextCursor"`
	Boundary   string                  `json:"boundary"`
}
type GatewayCredentialPreview struct {
	Returned int    `json:"returned"`
	Limit    int    `json:"limit"`
	HasMore  bool   `json:"hasMore"`
	Order    string `json:"order"`
	Boundary string `json:"boundary"`
}

func (s *Store) GatewayCredentials(ctx context.Context, ws, actor int64, before, state, applicationID string, limit int) (out GatewayCredentialPage, err error) {
	out = GatewayCredentialPage{Items: []GatewayCredentialItem{}, Limit: limit, Boundary: GatewayCredentialPageBoundary}
	if limit < 1 || limit > 200 || !slices.Contains([]string{"", "active", "expired", "disabled"}, state) || (applicationID != "" && !gatewayRuntimeID.MatchString(applicationID)) {
		return out, ErrInvalid
	}
	scope := aiOperationsScope("gateway-credentials", ws, state, applicationID)
	cursor, err := aiOperationsBefore(before, scope)
	if err != nil {
		return out, err
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		// Expired stays in the expired category after bounded exchange maintenance
		// marks it disabled. No token_hash column enters this projection.
		rows, e := tx.Query(ctx, `SELECT id,application_id,name,prefix,enabled AND (expires_at IS NULL OR expires_at>statement_timestamp()),created_at,expires_at,workload_binding_id,workload_binding_revision,revoked_at,COALESCE(revocation_reason,''),CASE WHEN expires_at<=statement_timestamp() THEN 'expired' WHEN NOT enabled THEN 'disabled' ELSE 'active' END FROM plume.ai_gateway_credentials WHERE workspace_id=$1 AND ($2::bigint=0 OR id<$2) AND ($3='' OR application_id=$3) AND ($4='' OR $4=CASE WHEN expires_at<=statement_timestamp() THEN 'expired' WHEN NOT enabled THEN 'disabled' ELSE 'active' END) ORDER BY id DESC LIMIT $5`, ws, cursor, applicationID, state, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v GatewayCredentialItem
			if e = rows.Scan(&v.ID, &v.ApplicationID, &v.Name, &v.Prefix, &v.Enabled, &v.CreatedAt, &v.ExpiresAt, &v.WorkloadBindingID, &v.WorkloadBindingRevision, &v.RevokedAt, &v.RevocationReason, &v.Status); e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(out.Items) > limit {
			out.HasMore = true
			out.Items = out.Items[:limit]
			out.NextCursor = aiOperationsNext(out.Items[len(out.Items)-1].ID, scope)
		}
		return nil
	})
	return out, storeError(err)
}
