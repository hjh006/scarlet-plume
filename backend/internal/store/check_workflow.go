package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) AddEvidence(ctx context.Context, workspaceID, actorID, checkID int64, input EvidenceInput) error {
	if err := validateEvidence(&input); err != nil {
		return err
	}

	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		check, err := checkEdit(ctx, tx, workspaceID, actorID, checkID)
		if err != nil {
			return err
		}
		if input.Kind == "retest" {
			finding, err := runtimeFindingForCheck(ctx, tx, check)
			if err != nil {
				return err
			}
			if finding != nil {
				return ErrEvidenceRequired
			}
		}

		_, err = addEvidenceTx(ctx, tx, check, actorID, input, false)
		return err
	})
}

func validateEvidence(input *EvidenceInput) error {
	input.ObservedAt = input.ObservedAt.Truncate(time.Microsecond)
	if !validCheckText(input.SourceSystem, 100) || !validCheckText(input.SourceRecord, 160) ||
		!validCheckText(input.Summary, 600) || input.ObservedAt.IsZero() ||
		input.ObservedAt.After(time.Now().Add(5*time.Minute)) {
		return ErrInvalid
	}

	return nil
}

func addEvidenceTx(ctx context.Context, tx pgx.Tx, check Check, actorID int64, input EvidenceInput, automated bool) (int64, error) {
	var snapshotDigest, permission *string
	var memberPresent *bool
	if feishu := input.Feishu; feishu != nil {
		if check.FeishuTargetDigest == "" || feishu.TargetDigest != check.FeishuTargetDigest ||
			!digestPattern.MatchString(feishu.SnapshotDigest) || !validFeishuPermission(*feishu) {
			return 0, ErrInvalid
		}
		snapshotDigest, memberPresent = &feishu.SnapshotDigest, &feishu.MemberPresent
		if feishu.Permission != "" {
			permission = &feishu.Permission
		}
		if check.Status == "draft" {
			input.Kind = "observation"
		} else {
			input.Kind = "retest"
			change, err := latestCheckEvent(ctx, tx, check.ID, "changes")
			if err != nil {
				return 0, err
			}
			if change.ID == 0 || !input.ObservedAt.After(change.CreatedAt) {
				return 0, ErrEvidenceRequired
			}
		}
	}

	var label string
	switch input.Kind {
	case "observation":
		label = "检查记录"
	case "remediation":
		label = "整改记录"
	case "retest":
		label = "复测记录"
	default:
		return 0, ErrInvalid
	}
	var evidenceID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO plume.check_evidence (
			check_id, kind, source_system, source_record, summary, observed_at, created_by,
			feishu_snapshot_digest, feishu_member_present, feishu_permission
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id
	`, check.ID, input.Kind, input.SourceSystem, input.SourceRecord, input.Summary,
		input.ObservedAt, actorID, snapshotDigest, memberPresent, permission).Scan(&evidenceID); err != nil {
		return 0, storeError(err)
	}
	if automated {
		label = "自动采集 · " + label
	}
	if err := recordCheckEvent(ctx, tx, check.ID, actorID, "evidence", label); err != nil {
		return 0, err
	}
	_, err := tx.Exec(ctx, "UPDATE plume.checks SET updated_at = clock_timestamp() WHERE id = $1", check.ID)
	return evidenceID, err
}

func validFeishuPermission(feishu FeishuObservation) bool {
	if !feishu.MemberPresent {
		return feishu.Permission == ""
	}
	switch feishu.Permission {
	case "view", "edit", "full_access":
		return true
	default:
		return false
	}
}

func latestCheckEvent(ctx context.Context, tx pgx.Tx, checkID int64, action string) (CheckEvent, error) {
	var event CheckEvent
	err := tx.QueryRow(ctx, `
		SELECT id, created_at FROM plume.check_events
		WHERE check_id = $1 AND action = $2 ORDER BY id DESC LIMIT 1
	`, checkID, action).Scan(&event.ID, &event.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CheckEvent{}, nil
	}
	return event, err
}

func latestFeishuEvidence(ctx context.Context, tx pgx.Tx, checkID int64) (Evidence, bool, error) {
	var evidence Evidence
	var present bool
	err := tx.QueryRow(ctx, `
		SELECT id, kind, observed_at, created_at, feishu_member_present FROM plume.check_evidence
		WHERE check_id = $1 AND feishu_snapshot_digest IS NOT NULL
		ORDER BY observed_at DESC, id DESC LIMIT 1
	`, checkID).Scan(&evidence.ID, &evidence.Kind, &evidence.ObservedAt, &evidence.CreatedAt, &present)
	if errors.Is(err, pgx.ErrNoRows) {
		return Evidence{}, false, nil
	}
	return evidence, present, err
}

func checkRemediationAt(ctx context.Context, tx pgx.Tx, checkID int64, change CheckEvent) (time.Time, error) {
	if change.ID == 0 {
		return time.Time{}, nil
	}
	var remediatedAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT GREATEST(observed_at, created_at) FROM plume.check_evidence
		WHERE check_id = $1 AND kind = 'remediation' AND observed_at > $2 AND created_at > $2
		ORDER BY id DESC LIMIT 1
	`, checkID, change.CreatedAt).Scan(&remediatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return remediatedAt, err
}

func submissionAllowed(ctx context.Context, tx pgx.Tx, check Check) (bool, error) {
	if check.FeishuTargetDigest != "" {
		evidence, present, err := latestFeishuEvidence(ctx, tx, check.ID)
		if err != nil || evidence.ID == 0 {
			return false, err
		}
		if check.Status == "draft" {
			return evidence.Kind == "observation", nil
		}
		if present || evidence.Kind != "retest" {
			return false, nil
		}
		change, err := latestCheckEvent(ctx, tx, check.ID, "changes")
		if err != nil {
			return false, err
		}
		remediatedAt, err := checkRemediationAt(ctx, tx, check.ID, change)
		if err != nil {
			return false, err
		}
		return !remediatedAt.IsZero() && evidence.ObservedAt.After(remediatedAt) && evidence.CreatedAt.After(remediatedAt), nil
	}
	var exists bool
	if check.Status == "draft" {
		err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM plume.check_evidence WHERE check_id = $1)", check.ID).Scan(&exists)
		return exists, err
	}
	if handled, allowed, err := automaticClassificationCheckAllowed(ctx, tx, check, true); handled || err != nil {
		return allowed, err
	}
	finding, err := runtimeFindingForCheck(ctx, tx, check)
	if err != nil {
		return false, err
	}
	if finding != nil {
		return runtimeRetestAllowed(ctx, tx, finding)
	}
	change, err := latestCheckEvent(ctx, tx, check.ID, "changes")
	if err != nil || change.ID == 0 {
		return false, err
	}
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM plume.check_evidence
		WHERE check_id = $1 AND kind = 'retest' AND created_at > $2 AND observed_at > $2)
	`, check.ID, change.CreatedAt).Scan(&exists)
	return exists, err
}

func (s *Store) SubmitCheck(ctx context.Context, workspaceID, actorID, checkID int64) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		check, err := checkEdit(ctx, tx, workspaceID, actorID, checkID)
		if err != nil {
			return err
		}
		allowed, err := submissionAllowed(ctx, tx, check)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrEvidenceRequired
		}
		if _, err := tx.Exec(ctx, `
			UPDATE plume.checks SET status = 'in_review', submitted_by = $2, updated_at = clock_timestamp() WHERE id = $1
		`, checkID, actorID); err != nil {
			return err
		}
		return recordCheckEvent(ctx, tx, checkID, actorID, "submitted", "")
	})
}

func independentReviewer(ctx context.Context, tx pgx.Tx, check Check, role string, actorID int64) (bool, error) {
	if (role != "owner" && role != "reviewer") || actorID == check.createdBy ||
		(check.submittedBy != nil && actorID == *check.submittedBy) {
		return false, nil
	}
	var authored bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM plume.check_evidence WHERE check_id = $1 AND created_by = $2)
	`, check.ID, actorID).Scan(&authored)
	return !authored, err
}

