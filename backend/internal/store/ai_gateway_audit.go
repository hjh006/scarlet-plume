package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"github.com/jackc/pgx/v5"
)

func gatewayAuditHash(e GatewayAuditEvent) string { return gatewaygovernance.HashEvent(e) }

// identityAudit appends a shared governance event inside the caller's transaction.
// The per-workspace head row serializes writers on every API and runtime node.
func identityAudit(ctx context.Context, tx pgx.Tx, ws, actor int64, action, target string, payload any) error {
	raw, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_audit_heads(workspace_id) VALUES($1) ON CONFLICT DO NOTHING`, ws); e != nil {
		return e
	}
	event := GatewayAuditEvent{WorkspaceID: ws, ActorID: actor, Action: action, Target: target, Payload: raw, OccurredAt: time.Now().UTC().Truncate(time.Microsecond)}
	if e = tx.QueryRow(ctx, `SELECT sequence,head_hash FROM plume.ai_gateway_audit_heads WHERE workspace_id=$1 FOR UPDATE`, ws).Scan(&event.Sequence, &event.PreviousHash); e != nil {
		return e
	}
	event.Sequence++
	event.Hash = gatewayAuditHash(event)
	if _, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_audit(workspace_id,sequence,actor_id,action,target,payload,occurred_at,previous_hash,hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, ws, event.Sequence, actor, action, target, string(raw), event.OccurredAt, event.PreviousHash, event.Hash); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_audit_heads SET sequence=$2,head_hash=$3 WHERE workspace_id=$1`, ws, event.Sequence, event.Hash)
	return e
}

func (s *Store) GatewayAudit(ctx context.Context, ws, actor, after int64, limit int) (GatewayAuditPage, error) {
	return s.gatewayAudit(ctx, ws, actor, after, limit, false)
}

func (s *Store) GatewayAuditMachine(ctx context.Context, slug string, after int64, limit int) (GatewayAuditPage, error) {
	var ws int64
	if e := s.pool.QueryRow(ctx, `SELECT id FROM plume.workspaces WHERE slug=$1`, slug).Scan(&ws); e != nil {
		return GatewayAuditPage{}, storeError(e)
	}
	return s.gatewayAudit(ctx, ws, 0, after, limit, true)
}

func (s *Store) gatewayAudit(ctx context.Context, ws, actor, after int64, limit int, machine bool) (out GatewayAuditPage, err error) {
	if after < 0 || limit < 1 || limit > 500 {
		return out, ErrInvalid
	}
	out.WorkspaceID = ws
	out.Items = []GatewayAuditEvent{}
	out.Valid = true
	out.NextCursor = after
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if !machine {
			if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
				return e
			}
		}
		if e := tx.QueryRow(ctx, `SELECT COALESCE((SELECT sequence FROM plume.ai_gateway_audit_heads WHERE workspace_id=$1),0),COALESCE((SELECT head_hash FROM plume.ai_gateway_audit_heads WHERE workspace_id=$1),'')`, ws).Scan(&out.HeadSequence, &out.HeadHash); e != nil {
			return e
		}
		if after > out.HeadSequence {
			return ErrInvalid
		}
		if after > 0 {
			if e := tx.QueryRow(ctx, `SELECT hash FROM plume.ai_gateway_audit WHERE workspace_id=$1 AND sequence=$2`, ws, after).Scan(&out.PreviousHash); e != nil {
				return e
			}
		}
		rows, e := tx.Query(ctx, `SELECT sequence,actor_id,action,target,payload,occurred_at,previous_hash,hash FROM plume.ai_gateway_audit WHERE workspace_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3`, ws, after, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		previous := out.PreviousHash
		for rows.Next() {
			event := GatewayAuditEvent{WorkspaceID: ws}
			var raw string
			if e = rows.Scan(&event.Sequence, &event.ActorID, &event.Action, &event.Target, &raw, &event.OccurredAt, &event.PreviousHash, &event.Hash); e != nil {
				return e
			}
			event.OccurredAt = event.OccurredAt.UTC()
			event.Payload = json.RawMessage(raw)
			if event.Sequence != out.NextCursor+1 || event.PreviousHash != previous || gatewayAuditHash(event) != event.Hash {
				out.Valid = false
			}
			out.NextCursor = event.Sequence
			previous = event.Hash
			out.Items = append(out.Items, event)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(out.Items) < limit && out.NextCursor < out.HeadSequence {
			out.Valid = false
		}
		if out.NextCursor == out.HeadSequence && previous != out.HeadHash {
			out.Valid = false
		}
		return nil
	})
	return
}

// Route selection is an authenticated projection over the complete chain page.
// Keeping all page events preserves independently verifiable sequence continuity.
func (s *Store) GatewayAuditRoute(ctx context.Context, ws, actor int64, after int64, limit int, providerID string) (out GatewayAuditPage, err error) {
	out, err = s.gatewayAudit(ctx, ws, actor, after, limit, false)
	if err != nil || providerID == "" {
		return
	}
	if !gatewayID.MatchString(providerID) {
		return out, ErrInvalid
	}
	out.ProviderID = providerID
	out.RouteSequences = []int64{}
	targets := make([]string, 0, len(out.Items))
	for _, event := range out.Items {
		targets = append(targets, event.Target)
	}
	rows, e := s.pool.Query(ctx, `SELECT id FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND authorized->>'providerId'=$2 AND id=ANY($3)`, ws, providerID, targets)
	if e != nil {
		return out, e
	}
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		ids[id] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, event := range out.Items {
		var payload struct {
			ProviderID string `json:"providerId"`
		}
		_ = json.Unmarshal(event.Payload, &payload)
		if ids[event.Target] || payload.ProviderID == providerID {
			out.RouteSequences = append(out.RouteSequences, event.Sequence)
		}
	}
	return
}
