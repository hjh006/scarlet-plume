package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Prices and token bounds are validated so these products cannot overflow int64.
func gatewayCost(tokens, price int64) int64 {
	whole := tokens * (price / 1_000_000)
	remainder := tokens * (price % 1_000_000)
	return whole + (remainder+999_999)/1_000_000
}

func gatewayDigest(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func (s *Store) ResolveGateway(ctx context.Context, in GatewayResolveInput) (out GatewayIdentity, err error) {
	if !gatewayText(in.WorkspaceSlug, 128) || !gatewayHash.MatchString(in.TokenHash) {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var ws int64
		if e := tx.QueryRow(ctx, `SELECT c.workspace_id,c.id,c.application_id FROM plume.ai_gateway_credentials c JOIN plume.workspaces w ON w.id=c.workspace_id WHERE w.slug=$1 AND c.token_hash=$2 AND c.enabled`, in.WorkspaceSlug, in.TokenHash).Scan(&ws, &out.CredentialID, &out.ApplicationID); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return ErrForbidden
			}
			return e
		}
		cfg, e := gatewayConfig(ctx, tx, ws, "")
		if e != nil {
			return e
		}
		if !cfg.Enabled {
			return ErrForbidden
		}
		for _, a := range cfg.Applications {
			if a.ID == out.ApplicationID && a.Enabled {
				out.PrincipalID = a.PrincipalID
				out.RequireTask = a.RequireTask
				out.ConfigRevision = cfg.Revision
				out.ToolIDs = append([]string{}, a.ToolIDs...)
				out.CatalogDigest = cfg.CatalogDigest
				out.Fallbacks = map[string][]string{}
				out.OpenModelIDs = []string{}
				healthRows, healthErr := tx.Query(ctx, `SELECT provider_id FROM plume.ai_gateway_route_health WHERE workspace_id=$1 AND open_until>clock_timestamp()`, ws)
				if healthErr != nil {
					return healthErr
				}
				openProviders := map[string]bool{}
				for healthRows.Next() {
					var id string
					if healthErr = healthRows.Scan(&id); healthErr != nil {
						healthRows.Close()
						return healthErr
					}
					openProviders[id] = true
				}
				healthErr = healthRows.Err()
				healthRows.Close()
				if healthErr != nil {
					return healthErr
				}
				for _, p := range cfg.Providers {
					if p.Enabled && slices.Contains(a.ModelIDs, p.ModelID) {
						out.Fallbacks[p.ModelID] = append([]string{}, p.FallbackModelIDs...)
						if openProviders[p.ID] {
							out.OpenModelIDs = append(out.OpenModelIDs, p.ModelID)
						}
					}
				}
				return nil
			}
		}
		return ErrForbidden
	})
	return
}

