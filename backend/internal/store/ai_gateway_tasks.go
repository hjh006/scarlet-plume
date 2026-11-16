package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"github.com/jackc/pgx/v5"
)

const gatewayTaskColumns = `id,parent_task_id,root_task_id,depth,application_id,principal_id,credential_id,configuration,scope_digest,status,revision,steps_used,reserved_micros,spent_micros,observed_categories,sources,created_at,updated_at`

func scanGatewayTask(row pgx.Row) (out GatewayTask, err error) {
	var raw, categories, sources []byte
	var app, principal, parent string
	var credential int64
	err = row.Scan(&out.ID, &parent, &out.RootTaskID, &out.Depth, &app, &principal, &credential, &raw, &out.ScopeDigest, &out.Status, &out.Revision, &out.StepsUsed, &out.ReservedMicros, &out.SpentMicros, &categories, &sources, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return out, storeError(err)
	}
	if json.Unmarshal(raw, &out.GatewayTaskInput) != nil || json.Unmarshal(categories, &out.ObservedCategories) != nil || json.Unmarshal(sources, &out.Sources) != nil || app != out.ApplicationID || principal != out.PrincipalID || parent != out.ParentTaskID || credential != out.CredentialID {
		return out, ErrInvalid
	}
	if out.ObservedCategories == nil {
		out.ObservedCategories = []string{}
	}
	if out.Sources == nil {
		out.Sources = []gatewaygovernance.TaskSource{}
	}
	return
}

func gatewayTask(ctx context.Context, tx pgx.Tx, ws int64, id string) (GatewayTask, error) {
	return scanGatewayTask(tx.QueryRow(ctx, `SELECT `+gatewayTaskColumns+` FROM plume.ai_gateway_tasks WHERE workspace_id=$1 AND id=$2`, ws, id))
}

