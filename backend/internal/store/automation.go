package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

var ErrAutomationUnauthorized = errors.New("invalid or revoked collection token")

var automationRunID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type AutomationJobInput struct {
	Provider string `json:"provider"`
	Source   string `json:"source,omitempty"`
	CheckID  *int64 `json:"checkId,omitempty"`
}

type AutomationJob struct {
	AutomationJobInput
	ID        int64                  `json:"id"`
	CreatedAt time.Time              `json:"createdAt"`
	RevokedAt *time.Time             `json:"revokedAt,omitempty"`
	LatestRun *AutomationRun         `json:"latestRun,omitempty"`
	Managed   *ManagedPostgresConfig `json:"managed,omitempty"`

	workspaceID int64
	actorID     int64
}

type AutomationRun struct {
	ID            int64     `json:"id"`
	JobID         int64     `json:"jobId"`
	RunID         string    `json:"runId"`
	Status        string    `json:"status"`
	Code          string    `json:"code,omitempty"`
	ObservedAt    time.Time `json:"observedAt"`
	CreatedAt     time.Time `json:"createdAt"`
	CreatedCount  int       `json:"createdCount"`
	ExistingCount int       `json:"existingCount"`
	ImportID      *int64    `json:"importId,omitempty"`
	EvidenceID    *int64    `json:"evidenceId,omitempty"`
	MemberPresent *bool     `json:"memberPresent,omitempty"`
	Permission    string    `json:"permission,omitempty"`
	Replayed      bool      `json:"replayed,omitempty"`

	digest string
}

type AutomationDelivery struct {
	RunID       string
	Digest      string
	Provider    string
	Source      string
	FailureCode string
	ObservedAt  time.Time
	Assets      []CollectedAsset
	Evidence    *EvidenceInput
}

const automationJobColumns = "j.id, j.workspace_id, j.actor_id, j.provider, COALESCE(j.source, ''), j.check_id, j.created_at, j.revoked_at"
const automationRunColumns = "id, job_id, run_id, payload_digest, status, COALESCE(code, ''), observed_at, created_at, created_count, existing_count, import_id, evidence_id, member_present, COALESCE(permission, '')"

func automationJobFields(job *AutomationJob) []any {
	return []any{&job.ID, &job.workspaceID, &job.actorID, &job.Provider, &job.Source, &job.CheckID, &job.CreatedAt, &job.RevokedAt}
}

func scanAutomationRun(row pgx.Row) (AutomationRun, error) {
	var run AutomationRun
	err := row.Scan(&run.ID, &run.JobID, &run.RunID, &run.digest, &run.Status, &run.Code,
		&run.ObservedAt, &run.CreatedAt, &run.CreatedCount, &run.ExistingCount, &run.ImportID,
		&run.EvidenceID, &run.MemberPresent, &run.Permission)
	return run, err
}

func validAutomationAssetSource(source string) bool {
	return validCheckText(source, 80) && strings.TrimSpace(source) == source &&
		strings.IndexFunc(source, unicode.IsControl) < 0
}

func validCollectionFailure(code string) bool {
	return code == "collection_failed" || code == "collection_timeout"
}

func (s *Store) CreateAutomationJob(ctx context.Context, workspaceID, actorID int64, input AutomationJobInput, tokenDigest string) (AutomationJob, error) {
	if !digestPattern.MatchString(tokenDigest) {
		return AutomationJob{}, ErrInvalid
	}
	switch input.Provider {
	case "postgresql", "openmetadata":
		if input.CheckID != nil || !validAutomationAssetSource(input.Source) {
			return AutomationJob{}, ErrInvalid
		}
	case "feishu":
		if input.CheckID == nil || *input.CheckID <= 0 || input.Source != "" {
			return AutomationJob{}, ErrInvalid
		}
	default:
		return AutomationJob{}, ErrInvalid
	}

	var job AutomationJob
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		if input.CheckID != nil {
			var bound bool
			if err := tx.QueryRow(ctx, "SELECT feishu_target_digest IS NOT NULL FROM plume.checks WHERE workspace_id = $1 AND id = $2",
				workspaceID, *input.CheckID).Scan(&bound); err != nil {
				return storeError(err)
			}
			if !bound {
				return ErrInvalid
			}
		}
		return tx.QueryRow(ctx, `
			INSERT INTO plume.automation_jobs AS j (workspace_id, actor_id, provider, source, check_id, token_digest)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6) RETURNING `+automationJobColumns,
			workspaceID, actorID, input.Provider, input.Source, input.CheckID, tokenDigest,
		).Scan(automationJobFields(&job)...)
	})
	return job, storeError(err)
}

