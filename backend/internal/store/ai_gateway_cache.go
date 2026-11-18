package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"github.com/jackc/pgx/v5"
)

type GatewayCacheScope = gatewaygovernance.CacheScope
type GatewayCacheResult = gatewaygovernance.CacheResult
type GatewayCachePut = gatewaygovernance.CachePut

func gatewayCacheAEAD(p GatewayCachePolicy) (cipher.AEAD, error) {
	// The environment name and immutable key version are configuration. Key bytes
	// never enter JSON, database records, logs, audit exports or error messages.
	if !p.Enabled || !gatewayReference.MatchString(p.KeyRef) {
		return nil, ErrForbidden
	}
	key, e := base64.StdEncoding.DecodeString(os.Getenv(strings.TrimPrefix(p.KeyRef, "env:")))
	if e != nil || len(key) != 32 {
		return nil, ErrForbidden
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, ErrForbidden
	}
	return cipher.NewGCM(block)
}

func gatewayCacheScope(ctx context.Context, tx pgx.Tx, in GatewayCacheScope) (ws int64, p GatewayCachePolicy, scope string, err error) {
	if !gatewayText(in.WorkspaceSlug, 128) || !gatewayHash.MatchString(in.TokenHash) || !gatewayRuntimeID.MatchString(in.ApplicationID) || !gatewayRuntimeID.MatchString(in.PrincipalID) || !gatewayRuntimeID.MatchString(in.ModelID) || !gatewayHash.MatchString(in.CatalogDigest) || in.InspectionVersion != aisecurity.InspectionVersion || !gatewayHash.MatchString(in.InputHash) || !gatewayHash.MatchString(in.DocumentHash) || !gatewayHash.MatchString(in.PolicyHash) {
		return 0, p, "", ErrInvalid
	}
	var appID string
	if err = tx.QueryRow(ctx, `SELECT c.workspace_id,c.application_id FROM plume.ai_gateway_credentials c JOIN plume.workspaces w ON w.id=c.workspace_id WHERE w.slug=$1 AND c.token_hash=$2 AND c.enabled AND (c.expires_at IS NULL OR c.expires_at>clock_timestamp()) FOR SHARE OF c`, in.WorkspaceSlug, in.TokenHash).Scan(&ws, &appID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrForbidden
		}
		return
	}
	if _, err = gatewayCurrentCatalogPolicy(ctx, tx, ws, gatewaygovernance.TaskCheckInput{CatalogDigest: in.CatalogDigest, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, Kind: "model", ModelID: in.ModelID}, true); err != nil {
		return
	}
	c, e := gatewayConfig(ctx, tx, ws, " FOR SHARE")
	if e != nil {
		err = e
		return
	}
	if !c.Enabled || c.CatalogDigest != in.CatalogDigest || appID != in.ApplicationID {
		err = ErrForbidden
		return
	}
	var app *GatewayApplication
	for i := range c.Applications {
		if c.Applications[i].ID == appID {
			app = &c.Applications[i]
			break
		}
	}
	if app == nil || !app.Enabled || app.PrincipalID != in.PrincipalID || !slices.Contains(app.ModelIDs, in.ModelID) {
		err = ErrForbidden
		return
	}
	var providerID string
	for _, v := range c.Providers {
		if v.Enabled && v.ModelID == in.ModelID {
			providerID = v.ID
			break
		}
	}
	if providerID == "" {
		err = ErrForbidden
		return
	}
	if in.TaskID == "" && app.RequireTask {
		return ws, p, "", ErrForbidden
	}
	p = app.CachePolicy
	// Disabled policies are a cache miss, never an authorization bypass.
	scope = gatewayDigest(struct {
		WorkspaceID                                                                                                                                                                        int64
		ApplicationID, PrincipalID, CatalogDigest, ProviderID, ModelID, InspectionVersion, InputHash, DocumentHash, PolicyHash, KeyVersion, TaskID, LeaseChainDigest, LeaseProtocolVersion string
		Revision                                                                                                                                                                           int64
	}{ws, in.ApplicationID, in.PrincipalID, in.CatalogDigest, providerID, in.ModelID, in.InspectionVersion, in.InputHash, in.DocumentHash, in.PolicyHash, p.KeyVersion, in.TaskID, in.LeaseChainDigest, in.LeaseProtocolVersion, c.Revision})
	return
}

