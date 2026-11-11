package collector

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryOpenMetadataLiveMetadataCoverage(t *testing.T) {
	endpoint, token := os.Getenv("SCARLET_OPENMETADATA_TEST_URL"), os.Getenv("SCARLET_OPENMETADATA_TEST_TOKEN")
	if endpoint == "" || token == "" {
		t.Skip("requires the real local OpenMetadata instance populated by its Postgres connector")
	}
	r, err := Discover(t.Context(), DiscoveryConfig{Provider: "openmetadata", System: "postgresql", Database: "scarlet_sample", Service: "ScarletPostgres", Scopes: []string{"inventory"}}, endpoint, token, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tables) < 1 || r.Coverage.Content != "not_collected" || r.Coverage.Permissions != "unknown" {
		t.Fatal("real metadata coverage was absent or overstated")
	}
	var fields, constraints int
	for _, table := range r.Tables {
		if table.Database != "scarlet_sample" || table.Schema != "inventory" || table.ScannedRows != 0 || table.PermissionsStatus != "unknown" {
			t.Fatal("upstream metadata became evidence of data content or database access")
		}
		fields += len(table.Fields)
		constraints += len(table.Constraints)
	}
	if fields == 0 || constraints == 0 {
		t.Fatal("real columns or constraints missing from upstream metadata")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/services/databaseServices/name/ScarletPostgres?fields=connection", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("cannot verify the metadata reader's connection boundary")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("metadata-only reader can access upstream connection resources")
	}
	t.Logf("Real OpenMetadata: %d tables, %d fields and %d constraints; content and ACL remain unknown", len(r.Tables), fields, constraints)
}
