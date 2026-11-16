package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/jackc/pgx/v5"
)

func gatewayDeniedMetadata(a *aisecurity.AuditRecord) error {
	cacheHit := a != nil && a.Stage == "completed" && a.Decision.Outcome == "allow" && a.Execution.CacheHit && a.CacheEvidence != nil && a.CacheEvidence.CacheHit && a.Decision.Kind == "model"
	if a == nil || (!cacheHit && (a.Stage != "decision" || a.Decision.Outcome != "deny")) || !validGatewayRuntimeEvidence(*a) || a.Execution.Attempted || a.Execution.Outcome != "not_attempted" || a.Execution.HTTPStatus != 0 || !gatewayRuntimeID.MatchString(a.EventID) || !gatewayRuntimeID.MatchString(a.AttemptID) || !gatewayRuntimeID.MatchString(a.RequestID) || a.ObservedAt.IsZero() || a.ObservedAt.After(time.Now().Add(5*time.Minute)) {
		return ErrInvalid
	}
	d := a.Decision
	if !gatewayRuntimeID.MatchString(d.ApplicationID) || !gatewayRuntimeID.MatchString(d.PrincipalID) || !gatewayRuntimeID.MatchString(d.Kind) || !gatewayRuntimeID.MatchString(d.Code) || !gatewayRuntimeID.MatchString(d.CatalogVersion) || !gatewayHash.MatchString(d.RequestDigest) || !gatewayHash.MatchString(d.CatalogDigest) {
		return ErrInvalid
	}
	// Only bounded identifiers and digests cross this endpoint. No free-form
	// message, arguments, endpoint, request body or output field is accepted.
	for _, v := range []string{a.RuntimeProtocolVersion, a.InspectionVersion, a.MCPProtocolVersion} {
		if v != "" && !gatewayRuntimeID.MatchString(v) {
			return ErrInvalid
		}
	}
	for _, values := range [][]string{d.Rules, d.Categories, d.AllowedDocumentIDs} {
		if len(values) > 64 {
			return ErrInvalid
		}
		for _, v := range values {
			if !gatewayRuntimeID.MatchString(v) {
				return ErrInvalid
			}
		}
	}
	if len(d.Coverage) > 64 {
		return ErrInvalid
	}
	for _, value := range d.Coverage {
		// The actual engine binds its classification profile with this named
		// SHA-256 marker. Check the known prefix first so a short malformed
		// digest cannot fall through to the generic identifier grammar.
		if profile, ok := strings.CutPrefix(value, "versioned_classification_profile:"); ok {
			if !gatewayHash.MatchString(profile) {
				return ErrInvalid
			}
		} else if !gatewayRuntimeID.MatchString(value) {
			return ErrInvalid
		}
	}
	raw, e := json.Marshal(a)
	if e != nil || len(raw) > 32768 {
		return ErrInvalid
	}
	return nil
}