func verificationAllowed(ctx context.Context, tx pgx.Tx, check Check) (bool, error) {
	if check.FeishuTargetDigest == "" {
		finding, err := runtimeFindingForCheck(ctx, tx, check)
		if err != nil {
			return false, err
		}
		if finding != nil {
			return runtimeRetestAllowed(ctx, tx, finding)
		}
		if handled, allowed, err := automaticClassificationCheckAllowed(ctx, tx, check, false); handled || err != nil {
			return allowed, err
		}
		return true, nil
	}
	evidence, present, err := latestFeishuEvidence(ctx, tx, check.ID)
	if err != nil || evidence.ID == 0 || present {
		return false, err
	}
	var newerPresent bool
	err = tx.QueryRow(ctx, `
		SELECT r.member_present FROM plume.automation_runs r JOIN plume.automation_jobs j ON j.id = r.job_id
		WHERE j.check_id = $1 AND r.observed_at > $2 AND r.member_present IS NOT NULL
		  AND r.code IS DISTINCT FROM 'stale_observation'
		ORDER BY r.observed_at DESC, r.id DESC LIMIT 1
	`, check.ID, evidence.ObservedAt).Scan(&newerPresent)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return !newerPresent, err
}

func (s *Store) ReviewCheck(ctx context.Context, workspaceID, actorID, checkID, revision int64, decision, note string) error {
	if revision < 1 || (decision != "verified" && decision != "changes") || !validCheckText(note, 600) {
		return ErrInvalid
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if decision == "verified" {
			if err := lockClassificationAssetForCheck(ctx, tx, workspaceID, checkID); err != nil {
				return err
			}
			if err := lockRuntimeBaselineForCheck(ctx, tx, workspaceID, checkID); err != nil {
				return err
			}
		}
		check, err := scanCheck(tx.QueryRow(ctx, checkSelect+" WHERE c.workspace_id = $1 AND c.id = $2 FOR NO KEY UPDATE OF c", workspaceID, checkID))
		if err != nil {
			return err
		}
		allowed, err := independentReviewer(ctx, tx, check, role, actorID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		if check.Status != "in_review" {
			return ErrConflict
		}
		submission, err := latestCheckEvent(ctx, tx, checkID, "submitted")
		if err != nil {
			return err
		}
		if submission.ID != revision {
			return ErrConflict
		}
		status := "needs_work"
		if decision == "verified" {
			allowed, err := verificationAllowed(ctx, tx, check)
			if err != nil {
				return err
			}
			if !allowed {
				return ErrEvidenceRequired
			}
			status = "verified"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE plume.checks SET status = $2, updated_at = clock_timestamp() WHERE id = $1
		`, checkID, status); err != nil {
			return err
		}
		return recordCheckEvent(ctx, tx, checkID, actorID, decision, note)
	})
}
