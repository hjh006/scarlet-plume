package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgreSQLCommentCandidates(t *testing.T) {
	for _, tc := range []struct{ name, kind, comment, want string }{
		{"value", "text", "customer email address used for notices", "email"},
		{"value", "text", "客户手机号码", "phone"},
		{"value", "text", "居民身份证号码", "national_id"},
		{"value", "text", "bank account number for settlement", "financial_account"},
		{"value", "boolean", "has email address", ""},
	} {
		if got := PostgreSQLMetadataSensitivity(tc.name, tc.kind, tc.comment); got != tc.want {
			t.Errorf("comment candidate=%q want=%q", got, tc.want)
		}
	}
}

func TestPostgreSQLBoundedSourceAggregates(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect test database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	suffix := fmt.Sprint(time.Now().UnixNano())
	schema, reader := "profile_"+suffix, "profile_reader_"+suffix
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := admin.Exec(cleanup, "DROP SCHEMA IF EXISTS "+schema+" CASCADE; DROP ROLE IF EXISTS "+reader)
		if err != nil {
			t.Error("clean profile fixture")
		}
	})
	_, err = admin.Exec(ctx, fmt.Sprintf(`
CREATE SCHEMA %[1]s;
CREATE ROLE %[2]s LOGIN;
GRANT USAGE ON SCHEMA %[1]s TO %[2]s;
CREATE TABLE %[1]s.a_customers(id bigint PRIMARY KEY, contact text NOT NULL UNIQUE, mobile text, doc text, card text, payload jsonb, CHECK (id>0));
COMMENT ON TABLE %[1]s.a_customers IS 'Synthetic customer metadata';
COMMENT ON COLUMN %[1]s.a_customers.contact IS 'customer email address used for notices';
COMMENT ON COLUMN %[1]s.a_customers.doc IS '居民身份证号码';
INSERT INTO %[1]s.a_customers VALUES (1,'fixture@example.test','13800138000','11010519491231002X','4111111111111111','{}'),(2,'plain','invalid','invalid','1234567890123456','{}'),
 (3,pg_catalog.repeat('a',243)||'@example.test'||'TAIL',NULL,'110105194912310021','8888888888888888','{}');
CREATE TABLE %[1]s.b_denied(secret text);
INSERT INTO %[1]s.b_denied VALUES ('not-exported');
CREATE TABLE %[1]s.c_rls(id integer, email text);
INSERT INTO %[1]s.c_rls VALUES (1,'visible@example.test'),(2,'hidden@example.test');
ALTER TABLE %[1]s.c_rls ENABLE ROW LEVEL SECURITY;
CREATE POLICY limited_rows ON %[1]s.c_rls USING(id=1);
CREATE TABLE %[1]s.d_large(email text);
INSERT INTO %[1]s.d_large SELECT 'item'||g||'@example.test' FROM generate_series(1,300) g;
CREATE TABLE %[1]s.e_slow(email text);
INSERT INTO %[1]s.e_slow VALUES ('slow@example.test');
CREATE FUNCTION %[1]s.slow_policy() RETURNS boolean LANGUAGE plpgsql STABLE AS $$ BEGIN PERFORM pg_catalog.pg_sleep(1); RETURN true; END $$;
ALTER TABLE %[1]s.e_slow ENABLE ROW LEVEL SECURITY;
CREATE POLICY slow_rows ON %[1]s.e_slow USING(%[1]s.slow_policy());
CREATE TABLE %[1]s."f_quoted" ("value""; select" text);
INSERT INTO %[1]s."f_quoted" VALUES ('quoted@example.test');
GRANT SELECT ON %[1]s.a_customers,%[1]s.c_rls,%[1]s.d_large,%[1]s.e_slow,%[1]s.f_quoted TO %[2]s;
ANALYZE %[1]s.a_customers;
`, schema, reader))
	must(err)
	_, err = admin.Exec(ctx, "COMMENT ON TABLE "+schema+".f_quoted IS '"+strings.Repeat("x", 600)+"'")
	must(err)
	for i := 0; i < 6; i++ {
		_, err = admin.Exec(ctx, fmt.Sprintf("CREATE TABLE %s.zz_budget_%d(value text); GRANT SELECT ON %s.zz_budget_%d TO %s", schema, i, schema, i, reader))
		must(err)
	}
	u, err := url.Parse(dsn)
	must(err)
	u.User = url.User(reader)
	tables, err := InspectPostgreSQL(ctx, u.String(), []string{schema}, true)
	must(err)
	find := func(name string) PostgreSQLTable {
		t.Helper()
		i := slices.IndexFunc(tables, func(table PostgreSQLTable) bool { return table.Name == name })
		if i < 0 {
			t.Fatalf("missing table %q", name)
		}
		return tables[i]
	}
	a := find("a_customers")
	if a.MetadataStatus != "complete" || a.Comment != "Synthetic customer metadata" || a.EstimatedRows == nil || *a.EstimatedRows != 3 || len(a.Constraints) < 3 {
		t.Fatalf("missing metadata: %+v", a)
	}
	if a.Columns[1].Comment == "" || a.Columns[1].Sensitivity != "email" || !a.Columns[1].CanSelect {
		t.Fatalf("missing comment candidate: %+v", a.Columns[1])
	}
	if a.ContentAnalysis == nil || a.ContentAnalysis.Status != "partial" || a.ContentAnalysis.RowsScanned != 3 || !slices.Contains(a.ContentAnalysis.Warnings, "column_value_length_limit") {
		t.Fatalf("unexpected content scope: %+v", a.ContentAnalysis)
	}
	count := func(a *PostgreSQLContentAnalysis, column, category string) int64 {
		for _, c := range a.Columns {
			if c.Column == column {
				for _, m := range c.Matches {
					if m.Category == category {
						return m.Count
					}
				}
			}
		}
		return -1
	}
	for _, tc := range []struct{ column, category string }{{"contact", "email"}, {"mobile", "phone"}, {"doc", "national_id"}, {"card", "financial_account"}} {
		if got := count(a.ContentAnalysis, tc.column, tc.category); got != 1 {
			t.Errorf("%s/%s aggregate=%d want=1", tc.column, tc.category, got)
		}
	}
	denied := find("b_denied")
	if denied.ContentAnalysis.Status != "unknown" || len(denied.ContentAnalysis.Columns) != 0 {
		t.Fatalf("denied content interpreted as absence: %+v", denied.ContentAnalysis)
	}
	rls := find("c_rls")
	if rls.RowSecurity == nil || !rls.RowSecurity.Active || rls.ContentAnalysis.RowsScanned != 1 || !slices.Contains(rls.ContentAnalysis.Warnings, "row_security_filtered_scope") {
		t.Fatalf("RLS scope omitted: %+v", rls)
	}
	large := find("d_large")
	if large.ContentAnalysis.RowsScanned != 200 || large.ContentAnalysis.Status != "partial" || !slices.Contains(large.ContentAnalysis.Warnings, "row_limit_reached") {
		t.Fatalf("source aggregate was not bounded: %+v", large.ContentAnalysis)
	}
	slow := find("e_slow")
	if slow.ContentAnalysis.Status != "unknown" {
		t.Fatalf("timed-out aggregate was accepted: %+v", slow.ContentAnalysis)
	}
	quoted := find("f_quoted")
	if len(quoted.Comment) != 512 || quoted.MetadataStatus != "partial" || !slices.Contains(quoted.Warnings, "comments_truncated") {
		t.Fatal("bounded comments lost truncation scope")
	}
	if count(quoted.ContentAnalysis, `value"; select`, "email") != 1 {
		t.Fatal("quoted source identifier broke collection after a local timeout")
	}
	budget := find("zz_budget_5")
	if budget.ContentAnalysis.Status != "unknown" || !slices.Contains(budget.ContentAnalysis.Warnings, "table_budget_exceeded") {
		t.Fatal("unobserved tables must expose the collection budget")
	}
	encoded, err := json.Marshal(tables)
	must(err)
	for _, value := range []string{"fixture@example.test", "13800138000", "11010519491231002X", "4111111111111111", "not-exported", "quoted@example.test"} {
		if strings.Contains(string(encoded), value) {
			t.Fatal("source business value escaped aggregate boundary")
		}
	}
	// A name reused by a replacement relation must never be attributed to the
	// earlier catalog object, even when the replacement has the same columns.
	readerConn, err := pgx.Connect(ctx, u.String())
	must(err)
	defer readerConn.Close(context.Background())
	tx, err := readerConn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	must(err)
	defer tx.Rollback(context.Background())
	stale := PostgreSQLContentAnalysis{Status: "unknown", Method: "bounded_prefix_aggregate", RowLimit: 200, Columns: []PostgreSQLColumnStatistics{}, Warnings: []string{}}
	postgresAnalyzeContent(ctx, tx, a, 1, &stale)
	if stale.Status != "unknown" || stale.RowsScanned != 0 || len(stale.Columns) != 0 {
		t.Fatal("replacement relation was accepted using a stale catalog identity")
	}
}
