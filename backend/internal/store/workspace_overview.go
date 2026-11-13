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
   UNION ALL
   SELECT ds.id,ds.source,ds.provider,'discovery',
          CASE WHEN NOT ds.enabled THEN 'paused'
               WHEN ds.lease_until>statement_timestamp() THEN 'collecting'
               WHEN ds.requested_mode IS NOT NULL THEN 'queued'
               WHEN latest.status='error' THEN 'failed'
               WHEN latest.id IS NULL THEN 'never_run' ELSE 'enabled' END,
          latest.observed_at,latest.created_at,COALESCE(latest.created_at,ds.created_at)
   FROM plume.discovery_sources ds LEFT JOIN plume.discovery_runs latest ON latest.id=ds.latest_run_id
   WHERE ds.workspace_id=$1
   UNION ALL
   SELECT id,name,CASE WHEN kind='security_events' THEN 'security-events' ELSE kind END,'operations',
          CASE WHEN last_seen IS NULL THEN 'never_run' ELSE 'observing' END,
          last_seen,NULL::timestamptz,COALESCE(last_seen,created_at)
   FROM plume.operations_sources WHERE workspace_id=$1 AND revoked_at IS NULL
   UNION ALL
   SELECT s.id,s.name,'application_api','application_api',
          CASE WHEN NOT EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.actor_id AND m.role='owner') THEN 'revoked'
               WHEN b.id IS NULL THEN 'never_run' WHEN b.status<>'success' THEN 'failed'
               WHEN b.observed_at+make_interval(mins=>s.max_age_minutes)<statement_timestamp() THEN 'stale' ELSE 'current' END,
          b.observed_at,b.created_at,COALESCE(b.created_at,s.created_at)
   FROM plume.application_sources s
   LEFT JOIN plume.application_batches b ON b.source_id=s.id AND b.id=s.latest_batch_id AND b.workspace_id=s.workspace_id
   WHERE s.workspace_id=$1 AND s.revoked_at IS NULL

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