func gatewayTaskLock(ctx context.Context, tx pgx.Tx, ws int64) error {
	// ponytail: one task mutation lock per workspace. Replace with ordered root
	// task locks if measured task throughput outgrows this bounded serialization.
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('scarlet-task:'||$1::bigint::text,0))`, ws)
	return e
}

func gatewayTaskIDs(values []string, maximum int) bool {
	if len(values) > maximum {
		return false
	}
	seen := map[string]bool{}
	for _, id := range values {
		if !gatewayRuntimeID.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func gatewayTaskSubset(values, allowed []string) bool {
	for _, v := range values {
		if !slices.Contains(allowed, v) {
			return false
		}
	}
	return true
}

func gatewayTaskToolScopes(scopes []gatewaygovernance.TaskToolScope, tools []string) bool {
	if len(scopes) != len(tools) {
		return false
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if seen[scope.ToolID] || !slices.Contains(tools, scope.ToolID) || len(scope.Actions) == 0 || !gatewayTaskIDs(scope.Actions, 200) || len(scope.Targets) == 0 || len(scope.Targets) > 200 {
			return false
		}
		seen[scope.ToolID] = true
		targets := map[string]bool{}
		for _, v := range scope.Targets {
			if !gatewayText(v, 2048) || strings.Contains(v, "*") || targets[v] {
				return false
			}
			targets[v] = true
		}
	}
	return true
}

func gatewayTaskToolSubset(child, parent []gatewaygovernance.TaskToolScope) bool {
	for _, c := range child {
		found := false
		for _, p := range parent {
			if p.ToolID == c.ToolID && gatewayTaskSubset(c.Actions, p.Actions) && gatewayTaskSubset(c.Targets, p.Targets) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func gatewayTaskToolAllowed(task GatewayTask, in gatewaygovernance.TaskCheckInput) bool {
	for _, s := range task.ToolScopes {
		if s.ToolID == in.ToolID {
			return slices.Contains(s.Actions, in.Action) && slices.Contains(s.Targets, in.Target)
		}
	}
	return false
}

func gatewayTaskUnion(a, b []string) []string {
	seen := map[string]bool{}
	for _, v := range append(append([]string{}, a...), b...) {
		seen[v] = true
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func gatewayTaskChain(ctx context.Context, tx pgx.Tx, ws int64, id string) ([]GatewayTask, error) {
	out := []GatewayTask{}
	seen := map[string]bool{}
	for id != "" {
		if seen[id] || len(out) >= 5 {
			return nil, ErrForbidden
		}
		seen[id] = true
		t, e := gatewayTask(ctx, tx, ws, id)
		if e != nil {
			return nil, e
		}
		out = append(out, t)
		id = t.ParentTaskID
	}
	slices.Reverse(out)
	for i, t := range out {
		if t.Depth != i || t.RootTaskID != out[0].ID {
			return nil, ErrForbidden
		}
	}
	return out, nil
}

func gatewayTaskApp(c GatewayConfig, id string) (GatewayApplication, bool) {
	for _, a := range c.Applications {
		if a.ID == id && a.Enabled {
			return a, true
		}
	}
	return GatewayApplication{}, false
}

func gatewayTaskCredential(ctx context.Context, tx pgx.Tx, ws int64, id int64, app string) error {
	var actual string
	e := tx.QueryRow(ctx, `SELECT application_id FROM plume.ai_gateway_credentials WHERE workspace_id=$1 AND id=$2 AND enabled`, ws, id).Scan(&actual)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && actual != app {
		return ErrForbidden
	}
	return e
}

func (s *Store) CreateGatewayTask(ctx context.Context, ws, actor int64, in GatewayTaskInput) (out GatewayTask, err error) {
	if !gatewayRuntimeID.MatchString(in.ApplicationID) || !gatewayRuntimeID.MatchString(in.PrincipalID) || in.CredentialID < 1 || in.ParentTaskID != "" && !gatewayRuntimeID.MatchString(in.ParentTaskID) || !gatewayRuntimeID.MatchString(in.Purpose) || !gatewayHash.MatchString(in.CatalogDigest) || !gatewayTaskIDs(in.ModelIDs, 200) || !gatewayTaskIDs(in.ToolIDs, 200) || !gatewayTaskToolScopes(in.ToolScopes, in.ToolIDs) || !gatewayTaskIDs(in.DocumentIDs, 64) || !gatewayTaskIDs(in.AllowedCategories, 64) || in.MaxSteps < 1 || in.MaxSteps > 1_000_000 || in.BudgetMicros < 0 || in.BudgetMicros > 9_000_000_000_000_000 || in.MaxDelegationDepth < 0 || in.MaxDelegationDepth > 4 || in.ExpiresAt.IsZero() {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := gatewayRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		c, e := gatewayConfig(ctx, tx, ws, " FOR SHARE")
		if e != nil {
			return e
		}
		if e := gatewayTaskLock(ctx, tx, ws); e != nil {
			return e
		}
		app, ok := gatewayTaskApp(c, in.ApplicationID)
		if !c.Enabled || !ok || app.PrincipalID != in.PrincipalID || c.CatalogDigest != in.CatalogDigest || !gatewayTaskSubset(in.ModelIDs, app.ModelIDs) || !gatewayTaskSubset(in.ToolIDs, app.ToolIDs) || len(in.DocumentIDs) > 0 && !app.AllowRag {
			return ErrForbidden
		}
		if e = gatewayTaskCredential(ctx, tx, ws, in.CredentialID, in.ApplicationID); e != nil {
			return e
		}
		var now time.Time
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		if !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(7*24*time.Hour)) {
			return ErrInvalid
		}
		out = GatewayTask{GatewayTaskInput: in, Status: "active", Revision: 1, ObservedCategories: []string{}, Sources: []gatewaygovernance.TaskSource{}}
		out.ID, e = gatewayRandomID()
		if e != nil {
			return e
		}
		out.RootTaskID = out.ID
		parentDigest := ""
		if in.ParentTaskID != "" {
			chain, e := gatewayTaskChain(ctx, tx, ws, in.ParentTaskID)
			if e != nil {
				return e
			}
			parent := chain[len(chain)-1]
			for _, p := range chain {
				if p.Status != "active" || !p.ExpiresAt.After(now) || p.ApplicationID != in.ApplicationID || p.PrincipalID != in.PrincipalID || p.CatalogDigest != in.CatalogDigest {
					return ErrForbidden
				}
				if e = gatewayTaskCredential(ctx, tx, ws, p.CredentialID, p.ApplicationID); e != nil {
					return e
				}
				if in.MaxSteps > p.MaxSteps-p.StepsUsed || in.BudgetMicros > p.BudgetMicros-p.ReservedMicros-p.SpentMicros {
					return ErrGatewayBudget
				}
				out.ObservedCategories = gatewayTaskUnion(out.ObservedCategories, p.ObservedCategories)
				out.Sources, e = gatewayMergeTaskSources(out.Sources, p.Sources)
				if e != nil {
					return e
				}
			}
			if parent.MaxDelegationDepth < 1 || parent.Depth >= 4 || in.MaxDelegationDepth >= parent.MaxDelegationDepth || in.Purpose != parent.Purpose || in.ExpiresAt.After(parent.ExpiresAt) || !gatewayTaskSubset(in.ModelIDs, parent.ModelIDs) || !gatewayTaskSubset(in.ToolIDs, parent.ToolIDs) || !gatewayTaskSubset(in.DocumentIDs, parent.DocumentIDs) || !gatewayTaskSubset(in.AllowedCategories, parent.AllowedCategories) || !gatewayTaskSubset(out.ObservedCategories, in.AllowedCategories) {
				return ErrForbidden
			}
			if !gatewayTaskToolSubset(in.ToolScopes, parent.ToolScopes) {
				return ErrForbidden
			}
			out.Depth = parent.Depth + 1
			out.RootTaskID = parent.RootTaskID
			parentDigest = parent.ScopeDigest
		}
		out.ScopeDigest = gatewayDigest(struct {
			Scope  GatewayTaskInput
			Parent string
		}{in, parentDigest})
		raw, _ := json.Marshal(in)
		cats, _ := json.Marshal(out.ObservedCategories)
		sources, _ := json.Marshal(out.Sources)
		_, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_tasks(workspace_id,id,parent_task_id,root_task_id,depth,application_id,principal_id,credential_id,configuration,scope_digest,observed_categories,sources) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, ws, out.ID, in.ParentTaskID, out.RootTaskID, out.Depth, in.ApplicationID, in.PrincipalID, in.CredentialID, raw, out.ScopeDigest, cats, sources)
		if e != nil {
			return storeError(e)
		}
		if e = identityAudit(ctx, tx, ws, actor, "gateway.task_created", out.ID, map[string]any{"scope": in, "scopeDigest": out.ScopeDigest, "rootTaskId": out.RootTaskID, "depth": out.Depth}); e != nil {
			return e
		}
		out, e = gatewayTask(ctx, tx, ws, out.ID)
		return e
	})
	return
}

func (s *Store) GatewayTasks(ctx context.Context, ws, actor int64, after string, limit int) (out GatewayTaskPage, err error) {
	if after != "" && !gatewayRuntimeID.MatchString(after) || limit < 1 || limit > 200 {
		return out, ErrInvalid
	}
	out.Items = []GatewayTask{}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT `+gatewayTaskColumns+` FROM plume.ai_gateway_tasks WHERE workspace_id=$1 AND id>$2 ORDER BY id LIMIT $3`, ws, after, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			t, e := scanGatewayTask(rows)
			if e != nil {
				return e
			}
			if len(out.Items) == limit {
				out.HasMore = true
				break
			}
			out.Items = append(out.Items, t)
			out.NextCursor = t.ID
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if !out.HasMore {
			out.NextCursor = ""
		}
		return nil
	})
	return
}

