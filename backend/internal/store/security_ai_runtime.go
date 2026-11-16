package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/jackc/pgx/v5"
)

var ErrAIRuntimeSourceLimit = errors.New("AI runtime active source limit reached")
var ErrAIRuntimeReceiptLimit = errors.New("AI runtime source receipt limit reached")

type AISecurityRuntimeSourceInput struct {
	Name          string `json:"name"`
	ApplicationID string `json:"applicationId"`
	CatalogDigest string `json:"catalogDigest"`
}
type AISecurityRuntimeSource struct {
	AISecurityRuntimeSourceInput
	ID           int64      `json:"id"`
	CreatedAt    time.Time  `json:"createdAt"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
	LastSeen     *time.Time `json:"lastSeen,omitempty"`
	ReceiptCount int        `json:"receiptCount"`
	Current      bool       `json:"current"`
}
type AISecurityRuntimeEvent struct {
	ID                   int64                  `json:"id"`
	SourceID             int64                  `json:"sourceId"`
	EventID              string                 `json:"eventId"`
	ApplicationID        string                 `json:"applicationId"`
	CatalogDigest        string                 `json:"catalogDigest"`
	RequestDigest        string                 `json:"requestDigest"`
	Kind                 string                 `json:"kind"`
	Outcome              string                 `json:"outcome"`
	ExecutionStatus      string                 `json:"executionStatus"`
	OccurredAt           time.Time              `json:"occurredAt"`
	ReceivedAt           time.Time              `json:"receivedAt"`
	Stage                string                 `json:"stage"`
	Current              bool                   `json:"current"`
	RuntimeCompatibility string                 `json:"runtimeCompatibility"`
	Record               aisecurity.AuditRecord `json:"record"`
}
type AISecurityRuntimeEvidence struct {
	Sources  []AISecurityRuntimeSource  `json:"sources"`
	Events   []AISecurityRuntimeEvent   `json:"events"`
	Limit    int                        `json:"limit"`
	Coverage []string                   `json:"coverage"`
	Requests []AISecurityRuntimeRequest `json:"requests"`
}
type AISecurityRuntimeReceipt struct {
	ID       int64 `json:"id"`
	Replayed bool  `json:"replayed"`
}

func securityHex(value string, n int) bool {
	if len(value) != n || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func publishedAISecurityCatalog(ctx context.Context, tx pgx.Tx, ws int64) (aisecurity.Config, string, error) {
	var config aisecurity.Config
	var digest string
	err := tx.QueryRow(ctx, `SELECT published_configuration,published_digest FROM plume.ai_security_configurations WHERE workspace_id=$1 AND published_revision>0`, ws).Scan(&config, &digest)
	if err != nil {
		return config, digest, storeError(err)
	}
	if !securityHex(digest, 64) || aisecurity.ConfigDigest(config) != digest {
		return config, digest, ErrConflict
	}
	return config, digest, nil
}

func (s *Store) CreateAISecurityRuntimeSource(ctx context.Context, ws, actor int64, in AISecurityRuntimeSourceInput, tokenHash string) (AISecurityRuntimeSource, error) {
	out := AISecurityRuntimeSource{AISecurityRuntimeSourceInput: in}
	if !securityText(in.Name, 160, true) || !securityText(in.ApplicationID, 160, true) || !securityHex(in.CatalogDigest, 64) || !securityHex(tokenHash, 64) {
		return out, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := runtimeOwner(ctx, tx, ws, actor); err != nil {
			return err
		}
		config, digest, err := publishedAISecurityCatalog(ctx, tx, ws)
		if err != nil {
			return err
		}
		if digest != in.CatalogDigest {
			return ErrConflict
		}
		if !slices.ContainsFunc(config.Applications, func(a aisecurity.Application) bool { return a.ID == in.ApplicationID && a.Enabled }) {
			return ErrInvalid
		}
		// Serialize the bounded workspace source allocation with existing runtime management.
		if _, err = tx.Exec(ctx, `SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE`, ws); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_security_runtime_sources WHERE workspace_id=$1 AND revoked_at IS NULL`, ws).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return ErrAIRuntimeSourceLimit
		}
		out.Current = true
		return tx.QueryRow(ctx, `INSERT INTO plume.ai_security_runtime_sources(workspace_id,created_by,name,application_id,catalog_digest,token_hash) VALUES($1,$2,$3,$4,$5,$6) RETURNING id,created_at`, ws, actor, in.Name, in.ApplicationID, in.CatalogDigest, tokenHash).Scan(&out.ID, &out.CreatedAt)
	})
	return out, storeError(err)
}

