package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type GatewayTaskStepPage struct {
	Items      []GatewayTaskStep `json:"items"`
	HasMore    bool              `json:"hasMore"`
	NextCursor string            `json:"nextCursor"`
}

func (s *Store) GatewayTaskSteps(ctx context.Context, ws, actor int64, task, cursor string, limit int) (out GatewayTaskStepPage, err error) {
	if !gatewayRuntimeID.MatchString(task) || limit < 1 || limit > 200 {
		return out, ErrInvalid
	}
	scope := aiOperationsScope("task-steps", ws, task)
	before, err := aiOperationsBefore(cursor, scope)
	if err != nil {
		return out, err
	}
	out.Items = []GatewayTaskStep{}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		if _, e := gatewayTask(ctx, tx, ws, task); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT id,step_id,step_digest,kind,model_id,tool_id,document_ids,categories,status,sources,created_at,completed_at FROM plume.ai_gateway_task_steps WHERE workspace_id=$1 AND task_id=$2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT $4`, ws, task, before, limit+1)
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
			if len(out.Items) == limit {
				out.HasMore = true
				break
			}
			out.Items = append(out.Items, step)
		}
		return rows.Err()
	})
	if err == nil && out.HasMore {
		out.NextCursor = aiOperationsNext(out.Items[len(out.Items)-1].ID, scope)
	}
	return out, storeError(err)
}
