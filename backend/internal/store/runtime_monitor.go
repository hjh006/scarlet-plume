package store

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

const (
	RuntimeAuditLimit            = 500
	RuntimeAuditRetentionDays    = 7
	RuntimeAuditPerSourceLimit   = 20000
	runtimeMonitorTopRoutesLimit = 10
)

type RuntimeMonitorCounts struct {
	Requests     int64 `json:"requests"`
	Responses4xx int64 `json:"responses4xx"`
	Responses5xx int64 `json:"responses5xx"`
}

type RuntimeMonitorPoint struct {
	At time.Time `json:"at"`
	RuntimeMonitorCounts
}

type RuntimeMonitorRoute struct {
	Method string `json:"method"`
	Route  string `json:"route"`
	RuntimeMonitorCounts
}

type RuntimeMonitor struct {
	Hours            int                   `json:"hours"`
	StartAt          time.Time             `json:"startAt"`
	EndAt            time.Time             `json:"endAt"`
	Totals           RuntimeMonitorCounts  `json:"totals"`
	Points           []RuntimeMonitorPoint `json:"points"`
	TopRoutes        []RuntimeMonitorRoute `json:"topRoutes"`
	TopRoutesPartial bool                  `json:"topRoutesPartial,omitempty"`
}

type RuntimeAuditFilter struct {
	Hours    int
	SourceID int64
	Method   string
	Status   int
	Route    string
}

type RuntimeAuditEntry struct {
	ID         int64     `json:"id"`
	ObservedAt time.Time `json:"observedAt"`
	SourceID   int64     `json:"sourceId"`
	Source     string    `json:"source"`
	Service    string    `json:"service"`
	Kind       string    `json:"kind"`
	Method     string    `json:"method"`
	Route      string    `json:"route"`
	Status     int       `json:"status"`
}

type RuntimeAudit struct {
	Items                   []RuntimeAuditEntry `json:"items"`
	Limit                   int                 `json:"limit"`
	RetentionDays           int                 `json:"retentionDays"`
	RetentionLimitPerSource int                 `json:"retentionLimitPerSource"`
	Truncated               bool                `json:"truncated,omitempty"`
}

type runtimeAuditWrite struct {
	ObservedAt time.Time `json:"observed_at"`
	Method     string    `json:"method"`
	Route      string    `json:"route"`
	RouteKey   string    `json:"route_key"`
	Status     int       `json:"status"`
}

