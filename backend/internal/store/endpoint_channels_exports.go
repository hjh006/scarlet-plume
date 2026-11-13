package store

import (
	"context"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationml"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateEndpointEvidenceExport(ctx context.Context, ws, actor, policyID int64) (EndpointEvidenceExport, error) {
	var result EndpointEvidenceExport
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, false)
		if err != nil {
			return err
		}
		p, err := readEndpointPolicy(ctx, tx, ws, policyID)
		if err != nil {
			return err
		}
		if err = refreshEndpointPolicy(ctx, tx, ws, actor, role, &p); err != nil {
			return err
		}
		d, err := readEndpointDevice(ctx, tx, ws, p.SourceID)
		if err != nil {
			return err
		}
		receipts, err := readEndpointReceipts(ctx, tx, ws, policyID)
		if err != nil {
			return err
		}
		snapshot := EndpointEvidenceSnapshot{Version: 1, Policy: p, Device: d, Receipts: receipts, Events: []OperationsSecurityEvent{}, Boundaries: []string{"心跳仅证明来源在线；批准仅授权策略；applied 回执仅证明执行器声明加载对应版本。", "controlled_operations 仅覆盖经该执行器发起的受控操作，不代表全设备或全部应用阻断。", "操作事件为授权来源声明及可核对的策略、设备和时间证据，不自动清除风险或替代独立复核。", "报告按生成时刻冻结；后续操作、回执和撤销不会改写本快照。"}}
		if p.Scope == "native_control" {
			snapshot.Version = 2
			snapshot.Boundaries = append(snapshot.Boundaries, "native_control仅覆盖冻结targetScope及匿名targetRefs；配置写入、回读、功能验证与恢复分开记录。USB不保证已开句柄即时拒写；暂停指定打印队列不等于拒绝提交且当前作业可能继续；RDP仅剪贴板重定向，不覆盖本地剪贴板。", "configured/readback不证明功能已阻断；functional_verified也仅为授权来源针对所列目标与方法的验证声明。当前未进行Windows实机验收。")
		}
		if p.Scope == "session_response" {
			snapshot.Version = 3
			snapshot.SessionEvents, snapshot.SessionEventsTotal, err = readEndpointSessionEvents(ctx, tx, ws, policyID, 200)
			if err != nil {
				return err
			}
			snapshot.SessionEventsTruncated = snapshot.SessionEventsTotal > int64(len(snapshot.SessionEvents))
			snapshot.SessionCoverageMissing, err = endpointSessionCoverage(ctx, tx, ws, policyID, snapshot.SessionEventsTotal, snapshot.SessionEventsTruncated)
			if err != nil {
				return err
			}
			snapshot.Boundaries = append(snapshot.Boundaries, "session_response仅响应冻结登录会话中的剪贴板变化；loaded仅表示监听载入，stopped仅表示停止后续响应，不恢复正文。cleared_after_change是授权来源报告的变化后清除事实，不代表粘贴前拦截或风险已清除。")
		}
		rows, err := tx.Query(ctx, `SELECT e.id,e.source_id,s.name,COALESCE(e.check_id,0),COALESCE(c.status,''),e.asset_id,e.metadata FROM plume.operations_security_events e JOIN plume.operations_sources s ON s.id=e.source_id LEFT JOIN plume.checks c ON c.id=e.check_id AND c.workspace_id=e.workspace_id WHERE e.workspace_id=$1 AND e.source_id=$2 AND e.metadata->'dataContext'->'endpoint'->>'policyId'=$3 ORDER BY e.id LIMIT 201`, ws, p.SourceID, strconv.FormatInt(policyID, 10))
		if err != nil {
			return err
		}
		for rows.Next() {
			var e OperationsSecurityEvent
			var assetID int64
			if err = rows.Scan(&e.ID, &e.SourceID, &e.Source, &e.CheckID, &e.CheckStatus, &assetID, &e.SecurityEventInput); err != nil {
				rows.Close()
				return err
			}
			e.AssetID = assetID
			snapshot.Events = append(snapshot.Events, e)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(snapshot.Events) > 200 {
			snapshot.Truncated = true
			snapshot.Events = snapshot.Events[:200]
		}
		var receiptCount int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM plume.endpoint_channel_receipts WHERE workspace_id=$1 AND policy_id=$2`, ws, policyID).Scan(&receiptCount); err != nil {
			return err
		}
		snapshot.Truncated = snapshot.Truncated || receiptCount > len(receipts) || snapshot.SessionEventsTruncated
		result.PolicyID = policyID
		result.Snapshot = snapshot
		result.SnapshotDigest = classificationml.Digest(snapshot)
		return tx.QueryRow(ctx, `INSERT INTO plume.endpoint_channel_exports(workspace_id,policy_id,actor_id,actor,snapshot,snapshot_digest) VALUES($1,$2,$3,(SELECT username FROM plume.users WHERE id=$3),$4,$5) RETURNING id,created_at,actor`, ws, policyID, actor, snapshot, result.SnapshotDigest).Scan(&result.ID, &result.CreatedAt, &result.Actor)
	})
	return result, storeError(err)
}
func (s *Store) EndpointEvidenceExport(ctx context.Context, ws, actor, id int64) (EndpointEvidenceExport, error) {
	var result EndpointEvidenceExport
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id,policy_id,created_at,actor,snapshot_digest,snapshot FROM plume.endpoint_channel_exports WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&result.ID, &result.PolicyID, &result.CreatedAt, &result.Actor, &result.SnapshotDigest, &result.Snapshot)
	})
	return result, storeError(err)
}
