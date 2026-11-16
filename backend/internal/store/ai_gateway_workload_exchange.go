package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/workloadidentity"
	"github.com/jackc/pgx/v5"
)

var ErrWorkloadAssertion = errors.New("workload assertion rejected")

type GatewayWorkloadToken struct {
	Credential GatewayCredential `json:"credential"`
	Token      string            `json:"token"`
	ExpiresAt  time.Time         `json:"expiresAt"`
}

func (s *Store) ExchangeGatewayWorkloadToken(ctx context.Context, slug string, bindingID int64, assertion string) (out GatewayWorkloadToken, err error) {
	if !gatewayText(slug, 128) || bindingID < 1 || len(assertion) == 0 || len(assertion) > 12288 {
		return out, ErrInvalid
	}
	var ws int64
	if e := s.pool.QueryRow(ctx, `SELECT id FROM plume.workspaces WHERE slug=$1`, slug).Scan(&ws); e != nil {
		return out, ErrWorkloadAssertion
	}
	// Reject signatures outside the mutation transaction so invalid clients cannot
	// hold a workspace allocation lock while performing expensive verification.
	binding, err := scanGatewayWorkload(s.pool.QueryRow(ctx, `SELECT `+gatewayWorkloadColumns+` FROM plume.ai_gateway_workload_bindings WHERE workspace_id=$1 AND id=$2`, ws, bindingID))
	if err != nil || !binding.Enabled {
		return out, ErrWorkloadAssertion
	}
	verifier, err := workloadidentity.New(binding.policy())
	if err != nil {
		return out, ErrWorkloadAssertion
	}
	proof, err := verifier.Verify(assertion, time.Now().UTC())
	if err != nil {
		return out, ErrWorkloadAssertion
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return out, err
	}
	token := "sgw_" + hex.EncodeToString(random[:])
	hash := sha256.Sum256([]byte(token))
	issuerDigest := sha256.Sum256([]byte(proof.Issuer))
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := gatewayWorkloadLock(ctx, tx, ws); e != nil {
			return e
		}
		cfg, e := gatewayConfig(ctx, tx, ws, " FOR SHARE")
		if e != nil {
			return e
		}
		current, e := scanGatewayWorkload(tx.QueryRow(ctx, `SELECT `+gatewayWorkloadColumns+` FROM plume.ai_gateway_workload_bindings WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, bindingID))
		if e != nil || !current.Enabled || current.Revision != binding.Revision {
			return ErrWorkloadAssertion
		}
		if _, ok := gatewayTaskApp(cfg, current.ApplicationID); !cfg.Enabled || !ok {
			return ErrForbidden
		}
		var now time.Time
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		if !proof.ExpiresAt.After(now) || proof.IssuedAt.After(now.Add(30*time.Second)) {
			return ErrWorkloadAssertion
		}
		// One exchange performs bounded maintenance. Expired assertions cannot be
		// reused even after deletion because signature claims are checked first.
		if _, e = tx.Exec(ctx, `DELETE FROM plume.ai_gateway_workload_exchanges WHERE ctid IN (SELECT ctid FROM plume.ai_gateway_workload_exchanges WHERE workspace_id=$1 AND expires_at<=clock_timestamp() ORDER BY expires_at LIMIT 500)`, ws); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_credentials SET enabled=false WHERE id IN (SELECT id FROM plume.ai_gateway_credentials WHERE workspace_id=$1 AND enabled AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT 500)`, ws); e != nil {
			return e
		}
		var liveExchanges, liveCredentials int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_gateway_workload_exchanges WHERE workspace_id=$1 AND expires_at>clock_timestamp()`, ws).Scan(&liveExchanges); e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_gateway_credentials WHERE workspace_id=$1 AND enabled AND (expires_at IS NULL OR expires_at>clock_timestamp())`, ws).Scan(&liveCredentials); e != nil {
			return e
		}
		if liveExchanges >= 10000 || liveCredentials >= 1000 {
			return ErrConflict
		}
		// Keep the replay fence until the assertion's full expiry, even when the
		// issued credential has a shorter binding TTL. Revocation never clears it.
		tag, e := tx.Exec(ctx, `INSERT INTO plume.ai_gateway_workload_exchanges(workspace_id,binding_id,issuer_digest,jti_digest,expires_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, ws, bindingID, hex.EncodeToString(issuerDigest[:]), proof.JTIHash, proof.IssuedAt.Add(300*time.Second))
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		out.ExpiresAt = proof.ExpiresAt
		e = tx.QueryRow(ctx, `INSERT INTO plume.ai_gateway_credentials(workspace_id,application_id,name,prefix,token_hash,expires_at,workload_binding_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,application_id,name,prefix,enabled,created_at,expires_at,workload_binding_id`, ws, current.ApplicationID, current.Name, token[:12], hex.EncodeToString(hash[:]), out.ExpiresAt, bindingID).Scan(&out.Credential.ID, &out.Credential.ApplicationID, &out.Credential.Name, &out.Credential.Prefix, &out.Credential.Enabled, &out.Credential.CreatedAt, &out.Credential.ExpiresAt, &out.Credential.WorkloadBindingID)
		if e != nil {
			return e
		}
		metadata := gatewayWorkloadAudit(current)
		metadata["credentialId"], metadata["jtiDigest"], metadata["keyId"], metadata["algorithm"], metadata["expiresAt"] = out.Credential.ID, proof.JTIHash, proof.KeyID, proof.Algorithm, out.ExpiresAt
		return identityAudit(ctx, tx, ws, 0, "gateway.workload_token_exchanged", fmt.Sprint(current.ID), metadata)
	})
	if err == nil {
		out.Token = token
	}
	return out, storeError(err)
}
