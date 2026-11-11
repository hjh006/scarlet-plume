package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

type PostgresInspection struct {
	AssetID        int64                     `json:"assetId"`
	JobID          int64                     `json:"jobId"`
	RunID          string                    `json:"runId"`
	ObservedAt     time.Time                 `json:"observedAt"`
	Table          collector.PostgreSQLTable `json:"table"`
	Classification *AutomaticClassification  `json:"classification,omitempty"`
}

type PostgresSensitiveField struct {
	Name        string `json:"name"`
	Sensitivity string `json:"sensitivity"`
}

type PostgresSelectGrant struct {
	Grantee     string `json:"grantee"`
	Public      bool   `json:"public"`
	Column      string `json:"column,omitempty"`
	SchemaUsage bool   `json:"schemaUsage"`
}

type PostgresObservation struct {
	SourceRef               string                   `json:"sourceRef"`
	Schema                  string                   `json:"schema"`
	Table                   string                   `json:"table"`
	Database                string                   `json:"database"`
	Scope                   string                   `json:"scope"`
	Assessment              string                   `json:"assessment,omitempty"`
	AssessmentReasons       []string                 `json:"assessmentReasons,omitempty"`
	ColumnCount             int                      `json:"columnCount"`
	SensitiveCandidateCount int                      `json:"sensitiveCandidateCount"`
	GrantCount              int                      `json:"grantCount"`
	PublicSchemaUsage       bool                     `json:"publicSchemaUsage"`
	PublicSelect            bool                     `json:"publicSelect"`
	PublicSelectColumnCount int                      `json:"publicSelectColumnCount"`
	SensitiveFields         []PostgresSensitiveField `json:"sensitiveFields,omitempty"`
	SelectGrants            []PostgresSelectGrant    `json:"selectGrants,omitempty"`
	SensitiveFieldsOmitted  int                      `json:"sensitiveFieldsOmitted,omitempty"`
	SelectGrantsOmitted     int                      `json:"selectGrantsOmitted,omitempty"`
	Classification          *AutomaticClassification `json:"classification,omitempty"`
}

