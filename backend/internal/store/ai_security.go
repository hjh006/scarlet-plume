package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

func aiConfiguration(ctx context.Context, tx pgx.Tx, ws int64, lock bool) (AISecurityConfiguration, error) {
	v := AISecurityConfiguration{Configuration: aisecurity.DefaultConfig()}
	var raw []byte
	query := `SELECT revision,configuration,updated_by,updated_at,published_revision,published_digest,published_at FROM plume.ai_security_configurations WHERE workspace_id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	e := tx.QueryRow(ctx, query, ws).Scan(&v.Revision, &raw, &v.UpdatedBy, &v.UpdatedAt, &v.PublishedRevision, &v.PublishedDigest, &v.PublishedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(raw, &v.Configuration)
	v.Configuration.Approvals = []aisecurity.Approval{}
	return v, e
}
func aiEnsure(ctx context.Context, tx pgx.Tx, ws, actor int64) error {
	raw, _ := json.Marshal(aisecurity.DefaultConfig())
	_, e := tx.Exec(ctx, `INSERT INTO plume.ai_security_configurations(workspace_id,configuration,updated_by) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, ws, raw, actor)
	return e
}
func aiEditor(ctx context.Context, tx pgx.Tx, ws, actor int64) error {
	role, e := checkRole(ctx, tx, ws, actor, true)
	if e != nil {
		return e
	}
	if !checkEditor(role) {
		return ErrForbidden
	}
	return nil
}
func (s *Store) SaveAISecurityConfiguration(ctx context.Context, ws, actor, expected int64, cfg aisecurity.Config) (out AISecurityConfiguration, err error) {
	if expected < 0 || len(cfg.Approvals) > 0 {
		return out, ErrInvalid
	}
	cfg.Approvals = []aisecurity.Approval{}
	if aisecurity.ValidateConfig(cfg) != nil {
		return out, ErrInvalid
	}
	raw, e := json.Marshal(cfg)
	if e != nil || len(raw) > 1<<20 {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := aiEditor(ctx, tx, ws, actor); e != nil {
			return e
		}
		if e := aiEnsure(ctx, tx, ws, actor); e != nil {
			return e
		}
		v, e := aiConfiguration(ctx, tx, ws, true)
		if e != nil {
			return e
		}
		if v.Revision != expected {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, `UPDATE plume.ai_security_configurations SET revision=revision+1,configuration=$2,updated_by=$3,updated_at=now() WHERE workspace_id=$1`, ws, raw, actor)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE plume.ai_security_approvals SET status='invalidated' WHERE workspace_id=$1 AND status IN ('proposed','approved')`, ws)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_task_lease_proposals SET status='invalidated',revision=revision+1,note='当前AI目录配置已变化。' WHERE workspace_id=$1 AND status='proposed'`, ws); e != nil {
			return e
		}
		out, e = aiConfiguration(ctx, tx, ws, false)
		return e
	})
	return
}

const aiApprovalColumns = `id,configuration_revision,request_digest,catalog_digest,request_metadata,status,requested_by,reviewed_by,expires_at,created_at,note,COALESCE(task_lease_digest,'')`

