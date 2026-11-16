package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestGatewayActualClassificationDenialAndEncryptedCacheReceiptHTTP(t *testing.T) {
	f := newIdentityHTTPFixture(t)
	catalog := aisecurity.DefaultConfig()
	catalog.Applications = []aisecurity.Application{{ID: "app", Enabled: true, PrincipalIDs: []string{"principal"}, ModelIDs: []string{"model"}}}
	catalog.Principals = []aisecurity.Principal{{ID: "principal", Enabled: true, ApplicationIDs: []string{"app"}}}
	catalog.Models = []aisecurity.Model{{ID: "model", Enabled: true, EndpointRef: "model", Provider: "openai", Region: "us-east-1", UpstreamModel: "fixed-model", ResponseFormat: "openai_chat", AllowedApplicationIDs: []string{"app"}}}
	catalog.Policy.AllowedModelRegions = []string{"us-east-1"}
	catalog.Policy.RequiredInspectionVersion = aisecurity.InspectionVersion
	catalog.Policy.Inspection = &aisecurity.InspectionPolicy{Version: aisecurity.InspectionVersion, BuiltinPacks: []string{"credential-v1"}}
	cfg := gatewayHTTPConfig()
	cfg.CatalogDigest = aisecurity.ConfigDigest(catalog)
	cfg.Applications[0].BudgetMicros, cfg.Applications[0].TokensPerMinute = 100000, 100000
	cfg.Applications[0].CachePolicy = store.GatewayCachePolicy{Enabled: true, TTLSeconds: 60, MaxEntries: 2, MaxBytes: 4096, KeyRef: "env:SCARLET_GATEWAY_TEST_CACHE_KEY", KeyVersion: "classification-v1"}
	t.Setenv("SCARLET_GATEWAY_TEST_CACHE_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x67}, 32)))
	if _, err := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.owner.ID, 0, cfg); err != nil {
		t.Fatal(err)
	}
	_, token, err := f.repo.CreateGatewayCredential(t.Context(), f.ws.ID, f.owner.ID, "app", "Synthetic classified HTTP client")
	if err != nil {
		t.Fatal(err)
	}
	control := strings.Repeat("synthetic-classification-control-", 2)
	t.Setenv("SCARLET_GATEWAY_CONTROL_TOKENS", gatewayHTTPJSON(t, map[string]string{f.ws.Slug: control}))
	server := httptest.NewServer(f.handler)
	defer server.Close()
	controller, err := aisecurity.NewGatewayController(aisecurity.GatewayControllerOptions{URL: server.URL, Token: control, WorkspaceSlug: f.ws.Slug, Catalog: func(context.Context) (aisecurity.Config, error) { return catalog, nil }, AllowLoopback: true, Profile: aisecurity.BifrostProfile{Provider: "openai", Region: "us-east-1", EndpointRef: "env:SCARLET_GATEWAY_BIFROST", APIKeyEnv: "SCARLET_GATEWAY_API_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"chat.completion","model":"fixed-model","choices":[{"index":0,"message":{"role":"assistant","content":"checked synthetic answer"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	journal, err := aisecurity.OpenReplayJournal(filepath.Join(t.TempDir(), "replay"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	var mu sync.Mutex
	var records []aisecurity.AuditRecord
	handler, err := aisecurity.NewGateway(aisecurity.GatewayOptions{Runtime: aisecurity.RuntimeOptions{Catalog: func(context.Context) (aisecurity.Config, error) { return catalog, nil }, Authenticate: controller.Authenticate, Targets: map[string]aisecurity.Target{"model": {URL: upstream.URL}}, Audit: func(ctx context.Context, record aisecurity.AuditRecord) error {
		mu.Lock()
		records = append(records, record)
		mu.Unlock()
		return controller.Audit(ctx, record)
	}, TaskControl: controller.TaskControl, Cache: controller, Journal: journal, AllowLoopback: true}, BeforeExecute: controller.BeforeExecute})
	if err != nil {
		t.Fatal(err)
	}
	for index, input := range []string{"person@example.invalid", "api_key=syntheticcredential0123456789", "safe request", "safe request"} {
		now := time.Now().UTC()
		wire := aisecurity.RuntimeRequest{RequestID: fmt.Sprintf("classified-http-request-%04d", index), Kind: "model", ModelID: "model", Input: fmt.Sprintf(`{"messages":[{"role":"user","content":%q}],"max_tokens":8}`, input), IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
		request := httptest.NewRequest("POST", "/v1/execute", strings.NewReader(gatewayHTTPJSON(t, wire)))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, request)
		if index < 2 {
			if out.Code != 403 || calls.Load() != 0 {
				t.Fatal("real classified denial lost its audited 403 boundary", out.Code, out.Body.String(), calls.Load())
			}
		} else if out.Code != 200 || calls.Load() != 1 || !strings.Contains(out.Body.String(), "checked synthetic answer") || index == 3 && out.Header().Get("X-Scarlet-Cache") != "hit" {
			t.Fatal("real classified encrypted cache receipt failed", out.Code, out.Body.String(), calls.Load())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	cache := slices.IndexFunc(records, func(record aisecurity.AuditRecord) bool {
		return record.Stage == "completed" && record.Execution.CacheHit
	})
	if cache < 0 || !slices.ContainsFunc(records[cache].Decision.Coverage, func(value string) bool {
		return strings.HasPrefix(value, "versioned_classification_profile:") && len(value) > 80
	}) || !slices.Contains(records[cache].Decision.Coverage, "encrypted_exact_cache_rechecked") {
		t.Fatal("cache proof did not originate in the actual versioned engine recheck")
	}
	var denied, reservations, cacheRows int
	var ciphertext []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM plume.ai_gateway_denied_events WHERE workspace_id=$1),(SELECT count(*) FROM plume.ai_gateway_reservations WHERE workspace_id=$1),(SELECT count(*) FROM plume.ai_gateway_cache WHERE workspace_id=$1)`, f.ws.ID).Scan(&denied, &reservations, &cacheRows); err != nil || denied != 3 || reservations != 1 || cacheRows != 1 {
		t.Fatal("denials/cache hits affected reservation count", denied, reservations, cacheRows, err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT ciphertext FROM plume.ai_gateway_cache WHERE workspace_id=$1`, f.ws.ID).Scan(&ciphertext); err != nil || len(ciphertext) == 0 || bytes.Contains(ciphertext, []byte("checked synthetic answer")) {
		t.Fatal("cache output was not encrypted", err)
	}
	for _, original := range []aisecurity.AuditRecord{records[0], records[cache]} {
		for _, coverage := range []string{"versioned_classification_profile:abc", "versioned_classification_profile:" + strings.Repeat("A", 64), "versioned_classification_profile:" + strings.Repeat("a", 65), "versioned_classification_profile:person@example.invalid", "other_long_profile_marker:" + strings.Repeat("a", 64), strings.Repeat("x", 200)} {
			bad := original
			bad.Decision.Coverage = []string{coverage}
			identityHTTPRequest(t, f.handler, "POST", "/api/v1/internal/ai-gateway/events", gatewayHTTPJSON(t, store.GatewayDeniedEvent{WorkspaceSlug: f.ws.Slug, Audit: &bad}), nil, control, 400)
		}
	}
	serialized, _ := json.Marshal(records)
	if bytes.Contains(serialized, []byte("person@example.invalid")) || bytes.Contains(serialized, []byte("syntheticcredential0123456789")) {
		t.Fatal("actual decision receipts contained raw input")
	}
}