func (s *Store) GetAssetPostgresInspection(ctx context.Context, workspaceID, actorID, assetID int64) (PostgresInspection, error) {
	var result PostgresInspection
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
		SELECT i.asset_id, i.job_id, r.run_id, r.observed_at, i.inspection, i.classification
		FROM plume.postgres_asset_inspections i
		JOIN plume.assets a ON a.id = i.asset_id
		JOIN plume.automation_runs r ON r.id = i.automation_run_id AND r.job_id = i.job_id
		JOIN plume.automation_jobs j ON j.id = i.job_id AND j.workspace_id = a.workspace_id
		JOIN plume.memberships m ON m.workspace_id = a.workspace_id
		WHERE a.workspace_id = $1 AND m.user_id = $2 AND a.id = $3
	`, workspaceID, actorID, assetID).
			Scan(&result.AssetID, &result.JobID, &result.RunID, &result.ObservedAt, &result.Table, &result.Classification)
		if err != nil {
			return storeError(err)
		}
		items, err := readClassifications(ctx, tx, workspaceID, []int64{assetID})
		if err != nil {
			return err
		}
		if len(items) != 1 {
			return ErrNotFound
		}
		result.Classification = items[0].Automatic
		return nil
	})
	return result, storeError(err)
}

func canUsePostgresEvidence(check Check) bool {
	return check.FeishuTargetDigest == "" && (check.Requirement.Code == "G02" || check.Requirement.Code == "G03")
}

func (s *Store) BindCheckPostgres(ctx context.Context, workspaceID, actorID, checkID, jobID int64) error {
	if checkID <= 0 || jobID <= 0 {
		return ErrInvalid
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Use the worker's lock order before binding a check to the source.
		if err := lockManagedPostgresJob(ctx, tx, workspaceID, jobID); err != nil {
			return err
		}
		check, err := checkEdit(ctx, tx, workspaceID, actorID, checkID)
		if err != nil {
			return err
		}
		if !canUsePostgresEvidence(check) {
			return ErrInvalid
		}
		var host, database string
		var port int
		err = tx.QueryRow(ctx, `
			SELECT i.source_host, i.source_port, i.source_database
			FROM plume.postgres_asset_inspections i
			JOIN plume.automation_jobs j ON j.id = i.job_id
			JOIN plume.managed_postgres_jobs p ON p.job_id = j.id
			WHERE i.asset_id = $1 AND j.workspace_id = $2 AND j.id = $3 AND j.revoked_at IS NULL
			  AND (i.source_host, i.source_port, i.source_database) = (p.host, p.port, p.database_name)
		`, check.AssetID, workspaceID, jobID).Scan(&host, &port, &database)
		if err != nil {
			return storeError(err)
		}
		var conflicts bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM plume.postgres_check_bindings b
				WHERE b.check_id = $1 AND (b.source_host, b.source_port, b.source_database) <> ($2, $3, $4)
				  AND EXISTS (
				      SELECT 1 FROM plume.check_evidence e
				      JOIN plume.postgres_check_evidence p ON p.evidence_id = e.id
				      WHERE e.check_id = b.check_id
				  )
			)
		`, checkID, host, port, database).Scan(&conflicts); err != nil {
			return err
		}
		if conflicts {
			return ErrConflict
		}
		result, err := tx.Exec(ctx, `
			INSERT INTO plume.postgres_check_bindings AS current
			    (check_id, job_id, source_host, source_port, source_database)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (check_id) DO UPDATE SET
			    job_id = excluded.job_id, source_host = excluded.source_host,
			    source_port = excluded.source_port, source_database = excluded.source_database
			WHERE (current.job_id, current.source_host, current.source_port, current.source_database)
			   IS DISTINCT FROM (excluded.job_id, excluded.source_host, excluded.source_port, excluded.source_database)
		`, checkID, jobID, host, port, database)
		if err != nil || result.RowsAffected() == 0 {
			return err
		}
		if err := recordCheckEvent(ctx, tx, checkID, actorID, "postgres_binding", fmt.Sprintf("关联 PostgreSQL 采集任务 #%d", jobID)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE plume.checks SET updated_at = clock_timestamp() WHERE id = $1", checkID)
		return err
	})
}

