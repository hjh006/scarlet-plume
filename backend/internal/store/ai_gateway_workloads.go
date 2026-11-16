package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/workloadidentity"
	"github.com/jackc/pgx/v5"
)

type GatewayWorkloadInput struct {
	Name          string          `json:"name"`
	ApplicationID string          `json:"applicationId"`
	Issuer        string          `json:"issuer"`
	Audience      string          `json:"audience"`
	Subject       string          `json:"subject"`
	JWKS          json.RawMessage `json:"jwks"`
	MaxTTLSeconds int64           `json:"maxTtlSeconds"`
}

type GatewayWorkloadBinding struct {
	GatewayWorkloadInput
	ID        int64     `json:"id"`
	Revision  int64     `json:"revision"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type GatewayWorkloadPage struct {
	Items      []GatewayWorkloadBinding `json:"items"`
	HasMore    bool                     `json:"hasMore"`
	NextCursor string                   `json:"nextCursor"`
}

const gatewayWorkloadColumns = `id,name,application_id,issuer,audience,subject,jwks,max_ttl_seconds,enabled,revision,created_at,updated_at`

func scanGatewayWorkload(row pgx.Row) (out GatewayWorkloadBinding, err error) {
	err = row.Scan(&out.ID, &out.Name, &out.ApplicationID, &out.Issuer, &out.Audience, &out.Subject, &out.JWKS, &out.MaxTTLSeconds, &out.Enabled, &out.Revision, &out.CreatedAt, &out.UpdatedAt)
	return out, storeError(err)
}

func (in GatewayWorkloadInput) policy() workloadidentity.Policy {
	return workloadidentity.Policy{Issuer: in.Issuer, Audience: in.Audience, Subject: in.Subject, JWKS: in.JWKS, MaxTTLSeconds: in.MaxTTLSeconds}
}

func gatewayWorkloadLock(ctx context.Context, tx pgx.Tx, ws int64) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('scarlet-workload:'||$1::bigint::text,0))`, ws)
	return err
}

func gatewayWorkloadAudit(in GatewayWorkloadBinding) map[string]any {
	return map[string]any{"bindingId": in.ID, "revision": in.Revision, "applicationId": in.ApplicationID, "issuer": in.Issuer, "audience": in.Audience, "subject": in.Subject, "publicKeysDigest": gatewayDigest(in.JWKS), "maxTtlSeconds": in.MaxTTLSeconds, "enabled": in.Enabled}
}

