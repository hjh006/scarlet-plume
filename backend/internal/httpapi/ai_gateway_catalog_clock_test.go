package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func TestGatewayLaggingNodeCannotReleaseExpiredCatalogCacheOrStream(t *testing.T) {
	for _, boundary := range []string{"principal", "declared_handling", "source"} {
		t.Run(boundary, func(t *testing.T) {
			f := newIdentityHTTPFixture(t)
			var nodeNow time.Time
			if e := f.pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&nodeNow); e != nil {
				t.Fatal(e)
			}
			// Leave time for setup and both requests under race instrumentation
			// before waiting for the real database clock to cross the deadline.
			deadline := nodeNow.Add(8 * time.Second)
			c := aisecurity.DefaultConfig()
			c.Applications = []aisecurity.Application{{ID: "app", Enabled: true, PrincipalIDs: []string{"principal"}, ModelIDs: []string{"model"}}}
			c.Principals = []aisecurity.Principal{{ID: "principal", Enabled: true, ApplicationIDs: []string{"app"}}}
			c.Models = []aisecurity.Model{{ID: "model", Enabled: true, EndpointRef: "model", Provider: "openai", Region: "us-east-1", UpstreamModel: "fixed-model", ResponseFormat: "openai_chat", AllowedApplicationIDs: []string{"app"}}}
			c.Policy.AllowedModelRegions = []string{"us-east-1"}
			switch boundary {
			case "principal":
				c.Policy.RequireAuthorizationLeases = true
				c.Principals[0].AuthorizationExpiresAt = &deadline
			case "declared_handling":
				c.Models[0].Handling = &aisecurity.DataHandling{TrainingUse: "prohibited", EvidenceRef: "synthetic-handling", AuthorizationExpiresAt: deadline}
			case "source":
				principalDeadline := nodeNow.Add(time.Hour)
				c.Policy.RequireAuthorizationLeases = true
				c.Principals[0].AuthorizationExpiresAt = &principalDeadline
				c.Applications[0].DocumentIDs = []string{"doc"}
				c.Documents = []aisecurity.Document{{ID: "doc", Enabled: true, Version: "v1", ACLVersion: "acl-v1", KnowledgeBaseID: "kb", AllowedPrincipalIDs: []string{"principal"}, AuthorizationExpiresAt: &deadline}}
			}
			if _, e := f.repo.SaveAISecurityConfiguration(t.Context(), f.ws.ID, f.owner.ID, 0, c); e != nil {
				t.Fatal(e)
			}
			cfg := gatewayHTTPConfig()
			cfg.CatalogDigest = aisecurity.ConfigDigest(c)
			cfg.Applications[0].BudgetMicros, cfg.Applications[0].TokensPerMinute = 100000, 100000
			cfg.Applications[0].CachePolicy = store.GatewayCachePolicy{Enabled: true, TTLSeconds: 60, MaxEntries: 2, MaxBytes: 4096, KeyRef: "env:SCARLET_GATEWAY_TEST_CACHE_KEY", KeyVersion: "clock-http-v1"}
			t.Setenv("SCARLET_GATEWAY_TEST_CACHE_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x65}, 32)))
			if _, e := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.owner.ID, 0, cfg); e != nil {
				t.Fatal(e)
			}
			_, token, e := f.repo.CreateGatewayCredential(t.Context(), f.ws.ID, f.owner.ID, "app", "Synthetic clock client")
			if e != nil {
				t.Fatal(e)
			}
			control := strings.Repeat("synthetic-clock-controller-", 2)
			t.Setenv("SCARLET_GATEWAY_CONTROL_TOKENS", gatewayHTTPJSON(t, map[string]string{f.ws.Slug: control}))
			server := httptest.NewServer(f.handler)
			defer server.Close()
			controller, e := aisecurity.NewGatewayController(aisecurity.GatewayControllerOptions{URL: server.URL, Token: control, WorkspaceSlug: f.ws.Slug, Catalog: func(context.Context) (aisecurity.Config, error) { return c, nil }, AllowLoopback: true, Profile: aisecurity.BifrostProfile{Provider: "openai", Region: "us-east-1", EndpointRef: "env:SCARLET_GATEWAY_BIFROST", APIKeyEnv: "SCARLET_GATEWAY_API_KEY"}})
			if e != nil {
				t.Fatal(e)
			}
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"object":"chat.completion","model":"fixed-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic clock checked answer"},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			journal, e := aisecurity.OpenReplayJournal(filepath.Join(t.TempDir(), "replay"))
			if e != nil {
				t.Fatal(e)
			}
			defer journal.Close()
			g, e := aisecurity.NewGateway(aisecurity.GatewayOptions{Runtime: aisecurity.RuntimeOptions{Catalog: func(context.Context) (aisecurity.Config, error) { return c, nil }, Now: func() time.Time { return nodeNow }, Authenticate: controller.Authenticate, TaskControl: controller.TaskControl, Targets: map[string]aisecurity.Target{"model": {URL: upstream.URL}}, Audit: controller.Audit, Cache: controller, Journal: journal, AllowLoopback: true}, BeforeExecute: controller.BeforeExecute})
			if e != nil {
				t.Fatal(e)
			}
			call := func(n int, stream bool) *httptest.ResponseRecorder {
				body := fmt.Sprintf(`{"model":"model","messages":[{"role":"user","content":"safe synthetic request"}],"max_tokens":8,"stream":%t}`, stream)
				in := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
				in.Header.Set("Content-Type", "application/json")
				in.Header.Set("Authorization", "Bearer "+token)
				in.Header.Set("Idempotency-Key", fmt.Sprintf("clock-http-request-%04d", n))
				out := httptest.NewRecorder()
				g.ServeHTTP(out, in)
				return out
			}
			for i := 0; i < 2; i++ {
				out := call(i, false)
				if out.Code != 200 || calls.Load() != 1 || i == 1 && out.Header().Get("X-Scarlet-Cache") != "hit" {
					t.Fatal("real current cache did not execute/hit", out.Code, calls.Load(), out.Body.String())
				}
			}
			var beforeReserved, beforeSpent int64
			if e = f.pool.QueryRow(t.Context(), `SELECT reserved_micros,spent_micros FROM plume.ai_gateway_accounts WHERE workspace_id=$1 AND application_id='app'`, f.ws.ID).Scan(&beforeReserved, &beforeSpent); e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(t.Context(), `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM $1::timestamptz-clock_timestamp()))+0.025)`, deadline); e != nil {
				t.Fatal(e)
			}
			for i, stream := range []bool{false, true} {
				out := call(i+2, stream)
				if out.Code < 400 || calls.Load() != 1 || strings.Contains(out.Body.String(), "synthetic clock checked answer") || strings.Contains(out.Body.String(), "[DONE]") {
					t.Fatal("lagging node released expired cached/streamed authorization", boundary, out.Code, calls.Load())
				}
			}
			var afterReserved, afterSpent int64
			if e = f.pool.QueryRow(t.Context(), `SELECT reserved_micros,spent_micros FROM plume.ai_gateway_accounts WHERE workspace_id=$1 AND application_id='app'`, f.ws.ID).Scan(&afterReserved, &afterSpent); e != nil || afterReserved != beforeReserved || afterSpent != beforeSpent {
				t.Fatal("expired cache/stream changed prior accounting", beforeReserved, beforeSpent, afterReserved, afterSpent, e)
			}
		})
	}
}
