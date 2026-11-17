package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sort"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"github.com/jackc/pgx/v5"
)

func gatewayTaskFact(in gatewaygovernance.TaskCheckInput) error {
	if !gatewayText(in.WorkspaceSlug, 128) || !gatewayHash.MatchString(in.TokenHash) || !gatewayRuntimeID.MatchString(in.ApplicationID) || !gatewayRuntimeID.MatchString(in.PrincipalID) || !gatewayHash.MatchString(in.CatalogDigest) || !slices.Contains([]string{"model", "rag", "tool", "discovery"}, in.Kind) || !gatewayTaskIDs(in.DocumentIDs, 64) || !gatewayTaskIDs(in.Categories, 64) {
		return ErrInvalid
	}
	if in.TaskID == "" {
		if in.TaskStepID != "" || in.TaskStepDigest != "" || in.LeaseChainDigest != "" || in.LeaseProtocolVersion != "" {
			return ErrInvalid
		}
	} else if !gatewayRuntimeID.MatchString(in.TaskID) || in.Kind != "discovery" && (!gatewayText(in.TaskStepID, 200) || !gatewayHash.MatchString(in.TaskStepDigest)) {
		return ErrInvalid
	}
	if in.Kind == "model" && !gatewayRuntimeID.MatchString(in.ModelID) || in.Kind == "tool" && !gatewayRuntimeID.MatchString(in.ToolID) {
		return ErrInvalid
	}
	if in.ApprovalID != "" && (in.Kind != "tool" || !gatewayRuntimeID.MatchString(in.ApprovalID) || !gatewayRuntimeID.MatchString(in.RequestID) || !gatewayHash.MatchString(in.RequestDigest)) || in.ApprovalID == "" && (in.RequestID != "" || in.RequestDigest != "") {
		return ErrInvalid
	}
	return nil
}

func gatewayMergeTaskSources(a, b []gatewaygovernance.TaskSource) ([]gatewaygovernance.TaskSource, error) {
	byID := map[string]gatewaygovernance.TaskSource{}
	for _, s := range append(append([]gatewaygovernance.TaskSource{}, a...), b...) {
		if !gatewayRuntimeID.MatchString(s.DocumentID) || !gatewayRuntimeID.MatchString(s.Version) || !gatewayRuntimeID.MatchString(s.ACLVersion) || !gatewayHash.MatchString(s.ContentDigest) {
			return nil, ErrInvalid
		}
		if old, ok := byID[s.DocumentID]; ok && old != s {
			return nil, ErrConflict
		}
		byID[s.DocumentID] = s
	}
	if len(byID) > 64 {
		return nil, ErrInvalid
	}
	out := make([]gatewaygovernance.TaskSource, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DocumentID < out[j].DocumentID })
	return out, nil
}

func gatewayCheckTask(ctx context.Context, tx pgx.Tx, in gatewaygovernance.TaskCheckInput) (ws int64, chain []GatewayTask, grant gatewaygovernance.TaskGrant, err error) {
	return gatewayCheckTaskScope(ctx, tx, in, true)
}

