package collector

import (
	"os"
	"strings"
	"testing"
)

func TestDiscoveryLocalDockerConnectionScope(t *testing.T) {
	for _, provider := range []string{"mysql", "mariadb", "mongodb", "postgresql"} {
		database := "discovery_sample"
		dsn := "reader:fixture@tcp(" + provider + ":3306)/discovery_sample"
		if provider == "mongodb" {
			dsn = "mongodb://reader:fixture@mongodb:27017/discovery_sample?authSource=discovery_sample"
		}
		if provider == "postgresql" {
			database = "scarlet_sample"
			dsn = "postgresql://reader:fixture@sample-db:5432/scarlet_sample?sslmode=disable"
		}
		cfg := DiscoveryConfig{Provider: provider, Database: database}
		connection := DiscoveryConnection{DSN: dsn, LocalDocker: true}
		if e := ValidateDiscoveryConnection(cfg, connection); e != nil {
			t.Fatalf("approved fixture %s rejected: %v", provider, e)
		}
		cfg.Database = "production"
		if ValidateDiscoveryConnection(cfg, connection) == nil {
			t.Fatal("configured production database accepted")
		}
	}
	for _, dsn := range []string{"reader:fixture@tcp(10.0.0.2:3306)/discovery_sample", "reader:fixture@tcp(example.com:3306)/discovery_sample", "reader:fixture@tcp(mysql.evil:3306)/discovery_sample", "reader:fixture@tcp(mysql:3307)/discovery_sample", "reader:fixture@tcp(mysql:3306)/production", "reader:fixture@tcp(mariadb:3306)/discovery_sample", "reader:fixture@tcp(127.0.0.1:3306)/discovery_sample"} {
		if ValidateDiscoveryConnection(DiscoveryConfig{Provider: "mysql", Database: "discovery_sample"}, DiscoveryConnection{DSN: dsn, LocalDocker: true, AllowInsecureLoopback: true}) == nil {
			t.Fatal("Docker mode accepted an unknown host, port, provider or database")
		}
	}
	for _, dsn := range []string{"mongodb://reader:fixture@10.0.0.2:27017/discovery_sample", "mongodb://reader:fixture@example.com:27017/discovery_sample", "mongodb://reader:fixture@mongodb:27018/discovery_sample", "mongodb://reader:fixture@mongodb:27017/production"} {
		if ValidateDiscoveryConnection(DiscoveryConfig{Provider: "mongodb", Database: "discovery_sample"}, DiscoveryConnection{DSN: dsn, LocalDocker: true}) == nil {
			t.Fatal("Mongo Docker boundary accepted unapproved source")
		}
	}
	if ValidateDiscoveryConnection(DiscoveryConfig{Provider: "openmetadata", Database: "discovery_sample"}, DiscoveryConnection{DSN: "http://metadata:8585", LocalDocker: true}) == nil {
		t.Fatal("Docker mode expanded to unknown provider")
	}
}

func TestDiscoveryLocalDockerOpenMetadataScope(t *testing.T) {
	cfg := DiscoveryConfig{Provider: "openmetadata", System: "postgresql", Database: "scarlet_sample", Scopes: []string{"inventory"}}
	for _, endpoint := range []string{"http://openmetadata:8585", "http://openmetadata:8585/api"} {
		if err := ValidateDiscoveryConnection(cfg, DiscoveryConnection{DSN: endpoint, LocalDocker: true}); err != nil {
			t.Fatal("fixed metadata fixture rejected", err)
		}
	}
	for _, endpoint := range []string{"http://10.0.0.2:8585/api", "http://openmetadata.evil:8585/api", "http://openmetadata:8586/api", "http://openmetadata:8585/api?token=x", "http://user:password@openmetadata:8585/api", "http://openmetadata:8585/other", "https://openmetadata:8585/api"} {
		if ValidateDiscoveryConnection(cfg, DiscoveryConnection{DSN: endpoint, LocalDocker: true}) == nil {
			t.Fatal("unexpected local metadata endpoint accepted")
		}
	}
	for _, changed := range []DiscoveryConfig{{Provider: "openmetadata", System: "postgresql", Database: "production", Scopes: []string{"inventory"}}, {Provider: "openmetadata", System: "postgresql", Database: "scarlet_sample"}, {Provider: "openmetadata", System: "mysql", Database: "scarlet_sample", Scopes: []string{"inventory"}}, {Provider: "openmetadata", System: "postgresql", Database: "scarlet_sample", Scopes: []string{"public"}}} {
		if ValidateDiscoveryConnection(changed, DiscoveryConnection{DSN: "http://openmetadata:8585/api", LocalDocker: true}) == nil {
			t.Fatal("local metadata mode escaped approved database/schema/system")
		}
	}
}

func TestDiscoveryLocalDockerRealFixtures(t *testing.T) {
	if os.Getenv("SCARLET_DISCOVERY_DOCKER_LIVE") != "1" {
		t.Skip("run in the explicitly named sample Docker network")
	}
	for _, provider := range []string{"mysql", "mariadb", "mongodb"} {
		t.Run(provider, func(t *testing.T) {
			dsn := os.Getenv("SCARLET_" + strings.ToUpper(provider) + "_TEST_DSN")
			result, e := DiscoverWithConnection(t.Context(), DiscoveryConfig{Provider: provider, System: provider, Database: "discovery_sample"}, DiscoveryConnection{DSN: dsn, LocalDocker: true})
			if e != nil {
				t.Fatal(e)
			}
			if len(result.Tables) != 1 || result.Tables[0].ScannedRows != 2 {
				t.Fatal("Docker sample source not read")
			}
			t.Logf("fixed %s service: %d tables, %d fields, %d synthetic rows observed", provider, len(result.Tables), len(result.Tables[0].Fields), result.Tables[0].ScannedRows)
		})
	}
}
