package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"github.com/jackc/pgx/v5"
)

type gatewayCatalogPolicy struct {
	registered           bool
	revision             int64
	tool                 aisecurity.Tool
	expiresAt            *time.Time
	toolIDs              []string
	documents            []aisecurity.Document
	requireDocumentLease bool
}

// Load the central policy before taking account/task/approval locks. Only a
// workspace with no central AI catalog retains the legacy static-node boundary;
// a missing tool or mismatched registered catalog must never become that mode.
func gatewayCurrentCatalogPolicy(ctx context.Context, tx pgx.Tx, ws int64, in gatewaygovernance.TaskCheckInput, lock bool) (out gatewayCatalogPolicy, err error) {
	var raw, published []byte
	var revision, publishedRevision int64
	var publishedDigest string
	var now time.Time
	query := `SELECT revision,configuration,published_revision,published_configuration,published_digest,clock_timestamp() FROM plume.ai_security_configurations WHERE workspace_id=$1`
	if lock {
		query += " FOR SHARE"
	}
	err = tx.QueryRow(ctx, query, ws).Scan(&revision, &raw, &publishedRevision, &published, &publishedDigest, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.registered = true
	if publishedRevision > 0 && publishedDigest == in.CatalogDigest {
		raw, revision = published, publishedRevision
	}
	var cfg aisecurity.Config
	if json.Unmarshal(raw, &cfg) != nil || aisecurity.ValidateConfig(cfg) != nil || aisecurity.ConfigDigest(cfg) != in.CatalogDigest {
		return out, ErrForbidden
	}
	var app aisecurity.Application
	for _, v := range cfg.Applications {
		if v.ID == in.ApplicationID && v.Enabled && slices.Contains(v.PrincipalIDs, in.PrincipalID) {
			app = v
		}
	}
	var principal aisecurity.Principal
	for _, v := range cfg.Principals {
		if v.ID == in.PrincipalID && v.Enabled && slices.Contains(v.ApplicationIDs, in.ApplicationID) {
			principal = v
		}
	}
	if app.ID == "" || principal.ID == "" || cfg.Policy.RequireAuthorizationLeases && principal.AuthorizationExpiresAt == nil {
		return out, ErrForbidden
	}
	out.revision, out.expiresAt = revision, principal.AuthorizationExpiresAt
	out.requireDocumentLease = cfg.Policy.RequireAuthorizationLeases
	for _, doc := range cfg.Documents {
		if doc.Enabled && slices.Contains(app.DocumentIDs, doc.ID) && slices.Contains(doc.AllowedPrincipalIDs, in.PrincipalID) {
			out.documents = append(out.documents, doc)
			// Match the engine's conservative model source scope even when a
			// caller omits documentIds. Tools only use actual persisted sources.
			if in.Kind == "model" || slices.Contains(in.DocumentIDs, doc.ID) {
				if err = out.includeDocumentLease(doc.ID); err != nil {
					return out, err
				}
			}
		}
	}
	var handling *aisecurity.DataHandling
	switch in.Kind {
	case "tool":
		for _, tool := range cfg.Tools {
			if tool.ID == in.ToolID && tool.Enabled && slices.Contains(app.ToolIDs, tool.ID) && slices.Contains(tool.AllowedApplicationIDs, in.ApplicationID) && slices.Contains(tool.AllowedPrincipalIDs, in.PrincipalID) && slices.Contains(tool.Actions, in.Action) && slices.Contains(tool.Targets, in.Target) {
				out.tool, handling = tool, tool.Handling
				break
			}
		}
		if out.tool.ID == "" {
			return out, ErrForbidden
		}
	case "model":
		found := false
		for _, model := range cfg.Models {
			if model.ID == in.ModelID && model.Enabled && slices.Contains(app.ModelIDs, model.ID) && slices.Contains(model.AllowedApplicationIDs, in.ApplicationID) {
				handling, found = model.Handling, true
				break
			}
		}
		if !found {
			return out, ErrForbidden
		}
	}
	if in.Kind == "model" || in.Kind == "tool" {
		if handling == nil && cfg.Policy.RequireDataHandling {
			return out, ErrForbidden
		}
		// A declared destination contract expires even when the global presence
		// requirement is off (for example a source UsePolicy requires it).
		if handling != nil && (out.expiresAt == nil || handling.AuthorizationExpiresAt.Before(*out.expiresAt)) {
			out.expiresAt = &handling.AuthorizationExpiresAt
		}
	}
	if !out.current(now) {
		return out, ErrForbidden
	}
	for _, tool := range cfg.Tools {
		if tool.Enabled && slices.Contains(app.ToolIDs, tool.ID) && slices.Contains(tool.AllowedApplicationIDs, in.ApplicationID) && slices.Contains(tool.AllowedPrincipalIDs, in.PrincipalID) && (tool.Handling == nil && !cfg.Policy.RequireDataHandling || tool.Handling != nil && tool.Handling.AuthorizationExpiresAt.After(now)) {
			out.toolIDs = append(out.toolIDs, tool.ID)
		}
	}
	return out, nil
}

func (p gatewayCatalogPolicy) current(now time.Time) bool {
	return !p.registered || p.expiresAt == nil || p.expiresAt.After(now)
}

func (p *gatewayCatalogPolicy) includeDocumentLease(id string) error {
	for _, doc := range p.documents {
		if doc.ID != id {
			continue
		}
		if doc.AuthorizationExpiresAt == nil && p.requireDocumentLease {
			return ErrForbidden
		}
		if doc.AuthorizationExpiresAt != nil && (p.expiresAt == nil || doc.AuthorizationExpiresAt.Before(*p.expiresAt)) {
			p.expiresAt = doc.AuthorizationExpiresAt
		}
		return nil
	}
	return ErrForbidden
}

// This validates a concrete current approval, never a caller-supplied risk flag.
// Call after the account lock when admitting, and always before any task lock
// acquisition load the central policy above. Review/rebind use the same
// task-advisory -> approval-row order, so invalidation is atomic with admission.
func gatewayCurrentToolApproval(ctx context.Context, tx pgx.Tx, ws int64, policy gatewayCatalogPolicy, in gatewaygovernance.TaskCheckInput, pin, protocol string) error {
	if !policy.registered {
		return nil
	}
	if !policy.tool.HighImpact {
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		if !policy.current(now) {
			return ErrForbidden
		}
		return nil
	}
	id, err := strconv.ParseInt(in.ApprovalID, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != in.ApprovalID || !gatewayHash.MatchString(in.RequestDigest) {
		return ErrForbidden
	}
	if in.TaskID == "" && (pin != "" || protocol != "") || in.TaskID != "" && (!gatewayHash.MatchString(pin) || protocol != gatewaygovernance.TaskLeaseProtocolVersion) {
		return ErrForbidden
	}
	if err = gatewayTaskLock(ctx, tx, ws); err != nil {
		return err
	}
	var status, catalog, requestDigest, savedPin string
	var revision, requester int64
	var reviewer *int64
	var expires, now time.Time
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT status,configuration_revision,catalog_digest,request_digest,request_metadata,requested_by,reviewed_by,expires_at,COALESCE(task_lease_digest,''),clock_timestamp() FROM plume.ai_security_approvals WHERE workspace_id=$1 AND id=$2 FOR SHARE`, ws, id).Scan(&status, &revision, &catalog, &requestDigest, &raw, &requester, &reviewer, &expires, &savedPin, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if !policy.current(now) || status != "approved" || revision != policy.revision || catalog != in.CatalogDigest || requestDigest != in.RequestDigest || !expires.After(now) || reviewer == nil || *reviewer == requester || savedPin != pin {
		return ErrForbidden
	}
	var canReview bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.memberships WHERE workspace_id=$1 AND user_id=$2 AND role IN ('owner','reviewer'))`, ws, *reviewer).Scan(&canReview); err != nil {
		return err
	}
	var request aisecurity.Request
	if !canReview || json.Unmarshal(raw, &request) != nil || aisecurity.RequestDigest(request) != requestDigest || request.RequestID != in.RequestID || request.ApplicationID != in.ApplicationID || request.PrincipalID != in.PrincipalID || request.Kind != "tool" || request.ToolID != in.ToolID || request.Action != in.Action || request.Target != in.Target || request.TaskID != in.TaskID || !request.ExpiresAt.Truncate(time.Microsecond).Equal(expires) {
		return ErrForbidden
	}
	return nil
}