// Receipt completion retains its original execution facts after a catalog lease
// expires. It still checks the existing credential/task scope and exact receipt.
func gatewayCheckTaskScope(ctx context.Context, tx pgx.Tx, in gatewaygovernance.TaskCheckInput, currentAuthority bool) (ws int64, chain []GatewayTask, grant gatewaygovernance.TaskGrant, err error) {
	grant.Categories = []string{}
	grant.Sources = []gatewaygovernance.TaskSource{}
	grant.SourceDocumentIDs = []string{}
	grant.ModelIDs = []string{}
	grant.ToolIDs = []string{}
	grant.ToolScopes = []gatewaygovernance.TaskToolScope{}
	grant.DocumentIDs = []string{}
	if err = gatewayTaskFact(in); err != nil {
		return
	}
	var credential int64
	var appID string
	err = tx.QueryRow(ctx, `SELECT c.workspace_id,c.id,c.application_id FROM plume.ai_gateway_credentials c JOIN plume.workspaces w ON w.id=c.workspace_id WHERE w.slug=$1 AND c.token_hash=$2 AND c.enabled AND (c.expires_at IS NULL OR c.expires_at>clock_timestamp())`, in.WorkspaceSlug, in.TokenHash).Scan(&ws, &credential, &appID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrForbidden
	}
	if err != nil {
		return
	}
	var toolPolicy gatewayCatalogPolicy
	if currentAuthority {
		toolPolicy, err = gatewayCurrentCatalogPolicy(ctx, tx, ws, in, true)
		if err != nil {
			return
		}
	}
	c, e := gatewayConfig(ctx, tx, ws, " FOR SHARE")
	if e != nil {
		err = e
		return
	}
	app, ok := gatewayTaskApp(c, appID)
	if !ok || !c.Enabled || app.ID != in.ApplicationID || app.PrincipalID != in.PrincipalID || c.CatalogDigest != in.CatalogDigest {
		err = ErrForbidden
		return
	}
	grant.Required = app.RequireTask
	if in.TaskID == "" {
		if app.RequireTask {
			err = ErrForbidden
		} else {
			if in.ApprovalID != "" || in.Kind != "tool" {
				err = gatewayCurrentToolApproval(ctx, tx, ws, toolPolicy, in, "", "")
			}
			grant.Allowed = err == nil
		}
		return
	}
	if err = gatewayTaskLock(ctx, tx, ws); err != nil {
		return
	}
	chain, err = gatewayTaskChain(ctx, tx, ws, in.TaskID)
	if err != nil {
		return
	}
	if err = gatewayTaskLeasePin(in.TaskContext, chain, false); err != nil {
		return
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return
	}
	if !toolPolicy.current(now) {
		err = ErrForbidden
		return
	}
	for _, task := range chain {
		if task.Status != "active" || !task.ExpiresAt.After(now) || task.ApplicationID != app.ID || task.PrincipalID != in.PrincipalID || task.CatalogDigest != in.CatalogDigest || !gatewayTaskSubset(in.Categories, task.AllowedCategories) || !gatewayTaskSubset(in.DocumentIDs, task.DocumentIDs) {
			err = ErrForbidden
			return
		}
		if err = gatewayTaskCredential(ctx, tx, ws, task.ExecutionLease.CredentialID, task.ApplicationID); err != nil {
			return
		}
		if in.Kind == "model" && (!slices.Contains(task.ModelIDs, in.ModelID) || !slices.Contains(app.ModelIDs, in.ModelID)) || in.Kind == "tool" && (!slices.Contains(task.ToolIDs, in.ToolID) || !slices.Contains(app.ToolIDs, in.ToolID)) || in.Kind == "rag" && !app.AllowRag {
			err = ErrForbidden
			return
		}
		if in.Kind == "tool" && !gatewayTaskToolAllowed(task, in) {
			err = ErrForbidden
			return
		}
		grant.Categories = gatewayTaskUnion(grant.Categories, task.ObservedCategories)
		grant.Sources, err = gatewayMergeTaskSources(grant.Sources, task.Sources)
		if err != nil {
			return
		}
	}
	leaf := chain[len(chain)-1]
	if leaf.ExecutionLease.CredentialID != credential {
		err = ErrForbidden
		return
	}
	for _, task := range chain {
		if !gatewayTaskSubset(grant.Categories, task.AllowedCategories) {
			err = ErrForbidden
			return
		}
	}
	for _, src := range grant.Sources {
		grant.SourceDocumentIDs = append(grant.SourceDocumentIDs, src.DocumentID)
		if toolPolicy.registered {
			if err = toolPolicy.includeDocumentLease(src.DocumentID); err != nil {
				return
			}
		}
	}
	if !toolPolicy.current(now) {
		err = ErrForbidden
		return
	}
	grant.Allowed = true
	grant.TaskID = leaf.ID
	grant.Revision = leaf.Revision
	grant.ScopeDigest = leaf.ScopeDigest
	grant.Purpose = leaf.Purpose
	grant.LeaseChainDigest = gatewayTaskLeaseDigest(chain)
	grant.LeaseProtocolVersion = gatewaygovernance.TaskLeaseProtocolVersion
	if in.ApprovalID != "" || in.Kind != "tool" {
		err = gatewayCurrentToolApproval(ctx, tx, ws, toolPolicy, in, grant.LeaseChainDigest, grant.LeaseProtocolVersion)
	}
	if err != nil {
		grant.Allowed = false
		return
	}
	for _, id := range leaf.ModelIDs {
		if slices.Contains(app.ModelIDs, id) {
			grant.ModelIDs = append(grant.ModelIDs, id)
		}
	}
	for _, scope := range leaf.ToolScopes {
		if slices.Contains(app.ToolIDs, scope.ToolID) {
			grant.ToolIDs = append(grant.ToolIDs, scope.ToolID)
			grant.ToolScopes = append(grant.ToolScopes, scope)
		}
	}
	grant.DocumentIDs = append([]string{}, leaf.DocumentIDs...)
	grant.ExpiresAt = &leaf.ExpiresAt
	return
}

