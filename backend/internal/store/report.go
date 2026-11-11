package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	ReportCheckLimit    = 200
	ReportTimelineLimit = 3000
)

var ErrReportScopeTooLarge = errors.New("report scope is too large")

type ComplianceReport struct {
	AssessmentReferences *ReportAssessmentReferences `json:"assessmentReferences,omitempty"`
	Operations           *ReportOperations           `json:"operations,omitempty"`
	Workspace            ReportWorkspace             `json:"workspace"`
	GeneratedAt          time.Time                   `json:"generatedAt"`
	Scope                ReportScope                 `json:"scope"`
	Counts               ReportCounts                `json:"counts"`
	Checks               []ReportCheck               `json:"checks"`
	Identification       *ReportIdentification       `json:"identification,omitempty"`
	DataFlows            *ReportDataFlows            `json:"dataFlows,omitempty"`
	CatalogDiscovery     *ReportCatalogDiscovery     `json:"catalogDiscovery,omitempty"`
}

// The embedded summary is frozen with the report. It remains available even
// after the graph snapshot's separate retention window expires.
type ReportDataFlows struct {
	SnapshotID int64           `json:"snapshotId"`
	CapturedAt time.Time       `json:"capturedAt"`
	Summary    DataFlowSummary `json:"summary"`
	Warnings   []string        `json:"warnings"`
	Basis      string          `json:"basis"`
}

type ReportWorkspace struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type ReportScope struct {
	ActivityID   int64         `json:"activityId,omitempty"`
	ActivityName string        `json:"activityName,omitempty"`
	Activity     *Activity     `json:"activity,omitempty"`
	Source       *ReportSource `json:"source,omitempty"`
	Basis        string        `json:"basis"`
}

type ReportCounts struct {
	Total        int `json:"total"`
	Draft        int `json:"draft"`
	InReview     int `json:"inReview"`
	NeedsWork    int `json:"needsWork"`
	Verified     int `json:"verified"`
	Templated    int `json:"templated"`
	Evidence     int `json:"evidence"`
	Remediations int `json:"remediations"`
	Retests      int `json:"retests"`
	Reviews      int `json:"reviews"`
}

type ReportCheck struct {
	RuntimeFinding      *RuntimeFinding          `json:"runtimeFinding,omitempty"`
	Check               Check                    `json:"check"`
	Evidence            []Evidence               `json:"evidence"`
	Events              []CheckEvent             `json:"events"`
	LatestReview        *CheckEvent              `json:"latestReview,omitempty"`
	AutomaticEvaluation *PostgresObservation     `json:"automaticEvaluation,omitempty"`
	Classification      *AssetClassificationItem `json:"classification,omitempty"`
}

const reportBasis = "报告范围以列明的已创建检查为准。已验证指对应检查完成独立复核。整体合规结论需结合实际适用范围及其他证据另行评定。"

