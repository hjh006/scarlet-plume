package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDiscoveryRulesAndBoundary(t *testing.T) {
	rule := DiscoveryRule{Key: "customer.departments", Version: 2, Name: "部门词典", Kind: "dictionary", Target: "content", Category: "business", Dictionary: []string{"operations"}}
	if ValidateDiscoveryRule(rule) != nil {
		t.Fatal("valid rule rejected")
	}
	f := DiscoveryField{Name: "department", Type: "string"}
	discoveryFieldHits(&f, []DiscoveryRule{rule}, []string{"operations", "engineering"})
	if len(f.Hits) != 1 || f.Hits[0].MatchCount != 1 || f.Hits[0].ScannedCount != 2 {
		t.Fatalf("wrong aggregate: %+v", f.Hits)
	}
	rule.Kind = "regexp"
	rule.Expression = "(a)\\1"
	if ValidateDiscoveryRule(rule) == nil {
		t.Fatal("RE2 backreference accepted")
	}
	result := newDiscoveryResult()
	result.Tables = []DiscoveryTable{blankDiscoveryTable("other", "inventory", "customers")}
	if ValidateDiscoveryResult(DiscoveryConfig{Database: "approved"}, result) == nil {
		t.Fatal("cross database accepted")
	}
}
func TestDiscoveryOpenMetadataDeepContract(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	var fields bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/tables" {
			fields = strings.Contains(r.URL.Query().Get("fields"), "tags")
			if r.URL.Query().Get("database") != "service.catalog" {
				t.Error("scope missing")
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"` + id + `","name":"customers","serviceType":"Oracle","description":"合成客户","service":{"name":"service"},"database":{"name":"catalog"},"databaseSchema":{"name":"sales"},"columns":[{"name":"email","dataType":"VARCHAR","description":"客户邮箱","tags":[{"tagFQN":"PII.Sensitive"}],"children":[]}],"tableConstraints":[{"constraintType":"PRIMARY_KEY","columns":["email"]}]}],"paging":{}}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/lineage/table/") {
			_, _ = w.Write([]byte(`{"upstreamEdges":[{"fromEntity":"upstream-id","toEntity":"` + id + `"}],"downstreamEdges":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	r, e := Discover(t.Context(), DiscoveryConfig{Provider: "openmetadata", System: "oracle", Database: "catalog", Service: "service", Scopes: []string{"sales"}}, server.URL, "test-token", true)
	if e != nil {
		t.Fatal(e)
	}
	if !fields || len(r.Tables) != 1 || len(r.Tables[0].Fields) != 1 || r.Tables[0].Fields[0].Sensitivity != "PII.Sensitive" || len(r.Tables[0].Lineage) != 1 || r.Tables[0].PermissionsStatus != "unknown" {
		t.Fatalf("deep metadata contract failed: %+v", r)
	}
	if e = ValidateDiscoveryResult(DiscoveryConfig{Provider: "openmetadata", Database: "catalog", Scopes: []string{"sales"}}, r); e != nil {
		t.Fatal(e)
	}
}
func TestDiscoveryMySQLLive(t *testing.T) {
	dsn := os.Getenv("SCARLET_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set SCARLET_MYSQL_TEST_DSN to synthetic MySQL fixture")
	}
	liveDiscovery(t, "mysql", dsn)
}
func TestDiscoveryMongoLive(t *testing.T) {
	dsn := os.Getenv("SCARLET_MONGO_TEST_DSN")
	if dsn == "" {
		t.Skip("set SCARLET_MONGO_TEST_DSN to synthetic MongoDB fixture")
	}
	liveDiscovery(t, "mongodb", dsn)
}
func liveDiscovery(t *testing.T, provider, dsn string) {
	t.Helper()
	rules := append(BuiltinDiscoveryRules(), DiscoveryRule{Key: "customer.department", Version: 1, Name: "部门词典", Kind: "dictionary", Target: "content", Category: "business", Dictionary: []string{"operations"}})
	r, e := Discover(context.Background(), DiscoveryConfig{Provider: provider, System: provider, Database: "discovery_sample", Rules: rules}, dsn, "", true)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Tables) != 1 || len(r.Tables[0].Fields) < 3 || r.Tables[0].ScannedRows != 2 {
		t.Fatalf("unexpected live coverage %+v", r)
	}
	var custom, email bool
	for _, f := range r.Tables[0].Fields {
		for _, h := range f.Hits {
			if h.RuleKey == "customer.department" && h.MatchCount == 1 {
				custom = true
			}
			if h.RuleKey == "builtin.email" && h.MatchCount == 2 {
				email = true
			}
		}
	}
	if !custom || !email {
		t.Fatalf("real source recognition missing (custom=%v,email=%v)", custom, email)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "alice@example.invalid") || strings.Contains(string(b), "13800000000") {
		t.Fatal("raw sample value exported")
	}
	if e = ValidateDiscoveryResult(DiscoveryConfig{Provider: provider, Database: "discovery_sample"}, r); e != nil {
		t.Fatal(e)
	}
	t.Logf("provider=%s tables=%d fields=%d scannedRows=%d custom+email aggregate verified; raw values absent", provider, len(r.Tables), len(r.Tables[0].Fields), r.Tables[0].ScannedRows)
}

func TestDiscoveryPostgresLive(t *testing.T) {
	dsn := os.Getenv("SCARLET_DISCOVERY_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set SCARLET_DISCOVERY_PG_TEST_DSN to synthetic PostgreSQL fixture")
	}
	rule := DiscoveryRule{Key: "customer.department", Version: 1, Name: "部门词典", Kind: "dictionary", Target: "content", Category: "business", Dictionary: []string{"operations"}}
	r, e := Discover(t.Context(), DiscoveryConfig{Provider: "postgresql", System: "postgresql", Database: "discovery_test", Scopes: []string{"catalog_sample"}, Rules: append(BuiltinDiscoveryRules(), rule)}, dsn, "", true)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Tables) != 1 || r.Tables[0].ScannedRows != 2 {
		t.Fatalf("wrong postgres scope %+v", r)
	}
	var custom, email bool
	for _, f := range r.Tables[0].Fields {
		for _, h := range f.Hits {
			if h.RuleKey == "customer.department" && h.MatchCount == 1 {
				custom = true
			}
			if h.RuleKey == "builtin.email" && h.MatchCount == 2 {
				email = true
			}
		}
	}
	if !custom || !email {
		t.Fatalf("postgres source custom+builtin rules missing custom=%v email=%v", custom, email)
	}
	if e = ValidateDiscoveryResult(DiscoveryConfig{Provider: "postgresql", Database: "discovery_test", Scopes: []string{"catalog_sample"}}, r); e != nil {
		t.Fatal(e)
	}
	t.Log("real PostgreSQL SCRAM source: bounded built-in aggregate and customer dictionary verified")
}

func TestDiscoveryMariaDBLive(t *testing.T) {
	dsn := os.Getenv("SCARLET_MARIADB_TEST_DSN")
	if dsn == "" {
		t.Skip("set SCARLET_MARIADB_TEST_DSN to synthetic MariaDB fixture")
	}
	liveDiscovery(t, "mariadb", dsn)
}
