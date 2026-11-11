package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hjh006/scarlet-plume/backend/internal/apisecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

const (
	APILifecycleLimit        = 2000
	apiLifecycleServiceLimit = 50
)

var ErrAPILifecycleScope = errors.New("API lifecycle scope exceeds supported limit")

type APILifecycleFilter struct {
	Service  string
	SourceID int64
}

type APILifecycleOperation struct {
	Paths            []string                     `json:"paths"`
	Auth             []string                     `json:"auth"`
	Deprecated       bool                         `json:"deprecated"`
	DeprecationMixed bool                         `json:"deprecationMixed,omitempty"`
	SensitiveFields  []apisecurity.SensitiveField `json:"sensitiveFields"`
	AssetIDs         []int64                      `json:"assetIds"`
	CheckIDs         []int64                      `json:"checkIds"`
}

type APILifecycleSource struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Kind         string     `json:"kind"`
	LastSeen     time.Time  `json:"lastSeen"`
	Requests     int64      `json:"requests"`
	Responses4xx int64      `json:"responses4xx"`
	Responses5xx int64      `json:"responses5xx"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
}

type APILifecycleItem struct {
	APILifecycleOperation
	Service        string               `json:"service"`
	Method         string               `json:"method"`
	Route          string               `json:"route"`
	RouteKey       string               `json:"routeKey"`
	State          string               `json:"state"`
	Registered     bool                 `json:"registered"`
	Observed       bool                 `json:"observed"`
	ScanID         int64                `json:"scanId,omitempty"`
	PreviousScanID int64                `json:"previousScanId,omitempty"`
	LastSeen       *time.Time           `json:"lastSeen,omitempty"`
	Sources        []APILifecycleSource `json:"sources"`
}

type APILifecycleChange struct {
	Method   string                 `json:"method"`
	Route    string                 `json:"route"`
	RouteKey string                 `json:"routeKey"`
	Kinds    []string               `json:"kinds"`
	Before   *APILifecycleOperation `json:"before,omitempty"`
	After    *APILifecycleOperation `json:"after,omitempty"`
}

type APILifecycleComparison struct {
	Service           string               `json:"service"`
	ScanID            int64                `json:"scanId"`
	CreatedAt         time.Time            `json:"createdAt"`
	PreviousScanID    int64                `json:"previousScanId,omitempty"`
	PreviousCreatedAt *time.Time           `json:"previousCreatedAt,omitempty"`
	Initial           bool                 `json:"initial"`
	Changes           []APILifecycleChange `json:"changes"`
}

type APILifecycle struct {
	GeneratedAt time.Time                `json:"generatedAt"`
	Items       []APILifecycleItem       `json:"items"`
	Comparisons []APILifecycleComparison `json:"comparisons"`
	Services    []string                 `json:"services"`
	Sources     []RuntimeSource          `json:"sources"`
	Limit       int                      `json:"limit"`
}

type apiLifecycleKey struct{ service, method, route string }

// APILifecycle uses immutable contracts and cumulative route observations, never
// capped request audit rows. Counts stay separated by collection source; lack of
// an observation is not proof that an endpoint is unused, retired or secure.
func (s *Store) APILifecycle(ctx context.Context, workspaceID, actorID int64, filter APILifecycleFilter) (APILifecycle, error) {
	if filter.SourceID < 0 || filter.Service != "" && !validAutomationAssetSource(filter.Service) {
		return APILifecycle{}, ErrInvalid
	}
	result := APILifecycle{Items: []APILifecycleItem{}, Comparisons: []APILifecycleComparison{}, Services: []string{}, Sources: []RuntimeSource{}, Limit: APILifecycleLimit}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, workspaceID, actorID, false); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT transaction_timestamp()").Scan(&result.GeneratedAt); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+runtimeSourceColumns+" FROM plume.runtime_security_sources WHERE workspace_id=$1 ORDER BY id", workspaceID)
		if err != nil {
			return err
		}
		result.Sources, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RuntimeSource, error) {
			var source RuntimeSource
			err := row.Scan(runtimeSourceFields(&source)...)
			return source, err
		})
		if err != nil {
			return err
		}
		if filter.SourceID > 0 {
			i := slices.IndexFunc(result.Sources, func(source RuntimeSource) bool { return source.ID == filter.SourceID })
			if i < 0 {
				return ErrNotFound
			}
			if filter.Service != "" && filter.Service != result.Sources[i].Service {
				return ErrInvalid
			}
			filter.Service = result.Sources[i].Service
		}
		rows, err = tx.Query(ctx, `SELECT service FROM (
			SELECT source AS service FROM plume.api_security_scans WHERE workspace_id=$1
			UNION SELECT service FROM plume.runtime_security_sources WHERE workspace_id=$1
		) services WHERE ($2='' OR service=$2) ORDER BY service LIMIT $3`, workspaceID, filter.Service, apiLifecycleServiceLimit+1)
		if err != nil {
			return err
		}
		result.Services, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(result.Services) > apiLifecycleServiceLimit {
			return ErrAPILifecycleScope
		}
		items := map[apiLifecycleKey]*APILifecycleItem{}
		for _, service := range result.Services {
			scans, err := readAPILifecycleScans(ctx, tx, workspaceID, service)
			if err != nil {
				return err
			}
			if len(scans) == 0 {
				continue
			}
			current := groupAPILifecycleOperations(scans[0].Operations)
			previous := map[runtimeRouteKey]APILifecycleOperation{}
			comparison := APILifecycleComparison{Service: service, ScanID: scans[0].ID, CreatedAt: scans[0].CreatedAt, Initial: len(scans) == 1, Changes: []APILifecycleChange{}}
			if len(scans) == 2 {
				previous = groupAPILifecycleOperations(scans[1].Operations)
				comparison.PreviousScanID = scans[1].ID
				comparison.PreviousCreatedAt = &scans[1].CreatedAt
				comparison.Changes = diffAPILifecycleOperations(previous, current)
			}
			result.Comparisons = append(result.Comparisons, comparison)
			for key, operation := range current {
				items[apiLifecycleKey{service, key.method, key.route}] = &APILifecycleItem{APILifecycleOperation: operation, Service: service, Method: key.method, Route: operation.Paths[0], RouteKey: key.route, State: "unobserved", Registered: true, ScanID: scans[0].ID, PreviousScanID: comparison.PreviousScanID, Sources: []APILifecycleSource{}}
			}
			// Removed operations enter the directory only if runtime has observed
			// them; otherwise they remain visible in the version comparison.
			for key, operation := range previous {
				if _, exists := current[key]; exists {
					continue
				}
				items[apiLifecycleKey{service, key.method, key.route}] = &APILifecycleItem{APILifecycleOperation: operation, Service: service, Method: key.method, Route: operation.Paths[0], RouteKey: key.route, State: "retired_candidate", PreviousScanID: comparison.PreviousScanID, ScanID: scans[0].ID, Sources: []APILifecycleSource{}}
			}
		}
		if err := readAPILifecycleRoutes(ctx, tx, workspaceID, filter, items); err != nil {
			return err
		}
		for _, comparison := range result.Comparisons {
			for key, item := range items {
				if key.service == comparison.Service && item.ScanID == 0 {
					item.ScanID, item.PreviousScanID = comparison.ScanID, comparison.PreviousScanID
				}
			}
		}
		for key, item := range items {
			if !item.Registered && !item.Observed {
				delete(items, key)
				continue
			}
			result.Items = append(result.Items, *item)
		}
		if len(result.Items) > APILifecycleLimit {
			return ErrAPILifecycleScope
		}
		if err := linkAPILifecycleRuntimeAssets(ctx, tx, workspaceID, filter.Service, result.Items); err != nil {
			return err
		}
		slices.SortFunc(result.Items, func(a, b APILifecycleItem) int {
			return strings.Compare(a.Service+"\x00"+a.Method+"\x00"+a.RouteKey, b.Service+"\x00"+b.Method+"\x00"+b.RouteKey)
		})
		return nil
	})
	if err != nil {
		return APILifecycle{}, storeError(err)
	}
	return result, nil
}

func readAPILifecycleScans(ctx context.Context, tx pgx.Tx, workspaceID int64, service string) ([]APISecurityScan, error) {
	rows, err := tx.Query(ctx, "SELECT "+apiSecuritySummaryColumns+", operations FROM plume.api_security_scans WHERE workspace_id=$1 AND source=$2 ORDER BY id DESC LIMIT 2", workspaceID, service)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (APISecurityScan, error) {
		var scan APISecurityScan
		err := row.Scan(append(apiSecuritySummaryFields(&scan.APISecurityScanSummary), &scan.Operations)...)
		return scan, err
	})
}

func groupAPILifecycleOperations(operations []apisecurity.Operation) map[runtimeRouteKey]APILifecycleOperation {
	grouped := map[runtimeRouteKey]APILifecycleOperation{}
	for _, operation := range operations {
		key := runtimeRouteKey{operation.Method, runtimesecurity.RouteKey(operation.Path)}
		group, exists := grouped[key]
		if !exists {
			group = APILifecycleOperation{Paths: []string{}, Auth: []string{}, SensitiveFields: []apisecurity.SensitiveField{}, AssetIDs: []int64{}, CheckIDs: []int64{}, Deprecated: operation.Deprecated}
		}
		if exists && group.Deprecated != operation.Deprecated {
			group.DeprecationMixed = true
		}
		group.Deprecated = group.Deprecated && operation.Deprecated
		group.Paths = append(group.Paths, operation.Path)
		group.Auth = append(group.Auth, operation.Auth)
		group.SensitiveFields = append(group.SensitiveFields, operation.SensitiveFields...)
		if operation.AssetID > 0 {
			group.AssetIDs = append(group.AssetIDs, operation.AssetID)
		}
		if operation.CheckID > 0 {
			group.CheckIDs = append(group.CheckIDs, operation.CheckID)
		}
		grouped[key] = group
	}
	for key, group := range grouped {
		slices.Sort(group.Paths)
		group.Paths = slices.Compact(group.Paths)
		slices.Sort(group.Auth)
		group.Auth = slices.Compact(group.Auth)
		slices.Sort(group.AssetIDs)
		group.AssetIDs = slices.Compact(group.AssetIDs)
		slices.Sort(group.CheckIDs)
		group.CheckIDs = slices.Compact(group.CheckIDs)
		slices.SortFunc(group.SensitiveFields, func(a, b apisecurity.SensitiveField) int {
			return strings.Compare(a.Location+"\x00"+a.Name+"\x00"+a.Category, b.Location+"\x00"+b.Name+"\x00"+b.Category)
		})
		group.SensitiveFields = slices.Compact(group.SensitiveFields)
		grouped[key] = group
	}
	return grouped
}

func diffAPILifecycleOperations(previous, current map[runtimeRouteKey]APILifecycleOperation) []APILifecycleChange {
	keys := map[runtimeRouteKey]bool{}
	for key := range previous {
		keys[key] = true
	}
	for key := range current {
		keys[key] = true
	}
	changes := []APILifecycleChange{}
	for key := range keys {
		before, existed := previous[key]
		after, exists := current[key]
		change := APILifecycleChange{Method: key.method, RouteKey: key.route, Kinds: []string{}}
		if existed {
			change.Before = &before
			change.Route = before.Paths[0]
		}
		if exists {
			change.After = &after
			change.Route = after.Paths[0]
		}
		switch {
		case !existed:
			change.Kinds = append(change.Kinds, "added")
		case !exists:
			change.Kinds = append(change.Kinds, "removed")
		default:
			if !slices.Equal(before.Auth, after.Auth) {
				change.Kinds = append(change.Kinds, "auth_changed")
			}
			if before.Deprecated != after.Deprecated || before.DeprecationMixed != after.DeprecationMixed {
				change.Kinds = append(change.Kinds, "deprecation_changed")
			}
			if !slices.Equal(before.SensitiveFields, after.SensitiveFields) {
				change.Kinds = append(change.Kinds, "sensitive_fields_changed")
			}
		}
		if len(change.Kinds) > 0 {
			changes = append(changes, change)
		}
	}
	slices.SortFunc(changes, func(a, b APILifecycleChange) int {
		return strings.Compare(a.Method+"\x00"+a.RouteKey, b.Method+"\x00"+b.RouteKey)
	})
	return changes
}

func readAPILifecycleRoutes(ctx context.Context, tx pgx.Tx, workspaceID int64, filter APILifecycleFilter, items map[apiLifecycleKey]*APILifecycleItem) error {
	rows, err := tx.Query(ctx, `SELECT s.service,r.method,r.route_key,r.route,s.id,s.name,s.kind,r.last_seen,
		r.requests,r.responses_4xx,r.responses_5xx,s.revoked_at
		FROM plume.runtime_security_routes r JOIN plume.runtime_security_sources s ON s.id=r.source_id
		WHERE s.workspace_id=$1 AND ($2='' OR s.service=$2) AND ($3=0::bigint OR s.id=$3)
		ORDER BY s.id,r.method,r.route_key LIMIT $4`, workspaceID, filter.Service, filter.SourceID, APILifecycleLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > APILifecycleLimit {
			return ErrAPILifecycleScope
		}
		var key apiLifecycleKey
		var route string
		var source APILifecycleSource
		if err := rows.Scan(&key.service, &key.method, &key.route, &route, &source.ID, &source.Name, &source.Kind, &source.LastSeen, &source.Requests, &source.Responses4xx, &source.Responses5xx, &source.RevokedAt); err != nil {
			return err
		}
		item := items[key]
		if item == nil {
			item = &APILifecycleItem{APILifecycleOperation: APILifecycleOperation{Paths: []string{route}, Auth: []string{}, SensitiveFields: []apisecurity.SensitiveField{}, AssetIDs: []int64{}, CheckIDs: []int64{}}, Service: key.service, Method: key.method, Route: route, RouteKey: key.route, State: "observed", Sources: []APILifecycleSource{}}
			items[key] = item
		}
		item.Observed = true
		if item.Registered {
			item.State = "registered"
		}
		if item.LastSeen == nil || source.LastSeen.After(*item.LastSeen) {
			at := source.LastSeen
			item.LastSeen = &at
		}
		item.Sources = append(item.Sources, source)
	}
	return rows.Err()
}

func linkAPILifecycleRuntimeAssets(ctx context.Context, tx pgx.Tx, workspaceID int64, service string, items []APILifecycleItem) error {
	positions := map[apiLifecycleKey]int{}
	for i, item := range items {
		positions[apiLifecycleKey{item.Service, item.Method, item.RouteKey}] = i
	}
	rows, err := tx.Query(ctx, `SELECT a.source,a.source_ref,a.id,COALESCE(array_agg(c.id ORDER BY c.id) FILTER (WHERE c.id IS NOT NULL),'{}'::bigint[])
		FROM plume.assets a LEFT JOIN plume.checks c ON c.workspace_id=a.workspace_id AND c.asset_id=a.id
		WHERE a.workspace_id=$1 AND a.source_provider='runtime-api' AND ($2='' OR a.source=$2)
		GROUP BY a.id ORDER BY a.id LIMIT $3`, workspaceID, service, APILifecycleLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > APILifecycleLimit {
			return ErrAPILifecycleScope
		}
		var alias, ref string
		var assetID int64
		var checkIDs []int64
		if err := rows.Scan(&alias, &ref, &assetID, &checkIDs); err != nil {
			return err
		}
		method, route, ok := strings.Cut(ref, " ")
		if !ok {
			continue
		}
		i, ok := positions[apiLifecycleKey{alias, method, runtimesecurity.RouteKey(route)}]
		if !ok {
			continue
		}
		items[i].AssetIDs = append(items[i].AssetIDs, assetID)
		items[i].CheckIDs = append(items[i].CheckIDs, checkIDs...)
		slices.Sort(items[i].AssetIDs)
		items[i].AssetIDs = slices.Compact(items[i].AssetIDs)
		slices.Sort(items[i].CheckIDs)
		items[i].CheckIDs = slices.Compact(items[i].CheckIDs)
	}
	return rows.Err()
}