func loadCheckPostgresBinding(ctx context.Context, tx pgx.Tx, detail *CheckDetail, role string) error {
	if !canUsePostgresEvidence(detail.Check) {
		return nil
	}
	if err := tx.QueryRow(ctx, "SELECT (SELECT job_id FROM plume.postgres_check_bindings WHERE check_id = $1)", detail.ID).
		Scan(&detail.PostgresJobID); err != nil {
		return err
	}
	if !checkEditor(role) || !evidenceOpen(detail.Check) {
		return nil
	}
	err := tx.QueryRow(ctx, `
		SELECT i.job_id FROM plume.postgres_asset_inspections i
		JOIN plume.automation_jobs j ON j.id = i.job_id
		JOIN plume.managed_postgres_jobs p ON p.job_id = j.id
		LEFT JOIN plume.postgres_check_bindings b ON b.check_id = $2
		WHERE i.asset_id = $1 AND j.revoked_at IS NULL
		  AND (i.source_host, i.source_port, i.source_database) = (p.host, p.port, p.database_name)
		  AND (i.job_id, i.source_host, i.source_port, i.source_database)
		       IS DISTINCT FROM (b.job_id, b.source_host, b.source_port, b.source_database)
		  AND (b.check_id IS NULL OR (i.source_host, i.source_port, i.source_database) =
		                             (b.source_host, b.source_port, b.source_database) OR NOT EXISTS (
		      SELECT 1 FROM plume.check_evidence e
		      JOIN plume.postgres_check_evidence pe ON pe.evidence_id = e.id
		      WHERE e.check_id = b.check_id
		  ))
	`, detail.AssetID, detail.ID).Scan(&detail.AvailablePostgresJobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func validPostgresTables(tables []collector.PostgreSQLTable, assets []CollectedAsset) bool {
	if len(tables) != len(assets) || len(tables) > 1000 {
		return false
	}
	refs := make(map[string]string, len(assets))
	for _, asset := range assets {
		refs[asset.SourceRef] = asset.Name
	}
	columnCount, grantCount := 0, 0
	for _, table := range tables {
		name, ok := refs[table.SourceRef]
		if !ok || name != table.Name || !validCheckText(table.Schema, 128) || !validCheckText(table.SourceRef, 500) {
			return false
		}
		delete(refs, table.SourceRef)
		columnCount += len(table.Columns)
		grantCount += len(table.Grants)
	}
	return columnCount <= 10000 && grantCount <= 20000
}

func persistPostgresInspections(ctx context.Context, tx pgx.Tx, job AutomationJob, cfg ManagedPostgresConfig,
	run AutomationRun, tables []collector.PostgreSQLTable,
) error {
	if len(tables) == 0 {
		return captureDataFlowSnapshot(ctx, tx, job.workspaceID, job.actorID, "postgresql")
	}
	refs := make([]string, len(tables))
	for i, table := range tables {
		refs[i] = table.SourceRef
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO plume.postgres_asset_inspections
		    (asset_id, job_id, automation_run_id, source_host, source_port, source_database, inspection)
		SELECT a.id, $1, $2, $3, $4, $5, data.inspection
		FROM unnest($6::text[], $7::jsonb[]) AS data(source_ref, inspection)
		JOIN plume.assets a ON a.workspace_id = $8 AND a.source_provider = 'postgresql'
		                    AND a.source = $9 AND a.source_ref = data.source_ref
		ORDER BY a.id
		ON CONFLICT (asset_id) DO UPDATE SET
		    job_id = excluded.job_id, automation_run_id = excluded.automation_run_id,
		    source_host = excluded.source_host, source_port = excluded.source_port,
		    source_database = excluded.source_database, inspection = excluded.inspection
		RETURNING asset_id, inspection->>'sourceRef'
	`, job.ID, run.ID, cfg.Host, cfg.Port, cfg.Database, refs, tables, job.workspaceID, job.Source)
	if err != nil {
		return err
	}
	defer rows.Close()
	assetIDs := make(map[string]int64, len(tables))
	for rows.Next() {
		var assetID int64
		var sourceRef string
		if err := rows.Scan(&assetID, &sourceRef); err != nil {
			return err
		}
		assetIDs[sourceRef] = assetID
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(assetIDs) != len(tables) {
		return ErrInvalid
	}
	if err := persistAutomaticClassifications(ctx, tx, job, run, tables, assetIDs); err != nil {
		return err
	}
	if err := addPostgresCheckEvidence(ctx, tx, job, cfg, run, tables, assetIDs); err != nil {
		return err
	}
	return captureDataFlowSnapshot(ctx, tx, job.workspaceID, job.actorID, "postgresql")
}

func addPostgresCheckEvidence(ctx context.Context, tx pgx.Tx, job AutomationJob, cfg ManagedPostgresConfig,
	run AutomationRun, tables []collector.PostgreSQLTable, assetIDs map[string]int64,
) error {
	byAssetID := make(map[int64]collector.PostgreSQLTable, len(tables))
	ids := make([]int64, 0, len(tables))
	for _, table := range tables {
		id := assetIDs[table.SourceRef]
		ids = append(ids, id)
		byAssetID[id] = table
	}
	if cfg.AutoChecks {
		if err := ensurePostgresChecks(ctx, tx, job, cfg, ids); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT c.id, c.asset_id, c.status, c.requirement_code
		FROM plume.postgres_check_bindings b
		JOIN plume.checks c ON c.id = b.check_id
		WHERE b.job_id = $1 AND c.workspace_id = $2 AND c.asset_id = ANY($3)
		  AND c.feishu_target_digest IS NULL AND c.requirement_code IN ('G02', 'G03')
		  AND c.status IN ('draft', 'needs_work')
		  AND (b.source_host, b.source_port, b.source_database) = ($4, $5, $6)
		ORDER BY c.id FOR NO KEY UPDATE OF c
	`, job.ID, job.workspaceID, ids, cfg.Host, cfg.Port, cfg.Database)
	if err != nil {
		return err
	}
	checks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Check, error) {
		var check Check
		err := row.Scan(&check.ID, &check.AssetID, &check.Status, &check.Requirement.Code)
		return check, err
	})
	if err != nil {
		return err
	}
	for _, check := range checks {
		if err := appendPostgresEvidence(ctx, tx, job, cfg, run, check, byAssetID[check.AssetID]); err != nil {
			return err
		}
	}
	return nil
}

