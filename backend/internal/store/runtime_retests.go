package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

type runtimeCheckFinding struct {
	RuntimeFinding
	workspaceID      int64
	routeKey         string
	retestEvidenceID *int64
	retestPolicyID   *int64
}

func runtimeFindingForCheck(ctx context.Context, tx pgx.Tx, check Check) (*runtimeCheckFinding, error) {
	var finding runtimeCheckFinding
	err := tx.QueryRow(ctx, `
		SELECT id, workspace_id, service, method, route_key, route, rule, source_id, source, baseline_scan_id,
		       first_seen, last_positive_at, retest_at, retest_evidence_id,NULLIF(detection,'{}'::jsonb),retest_policy_id
		FROM plume.runtime_findings WHERE check_id = $1
	`, check.ID).Scan(&finding.ID, &finding.workspaceID, &finding.Service, &finding.Method, &finding.routeKey,
		&finding.Route, &finding.Rule, &finding.SourceID, &finding.Source, &finding.BaselineScanID,
		&finding.FirstSeen, &finding.LastSeen, &finding.RetestAt, &finding.retestEvidenceID, &finding.Detection, &finding.retestPolicyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	finding.CheckID, finding.CheckStatus = check.ID, check.Status
	return &finding, nil
}

// Approval and OpenAPI publication share the same service lock. Acquire it
// before taking the check row lock, in the same order as runtime ingestion.
func lockRuntimeBaselineForCheck(ctx context.Context, tx pgx.Tx, workspaceID, checkID int64) error {
	_, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock_shared(hashtextextended('plume:api-security:' || service, workspace_id))
		FROM plume.runtime_findings WHERE workspace_id = $1 AND check_id = $2
	`, workspaceID, checkID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('plume:runtime-findings',$1)) WHERE EXISTS(SELECT 1 FROM plume.runtime_findings WHERE workspace_id=$1 AND check_id=$2)`, workspaceID, checkID)
	return err
}

func runtimeRetestAllowed(ctx context.Context, tx pgx.Tx, finding *runtimeCheckFinding) (bool, error) {
	if finding.RetestAt == nil || finding.retestEvidenceID == nil || !finding.RetestAt.After(finding.LastSeen) {
		return false, nil
	}
	remediatedAt, err := runtimeRemediationAt(ctx, tx, finding.CheckID)
	if err != nil || remediatedAt.IsZero() || !finding.RetestAt.After(remediatedAt) {
		return false, err
	}
	if slices.Contains(runtimeDetectionRules, finding.Rule) {
		return runtimeDetectionRetestAllowed(ctx, tx, finding, remediatedAt)
	}
	scanID, baseline, err := runtimeFindingBaseline(ctx, tx, runtimeFindingBatch{workspaceID: finding.workspaceID, service: finding.Service})
	if err != nil || !runtimeRetestBaseline(finding.Rule, finding.BaselineScanID, scanID, baseline,
		runtimeRouteKey{finding.Method, finding.routeKey}) {
		return false, err
	}
	var valid bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM plume.check_evidence
			WHERE id = $1 AND check_id = $2 AND kind = 'retest' AND observed_at = $3 AND created_at > $4
		)
	`, *finding.retestEvidenceID, finding.CheckID, *finding.RetestAt, remediatedAt).Scan(&valid)
	return valid, err
}

func runtimeRemediationAt(ctx context.Context, tx pgx.Tx, checkID int64) (time.Time, error) {
	var extended bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.runtime_findings WHERE check_id=$1 AND rule=ANY($2))`, checkID, extendedRuntimeRules).Scan(&extended); err != nil {
		return time.Time{}, err
	}
	if extended {
		var at *time.Time
		err := tx.QueryRow(ctx, `SELECT max(GREATEST(e.created_at,e.observed_at)) FROM plume.check_evidence e JOIN plume.checks c ON c.id=e.check_id WHERE e.check_id=$1 AND e.kind='remediation' AND e.created_at>GREATEST(c.created_at,COALESCE((SELECT max(created_at) FROM plume.check_events WHERE check_id=$1 AND action='changes'),c.created_at)) AND e.observed_at>GREATEST(c.created_at,COALESCE((SELECT max(created_at) FROM plume.check_events WHERE check_id=$1 AND action='changes'),c.created_at))`, checkID).Scan(&at)
		if at == nil {
			return time.Time{}, err
		}
		return *at, err
	}
	change, err := latestCheckEvent(ctx, tx, checkID, "changes")
	if err != nil {
		return time.Time{}, err
	}
	return checkRemediationAt(ctx, tx, checkID, change)
}

func runtimeRetestBaseline(rule string, openingScan, currentScan int64, baseline map[runtimeRouteKey]bool, key runtimeRouteKey) bool {
	deprecated, documented := baseline[key]
	switch rule {
	case "unregistered_success":
		return currentScan > openingScan && documented && !deprecated
	case "deprecated_success":
		return currentScan != 0 && (!documented || deprecated)
	default:
		return false
	}
}

type runtimeRetestSignal struct {
	successAt time.Time
	success   int
	goneAt    time.Time
}

type runtimeRetestCheck struct {
	id            int64
	check         Check
	sourceID      int64
	openingScanID int64
	rule          string
	key           runtimeRouteKey
	route         string
	lastPositive  time.Time
	retestAt      *time.Time
}