func (s *Store) GatewayWorkloads(ctx context.Context, ws, actor int64, cursor string, limit int) (out GatewayWorkloadPage, err error) {
	if limit < 1 || limit > 200 {
		return out, ErrInvalid
	}
	scope := aiOperationsScope("gateway-workloads", ws)
	before, err := aiOperationsBefore(cursor, scope)
	if err != nil {
		return out, err
	}
	out.Items = []GatewayWorkloadBinding{}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT `+gatewayWorkloadColumns+` FROM plume.ai_gateway_workload_bindings WHERE workspace_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT $3`, ws, before, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			item, e := scanGatewayWorkload(rows)
			if e != nil {
				return e
			}
			if len(out.Items) == limit {
				out.HasMore = true
				break
			}
			out.Items = append(out.Items, item)
		}
		return rows.Err()
	})
	if err == nil && out.HasMore {
		out.NextCursor = aiOperationsNext(out.Items[len(out.Items)-1].ID, scope)
	}
	return out, storeError(err)
}

func (s *Store) SaveGatewayWorkload(ctx context.Context, ws, actor, id, expected int64, in GatewayWorkloadInput, enabled bool) (out GatewayWorkloadBinding, err error) {
	if id < 0 || id == 0 && (expected != 0 || !enabled) || id > 0 && expected < 1 || !gatewayText(in.Name, 128) || len(in.Name) > 128 || !gatewayRuntimeID.MatchString(in.ApplicationID) {
		return out, ErrInvalid
	}
	if _, e := workloadidentity.New(in.policy()); e != nil {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := gatewayRole(ctx, tx, ws, actor, true); e != nil {
			return e
		}
		if e := gatewayWorkloadLock(ctx, tx, ws); e != nil {
			return e
		}
		cfg, e := gatewayConfig(ctx, tx, ws, " FOR SHARE")
		if e != nil {
			return e
		}
		if _, ok := gatewayTaskApp(cfg, in.ApplicationID); !ok {
			return ErrNotFound
		}
		var total, active int
		if e = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE enabled AND id<>$2) FROM plume.ai_gateway_workload_bindings WHERE workspace_id=$1`, ws, id).Scan(&total, &active); e != nil {
			return e
		}
		if id == 0 && total >= 1000 || enabled && active >= 100 {
			return ErrConflict
		}
		if id == 0 {
			out, e = scanGatewayWorkload(tx.QueryRow(ctx, `INSERT INTO plume.ai_gateway_workload_bindings(workspace_id,name,application_id,issuer,audience,subject,jwks,max_ttl_seconds) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+gatewayWorkloadColumns, ws, in.Name, in.ApplicationID, in.Issuer, in.Audience, in.Subject, in.JWKS, in.MaxTTLSeconds))
		} else {
			old, readErr := scanGatewayWorkload(tx.QueryRow(ctx, `SELECT `+gatewayWorkloadColumns+` FROM plume.ai_gateway_workload_bindings WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id))
			if readErr != nil {
				return readErr
			}
			if old.Revision != expected || expected == 9223372036854775807 {
				return ErrConflict
			}
			out, e = scanGatewayWorkload(tx.QueryRow(ctx, `UPDATE plume.ai_gateway_workload_bindings SET name=$3,application_id=$4,issuer=$5,audience=$6,subject=$7,jwks=$8,max_ttl_seconds=$9,enabled=$10,revision=revision+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+gatewayWorkloadColumns, ws, id, in.Name, in.ApplicationID, in.Issuer, in.Audience, in.Subject, in.JWKS, in.MaxTTLSeconds, enabled))
			if e == nil {
				_, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_credentials SET enabled=false,revoked_at=COALESCE(revoked_at,clock_timestamp()),revocation_reason=COALESCE(revocation_reason,'binding_changed') WHERE workspace_id=$1 AND workload_binding_id=$2 AND (enabled OR revoked_at IS NULL)`, ws, id)
			}
		}
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_task_lease_proposals SET status='invalidated',revision=revision+1,note='工作负载信任绑定已变化。' WHERE workspace_id=$1 AND status='proposed' AND EXISTS(SELECT 1 FROM plume.ai_gateway_credentials c WHERE c.workspace_id=$1 AND c.workload_binding_id=$2 AND (c.id=ANY(source_ids) OR c.id=ANY(target_ids)))`, ws, id); e != nil {
			return e
		}
		return identityAudit(ctx, tx, ws, actor, "gateway.workload_binding_saved", fmt.Sprint(out.ID), gatewayWorkloadAudit(out))
	})
	return out, storeError(err)
}

func (s *Store) RevokeGatewayWorkload(ctx context.Context, ws, actor, id, expected int64) (out GatewayWorkloadBinding, err error) {
	if id < 1 || expected < 1 || expected == 9223372036854775807 {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := gatewayRole(ctx, tx, ws, actor, true); e != nil {
			return e
		}
		if e := gatewayWorkloadLock(ctx, tx, ws); e != nil {
			return e
		}
		old, e := scanGatewayWorkload(tx.QueryRow(ctx, `SELECT `+gatewayWorkloadColumns+` FROM plume.ai_gateway_workload_bindings WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id))
		if e != nil {
			return e
		}
		if old.Revision != expected {
			return ErrConflict
		}
		out, e = scanGatewayWorkload(tx.QueryRow(ctx, `UPDATE plume.ai_gateway_workload_bindings SET enabled=false,revision=revision+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 RETURNING `+gatewayWorkloadColumns, ws, id))
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_credentials SET enabled=false,revoked_at=COALESCE(revoked_at,clock_timestamp()),revocation_reason=COALESCE(revocation_reason,'binding_revoked') WHERE workspace_id=$1 AND workload_binding_id=$2 AND (enabled OR revoked_at IS NULL)`, ws, id); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_task_lease_proposals SET status='invalidated',revision=revision+1,note='工作负载信任绑定已变化。' WHERE workspace_id=$1 AND status='proposed' AND EXISTS(SELECT 1 FROM plume.ai_gateway_credentials c WHERE c.workspace_id=$1 AND c.workload_binding_id=$2 AND (c.id=ANY(source_ids) OR c.id=ANY(target_ids)))`, ws, id); e != nil {
			return e
		}
		return identityAudit(ctx, tx, ws, actor, "gateway.workload_binding_revoked", fmt.Sprint(out.ID), gatewayWorkloadAudit(out))
	})
	return out, storeError(err)
}