// RecordGatewayDenied records only a node-reported non-execution decision. Even
// verified metadata is never execution, authorization or provider-charge proof.
func (s *Store) RecordGatewayDenied(ctx context.Context, in GatewayDeniedEvent) (out GatewayDeniedReceipt, err error) {
	if !gatewayText(in.WorkspaceSlug, 128) {
		return out, ErrInvalid
	}
	if err = gatewayDeniedMetadata(in.Audit); err != nil {
		return
	}
	digest := gatewayDigest(in)
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var ws int64
		if e := tx.QueryRow(ctx, `SELECT id FROM plume.workspaces WHERE slug=$1`, in.WorkspaceSlug).Scan(&ws); e != nil {
			return storeError(e)
		}
		cfg, e := gatewayConfig(ctx, tx, ws, " FOR SHARE")
		if e != nil {
			return e
		}
		reasons := []string{}
		a := in.Audit
		if a.RuntimeProtocolVersion != aisecurity.RuntimeProtocolVersion {
			reasons = append(reasons, "runtime_version_unrecognized")
		}
		if a.Decision.MediaInspectionVersion != "" && a.Decision.MediaInspectionVersion != aisecurity.MediaInspectionVersion {
			reasons = append(reasons, "media_inspection_version_unrecognized")
		}
		if a.InspectionVersion != aisecurity.InspectionVersion {
			reasons = append(reasons, "inspection_version_unrecognized")
		}
		if a.MCPProtocolVersion != "" && a.MCPProtocolVersion != aisecurity.MCPVersion {
			reasons = append(reasons, "mcp_version_unrecognized")
		}
		if !cfg.Enabled {
			reasons = append(reasons, "gateway_not_currently_enabled")
		}
		if cfg.CatalogDigest != a.Decision.CatalogDigest {
			reasons = append(reasons, "catalog_not_current")
		}
		if !slices.Contains([]string{"model", "tool", "rag"}, a.Decision.Kind) {
			reasons = append(reasons, "request_kind_unrecognized")
		}
		if !slices.ContainsFunc(cfg.Applications, func(app GatewayApplication) bool {
			return app.ID == a.Decision.ApplicationID && app.PrincipalID == a.Decision.PrincipalID && app.Enabled
		}) {
			reasons = append(reasons, "identity_not_current")
		}

		if a.Execution.CacheHit {
			// A metadata replay must not resurrect a retired execution epoch. Keep the
			// global account -> task order before touching the idempotency event row.
			if in.TaskID != a.Decision.TaskID || in.LeaseChainDigest != a.Decision.LeaseChainDigest || in.LeaseProtocolVersion != a.Decision.LeaseProtocolVersion {
				return ErrForbidden
			}
			var credential string
			if e = tx.QueryRow(ctx, `SELECT application_id FROM plume.ai_gateway_credentials WHERE workspace_id=$1 AND token_hash=$2 AND enabled AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp()) FOR SHARE`, ws, in.TokenHash).Scan(&credential); e != nil {
				return ErrForbidden
			}
			if credential != a.Decision.ApplicationID {
				return ErrForbidden
			}
			var account int64
			if e = tx.QueryRow(ctx, `SELECT reserved_micros FROM plume.ai_gateway_accounts WHERE workspace_id=$1 AND application_id=$2 FOR UPDATE`, ws, credential).Scan(&account); e != nil {
				return e
			}
			fact := GatewayAuthorizeInput{TaskContext: in.TaskContext, WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash, ApplicationID: a.Decision.ApplicationID, PrincipalID: a.Decision.PrincipalID, CatalogDigest: a.Decision.CatalogDigest, Kind: "model", ModelID: in.ModelID, RequestID: a.RequestID, RequestDigest: a.Decision.RequestDigest, ParentRequestID: in.ParentRequestID, Attempt: in.Attempt}
			if e = gatewayTaskCacheCurrentStatus(ctx, tx, ws, fact, true); e != nil {
				return e
			}
		}
		out = GatewayDeniedReceipt{Accepted: true, Verified: len(reasons) == 0, VerificationReasons: reasons}
		raw, _ := json.Marshal(reasons)
		var inserted string
		e = tx.QueryRow(ctx, `INSERT INTO plume.ai_gateway_denied_events(workspace_id,event_id,event_hash,verified,verification_reasons) VALUES($1,$2,$3,$4,$5) ON CONFLICT(workspace_id,event_id) DO NOTHING RETURNING event_id`, ws, a.EventID, digest, out.Verified, raw).Scan(&inserted)
		if errors.Is(e, pgx.ErrNoRows) {
			var previous string
			if e = tx.QueryRow(ctx, `SELECT event_hash,verified,verification_reasons FROM plume.ai_gateway_denied_events WHERE workspace_id=$1 AND event_id=$2`, ws, a.EventID).Scan(&previous, &out.Verified, &raw); e != nil {
				return e
			}
			if previous != digest {
				return ErrConflict
			}
			if e = json.Unmarshal(raw, &out.VerificationReasons); e != nil {
				return e
			}
			out.Replayed = true
			return nil
		}
		if e != nil {
			return e
		}
		if a.Execution.CacheHit {
			if in.TaskID != a.Decision.TaskID || in.LeaseChainDigest != a.Decision.LeaseChainDigest || in.LeaseProtocolVersion != a.Decision.LeaseProtocolVersion {
				return ErrForbidden
			}
			if in.TaskID != "" {
				task, e := gatewayTask(ctx, tx, ws, in.TaskID)
				if e != nil {
					return e
				}
				if a.Decision.TaskPurpose != task.Purpose || a.Decision.TaskScopeDigest != task.ScopeDigest {
					return ErrForbidden
				}
			}
			if !out.Verified || !gatewayHash.MatchString(in.TokenHash) || in.ConfigRevision != cfg.Revision || !gatewayRuntimeID.MatchString(in.ModelID) || a.CacheEvidence.ModelID != in.ModelID {
				return ErrForbidden
			}
			var credentialApp string
			if e = tx.QueryRow(ctx, `SELECT application_id FROM plume.ai_gateway_credentials WHERE workspace_id=$1 AND token_hash=$2 AND enabled AND (expires_at IS NULL OR expires_at>clock_timestamp()) FOR SHARE`, ws, in.TokenHash).Scan(&credentialApp); e != nil {
				if errors.Is(e, pgx.ErrNoRows) {
					return ErrForbidden
				}
				return e
			}
			if credentialApp != a.Decision.ApplicationID {
				return ErrForbidden
			}
			allowed := false
			var cacheApp GatewayApplication
			for _, app := range cfg.Applications {
				if app.ID == credentialApp && app.PrincipalID == a.Decision.PrincipalID && app.Enabled && app.CachePolicy.Enabled && slices.Contains(app.ModelIDs, in.ModelID) {
					allowed = true
					cacheApp = app
					break
				}
			}
			if !allowed {
				return ErrForbidden
			}
			var provider GatewayProvider
			for _, p := range cfg.Providers {
				if p.Enabled && p.ModelID == in.ModelID {
					provider = p
					break
				}
			}
			if provider.ID == "" {
				return ErrForbidden
			}
			fact := GatewayAuthorizeInput{WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash, ApplicationID: a.Decision.ApplicationID, PrincipalID: a.Decision.PrincipalID, Kind: "model", RequestID: a.RequestID, RequestDigest: a.Decision.RequestDigest, CatalogDigest: a.Decision.CatalogDigest, ModelID: in.ModelID, ParentRequestID: in.ParentRequestID, RootModelID: in.RootModelID, PreviousReservationID: in.PreviousReservationID, Attempt: in.Attempt}
			fact.TaskContext = in.TaskContext
			if e = gatewayAuthorizeAttempt(ctx, tx, ws, fact, cfg, provider); e != nil {
				return e
			}
			if e = gatewayCacheTerminalRate(ctx, tx, ws, cacheApp, a.CacheEvidence); e != nil {
				return e
			}
			if e = gatewayTaskCacheCurrent(ctx, tx, ws, fact); e != nil {
				return e
			}
			if e = gatewayClaimRequest(ctx, tx, ws, fact, "cache"); e != nil {
				return e
			}
		}
		status := "verified_denial_metadata"
		if !out.Verified {
			status = "unverified_denial_metadata"
		}
		action := "gateway.call_denied"
		basis := "node_reported_deny_metadata_only"
		if a.Execution.CacheHit {
			action = "gateway.cache_completed"
			basis = "node_reported_cache_metadata_only"
			status = "reported_cache_hit"
		}
		return identityAudit(ctx, tx, ws, 0, action, a.EventID, map[string]any{"audit": a, "verified": out.Verified, "verificationReasons": reasons, "evidenceStatus": status, "verificationBasis": basis, "billingEffect": "none", "configRevision": cfg.Revision, "taskId": in.TaskID, "taskStepId": in.TaskStepID, "taskStepDigest": in.TaskStepDigest})
	})
	return
}
