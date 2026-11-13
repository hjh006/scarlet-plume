package store

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

var ErrRuntimeLimit = errors.New("runtime telemetry storage limit exceeded")

type RuntimeSource struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	Service           string     `json:"service"`
	Kind              string     `json:"kind"`
	CreatedAt         time.Time  `json:"createdAt"`
	RevokedAt         *time.Time `json:"revokedAt,omitempty"`
	LastSeen          *time.Time `json:"lastSeen,omitempty"`
	FindingsWarning   string     `json:"findingsWarning,omitempty"`
	FindingsWarningAt *time.Time `json:"findingsWarningAt,omitempty"`
}

type RuntimeReceipt struct {
	Accepted int  `json:"accepted"`
	Replayed bool `json:"replayed,omitempty"`
}

type RuntimeRoute struct {
	SourceID        int64     `json:"sourceId"`
	Source          string    `json:"source"`
	Service         string    `json:"service"`
	Kind            string    `json:"kind"`
	Method          string    `json:"method"`
	Route           string    `json:"route"`
	Posture         string    `json:"posture"`
	Requests        int64     `json:"requests"`
	Responses4xx    int64     `json:"responses4xx"`
	Responses5xx    int64     `json:"responses5xx"`
	LastSeen        time.Time `json:"lastSeen"`
	DatabaseSystems []string  `json:"databaseSystems"`
}

type runtimeRouteKey struct{ method, route string }

type runtimeAggregate struct {
	Method          string    `json:"method"`
	RouteKey        string    `json:"route_key"`
	Route           string    `json:"route"`
	Requests        int       `json:"requests"`
	Responses4xx    int       `json:"responses_4xx"`
	Responses5xx    int       `json:"responses_5xx"`
	LastSeen        time.Time `json:"last_seen"`
	DatabaseSystems []string  `json:"database_systems"`
}

const (
	maxRuntimeSources       = 50
	maxRuntimeRoutes        = 1000
	maxRuntimeReceipts      = 20000
	maxRuntimePostureRoutes = 1000
	runtimeSourceColumns    = "id, name, service, kind, created_at, revoked_at, last_seen, COALESCE(findings_warning, ''), findings_warning_at"
)

func runtimeSourceFields(source *RuntimeSource) []any {
	return []any{&source.ID, &source.Name, &source.Service, &source.Kind, &source.CreatedAt, &source.RevokedAt,
		&source.LastSeen, &source.FindingsWarning, &source.FindingsWarningAt}
}

func runtimeOwner(ctx context.Context, tx pgx.Tx, workspaceID, actorID int64) error {
	role, err := checkRole(ctx, tx, workspaceID, actorID, true)
	if err != nil {
		return err
	}
	if role != "owner" {
		return ErrForbidden
	}
	return nil
}

func (s *Store) CreateRuntimeSource(ctx context.Context, workspaceID, actorID int64, name, service, kind, tokenDigest string) (RuntimeSource, error) {
	if (kind != "gateway" && kind != "otlp") || !validAutomationAssetSource(name) ||
		!validAutomationAssetSource(service) || !digestPattern.MatchString(tokenDigest) {
		return RuntimeSource{}, ErrInvalid
	}
	var source RuntimeSource
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := runtimeOwner(ctx, tx, workspaceID, actorID); err != nil {
			return err
		}
		var count int
		// Serialize the workspace quota check across simultaneous source creation.
		if _, err := tx.Exec(ctx, `
			SELECT pg_advisory_xact_lock(hashtextextended('plume:runtime-source-quota', $1))
		`, workspaceID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM plume.runtime_security_sources WHERE workspace_id = $1", workspaceID).Scan(&count); err != nil {
			return err
		}
		if count >= maxRuntimeSources {
			return ErrRuntimeLimit
		}
		return tx.QueryRow(ctx, `
			INSERT INTO plume.runtime_security_sources (workspace_id, actor_id, kind, name, service, token_digest)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+runtimeSourceColumns,
			workspaceID, actorID, kind, name, service, tokenDigest,
		).Scan(runtimeSourceFields(&source)...)
	})
	return source, storeError(err)
}

func (s *Store) ListRuntimeSources(ctx context.Context, workspaceID int64) ([]RuntimeSource, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+runtimeSourceColumns+`
		FROM plume.runtime_security_sources WHERE workspace_id = $1 ORDER BY id DESC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RuntimeSource, error) {
		var source RuntimeSource
		err := row.Scan(runtimeSourceFields(&source)...)
		return source, err
	})
}

