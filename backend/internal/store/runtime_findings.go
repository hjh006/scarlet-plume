package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

const (
	maxRuntimeFindingCandidates = 50
	maxRuntimeFindingCycles     = 250
)

type RuntimeFinding struct {
	ID              int64        `json:"id"`
	Service         string       `json:"service"`
	Method          string       `json:"method"`
	Route           string       `json:"route"`
	Rule            string       `json:"rule"`
	SourceID        int64        `json:"sourceId"`
	Source          string       `json:"source"`
	BaselineScanID  int64        `json:"baselineScanId"`
	CheckID         int64        `json:"checkId"`
	CheckStatus     string       `json:"checkStatus"`
	FirstSeen       time.Time    `json:"firstSeen"`
	LastSeen        time.Time    `json:"lastSeen"`
	PreviousCheckID *int64       `json:"previousCheckId,omitempty"`
	RetestAt        *time.Time   `json:"retestAt,omitempty"`
	Risk            *RuntimeRisk `json:"risk,omitempty"`
}

func (s *Store) ListRuntimeFindings(ctx context.Context, workspaceID int64) ([]RuntimeFinding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.id, f.service, f.method, f.route, f.rule, f.source_id, f.source, f.baseline_scan_id,
		       f.check_id, c.status, f.first_seen, f.last_positive_at, f.previous_check_id, f.retest_at,
		       risk.metadata
		FROM plume.runtime_findings f
		JOIN plume.checks c ON c.id = f.check_id AND c.workspace_id = f.workspace_id
		LEFT JOIN LATERAL (
			SELECT jsonb_build_object('revision', t.revision, 'severity', t.severity, 'ownerTeam', t.owner_team,
			       'dueDate', COALESCE(to_char(t.due_date, 'YYYY-MM-DD'), ''), 'reason', t.reason,
			       'updatedBy', u.username, 'updatedAt', t.created_at) AS metadata
			FROM plume.runtime_risk_triage t JOIN plume.users u ON u.id = t.actor_id
			WHERE t.finding_id = f.id ORDER BY t.revision DESC LIMIT 1
		) risk ON true
		WHERE f.workspace_id = $1 ORDER BY f.id DESC LIMIT $2
	`, workspaceID, maxRuntimeFindingCycles)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RuntimeFinding, error) {
		var finding RuntimeFinding
		err := row.Scan(&finding.ID, &finding.Service, &finding.Method, &finding.Route, &finding.Rule,
			&finding.SourceID, &finding.Source, &finding.BaselineScanID, &finding.CheckID, &finding.CheckStatus,
			&finding.FirstSeen, &finding.LastSeen, &finding.PreviousCheckID, &finding.RetestAt, &finding.Risk)
		return finding, err
	})
}

type runtimeFindingBatch struct {
	workspaceID int64
	actorID     int64
	sourceID    int64
	sourceName  string
	service     string
	kind        string
	batchID     string
}

type runtimeFindingCandidate struct {
	key        runtimeRouteKey
	rule       string
	route      string
	status     int
	firstSeen  time.Time
	observedAt time.Time
}

func recordRuntimeFindings(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, observations []runtimesecurity.Observation) string {
	limited := false
	err := pgx.BeginFunc(ctx, tx, func(savepoint pgx.Tx) error {
		var previousTimeout string
		if err := savepoint.QueryRow(ctx, "SHOW statement_timeout").Scan(&previousTimeout); err != nil {
			return err
		}
		if _, err := savepoint.Exec(ctx, "SET LOCAL statement_timeout = '1s'"); err != nil {
			return err
		}
		if _, err := savepoint.Exec(ctx, `
			SELECT pg_advisory_xact_lock_shared(hashtextextended('plume:api-security:' || $2, $1))
		`, batch.workspaceID, batch.service); err != nil {
			return err
		}
		scanID, baseline, err := runtimeFindingBaseline(ctx, savepoint, batch)
		if err != nil {
			return err
		}
		if scanID == 0 {
			_, err = savepoint.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", previousTimeout)
			return err
		}
		// Different telemetry sources can describe the same route. Serialize
		// the workspace quota and its route identities across both kinds.
		if _, err := savepoint.Exec(ctx, `
			SELECT pg_advisory_xact_lock(hashtextextended('plume:runtime-findings', $1))
		`, batch.workspaceID); err != nil {
			return err
		}
		limited, err = evaluateRuntimeFindings(ctx, savepoint, batch, scanID, baseline, observations)
		if err != nil {
			return err
		}
		if err := persistRuntimeRetests(ctx, savepoint, batch, scanID, baseline, observations); err != nil {
			return err
		}
		_, err = savepoint.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", previousTimeout)
		return err
	})
	if err != nil {
		slog.Warn("runtime finding evaluation failed", "sourceId", batch.sourceID)
		return "evaluation_failed"
	}
	if limited {
		return "scope_limited"
	}
	return ""
}

func runtimeFindingBaseline(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch) (int64, map[runtimeRouteKey]bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT latest.id, op.method, op.path, COALESCE(op.deprecated, false)
		FROM (
			SELECT id, operations FROM plume.api_security_scans
			WHERE workspace_id = $1 AND source = $2 ORDER BY id DESC LIMIT 1
		) latest
		CROSS JOIN LATERAL jsonb_to_recordset(latest.operations) AS op(method text, path text, deprecated boolean)
	`, batch.workspaceID, batch.service)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	baseline := make(map[runtimeRouteKey]bool)
	var scanID int64
	for rows.Next() {
		var method, route string
		var deprecated bool
		if err := rows.Scan(&scanID, &method, &route, &deprecated); err != nil {
			return 0, nil, err
		}
		key := runtimeRouteKey{method, runtimesecurity.RouteKey(route)}
		previous, exists := baseline[key]
		baseline[key] = deprecated && (!exists || previous)
	}
	return scanID, baseline, rows.Err()
}