func (s *Store) RevokeAISecurityRuntimeSource(ctx context.Context, ws, actor, id int64) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := runtimeOwner(ctx, tx, ws, actor); err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `UPDATE plume.ai_security_runtime_sources SET revoked_at=COALESCE(revoked_at,now()) WHERE workspace_id=$1 AND id=$2`, ws, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) AISecurityRuntimeEvidence(ctx context.Context, ws, actor int64) (AISecurityRuntimeEvidence, error) {
	out := AISecurityRuntimeEvidence{Sources: []AISecurityRuntimeSource{}, Events: []AISecurityRuntimeEvent{}, Requests: []AISecurityRuntimeRequest{}, Limit: 100, Coverage: []string{"运行回执由独立令牌认证的客户侧来源提交，不等于平台独立复测或人工核验。", "prepared表示发出前的持久化准备记录；仅有prepared不能证明操作成功。", "旧配置、已撤销来源的记录保留历史，不作为当前控制有效的证明。", "请求链仅关联本次最近100条回执；缺失阶段可能来自采样窗口、未上报或执行中断，不能据此推断未发生副作用。", "执行器与检测版本由来源报告，匹配当前支持版本不等于已远程证明实际二进制或运行环境。"}}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := securityPlatformMember(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT s.id,s.name,s.application_id,s.catalog_digest,s.created_at,s.revoked_at,s.last_seen,s.receipt_count,COALESCE(s.revoked_at IS NULL AND c.published_digest=s.catalog_digest AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.created_by AND m.role='owner'),false) FROM plume.ai_security_runtime_sources s LEFT JOIN plume.ai_security_configurations c ON c.workspace_id=s.workspace_id WHERE s.workspace_id=$1 ORDER BY (s.revoked_at IS NULL) DESC,s.id DESC LIMIT 100`, ws)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item AISecurityRuntimeSource
			if err = rows.Scan(&item.ID, &item.Name, &item.ApplicationID, &item.CatalogDigest, &item.CreatedAt, &item.RevokedAt, &item.LastSeen, &item.ReceiptCount, &item.Current); err != nil {
				rows.Close()
				return err
			}
			out.Sources = append(out.Sources, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT e.id,s.id,e.event_id,e.record,e.received_at,COALESCE(s.revoked_at IS NULL AND c.published_digest=s.catalog_digest AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.created_by AND m.role='owner'),false) FROM plume.ai_security_runtime_events e JOIN plume.ai_security_runtime_sources s ON s.id=e.source_id LEFT JOIN plume.ai_security_configurations c ON c.workspace_id=s.workspace_id WHERE s.workspace_id=$1 ORDER BY e.id DESC LIMIT 100`, ws)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e AISecurityRuntimeEvent
			if err = rows.Scan(&e.ID, &e.SourceID, &e.EventID, &e.Record, &e.ReceivedAt, &e.Current); err != nil {
				rows.Close()
				return err
			}
			e.ApplicationID = e.Record.Decision.ApplicationID
			e.CatalogDigest = e.Record.Decision.CatalogDigest
			e.RequestDigest = e.Record.Decision.RequestDigest
			e.Kind = e.Record.Decision.Kind
			e.Outcome = e.Record.Decision.Outcome
			e.ExecutionStatus = e.Record.Execution.Outcome
			e.OccurredAt = e.Record.ObservedAt
			e.Stage = e.Record.Stage
			e.RuntimeCompatibility = aiRuntimeCompatibility(e.Record)
			out.Events = append(out.Events, e)
		}
		err = rows.Err()
		rows.Close()
		return err
	})
	if err == nil {
		out.Requests = aiRuntimeRequests(out.Events)
	}
	return out, storeError(err)
}

