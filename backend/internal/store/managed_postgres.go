package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type ManagedPostgresInput struct {
	Host              string   `json:"host"`
	Port              int      `json:"port"`
	Database          string   `json:"database"`
	User              string   `json:"user"`
	Schemas           []string `json:"schemas"`
	IntervalMinutes   int      `json:"intervalMinutes"`
	Enabled           bool     `json:"enabled"`
	EncryptedPassword []byte   `json:"-"`
}

type ManagedPostgresConfig struct {
	ManagedPostgresInput
	Revision  int64      `json:"revision"`
	NextRunAt *time.Time `json:"nextRunAt,omitempty"`
	Queued    bool       `json:"queued"`
	Running   bool       `json:"running"`
}

type ManagedPostgresClaim struct {
	JobID  int64
	Source string
	RunID  string
	Config ManagedPostgresConfig

	claimedAt time.Time
}

const managedPostgresFields = `
	mp.host, mp.port, mp.database_name, mp.username, mp.schemas, mp.encrypted_password,
	mp.interval_minutes, mp.enabled, mp.revision, mp.next_run_at, mp.requested_at IS NOT NULL,
	COALESCE(mp.lease_until > clock_timestamp(), false)`

const managedPostgresPublicJSON = `
	CASE WHEN mp.job_id IS NOT NULL THEN jsonb_build_object(
		'host', mp.host, 'port', mp.port, 'database', mp.database_name, 'user', mp.username,
		'schemas', mp.schemas, 'intervalMinutes', mp.interval_minutes, 'enabled', mp.enabled AND j.revoked_at IS NULL,
		'revision', mp.revision, 'nextRunAt', CASE WHEN j.revoked_at IS NULL THEN mp.next_run_at END,
		'queued', j.revoked_at IS NULL AND mp.requested_at IS NOT NULL,
		'running', j.revoked_at IS NULL AND COALESCE(mp.lease_until > clock_timestamp(), false)
	) END`

func managedPostgresScan(c *ManagedPostgresConfig) []any {
	return []any{
		&c.Host, &c.Port, &c.Database, &c.User, &c.Schemas, &c.EncryptedPassword,
		&c.IntervalMinutes, &c.Enabled, &c.Revision, &c.NextRunAt, &c.Queued, &c.Running,
	}
}

func validManagedPostgresInput(in ManagedPostgresInput) bool {
	validField := func(value string, limit int) bool {
		return validCheckText(value, limit) && strings.IndexFunc(value, unicode.IsControl) == -1
	}
	if !validField(in.Host, 253) || in.Port < 1 || in.Port > 65535 ||
		!validField(in.Database, 63) || !validField(in.User, 63) ||
		len(in.Schemas) < 1 || len(in.Schemas) > 64 ||
		in.IntervalMinutes < 1 || in.IntervalMinutes > 1440 ||
		len(in.EncryptedPassword) > 4096 {
		return false
	}
	seen := make(map[string]struct{}, len(in.Schemas))
	for _, schema := range in.Schemas {
		if !validField(schema, 128) || strings.HasPrefix(schema, "pg_") || schema == "information_schema" {
			return false
		}
		if _, duplicate := seen[schema]; duplicate {
			return false
		}
		seen[schema] = struct{}{}
	}
	return true
}

func managedPostgresOwner(ctx context.Context, tx pgx.Tx, workspaceID, actorID int64, lock bool) error {
	role, err := checkRole(ctx, tx, workspaceID, actorID, lock)
	if err != nil {
		return err
	}
	if role != "owner" {
		return ErrForbidden
	}
	return nil
}

func managedPostgresJob(ctx context.Context, tx pgx.Tx, workspaceID, jobID int64) (AutomationJob, error) {
	job := AutomationJob{Managed: &ManagedPostgresConfig{}}
	err := tx.QueryRow(ctx, "SELECT "+automationJobColumns+", "+managedPostgresFields+`
		FROM plume.automation_jobs j
		JOIN plume.managed_postgres_jobs mp ON mp.job_id = j.id
		WHERE j.workspace_id = $1 AND j.id = $2 AND j.revoked_at IS NULL`, workspaceID, jobID).
		Scan(append(automationJobFields(&job), managedPostgresScan(job.Managed)...)...)
	return job, storeError(err)
}

