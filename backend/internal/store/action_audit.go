package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const actionAuditLimit = 500

type ActionAuditEntry struct {
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	TargetType string    `json:"targetType"`
	TargetID   int64     `json:"targetId"`
	Automated  bool      `json:"automated,omitempty"`
}

type ActionAuditPage struct {
	Items     []ActionAuditEntry `json:"items"`
	Limit     int                `json:"limit"`
	Truncated bool               `json:"truncated,omitempty"`
}

func (s *Store) ActionAudit(ctx context.Context, workspaceID int64, hours int) (ActionAuditPage, error) {
	switch hours {
	case 24, 168, 720:
	default:
		return ActionAuditPage{}, ErrInvalid
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	rows, err := s.pool.Query(ctx, `
		WITH actions AS (
			SELECT e.created_at AS at, e.actor_id, 'check.' || e.action AS action, 'check' AS target_type, e.check_id AS target_id, e.id,
			       ((e.action = 'evidence' AND e.note LIKE '自动采集 · %')
			       OR (e.action = 'created' AND e.note LIKE '%自动%生成%')) AS automated
			FROM plume.check_events e JOIN plume.checks c ON c.id = e.check_id
			WHERE c.workspace_id = $1 AND e.created_at >= $2
			UNION ALL
			SELECT created_at, actor_id,
			       CASE WHEN origin = 'automatic' THEN 'classification.detected' ELSE 'classification.reviewed' END,
			       'asset', asset_id, id, origin = 'automatic'
			FROM plume.asset_classification_history WHERE workspace_id = $1 AND created_at >= $2
			UNION ALL
			SELECT created_at, actor_id, 'risk.triaged', 'runtime_finding', finding_id, id, false
			FROM plume.runtime_risk_triage WHERE workspace_id = $1 AND created_at >= $2
			UNION ALL
			SELECT created_at, actor_id, 'api.scanned', 'api_scan', id, id, false
			FROM plume.api_security_scans WHERE workspace_id = $1 AND created_at >= $2
			UNION ALL
			SELECT created_at, actor_id, 'runtime.source_created', 'runtime_source', id, id, false
			FROM plume.runtime_security_sources WHERE workspace_id = $1 AND created_at >= $2
			UNION ALL
			SELECT revoked_at, revoked_by, 'runtime.source_revoked', 'runtime_source', id, id, false
			FROM plume.runtime_security_sources WHERE workspace_id = $1 AND revoked_at >= $2 AND revoked_by IS NOT NULL
		)
		SELECT a.at, u.username, a.action, a.target_type, a.target_id, a.automated
		FROM actions a JOIN plume.users u ON u.id = a.actor_id
		ORDER BY a.at DESC, a.action, a.id DESC LIMIT $3
	`, workspaceID, cutoff, actionAuditLimit+1)
	if err != nil {
		return ActionAuditPage{}, err
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ActionAuditEntry, error) {
		var entry ActionAuditEntry
		err := row.Scan(&entry.At, &entry.Actor, &entry.Action, &entry.TargetType, &entry.TargetID, &entry.Automated)
		return entry, err
	})
	if err != nil {
		return ActionAuditPage{}, err
	}
	if entries == nil {
		entries = []ActionAuditEntry{}
	}
	return ActionAuditPage{Items: entries[:min(len(entries), actionAuditLimit)], Limit: actionAuditLimit, Truncated: len(entries) > actionAuditLimit}, nil
}