func evaluateRuntimeFindings(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, scanID int64, baseline map[runtimeRouteKey]bool,
	observations []runtimesecurity.Observation,
) (bool, error) {
	candidates := make([]runtimeFindingCandidate, 0)
	byRoute := make(map[runtimeRouteKey]int)
	for _, observation := range observations {
		if observation.Status < 200 || observation.Status >= 300 {
			continue
		}
		key := runtimeRouteKey{strings.ToUpper(observation.Method), runtimesecurity.RouteKey(observation.Route)}
		rule := "unregistered_success"
		if deprecated, documented := baseline[key]; documented {
			if !deprecated {
				continue
			}
			rule = "deprecated_success"
		}
		candidate := runtimeFindingCandidate{
			key: key, rule: rule, route: observation.Route, status: observation.Status,
			firstSeen: observation.ObservedAt, observedAt: observation.ObservedAt,
		}
		if index, exists := byRoute[key]; exists {
			firstSeen := candidates[index].firstSeen
			if candidate.firstSeen.Before(firstSeen) {
				firstSeen = candidate.firstSeen
			}
			if candidate.observedAt.After(candidates[index].observedAt) {
				candidates[index] = candidate
			}
			candidates[index].firstSeen = firstSeen
			continue
		}
		byRoute[key] = len(candidates)
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return false, nil
	}
	limited := len(candidates) > maxRuntimeFindingCandidates
	if limited {
		candidates = candidates[:maxRuntimeFindingCandidates]
	}
	var current int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM plume.runtime_findings WHERE workspace_id = $1", batch.workspaceID).Scan(&current); err != nil {
		return false, err
	}
	created := 0
	for _, candidate := range candidates {
		isNew, previousCheckID, err := updateRuntimeFinding(ctx, tx, batch, candidate)
		if err != nil {
			return false, err
		}
		if !isNew {
			continue
		}
		if current+created >= maxRuntimeFindingCycles {
			limited = true
			continue
		}
		if previousCheckID != nil {
			candidate.firstSeen = candidate.observedAt
		}
		if err := createRuntimeFinding(ctx, tx, batch, scanID, previousCheckID, candidate); err != nil {
			return false, err
		}
		created++
	}
	return limited, nil
}

