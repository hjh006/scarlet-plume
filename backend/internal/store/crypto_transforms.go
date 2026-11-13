package store

import (
	"context"
	"encoding/json"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"github.com/hjh006/scarlet-plume/backend/pkg/cryptoevidence"
	"github.com/jackc/pgx/v5"
	"time"
)

type CryptoTransformInput struct {
	Kind    string                  `json:"kind"`
	Records []cryptoevidence.Record `json:"records"`
}
type TransformRecord cryptoevidence.Record
type CryptoTransformRelation struct {
	ID       int64  `json:"id"`
	SourceID int64  `json:"sourceId"`
	Source   string `json:"source"`
	Record   string `json:"record"`
	TransformRecord
	Current      bool   `json:"current"`
	EvidenceType string `json:"evidenceType"`
}

func (s *Store) RecordCryptoTransforms(ctx context.Context, tokenHash string, in CryptoTransformInput) (int, error) {
	if !validRuntimeToken(in.Kind, tokenHash) || len(in.Records) == 0 || len(in.Records) > 32 {
		return 0, ErrInvalid
	}
	for _, r := range in.Records {
		if !cryptoevidence.Valid(r) || r.ObservedAt.Before(time.Now().Add(-5*time.Minute)) || r.ObservedAt.After(time.Now().Add(time.Minute)) {
			return 0, ErrInvalid
		}
	}
	accepted := 0
	e := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ws, source int64
		e := tx.QueryRow(ctx, `SELECT src.workspace_id,src.id FROM plume.runtime_security_sources src JOIN plume.memberships m ON m.workspace_id=src.workspace_id AND m.user_id=src.actor_id WHERE src.token_digest=$1 AND src.kind=$2 AND src.revoked_at IS NULL AND m.role='owner' FOR NO KEY UPDATE OF src`, tokenHash, in.Kind).Scan(&ws, &source)
		if e != nil {
			return ErrAutomationUnauthorized
		}
		for _, r := range in.Records {
			r.ObservedAt = r.ObservedAt.UTC()
			var auditID int64
			e = tx.QueryRow(ctx, `SELECT id FROM plume.runtime_request_audit WHERE source_id=$1 AND observation->>'traceRef'=$2 AND observation->>'spanRef'=$3 AND observed_at BETWEEN $4::timestamptz-interval '5 minutes' AND $4::timestamptz+interval '5 minutes' ORDER BY id DESC LIMIT 1`, source, r.TraceRef, r.SpanRef, r.ObservedAt).Scan(&auditID)
			if e != nil {
				return ErrEvidenceRequired
			}
			raw, _ := json.Marshal(r)
			var previous []byte
			e = tx.QueryRow(ctx, `SELECT evidence FROM plume.crypto_transform_evidence WHERE source_id=$1 AND event_id=$2`, source, r.EventID).Scan(&previous)
			if e == nil {
				var stored cryptoevidence.Record
				if json.Unmarshal(previous, &stored) != nil || discoveryDigest(stored) != discoveryDigest(r) {
					return ErrConflict
				}
				accepted++
				continue
			}
			if e != pgx.ErrNoRows {
				return e
			}
			_, e = tx.Exec(ctx, `INSERT INTO plume.crypto_transform_evidence(workspace_id,source_id,audit_id,event_id,trace_ref,observed_at,input_key_id,input_scope,input_version,input_digest,output_key_id,output_scope,output_version,output_digest,evidence)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, ws, source, auditID, r.EventID, r.TraceRef, r.ObservedAt, r.Input.KeyID, r.Input.Scope, r.Input.Version, r.Input.Digest, r.Output.KeyID, r.Output.Scope, r.Output.Version, r.Output.Digest, raw)
			if e != nil {
				return e
			}
			accepted++
		}
		return nil
	})
	if e != nil {
		return 0, storeError(e)
	}
	return accepted, nil
}
func (s *Store) SearchCryptoTransformRelations(ctx context.Context, ws, actor int64, token valuefingerprint.Token) ([]CryptoTransformRelation, error) {
	out := []CryptoTransformRelation{}
	if !valuefingerprint.Valid(token) {
		return out, ErrInvalid
	}
	e := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, false)
		if e != nil {
			return e
		}
		if role != "owner" && role != "operator" && role != "reviewer" {
			return ErrForbidden
		}
		rows, e := tx.Query(ctx, `SELECT t.id,t.source_id,src.name,'runtime-audit:'||t.audit_id,t.evidence,src.revoked_at IS NULL AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=src.workspace_id AND m.user_id=src.actor_id AND m.role='owner') FROM plume.crypto_transform_evidence t JOIN plume.runtime_security_sources src ON src.id=t.source_id AND src.workspace_id=t.workspace_id WHERE t.workspace_id=$1 AND t.observed_at>now()-interval '7 days' AND EXISTS(SELECT 1 FROM plume.crypto_transform_evidence matched WHERE matched.workspace_id=t.workspace_id AND matched.source_id=t.source_id AND matched.trace_ref=t.trace_ref AND ((matched.input_key_id,matched.input_scope,matched.input_version,matched.input_digest)=($2,$3,$4,$5) OR (matched.output_key_id,matched.output_scope,matched.output_version,matched.output_digest)=($2,$3,$4,$5))) AND t.input_key_id=$2 AND t.input_scope=$3 ORDER BY t.id DESC LIMIT 200`, ws, token.KeyID, token.Scope, token.Version, token.Digest)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v := CryptoTransformRelation{EvidenceType: "customer_instrumented_transform"}
			var raw []byte
			if e = rows.Scan(&v.ID, &v.SourceID, &v.Source, &v.Record, &raw, &v.Current); e != nil {
				return e
			}
			if e = json.Unmarshal(raw, &v.TransformRecord); e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, e
}