func (s *Store) ListAutomationJobs(ctx context.Context, workspaceID, actorID int64) ([]AutomationJob, error) {
	jobs := make([]AutomationJob, 0)
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+automationJobColumns+`, latest.data, `+managedPostgresPublicJSON+`
			FROM plume.automation_jobs j
			LEFT JOIN plume.managed_postgres_jobs mp ON mp.job_id = j.id
			LEFT JOIN LATERAL (
				SELECT jsonb_build_object('id', r.id, 'jobId', r.job_id, 'runId', r.run_id,
					'status', r.status, 'code', r.code, 'observedAt', r.observed_at, 'createdAt', r.created_at,
					'createdCount', r.created_count, 'existingCount', r.existing_count,
					'importId', r.import_id, 'evidenceId', r.evidence_id,
					'memberPresent', r.member_present, 'permission', r.permission) AS data
				FROM plume.automation_runs r WHERE r.job_id = j.id ORDER BY r.id DESC LIMIT 1
			) latest ON true
			WHERE j.workspace_id = $1 ORDER BY j.revoked_at IS NOT NULL, j.id DESC`, workspaceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var job AutomationJob
			if err := rows.Scan(append(automationJobFields(&job), &job.LatestRun, &job.Managed)...); err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	return jobs, err
}

func (s *Store) RevokeAutomationJob(ctx context.Context, workspaceID, actorID, jobID int64) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if err != nil {
			return err
		}
		if role != "owner" {
			return ErrForbidden
		}
		result, err := tx.Exec(ctx, `UPDATE plume.automation_jobs SET revoked_at = COALESCE(revoked_at, clock_timestamp())
			WHERE workspace_id = $1 AND id = $2`, workspaceID, jobID)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) ListAutomationRuns(ctx context.Context, workspaceID, actorID, jobID int64) ([]AutomationRun, error) {
	runs := make([]AutomationRun, 0)
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, "SELECT id FROM plume.automation_jobs WHERE workspace_id = $1 AND id = $2",
			workspaceID, jobID).Scan(&id); err != nil {
			return storeError(err)
		}
		rows, err := tx.Query(ctx, "SELECT "+automationRunColumns+`
			FROM plume.automation_runs WHERE job_id = $1 ORDER BY id DESC LIMIT 25`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanAutomationRun(rows)
			if err != nil {
				return err
			}
			runs = append(runs, run)
		}
		return rows.Err()
	})
	return runs, err
}

func (s *Store) DeliverAutomationRun(ctx context.Context, tokenDigest string, input AutomationDelivery) (AutomationRun, error) {
	if !digestPattern.MatchString(tokenDigest) {
		return AutomationRun{}, ErrAutomationUnauthorized
	}
	var run AutomationRun
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var job AutomationJob
		err := tx.QueryRow(ctx, "SELECT "+automationJobColumns+`
			FROM plume.automation_jobs j WHERE j.token_digest = $1 AND j.revoked_at IS NULL
				AND NOT EXISTS (SELECT 1 FROM plume.managed_postgres_jobs mp WHERE mp.job_id = j.id)
			FOR UPDATE OF j`,
			tokenDigest).Scan(automationJobFields(&job)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAutomationUnauthorized
		}
		if err != nil {
			return err
		}
		role, err := checkRole(ctx, tx, job.workspaceID, job.actorID, true)
		if errors.Is(err, ErrNotFound) || (err == nil && !checkEditor(role)) {
			return ErrAutomationUnauthorized
		}
		if err != nil {
			return err
		}
		if input.Provider != job.Provider || !automationRunID.MatchString(input.RunID) ||
			!digestPattern.MatchString(input.Digest) || input.ObservedAt.IsZero() ||
			input.ObservedAt.After(time.Now().Add(5*time.Minute)) {
			return ErrInvalid
		}

		previous, err := scanAutomationRun(tx.QueryRow(ctx, "SELECT "+automationRunColumns+
			" FROM plume.automation_runs WHERE job_id = $1 AND run_id = $2", job.ID, input.RunID))
		if err == nil {
			if previous.digest != input.Digest {
				return ErrConflict
			}
			previous.Replayed = true
			run = previous
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		run = AutomationRun{JobID: job.ID, RunID: input.RunID, ObservedAt: input.ObservedAt, Status: "success"}
		switch {
		case input.FailureCode != "":
			if !validCollectionFailure(input.FailureCode) ||
				input.Assets != nil || input.Evidence != nil {
				return ErrInvalid
			}
			run.Status, run.Code = "error", input.FailureCode
		case job.CheckID != nil:
			if input.Source != "" || input.Assets != nil || input.Evidence == nil || input.Evidence.Feishu == nil {
				return ErrInvalid
			}
			if err := collectFeishuEvidence(ctx, tx, job, *input.Evidence, &run); err != nil {
				return err
			}
		default:
			if input.Source != job.Source || input.Assets == nil || len(input.Assets) > 1000 || input.Evidence != nil {
				return ErrInvalid
			}
			if err := importAutomationAssets(ctx, tx, job, input.Digest, input.Assets, &run); err != nil {
				return err
			}
		}
		return recordAutomationRun(ctx, tx, input.Digest, &run)
	})
	if err != nil {
		return AutomationRun{}, storeError(err)
	}
	return run, nil
}

