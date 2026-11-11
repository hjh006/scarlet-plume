package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (s *Store) CheckDetail(ctx context.Context, workspaceID, actorID, checkID int64) (CheckDetail, error) {
	var detail CheckDetail
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, false)
		if err != nil {
			return err
		}
		check, err := scanCheck(tx.QueryRow(ctx, checkSelect+" WHERE c.workspace_id = $1 AND c.id = $2", workspaceID, checkID))
		if err != nil {
			return err
		}
		detail.Check = check
		classifications, err := readClassifications(ctx, tx, workspaceID, []int64{check.AssetID})
		if err != nil {
			return err
		}
		if len(classifications) != 1 {
			return ErrNotFound
		}
		detail.Classification = &classifications[0]
		if err := loadCheckPostgresBinding(ctx, tx, &detail, role); err != nil {
			return err
		}
		finding, err := runtimeFindingForCheck(ctx, tx, check)
		if err != nil {
			return err
		}
		if finding != nil {
			detail.RuntimeFinding = &finding.RuntimeFinding
		}
		detail.Evidence, err = listCheckEvidence(ctx, tx, check)
		if err != nil {
			return err
		}
		detail.Events, err = listCheckEvents(ctx, tx, check.ID)
		if err != nil {
			return err
		}
		detail.CanAddEvidence = checkEditor(role) && evidenceOpen(check)
		if detail.CanAddEvidence {
			detail.CanSubmit, err = submissionAllowed(ctx, tx, check)
			return err
		}
		if check.Status != "in_review" {
			return nil
		}
		detail.CanReview, err = independentReviewer(ctx, tx, check, role, actorID)
		if err != nil || !detail.CanReview {
			return err
		}
		submission, err := latestCheckEvent(ctx, tx, check.ID, "submitted")
		if err != nil {
			return err
		}
		detail.ReviewRevision = submission.ID
		detail.CanReview = submission.ID != 0
		if detail.CanReview {
			detail.CanVerify, err = verificationAllowed(ctx, tx, check)
		}
		return err
	})
	return detail, err
}

func listCheckEvidence(ctx context.Context, tx pgx.Tx, check Check) ([]Evidence, error) {
	rows, err := tx.Query(ctx, `
		SELECT e.id, e.kind, e.source_system, e.source_record, e.summary, e.observed_at,
		       e.created_at, u.username, e.feishu_snapshot_digest, e.feishu_member_present, COALESCE(e.feishu_permission, ''),
		       COALESCE(r.job_id, pr.job_id), COALESCE(r.run_id, pr.run_id, ''), p.observation
		FROM plume.check_evidence e JOIN plume.users u ON u.id = e.created_by
		LEFT JOIN plume.automation_runs r ON r.evidence_id = e.id
		LEFT JOIN plume.postgres_check_evidence p ON p.evidence_id = e.id
		LEFT JOIN plume.automation_runs pr ON pr.id = p.automation_run_id
		WHERE e.check_id = $1 ORDER BY e.id DESC`, check.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evidence := make([]Evidence, 0)
	for rows.Next() {
		var entry Evidence
		var snapshotDigest *string
		var present *bool
		var permission string
		if err := rows.Scan(&entry.ID, &entry.Kind, &entry.SourceSystem, &entry.SourceRecord, &entry.Summary,
			&entry.ObservedAt, &entry.CreatedAt, &entry.RecordedBy, &snapshotDigest, &present, &permission,
			&entry.AutomationJobID, &entry.AutomationRunID, &entry.Postgres); err != nil {
			return nil, err
		}
		if snapshotDigest != nil {
			entry.Feishu = &FeishuObservation{
				TargetDigest: check.FeishuTargetDigest, SnapshotDigest: *snapshotDigest, MemberPresent: *present, Permission: permission,
			}
		}
		evidence = append(evidence, entry)
	}
	return evidence, rows.Err()
}

func listCheckEvents(ctx context.Context, tx pgx.Tx, checkID int64) ([]CheckEvent, error) {
	rows, err := tx.Query(ctx, `
		SELECT e.id, e.action, u.username, e.note, e.created_at
		FROM plume.check_events e JOIN plume.users u ON u.id = e.actor_id
		WHERE e.check_id = $1 ORDER BY e.id DESC`, checkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]CheckEvent, 0)
	for rows.Next() {
		var event CheckEvent
		if err := rows.Scan(&event.ID, &event.Action, &event.Actor, &event.Note, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
