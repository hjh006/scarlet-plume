package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type OverviewSource struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Provider       string     `json:"provider"`
	Kind           string     `json:"kind"`
	State          string     `json:"state"`
	LastObservedAt *time.Time `json:"lastObservedAt,omitempty"`
	LastRunAt      *time.Time `json:"lastRunAt,omitempty"`
}

// OverviewSources is a bounded preview of configured collection channels. The
// source count deliberately does not claim a count of unique physical systems.
func (s *Store) OverviewSources(ctx context.Context, workspaceID int64) ([]OverviewSource, error) {
	rows, err := s.pool.Query(ctx, `
		WITH sources AS (
			SELECT j.id, COALESCE(j.source, '检查 #' || j.check_id::text) AS name,
			       j.provider, 'automation' AS kind,
			       CASE WHEN mp.lease_until > statement_timestamp() THEN 'collecting'
			            WHEN mp.requested_at IS NOT NULL THEN 'queued'
			            WHEN mp.enabled IS FALSE THEN 'paused'
			            WHEN latest.status = 'error' THEN 'failed'
			            WHEN latest.id IS NULL THEN 'never_run' ELSE 'enabled' END AS state,
			       latest.observed_at AS last_observed_at, latest.created_at AS last_run_at,
			       COALESCE(latest.created_at, j.created_at) AS updated_at
			FROM plume.automation_jobs j
			LEFT JOIN plume.managed_postgres_jobs mp ON mp.job_id = j.id
			LEFT JOIN LATERAL (SELECT id, status, observed_at, created_at FROM plume.automation_runs
			                   WHERE job_id = j.id ORDER BY id DESC LIMIT 1) latest ON true
			WHERE j.workspace_id = $1 AND j.revoked_at IS NULL
			UNION ALL
			SELECT id, name, kind, 'runtime',
			       CASE WHEN last_seen IS NULL THEN 'never_run' ELSE 'observing' END,
			       last_seen, NULL::timestamptz, COALESCE(last_seen, created_at)
			FROM plume.runtime_security_sources WHERE workspace_id = $1 AND revoked_at IS NULL
		)
		SELECT id, name, provider, kind, state, last_observed_at, last_run_at
		FROM sources ORDER BY updated_at DESC, kind, id DESC LIMIT 6
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (OverviewSource, error) {
		var source OverviewSource
		err := row.Scan(&source.ID, &source.Name, &source.Provider, &source.Kind, &source.State,
			&source.LastObservedAt, &source.LastRunAt)
		return source, err
	})
}