func validAISecurityRuntimeRecord(in aisecurity.AuditRecord, now time.Time) bool {
	if in.AttemptID != "" && !securityHex(in.AttemptID, 32) {
		return false
	}
	if !validAIRuntimeVersions(in) || !validAIRuntimePhase(in) || !validGatewayRuntimeEvidence(in) {
		return false
	}
	if !securityHex(in.EventID, 32) || !securityText(in.RequestID, 160, true) || !securityHex(in.Decision.CatalogDigest, 64) || !securityHex(in.Decision.RequestDigest, 64) || !securityText(in.Decision.ApplicationID, 160, true) || !securityText(in.Decision.PrincipalID, 160, true) || !securityText(in.Decision.Code, 160, true) || !securityText(in.Decision.CatalogVersion, 160, true) {
		return false
	}
	if !slices.Contains([]string{"rag", "model", "tool", "output"}, in.Decision.Kind) || !slices.Contains([]string{"decision", "prepared", "completed"}, in.Stage) || !slices.Contains([]string{"allow", "deny", "approval_required", "unknown"}, in.Decision.Outcome) || !slices.Contains([]string{"not_attempted", "succeeded", "failed", "unknown", "blocked_output"}, in.Execution.Outcome) || in.Execution.HTTPStatus < 0 || in.Execution.HTTPStatus > 599 || (in.Execution.HTTPStatus > 0 && in.Execution.HTTPStatus < 100) {
		return false
	}
	if in.ObservedAt.IsZero() || in.ObservedAt.After(now.Add(5*time.Minute)) || in.ObservedAt.Before(now.Add(-7*24*time.Hour)) {
		return false
	}
	if len(in.Decision.Rules) > 64 || len(in.Decision.AllowedDocumentIDs) > 100 || len(in.Decision.Categories) > 100 || len(in.Decision.Coverage) > 32 {
		return false
	}
	for _, items := range [][]string{in.Decision.Rules, in.Decision.AllowedDocumentIDs, in.Decision.Categories, in.Decision.Coverage} {
		for _, item := range items {
			if !securityText(item, 2000, false) {
				return false
			}
		}
	}
	return true
}

func (s *Store) ReceiveAISecurityRuntimeEvent(ctx context.Context, tokenHash string, in aisecurity.AuditRecord) (AISecurityRuntimeReceipt, error) {
	var out AISecurityRuntimeReceipt
	if !securityHex(tokenHash, 64) || !validAISecurityRuntimeRecord(in, time.Now()) {
		return out, ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var id int64
		var app, digest string
		var count int
		err := tx.QueryRow(ctx, `SELECT s.id,s.application_id,s.catalog_digest,s.receipt_count FROM plume.ai_security_runtime_sources s WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.created_by AND m.role='owner') FOR UPDATE`, tokenHash).Scan(&id, &app, &digest, &count)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAutomationUnauthorized
		}
		if err != nil {
			return storeError(err)
		}
		if app != in.Decision.ApplicationID || digest != in.Decision.CatalogDigest {
			return ErrForbidden
		}
		body, err := json.Marshal(in)
		if err != nil {
			return err
		}
		payloadDigest := assessmentDigest(scenarioCanonicalJSON(body))
		var previous string
		err = tx.QueryRow(ctx, `SELECT id,payload_digest FROM plume.ai_security_runtime_events WHERE source_id=$1 AND event_id=$2`, id, in.EventID).Scan(&out.ID, &previous)
		if err == nil {
			if previous != payloadDigest {
				return ErrConflict
			}
			out.Replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if count >= 20000 {
			return ErrAIRuntimeReceiptLimit
		}
		if err = tx.QueryRow(ctx, `INSERT INTO plume.ai_security_runtime_events(source_id,event_id,payload_digest,record) VALUES($1,$2,$3,$4) RETURNING id`, id, in.EventID, payloadDigest, body).Scan(&out.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE plume.ai_security_runtime_sources SET receipt_count=receipt_count+1,last_seen=now() WHERE id=$1`, id)
		return err
	})
	return out, storeError(err)
}
