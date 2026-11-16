package aisecurity

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func (c *GatewayController) TaskControl(ctx context.Context, op TaskOperation) (gatewaygovernance.TaskGrant, error) {
	r := op.Request
	token, _ := ctx.Value(gatewayCredentialHashKey{}).(string)
	in := gatewaygovernance.TaskCheckInput{TaskContext: taskBinding(ctx, r), WorkspaceSlug: c.options.WorkspaceSlug, TokenHash: token, ApplicationID: r.ApplicationID, PrincipalID: r.PrincipalID, CatalogDigest: op.Decision.CatalogDigest, Kind: r.Kind, ModelID: r.ModelID, ToolID: r.ToolID, Action: r.Action, Target: r.Target, DocumentIDs: append([]string(nil), r.DocumentIDs...), Categories: append([]string(nil), op.Decision.Categories...)}
	var out gatewaygovernance.TaskGrant
	switch op.Stage {
	case "check":
		err := c.postTask(ctx, "task/check", in, &out)
		return out, err
	case "admit":
		err := c.postTask(ctx, "task/steps/admit", in, &out)
		return out, err
	case "complete":
		if op.Audit == nil {
			return out, errors.New("task completion evidence required")
		}
		raw, e := json.Marshal(op.Audit)
		if e != nil {
			return out, e
		}
		err := c.postTask(ctx, "task/steps/complete", gatewaygovernance.TaskCompleteInput{TaskCheckInput: in, Audit: raw, Sources: append([]SourceEvidence(nil), r.sourceEvidence...)}, &out)
		return out, err
	default:
		return out, errors.New("task operation unsupported")
	}
}

func (c *GatewayController) postTask(ctx context.Context, operation string, input any, out *gatewaygovernance.TaskGrant) error {
	if e := c.post(ctx, operation, input, out); e != nil {
		return e
	}
	if !out.Allowed {
		return errors.New("task scope rejected")
	}
	return nil
}

func taskContextFromAudit(ctx context.Context, a AuditRecord) gatewaygovernance.TaskContext {
	if a.Decision.TaskID == "" {
		return gatewaygovernance.TaskContext{}
	}
	if current, ok := ctx.Value(gatewayTaskExecutionKey{}).(*gatewayTaskExecution); ok {
		return current.Binding
	}
	return gatewaygovernance.TaskContext{TaskID: a.Decision.TaskID, TaskStepID: a.RequestID, TaskStepDigest: a.Decision.RequestDigest}
}
