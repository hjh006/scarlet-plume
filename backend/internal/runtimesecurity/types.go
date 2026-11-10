package runtimesecurity

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
)

const MaxObservations = 250

var (
	ErrInvalidObservation = errors.New("invalid runtime observation")
	staticSegment         = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.~-]{0,59}$`)
	placeholder           = regexp.MustCompile(`^\{[a-zA-Z_][a-zA-Z0-9_]{0,39}\}$`)
	routeParameter        = regexp.MustCompile(`\{[a-zA-Z_][a-zA-Z0-9_]{0,39}\}`)
	uuidSegment           = regexp.MustCompile(`(?i)^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	hexSegment            = regexp.MustCompile(`(?i)^[a-f0-9]{16,}$`)
)

type Observation struct {
	Method          string    `json:"method"`
	Route           string    `json:"route"`
	Status          int       `json:"status"`
	ObservedAt      time.Time `json:"observedAt"`
	DatabaseSystems []string  `json:"databaseSystems,omitempty"`
}

// RouteKey compares trusted templates whose parameter labels may differ.
func RouteKey(route string) string {
	return routeParameter.ReplaceAllString(route, "{}")
}

func NormalizeObservation(o Observation) (Observation, error) {
	o.Method = strings.ToUpper(o.Method)
	switch o.Method {
	case "GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE":
	default:
		return Observation{}, ErrInvalidObservation
	}
	if o.Status < 100 || o.Status > 599 || o.ObservedAt.IsZero() || !validRoute(o.Route) {
		return Observation{}, ErrInvalidObservation
	}
	o.ObservedAt = o.ObservedAt.UTC()
	systems := make([]string, 0)
	for _, item := range o.DatabaseSystems {
		system := strings.ToLower(item)
		switch system {
		case "postgresql", "mysql", "mariadb", "microsoft.sql_server", "mssql", "oracle.db", "oracle",
			"ibm.db2", "db2", "sqlite", "h2", "mongodb", "redis", "cassandra", "elasticsearch", "cockroachdb",
			"clickhouse", "opensearch", "snowflake", "dynamodb":
			if !slices.Contains(systems, system) {
				systems = append(systems, system)
			}
		}
	}
	slices.Sort(systems)
	o.DatabaseSystems = systems
	return o, nil
}

func validRoute(route string) bool {
	if route == "/" {
		return true
	}
	if len(route) < 2 || len(route) > 200 || route[0] != '/' {
		return false
	}
	segments := strings.Split(route[1:], "/")
	if len(segments) > 20 {
		return false
	}
	for _, part := range segments {
		if placeholder.MatchString(part) {
			continue
		}
		if !staticSegment.MatchString(part) || uuidSegment.MatchString(part) || hexSegment.MatchString(part) {
			return false
		}
	}
	return true
}