func persistRuntimeMonitor(ctx context.Context, tx pgx.Tx, sourceID int64, retained int, observations []runtimesecurity.Observation) (int, error) {
	// The delivery transaction has already normalized and checked every event.
	// Project only fields used by monitoring and audit.
	events := make([]runtimeAuditWrite, len(observations))
	for i, event := range observations {
		events[i] = runtimeAuditWrite{
			ObservedAt: event.ObservedAt, Method: strings.ToUpper(event.Method), Route: event.Route,
			RouteKey: runtimesecurity.RouteKey(event.Route), Status: event.Status,
		}
	}
	if _, err := tx.Exec(ctx, `
		WITH incoming AS (
			SELECT * FROM jsonb_to_recordset($2)
			AS event(observed_at timestamptz, method text, route text, route_key text, status integer)
		), audit AS (
			INSERT INTO plume.runtime_request_audit (source_id, observed_at, method, route, route_key, status)
			SELECT $1, observed_at, method, route, route_key, status FROM incoming
		)
		INSERT INTO plume.runtime_monitor_minutes AS current (source_id, minute_at, requests, responses_4xx, responses_5xx)
		SELECT $1, date_trunc('minute', observed_at, 'UTC'), count(*),
		       count(*) FILTER (WHERE status BETWEEN 400 AND 499),
		       count(*) FILTER (WHERE status BETWEEN 500 AND 599)
		FROM incoming GROUP BY 2
		ON CONFLICT (source_id, minute_at) DO UPDATE SET
			requests = current.requests + excluded.requests,
			responses_4xx = current.responses_4xx + excluded.responses_4xx,
			responses_5xx = current.responses_5xx + excluded.responses_5xx
	`, sourceID, events); err != nil {
		return 0, err
	}
	expired, err := tx.Exec(ctx, `
		DELETE FROM plume.runtime_request_audit
		WHERE source_id = $1 AND observed_at < statement_timestamp() - make_interval(hours => $2)
	`, sourceID, RuntimeAuditRetentionDays*24)
	if err != nil {
		return 0, err
	}
	retained += len(events) - int(expired.RowsAffected())
	if overflow := retained - RuntimeAuditPerSourceLimit; overflow > 0 {
		deleted, err := tx.Exec(ctx, `
			DELETE FROM plume.runtime_request_audit WHERE id IN (
				SELECT id FROM plume.runtime_request_audit
				WHERE source_id = $1 ORDER BY observed_at, id LIMIT $2
			)
		`, sourceID, overflow)
		if err != nil {
			return 0, err
		}
		retained -= int(deleted.RowsAffected())
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM plume.runtime_monitor_minutes
		WHERE source_id = $1 AND minute_at < statement_timestamp() - interval '25 hours'
	`, sourceID)
	return retained, err
}

func (s *Store) RuntimeMonitor(ctx context.Context, workspaceID int64, hours int, sourceID int64) (RuntimeMonitor, error) {
	var step time.Duration
	switch hours {
	case 1:
		step = 5 * time.Minute
	case 6:
		step = 15 * time.Minute
	case 24:
		step = time.Hour
	default:
		return RuntimeMonitor{}, ErrInvalid
	}
	if sourceID < 0 {
		return RuntimeMonitor{}, ErrInvalid
	}
	// Include the current minute and keep the selected window exactly hours long.
	end := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
	start := end.Add(-time.Duration(hours) * time.Hour)
	result := RuntimeMonitor{
		Hours: hours, StartAt: start, EndAt: end,
		Points:    make([]RuntimeMonitorPoint, end.Sub(start)/step),
		TopRoutes: make([]RuntimeMonitorRoute, 0),
	}
	for i := range result.Points {
		result.Points[i].At = start.Add(time.Duration(i) * step)
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := readRuntimeMonitorPoints(ctx, tx, workspaceID, sourceID, step, &result); err != nil {
			return err
		}
		return readRuntimeMonitorTopRoutes(ctx, tx, workspaceID, sourceID, &result)
	})
	if err != nil {
		return RuntimeMonitor{}, storeError(err)
	}
	return result, nil
}

func readRuntimeMonitorPoints(ctx context.Context, tx pgx.Tx, workspaceID, sourceID int64, step time.Duration, monitor *RuntimeMonitor) error {
	rows, err := tx.Query(ctx, `
		SELECT m.minute_at, sum(m.requests)::bigint, sum(m.responses_4xx)::bigint, sum(m.responses_5xx)::bigint
		FROM plume.runtime_monitor_minutes m JOIN plume.runtime_security_sources s ON s.id = m.source_id
		WHERE s.workspace_id = $1 AND ($2 = 0::bigint OR s.id = $2) AND m.minute_at >= $3 AND m.minute_at < $4
		GROUP BY m.minute_at
	`, workspaceID, sourceID, monitor.StartAt, monitor.EndAt)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var at time.Time
		var count RuntimeMonitorCounts
		if err := rows.Scan(&at, &count.Requests, &count.Responses4xx, &count.Responses5xx); err != nil {
			return err
		}
		bucket := &monitor.Points[at.Sub(monitor.StartAt)/step].RuntimeMonitorCounts
		bucket.Requests += count.Requests
		bucket.Responses4xx += count.Responses4xx
		bucket.Responses5xx += count.Responses5xx
		monitor.Totals.Requests += count.Requests
		monitor.Totals.Responses4xx += count.Responses4xx
		monitor.Totals.Responses5xx += count.Responses5xx
	}
	return rows.Err()
}

func readRuntimeMonitorTopRoutes(ctx context.Context, tx pgx.Tx, workspaceID, sourceID int64, monitor *RuntimeMonitor) error {
	rows, err := tx.Query(ctx, `
		SELECT a.method, min(a.route), count(*),
		       count(*) FILTER (WHERE a.status BETWEEN 400 AND 499),
		       count(*) FILTER (WHERE a.status BETWEEN 500 AND 599),
		       (sum(count(*)) OVER ())::bigint
		FROM plume.runtime_request_audit a JOIN plume.runtime_security_sources s ON s.id = a.source_id
		WHERE s.workspace_id = $1 AND ($2 = 0::bigint OR s.id = $2) AND a.observed_at >= $3 AND a.observed_at < $4
		GROUP BY a.method, a.route_key
		ORDER BY count(*) DESC, a.method, a.route_key LIMIT $5
	`, workspaceID, sourceID, monitor.StartAt, monitor.EndAt, runtimeMonitorTopRoutesLimit)
	if err != nil {
		return err
	}
	defer rows.Close()
	var audited int64
	for rows.Next() {
		var route RuntimeMonitorRoute
		if err := rows.Scan(&route.Method, &route.Route, &route.Requests, &route.Responses4xx, &route.Responses5xx, &audited); err != nil {
			return err
		}
		monitor.TopRoutes = append(monitor.TopRoutes, route)
	}
	monitor.TopRoutesPartial = audited < monitor.Totals.Requests
	return rows.Err()
}

func (s *Store) RuntimeAudit(ctx context.Context, workspaceID int64, filter RuntimeAuditFilter) (RuntimeAudit, error) {
	if !validRuntimeAuditFilter(filter) {
		return RuntimeAudit{}, ErrInvalid
	}
	end := time.Now().UTC()
	start := end.Add(-time.Duration(filter.Hours) * time.Hour)
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.observed_at, s.id, s.name, s.service, s.kind, a.method, a.route, a.status
		FROM plume.runtime_request_audit a JOIN plume.runtime_security_sources s ON s.id = a.source_id
		WHERE s.workspace_id = $1 AND ($2 = 0::bigint OR s.id = $2)
		  AND a.observed_at >= $3 AND a.observed_at <= $4
		  AND ($5 = '' OR a.method = $5) AND ($6 = 0 OR a.status = $6)
		  AND ($7 = '' OR strpos(lower(a.route), lower($7)) > 0 OR strpos(lower(a.route_key), lower($8)) > 0)
		ORDER BY a.observed_at DESC, a.id DESC LIMIT $9
	`, workspaceID, filter.SourceID, start, end, filter.Method, filter.Status, filter.Route,
		runtimesecurity.RouteKey(filter.Route), RuntimeAuditLimit+1)
	if err != nil {
		return RuntimeAudit{}, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RuntimeAuditEntry, error) {
		var item RuntimeAuditEntry
		err := row.Scan(&item.ID, &item.ObservedAt, &item.SourceID, &item.Source, &item.Service,
			&item.Kind, &item.Method, &item.Route, &item.Status)
		return item, err
	})
	if err != nil {
		return RuntimeAudit{}, err
	}
	result := RuntimeAudit{
		Items: items, Limit: RuntimeAuditLimit, RetentionDays: RuntimeAuditRetentionDays,
		RetentionLimitPerSource: RuntimeAuditPerSourceLimit, Truncated: len(items) > RuntimeAuditLimit,
	}
	if result.Truncated {
		result.Items = result.Items[:RuntimeAuditLimit]
	}
	return result, nil
}

func validRuntimeAuditFilter(filter RuntimeAuditFilter) bool {
	switch filter.Hours {
	case 1, 6, 24, RuntimeAuditRetentionDays * 24:
	default:
		return false
	}
	switch filter.Method {
	case "", "GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE":
	default:
		return false
	}
	return filter.SourceID >= 0 && (filter.Status == 0 || filter.Status >= 100 && filter.Status <= 599) &&
		(filter.Route == "" || validCheckText(filter.Route, 200) && strings.IndexFunc(filter.Route, unicode.IsControl) < 0)
}