func persistRuntimeRetests(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, scanID int64, baseline map[runtimeRouteKey]bool,
	observations []runtimesecurity.Observation,
) error {
	signals := make(map[runtimeRouteKey]runtimeRetestSignal)
	methods, routes := make([]string, 0), make([]string, 0)
	for _, observation := range observations {
		success := observation.Status >= 200 && observation.Status < 300
		if !success && observation.Status != 410 {
			continue
		}
		key := runtimeRouteKey{strings.ToUpper(observation.Method), runtimesecurity.RouteKey(observation.Route)}
		signal, exists := signals[key]
		if !exists {
			methods, routes = append(methods, key.method), append(routes, key.route)
		}
		observedAt := observation.ObservedAt.Truncate(time.Microsecond)
		if success && observedAt.After(signal.successAt) {
			signal.successAt, signal.success = observedAt, observation.Status
		}
		if observation.Status == 410 && observedAt.After(signal.goneAt) {
			signal.goneAt = observedAt
		}
		signals[key] = signal
	}
	if len(signals) == 0 {
		return nil
	}

	rows, err := tx.Query(ctx, `
		SELECT f.id, c.id, c.status, f.source_id, f.baseline_scan_id, f.rule, f.method, f.route_key,
		       f.route, f.last_positive_at, f.retest_at
		FROM plume.runtime_findings f
		JOIN plume.checks c ON c.id = f.check_id AND c.workspace_id = f.workspace_id
		JOIN unnest($3::text[], $4::text[]) AS target(method, route_key)
		  ON target.method = f.method AND target.route_key = f.route_key
		WHERE f.workspace_id = $1 AND f.service = $2 AND f.source_id=$5 AND c.status <> 'verified' AND f.rule IN ('unregistered_success','deprecated_success')
		ORDER BY c.id FOR NO KEY UPDATE OF c
	`, batch.workspaceID, batch.service, methods, routes, batch.sourceID)
	if err != nil {
		return err
	}
	checks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (runtimeRetestCheck, error) {
		var candidate runtimeRetestCheck
		err := row.Scan(&candidate.id, &candidate.check.ID, &candidate.check.Status, &candidate.sourceID,
			&candidate.openingScanID, &candidate.rule, &candidate.key.method, &candidate.key.route,
			&candidate.route, &candidate.lastPositive, &candidate.retestAt)
		return candidate, err
	})
	if err != nil || len(checks) == 0 {
		return err
	}

	var baselineCreatedAt time.Time
	if err := tx.QueryRow(ctx, "SELECT created_at FROM plume.api_security_scans WHERE id = $1", scanID).Scan(&baselineCreatedAt); err != nil {
		return err
	}
	for _, candidate := range checks {
		signal := signals[candidate.key]
		deprecated, documented := baseline[candidate.key]
		// A formerly deprecated route responding with 2xx remains positive
		// even if a newer contract removed the operation or restored it.
		positive := candidate.rule == "deprecated_success" || !documented || deprecated
		if positive && signal.successAt.After(candidate.lastPositive) {
			if _, err := tx.Exec(ctx, "UPDATE plume.runtime_findings SET last_positive_at = $2 WHERE id = $1",
				candidate.id, signal.successAt); err != nil {
				return err
			}
			candidate.lastPositive = signal.successAt
		}
		if candidate.check.Status != "needs_work" || candidate.sourceID != batch.sourceID ||
			!runtimeRetestBaseline(candidate.rule, candidate.openingScanID, scanID, baseline, candidate.key) {
			continue
		}
		if err := appendRuntimeRetest(ctx, tx, batch, candidate, signal, scanID, baselineCreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func appendRuntimeRetest(ctx context.Context, tx pgx.Tx, batch runtimeFindingBatch, candidate runtimeRetestCheck,
	signal runtimeRetestSignal, scanID int64, baselineCreatedAt time.Time,
) error {
	observedAt := signal.goneAt
	registered := candidate.rule == "unregistered_success"
	if registered {
		observedAt = signal.successAt
		if !observedAt.After(baselineCreatedAt) {
			return nil
		}
	} else if !signal.successAt.IsZero() {
		return nil
	}
	if !observedAt.After(candidate.lastPositive) {
		return nil
	}
	remediatedAt, err := runtimeRemediationAt(ctx, tx, candidate.check.ID)
	if err != nil || remediatedAt.IsZero() || !observedAt.After(remediatedAt) {
		return err
	}
	if candidate.retestAt != nil && candidate.retestAt.After(remediatedAt) && candidate.retestAt.After(candidate.lastPositive) {
		return nil
	}
	operation := candidate.key.method + " " + candidate.route
	summary := fmt.Sprintf("整改后，同一来源观测到 %s 返回 HTTP 410。同一批次未观测到此操作的 HTTP 2xx。", operation)
	if registered {
		summary = fmt.Sprintf("整改及更新契约后，同一来源观测到 %s 返回 HTTP %d。最新契约已登记该操作且未标记弃用。", operation, signal.success)
	}
	evidenceID, err := addEvidenceTx(ctx, tx, candidate.check, batch.actorID, EvidenceInput{
		Kind: "retest", SourceSystem: "API 运行时自动复测",
		SourceRecord: fmt.Sprintf("来源 #%d · OpenAPI 扫描 #%d · 批次 %s", batch.sourceID, scanID, batch.batchID),
		Summary:      summary,
		ObservedAt:   observedAt,
	}, true)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE plume.runtime_findings SET retest_at = $2, retest_evidence_id = $3 WHERE id = $1",
		candidate.id, observedAt, evidenceID)
	return err
}
