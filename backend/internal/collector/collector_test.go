package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSnapshotWireFormatAndStableBatch(t *testing.T) {
	assets := []Asset{
		{SourceRef: "public.zeta", Name: "记录"},
		{SourceRef: "public.alpha", Name: "订单"},
	}
	first, err := EncodeSnapshot("postgresql", "业务源", assets)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeSnapshot("postgresql", "业务源", []Asset{assets[1], assets[0]})
	if err != nil || string(first) != string(second) {
		t.Fatal("snapshot changed with input order")
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(first, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 5 || string(wire["version"]) != "1" ||
		string(wire["provider"]) != "\"postgresql\"" || string(wire["source"]) != "\"业务源\"" ||
		string(wire["assets"]) != "[{\"sourceRef\":\"public.alpha\",\"name\":\"订单\"},{\"sourceRef\":\"public.zeta\",\"name\":\"记录\"}]" {
		t.Fatalf("unexpected snapshot: %s", first)
	}
	var batch string
	if json.Unmarshal(wire["batchId"], &batch) != nil || len(batch) != 32 {
		t.Fatal("missing batch ID")
	}
	empty, err := EncodeSnapshot("openmetadata", "目录", nil)
	if err != nil || !strings.Contains(string(empty), "\"assets\":[]") {
		t.Fatalf("empty assets must serialize as an array: %s, %v", empty, err)
	}
}

func TestSnapshotRejectsInvalidOrOversizedResults(t *testing.T) {
	wide := make([]Asset, maxAssets)
	for i := range wide {
		wide[i] = Asset{SourceRef: fmt.Sprintf("public.t%d", i), Name: strings.Repeat("表", 300)}
	}
	for _, test := range []struct {
		name   string
		source string
		assets []Asset
	}{
		{"source too long", strings.Repeat("源", 81), nil},
		{"too many assets", "source", make([]Asset, maxAssets+1)},
		{"snapshot too large", "source", wide},
		{"asset id too long", "source", []Asset{{SourceRef: strings.Repeat("i", 501), Name: "名称"}}},
		{"control character", "source", []Asset{{SourceRef: "ref", Name: "名称\n"}}},
		{"duplicate", "source", []Asset{{SourceRef: "ref", Name: "甲"}, {SourceRef: "ref", Name: "乙"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := EncodeSnapshot("postgresql", test.source, test.assets); err == nil || got != nil {
				t.Fatal("invalid input yielded a snapshot")
			}
		})
	}
}

func TestPostgreSQLConnectionPolicy(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	secure, err := postgresConfig("postgresql://user:credential@db.example.test/inventory", false)
	if err != nil {
		t.Fatal(err)
	}
	if secure.TLSConfig == nil || secure.TLSConfig.InsecureSkipVerify ||
		secure.TLSConfig.ServerName != "db.example.test" || len(secure.Fallbacks) != 0 {
		t.Fatal("remote PostgreSQL must verify the host and have no fallback")
	}
	local, err := postgresConfig("postgresql://user:credential@localhost:5434/inventory?sslmode=disable", true)
	if err != nil || local.Host != "127.0.0.1" || local.Port != 5434 || local.TLSConfig != nil {
		t.Fatalf("localhost development host must be pinned: %v", err)
	}
	for _, dsn := range []string{
		"postgresql://user:credential@db.example.test/inventory?sslmode=disable",
		"postgresql://user:credential@db.example.test/inventory?sslmode=require",
		"postgresql://user:credential@db.example.test/inventory?sslmode=verify-full&sslmode=disable",
		"postgresql://user:credential@db.example.test/inventory?host=127.0.0.1",
		"postgresql://user:credential@db.example.test,localhost/inventory",
		"postgresql://user:credential@db.example.test/inventory?servicefile=credential",
		"postgresql://user:credential@",
	} {
		if _, err := postgresConfig(dsn, true); err == nil || strings.Contains(err.Error(), "credential") {
			t.Fatal("unsafe URL was accepted or revealed connection credentials")
		}
	}
	if _, err := postgresConfig("postgresql://user@127.0.0.1/inventory?sslmode=disable", false); err == nil {
		t.Fatal("plaintext loopback must require an explicit flag")
	}
	if _, err := PostgreSQL(context.Background(), "", []string{"pg_catalog"}, false); err == nil ||
		!strings.Contains(err.Error(), "schema") {
		t.Fatal("system schemas should be rejected before parsing the DSN")
	}
	t.Setenv("PGSERVICE", "must-not-resolve-a-service")
	if _, err := postgresConfig("postgresql://user@db.example.test/inventory", false); err == nil {
		t.Fatal("environment service configuration could override the collector connection")
	}
}

func TestOpenMetadataReadsScopedPaginatedMetadata(t *testing.T) {
	const first = "77ada4b4-3d2d-410d-b640-b8e08ad9763c"
	const second = "df0b830c-632c-42f8-b2e1-be05e4ca720c"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer local-token" ||
			r.URL.Query().Get("include") != "non-deleted" {
			t.Error("expected authenticated non-deleted GET")
		}
		var response string
		switch r.URL.Path {
		case "/api/v1/databases":
			if r.URL.Query().Get("service") != "\"inventory.eu\"" {
				t.Error("database query escaped service scope")
			}
			response = "{\"data\":[{\"fullyQualifiedName\":\"\\\"inventory.eu\\\".app\"}],\"paging\":{\"total\":1}}"
		case "/api/v1/tables":
			if r.URL.Query().Get("database") != "\"inventory.eu\".app" {
				t.Error("table query escaped database scope")
			}
			switch r.URL.Query().Get("after") {
			case "":
				response = fmt.Sprintf("{\"data\":[{\"id\":%q,\"fullyQualifiedName\":\"\\\"inventory.eu\\\".app.public.orders\",\"description\":\"never transmit this\"}],\"paging\":{\"after\":\"cursor+/=\",\"total\":2}}", first)
			case "cursor+/=":
				response = fmt.Sprintf("{\"data\":[{\"id\":%q,\"name\":\"returns\",\"database\":{\"fullyQualifiedName\":\"\\\"inventory.eu\\\".app\"}}],\"paging\":{\"total\":2}}", second)
			default:
				t.Error("unexpected cursor")
			}
		default:
			t.Error("unexpected API endpoint")
		}
		io.WriteString(w, response)
	}))
	defer server.Close()

	assets, err := OpenMetadata(context.Background(), server.URL+"/api/", "local-token", "inventory.eu", true)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 || len(assets) != 2 ||
		assets[0] != (Asset{SourceRef: first, Name: "\"inventory.eu\".app.public.orders"}) ||
		assets[1] != (Asset{SourceRef: second, Name: "returns"}) {
		t.Fatalf("unexpected assets or requests: %v, %d", assets, requests.Load())
	}
}