func ensurePostgresChecks(ctx context.Context, tx pgx.Tx, job AutomationJob, cfg ManagedPostgresConfig, assetIDs []int64) error {
	// The managed job lock also serializes manual bindings for this source.
	_, err := tx.Exec(ctx, `
		WITH new_checks AS (
			INSERT INTO plume.checks (workspace_id, asset_id, requirement_code, owner_team, note, created_by)
			SELECT a.workspace_id, a.id, rule.code, COALESCE(NULLIF(left(a.owner_team, 80), ''), '自动采集'), rule.note, $7
			FROM plume.assets a
			CROSS JOIN (VALUES
				('G02', '由 PostgreSQL 采集生成。依据采用的标准核对源内统计、自动分类及尚待采证的定级条件。'),
				('G03', '由 PostgreSQL 采集生成。检查表和字段的直接 SELECT 授权，实际访问及其他保护措施需要复核。')
			) AS rule(code, note)
			WHERE a.workspace_id = $1 AND a.id = ANY($2)
			  AND NOT EXISTS (
				SELECT 1 FROM plume.postgres_check_bindings b
				JOIN plume.checks c ON c.id = b.check_id
				WHERE b.job_id = $3 AND c.workspace_id = a.workspace_id AND c.asset_id = a.id
				  AND c.requirement_code = rule.code AND c.feishu_target_digest IS NULL
				  AND (b.source_host, b.source_port, b.source_database) = ($4, $5, $6)
				  AND (c.status <> 'verified' OR rule.code <> 'G02' OR COALESCE((
				      SELECT i.classification->>'comparison' FROM plume.postgres_asset_inspections i WHERE i.asset_id = a.id
				  ), '') <> 'conflict')
			  )
			ORDER BY a.id, rule.code
			RETURNING id
		), bindings AS (
			INSERT INTO plume.postgres_check_bindings (check_id, job_id, source_host, source_port, source_database)
			SELECT id, $3, $4, $5, $6 FROM new_checks
			RETURNING check_id
		)
		INSERT INTO plume.check_events (check_id, action, actor_id, note)
		SELECT b.check_id, event.action, $7, event.note
		FROM bindings b
		CROSS JOIN (VALUES
			(1, 'created', '由 PostgreSQL 自动采集生成'),
			(2, 'postgres_binding', '自动关联 PostgreSQL 采集任务')
		) AS event(position, action, note)
		ORDER BY b.check_id, event.position
	`, job.workspaceID, assetIDs, job.ID, cfg.Host, cfg.Port, cfg.Database, job.actorID)
	return err
}