func (s *Store) CheckGatewayTask(ctx context.Context, in gatewaygovernance.TaskCheckInput) (out gatewaygovernance.TaskGrant, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error { _, _, v, e := gatewayCheckTask(ctx, tx, in); out = v; return e })
	return
}

func gatewayAdmitTask(ctx context.Context, tx pgx.Tx, in gatewaygovernance.TaskCheckInput) (out gatewaygovernance.TaskGrant, err error) {
	ws, chain, out, err := gatewayCheckTask(ctx, tx, in)
	if err != nil || in.TaskID == "" {
		return
	}
	if e := gatewayTaskLeasePin(in.TaskContext, chain, true); e != nil {
		return out, e
	}
	if e := taskLeasePendingPreviousEpoch(ctx, tx, ws, chain, ""); e != nil {
		return out, e
	}
	if in.Kind == "discovery" {
		return out, ErrInvalid
	}
	var digest, kind, taskID, status string
	e := tx.QueryRow(ctx, `SELECT task_id,step_digest,kind,status FROM plume.ai_gateway_task_steps WHERE workspace_id=$1 AND step_id=$2`, ws, in.TaskStepID).Scan(&taskID, &digest, &kind, &status)
	if e == nil {
		if taskID != in.TaskID || digest != in.TaskStepDigest || kind != in.Kind || status != "admitted" {
			return out, ErrConflict
		}
		out.Admitted = true
		out.Replayed = true
		return out, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	for _, task := range chain {
		if task.StepsUsed >= task.MaxSteps {
			return out, ErrGatewayTaskSteps
		}
	}
	docs, _ := json.Marshal(in.DocumentIDs)
	cats, _ := json.Marshal(in.Categories)
	snapshot := []TaskLeaseExpected{}
	for _, t := range chain {
		snapshot = append(snapshot, TaskLeaseExpected{TaskID: t.ID, TaskRevision: t.Revision, LeaseRevision: t.ExecutionLease.Revision, CredentialID: t.ExecutionLease.CredentialID})
	}
	snapshotRaw, _ := json.Marshal(snapshot)
	if _, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_task_steps(workspace_id,task_id,step_id,step_digest,kind,model_id,tool_id,document_ids,categories,lease_chain_digest,lease_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, ws, in.TaskID, in.TaskStepID, in.TaskStepDigest, in.Kind, in.ModelID, in.ToolID, docs, cats, in.LeaseChainDigest, snapshotRaw); e != nil {
		return out, storeError(e)
	}
	for _, task := range chain {
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_tasks SET steps_used=steps_used+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, ws, task.ID); e != nil {
			return out, e
		}
	}
	if e = identityAudit(ctx, tx, ws, 0, "gateway.task_step_admitted", in.TaskID, map[string]any{"stepId": in.TaskStepID, "stepDigest": in.TaskStepDigest, "kind": in.Kind, "modelId": in.ModelID, "toolId": in.ToolID, "documentIds": in.DocumentIDs, "categories": in.Categories, "scopeDigest": out.ScopeDigest, "leaseChainDigest": out.LeaseChainDigest, "leaseProtocolVersion": out.LeaseProtocolVersion, "leaseSnapshot": snapshot}); e != nil {
		return out, e
	}
	out.Admitted = true
	return out, nil
}

