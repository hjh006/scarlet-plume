package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/jackc/pgx/v5"
)

func assessmentDigest(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
func assessmentActivityDigest(activity Activity) string {
	return assessmentDigest(activity.ActivityInput)
}
func assessmentFacts(ctx context.Context, tx pgx.Tx, workspaceID int64, activity Activity) ([]AssessmentFact, error) {
	facts := []AssessmentFact{}
	add := func(key, value, source, record string, observed *time.Time) {
		state := "known"
		if strings.TrimSpace(value) == "" || value == "unknown" {
			state = "unknown"
			value = ""
		}
		facts = append(facts, AssessmentFact{Key: key, State: state, Value: value, Source: source, Record: record, ObservedAt: observed})
	}
	ref := fmt.Sprint(activity.ID)
	add("activity_kind", activity.Kind, "activity", ref, nil)
	if activity.Kind == "ai" || activity.Kind == "cross_border" {
		add("assessment_scope", activity.Kind, "activity", ref, nil)
	} else {
		add("assessment_scope", "", "business_fact", "requires_verified_business_evidence", nil)
	}
	add("activity_purpose", activity.Purpose, "activity", ref, nil)
	add("activity_recipient", activity.Recipient, "activity", ref, nil)
	add("activity_region", activity.Region, "activity", ref, nil)
	add("activity_retention", activity.RetentionPolicy, "activity", ref, nil)
	add("activity_transfer", activity.TransferPath, "activity", ref, nil)
	add("activity_safeguards", activity.Safeguards, "activity", ref, nil)
	var category, level, statutory string
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT category,business_level,statutory_category,classification_revision FROM plume.assets WHERE workspace_id=$1 AND id=$2`, workspaceID, activity.AssetID).Scan(&category, &level, &statutory, &revision); err != nil {
		return nil, storeError(err)
	}
	add("asset_category", category, "classification", fmt.Sprintf("asset:%d/revision:%d", activity.AssetID, revision), nil)
	add("asset_business_level", level, "classification", fmt.Sprint(revision), nil)
	add("asset_statutory_category", statutory, "classification", fmt.Sprint(revision), nil)
	var table collector.PostgreSQLTable
	var run string
	var at time.Time
	err := tx.QueryRow(ctx, `SELECT i.inspection,r.run_id,r.observed_at FROM plume.postgres_asset_inspections i
 JOIN plume.assets a ON a.id=i.asset_id JOIN plume.automation_jobs j ON j.id=i.job_id AND j.workspace_id=a.workspace_id
 JOIN plume.managed_postgres_jobs p ON p.job_id=j.id
 JOIN plume.automation_runs r ON r.id=i.automation_run_id AND r.status='success'
 WHERE a.workspace_id=$1 AND a.id=$2 AND j.revoked_at IS NULL
 AND (i.source_host,i.source_port,i.source_database)=(p.host,p.port,p.database_name)
 AND r.id=(SELECT latest.id FROM plume.automation_runs latest WHERE latest.job_id=i.job_id ORDER BY latest.id DESC LIMIT 1)`, workspaceID, activity.AssetID).Scan(&table, &run, &at)
	if err == nil {
		fields := postgresSensitiveFields(table)
		types := map[string]bool{}
		for _, field := range fields {
			types[field.Sensitivity] = true
		}
		add("source_observed_types", strings.Join(sortedAssessmentKeys(types), ", "), "postgresql", run, &at)
		scope := "metadata"
		if table.ContentAnalysis != nil {
			scope = table.ContentAnalysis.Status + fmt.Sprintf(" / sampled rows %d", table.ContentAnalysis.RowsScanned)
		}
		add("source_coverage", scope, "postgresql", run, &at)
	} else if err == pgx.ErrNoRows {
		add("source_observed_types", "", "source", "not_collected_or_stale", nil)
		add("source_coverage", "", "source", "not_collected_or_stale", nil)
	} else {
		return nil, err
	}
	// Business/legal facts are never inferred from a field count or a server region.
	for _, key := range []string{"deduplicated_subject_count", "cii_status", "cross_border_exception", "public_ai_service", "automated_decision", "processing_start_time", "lawful_basis", "notice_consent"} {
		add(key, "", "business_fact", "requires_verified_business_evidence", nil)
	}
	return facts, nil
}
func sortedAssessmentKeys(values map[string]bool) []string {
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
func markAssessmentsStale(ctx context.Context, tx pgx.Tx, workspaceID, activityID, actorID int64) error {
	rows, err := tx.Query(ctx, `UPDATE plume.assessments SET stale=true,reassessment_pending=true,revision=revision+1,updated_at=clock_timestamp()
 WHERE workspace_id=$1 AND activity_id=$2 AND NOT stale RETURNING id`, workspaceID, activityID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO plume.assessment_reassessment_tasks(assessment_id,reason)VALUES($1,'activity_changed')ON CONFLICT(assessment_id)DO UPDATE SET state='pending',reason=excluded.reason,resolved_at=NULL`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plume.assessment_events(assessment_id,actor_id,action,note)VALUES($1,$2,'stale','activity_changed')`, id, actorID); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) RefreshDueAssessments(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 50 {
		return 0, ErrInvalid
	}
	refreshed := 0
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,workspace_id FROM plume.assessments WHERE next_refresh_at<=statement_timestamp() ORDER BY next_refresh_at,id FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
		if err != nil {
			return err
		}
		type target struct{ id, workspace int64 }
		targets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (target, error) {
			var t target
			err := row.Scan(&t.id, &t.workspace)
			return t, err
		})
		if err != nil {
			return err
		}
		for _, t := range targets {
			if err := refreshAssessmentFacts(ctx, tx, t.workspace, 0, t.id, false); err != nil {
				return err
			}
			refreshed++
		}
		return nil
	})
	return refreshed, storeError(err)
}