func appendPostgresEvidence(ctx context.Context, tx pgx.Tx, job AutomationJob, cfg ManagedPostgresConfig, run AutomationRun,
	check Check, table collector.PostgreSQLTable,
) error {
	kind := "observation"
	var cycleID int64
	if check.Status == "needs_work" {
		change, err := latestCheckEvent(ctx, tx, check.ID, "changes")
		if err != nil {
			return err
		}
		remediatedAt, err := checkRemediationAt(ctx, tx, check.ID, change)
		if err != nil || remediatedAt.IsZero() || !run.ObservedAt.After(remediatedAt) {
			return err
		}
		kind, cycleID = "retest", change.ID
	}

	observation, summary, facts := evaluatePostgresInspection(table, cfg.Database, check.Requirement.Code)
	if check.Requirement.Code == "G02" {
		if err := tx.QueryRow(ctx, "SELECT classification FROM plume.postgres_asset_inspections WHERE asset_id = $1 AND automation_run_id = $2",
			check.AssetID, run.ID).Scan(&observation.Classification); err != nil {
			return err
		}
		if observation.Classification != nil {
			observation.Scope = "依据所采用标准和源内有界聚合判定数据属性，抽样范围与定级取证缺口见规则快照。"
			summary = fmt.Sprintf("已采集 %d 个字段，采用 %s 自动判定并与分类版本 %d 比对。",
				observation.ColumnCount, observation.Classification.Evaluation.Standard.Code, observation.Classification.ClassificationRevision)
			observation.Assessment = "review_required"
			observation.AssessmentReasons = []string{"classification_" + observation.Classification.Comparison}
		}
	}
	payload, err := json.Marshal(struct {
		Assessment        string
		AssessmentReasons []string
		Facts             any
		Classification    json.RawMessage
	}{observation.Assessment, observation.AssessmentReasons, facts, automaticClassificationFacts(observation.Classification)})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	var previousDigest string
	var previousJobID int64
	var previousAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT p.fact_digest, r.job_id, e.observed_at
		FROM plume.check_evidence e
		JOIN plume.postgres_check_evidence p ON p.evidence_id = e.id
		JOIN plume.automation_runs r ON r.id = p.automation_run_id
		WHERE e.check_id = $1 AND p.cycle_id = $2
		ORDER BY e.id DESC LIMIT 1
	`, check.ID, cycleID).Scan(&previousDigest, &previousJobID, &previousAt)
	if err == nil && (!run.ObservedAt.After(previousAt) || (previousJobID == job.ID && previousDigest == digest)) {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	input := EvidenceInput{
		Kind: kind, SourceSystem: "PostgreSQL 自动采集", SourceRecord: fmt.Sprintf("采集任务 #%d · 运行 #%d", job.ID, run.ID),
		Summary: summary + observation.Scope, ObservedAt: run.ObservedAt,
	}
	id, err := addEvidenceTx(ctx, tx, check, job.actorID, input, true)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO plume.postgres_check_evidence (evidence_id, automation_run_id, cycle_id, fact_digest, observation)
		VALUES ($1, $2, $3, $4, $5)
	`, id, run.ID, cycleID, digest, observation)
	return err
}

func evaluatePostgresInspection(table collector.PostgreSQLTable, database, requirementCode string) (PostgresObservation, string, any) {
	var scope, summary string
	observation := summarizePostgresInspection(table, database)
	var facts any
	if requirementCode == "G02" {
		scope = "依据字段名、注释、类型和可用的源内格式统计识别属性，实际观察范围与业务定级条件需要核对。"
		summary = fmt.Sprintf("已观测 %d 个字段，其中 %d 个字段的名称或类型提示可能涉及敏感数据。", observation.ColumnCount, observation.SensitiveCandidateCount)
		observation.Assessment = "no_signal"
		observation.AssessmentReasons = []string{"no_sensitive_candidate"}
		if observation.SensitiveCandidateCount > 0 {
			observation.Assessment = "review_required"
			observation.AssessmentReasons = []string{"sensitive_candidate"}
		}
		facts = table.Columns
	} else {
		scope = "仅观测表和字段的直接授权及 schema USAGE。实际访问受角色继承和行级策略等影响。加密及备份需另行验证。"
		summary = fmt.Sprintf("已观测 %d 条表或字段 ACL。PUBLIC 的 schema USAGE %s，表级 SELECT %s，字段级 SELECT 涉及 %d 个字段。",
			len(table.Grants), observedFlag(table.PublicSchemaUsage), observedFlag(table.PublicSelect), len(table.PublicSelectColumns))
		if table.PublicSelect || len(table.PublicSelectColumns) > 0 {
			observation.AssessmentReasons = append(observation.AssessmentReasons, "public_select")
		}
		for _, grant := range table.Grants {
			if grant.Privilege == "SELECT" && !grant.Public && grant.Grantee != table.Owner {
				observation.AssessmentReasons = append(observation.AssessmentReasons, "named_select")
				break
			}
		}
		observation.Assessment = "review_required"
		if len(observation.AssessmentReasons) == 0 {
			observation.Assessment = "no_signal"
			observation.AssessmentReasons = []string{"no_non_owner_select"}
		}
		facts = struct {
			Owner             string
			Grants            []collector.PostgreSQLGrant
			PublicSchemaUsage bool
			PublicSelect      bool
			PublicColumns     []string
		}{table.Owner, table.Grants, table.PublicSchemaUsage, table.PublicSelect, table.PublicSelectColumns}
	}
	observation.Scope = scope
	return observation, summary, facts
}