func (s *Store) GetGatewayCache(ctx context.Context, in GatewayCacheScope) (out GatewayCacheResult, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		ws, p, scope, e := gatewayCacheScope(ctx, tx, in)
		if e != nil {
			return e
		}
		if !p.Enabled {
			return nil
		}
		aead, e := gatewayCacheAEAD(p)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_accounts(workspace_id,application_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, ws, in.ApplicationID); e != nil {
			return e
		}
		var dummy int64
		if e = tx.QueryRow(ctx, `SELECT reserved_micros FROM plume.ai_gateway_accounts WHERE workspace_id=$1 AND application_id=$2 FOR UPDATE`, ws, in.ApplicationID).Scan(&dummy); e != nil {
			return e
		}
		if e = gatewayCacheTaskCurrent(ctx, tx, ws, in, false); e != nil {
			return e
		}
		if _, e = gatewayCurrentCatalogPolicy(ctx, tx, ws, gatewaygovernance.TaskCheckInput{CatalogDigest: in.CatalogDigest, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, Kind: "model", ModelID: in.ModelID}, true); e != nil {
			return e
		}
		var payload []byte
		e = tx.QueryRow(ctx, `UPDATE plume.ai_gateway_cache SET last_used_at=clock_timestamp() WHERE workspace_id=$1 AND application_id=$2 AND scope_hash=$3 AND key_version=$4 AND expires_at>clock_timestamp() RETURNING ciphertext`, ws, in.ApplicationID, scope, p.KeyVersion).Scan(&payload)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if len(payload) < aead.NonceSize() {
			return ErrConflict
		}
		plain, e := aead.Open(nil, payload[:aead.NonceSize()], payload[aead.NonceSize():], []byte(scope))
		if e != nil {
			return ErrConflict
		}
		if len(plain) == 0 || len(plain) > 1<<20 || !utf8.Valid(plain) {
			return ErrConflict
		}
		sum := sha256.Sum256(plain)
		out = GatewayCacheResult{Hit: true, Output: string(plain), OutputHash: hex.EncodeToString(sum[:])}
		return identityAudit(ctx, tx, ws, 0, "gateway.cache_hit", scope, map[string]any{"applicationId": in.ApplicationID, "modelId": in.ModelID, "inputHash": in.InputHash, "outputHash": out.OutputHash, "keyVersion": p.KeyVersion})
	})
	return
}

func (s *Store) PutGatewayCache(ctx context.Context, in GatewayCachePut) error {
	sum := sha256.Sum256([]byte(in.Output))
	if len(in.Output) == 0 || len(in.Output) > 1<<20 || !utf8.ValidString(in.Output) || len(in.ReservationID) != 32 || hex.EncodeToString(sum[:]) != in.OutputHash {
		return ErrInvalid
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		ws, p, scope, e := gatewayCacheScope(ctx, tx, in.CacheScope)
		if e != nil {
			return e
		}
		if !p.Enabled {
			return ErrForbidden
		}
		aead, e := gatewayCacheAEAD(p)
		if e != nil {
			return e
		}
		// Use the same account lock as admission/config writes; size and LRU limits
		// remain global across nodes rather than a best-effort process-local limit.
		var dummy int64
		if e = tx.QueryRow(ctx, `SELECT reserved_micros FROM plume.ai_gateway_accounts WHERE workspace_id=$1 AND application_id=$2 FOR UPDATE`, ws, in.ApplicationID).Scan(&dummy); e != nil {
			return e
		}
		if e = gatewayCacheTaskCurrent(ctx, tx, ws, in.CacheScope, true); e != nil {
			return e
		}
		if _, e = gatewayCurrentCatalogPolicy(ctx, tx, ws, gatewaygovernance.TaskCheckInput{CatalogDigest: in.CatalogDigest, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, Kind: "model", ModelID: in.ModelID}, true); e != nil {
			return e
		}
		var state, app string
		var raw, auth []byte
		if e = tx.QueryRow(ctx, `SELECT state,application_id,last_audit,authorized FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND id=$2`, ws, in.ReservationID).Scan(&state, &app, &raw, &auth); e != nil {
			return storeError(e)
		}
		var revision int64
		if e = tx.QueryRow(ctx, `SELECT revision FROM plume.ai_gateway_settings WHERE workspace_id=$1`, ws).Scan(&revision); e != nil {
			return e
		}
		var admission GatewayAuthorization
		if json.Unmarshal(auth, &admission) != nil || state != "settled" || app != in.ApplicationID || admission.ModelID != in.ModelID || admission.PrincipalID != in.PrincipalID || admission.CatalogDigest != in.CatalogDigest || admission.Kind != "model" || admission.ConfigRevision != revision || admission.TaskContext != in.TaskContext {
			return ErrForbidden
		}
		// Binding the checked output digest and complete scope prevents a successful
		// unrelated request from being used to poison another request's cache entry.
		var receipt struct {
			Stage     string `json:"stage"`
			Execution struct {
				Attempted bool   `json:"attempted"`
				Outcome   string `json:"outcome"`
			} `json:"execution"`
			Decision struct {
				Outcome              string `json:"outcome"`
				LeaseChainDigest     string `json:"leaseChainDigest"`
				LeaseProtocolVersion string `json:"leaseProtocolVersion"`
			} `json:"decision"`
			CacheEvidence *aisecurity.CacheEvidence `json:"cacheEvidence"`
		}
		if json.Unmarshal(raw, &receipt) != nil || receipt.Stage != "completed" || !receipt.Execution.Attempted || receipt.Execution.Outcome != "succeeded" || receipt.Decision.Outcome != "allow" || receipt.CacheEvidence == nil || receipt.Decision.LeaseChainDigest != admission.LeaseChainDigest || receipt.Decision.LeaseProtocolVersion != admission.LeaseProtocolVersion {
			return ErrForbidden
		}
		evidence := receipt.CacheEvidence
		if evidence.OutputBytes != int64(len(in.Output)) || evidence.ModelID != in.ModelID || evidence.CacheHit || evidence.InputHash != in.InputHash || evidence.DocumentHash != in.DocumentHash || evidence.PolicyHash != in.PolicyHash || evidence.OutputHash != in.OutputHash || evidence.InspectionVersion != in.InspectionVersion {
			return ErrForbidden
		}
		nonce := make([]byte, aead.NonceSize())
		if _, e = rand.Read(nonce); e != nil {
			return e
		}
		encrypted := aead.Seal(nonce, nonce, []byte(in.Output), []byte(scope))
		if int64(len(encrypted)) > p.MaxBytes {
			return ErrInvalid
		}
		if _, e = tx.Exec(ctx, `DELETE FROM plume.ai_gateway_cache WHERE workspace_id=$1 AND application_id=$2 AND (expires_at<=clock_timestamp() OR key_version<>$3)`, ws, in.ApplicationID, p.KeyVersion); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_cache(workspace_id,application_id,scope_hash,key_version,ciphertext,size_bytes,expires_at) VALUES($1,$2,$3,$4,$5,$6,clock_timestamp()+$7*interval '1 second') ON CONFLICT(workspace_id,application_id,scope_hash) DO UPDATE SET key_version=EXCLUDED.key_version,ciphertext=EXCLUDED.ciphertext,size_bytes=EXCLUDED.size_bytes,expires_at=EXCLUDED.expires_at,last_used_at=clock_timestamp()`, ws, in.ApplicationID, scope, p.KeyVersion, encrypted, len(encrypted), p.TTLSeconds); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM plume.ai_gateway_cache WHERE workspace_id=$1 AND application_id=$2 AND scope_hash IN (SELECT scope_hash FROM (SELECT scope_hash,row_number() OVER (ORDER BY last_used_at DESC,scope_hash) n,sum(size_bytes) OVER (ORDER BY last_used_at DESC,scope_hash) total FROM plume.ai_gateway_cache WHERE workspace_id=$1 AND application_id=$2) ranked WHERE n>$3 OR total>$4)`, ws, in.ApplicationID, p.MaxEntries, p.MaxBytes); e != nil {
			return e
		}
		return identityAudit(ctx, tx, ws, 0, "gateway.cache_stored", scope, map[string]any{"applicationId": in.ApplicationID, "modelId": in.ModelID, "outputHash": in.OutputHash, "keyVersion": p.KeyVersion, "ttlSeconds": p.TTLSeconds})
	})
}