func (s *Store) RevokeRuntimeSource(ctx context.Context, workspaceID, actorID, sourceID int64) error {
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := runtimeOwner(ctx, tx, workspaceID, actorID); err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `
			UPDATE plume.runtime_security_sources
			SET revoked_by = CASE WHEN revoked_at IS NULL THEN $3 ELSE revoked_by END,
			    revoked_at = COALESCE(revoked_at, clock_timestamp()), receipt_count = 0
			WHERE workspace_id = $1 AND id = $2
		`, workspaceID, sourceID, actorID)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, "DELETE FROM plume.runtime_security_batches WHERE source_id = $1", sourceID)
		if err != nil {
			return err
		}
		return invalidateExtendedRuntimeReviews(ctx, tx, workspaceID, actorID, sourceID, "", "", "来源已停用，原自动复测失效，不能继续独立通过。")
	})
	return storeError(err)
}

func validRuntimeToken(kind, tokenDigest string) bool {
	return (kind == "gateway" || kind == "otlp") && digestPattern.MatchString(tokenDigest)
}

func (s *Store) AuthorizeRuntimeSource(ctx context.Context, kind, tokenDigest string) error {
	if !validRuntimeToken(kind, tokenDigest) {
		return ErrAutomationUnauthorized
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM plume.runtime_security_sources s
			JOIN plume.memberships m ON m.workspace_id = s.workspace_id AND m.user_id = s.actor_id
			WHERE s.kind = $1 AND s.token_digest = $2 AND s.revoked_at IS NULL AND m.role = 'owner'
		)
	`, kind, tokenDigest).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrAutomationUnauthorized
	}
	return nil
}

func (s *Store) DeliverRuntimeBatch(ctx context.Context, kind, tokenDigest, batchID string, observations []runtimesecurity.Observation) (RuntimeReceipt, error) {
	if !validRuntimeToken(kind, tokenDigest) {
		return RuntimeReceipt{}, ErrAutomationUnauthorized
	}
	if !digestPattern.MatchString(batchID) || len(observations) > runtimesecurity.MaxObservations ||
		(kind == "gateway" && len(observations) == 0) {
		return RuntimeReceipt{}, ErrInvalid
	}
	// Persist the same normalized projection used for replay identity. A local
	// loop variable in prepareRuntimeBatch must not leave the input slice raw.
	projected := make([]runtimesecurity.Observation, len(observations))
	for i, observation := range observations {
		if kind == "gateway" && (len(observation.DatabaseTargets) > 0 || len(observation.DatabaseSystems) > 0) {
			return RuntimeReceipt{}, ErrInvalid
		}
		normalized, err := runtimesecurity.NormalizeObservation(observation)
		if err != nil {
			return RuntimeReceipt{}, ErrInvalid
		}
		projected[i] = normalized
	}
	observations = projected
	aggregates, digest, lastSeen, err := prepareRuntimeBatch(kind, observations)
	if err != nil {
		return RuntimeReceipt{}, err
	}
	receipt := RuntimeReceipt{Accepted: len(observations)}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var sourceID, workspaceID, actorID int64
		var service, sourceName string
		var retained, auditCount int
		err := tx.QueryRow(ctx, `
			SELECT id, workspace_id, actor_id, service, name, receipt_count, audit_count FROM plume.runtime_security_sources
			WHERE token_digest = $1 AND kind = $2 AND revoked_at IS NULL FOR NO KEY UPDATE
		`, tokenDigest, kind).Scan(&sourceID, &workspaceID, &actorID, &service, &sourceName, &retained, &auditCount)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAutomationUnauthorized
		}
		if err != nil {
			return err
		}
		role, err := checkRole(ctx, tx, workspaceID, actorID, true)
		if errors.Is(err, ErrNotFound) || err == nil && role != "owner" {
			return ErrAutomationUnauthorized
		}
		if err != nil {
			return err
		}
		// An OTLP export can contain no eligible HTTP server spans. It has
		// no telemetry to deduplicate after authorization is rechecked.
		if len(observations) == 0 {
			return nil
		}

		var previousDigest string
		err = tx.QueryRow(ctx, `
			SELECT payload_digest, accepted FROM plume.runtime_security_batches WHERE source_id = $1 AND batch_id = $2
		`, sourceID, batchID).Scan(&previousDigest, &receipt.Accepted)
		if err == nil {
			if previousDigest != digest {
				return ErrConflict
			}
			receipt.Replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		deleted, err := tx.Exec(ctx, `
			DELETE FROM plume.runtime_security_batches WHERE source_id = $1 AND expires_at <= statement_timestamp()
		`, sourceID)
		if err != nil {
			return err
		}
		retained -= int(deleted.RowsAffected())
		if retained >= maxRuntimeReceipts {
			return ErrRuntimeLimit
		}
		if err := mergeRuntimeRoutes(ctx, tx, sourceID, aggregates); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO plume.runtime_security_batches (source_id, batch_id, payload_digest, accepted, expires_at)
			VALUES ($1, $2, $3, $4, GREATEST($5::timestamptz, clock_timestamp()) + interval '168 hours')
		`, sourceID, batchID, digest, receipt.Accepted, lastSeen); err != nil {
			return err
		}
		auditCount, err = persistRuntimeMonitor(ctx, tx, sourceID, auditCount, observations)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE plume.runtime_security_sources
			SET last_seen = GREATEST(last_seen, $2), receipt_count = $3, audit_count = $4
			WHERE id = $1
		`, sourceID, lastSeen, retained+1, auditCount)
		if err != nil {
			return err
		}
		warning := recordRuntimeFindings(ctx, tx, runtimeFindingBatch{
			workspaceID: workspaceID, actorID: actorID, sourceID: sourceID, sourceName: sourceName,
			service: service, kind: kind, batchID: batchID,
		}, observations)
		if warning != "" {
			_, err := tx.Exec(ctx, `
				UPDATE plume.runtime_security_sources
				SET findings_warning = $2, findings_warning_at = clock_timestamp() WHERE id = $1
			`, sourceID, warning)
			if err != nil {
				return err
			}
		}
		if err := persistDataFlowObservations(ctx, tx, workspaceID, sourceID, actorID, service, kind, batchID, observations); err != nil {
			return err
		}
		return captureDataFlowSnapshot(ctx, tx, workspaceID, actorID, "runtime")
	})
	if err != nil {
		return RuntimeReceipt{}, storeError(err)
	}
	return receipt, nil
}

func prepareRuntimeBatch(kind string, observations []runtimesecurity.Observation) ([]runtimeAggregate, string, time.Time, error) {
	var lastSeen time.Time
	now := time.Now()
	buckets := make(map[runtimeRouteKey]runtimeAggregate)
	normalized := make([]string, 0, len(observations))
	for _, observation := range observations {
		observation, err := runtimesecurity.NormalizeObservation(observation)
		if err != nil || kind == "gateway" && (len(observation.DatabaseSystems) != 0 || len(observation.DatabaseTargets) != 0) ||
			observation.ObservedAt.Before(now.Add(-7*24*time.Hour)) || observation.ObservedAt.After(now.Add(5*time.Minute)) {
			return nil, "", time.Time{}, ErrInvalid
		}
		key := runtimeRouteKey{observation.Method, runtimesecurity.RouteKey(observation.Route)}
		canonical := observation
		canonical.Route = key.route
		encoded, err := json.Marshal(canonical)
		if err != nil {
			return nil, "", time.Time{}, err
		}
		normalized = append(normalized, string(encoded))
		if observation.ObservedAt.After(lastSeen) {
			lastSeen = observation.ObservedAt
		}

		route := buckets[key]
		route.Method, route.RouteKey = key.method, key.route
		if route.Route == "" || observation.Route < route.Route {
			route.Route = observation.Route
		}
		route.Requests++
		if observation.Status >= 500 {
			route.Responses5xx++
		} else if observation.Status >= 400 {
			route.Responses4xx++
		}
		if observation.ObservedAt.After(route.LastSeen) {
			route.LastSeen = observation.ObservedAt
		}
		route.DatabaseSystems = append(route.DatabaseSystems, observation.DatabaseSystems...)
		buckets[key] = route
	}
	aggregates := make([]runtimeAggregate, 0, len(buckets))
	for _, aggregate := range buckets {
		slices.Sort(aggregate.DatabaseSystems)
		aggregate.DatabaseSystems = slices.Compact(aggregate.DatabaseSystems)
		if aggregate.DatabaseSystems == nil {
			aggregate.DatabaseSystems = []string{}
		}
		aggregates = append(aggregates, aggregate)
	}
	slices.SortFunc(aggregates, func(a, b runtimeAggregate) int {
		if method := cmp.Compare(a.Method, b.Method); method != 0 {
			return method
		}
		return cmp.Compare(a.RouteKey, b.RouteKey)
	})
	// Entries are a multiset: order and database-list duplicates don't affect replay.
	slices.Sort(normalized)
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\n")))
	return aggregates, fmt.Sprintf("%x", sum), lastSeen, nil
}

func mergeRuntimeRoutes(ctx context.Context, tx pgx.Tx, sourceID int64, aggregates []runtimeAggregate) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO plume.runtime_security_routes AS previous
		    (source_id, method, route_key, route, requests, responses_4xx, responses_5xx, last_seen, database_systems)
		SELECT $1, method, route_key, route, requests, responses_4xx, responses_5xx, last_seen, database_systems
		FROM jsonb_to_recordset($2) AS items(
			method text, route_key text, route text, requests bigint, responses_4xx bigint,
			responses_5xx bigint, last_seen timestamptz, database_systems text[])
		ON CONFLICT (source_id, method, route_key) DO UPDATE SET
			requests = previous.requests + excluded.requests,
			responses_4xx = previous.responses_4xx + excluded.responses_4xx,
			responses_5xx = previous.responses_5xx + excluded.responses_5xx,
			last_seen = GREATEST(previous.last_seen, excluded.last_seen),
			database_systems = ARRAY(SELECT DISTINCT system
				FROM unnest(previous.database_systems || excluded.database_systems) AS system ORDER BY system)
	`, sourceID, aggregates); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM plume.runtime_security_routes WHERE source_id = $1", sourceID).Scan(&count); err != nil {
		return err
	}
	if count > maxRuntimeRoutes {
		return ErrRuntimeLimit
	}
	return nil
}