func observedFlag(observed bool) string {
	if observed {
		return "已观测"
	}
	return "未观测"
}

func summarizePostgresInspection(table collector.PostgreSQLTable, database string) PostgresObservation {
	const maxDetail = 20
	observation := PostgresObservation{
		SourceRef: table.SourceRef, Schema: table.Schema, Table: table.Name, Database: database,
		ColumnCount: len(table.Columns), GrantCount: len(table.Grants),
		PublicSchemaUsage: table.PublicSchemaUsage, PublicSelect: table.PublicSelect,
		PublicSelectColumnCount: len(table.PublicSelectColumns),
	}
	for _, field := range postgresSensitiveFields(table) {
		observation.SensitiveCandidateCount++
		if len(observation.SensitiveFields) == maxDetail {
			observation.SensitiveFieldsOmitted++
			continue
		}
		observation.SensitiveFields = append(observation.SensitiveFields, field)
	}
	for _, grant := range table.Grants {
		if grant.Privilege != "SELECT" || (!grant.Public && grant.Grantee == table.Owner) {
			continue
		}
		if len(observation.SelectGrants) == maxDetail {
			observation.SelectGrantsOmitted++
			continue
		}
		observation.SelectGrants = append(observation.SelectGrants, PostgresSelectGrant{
			Grantee: grant.Grantee, Public: grant.Public, Column: grant.Column, SchemaUsage: grant.SchemaUsage,
		})
	}
	return observation
}

func postgresSensitiveFields(table collector.PostgreSQLTable) []PostgresSensitiveField {
	priorities := []string{"national_id", "financial_account", "credential", "email", "phone", "personal_name", "address", "ip_address"}
	byColumn := make(map[string]string, len(table.Columns))
	for _, column := range table.Columns {
		if slices.Contains(priorities, column.Sensitivity) {
			byColumn[column.Name] = column.Sensitivity
		}
	}
	if analysis := table.ContentAnalysis; analysis != nil && (analysis.Status == "complete" || analysis.Status == "partial") {
		for _, column := range analysis.Columns {
			if column.Status != "complete" {
				continue
			}
			for _, match := range column.Matches {
				rank := slices.Index(priorities, match.Category)
				if rank < 0 || match.Count <= 0 || match.Count > column.NonNullRows {
					continue
				}
				if current := byColumn[column.Column]; current == "" || rank < slices.Index(priorities, current) {
					byColumn[column.Column] = match.Category
				}
			}
		}
	}
	fields := make([]PostgresSensitiveField, 0, len(byColumn))
	for _, column := range table.Columns {
		if category := byColumn[column.Name]; category != "" {
			fields = append(fields, PostgresSensitiveField{Name: column.Name, Sensitivity: category})
		}
	}
	return fields
}