func (s *Store) CreateManagedPostgresJob(ctx context.Context, workspaceID, actorID int64, source, tokenDigest string, input ManagedPostgresInput) (AutomationJob, error) {
	if !validManagedPostgresInput(input) || len(input.EncryptedPassword) == 0 ||
		!validAutomationAssetSource(source) || !digestPattern.MatchString(tokenDigest) {
		return AutomationJob{}, ErrInvalid
	}
	var job AutomationJob
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := managedPostgresOwner(ctx, tx, workspaceID, actorID, true); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO plume.automation_jobs (workspace_id, actor_id, provider, source, token_digest)
			VALUES ($1, $2, 'postgresql', $3, $4) RETURNING id`, workspaceID, actorID, source, tokenDigest).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO plume.managed_postgres_jobs
				(job_id, host, port, database_name, username, schemas, encrypted_password, interval_minutes, enabled, next_run_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, CASE WHEN $9 THEN clock_timestamp() END)`,
			id, input.Host, input.Port, input.Database, input.User, input.Schemas, input.EncryptedPassword,
			input.IntervalMinutes, input.Enabled); err != nil {
			return err
		}
		var err error
		job, err = managedPostgresJob(ctx, tx, workspaceID, id)
		return err
	})
	if err != nil {
		return AutomationJob{}, storeError(err)
	}
	return job, nil
}

func (s *Store) ManagedPostgresJob(ctx context.Context, workspaceID, actorID, jobID int64) (AutomationJob, error) {
	var job AutomationJob
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if err := managedPostgresOwner(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		var err error
		job, err = managedPostgresJob(ctx, tx, workspaceID, jobID)
		return err
	})
	if err != nil {
		return AutomationJob{}, storeError(err)
	}
	return job, nil
}

func lockManagedPostgresJob(ctx context.Context, tx pgx.Tx, workspaceID, jobID int64) error {
	return storeError(tx.QueryRow(ctx, `
		SELECT j.id FROM plume.automation_jobs j
		JOIN plume.managed_postgres_jobs mp ON mp.job_id = j.id
		WHERE j.workspace_id = $1 AND j.id = $2 AND j.revoked_at IS NULL
		FOR UPDATE OF j, mp`, workspaceID, jobID).Scan(&jobID))
}

func (s *Store) UpdateManagedPostgresJob(ctx context.Context, workspaceID, actorID, jobID, revision int64, input ManagedPostgresInput) (AutomationJob, error) {
	if revision < 1 || !validManagedPostgresInput(input) {
		return AutomationJob{}, ErrInvalid
	}
	var job AutomationJob
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := managedPostgresOwner(ctx, tx, workspaceID, actorID, true); err != nil {
			return err
		}
		if err := lockManagedPostgresJob(ctx, tx, workspaceID, jobID); err != nil {
			return err
		}
		current, err := managedPostgresJob(ctx, tx, workspaceID, jobID)
		if err != nil {
			return err
		}
		if current.Managed.Revision != revision {
			return ErrConflict
		}
		if len(input.EncryptedPassword) == 0 &&
			(input.Host != current.Managed.Host || input.Port != current.Managed.Port ||
				input.Database != current.Managed.Database || input.User != current.Managed.User) {
			return ErrInvalid
		}
		if len(input.EncryptedPassword) == 0 {
			input.EncryptedPassword = current.Managed.EncryptedPassword
		}
		if _, err := tx.Exec(ctx, `
			UPDATE plume.managed_postgres_jobs
			SET host = $2, port = $3, database_name = $4, username = $5, schemas = $6,
				encrypted_password = $7, interval_minutes = $8, enabled = $9, revision = revision + 1,
				next_run_at = CASE WHEN $9 THEN clock_timestamp() + make_interval(mins => $8) END
			WHERE job_id = $1`,
			jobID, input.Host, input.Port, input.Database, input.User, input.Schemas,
			input.EncryptedPassword, input.IntervalMinutes, input.Enabled); err != nil {
			return err
		}
		if current.actorID != actorID {
			if _, err := tx.Exec(ctx, "UPDATE plume.automation_jobs SET actor_id = $2 WHERE id = $1", jobID, actorID); err != nil {
				return err
			}
		}
		job, err = managedPostgresJob(ctx, tx, workspaceID, jobID)
		return err
	})
	if err != nil {
		return AutomationJob{}, storeError(err)
	}
	return job, nil
}

func (s *Store) QueueManagedPostgresJob(ctx context.Context, workspaceID, actorID, jobID int64) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := managedPostgresOwner(ctx, tx, workspaceID, actorID, true); err != nil {
			return err
		}
		if err := lockManagedPostgresJob(ctx, tx, workspaceID, jobID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE plume.managed_postgres_jobs SET requested_at = clock_timestamp() WHERE job_id = $1", jobID)
		return err
	})
}