func aiScanApproval(row pgx.Row) (v AISecurityApproval, e error) {
	var raw []byte
	e = row.Scan(&v.ID, &v.ConfigurationRevision, &v.RequestDigest, &v.CatalogDigest, &raw, &v.Status, &v.RequestedBy, &v.ReviewedBy, &v.ExpiresAt, &v.CreatedAt, &v.Note, &v.LeaseChainDigest)
	if e != nil {
		return v, storeError(e)
	}
	var r aisecurity.Request
	if e = json.Unmarshal(raw, &r); e != nil {
		return
	}
	if v.LeaseChainDigest != "" {
		v.LeaseProtocolVersion = gatewaygovernance.TaskLeaseProtocolVersion
	}
	v.RequestID = r.RequestID
	v.TaskID = r.TaskID
	v.IssuedAt = r.IssuedAt
	v.ApplicationID = r.ApplicationID
	v.PrincipalID = r.PrincipalID
	v.ToolID = r.ToolID
	v.Action = r.Action
	v.Target = r.Target
	v.Arguments = r.Arguments
	return
}
func aiApproval(ctx context.Context, tx pgx.Tx, ws, id int64) (AISecurityApproval, error) {
	return aiScanApproval(tx.QueryRow(ctx, `SELECT `+aiApprovalColumns+` FROM plume.ai_security_approvals WHERE workspace_id=$1 AND id=$2`, ws, id))
}
func aiApprovals(ctx context.Context, tx pgx.Tx, ws, revision int64) ([]AISecurityApproval, error) {
	rows, e := tx.Query(ctx, `SELECT `+aiApprovalColumns+` FROM plume.ai_security_approvals WHERE workspace_id=$1 ORDER BY id DESC LIMIT 100`, ws)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []AISecurityApproval{}
	for rows.Next() {
		v, e := aiScanApproval(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func aiAttachApprovals(ctx context.Context, tx pgx.Tx, ws, revision int64, cfg *aisecurity.Config) error {
	digest := aisecurity.ConfigDigest(*cfg)
	rows, e := tx.Query(ctx, `SELECT `+aiApprovalColumns+` FROM plume.ai_security_approvals WHERE workspace_id=$1 AND configuration_revision=$2 AND catalog_digest=$3 AND status='approved' AND expires_at>now() ORDER BY id LIMIT 1001`, ws, revision, digest)
	if e != nil {
		return e
	}
	defer rows.Close()
	cfg.Approvals = []aisecurity.Approval{}
	for rows.Next() {
		v, e := aiScanApproval(rows)
		if e != nil {
			return e
		}
		if len(cfg.Approvals) >= 1000 {
			return ErrRuntimeLimit
		}
		if v.ReviewedBy == nil {
			continue
		}
		cfg.Approvals = append(cfg.Approvals, aisecurity.Approval{ID: fmt.Sprint(v.ID), LeaseChainDigest: v.LeaseChainDigest, LeaseProtocolVersion: v.LeaseProtocolVersion, RequestDigest: v.RequestDigest, CatalogDigest: v.CatalogDigest, ApplicationID: v.ApplicationID, PrincipalID: v.PrincipalID, ToolID: v.ToolID, Action: v.Action, Target: v.Target, ApprovedBy: fmt.Sprintf("user:%d", *v.ReviewedBy), ExpiresAt: v.ExpiresAt})
	}
	return rows.Err()
}
func (s *Store) AISecurityBundle(ctx context.Context, ws, actor int64) (out AISecurityBundle, err error) {
	out.Decisions = []AISecurityDecisionRecord{}
	out.Evaluations = []AISecurityEvaluation{}
	out.Approvals = []AISecurityApproval{}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		v, e := aiConfiguration(ctx, tx, ws, false)
		if e != nil {
			return e
		}
		out.AISecurityConfiguration = v
		rows, e := tx.Query(ctx, `SELECT id,configuration_revision,decision,created_at FROM plume.ai_security_decisions WHERE workspace_id=$1 ORDER BY id DESC LIMIT 100`, ws)
		if e != nil {
			return e
		}
		for rows.Next() {
			var d AISecurityDecisionRecord
			var raw []byte
			if e = rows.Scan(&d.ID, &d.ConfigurationRevision, &raw, &d.CreatedAt); e != nil {
				rows.Close()
				return e
			}
			if e = json.Unmarshal(raw, &d.Decision); e != nil {
				rows.Close()
				return e
			}
			d.Mode = "evaluation"
			out.Decisions = append(out.Decisions, d)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		rows, e = tx.Query(ctx, `SELECT id,result,created_at FROM plume.ai_security_evaluations WHERE workspace_id=$1 ORDER BY id DESC LIMIT 30`, ws)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id int64
			var raw []byte
			var at time.Time
			if e = rows.Scan(&id, &raw, &at); e != nil {
				rows.Close()
				return e
			}
			var batch AISecurityEvaluation
			if e = json.Unmarshal(raw, &batch); e != nil {
				rows.Close()
				return e
			}
			batch.ID = id
			batch.CreatedAt = at
			out.Evaluations = append(out.Evaluations, batch)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		out.Approvals, e = aiApprovals(ctx, tx, ws, v.Revision)
		if e != nil {
			return e
		}
		out.HistoryCoverage = map[string]AISecurityHistoryWindow{}
		for _, item := range []struct {
			collection, table string
			limit, count      int
			last              int64
		}{
			{"decisions", "plume.ai_security_decisions", 100, len(out.Decisions), 0},
			{"evaluations", "plume.ai_security_evaluations", 30, len(out.Evaluations), 0},
			{"approvals", "plume.ai_security_approvals", 100, len(out.Approvals), 0},
		} {
			window := AISecurityHistoryWindow{Limit: item.limit}
			if item.count == item.limit {
				switch item.collection {
				case "decisions":
					item.last = out.Decisions[item.count-1].ID
				case "evaluations":
					item.last = out.Evaluations[item.count-1].ID
				case "approvals":
					item.last = out.Approvals[item.count-1].ID
				}
				if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+item.table+` WHERE workspace_id=$1 AND id<$2)`, ws, item.last).Scan(&window.HasMore); e != nil {
					return e
				}
				if window.HasMore {
					window.NextCursor = aiOperationsNext(item.last, aiHistoryScope(item.collection, ws, AISecurityHistoryQuery{}))
				}
			}
			out.HistoryCoverage[item.collection] = window
		}
		c := v.Configuration
		out.Coverage = AISecurityCoverage{Mode: "evaluation", Assets: len(c.Applications) + len(c.Documents) + len(c.Models) + len(c.Tools), TrustedSources: 0, Limitations: []string{"服务端目录由授权人员配置，不等于已验证的源端资产/ACL观察。", "控制台请求的应用和主体是评估声明，不是运行时认证身份。", "决策与评测不表示实际检索、模型调用或工具执行；真实执行需客户侧接入及独立来源证据。", "最近100条决策、30批评测与100条审批预览，导出采用相同有界范围。"}}
		return nil
	})
	return
}
func (s *Store) EvaluateAISecurity(ctx context.Context, ws, actor int64, r aisecurity.Request) (out AISecurityDecisionRecord, err error) {
	if len(r.Input)+len(r.Output) > 2<<20 {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := aiEditor(ctx, tx, ws, actor); e != nil {
			return e
		}
		v, e := aiConfiguration(ctx, tx, ws, true)
		if e != nil {
			return e
		}
		if e = aiAttachApprovals(ctx, tx, ws, v.Revision, &v.Configuration); e != nil {
			return e
		}
		out.Decision = aisecurity.Evaluate(v.Configuration, r, time.Now().UTC())
		out.ConfigurationRevision = v.Revision
		out.Mode = "evaluation"
		raw, e := json.Marshal(out.Decision)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, `INSERT INTO plume.ai_security_decisions(workspace_id,actor_id,configuration_revision,decision) VALUES($1,$2,$3,$4) RETURNING id,created_at`, ws, actor, v.Revision, raw).Scan(&out.ID, &out.CreatedAt)
	})
	return
}
func (s *Store) PublishAISecurity(ctx context.Context, ws, actor, expected, evaluationID int64, note string) (out AISecurityConfiguration, err error) {
	if expected < 1 || evaluationID < 1 || len(strings.TrimSpace(note)) < 1 || len(note) > 1000 {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, e := checkRole(ctx, tx, ws, actor, true)
		if e != nil {
			return e
		}
		if role != "owner" && role != "reviewer" {
			return ErrForbidden
		}
		if _, e = tx.Exec(ctx, `SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE`, ws); e != nil {
			return e
		}
		v, e := aiConfiguration(ctx, tx, ws, true)
		if e != nil {
			return e
		}
		if v.Revision != expected {
			return ErrConflict
		}
		if v.UpdatedBy == actor {
			return ErrForbidden
		}
		var revision int64
		var digest, status string
		e = tx.QueryRow(ctx, `SELECT configuration_revision,catalog_digest,status FROM plume.ai_security_evaluations WHERE workspace_id=$1 AND id=$2`, ws, evaluationID).Scan(&revision, &digest, &status)
		if e != nil {
			return storeError(e)
		}
		if revision != v.Revision || digest != aisecurity.ConfigDigest(v.Configuration) || status != "passed" {
			return ErrEvidenceRequired
		}
		gate, e := aiLoadEvaluation(ctx, tx, ws, evaluationID)
		if e != nil {
			return e
		}
		if gate.GateVersion != AISecurityRegressionGate || !gate.GateEligible || gate.EngineVersion != aisecurity.RuntimeProtocolVersion || gate.InspectionVersion != aisecurity.InspectionVersion || gate.Pack == nil {
			return ErrEvidenceRequired
		}
		pack, summary, e := aiLoadPack(ctx, tx, ws, gate.Pack.ID)
		if e != nil {
			return e
		}
		if summary.Digest != gate.Pack.Digest {
			return ErrEvidenceRequired
		}
		var latestPack int64
		if e = tx.QueryRow(ctx, `SELECT id FROM plume.ai_security_regression_packs WHERE workspace_id=$1 AND pack_id=$2 ORDER BY id DESC LIMIT 1`, ws, summary.PackID).Scan(&latestPack); e != nil {
			return e
		}
		if latestPack != summary.ID {
			return ErrEvidenceRequired
		}
		now := time.Now().UTC()
		if !aiEvaluateRegression(v.Configuration, v.Revision, pack, summary, now).GateEligible {
			return ErrEvidenceRequired
		}
		review, _ := json.Marshal([]AISecurityPublicationReview{{ActorID: actor, ConfigurationRevision: v.Revision, Note: strings.TrimSpace(note), CreatedAt: now, PackDigest: summary.Digest, EngineVersion: aisecurity.RuntimeProtocolVersion, RecheckedAt: &now}})
		_, e = tx.Exec(ctx, `UPDATE plume.ai_security_evaluations SET result=jsonb_set(result,'{publicationReviews}',COALESCE(result->'publicationReviews','[]'::jsonb)||$3::jsonb) WHERE workspace_id=$1 AND id=$2`, ws, evaluationID, review)
		if e != nil {
			return e
		}
		raw, _ := json.Marshal(v.Configuration)
		_, e = tx.Exec(ctx, `UPDATE plume.ai_security_configurations SET published_revision=revision,published_configuration=$2,published_digest=$3,published_by=$4,published_at=now() WHERE workspace_id=$1`, ws, raw, digest, actor)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_task_lease_proposals SET status='invalidated',revision=revision+1,note='当前AI目录配置已变化。' WHERE workspace_id=$1 AND status='proposed'`, ws); e != nil {
			return e
		}
		out, e = aiConfiguration(ctx, tx, ws, false)
		return e
	})
	return
}
func (s *Store) AISecurityRuntimeConfig(ctx context.Context, ws, actor int64) (out aisecurity.Config, revision int64, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var raw []byte
		e := tx.QueryRow(ctx, `SELECT published_revision,published_configuration FROM plume.ai_security_configurations WHERE workspace_id=$1 AND published_revision>0`, ws).Scan(&revision, &raw)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrConflict
		}
		if e != nil {
			return e
		}
		if e = json.Unmarshal(raw, &out); e != nil {
			return e
		}
		return aiAttachApprovals(ctx, tx, ws, revision, &out)
	})
	return
}