func (s *Store) GatewayTaskDetail(ctx context.Context, ws, actor int64, id string, limit int) (out GatewayTaskDetail, err error) {
	if !gatewayRuntimeID.MatchString(id) || limit < 1 || limit > 200 {
		return out, ErrInvalid
	}
	out.Steps = []GatewayTaskStep{}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out.Task, e = gatewayTask(ctx, tx, ws, id)
		if e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT id,step_id,step_digest,kind,model_id,tool_id,document_ids,categories,status,sources,created_at,completed_at FROM plume.ai_gateway_task_steps WHERE workspace_id=$1 AND task_id=$2 ORDER BY id DESC LIMIT $3`, ws, id, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var step GatewayTaskStep
			var docs, cats, sources []byte
			if e = rows.Scan(&step.ID, &step.StepID, &step.StepDigest, &step.Kind, &step.ModelID, &step.ToolID, &docs, &cats, &step.Status, &sources, &step.CreatedAt, &step.CompletedAt); e != nil {
				return e
			}
			if json.Unmarshal(docs, &step.DocumentIDs) != nil || json.Unmarshal(cats, &step.Categories) != nil || json.Unmarshal(sources, &step.SourceEvidence) != nil {
				return ErrInvalid
			}
			if len(out.Steps) == limit {
				out.StepsTruncated = true
				break
			}
			out.Steps = append(out.Steps, step)
		}
		return rows.Err()
	})
	return
}

func (s *Store) SetGatewayTaskStatus(ctx context.Context, ws, actor int64, id string, in GatewayTaskStatusInput) (out GatewayTask, err error) {
	if !gatewayRuntimeID.MatchString(id) || in.ExpectedRevision < 1 || !slices.Contains([]string{"cancelled", "completed"}, in.Status) || !gatewayText(in.Reason, 240) {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := gatewayRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		if e := gatewayTaskLock(ctx, tx, ws); e != nil {
			return e
		}
		t, e := gatewayTask(ctx, tx, ws, id)
		if e != nil {
			return e
		}
		if t.Revision != in.ExpectedRevision || t.Status != "active" {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, `WITH RECURSIVE tree AS (SELECT id FROM plume.ai_gateway_tasks WHERE workspace_id=$1 AND id=$2 UNION ALL SELECT c.id FROM plume.ai_gateway_tasks c JOIN tree p ON c.parent_task_id=p.id WHERE c.workspace_id=$1) UPDATE plume.ai_gateway_tasks SET status=$3,revision=revision+1,updated_at=clock_timestamp() WHERE workspace_id=$1 AND id IN (SELECT id FROM tree) AND status='active'`, ws, id, in.Status)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `WITH RECURSIVE tree AS (SELECT id FROM plume.ai_gateway_tasks WHERE workspace_id=$1 AND id=$2 UNION ALL SELECT c.id FROM plume.ai_gateway_tasks c JOIN tree p ON c.parent_task_id=p.id WHERE c.workspace_id=$1) UPDATE plume.ai_security_approvals SET status='invalidated' WHERE workspace_id=$1 AND request_metadata->>'taskId' IN (SELECT id FROM tree) AND status IN ('proposed','approved')`, ws, id)
		if e != nil {
			return e
		}
		if e = identityAudit(ctx, tx, ws, actor, "gateway.task_"+in.Status, id, map[string]any{"reason": in.Reason, "scopeDigest": t.ScopeDigest, "revision": t.Revision + 1, "descendantsStopped": true, "sideEffectsRolledBack": false}); e != nil {
			return e
		}
		out, e = gatewayTask(ctx, tx, ws, id)
		return e
	})
	return
}