func updateRuntimeFinding(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, candidate runtimeFindingCandidate) (bool, *int64, error) {
	var findingID, checkID int64
	var status string
	var lastPositive time.Time
	var retestAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT f.id, c.id, c.status, f.last_positive_at, f.retest_at
		FROM plume.runtime_findings f JOIN plume.checks c ON c.id = f.check_id AND c.workspace_id = f.workspace_id
		WHERE f.workspace_id = $1 AND f.service = $2 AND f.method = $3 AND f.route_key = $4 AND f.rule = $5
		ORDER BY f.id DESC LIMIT 1 FOR NO KEY UPDATE OF c
	`, batch.workspaceID, batch.service, candidate.key.method, candidate.key.route, candidate.rule).
		Scan(&findingID, &checkID, &status, &lastPositive, &retestAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil, nil
	}
	if err != nil || !candidate.observedAt.After(lastPositive) {
		return false, nil, err
	}
	if status == "verified" {
		if retestAt == nil || !candidate.observedAt.After(*retestAt) {
			return false, nil, nil
		}
		verified, err := latestCheckEvent(ctx, tx, checkID, "verified")
		return err == nil && verified.ID > 0 && candidate.observedAt.After(verified.CreatedAt), &checkID, err
	}
	_, err = tx.Exec(ctx, "UPDATE plume.runtime_findings SET last_positive_at = $2 WHERE id = $1",
		findingID, candidate.observedAt)
	return false, nil, err
}

func createRuntimeFinding(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, scanID int64, previousCheckID *int64, candidate runtimeFindingCandidate) error {
	assetRef := candidate.key.method + " " + candidate.key.route
	assetName := candidate.key.method + " " + candidate.route
	if _, err := tx.Exec(ctx, `
		INSERT INTO plume.assets (workspace_id, name, system, source_provider, source, source_ref)
		VALUES ($1, $2, $3, 'runtime-api', $3, $4)
		ON CONFLICT (workspace_id, source_provider, source, source_ref) DO NOTHING
	`, batch.workspaceID, assetName, batch.service, assetRef); err != nil {
		return err
	}
	title, note := "契约未登记的活跃接口", "核对实际发布范围和访问策略，更新契约或按审批关闭未登记接口。"
	if candidate.rule == "deprecated_success" {
		title, note = "已弃用接口仍在提供服务", "核对下线计划和实际访问范围，完成下线或复核保留依据。"
	}
	var check Check
	if err := tx.QueryRow(ctx, `
		INSERT INTO plume.checks (workspace_id, asset_id, requirement_code, owner_team, note, created_by)
		SELECT a.workspace_id, a.id, 'G03', '自动采集', $5, $4
		FROM plume.assets a WHERE a.workspace_id = $1 AND a.source_provider = 'runtime-api' AND a.source = $2 AND a.source_ref = $3
		RETURNING id, status
	`, batch.workspaceID, batch.service, assetRef, batch.actorID, title+"。"+note).Scan(&check.ID, &check.Status); err != nil {
		return err
	}
	if err := recordCheckEvent(ctx, tx, check.ID, batch.actorID, "created", "由 API 运行时观测自动生成"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO plume.runtime_findings
		    (workspace_id, check_id, previous_check_id, source_id, source, baseline_scan_id, service, method, route_key, route, rule, first_seen, last_positive_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, batch.workspaceID, check.ID, previousCheckID, batch.sourceID, batch.sourceName, scanID, batch.service, candidate.key.method,
		candidate.key.route, candidate.route, candidate.rule, candidate.firstSeen, candidate.observedAt); err != nil {
		return err
	}
	_, err := addEvidenceTx(ctx, tx, check, batch.actorID, EvidenceInput{
		Kind: "observation", SourceSystem: "API 运行时自动观测",
		SourceRecord: fmt.Sprintf("来源 #%d · OpenAPI 扫描 #%d · 批次 %s", batch.sourceID, scanID, batch.batchID),
		Summary:      fmt.Sprintf("%s。%s 返回 HTTP %d，来源类型 %s。按当前契约范围核对访问策略。", title, assetName, candidate.status, batch.kind),
		ObservedAt:   candidate.observedAt,
	}, true)
	return err
}