func (s *Store) RuntimePosture(ctx context.Context, workspaceID int64) ([]RuntimeRoute, error) {
	var routes []RuntimeRoute
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		// Revocation stops ingest. Previously observed routes remain available.
		rows, err := tx.Query(ctx, `
			SELECT r.source_id, s.name, s.service, s.kind, r.method, r.route, r.requests, r.responses_4xx,
			       r.responses_5xx, r.last_seen, r.database_systems
			FROM plume.runtime_security_routes r
			JOIN plume.runtime_security_sources s ON s.id = r.source_id
			WHERE s.workspace_id = $1
			ORDER BY r.last_seen DESC, r.source_id, r.method, r.route_key LIMIT $2
		`, workspaceID, maxRuntimePostureRoutes)
		if err != nil {
			return err
		}
		routes, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RuntimeRoute, error) {
			var route RuntimeRoute
			err := row.Scan(&route.SourceID, &route.Source, &route.Service, &route.Kind, &route.Method,
				&route.Route, &route.Requests, &route.Responses4xx, &route.Responses5xx, &route.LastSeen, &route.DatabaseSystems)
			return route, err
		})
		if err != nil || len(routes) == 0 {
			return err
		}
		return reconcileRuntimePosture(ctx, tx, workspaceID, routes)
	})
	return routes, storeError(err)
}