// ComplianceReport reads the checks and their history from the same database snapshot.
func (s *Store) ComplianceReport(ctx context.Context, workspaceID, actorID, activityID int64) (ComplianceReport, error) {
	report := ComplianceReport{
		Scope:  ReportScope{Basis: reportBasis},
		Checks: make([]ReportCheck, 0),
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT w.slug, w.name, transaction_timestamp()
			FROM plume.workspaces w JOIN plume.memberships m ON m.workspace_id = w.id
			WHERE w.id = $1 AND m.user_id = $2
		`, workspaceID, actorID).Scan(&report.Workspace.Slug, &report.Workspace.Name, &report.GeneratedAt); err != nil {
			return storeError(err)
		}
		report.GeneratedAt = report.GeneratedAt.UTC()
		if activityID != 0 {
			activity, err := scanActivity(tx.QueryRow(ctx, activitySelect+" WHERE p.workspace_id = $1 AND p.id = $2", workspaceID, activityID))
			if err != nil {
				return err
			}
			report.Scope.ActivityID = activity.ID
			report.Scope.ActivityName = activity.Name
			report.Scope.Activity = &activity
		}
		if err := readReportChecks(ctx, tx, workspaceID, activityID, &report); err != nil {
			return err
		}
		if err := readReportHistory(ctx, tx, workspaceID, &report); err != nil {
			return err
		}
		if err := readReportDataFlows(ctx, tx, workspaceID, &report); err != nil {
			return err
		}
		if err := readReportCatalogDiscovery(ctx, tx, workspaceID, &report); err != nil {
			return err
		}
		if err := readReportOperations(ctx, tx, workspaceID, &report); err != nil {
			return err
		}
		return readReportAssessmentReferences(ctx, tx, workspaceID, &report)
	})
	if err != nil {
		return ComplianceReport{}, err
	}
	return report, nil
}

func readReportDataFlows(ctx context.Context, tx pgx.Tx, workspaceID int64, report *ComplianceReport) error {
	var result ReportDataFlows
	err := tx.QueryRow(ctx, `
		SELECT id, created_at, payload->'summary', COALESCE(payload->'warnings', '[]'::jsonb)
		FROM plume.data_flow_snapshots
		WHERE workspace_id = $1 AND status = 'ready'
		  AND COALESCE(scope->>'service', '') = '' AND COALESCE(scope->>'category', '') = ''
		  AND COALESCE(scope->>'subjectRef', '') = '' AND COALESCE(scope->>'traceRef', '') = ''
		  AND COALESCE(scope->>'focus', '') = ''
		ORDER BY id DESC LIMIT 1
	`, workspaceID).Scan(&result.SnapshotID, &result.CapturedAt, &result.Summary, &result.Warnings)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	result.Basis = "补充范围为工作区最新可用的全范围数据流结构快照，与本报告的检查或采集来源范围分别列示。统计覆盖已接入来源及其观察窗口，不构成全量业务链路或整体合规结论。快照摘要随报告冻结。"
	report.DataFlows = &result
	return nil
}

func readReportChecks(ctx context.Context, tx pgx.Tx, workspaceID, activityID int64, report *ComplianceReport) error {
	rows, err := tx.Query(ctx, checkSelect+`
		WHERE c.workspace_id = $1 AND ($2 = 0::bigint OR c.activity_id = $2)
		ORDER BY c.id DESC LIMIT $3
	`, workspaceID, activityID, ReportCheckLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	return readReportCheckRows(rows, report)
}

func readReportCheckRows(rows pgx.Rows, report *ComplianceReport) error {
	for rows.Next() {
		if len(report.Checks) == ReportCheckLimit {
			return ErrReportScopeTooLarge
		}
		check, err := scanCheck(rows)
		if err != nil {
			return err
		}
		report.Counts.Total++
		switch check.Status {
		case "draft":
			report.Counts.Draft++
		case "in_review":
			report.Counts.InReview++
		case "needs_work":
			report.Counts.NeedsWork++
		case "verified":
			report.Counts.Verified++
		}
		if check.Template != nil {
			report.Counts.Templated++
		}
		report.Checks = append(report.Checks, ReportCheck{Check: check, Evidence: []Evidence{}, Events: []CheckEvent{}})
	}
	return rows.Err()
}

func readReportHistory(ctx context.Context, tx pgx.Tx, workspaceID int64, report *ComplianceReport) error {
	if len(report.Checks) == 0 {
		return nil
	}
	if err := readReportRuntimeFindings(ctx, tx, report); err != nil {
		return err
	}
	if err := readReportClassifications(ctx, tx, workspaceID, report); err != nil {
		return err
	}
	ids := make([]int64, 0, len(report.Checks))
	positions := make(map[int64]int, len(report.Checks))
	for i, entry := range report.Checks {
		ids = append(ids, entry.Check.ID)
		positions[entry.Check.ID] = i
	}
	if err := readReportEvidence(ctx, tx, ids, positions, report); err != nil {
		return err
	}
	return readReportEvents(ctx, tx, ids, positions, report)
}

func readReportClassifications(ctx context.Context, tx pgx.Tx, workspaceID int64, report *ComplianceReport) error {
	ids := make([]int64, 0, len(report.Checks))
	for _, entry := range report.Checks {
		ids = append(ids, entry.Check.AssetID)
	}
	items, err := readClassifications(ctx, tx, workspaceID, ids)
	if err != nil {
		return err
	}
	byID := make(map[int64]*AssetClassificationItem, len(items))
	for i := range items {
		byID[items[i].Asset.ID] = &items[i]
	}
	for i := range report.Checks {
		report.Checks[i].Classification = byID[report.Checks[i].Check.AssetID]
		if report.Checks[i].Classification == nil {
			return ErrNotFound
		}
		if automatic := report.Checks[i].Classification.Automatic; automatic != nil && report.Checks[i].AutomaticEvaluation != nil &&
			report.Checks[i].Check.Requirement.Code == "G02" {
			report.Checks[i].AutomaticEvaluation.Classification = automatic
			report.Checks[i].AutomaticEvaluation.Assessment = "review_required"
			report.Checks[i].AutomaticEvaluation.AssessmentReasons = []string{"classification_" + automatic.Comparison}
			report.Checks[i].AutomaticEvaluation.Scope = "按所采用标准核对源内有界统计、自动分类与本次采证缺口。"
		}
	}
	return nil
}

func readReportEvidence(ctx context.Context, tx pgx.Tx, ids []int64, positions map[int64]int, report *ComplianceReport) error {
	rows, err := tx.Query(ctx, `
		SELECT e.check_id, e.id, e.kind, e.source_system, e.source_record, e.summary, e.observed_at,
			e.created_at, u.username, e.feishu_snapshot_digest, e.feishu_member_present, COALESCE(e.feishu_permission, ''),
		       COALESCE(r.job_id, pr.job_id), COALESCE(r.run_id, pr.run_id, ''), p.observation
		FROM plume.check_evidence e JOIN plume.users u ON u.id = e.created_by
		LEFT JOIN plume.automation_runs r ON r.evidence_id = e.id
		LEFT JOIN plume.postgres_check_evidence p ON p.evidence_id = e.id
		LEFT JOIN plume.automation_runs pr ON pr.id = p.automation_run_id
		WHERE e.check_id = ANY($1) ORDER BY e.check_id, e.id DESC LIMIT $2
	`, ids, ReportTimelineLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if report.Counts.Evidence == ReportTimelineLimit {
			return ErrReportScopeTooLarge
		}
		var checkID int64
		var evidence Evidence
		var snapshotDigest *string
		var present *bool
		var permission string
		if err := rows.Scan(&checkID, &evidence.ID, &evidence.Kind, &evidence.SourceSystem, &evidence.SourceRecord,
			&evidence.Summary, &evidence.ObservedAt, &evidence.CreatedAt, &evidence.RecordedBy,
			&snapshotDigest, &present, &permission,
			&evidence.AutomationJobID, &evidence.AutomationRunID, &evidence.Postgres); err != nil {
			return err
		}
		entry := &report.Checks[positions[checkID]]
		if snapshotDigest != nil {
			evidence.Feishu = &FeishuObservation{
				TargetDigest: entry.Check.FeishuTargetDigest, SnapshotDigest: *snapshotDigest,
				MemberPresent: *present, Permission: permission,
			}
		}
		entry.Evidence = append(entry.Evidence, evidence)
		report.Counts.Evidence++
		switch evidence.Kind {
		case "remediation":
			report.Counts.Remediations++
		case "retest":
			report.Counts.Retests++
		}
	}
	return rows.Err()
}

func readReportEvents(ctx context.Context, tx pgx.Tx, ids []int64, positions map[int64]int, report *ComplianceReport) error {
	budget := ReportTimelineLimit - report.Counts.Evidence
	rows, err := tx.Query(ctx, `
		SELECT e.check_id, e.id, e.action, u.username, e.note, e.created_at
		FROM plume.check_events e JOIN plume.users u ON u.id = e.actor_id
		WHERE e.check_id = ANY($1) ORDER BY e.check_id, e.id DESC LIMIT $2
	`, ids, budget+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if budget == 0 {
			return ErrReportScopeTooLarge
		}
		var checkID int64
		var event CheckEvent
		if err := rows.Scan(&checkID, &event.ID, &event.Action, &event.Actor, &event.Note, &event.CreatedAt); err != nil {
			return err
		}
		entry := &report.Checks[positions[checkID]]
		entry.Events = append(entry.Events, event)
		if event.Action == "changes" || event.Action == "verified" {
			report.Counts.Reviews++
			if entry.LatestReview == nil {
				entry.LatestReview = &event
			}
		}
		budget--
	}
	return rows.Err()
}