func TestOpenMetadataRejectsUnsafeResponses(t *testing.T) {
	const databasePage = "{\"data\":[{\"fullyQualifiedName\":\"inventory.app\"}],\"paging\":{}}"
	const foreignTable = "{\"data\":[{\"id\":\"77ada4b4-3d2d-410d-b640-b8e08ad9763c\",\"fullyQualifiedName\":\"inventory.other.public.orders\"}],\"paging\":{}}"
	for _, test := range []struct {
		name      string
		databases string
		tables    string
	}{
		{"foreign database", strings.Replace(databasePage, "inventory.app", "other.app", 1), ""},
		{"foreign table", databasePage, foreignTable},
		{"too many entries", databasePage, "{\"data\":[],\"paging\":{\"total\":1001}}"},
		{"oversized response", strings.Repeat("x", maxBytes+1), ""},
		{"empty cursor page", "{\"data\":[],\"paging\":{\"after\":\"repeat\"}}", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/databases":
					io.WriteString(w, test.databases)
				case "/api/v1/tables":
					io.WriteString(w, test.tables)
				default:
					t.Error("unexpected API endpoint")
				}
			}))
			defer server.Close()
			if assets, err := OpenMetadata(context.Background(), server.URL, "local-token", "inventory", true); err == nil || assets != nil {
				t.Fatal("unsafe response yielded a snapshot")
			}
		})
	}
}

func TestOpenMetadataTransportDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.Store(true)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/secret-from-remote", http.StatusFound)
	}))
	defer server.Close()

	_, err := OpenMetadata(context.Background(), server.URL, "local-token", "inventory", true)
	if err == nil || followed.Load() || strings.Contains(err.Error(), "secret-from-remote") {
		t.Fatal("redirect should fail without reaching the destination or disclosing its URL")
	}
	for _, endpoint := range []string{"http://example.test", "https://user:secret@example.test", "https://example.test/?secret=1"} {
		if _, err := metadataURL(endpoint, true); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe endpoint was accepted or revealed credentials")
		}
	}
	if _, err := metadataURL(server.URL, false); err == nil {
		t.Fatal("plaintext loopback must require an explicit flag")
	}
}
