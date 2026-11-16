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

type gatewayToolPolicy struct {
	registered bool
	revision   int64
	tool       aisecurity.Tool
}

// Load the central policy before taking account/task/approval locks. Only a
// workspace with no central AI catalog retains the legacy static-node boundary;
// a missing tool or mismatched registered catalog must never become that mode.
func gatewayCurrentToolPolicy(ctx context.Context, tx pgx.Tx, ws int64, in gatewaygovernance.TaskCheckInput) (out gatewayToolPolicy, err error) {
	if in.Kind != "tool" {
		return out, nil
	}
	var raw, published []byte
	var revision, publishedRevision int64
	var publishedDigest string
	err = tx.QueryRow(ctx, `SELECT revision,configuration,published_revision,published_configuration,published_digest FROM plume.ai_security_configurations WHERE workspace_id=$1 FOR SHARE`, ws).Scan(&revision, &raw, &publishedRevision, &published, &publishedDigest)
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
	appAllowed := slices.ContainsFunc(cfg.Applications, func(app aisecurity.Application) bool {
		return app.ID == in.ApplicationID && app.Enabled && slices.Contains(app.PrincipalIDs, in.PrincipalID) && slices.Contains(app.ToolIDs, in.ToolID)
	})
	principalAllowed := slices.ContainsFunc(cfg.Principals, func(principal aisecurity.Principal) bool {
		return principal.ID == in.PrincipalID && principal.Enabled && slices.Contains(principal.ApplicationIDs, in.ApplicationID)
	})
	if !appAllowed || !principalAllowed {
		return out, ErrForbidden
	}
	for _, tool := range cfg.Tools {
		if tool.ID == in.ToolID && tool.Enabled && slices.Contains(tool.AllowedApplicationIDs, in.ApplicationID) && slices.Contains(tool.AllowedPrincipalIDs, in.PrincipalID) && slices.Contains(tool.Actions, in.Action) && slices.Contains(tool.Targets, in.Target) {
			out.revision, out.tool = revision, tool
			return out, nil
		}
	}
	return out, ErrForbidden
}

// This validates a concrete current approval, never a caller-supplied risk flag.
// Call after the account lock when admitting, and always before any task lock
// acquisition load the central policy above. Review/rebind use the same
// task-advisory -> approval-row order, so invalidation is atomic with admission.
func gatewayCurrentToolApproval(ctx context.Context, tx pgx.Tx, ws int64, policy gatewayToolPolicy, in gatewaygovernance.TaskCheckInput, pin, protocol string) error {
	if !policy.registered || !policy.tool.HighImpact {
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
	if status != "approved" || revision != policy.revision || catalog != in.CatalogDigest || requestDigest != in.RequestDigest || !expires.After(now) || reviewer == nil || *reviewer == requester || savedPin != pin {
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