func (s *Store) AdmitGatewayTask(ctx context.Context, in gatewaygovernance.TaskCheckInput) (out gatewaygovernance.TaskGrant, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error { v, e := gatewayAdmitTask(ctx, tx, in); out = v; return e })
	return
}

func (s *Store) CompleteGatewayTask(ctx context.Context, in gatewaygovernance.TaskCompleteInput) (out gatewaygovernance.TaskGrant, err error) {
	if in.TaskID == "" || len(in.Sources) > 64 {
		return out, ErrInvalid
	}
	var audit aisecurity.AuditRecord
	decoder := json.NewDecoder(bytes.NewReader(in.Audit))
	decoder.DisallowUnknownFields()
	if len(in.Audit) > 65536 || decoder.Decode(&audit) != nil || decoder.Decode(new(any)) != io.EOF || !validGatewayRuntimeEvidence(audit) || audit.RuntimeProtocolVersion != aisecurity.RuntimeProtocolVersion || audit.InspectionVersion != aisecurity.InspectionVersion || (audit.Stage != "completed" && !(audit.Stage == "decision" && audit.Decision.Outcome == "deny" && !audit.Execution.Attempted && audit.Execution.Outcome == "not_attempted")) || !slices.Contains([]string{"allow", "deny"}, audit.Decision.Outcome) || !gatewayRuntimeID.MatchString(audit.EventID) || !gatewayRuntimeID.MatchString(audit.AttemptID) || !gatewayText(audit.RequestID, 200) || audit.Decision.ApplicationID != in.ApplicationID || audit.Decision.PrincipalID != in.PrincipalID || audit.Decision.CatalogDigest != in.CatalogDigest || audit.Decision.Kind != in.Kind || !gatewayTaskIDs(audit.Decision.Categories, 64) || !gatewayTaskIDs(audit.Decision.AllowedDocumentIDs, 64) || audit.ObservedAt.IsZero() || audit.ObservedAt.After(time.Now().Add(5*time.Minute)) {
		return out, ErrInvalid
	}
	successful := audit.Decision.Outcome == "allow" && (audit.Execution.Outcome == "succeeded" || (audit.Execution.CacheHit && audit.Execution.Outcome == "not_attempted"))
	if !successful && (audit.Execution.OutputReleased || audit.Execution.CacheHit || !slices.Contains([]string{"failed", "not_attempted", "blocked_output"}, audit.Execution.Outcome)) {
		return out, ErrInvalid
	}
	if successful && !audit.Execution.CacheHit && !audit.Execution.Attempted {
		return out, ErrInvalid
	}
	in.Categories = gatewayTaskUnion(in.Categories, audit.Decision.Categories)
	completionIntent := in
	completionIntent.TokenHash = ""
	completionIntent.LeaseChainDigest = audit.Decision.LeaseChainDigest
	completionIntent.LeaseProtocolVersion = audit.Decision.LeaseProtocolVersion
	digest := gatewayDigest(completionIntent)
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		ws, chain, grant, e := gatewayCheckTaskScope(ctx, tx, in.TaskCheckInput, false)
		out = grant
		if e != nil {
			return e
		}
		if e = gatewayTaskLeasePin(in.TaskContext, chain, true); e != nil {
			return e
		}
		if audit.Decision.LeaseProtocolVersion != gatewaygovernance.TaskLeaseProtocolVersion {
			return ErrForbidden
		}
		if audit.Decision.LeaseChainDigest != grant.LeaseChainDigest {
			if e = taskLeaseHistoricalCompletion(ctx, tx, ws, chain, in.TaskID, in.TaskStepID, audit.Decision.LeaseChainDigest); e != nil {
				return e
			}
		}
		if audit.Decision.TaskID != in.TaskID || audit.Decision.TaskPurpose != grant.Purpose || audit.Decision.TaskScopeDigest != grant.ScopeDigest {
			return ErrForbidden
		}
		receiptIn := in
		receiptIn.LeaseChainDigest = audit.Decision.LeaseChainDigest
		receiptIn.LeaseProtocolVersion = audit.Decision.LeaseProtocolVersion
		if e = gatewayTaskCompletionAudit(ctx, tx, ws, receiptIn, audit); e != nil {
			return e
		}
		var bound, kind, status, previous string
		var docsRaw []byte
		e = tx.QueryRow(ctx, `SELECT step_digest,kind,status,completion_hash,document_ids FROM plume.ai_gateway_task_steps WHERE workspace_id=$1 AND task_id=$2 AND step_id=$3`, ws, in.TaskID, in.TaskStepID).Scan(&bound, &kind, &status, &previous, &docsRaw)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if e != nil {
			return e
		}
		if bound != in.TaskStepDigest || kind != in.Kind {
			return ErrConflict
		}
		if status == "completed" || status == "closed" {
			if previous == digest {
				out.Replayed = true
				return nil
			}
			return ErrConflict
		}
		var stepDocs []string
		if json.Unmarshal(docsRaw, &stepDocs) != nil {
			return ErrInvalid
		}
		if !gatewayTaskSubset(grant.Categories, audit.Decision.Categories) {
			return ErrForbidden
		}
		for _, source := range grant.Sources {
			if !slices.Contains(audit.Decision.SourceDigests, gatewayDigest(source)) {
				return ErrForbidden
			}
		}
		merged, e := gatewayMergeTaskSources(grant.Sources, in.Sources)
		if e != nil {
			return e
		}
		known := map[string]gatewaygovernance.TaskSource{}
		for _, source := range grant.Sources {
			known[source.DocumentID] = source
		}
		for _, source := range in.Sources {
			if !slices.Contains(audit.Decision.SourceDigests, gatewayDigest(source)) {
				return ErrForbidden
			}
			if _, ok := known[source.DocumentID]; !ok {
				if !slices.Contains([]string{"rag", "model"}, in.Kind) || !slices.Contains(stepDocs, source.DocumentID) || !slices.Contains(audit.Decision.AllowedDocumentIDs, source.DocumentID) {
					return ErrForbidden
				}
			}
		}
		if in.Kind == "rag" || in.Kind == "model" {
			for _, id := range stepDocs {
				if slices.Contains(audit.Decision.AllowedDocumentIDs, id) && !slices.ContainsFunc(merged, func(source gatewaygovernance.TaskSource) bool { return source.DocumentID == id }) {
					return ErrForbidden
				}
			}
		}
		for _, task := range chain {
			if !gatewayTaskSubset(in.Categories, task.AllowedCategories) {
				return ErrForbidden
			}
			sources, e := gatewayMergeTaskSources(task.Sources, in.Sources)
			if e != nil {
				return e
			}
			cats := gatewayTaskUnion(task.ObservedCategories, in.Categories)
			rawCats, _ := json.Marshal(cats)
			rawSources, _ := json.Marshal(sources)
			if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_tasks SET observed_categories=$3,sources=$4,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, ws, task.ID, rawCats, rawSources); e != nil {
				return e
			}
		}
		cats, _ := json.Marshal(in.Categories)
		sources, _ := json.Marshal(in.Sources)
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_task_steps SET status=$7,completion_hash=$4,categories=$5,sources=$6,completed_at=clock_timestamp(),closure_evidence=$8 WHERE workspace_id=$1 AND task_id=$2 AND step_id=$3`, ws, in.TaskID, in.TaskStepID, digest, cats, sources, func() string {
			if successful {
				return "completed"
			}
			return "closed"
		}(), in.Audit); e != nil {
			return e
		}
		if e = identityAudit(ctx, tx, ws, 0, "gateway.task_step_completed", in.TaskID, map[string]any{"stepId": in.TaskStepID, "stepDigest": in.TaskStepDigest, "audit": audit, "sources": in.Sources, "scopeDigest": out.ScopeDigest, "completionLeaseChainDigest": out.LeaseChainDigest, "receiptLeaseChainDigest": audit.Decision.LeaseChainDigest, "completionStatus": func() string {
			if successful {
				return "completed"
			}
			return "closed"
		}(), "deliveryConfirmed": false}); e != nil {
			return e
		}
		_, _, out, e = gatewayCheckTaskScope(ctx, tx, in.TaskCheckInput, false)
		return e
	})
	return
}

func gatewayTaskReserve(ctx context.Context, tx pgx.Tx, ws int64, in GatewayAuthorizeInput, cost int64) error {
	if in.TaskID == "" {
		c, e := gatewayConfig(ctx, tx, ws, "")
		if e != nil {
			return e
		}
		app, ok := gatewayTaskApp(c, in.ApplicationID)
		if !ok || app.RequireTask {
			return ErrForbidden
		}
		return nil
	}
	fact := gatewaygovernance.TaskCheckInput{TaskContext: in.TaskContext, WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, CatalogDigest: in.CatalogDigest, Kind: in.Kind, ModelID: in.ModelID, ToolID: in.ToolID, Action: in.Action, Target: in.Target}
	actualWS, chain, _, e := gatewayCheckTask(ctx, tx, fact)
	if e != nil {
		return e
	}
	if actualWS != ws {
		return ErrForbidden
	}
	if e = gatewayTaskLeasePin(in.TaskContext, chain, true); e != nil {
		return e
	}
	logical := in.ParentRequestID
	if logical == "" {
		logical = in.RequestID
	}
	if in.TaskStepID != logical {
		return ErrForbidden
	}
	if in.Attempt == 0 && in.TaskStepDigest != in.RequestDigest {
		return ErrForbidden
	}
	var digest, kind, status, pin string
	if e = tx.QueryRow(ctx, `SELECT step_digest,kind,status,COALESCE(lease_chain_digest,'') FROM plume.ai_gateway_task_steps WHERE workspace_id=$1 AND task_id=$2 AND step_id=$3`, ws, in.TaskID, in.TaskStepID).Scan(&digest, &kind, &status, &pin); e != nil {
		return ErrForbidden
	}
	if digest != in.TaskStepDigest || kind != in.Kind || status != "admitted" || pin != in.LeaseChainDigest {
		return ErrConflict
	}
	for _, task := range chain {
		if cost < 0 || cost > task.BudgetMicros-task.ReservedMicros-task.SpentMicros {
			return ErrGatewayBudget
		}
	}
	for _, task := range chain {
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_tasks SET reserved_micros=reserved_micros+$3,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, ws, task.ID, cost); e != nil {
			return e
		}
	}
	return nil
}

func gatewayTaskSettle(ctx context.Context, tx pgx.Tx, ws int64, admission GatewayAuthorization, reserved, cost int64) error {
	if admission.TaskID == "" {
		return nil
	}
	if e := gatewayTaskLock(ctx, tx, ws); e != nil {
		return e
	}
	chain, e := gatewayTaskChain(ctx, tx, ws, admission.TaskID)
	if e != nil {
		return e
	}
	for _, task := range chain {
		if task.ReservedMicros < reserved {
			return ErrConflict
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_tasks SET reserved_micros=reserved_micros-$3,spent_micros=spent_micros+$4,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, ws, task.ID, reserved, cost); e != nil {
			return e
		}
	}
	return nil
}

func gatewayTaskCacheCurrent(ctx context.Context, tx pgx.Tx, ws int64, in GatewayAuthorizeInput) error {
	return gatewayTaskCacheCurrentStatus(ctx, tx, ws, in, false)
}
func gatewayTaskCacheCurrentStatus(ctx context.Context, tx pgx.Tx, ws int64, in GatewayAuthorizeInput, allowCompleted bool) error {
	if in.TaskID == "" {
		return gatewayTaskReserve(ctx, tx, ws, in, 0)
	}
	fact := gatewaygovernance.TaskCheckInput{TaskContext: in.TaskContext, WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, CatalogDigest: in.CatalogDigest, Kind: in.Kind, ModelID: in.ModelID}
	actualWS, chain, _, e := gatewayCheckTask(ctx, tx, fact)
	if e != nil {
		return e
	}
	if actualWS != ws {
		return ErrForbidden
	}
	if e = gatewayTaskLeasePin(in.TaskContext, chain, true); e != nil {
		return e
	}
	logical := in.ParentRequestID
	if logical == "" {
		logical = in.RequestID
	}
	if logical != in.TaskStepID {
		return ErrForbidden
	}
	if in.Attempt == 0 && in.TaskStepDigest != in.RequestDigest {
		return ErrForbidden
	}
	var digest, kind, status, pin string
	if e = tx.QueryRow(ctx, `SELECT step_digest,kind,status,COALESCE(lease_chain_digest,'') FROM plume.ai_gateway_task_steps WHERE workspace_id=$1 AND task_id=$2 AND step_id=$3`, ws, in.TaskID, in.TaskStepID).Scan(&digest, &kind, &status, &pin); e != nil {
		return ErrForbidden
	}
	if digest != in.TaskStepDigest || kind != "model" || (status != "admitted" && !(allowCompleted && status == "completed")) || pin != in.LeaseChainDigest {
		return ErrConflict
	}
	return nil
}

func gatewayTaskCompletionAudit(ctx context.Context, tx pgx.Tx, ws int64, in gatewaygovernance.TaskCompleteInput, audit aisecurity.AuditRecord) error {
	var raw []byte
	if audit.Execution.CacheHit {
		e := tx.QueryRow(ctx, `SELECT payload::jsonb->'audit' FROM plume.ai_gateway_audit WHERE workspace_id=$1 AND action='gateway.cache_completed' AND target=$2 AND payload::jsonb->>'taskId'=$3 AND payload::jsonb->>'taskStepId'=$4 AND payload::jsonb->>'taskStepDigest'=$5 ORDER BY sequence DESC LIMIT 1`, ws, audit.EventID, in.TaskID, in.TaskStepID, in.TaskStepDigest).Scan(&raw)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if e != nil {
			return e
		}
	} else {
		var authorized []byte
		var state string
		e := tx.QueryRow(ctx, `SELECT authorized,last_audit,state FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND request_id=$2`, ws, audit.RequestID).Scan(&authorized, &raw, &state)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if e != nil {
			return e
		}
		var admission GatewayAuthorization
		if json.Unmarshal(authorized, &admission) != nil || state != "settled" || admission.TaskContext != in.TaskContext || admission.RequestDigest != audit.Decision.RequestDigest || admission.ModelID != in.ModelID || admission.Kind != in.Kind {
			return ErrForbidden
		}
	}
	var recorded aisecurity.AuditRecord
	if json.Unmarshal(raw, &recorded) != nil || gatewayDigest(recorded) != gatewayDigest(audit) {
		return ErrForbidden
	}
	return nil
}
