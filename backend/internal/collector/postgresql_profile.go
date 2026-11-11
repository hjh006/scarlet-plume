package collector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errPostgreSQLRelationChanged = errors.New("source relation identity changed")

const (
	postgreSQLContentTableLimit  = 10
	postgreSQLContentColumnLimit = 16
	postgreSQLContentRowLimit    = 200
	postgreSQLConstraintLimit    = 20000
)

type PostgreSQLRowSecurity struct {
	Enabled bool `json:"enabled"`
	Forced  bool `json:"forced"`
	Active  bool `json:"active"`
}

type PostgreSQLConstraint struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Columns   []string `json:"columns"`
	Validated bool     `json:"validated"`
}

type PostgreSQLPatternCount struct {
	Category string `json:"category"`
	Count    int64  `json:"count"`
}

type PostgreSQLColumnStatistics struct {
	Column      string                   `json:"column"`
	Status      string                   `json:"status"`
	NonNullRows int64                    `json:"nonNullRows"`
	LimitedRows int64                    `json:"limitedRows"`
	Matches     []PostgreSQLPatternCount `json:"matches"`
}

type PostgreSQLContentAnalysis struct {
	Status      string                       `json:"status"`
	Method      string                       `json:"method"`
	RowLimit    int                          `json:"rowLimit"`
	RowsScanned int64                        `json:"rowsScanned"`
	Columns     []PostgreSQLColumnStatistics `json:"columns"`
	Warnings    []string                     `json:"warnings"`
}

const postgresInspectConstraintQuery = `
SELECT c.conrelid, c.conname, c.contype::text, c.convalidated,
       ARRAY(SELECT a.attname::text FROM pg_catalog.unnest(c.conkey) WITH ORDINALITY AS k(attnum, position)
             JOIN pg_catalog.pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
             ORDER BY k.position LIMIT 64),
       COALESCE(pg_catalog.cardinality(c.conkey),0) > 64
FROM pg_catalog.pg_constraint c
WHERE c.conrelid = ANY($1::oid[])
ORDER BY c.conrelid, c.conname, c.oid LIMIT $2`

func postgresInspectProfiles(ctx context.Context, tx pgx.Tx, ids []uint32, positions map[uint32]int, tables []PostgreSQLTable) {
	// New optional catalogs and source aggregates run behind savepoints: an
	// unavailable profile must not discard an otherwise valid metadata snapshot.
	if err := postgresInspectConstraints(ctx, tx, ids, positions, tables); err != nil {
		for i := range tables {
			tables[i].MetadataStatus = "partial"
			tables[i].Warnings = append(tables[i].Warnings, "constraints_unavailable")
			tables[i].Constraints = []PostgreSQLConstraint{}
		}
	}
	started := time.Now()
	for i := range tables {
		slices.Sort(tables[i].Warnings)
		tables[i].Warnings = slices.Compact(tables[i].Warnings)
		analysis := PostgreSQLContentAnalysis{Status: "unknown", Method: "bounded_prefix_aggregate", RowLimit: postgreSQLContentRowLimit,
			Columns: []PostgreSQLColumnStatistics{}, Warnings: []string{}}
		tables[i].ContentAnalysis = &analysis
		if i >= postgreSQLContentTableLimit || time.Since(started) > 6*time.Second {
			analysis.Warnings = append(analysis.Warnings, "table_budget_exceeded")
			continue
		}
		if deadline, ok := ctx.Deadline(); ctx.Err() != nil || ok && time.Until(deadline) < time.Second {
			analysis.Warnings = append(analysis.Warnings, "collection_deadline")
			continue
		}
		postgresAnalyzeContent(ctx, tx, tables[i], ids[i], &analysis)
		slices.Sort(analysis.Warnings)
		analysis.Warnings = slices.Compact(analysis.Warnings)
	}
}