func (s *Store) ClaimManagedPostgresJob(ctx context.Context, runID string, lease time.Duration) (*ManagedPostgresClaim, error) {
	if !automationRunID.MatchString(runID) || lease < 5*time.Second || lease > 5*time.Minute {
		return nil, ErrInvalid
	}
	claim := ManagedPostgresClaim{RunID: runID}
	err := s.pool.QueryRow(ctx, `
		WITH due AS (
			SELECT mp.job_id, j.source, statement_timestamp() AS claimed_at
			FROM plume.managed_postgres_jobs mp
			JOIN plume.automation_jobs j ON j.id = mp.job_id
			JOIN plume.memberships member ON member.workspace_id = j.workspace_id AND member.user_id = j.actor_id
			WHERE j.revoked_at IS NULL AND member.role IN ('owner', 'operator')
				AND (mp.lease_until IS NULL OR mp.lease_until <= statement_timestamp())
				AND LEAST(mp.requested_at, mp.next_run_at) <= statement_timestamp()
			ORDER BY LEAST(mp.requested_at, mp.next_run_at), mp.job_id
			FOR UPDATE OF mp SKIP LOCKED LIMIT 1
		)
		UPDATE plume.managed_postgres_jobs mp SET
			lease_id = $1, lease_until = due.claimed_at + $2 * interval '1 millisecond'
		FROM due WHERE mp.job_id = due.job_id
		RETURNING mp.job_id, due.source, due.claimed_at, `+managedPostgresFields, runID, lease.Milliseconds()).
		Scan(append([]any{&claim.JobID, &claim.Source, &claim.claimedAt}, managedPostgresScan(&claim.Config)...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &claim, nil
}

func (s *Store) FinishManagedPostgresJob(ctx context.Context, claim ManagedPostgresClaim, input AutomationDelivery) (AutomationRun, error) {
	if claim.JobID < 1 || claim.claimedAt.IsZero() || !automationRunID.MatchString(claim.RunID) ||
		!digestPattern.MatchString(input.Digest) || input.ObservedAt.IsZero() || input.ObservedAt.After(time.Now().Add(5*time.Minute)) ||
		input.Evidence != nil || (input.FailureCode == "" && (input.Assets == nil || len(input.Assets) > 1000)) ||
		(input.FailureCode != "" && (!validCollectionFailure(input.FailureCode) || input.Assets != nil)) {
		return AutomationRun{}, ErrInvalid
	}
	var run AutomationRun
	var staleRevision bool
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var job AutomationJob
		var revision int64
		var validLease bool
		err := tx.QueryRow(ctx, "SELECT "+automationJobColumns+`,
			mp.revision, COALESCE(mp.lease_id = $2 AND mp.lease_until > clock_timestamp(), false)
			FROM plume.automation_jobs j JOIN plume.managed_postgres_jobs mp ON mp.job_id = j.id
			WHERE j.id = $1 AND j.revoked_at IS NULL FOR UPDATE OF j, mp`, claim.JobID, claim.RunID).
			Scan(append(automationJobFields(&job), &revision, &validLease)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		role, err := checkRole(ctx, tx, job.workspaceID, job.actorID, true)
		if errors.Is(err, ErrNotFound) || (err == nil && !checkEditor(role)) {
			return ErrConflict
		}
		if err != nil {
			return err
		}

		previous, err := scanAutomationRun(tx.QueryRow(ctx, "SELECT "+automationRunColumns+
			" FROM plume.automation_runs WHERE job_id = $1 AND run_id = $2", job.ID, claim.RunID))
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
		if !validLease {
			return ErrConflict
		}
		if revision != claim.Config.Revision {
			staleRevision = true
			_, err := tx.Exec(ctx, `UPDATE plume.managed_postgres_jobs SET lease_id = NULL, lease_until = NULL WHERE job_id = $1`, job.ID)
			return err
		}

		run = AutomationRun{JobID: job.ID, RunID: claim.RunID, ObservedAt: input.ObservedAt, Status: "success"}
		if input.FailureCode != "" {
			run.Status, run.Code = "error", input.FailureCode
		} else if err := importAutomationAssets(ctx, tx, job, input.Digest, input.Assets, &run); err != nil {
			return err
		}
		if err := recordAutomationRun(ctx, tx, input.Digest, &run); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE plume.managed_postgres_jobs
			SET lease_id = NULL, lease_until = NULL,
				requested_at = CASE WHEN requested_at > $2 THEN requested_at END,
				next_run_at = CASE WHEN enabled THEN clock_timestamp() + make_interval(mins => interval_minutes) END
			WHERE job_id = $1`, job.ID, claim.claimedAt)
		return err
	})
	if err != nil {
		return AutomationRun{}, storeError(err)
	}
	if staleRevision {
		return AutomationRun{}, fmt.Errorf("managed PostgreSQL configuration changed: %w", ErrConflict)
	}
	return run, nil
}