// PruneExpiredGatewayCache removes a bounded batch of expired ciphertext,
// even for idle applications. The database clocks both expiry and reclamation.
func (s *Store) PruneExpiredGatewayCache(ctx context.Context) (int64, error) {
	result, err := s.pool.Exec(ctx, `
		WITH expired AS (
			SELECT workspace_id,application_id,scope_hash FROM plume.ai_gateway_cache
			WHERE expires_at<=statement_timestamp()
			ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED
		)
		DELETE FROM plume.ai_gateway_cache AS cached USING expired
		WHERE (cached.workspace_id,cached.application_id,cached.scope_hash)=
		      (expired.workspace_id,expired.application_id,expired.scope_hash)`)
	return result.RowsAffected(), err
}

func (s *Store) InvalidateGatewayCache(ctx context.Context, ws, actor int64, app string) (count int64, err error) {
	if app != "" && !gatewayRuntimeID.MatchString(app) {
		return 0, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := gatewayRole(ctx, tx, ws, actor, true); e != nil {
			return e
		}
		// Configuration lock orders invalidation with cache readers and writers.
		if _, e := gatewayConfig(ctx, tx, ws, " FOR UPDATE"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE plume.ai_gateway_settings SET revision=revision+1,updated_at=clock_timestamp() WHERE workspace_id=$1`, ws); e != nil {
			return e
		}
		result, e := tx.Exec(ctx, `DELETE FROM plume.ai_gateway_cache WHERE workspace_id=$1 AND ($2='' OR application_id=$2)`, ws, app)
		if e != nil {
			return e
		}
		count = result.RowsAffected()
		return identityAudit(ctx, tx, ws, actor, "gateway.cache_invalidated", app, map[string]any{"invalidated": count})
	})
	return
}

func gatewayCacheTaskCurrent(ctx context.Context, tx pgx.Tx, ws int64, in GatewayCacheScope, allowCompleted bool) error {
	if in.TaskID == "" {
		return nil
	}
	fact := GatewayAuthorizeInput{TaskContext: in.TaskContext, WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, CatalogDigest: in.CatalogDigest, Kind: "model", ModelID: in.ModelID, RequestID: in.TaskStepID, RequestDigest: in.TaskStepDigest}
	return gatewayTaskCacheCurrentStatus(ctx, tx, ws, fact, allowCompleted)
}
