package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5"
)

type AISecurityHistoryQuery struct {
	Before  string
	Limit   int
	Kind    string
	Outcome string
	Status  string
}

type AISecurityHistoryWindow struct {
	Limit      int    `json:"limit"`
	HasMore    bool   `json:"hasMore"`
	NextCursor string `json:"nextCursor"`
}

type AISecurityHistoryPage[T any] struct {
	AISecurityHistoryWindow
	Items []T `json:"items"`
}

func aiHistoryScope(collection string, ws int64, q AISecurityHistoryQuery) string {
	return aiOperationsScope("ai-history:"+collection, ws, q.Kind, q.Outcome, q.Status)
}

func validAIHistoryQuery(collection string, q AISecurityHistoryQuery) bool {
	if q.Limit < 1 || q.Limit > 200 {
		return false
	}
	switch collection {
	case "decisions":
		return q.Status == "" && (q.Kind == "" || slices.Contains([]string{"rag", "model", "tool", "unknown"}, q.Kind)) && (q.Outcome == "" || slices.Contains([]string{"allow", "deny", "approval_required"}, q.Outcome))
	case "approvals":
		return q.Kind == "" && q.Outcome == "" && (q.Status == "" || slices.Contains([]string{"proposed", "approved", "rejected", "invalidated"}, q.Status))
	case "evaluations":
		return q.Kind == "" && q.Outcome == "" && (q.Status == "" || q.Status == "passed" || q.Status == "failed")
	case "regression-packs":
		return q.Kind == "" && q.Outcome == "" && q.Status == ""
	}
	return false
}

// This common reader keeps authorization and cursor scope identical for the four
// persisted histories. It does not evaluate policy or mutate publication gates.
func aiSecurityHistoryPage[T any](ctx context.Context, s *Store, ws, actor int64, collection string, q AISecurityHistoryQuery, query string, filters []any, scan func(pgx.Row) (T, error), id func(T) int64) (out AISecurityHistoryPage[T], err error) {
	if !validAIHistoryQuery(collection, q) {
		return out, ErrInvalid
	}
	scope := aiHistoryScope(collection, ws, q)
	before, err := aiOperationsBefore(q.Before, scope)
	if err != nil {
		return out, err
	}
	out.Limit, out.Items = q.Limit, []T{}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		args := append([]any{ws, before, q.Limit + 1}, filters...)
		rows, e := tx.Query(ctx, query, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			if len(out.Items) == q.Limit {
				out.HasMore = true
				break
			}
			item, e := scan(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, item)
		}
		return rows.Err()
	})
	if err == nil && out.HasMore {
		out.NextCursor = aiOperationsNext(id(out.Items[len(out.Items)-1]), scope)
	}
	return out, storeError(err)
}

func aiScanDecision(row pgx.Row) (out AISecurityDecisionRecord, err error) {
	var raw []byte
	err = row.Scan(&out.ID, &out.ConfigurationRevision, &raw, &out.CreatedAt)
	if err == nil {
		err = json.Unmarshal(raw, &out.Decision)
		out.Mode = "evaluation"
	}
	return out, storeError(err)
}

func aiScanEvaluation(row pgx.Row) (out AISecurityEvaluation, err error) {
	var raw []byte
	var id int64
	err = row.Scan(&id, &raw, &out.CreatedAt)
	if err == nil {
		at := out.CreatedAt
		err = json.Unmarshal(raw, &out)
		out.ID, out.CreatedAt = id, at
	}
	return out, storeError(err)
}

func (s *Store) AISecurityDecisions(ctx context.Context, ws, actor int64, q AISecurityHistoryQuery) (AISecurityHistoryPage[AISecurityDecisionRecord], error) {
	return aiSecurityHistoryPage(ctx, s, ws, actor, "decisions", q, `SELECT id,configuration_revision,decision,created_at FROM plume.ai_security_decisions WHERE workspace_id=$1 AND ($2::bigint=0 OR id<$2) AND ($4::text='' OR decision->>'kind'=$4) AND ($5::text='' OR decision->>'outcome'=$5) ORDER BY id DESC LIMIT $3`, []any{q.Kind, q.Outcome}, aiScanDecision, func(v AISecurityDecisionRecord) int64 { return v.ID })
}

func (s *Store) AISecurityApprovalsPage(ctx context.Context, ws, actor int64, q AISecurityHistoryQuery) (AISecurityHistoryPage[AISecurityApproval], error) {
	return aiSecurityHistoryPage(ctx, s, ws, actor, "approvals", q, `SELECT `+aiApprovalColumns+` FROM plume.ai_security_approvals WHERE workspace_id=$1 AND ($2::bigint=0 OR id<$2) AND ($4::text='' OR status=$4) ORDER BY id DESC LIMIT $3`, []any{q.Status}, aiScanApproval, func(v AISecurityApproval) int64 { return v.ID })
}

func (s *Store) AISecurityEvaluationsPage(ctx context.Context, ws, actor int64, q AISecurityHistoryQuery) (AISecurityHistoryPage[AISecurityEvaluation], error) {
	return aiSecurityHistoryPage(ctx, s, ws, actor, "evaluations", q, `SELECT id,result,created_at FROM plume.ai_security_evaluations WHERE workspace_id=$1 AND ($2::bigint=0 OR id<$2) AND ($4::text='' OR status=$4) ORDER BY id DESC LIMIT $3`, []any{q.Status}, aiScanEvaluation, func(v AISecurityEvaluation) int64 { return v.ID })
}

func (s *Store) AISecurityRegressionPackPage(ctx context.Context, ws, actor int64, q AISecurityHistoryQuery) (AISecurityHistoryPage[AISecurityRegressionPackSummary], error) {
	return aiSecurityHistoryPage(ctx, s, ws, actor, "regression-packs", q, `SELECT id,pack,digest,created_at FROM plume.ai_security_regression_packs WHERE workspace_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT $3`, nil, aiScanPackSummary, func(v AISecurityRegressionPackSummary) int64 { return v.ID })
}

func aiHistoryItem[T any](ctx context.Context, s *Store, ws, actor, id int64, query string, scan func(pgx.Row) (T, error)) (out T, err error) {
	if id < 1 {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out, e = scan(tx.QueryRow(ctx, query, ws, id))
		return e
	})
	return out, storeError(err)
}

func (s *Store) AISecurityDecision(ctx context.Context, ws, actor, id int64) (AISecurityDecisionRecord, error) {
	return aiHistoryItem(ctx, s, ws, actor, id, `SELECT id,configuration_revision,decision,created_at FROM plume.ai_security_decisions WHERE workspace_id=$1 AND id=$2`, aiScanDecision)
}

func (s *Store) AISecurityApproval(ctx context.Context, ws, actor, id int64) (AISecurityApproval, error) {
	return aiHistoryItem(ctx, s, ws, actor, id, `SELECT `+aiApprovalColumns+` FROM plume.ai_security_approvals WHERE workspace_id=$1 AND id=$2`, aiScanApproval)
}

func (s *Store) AISecurityEvaluation(ctx context.Context, ws, actor, id int64) (AISecurityEvaluation, error) {
	return aiHistoryItem(ctx, s, ws, actor, id, `SELECT id,result,created_at FROM plume.ai_security_evaluations WHERE workspace_id=$1 AND id=$2`, aiScanEvaluation)
}