func (s *Store) AuthorizeGateway(ctx context.Context, in GatewayAuthorizeInput) (out GatewayAuthorization, err error) {
	// Validate each operand before addition; the control plane never trusts caller prices or usage.
	if !gatewayTaskMetadata(aisecurity.Decision{SourceDigests: in.SourceDigests}) {
		return out, ErrInvalid
	}
	if !gatewayText(in.WorkspaceSlug, 128) || !gatewayHash.MatchString(in.TokenHash) || !gatewayRuntimeID.MatchString(in.ApplicationID) || !gatewayRuntimeID.MatchString(in.PrincipalID) || !gatewayText(in.RequestID, 200) || !gatewayHash.MatchString(in.RequestDigest) || !gatewayHash.MatchString(in.CatalogDigest) || !slices.Contains([]string{"model", "tool", "rag"}, in.Kind) || in.InputBytes < 0 || in.InputBytes > 16_777_216 || in.ProjectionBytes < 0 || in.ProjectionBytes > 1_048_576 || in.MediaBytes < 0 || in.MediaBytes > 8_388_608 || !gatewayMediaMetadata(in.MediaDigests, in.MediaInspectionVersion) || in.MaxOutputTokens < 0 || in.MaxOutputTokens > 1_000_000 || len(in.Target) > 2048 {
		return out, ErrInvalid
	}
	if in.Kind == "model" && (!gatewayRuntimeID.MatchString(in.ModelID) || in.MaxOutputTokens == 0) || in.Kind == "tool" && (!gatewayRuntimeID.MatchString(in.ToolID) || !gatewayRuntimeID.MatchString(in.Action)) {
		return out, ErrInvalid
	}
	if in.InputBytes > 1_048_576 && len(in.MediaDigests) == 0 && len(in.SourceDigests) == 0 || len(in.MediaDigests) > 0 && in.MediaInspectionVersion != "scarlet-media-ocr-asr-v1" || len(in.MediaDigests) == 0 && (in.MediaBytes != 0 || in.ProjectionBytes != 0) {
		return out, ErrInvalid
	}
	requestHash := gatewayDigest(in)
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var ws, credID int64
		var appID string
		// A credential can only select its own workspace and application. Revoke waits for in-flight admission.
		e := tx.QueryRow(ctx, `SELECT c.workspace_id,c.id,c.application_id FROM plume.ai_gateway_credentials c JOIN plume.workspaces w ON w.id=c.workspace_id WHERE w.slug=$1 AND c.token_hash=$2 AND c.enabled FOR SHARE OF c`, in.WorkspaceSlug, in.TokenHash).Scan(&ws, &credID, &appID)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if e != nil {
			return e
		}
		c, e := gatewayConfig(ctx, tx, ws, " FOR SHARE")
		if e != nil {
			return e
		}
		if !c.Enabled || c.CatalogDigest != in.CatalogDigest {
			return ErrForbidden
		}
		var app *GatewayApplication
		for i := range c.Applications {
			if c.Applications[i].ID == appID {
				app = &c.Applications[i]
				break
			}
		}
		if app == nil || !app.Enabled || app.ID != in.ApplicationID || app.PrincipalID != in.PrincipalID {
			return ErrForbidden
		}
		if in.Kind == "model" && !slices.Contains(app.ModelIDs, in.ModelID) || in.Kind == "tool" && !slices.Contains(app.ToolIDs, in.ToolID) || in.Kind == "rag" && !app.AllowRag {
			return ErrForbidden
		}
		var provider GatewayProvider
		if in.Kind == "model" {
			found := false
			for _, p := range c.Providers {
				if p.ModelID == in.ModelID && p.Enabled {
					provider = p
					found = true
					break
				}
			}
			if !found {
				return ErrForbidden
			}
		}
		var reserved, spent int64
		if e = tx.QueryRow(ctx, `SELECT reserved_micros,spent_micros FROM plume.ai_gateway_accounts WHERE workspace_id=$1 AND application_id=$2 FOR UPDATE`, ws, appID).Scan(&reserved, &spent); e != nil {
			return e
		}
		var priorHash string
		var prior []byte
		e = tx.QueryRow(ctx, `SELECT request_hash,authorized FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND request_id=$2`, ws, in.RequestID).Scan(&priorHash, &prior)
		if e == nil {
			if priorHash != requestHash {
				return ErrConflict
			}
			if e = json.Unmarshal(prior, &out); e != nil {
				return e
			}
			out.Replayed = true
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e = gatewayAuthorizeAttempt(ctx, tx, ws, in, c, provider); e != nil {
			return e
		}
		if e = gatewayClaimRequest(ctx, tx, ws, in, "provider"); e != nil {
			return e
		}
		// Conservative governance quote, not a claim about provider usage or billing.
		// One input byte counts as one token and the full configured output bound is reserved.
		tokens := max(int64(1), in.InputBytes+in.ProjectionBytes+in.MaxOutputTokens)
		cost := int64(0)
		if in.Kind == "model" {
			cost = gatewayCost(in.InputBytes+in.ProjectionBytes, provider.InputMicrosPerMillion) + gatewayCost(in.MaxOutputTokens, provider.OutputMicrosPerMillion)
			if app.BudgetMicros == 0 || spent+reserved > app.BudgetMicros-cost {
				return ErrGatewayBudget
			}
		}
		if app.RequestsPerMinute == 0 || app.TokensPerMinute == 0 {
			return ErrGatewayRate
		}
		if e = gatewayTaskReserve(ctx, tx, ws, in, cost); e != nil {
			return e
		}
		window, e := gatewayNow(ctx, tx)
		if e != nil {
			return e
		}
		var requests, windowTokens int64
		e = tx.QueryRow(ctx, `SELECT requests,tokens FROM plume.ai_gateway_windows WHERE workspace_id=$1 AND application_id=$2 AND window_start=$3`, ws, appID, window).Scan(&requests, &windowTokens)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if requests >= app.RequestsPerMinute || windowTokens > app.TokensPerMinute-tokens {
			return ErrGatewayRate
		}
		id, e := gatewayRandomID()
		if e != nil {
			return e
		}
		out = GatewayAuthorization{ProjectionBytes: in.ProjectionBytes, MediaBytes: in.MediaBytes, MediaDigests: in.MediaDigests, MediaInspectionVersion: in.MediaInspectionVersion, ParentRequestID: in.ParentRequestID, RootModelID: in.RootModelID, PreviousReservationID: in.PreviousReservationID, Attempt: in.Attempt, FallbackModelIDs: append([]string{}, provider.FallbackModelIDs...), ConfigRevision: c.Revision, RequestID: in.RequestID, RequestDigest: in.RequestDigest, CatalogDigest: in.CatalogDigest, Kind: in.Kind, CredentialID: credID, ReservationID: id, ApplicationID: appID, PrincipalID: app.PrincipalID, ModelID: in.ModelID, ProviderID: provider.ID, Provider: provider.Provider, UpstreamModel: provider.UpstreamModel, EndpointRef: provider.EndpointRef, SecretRef: provider.SecretRef, Region: provider.Region, ResponseFormat: provider.ResponseFormat, MaxCostMicros: cost, MaxTokens: tokens}
		out.TaskContext = in.TaskContext
		out.SourceDigests = append([]string{}, in.SourceDigests...)
		raw, e := json.Marshal(out)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_reservations(id,workspace_id,application_id,credential_id,request_id,request_hash,model_id,reserved_micros,reserved_tokens,input_price,output_price,authorized,window_start,parent_request_id,root_model_id,attempt,previous_reservation_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, id, ws, appID, credID, in.RequestID, requestHash, in.ModelID, cost, tokens, provider.InputMicrosPerMillion, provider.OutputMicrosPerMillion, raw, window, in.ParentRequestID, in.RootModelID, in.Attempt, in.PreviousReservationID)
		if e != nil {
			return storeError(e)
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_accounts SET reserved_micros=reserved_micros+$3 WHERE workspace_id=$1 AND application_id=$2`, ws, appID, cost); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_windows(workspace_id,application_id,window_start,requests,tokens) VALUES($1,$2,$3,1,$4) ON CONFLICT(workspace_id,application_id,window_start) DO UPDATE SET requests=ai_gateway_windows.requests+1,tokens=ai_gateway_windows.tokens+EXCLUDED.tokens`, ws, appID, window, tokens); e != nil {
			return e
		}
		return identityAudit(ctx, tx, ws, 0, "gateway.call_reserved", id, map[string]any{"requestId": in.RequestID, "requestDigest": in.RequestDigest, "catalogDigest": in.CatalogDigest, "applicationId": appID, "credentialId": credID, "modelId": in.ModelID, "toolId": in.ToolID, "kind": in.Kind, "reservedMicros": cost, "reservedTokens": tokens, "quoteBasis": "bounded_input_and_projection_bytes_and_max_output_tokens", "inputBytes": in.InputBytes, "projectionBytes": in.ProjectionBytes, "mediaBytes": in.MediaBytes, "mediaDigests": in.MediaDigests, "mediaInspectionVersion": in.MediaInspectionVersion, "sourceDigests": in.SourceDigests, "providerId": provider.ID, "configRevision": c.Revision, "parentRequestId": in.ParentRequestID, "rootModelId": in.RootModelID, "attempt": in.Attempt, "previousReservationId": in.PreviousReservationID})
	})
	return
}

