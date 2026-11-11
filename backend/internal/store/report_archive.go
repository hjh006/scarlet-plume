package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

const (
	ReportArchiveRetentionPerJob = 30
	reportArchiveListLimit       = 100
	reportArchiveByteLimit       = 4 << 20
)

var ErrReportArchiveFailed = errors.New("report archive generation failed")

type ReportSource struct {
	JobID      int64     `json:"jobId"`
	Source     string    `json:"source"`
	RunID      string    `json:"runId"`
	ObservedAt time.Time `json:"observedAt"`
}

type ReportIdentification struct {
	Tables               int `json:"tables"`
	Columns              int `json:"columns"`
	SensitiveCandidates  int `json:"sensitiveCandidates"`
	TablesWithCandidates int `json:"tablesWithCandidates"`
	PublicTableSelect    int `json:"publicTableSelect"`
	PublicColumnSelect   int `json:"publicColumnSelect"`
}

type ReportArchive struct {
	ID int64 `json:"id"`
	ReportSource
	GeneratedAt    time.Time             `json:"generatedAt"`
	Status         string                `json:"status"`
	Code           string                `json:"code,omitempty"`
	Counts         *ReportCounts         `json:"counts,omitempty"`
	Identification *ReportIdentification `json:"identification,omitempty"`
}

const reportArchiveBasis = "报告仅覆盖本次 PostgreSQL 目录采集及已关联的检查。自动判定反映本次采集可观测的信号，检查状态和独立复核记录反映归档时点的工作流。整体合规结论需结合实际适用范围及其他证据另行评定。"

func archiveManagedPostgresReport(ctx context.Context, tx pgx.Tx, job AutomationJob, cfg ManagedPostgresConfig, run AutomationRun, tables []collector.PostgreSQLTable) error {
	var generatedAt time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&generatedAt); err != nil {
		return err
	}

	// A savepoint keeps a report-specific failure from discarding collection results.
	err := pgx.BeginFunc(ctx, tx, func(reportTx pgx.Tx) error {
		var previousTimeout string
		if err := reportTx.QueryRow(ctx, "SHOW statement_timeout").Scan(&previousTimeout); err != nil {
			return err
		}
		if _, err := reportTx.Exec(ctx, "SET LOCAL statement_timeout = '2s'"); err != nil {
			return err
		}

		report := ComplianceReport{
			GeneratedAt: generatedAt.UTC(),
			Scope: ReportScope{
				Basis: reportArchiveBasis,
				Source: &ReportSource{
					JobID: job.ID, Source: job.Source, RunID: run.RunID, ObservedAt: run.ObservedAt.UTC(),
				},
			},
			Checks:         make([]ReportCheck, 0),
			Identification: summarizeReportIdentification(tables),
		}
		if err := reportTx.QueryRow(ctx, "SELECT slug, name FROM plume.workspaces WHERE id = $1", job.workspaceID).
			Scan(&report.Workspace.Slug, &report.Workspace.Name); err != nil {
			return err
		}
		if err := readArchiveChecks(ctx, reportTx, job, cfg, run, tables, &report); err != nil {
			return err
		}
		if err := readReportHistory(ctx, reportTx, job.workspaceID, &report); err != nil {
			return err
		}
		if err := readReportDataFlows(ctx, reportTx, job.workspaceID, &report); err != nil {
			return err
		}
		snapshot, err := json.Marshal(report)
		if err != nil {
			return err
		}
		if len(snapshot) > reportArchiveByteLimit {
			return ErrReportScopeTooLarge
		}
		// The archive list can read counts without decompressing every full report.
		summary := struct {
			Counts         ReportCounts          `json:"counts"`
			Identification *ReportIdentification `json:"identification"`
		}{Counts: report.Counts, Identification: report.Identification}
		if _, err := reportTx.Exec(ctx, `
			INSERT INTO plume.automation_report_archives (automation_run_id, generated_at, status, summary, report)
			VALUES ($1, $2, 'ready', $3, $4)
		`, run.ID, generatedAt, summary, json.RawMessage(snapshot)); err != nil {
			return err
		}
		_, err = reportTx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", previousTimeout)
		return err
	})

	if err != nil {
		code := "generation_failed"
		if errors.Is(err, ErrReportScopeTooLarge) {
			code = "scope_too_large"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO plume.automation_report_archives (automation_run_id, generated_at, status, code)
			VALUES ($1, $2, 'failed', $3)
		`, run.ID, generatedAt, code); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM plume.automation_report_archives
		WHERE automation_run_id IN (
			SELECT r.id FROM plume.automation_runs r
			JOIN plume.automation_report_archives a ON a.automation_run_id = r.id
			WHERE r.job_id = $1 ORDER BY r.id DESC OFFSET $2
		)
	`, job.ID, ReportArchiveRetentionPerJob)
	return err
}

