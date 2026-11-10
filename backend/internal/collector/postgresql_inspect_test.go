package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgreSQLSensitivity(t *testing.T) {
	for _, tc := range []struct{ name, kind, want string }{
		{"contactEmailAddress", "character varying(255)", "email"},
		{"customer_e_mail", "text", "email"},
		{"customer_phone_number", "bigint", "phone"},
		{"持有人身份证号码", "text", "national_id"},
		{"billing_bank_account", "text", "financial_account"},
		{"merchantAPIKey", "text", "credential"},
		{"customer_full_name", "text", "personal_name"},
		{"客户收货地址", "text", "address"},
		{"remote_network", "inet", "ip_address"},
		{"customer_id", "bigint", ""},
		{"has_email", "boolean", ""},
		{"password_updated_at", "timestamp without time zone", ""},
		{"voicemail", "text", ""},
		{"headphone", "text", ""},
	} {
		if got := MetadataSensitivity(tc.name, tc.kind); got != tc.want {
			t.Errorf("%s (%s) classified as %q, want %q", tc.name, tc.kind, got, tc.want)
		}
	}
}

func TestPostgreSQLDeepCatalog(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLUME_STORE_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("could not connect to PostgreSQL test database")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	suffix := fmt.Sprint(time.Now().UnixNano())
	schema, shared, restricted := "deep_"+suffix, "shared_"+suffix, "restricted_"+suffix
	reader, aclRole := "read_"+suffix, "acl_"+suffix
	const password = "catalog-test-fixture"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE; DROP SCHEMA IF EXISTS "+restricted+
			" CASCADE; DROP SCHEMA IF EXISTS "+shared+
			" CASCADE; DROP ROLE IF EXISTS "+reader+"; DROP ROLE IF EXISTS "+aclRole); err != nil {
			t.Error("could not clean PostgreSQL catalog fixtures")
		}
	})
	setup := fmt.Sprintf(`
CREATE SCHEMA %[1]s;
CREATE SCHEMA %[2]s;
CREATE SCHEMA %[6]s;
CREATE TABLE %[6]s.restricted_table (id bigint);
CREATE ROLE %[3]s LOGIN PASSWORD '%[5]s';
CREATE ROLE %[4]s NOLOGIN;
GRANT USAGE ON SCHEMA %[1]s TO %[3]s;
GRANT USAGE ON SCHEMA %[2]s TO PUBLIC;
CREATE DOMAIN %[1]s.required_email AS text NOT NULL;
CREATE TABLE %[1]s.customer_private (
	id bigint NOT NULL, client_email %[1]s.required_email, customer_phone text,
	secret_note text DEFAULT 'no-catalog-value', obsolete text, api_key text, client_ip inet
);
ALTER TABLE %[1]s.customer_private DROP COLUMN obsolete;
INSERT INTO %[1]s.customer_private VALUES (1, 'never-output@example.test', 'no-catalog-phone', 'no-catalog-text', 'no-catalog-key', '192.0.2.1');
GRANT SELECT ON %[1]s.customer_private TO %[4]s WITH GRANT OPTION;
CREATE TABLE %[1]s.catalog_columns (id bigint, email_address text);
GRANT SELECT (email_address) ON %[1]s.catalog_columns TO PUBLIC;
CREATE TABLE %[2]s.catalog_table (id bigint, category text);
GRANT SELECT ON %[2]s.catalog_table TO PUBLIC;
`, schema, shared, reader, aclRole, password, restricted)
	if _, err := admin.Exec(ctx, setup); err != nil {
		t.Fatalf("could not create catalog fixtures: %v", err)
	}
	var canReadRows bool
	if err := admin.QueryRow(ctx, "SELECT has_table_privilege($1::name, $2::text, 'SELECT')",
		reader, schema+".customer_private").Scan(&canReadRows); err != nil || canReadRows {
		t.Fatal("catalog reader can read protected fixture rows")
	}
	readerURL, err := url.Parse(dsn)
	if err != nil || readerURL.Host == "" {
		t.Fatal("catalog test requires a PostgreSQL URL")
	}
	readerURL.User = url.UserPassword(reader, password)
	result, err := InspectPostgreSQL(ctx, readerURL.String(), []string{schema, shared}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 {
		t.Fatalf("got %d catalog tables, want 3", len(result))
	}
	private := result[slices.IndexFunc(result, func(t PostgreSQLTable) bool { return t.Name == "customer_private" })]
	if private.Schema != schema || private.Owner == "" || private.PublicSchemaUsage || private.PublicSelect ||
		len(private.PublicSelectColumns) != 0 || len(private.Columns) != 6 {
		t.Fatalf("unexpected protected table metadata: %+v", private)
	}
	if private.Columns[0].Nullable || private.Columns[1].Nullable ||
		private.Columns[1].Sensitivity != "email" || private.Columns[4].Ordinal != 6 ||
		private.Columns[4].Sensitivity != "credential" || private.Columns[5].Sensitivity != "ip_address" {
		t.Fatalf("unexpected protected columns: %+v", private.Columns)
	}
	if !slices.ContainsFunc(private.Grants, func(grant PostgreSQLGrant) bool {
		return grant.Grantee == aclRole && grant.Grantor == private.Owner && grant.Privilege == "SELECT" &&
			grant.Grantable && !grant.SchemaUsage && !grant.Public && grant.Column == ""
	}) || !slices.ContainsFunc(private.Grants, func(grant PostgreSQLGrant) bool {
		return grant.Grantee == private.Owner && grant.Privilege == "INSERT" && grant.Column == ""
	}) {
		t.Fatalf("unexpected protected grants: %+v", private.Grants)
	}
	columns := result[slices.IndexFunc(result, func(t PostgreSQLTable) bool { return t.Name == "catalog_columns" })]
	if columns.PublicSelect || columns.PublicSchemaUsage || !slices.Equal(columns.PublicSelectColumns, []string{"email_address"}) {
		t.Fatalf("unexpected public column scope: %+v", columns)
	}
	table := result[slices.IndexFunc(result, func(t PostgreSQLTable) bool { return t.Name == "catalog_table" })]
	if !table.PublicSelect || !table.PublicSchemaUsage || !slices.ContainsFunc(table.Grants, func(grant PostgreSQLGrant) bool {
		return grant.Public && grant.Grantee == "PUBLIC" && grant.Privilege == "SELECT" && grant.SchemaUsage
	}) {
		t.Fatalf("unexpected public table scope: %+v", table)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "never-output") || strings.Contains(string(encoded), "no-catalog") {
		t.Fatal("catalog response included a source row or column default")
	}
	for _, unavailable := range []string{schema + "_absent", restricted} {
		scope := []string{schema, unavailable}
		denied, err := InspectPostgreSQL(ctx, readerURL.String(), scope, true)
		if !errors.Is(err, ErrPostgreSQLSchemaUnavailable) || denied != nil {
			t.Fatalf("deep catalog returned an incomplete response for %s: %v", unavailable, err)
		}
		assets, err := PostgreSQL(ctx, readerURL.String(), scope, true)
		if !errors.Is(err, ErrPostgreSQLSchemaUnavailable) || assets != nil {
			t.Fatalf("shallow catalog returned an incomplete response for %s: %v", unavailable, err)
		}
	}
}