func importAutomationAssets(ctx context.Context, tx pgx.Tx, job AutomationJob, digest string, assets []CollectedAsset, run *AutomationRun) error {
	batchID := fmt.Sprintf("auto-%d-%s", job.ID, run.RunID)
	imported, err := importAssetsTx(ctx, tx, job.workspaceID, job.actorID, job.Provider, job.Source, batchID, digest, assets)
	if err != nil {
		return err
	}
	run.ImportID, run.CreatedCount, run.ExistingCount = &imported.ID, imported.Created, imported.Existing
	return nil
}

func recordAutomationRun(ctx context.Context, tx pgx.Tx, digest string, run *AutomationRun) error {
	return tx.QueryRow(ctx, `
		INSERT INTO plume.automation_runs (job_id, run_id, payload_digest, status, code, observed_at,
			created_count, existing_count, import_id, evidence_id, member_present, permission)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9, $10, $11, NULLIF($12, ''))
		RETURNING id, observed_at, created_at
	`, run.JobID, run.RunID, digest, run.Status, run.Code, run.ObservedAt, run.CreatedCount,
		run.ExistingCount, run.ImportID, run.EvidenceID, run.MemberPresent, run.Permission,
	).Scan(&run.ID, &run.ObservedAt, &run.CreatedAt)
}

func collectFeishuEvidence(ctx context.Context, tx pgx.Tx, job AutomationJob, input EvidenceInput, run *AutomationRun) error {
	if !input.ObservedAt.Equal(run.ObservedAt) || validateEvidence(&input) != nil ||
		!digestPattern.MatchString(input.Feishu.SnapshotDigest) || !validFeishuPermission(*input.Feishu) {
		return ErrInvalid
	}
	check, err := scanCheck(tx.QueryRow(ctx, checkSelect+" WHERE c.workspace_id = $1 AND c.id = $2 FOR NO KEY UPDATE OF c",
		job.workspaceID, *job.CheckID))
	if err != nil {
		return err
	}
	if input.Feishu.TargetDigest != check.FeishuTargetDigest {
		return ErrInvalid
	}

	run.MemberPresent = &input.Feishu.MemberPresent
	run.Permission = input.Feishu.Permission
	var lastRunAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT max(r.observed_at) FROM plume.automation_runs r JOIN plume.automation_jobs j ON j.id = r.job_id
		WHERE j.check_id = $1 AND r.member_present IS NOT NULL AND r.code IS DISTINCT FROM 'stale_observation'
	`, check.ID).Scan(&lastRunAt); err != nil {
		return err
	}
	lastEvidence, present, err := latestFeishuEvidence(ctx, tx, check.ID)
	if err != nil {
		return err
	}
	if !input.ObservedAt.After(lastEvidence.ObservedAt) || (lastRunAt != nil && !input.ObservedAt.After(*lastRunAt)) {
		run.Status, run.Code = "skipped", "stale_observation"
		return nil
	}
	if !evidenceOpen(check) {
		run.Status, run.Code = "skipped", "check_closed"
		return nil
	}

	var remediatedAt time.Time
	if check.Status == "needs_work" {
		change, err := latestCheckEvent(ctx, tx, check.ID, "changes")
		if err != nil {
			return err
		}
		remediatedAt, err = feishuRemediationAt(ctx, tx, check.ID, change)
		if err != nil {
			return err
		}
		if remediatedAt.IsZero() || !input.ObservedAt.After(remediatedAt) {
			run.Status, run.Code = "skipped", "awaiting_remediation"
			return nil
		}
	}

	if lastEvidence.ID != 0 && lastEvidence.ObservedAt.After(remediatedAt) && present == *run.MemberPresent {
		var unchanged bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM plume.automation_runs
			WHERE job_id = $1 AND evidence_id = $2 AND permission IS NOT DISTINCT FROM NULLIF($3, ''))
		`, job.ID, lastEvidence.ID, run.Permission).Scan(&unchanged); err != nil {
			return err
		}
		if unchanged {
			run.Status, run.Code = "skipped", "unchanged"
			return nil
		}
	}

	id, err := addEvidenceTx(ctx, tx, check, job.actorID, input, true)
	if err != nil {
		return err
	}
	run.EvidenceID = &id
	return nil
}