func summarizeReportIdentification(tables []collector.PostgreSQLTable) *ReportIdentification {
	summary := ReportIdentification{Tables: len(tables)}
	for _, table := range tables {
		summary.Columns += len(table.Columns)
		candidates := len(postgresSensitiveFields(table))
		summary.SensitiveCandidates += candidates
		if candidates > 0 {
			summary.TablesWithCandidates++
		}
		if table.PublicSelect {
			summary.PublicTableSelect++
		}
		summary.PublicColumnSelect += len(table.PublicSelectColumns)
	}
	return &summary
}

func readArchiveChecks(ctx context.Context, tx pgx.Tx, job AutomationJob, cfg ManagedPostgresConfig, run AutomationRun,
	tables []collector.PostgreSQLTable, report *ComplianceReport,
) error {
	// Check editors lock the same rows before writing workflow or evidence history.
	rows, err := tx.Query(ctx, checkSelect+`
		JOIN plume.postgres_check_bindings b ON b.check_id = c.id
		JOIN plume.postgres_asset_inspections ins ON ins.asset_id = c.asset_id
		WHERE c.workspace_id = $1 AND b.job_id = $2 AND ins.automation_run_id = $3
		  AND c.feishu_target_digest IS NULL AND c.requirement_code IN ('G02', 'G03')
		  AND (b.source_host, b.source_port, b.source_database) = ($4, $5, $6)
		ORDER BY c.id LIMIT $7 FOR SHARE OF c NOWAIT
	`, job.workspaceID, job.ID, run.ID, cfg.Host, cfg.Port, cfg.Database, ReportCheckLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	if err := readReportCheckRows(rows, report); err != nil || len(report.Checks) == 0 {
		return err
	}

	assetIDs := make([]int64, 0, len(report.Checks))
	for _, entry := range report.Checks {
		assetIDs = append(assetIDs, entry.Check.AssetID)
	}
	rows, err = tx.Query(ctx, "SELECT id, source_ref FROM plume.assets WHERE workspace_id = $1 AND id = ANY($2)", job.workspaceID, assetIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	references := make(map[int64]string, len(assetIDs))
	for rows.Next() {
		var assetID int64
		var reference string
		if err := rows.Scan(&assetID, &reference); err != nil {
			return err
		}
		references[assetID] = reference
	}
	if err := rows.Err(); err != nil {
		return err
	}

	byReference := make(map[string]collector.PostgreSQLTable, len(tables))
	for _, table := range tables {
		byReference[table.SourceRef] = table
	}
	for i := range report.Checks {
		entry := &report.Checks[i]
		table, ok := byReference[references[entry.Check.AssetID]]
		if !ok {
			return ErrNotFound
		}
		observation, _, _ := evaluatePostgresInspection(table, cfg.Database, entry.Check.Requirement.Code)
		entry.AutomaticEvaluation = &observation
	}
	return nil
}

func (s *Store) ListReportArchives(ctx context.Context, workspaceID, actorID int64) ([]ReportArchive, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.automation_run_id, j.id, j.source, r.run_id, r.observed_at,
		       a.generated_at, a.status, COALESCE(a.code, ''), a.summary->'counts', a.summary->'identification'
		FROM plume.automation_report_archives a
		JOIN plume.automation_runs r ON r.id = a.automation_run_id
		JOIN plume.automation_jobs j ON j.id = r.job_id
		JOIN plume.memberships m ON m.workspace_id = j.workspace_id
		WHERE j.workspace_id = $1 AND m.user_id = $2
		ORDER BY a.automation_run_id DESC LIMIT $3
	`, workspaceID, actorID, reportArchiveListLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ReportArchive, 0)
	for rows.Next() {
		var item ReportArchive
		if err := rows.Scan(&item.ID, &item.JobID, &item.Source, &item.RunID, &item.ObservedAt, &item.GeneratedAt,
			&item.Status, &item.Code, &item.Counts, &item.Identification); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ReportArchive(ctx context.Context, workspaceID, actorID, archiveID int64) (ComplianceReport, error) {
	var status string
	var report *ComplianceReport
	err := s.pool.QueryRow(ctx, `
		SELECT a.status, a.report
		FROM plume.automation_report_archives a
		JOIN plume.automation_runs r ON r.id = a.automation_run_id
		JOIN plume.automation_jobs j ON j.id = r.job_id
		JOIN plume.memberships m ON m.workspace_id = j.workspace_id
		WHERE j.workspace_id = $1 AND m.user_id = $2 AND a.automation_run_id = $3
	`, workspaceID, actorID, archiveID).Scan(&status, &report)
	if err != nil {
		return ComplianceReport{}, storeError(err)
	}
	if status == "failed" {
		return ComplianceReport{}, ErrReportArchiveFailed
	}
	return *report, nil
}