func (s *Store) SettleGateway(ctx context.Context, in GatewaySettlement) error {
	if !gatewayText(in.WorkspaceSlug, 128) || len(in.ReservationID) != 32 || !gatewayHash.MatchString(in.RequestDigest) || in.Audit == nil {
		return ErrInvalid
	}
	audit := in.Audit
	if !validGatewayRuntimeEvidence(*audit) {
		return ErrInvalid
	}
	raw, e := json.Marshal(audit)
	if e != nil || len(raw) > 65536 || !gatewayText(audit.EventID, 200) || audit.ObservedAt.IsZero() || audit.ObservedAt.After(time.Now().Add(5*time.Minute)) || !gatewayText(audit.AttemptID, 200) || audit.RequestID == "" || audit.Decision.RequestDigest != in.RequestDigest {
		return ErrInvalid
	}
	if audit.Stage != "completed" && !(audit.Stage == "decision" && audit.Decision.Outcome != "allow") {
		return ErrInvalid
	}
	if !slices.Contains([]string{"allow", "deny"}, audit.Decision.Outcome) || !audit.Execution.Attempted && audit.Execution.Outcome != "not_attempted" || audit.Execution.Attempted && audit.Execution.Outcome == "not_attempted" || audit.Stage == "decision" && audit.Execution.Attempted || audit.Execution.Outcome == "succeeded" && audit.Decision.Outcome != "allow" || audit.Execution.HTTPStatus < 0 || audit.Execution.HTTPStatus > 599 {
		return ErrInvalid
	}
	if !slices.Contains([]string{"succeeded", "not_attempted", "unknown", "failed", "blocked_output"}, audit.Execution.Outcome) {
		return ErrInvalid
	}
	digest := gatewayDigest(in)
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var ws int64
		var appID string
		if e := tx.QueryRow(ctx, `SELECT r.workspace_id,r.application_id FROM plume.ai_gateway_reservations r JOIN plume.workspaces w ON w.id=r.workspace_id WHERE w.slug=$1 AND r.id=$2`, in.WorkspaceSlug, in.ReservationID).Scan(&ws, &appID); e != nil {
			return storeError(e)
		}
		// Always account -> reservation -> audit; the same order as admission prevents cross-node deadlocks.
		var dummy int64
		if e := tx.QueryRow(ctx, `SELECT reserved_micros FROM plume.ai_gateway_accounts WHERE workspace_id=$1 AND application_id=$2 FOR UPDATE`, ws, appID).Scan(&dummy); e != nil {
			return e
		}
		var reserved, tokens int64
		var state, prior string
		var window time.Time
		var authorized []byte
		var admission GatewayAuthorization
		if e := tx.QueryRow(ctx, `SELECT reserved_micros,reserved_tokens,state,settlement_hash,window_start,authorized FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, in.ReservationID).Scan(&reserved, &tokens, &state, &prior, &window, &authorized); e != nil {
			return e
		}
		if e := json.Unmarshal(authorized, &admission); e != nil {
			return e
		}
		if admission.RequestDigest != in.RequestDigest || admission.RequestID != audit.RequestID || admission.CatalogDigest != audit.Decision.CatalogDigest || admission.ApplicationID != audit.Decision.ApplicationID || admission.PrincipalID != audit.Decision.PrincipalID || admission.Kind != audit.Decision.Kind {
			return ErrForbidden
		}
		if admission.TaskID != audit.Decision.TaskID {
			return ErrForbidden
		}
		if (admission.TaskID != "" || len(admission.SourceDigests) > 0) && (audit.RuntimeProtocolVersion != aisecurity.RuntimeProtocolVersion || audit.InspectionVersion != aisecurity.InspectionVersion) {
			return ErrForbidden
		}
		for _, pin := range admission.SourceDigests {
			if !slices.Contains(audit.Decision.SourceDigests, pin) {
				return ErrForbidden
			}
		}
		if admission.TaskID != "" {
			task, e := gatewayTask(ctx, tx, ws, admission.TaskID)
			if e != nil {
				return e
			}
			if audit.Decision.TaskPurpose != task.Purpose || audit.Decision.TaskScopeDigest != task.ScopeDigest {
				return ErrForbidden
			}
		}
		if len(admission.MediaDigests) > 0 {
			if audit.Decision.MediaInspectionVersion != admission.MediaInspectionVersion {
				return ErrForbidden
			}
			for _, d := range admission.MediaDigests {
				if !slices.Contains(audit.Decision.MediaDigests, d) {
					return ErrForbidden
				}
			}
		}
		if state == "settled" {
			if prior == digest {
				return nil
			}
			return ErrConflict
		}
		var previous string
		e := tx.QueryRow(ctx, `SELECT event_hash FROM plume.ai_gateway_runtime_events WHERE workspace_id=$1 AND event_id=$2`, ws, audit.EventID).Scan(&previous)
		if e == nil {
			if previous == digest {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_runtime_events(workspace_id,event_id,reservation_id,event_hash) VALUES($1,$2,$3,$4)`, ws, audit.EventID, in.ReservationID, digest); e != nil {
			return storeError(e)
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_reservations SET last_audit=$3 WHERE workspace_id=$1 AND id=$2`, ws, in.ReservationID, raw); e != nil {
			return e
		}
		if admission.Kind == "model" && audit.Execution.Attempted && (audit.Execution.Outcome == "succeeded" || audit.Execution.Outcome == "failed" && slices.Contains([]int{429, 502, 503, 504}, audit.Execution.HTTPStatus)) {
			failed := audit.Execution.Outcome != "succeeded"
			if _, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_route_health(workspace_id,provider_id,failures,open_until,last_outcome) VALUES($1,$2,$3,NULL,$4) ON CONFLICT(workspace_id,provider_id) DO UPDATE SET failures=CASE WHEN $3=0 THEN 0 ELSE ai_gateway_route_health.failures+1 END,open_until=CASE WHEN $3=0 THEN NULL WHEN ai_gateway_route_health.failures+1>=3 THEN clock_timestamp()+interval '30 seconds' ELSE ai_gateway_route_health.open_until END,last_outcome=$4`, ws, admission.ProviderID, map[bool]int{true: 1, false: 0}[failed], audit.Execution.Outcome); e != nil {
				return e
			}
		}
		if audit.Execution.Attempted && audit.Execution.Outcome != "succeeded" {
			if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_reservations SET state='unknown' WHERE workspace_id=$1 AND id=$2`, ws, in.ReservationID); e != nil {
				return e
			}
			return identityAudit(ctx, tx, ws, 0, "gateway.call_unknown", in.ReservationID, audit)
		}
		if state == "unknown" && !audit.Execution.Attempted {
			return ErrConflict
		}
		// A successful execution consumes the fixed quote. A proven pre-dispatch rejection releases it.
		cost, chargedTokens := int64(0), int64(0)
		if audit.Execution.Attempted {
			cost = reserved
			chargedTokens = tokens
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_accounts SET reserved_micros=reserved_micros-$3,spent_micros=spent_micros+$4 WHERE workspace_id=$1 AND application_id=$2`, ws, appID, reserved, cost); e != nil {
			return e
		}
		if e = gatewayTaskSettle(ctx, tx, ws, admission, reserved, cost); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_windows SET tokens=tokens-$4+$5 WHERE workspace_id=$1 AND application_id=$2 AND window_start=$3`, ws, appID, window, tokens, chargedTokens); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_reservations SET state='settled',actual_micros=$3,actual_tokens=$4,settlement_hash=$5,settled_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, ws, in.ReservationID, cost, chargedTokens, digest); e != nil {
			return e
		}
		return identityAudit(ctx, tx, ws, 0, "gateway.call_settled", in.ReservationID, map[string]any{"applicationId": appID, "chargedQuoteMicros": cost, "chargedBoundTokens": chargedTokens, "billingBasis": "governance_quote_not_provider_invoice", "audit": audit})
	})
}

func (s *Store) GatewayHeartbeat(ctx context.Context, slug string, node GatewayNode) error {
	if node.Capabilities == nil {
		node.Capabilities = []string{}
	}
	if len(node.Capabilities) > 16 || node.Version != "" && !gatewayRuntimeID.MatchString(node.Version) {
		return ErrInvalid
	}
	for _, cap := range node.Capabilities {
		if !slices.Contains([]string{"streaming_strict_hold", "multimodal_projection", "encrypted_exact_cache", "governed_fallback", "task_scope_and_kill", "source_document_binding"}, cap) {
			return ErrInvalid
		}
	}
	if !gatewayText(slug, 128) || !gatewayID.MatchString(node.ID) || !slices.Contains([]string{"healthy", "degraded", "open"}, node.Status) || node.Failures < 0 || node.Failures > 1_000_000 {
		return ErrInvalid
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var ws int64
		if e := tx.QueryRow(ctx, `SELECT id FROM plume.workspaces WHERE slug=$1`, slug).Scan(&ws); e != nil {
			return storeError(e)
		}
		var prior, priorVersion string
		var priorCapabilities []string
		e := tx.QueryRow(ctx, `SELECT status,capabilities,version FROM plume.ai_gateway_nodes WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, node.ID).Scan(&prior, &priorCapabilities, &priorVersion)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_nodes(workspace_id,id,status,failures,capabilities,version) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,id) DO UPDATE SET status=EXCLUDED.status,failures=EXCLUDED.failures,capabilities=EXCLUDED.capabilities,version=EXCLUDED.version,last_seen_at=clock_timestamp()`, ws, node.ID, node.Status, node.Failures, node.Capabilities, node.Version); e != nil {
			return e
		}
		if prior != node.Status || priorVersion != node.Version || !slices.Equal(priorCapabilities, node.Capabilities) {
			return identityAudit(ctx, tx, ws, 0, "gateway.node_state", node.ID, map[string]any{"status": node.Status, "failures": node.Failures, "capabilities": node.Capabilities, "version": node.Version, "evidenceBasis": "node_reported_capabilities"})
		}
		return nil
	})
}

func (s *Store) GatewayReservations(ctx context.Context, ws, actor int64, after string, limit int) (out []map[string]any, err error) {
	if limit < 1 || limit > 200 || len(after) > 32 {
		return nil, ErrInvalid
	}
	out = []map[string]any{}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT id,application_id,request_id,model_id,reserved_micros,reserved_tokens,state,actual_micros,actual_tokens,created_at,settled_at FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND id>$2 ORDER BY id LIMIT $3`, ws, after, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id, app, req, model, state string
			var cost, tokens int64
			var chargedCost, chargedTokens *int64
			var created time.Time
			var settled *time.Time
			if e = rows.Scan(&id, &app, &req, &model, &cost, &tokens, &state, &chargedCost, &chargedTokens, &created, &settled); e != nil {
				return e
			}
			out = append(out, map[string]any{"id": id, "applicationId": app, "requestId": req, "modelId": model, "reservedMicros": cost, "reservedTokens": tokens, "state": state, "chargedQuoteMicros": chargedCost, "chargedBoundTokens": chargedTokens, "createdAt": created, "settledAt": settled})
		}
		return rows.Err()
	})
	return
}