func reconcileRuntimePosture(ctx context.Context, tx pgx.Tx, workspaceID int64, routes []RuntimeRoute) error {
	baselines := make(map[string]map[runtimeRouteKey]bool)
	for _, route := range routes {
		baselines[route.Service] = nil
	}
	services := make([]string, 0, len(baselines))
	for service := range baselines {
		services = append(services, service)
	}
	rows, err := tx.Query(ctx, `
		SELECT aliases.service, op.method, op.path, COALESCE(op.deprecated, false)
		FROM unnest($2::text[]) AS aliases(service)
		CROSS JOIN LATERAL (
			SELECT operations FROM plume.api_security_scans
			WHERE workspace_id = $1 AND source = aliases.service
			ORDER BY id DESC LIMIT 1
		) latest
		CROSS JOIN LATERAL jsonb_to_recordset(latest.operations)
			AS op(method text, path text, deprecated boolean)
	`, workspaceID, services)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var service, method, path string
		var deprecated bool
		if err := rows.Scan(&service, &method, &path, &deprecated); err != nil {
			return err
		}
		if baselines[service] == nil {
			baselines[service] = make(map[runtimeRouteKey]bool)
		}
		key := runtimeRouteKey{method, runtimesecurity.RouteKey(path)}
		previous, exists := baselines[service][key]
		baselines[service][key] = deprecated && (!exists || previous)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range routes {
		route := &routes[i]
		baseline := baselines[route.Service]
		deprecated, documented := baseline[runtimeRouteKey{route.Method, runtimesecurity.RouteKey(route.Route)}]
		switch {
		case baseline == nil:
			route.Posture = "no_baseline"
		case !documented:
			route.Posture = "observed_only"
		case deprecated:
			route.Posture = "deprecated_active"
		default:
			route.Posture = "documented"
		}
	}
	return nil
}
