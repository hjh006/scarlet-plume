package runtimesecurity

import (
	"regexp"
	"slices"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

const MaxCaptureItems = 32

var (
	traceReference     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	spanReference      = regexp.MustCompile(`^[0-9a-f]{16}$`)
	subjectReference   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	databaseIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)
	fieldPath          = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,39}(\[\])?(\.[A-Za-z_][A-Za-z0-9_]{0,39}(\[\])?){0,7}$`)
)

func normalizeCapture(o *Observation) error {
	if o.TraceRef != "" && (!traceReference.MatchString(o.TraceRef) || o.TraceRef == strings.Repeat("0", 32)) ||
		o.SpanRef != "" && (!spanReference.MatchString(o.SpanRef) || o.SpanRef == strings.Repeat("0", 16) || o.TraceRef == "") ||
		o.SubjectRef != "" && !subjectReference.MatchString(o.SubjectRef) || len(o.DataFields) > MaxCaptureItems || len(o.DatabaseTargets) > MaxCaptureItems || len(o.CaptureWarnings) > MaxCaptureItems {
		return ErrInvalidObservation
	}
	fields := slices.Clone(o.DataFields)
	for _, field := range fields {
		if !fieldPath.MatchString(field.Name) || len(field.Name) > 240 || field.Count < 0 || field.Observed < 1 || field.Observed > 1000000 || field.Count > field.Observed {
			return ErrInvalidObservation
		}
		for _, part := range strings.Split(strings.ReplaceAll(field.Name, "[]", ""), ".") {
			if hexSegment.MatchString(part) {
				return ErrInvalidObservation
			}
		}
		if !slices.Contains([]string{"request", "response", "database"}, field.Location) || !slices.Contains([]string{"email", "phone", "national_id", "financial_account", "credential", "personal_name", "address", "ip_address"}, field.Category) || !slices.Contains([]string{"metadata", "format"}, field.Method) {
			return ErrInvalidObservation
		}
	}
	slices.SortFunc(fields, func(a, b DataField) int {
		if key := strings.Compare(a.Location+"\x00"+a.Name+"\x00"+a.Category+"\x00"+a.Method, b.Location+"\x00"+b.Name+"\x00"+b.Category+"\x00"+b.Method); key != 0 {
			return key
		}
		if a.Observed < b.Observed {
			return -1
		}
		if a.Observed > b.Observed {
			return 1
		}
		if a.Count < b.Count {
			return -1
		}
		if a.Count > b.Count {
			return 1
		}
		return 0
	})
	for i := 1; i < len(fields); i++ {
		a, b := fields[i-1], fields[i]
		if a.Location == b.Location && a.Name == b.Name && a.Category == b.Category && a.Method == b.Method && (a.Count != b.Count || a.Observed != b.Observed) {
			return ErrInvalidObservation
		}
	}
	o.DataFields = slices.Compact(fields)
	targets := slices.Clone(o.DatabaseTargets)
	for i := range targets {
		t := &targets[i]
		t.System = strings.ToLower(t.System)
		t.Operation = strings.ToUpper(t.Operation)
		if !validDatabaseSystem(t.System) || !collector.ValidSource(t.Source) || !databaseIdentifier.MatchString(t.Database) || !databaseIdentifier.MatchString(t.Table) || t.Schema != "" && !databaseIdentifier.MatchString(t.Schema) || t.System == "postgresql" && t.Schema == "" || !slices.Contains([]string{"SELECT", "INSERT", "UPDATE", "DELETE", "READ", "WRITE", "FIND", "AGGREGATE", "COUNT", "GET", "SET"}, t.Operation) {
			return ErrInvalidObservation
		}
	}
	slices.SortFunc(targets, func(a, b DatabaseTarget) int {
		return strings.Compare(a.System+"\x00"+a.Source+"\x00"+a.Database+"\x00"+a.Schema+"\x00"+a.Table+"\x00"+a.Operation, b.System+"\x00"+b.Source+"\x00"+b.Database+"\x00"+b.Schema+"\x00"+b.Table+"\x00"+b.Operation)
	})
	o.DatabaseTargets = slices.Compact(targets)
	warnings := slices.Clone(o.CaptureWarnings)
	for _, warning := range warnings {
		if !slices.Contains([]string{"json_size_limit", "json_depth_limit", "json_field_limit", "json_dynamic_keys", "json_invalid", "json_read_error", "json_encoding_unsupported", "invalid_trace_context", "invalid_data_fields", "invalid_database_target", "capture_item_limit"}, warning) {
			return ErrInvalidObservation
		}
	}
	slices.Sort(warnings)
	o.CaptureWarnings = slices.Compact(warnings)
	return nil
}

func validDatabaseSystem(system string) bool {
	return slices.Contains([]string{"postgresql", "mysql", "mariadb", "microsoft.sql_server", "mssql", "oracle.db", "oracle", "ibm.db2", "db2", "sqlite", "h2", "mongodb", "redis", "cassandra", "elasticsearch", "cockroachdb", "clickhouse", "opensearch", "snowflake", "dynamodb"}, system)
}
