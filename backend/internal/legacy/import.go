package legacy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	pendingPassword = "django-activation-required"
	lockedPassword  = "django-login-forbidden"
)

type Counts struct {
	Users           int64 `json:"users"`
	Workspaces      int64 `json:"workspaces"`
	Memberships     int64 `json:"memberships"`
	Assets          int64 `json:"assets"`
	Activities      int64 `json:"activities"`
	Requirements    int64 `json:"requirements"`
	NewRequirements int64 `json:"newRequirements"`
	Checks          int64 `json:"checks"`
	Evidence        int64 `json:"evidence"`
	Events          int64 `json:"events"`
}

type Report struct {
	Mode        string     `json:"mode"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Counts      Counts     `json:"counts"`
}

const referencedUsers = `
	SELECT user_id FROM public.governance_membership
	UNION SELECT created_by_id FROM public.governance_check
	UNION SELECT submitted_by_id FROM public.governance_check WHERE submitted_by_id IS NOT NULL
	UNION SELECT created_by_id FROM public.governance_evidence
	UNION SELECT actor_id FROM public.governance_checkevent
`

// Import copies one snapshot of this database's Django schema into its Go schema.
func Import(ctx context.Context, pool *pgxpool.Pool, apply bool) (Report, error) {
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if apply {
		options = pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
	}
	var report Report
	err := pgx.BeginTxFunc(ctx, pool, options, func(tx pgx.Tx) error {
		if apply {
			var locked bool
			const importLock int64 = 0x534341524c455402
			if err := tx.QueryRow(ctx, "SELECT pg_catalog.pg_try_advisory_xact_lock($1)", importLock).Scan(&locked); err != nil {
				return sqlFailure("acquire the Django import lock", err)
			}
			if !locked {
				return errors.New("another Django import is running")
			}
		}

		var completed time.Time
		var storedCounts []byte
		err := tx.QueryRow(ctx, "SELECT completed_at, counts FROM plume.django_import WHERE singleton").
			Scan(&completed, &storedCounts)
		if err == nil {
			if err := json.Unmarshal(storedCounts, &report.Counts); err != nil {
				return errors.New("could not read the recorded Django import counts")
			}
			report.Mode, report.CompletedAt = "already-applied", &completed
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return sqlFailure("read the Go import ledger; apply Go schema migrations first", err)
		}

		if apply {
			// Hold a consistent source until commit without writing to Django.
			if _, err := tx.Exec(ctx, `
				LOCK TABLE public.auth_user, public.governance_workspace, public.governance_membership,
					public.governance_asset, public.governance_activity, public.governance_requirement,
					public.governance_check, public.governance_evidence, public.governance_checkevent
				IN SHARE MODE NOWAIT
			`); err != nil {
				return sqlFailure("lock the public Django tables; check the database and pause Django writes", err)
			}
			if _, err := tx.Exec(ctx, "LOCK TABLE plume.requirements IN SHARE ROW EXCLUSIVE MODE NOWAIT"); err != nil {
				return sqlFailure("lock the Go rule catalog", err)
			}
		}

		counts, err := preflight(ctx, tx)
		if err != nil {
			return err
		}
		report = Report{Mode: "check", Counts: counts}
		if !apply {
			return nil
		}
		if counts.Workspaces == 0 {
			return errors.New("Django has no workspaces to import")
		}
		if err := copyRecords(ctx, tx, counts); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, "INSERT INTO plume.django_import (counts) VALUES ($1) RETURNING completed_at", counts).
			Scan(&completed)
		if err != nil {
			return sqlFailure("record the completed Django import", err)
		}
		report.Mode, report.CompletedAt = "applied", &completed
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	return report, nil
}

func preflight(ctx context.Context, tx pgx.Tx) (Counts, error) {
	var counts Counts
	var usernames, slugs, requirements int64
	err := tx.QueryRow(ctx, "WITH source_users AS ("+referencedUsers+`) SELECT
		(SELECT count(*) FROM source_users),
		(SELECT count(*) FROM public.governance_workspace),
		(SELECT count(*) FROM public.governance_membership),
		(SELECT count(*) FROM public.governance_asset),
		(SELECT count(*) FROM public.governance_activity),
		(SELECT count(*) FROM public.governance_requirement),
		(SELECT count(*) FROM public.governance_requirement d WHERE NOT EXISTS
			(SELECT FROM plume.requirements p WHERE p.code = d.code)),
		(SELECT count(*) FROM public.governance_check),
		(SELECT count(*) FROM public.governance_evidence),
		(SELECT count(*) FROM public.governance_checkevent),
		(SELECT count(*) FROM source_users s JOIN public.auth_user d ON d.id = s.user_id
			JOIN plume.users p USING (username)),
		(SELECT count(*) FROM public.governance_workspace d JOIN plume.workspaces p USING (slug)),
		(SELECT count(*) FROM public.governance_requirement d JOIN plume.requirements p USING (code)
			WHERE (d.title, d.citation, d.source_url, d.summary)
			IS DISTINCT FROM (p.title, p.citation, p.source_url, p.summary))
	`).Scan(&counts.Users, &counts.Workspaces, &counts.Memberships, &counts.Assets, &counts.Activities,
		&counts.Requirements, &counts.NewRequirements, &counts.Checks, &counts.Evidence, &counts.Events,
		&usernames, &slugs, &requirements)
	if err != nil {
		return Counts{}, sqlFailure("read the public Django schema and Go catalog", err)
	}
	if usernames+slugs+requirements != 0 {
		return Counts{}, fmt.Errorf("Django import conflicts with %d Go usernames, %d workspace slugs and %d rule codes with different content", usernames, slugs, requirements)
	}
	return counts, nil
}

func copyRecords(ctx context.Context, tx pgx.Tx, counts Counts) error {
	users, err := tx.Exec(ctx, "WITH source_users AS ("+referencedUsers+`)
		INSERT INTO plume.users (username, password_hash, django_id)
		SELECT u.username, CASE WHEN u.is_active AND u.password <> '' AND left(u.password, 1) <> '!'
			THEN $1 ELSE $2 END, u.id
		FROM public.auth_user u JOIN source_users s ON s.user_id = u.id ORDER BY u.id
	`, pendingPassword, lockedPassword)
	if err != nil {
		return sqlFailure("copy Django users", err)
	}
	if users.RowsAffected() != counts.Users {
		return errors.New("Django user count changed while copying")
	}
	for _, step := range []struct {
		name  string
		count int64
		query string
	}{
		{"workspaces", counts.Workspaces, `
			INSERT INTO plume.workspaces (slug, name, django_id)
			SELECT slug, name, id FROM public.governance_workspace ORDER BY id`},
		{"memberships", counts.Memberships, `
			INSERT INTO plume.memberships (workspace_id, user_id, role)
			SELECT w.id, u.id, m.role FROM public.governance_membership m
			JOIN plume.workspaces w ON w.django_id = m.workspace_id
			JOIN plume.users u ON u.django_id = m.user_id`},
		{"assets", counts.Assets, `
			INSERT INTO plume.assets (workspace_id, name, system, owner_team, category,
				business_level, statutory_category, classification_basis, description, created_at, django_id)
			SELECT w.id, a.name, a.system, a.owner_team, a.category, a.business_level,
				a.statutory_category, a.classification_basis, a.description, a.created_at, a.id
			FROM public.governance_asset a JOIN plume.workspaces w ON w.django_id = a.workspace_id
			ORDER BY a.created_at, a.id`},
		{"activities", counts.Activities, `
			INSERT INTO plume.activities (workspace_id, asset_id, name, kind, purpose,
				recipient, region, retention_policy, created_at, django_id)
			SELECT a.workspace_id, a.id, d.name, d.kind, d.purpose, d.recipient, d.region,
				d.retention_policy, d.created_at, d.id
			FROM public.governance_activity d JOIN plume.assets a ON a.django_id = d.asset_id
			ORDER BY d.created_at, d.id`},
		{"requirements", counts.NewRequirements, `
			INSERT INTO plume.requirements (code, title, citation, source_url, summary)
			SELECT d.code, d.title, d.citation, d.source_url, d.summary
			FROM public.governance_requirement d
			WHERE NOT EXISTS (SELECT FROM plume.requirements p WHERE p.code = d.code)`},
		{"checks", counts.Checks, `
			INSERT INTO plume.checks (workspace_id, asset_id, requirement_code, owner_team, note,
				status, created_by, submitted_by, created_at, updated_at, django_id)
			SELECT a.workspace_id, a.id, r.code, c.owner_team, c.note, c.status,
				creator.id, submitter.id, c.created_at, c.updated_at, c.id
			FROM public.governance_check c JOIN plume.assets a ON a.django_id = c.asset_id
			JOIN public.governance_requirement r ON r.id = c.requirement_id
			JOIN plume.users creator ON creator.django_id = c.created_by_id
			LEFT JOIN plume.users submitter ON submitter.django_id = c.submitted_by_id
			ORDER BY c.updated_at, c.id`},
		{"evidence", counts.Evidence, `
			INSERT INTO plume.check_evidence (check_id, kind, source_system, source_record, summary,
				observed_at, created_by, created_at, django_id)
			SELECT c.id, e.kind, e.source_system, e.source_record, e.summary,
				e.observed_at, u.id, e.created_at, e.id
			FROM public.governance_evidence e JOIN plume.checks c ON c.django_id = e.control_check_id
			JOIN plume.users u ON u.django_id = e.created_by_id
			ORDER BY e.created_at, e.id`},
		{"events", counts.Events, `
			INSERT INTO plume.check_events (check_id, action, actor_id, note, created_at, django_id)
			SELECT c.id, e.action, u.id, e.note, e.created_at, e.id
			FROM public.governance_checkevent e JOIN plume.checks c ON c.django_id = e.control_check_id
			JOIN plume.users u ON u.django_id = e.actor_id
			ORDER BY e.created_at, e.id`},
	} {
		result, err := tx.Exec(ctx, step.query)
		if err != nil {
			return sqlFailure("copy Django "+step.name, err)
		}
		if result.RowsAffected() != step.count {
			return fmt.Errorf("Django %s count changed while copying", step.name)
		}
	}
	return nil
}

// ActivateUser grants a previously imported active member a newly provisioned password.
func ActivateUser(ctx context.Context, pool *pgxpool.Pool, username, passwordHash string) error {
	result, err := pool.Exec(ctx, `
		UPDATE plume.users u SET password_hash = $2
		WHERE u.username = $1 AND u.django_id IS NOT NULL AND u.password_hash = $3
		  AND EXISTS (SELECT FROM plume.memberships m WHERE m.user_id = u.id)
	`, username, passwordHash, pendingPassword)
	if err != nil {
		return sqlFailure("activate the imported user", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("no imported active member is awaiting activation with that username")
	}
	return nil
}

// SQL errors can contain row data or a connection string. Report the step and error class only.
func sqlFailure(step string, err error) error {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		return fmt.Errorf("%s failed (PostgreSQL %s)", step, postgres.Code)
	}
	return fmt.Errorf("%s failed", step)
}