func postgresInspectConstraints(ctx context.Context, tx pgx.Tx, ids []uint32, positions map[uint32]int, tables []PostgreSQLTable) error {
	if deadline, ok := ctx.Deadline(); ctx.Err() != nil || ok && time.Until(deadline) < time.Second {
		return fmt.Errorf("optional profile deadline")
	}
	return pgx.BeginFunc(ctx, tx, func(nested pgx.Tx) error {
		if _, err := nested.Exec(ctx, "SET LOCAL statement_timeout = '500ms'"); err != nil {
			return err
		}
		rows, err := nested.Query(ctx, postgresInspectConstraintQuery, ids, postgreSQLConstraintLimit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			if count == postgreSQLConstraintLimit {
				return fmt.Errorf("constraint limit")
			}
			var id uint32
			var c PostgreSQLConstraint
			var columnsTruncated bool
			if err := rows.Scan(&id, &c.Name, &c.Type, &c.Validated, &c.Columns, &columnsTruncated); err != nil {
				return err
			}
			if columnsTruncated {
				tables[positions[id]].MetadataStatus = "partial"
				tables[positions[id]].Warnings = append(tables[positions[id]].Warnings, "constraint_columns_truncated")
			}
			tables[positions[id]].Constraints = append(tables[positions[id]].Constraints, c)
			count++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		_, err = nested.Exec(ctx, "SET LOCAL statement_timeout = '5000ms'")
		return err
	})
}

func profileTypeSupported(column PostgreSQLColumn) bool {
	switch column.BaseType {
	case "text", "varchar", "bpchar", "int2", "int4", "int8", "numeric":
		return true
	default:
		return false
	}
}

func postgresAnalyzeContent(ctx context.Context, tx pgx.Tx, table PostgreSQLTable, expectedOID uint32, analysis *PostgreSQLContentAnalysis) {
	selected := make([]PostgreSQLColumn, 0)
	for _, column := range table.Columns {
		if !column.CanSelect {
			analysis.Warnings = append(analysis.Warnings, "columns_without_select")
			continue
		}
		if !profileTypeSupported(column) {
			analysis.Warnings = append(analysis.Warnings, "unsupported_column_types")
			continue
		}
		selected = append(selected, column)
	}
	// Prefer declared candidates but also inspect unnamed supported fields.
	slices.SortStableFunc(selected, func(a, b PostgreSQLColumn) int {
		if (a.Sensitivity != "") != (b.Sensitivity != "") {
			if a.Sensitivity != "" {
				return -1
			}
			return 1
		}
		return a.Ordinal - b.Ordinal
	})
	if len(selected) > postgreSQLContentColumnLimit {
		selected = selected[:postgreSQLContentColumnLimit]
		analysis.Warnings = append(analysis.Warnings, "column_budget_exceeded")
	}
	if len(selected) == 0 {
		analysis.Warnings = append(analysis.Warnings, "no_readable_supported_columns")
		return
	}
	slices.SortFunc(selected, func(a, b PostgreSQLColumn) int { return a.Ordinal - b.Ordinal })
	if table.RowSecurity != nil && table.RowSecurity.Active {
		analysis.Warnings = append(analysis.Warnings, "row_security_filtered_scope")
	}
	query, args := postgresContentQuery(table, selected, expectedOID)
	err := pgx.BeginFunc(ctx, tx, func(nested pgx.Tx) error {
		if _, err := nested.Exec(ctx, "SET LOCAL statement_timeout = '500ms'"); err != nil {
			return err
		}
		var truncated bool
		var sameRelation bool
		dest := []any{&analysis.RowsScanned, &truncated, &sameRelation}
		stats := make([]PostgreSQLColumnStatistics, len(selected))
		for i, column := range selected {
			stats[i] = PostgreSQLColumnStatistics{Column: column.Name, Status: "complete", Matches: []PostgreSQLPatternCount{{Category: "email"}, {Category: "phone"}, {Category: "national_id"}, {Category: "financial_account"}}}
			dest = append(dest, &stats[i].NonNullRows, &stats[i].LimitedRows)
			for j := range stats[i].Matches {
				dest = append(dest, &stats[i].Matches[j].Count)
			}
		}
		if err := nested.QueryRow(ctx, query, args...).Scan(dest...); err != nil {
			return err
		}
		if !sameRelation {
			return errPostgreSQLRelationChanged
		}
		if _, err := nested.Exec(ctx, "SET LOCAL statement_timeout = '5000ms'"); err != nil {
			return err
		}
		analysis.Columns = stats
		analysis.Status = "complete"
		for _, column := range stats {
			if column.LimitedRows > 0 {
				analysis.Warnings = append(analysis.Warnings, "column_value_length_limit")
				break
			}
		}
		if truncated {
			analysis.Warnings = append(analysis.Warnings, "row_limit_reached")
		}
		if len(analysis.Warnings) > 0 {
			analysis.Status = "partial"
		}
		return nil
	})
	if err != nil {
		analysis.Status = "unknown"
		analysis.RowsScanned = 0
		analysis.Columns = []PostgreSQLColumnStatistics{}
		warning := "aggregate_unavailable"
		var pgError *pgconn.PgError
		if errors.Is(err, errPostgreSQLRelationChanged) {
			warning = "source_relation_changed"
		} else if errors.As(err, &pgError) {
			switch pgError.Code {
			case "42501":
				warning = "aggregate_permission_denied"
			case "57014":
				warning = "aggregate_timeout"
			}
		}
		analysis.Warnings = append(analysis.Warnings, warning)
	}
}

func postgresContentQuery(table PostgreSQLTable, columns []PostgreSQLColumn, expectedOID uint32) (string, []any) {
	projections := make([]string, len(columns))
	counts := []string{"count(*)::bigint", "(SELECT count(*) FROM sampled) >= $1", "COALESCE(pg_catalog.to_regclass($6)::oid = $7::oid,false)"}
	for i, column := range columns {
		v := fmt.Sprintf("v%d", i)
		projections[i] = "pg_catalog.left(" + pgx.Identifier{column.Name}.Sanitize() + "::text,257) AS " + v
		counts = append(counts, "count("+v+")::bigint", "count(*) FILTER (WHERE pg_catalog.length("+v+")>256)::bigint", "count(*) FILTER (WHERE pg_catalog.length("+v+")<=256 AND "+v+" ~ $2)::bigint", "count(*) FILTER (WHERE "+v+" ~ $3)::bigint")
		// Checksum validation stays inside PostgreSQL. Neither a matching value
		// nor an identifier hash is part of the query result.
		idCheck := `CASE WHEN ` + v + ` ~ $4 THEN pg_catalog.upper(pg_catalog.right(` + v + `,1)) = pg_catalog.substr('10X98765432',
 (SELECT (pg_catalog.mod(sum(pg_catalog.substr(` + v + `,g,1)::integer * (ARRAY[7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2])[g]),11)+1)::integer FROM pg_catalog.generate_series(1,17) g),1) ELSE false END`
		cardCheck := `CASE WHEN ` + v + ` ~ $5 AND ` + v + ` <> pg_catalog.repeat(pg_catalog.left(` + v + `,1),pg_catalog.length(` + v + `)) THEN (SELECT pg_catalog.mod(sum(CASE WHEN d>9 THEN d-9 ELSE d END),10)=0
 FROM (SELECT pg_catalog.substr(` + v + `,g,1)::integer * CASE WHEN pg_catalog.mod(pg_catalog.length(` + v + `)-g,2)=1 THEN 2 ELSE 1 END AS d
 FROM pg_catalog.generate_series(1,pg_catalog.length(` + v + `)) g) digits) ELSE false END`
		counts = append(counts, "count(*) FILTER (WHERE "+idCheck+")::bigint", "count(*) FILTER (WHERE "+cardCheck+")::bigint")
	}
	query := "WITH sampled AS MATERIALIZED (SELECT " + strings.Join(projections, ",") + " FROM " + pgx.Identifier{table.Schema, table.Name}.Sanitize() +
		" WHERE pg_catalog.to_regclass($6)::oid = $7::oid LIMIT $1::integer), bounded AS (SELECT * FROM sampled) SELECT " + strings.Join(counts, ",") + " FROM bounded"
	return query, []any{postgreSQLContentRowLimit, `^[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9][A-Za-z0-9.-]*\.[A-Za-z]{2,63}$`,
		`^(\+86[- ]?)?1[3-9][0-9]{9}$`, `^[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]$`, `^[1-9][0-9]{15,18}$`, pgx.Identifier{table.Schema, table.Name}.Sanitize(), expectedOID}
}

// PostgreSQLMetadataSensitivity returns a candidate, never proof that data
// belongs to a real person. Comments participate without exporting row values.
func PostgreSQLMetadataSensitivity(name, dataType, comment string) string {
	if candidate := MetadataSensitivity(name, dataType); candidate != "" {
		return candidate
	}
	words := postgresNameWords(comment)
	for start := range words {
		for end := start + 1; end <= min(len(words), start+4); end++ {
			if candidate := MetadataSensitivity(strings.Join(words[start:end], "_"), dataType); candidate != "" {
				return candidate
			}
		}
	}
	return ""
}
