package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
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

// This test uses the real controller client, machine mux, transactions and task
// ancestors. The caller disconnects after /settle reaches the HTTP server, before
// the receipt is passed to the store. No provider or billing result is invented
// by a replacement audit callback.
func TestGatewayRuntimeTerminalReceiptSurvivesInFlightCallerCancellation(t *testing.T) {
	for _, mode := range []string{"success", "unknown", "ancestor_cancelled", "media_input_v2", "media_output_v2", "receipt_timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newIdentityHTTPFixture(t)
			media := strings.HasPrefix(mode, "media_")
			var picture bytes.Buffer
			if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			pictureURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(picture.Bytes())
			catalog := aisecurity.DefaultConfig()
			catalog.Applications = []aisecurity.Application{{ID: "app", Name: "Synthetic application", Enabled: true, PrincipalIDs: []string{"principal"}, ModelIDs: []string{"model"}, RequireTask: true}}
			catalog.Principals = []aisecurity.Principal{{ID: "principal", Enabled: true, ApplicationIDs: []string{"app"}}}
			catalog.Models = []aisecurity.Model{{ID: "model", Enabled: true, EndpointRef: "model", Provider: "openai", Region: "us-east-1", UpstreamModel: "fixed-model", ResponseFormat: "openai_chat", AllowedApplicationIDs: []string{"app"}}}
			catalog.Policy.AllowedModelRegions = []string{"us-east-1"}
			if media {
				catalog.Models[0].Media = &aisecurity.MediaPolicy{AllowedMIMETypes: []string{"image/png"}, MaxAssetBytes: 4096, MaxTotalBytes: 4096, MaxPixels: 16, InspectionVersion: aisecurity.MediaInspectionVersion}
				catalog.Models[0].AllowedInputCategories, catalog.Models[0].AllowedOutputCategories = []string{"image"}, []string{"image"}
				catalog.Policy.AllowedInputCategories, catalog.Policy.AllowedOutputCategories = []string{"image"}, []string{"image"}
			}
			cfg := gatewayHTTPConfig()
			cfg.CatalogDigest = aisecurity.ConfigDigest(catalog)
			cfg.Applications[0].RequireTask = true
			cfg.Applications[0].BudgetMicros, cfg.Applications[0].TokensPerMinute = 100000, 100000
			if _, err := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.owner.ID, 0, cfg); err != nil {
				t.Fatal(err)
			}
			credential, token, err := f.repo.CreateGatewayCredential(t.Context(), f.ws.ID, f.owner.ID, "app", "Synthetic cancellation fixture")
			if err != nil {
				t.Fatal(err)
			}
			input := store.GatewayTaskInput{ApplicationID: "app", PrincipalID: "principal", CredentialID: credential.ID, Purpose: "support", CatalogDigest: cfg.CatalogDigest, ModelIDs: []string{"model"}, MaxSteps: 3, BudgetMicros: 100000, MaxDelegationDepth: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
			if media {
				input.AllowedCategories = []string{"image"}
			}
			root, err := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.owner.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			input.ParentTaskID, input.MaxDelegationDepth = root.ID, 0
			leaf, err := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.owner.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			control := strings.Repeat("synthetic-terminal-control-", 2)
			t.Setenv("SCARLET_GATEWAY_CONTROL_TOKENS", gatewayHTTPJSON(t, map[string]string{f.ws.Slug: control}))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var receipts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/settle") && receipts.Add(1) == 1 {
					body, e := io.ReadAll(r.Body)
					if e != nil {
						t.Error(e)
						return
					}
					_ = r.Body.Close()
					r.Body = io.NopCloser(bytes.NewReader(body))
					cancel()
					if mode == "receipt_timeout" {
						select {
						case <-r.Context().Done():
						case <-time.After(6 * time.Second):
							t.Error("receipt HTTP request outlived its bounded context")
						}
						return
					}
					if mode == "ancestor_cancelled" {
						if _, e := f.repo.SetGatewayTaskStatus(t.Context(), f.ws.ID, f.owner.ID, root.ID, store.GatewayTaskStatusInput{ExpectedRevision: root.Revision, Status: "cancelled", Reason: "Synthetic ancestor cancellation"}); e != nil {
							t.Error(e)
						}
					}
					// Permit the HTTP transport to observe the caller disconnect. An
					// independent terminal receipt context stays live across this gate.
					select {
					case <-r.Context().Done():
						return
					case <-time.After(100 * time.Millisecond):
					}
				}
				f.handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			controller, err := aisecurity.NewGatewayController(aisecurity.GatewayControllerOptions{URL: server.URL, Token: control, WorkspaceSlug: f.ws.Slug, Catalog: func(context.Context) (aisecurity.Config, error) { return catalog, nil }, AllowLoopback: true, Profile: aisecurity.BifrostProfile{Provider: "openai", Region: "us-east-1", EndpointRef: "env:SCARLET_GATEWAY_BIFROST", APIKeyEnv: "SCARLET_GATEWAY_API_KEY"}})
			if err != nil {
				t.Fatal(err)
			}
			var executions atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				executions.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if mode == "unknown" {
					w.Header().Set("Content-Length", "1000")
					_, _ = io.WriteString(w, `{"incomplete":`)
					return
				}
				if mode == "media_output_v2" {
					_ = json.NewEncoder(w).Encode(map[string]any{"object": "chat.completion", "model": "fixed-model", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": pictureURL}}}}, "finish_reason": "stop"}}})
					return
				}
				_, _ = io.WriteString(w, `{"object":"chat.completion","model":"fixed-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic held answer"},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			journal, err := aisecurity.OpenReplayJournal(filepath.Join(t.TempDir(), "replay"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			var inspections atomic.Int32
			var processor aisecurity.MediaProcessor
			if media {
				// Real scanner HTTP and digest/version protocol, using a deterministic
				// synthetic recognition result; this does not claim OCR accuracy.
				scannerToken := strings.Repeat("synthetic-scanner-token-", 2)
				scanner, e := aisecurity.NewMediaInspectionHandler(func(_ context.Context, asset aisecurity.MediaAsset) (aisecurity.MediaReport, error) {
					inspections.Add(1)
					return aisecurity.MediaReport{Digest: asset.Digest, MIMEType: asset.MIMEType, Complete: true, InspectionVersion: aisecurity.MediaInspectionVersion, Text: "synthetic clean image", Confidence: 99}, nil
				}, scannerToken)
				if e != nil {
					t.Fatal(e)
				}
				scannerServer := httptest.NewServer(scanner)
				defer scannerServer.Close()
				processor, e = aisecurity.NewHTTPMediaProcessor(scannerServer.URL+"/v1/inspect", scannerToken, true)
				if e != nil {
					t.Fatal(e)
				}
			}
			handler, err := aisecurity.NewGateway(aisecurity.GatewayOptions{Runtime: aisecurity.RuntimeOptions{Catalog: func(context.Context) (aisecurity.Config, error) { return catalog, nil }, Authenticate: controller.Authenticate, Targets: map[string]aisecurity.Target{"model": {URL: upstream.URL}}, Audit: controller.Audit, TaskControl: controller.TaskControl, Journal: journal, AllowLoopback: true, MediaProcessor: processor}, BeforeExecute: controller.BeforeExecute})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			wire := aisecurity.RuntimeRequest{TaskID: leaf.ID, RequestID: "terminal-receipt-request-0001", Kind: "model", ModelID: "model", Input: `{"messages":[{"role":"user","content":"safe synthetic request"}],"max_tokens":8}`, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
			if mode == "media_input_v2" {
				wire.Input = gatewayHTTPJSON(t, map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": pictureURL}}}}}, "max_tokens": 8})
			}
			raw, _ := json.Marshal(wire)
			request := httptest.NewRequest("POST", "/v1/execute", strings.NewReader(string(raw))).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, request)
			if executions.Load() != 1 || receipts.Load() != 1 {
				t.Fatal("expected one actual provider attempt and receipt", executions.Load(), receipts.Load(), out.Code, out.Body.String())
			}
			if ctx.Err() == nil || out.Code < 400 || strings.Contains(out.Body.String(), "synthetic held answer") {
				t.Fatal("caller cancellation was turned into output permission", out.Code, out.Body.String())
			}
			if mode == "receipt_timeout" {
				var state, step string
				var reserved, spent int64
				var missingReceipt bool
				if e := f.pool.QueryRow(t.Context(), `SELECT r.state,r.last_audit IS NULL,a.reserved_micros,a.spent_micros,s.status FROM plume.ai_gateway_reservations r JOIN plume.ai_gateway_accounts a ON a.workspace_id=r.workspace_id AND a.application_id=r.application_id JOIN plume.ai_gateway_task_steps s ON s.workspace_id=r.workspace_id AND s.task_id=$2 WHERE r.workspace_id=$1`, f.ws.ID, leaf.ID).Scan(&state, &missingReceipt, &reserved, &spent, &step); e != nil || state != "reserved" || !missingReceipt || reserved <= 0 || spent != 0 || step != "admitted" {
					t.Fatal("receipt timeout released unresolved budget or invented completion", e, state, missingReceipt, reserved, spent, step)
				}
				return
			}
			var state, outcome, step string
			var reserved, spent, rootReserved, rootSpent, leafReserved, leafSpent int64
			err = f.pool.QueryRow(t.Context(), `SELECT r.state,r.last_audit->'execution'->>'outcome',a.reserved_micros,a.spent_micros,s.status,root.reserved_micros,root.spent_micros,leaf.reserved_micros,leaf.spent_micros FROM plume.ai_gateway_reservations r JOIN plume.ai_gateway_accounts a ON a.workspace_id=r.workspace_id AND a.application_id=r.application_id JOIN plume.ai_gateway_task_steps s ON s.workspace_id=r.workspace_id AND s.task_id=$2 JOIN plume.ai_gateway_tasks root ON root.workspace_id=r.workspace_id AND root.id=$3 JOIN plume.ai_gateway_tasks leaf ON leaf.workspace_id=r.workspace_id AND leaf.id=$2 WHERE r.workspace_id=$1`, f.ws.ID, leaf.ID, root.ID).Scan(&state, &outcome, &reserved, &spent, &step, &rootReserved, &rootSpent, &leafReserved, &leafSpent)
			if err != nil {
				t.Fatal("terminal receipt was lost during the real HTTP cancellation", err)
			}
			if mode == "unknown" {
				if state != "unknown" || outcome != "unknown" || reserved <= 0 || spent != 0 || rootReserved != reserved || leafReserved != reserved || step != "admitted" {
					t.Fatal("unknown execution was refunded or falsely completed", state, outcome, reserved, spent, step, rootReserved, leafReserved)
				}
			} else if state != "settled" || outcome != "succeeded" || reserved != 0 || spent <= 0 || rootReserved != 0 || leafReserved != 0 || rootSpent != spent || leafSpent != spent || mode != "ancestor_cancelled" && step != "completed" || mode == "ancestor_cancelled" && step != "admitted" {
				t.Fatal("known execution or ancestor boundary lost", state, outcome, reserved, spent, step, rootSpent, leafSpent)
			}
			if media {
				var required, observed string
				var count int
				if e := f.pool.QueryRow(t.Context(), `SELECT authorized->>'requiredMediaInspectionVersion',last_audit->'decision'->>'mediaInspectionVersion',jsonb_array_length(last_audit->'decision'->'mediaDigests') FROM plume.ai_gateway_reservations WHERE workspace_id=$1`, f.ws.ID).Scan(&required, &observed, &count); e != nil || required != aisecurity.MediaInspectionVersion || observed != aisecurity.MediaInspectionVersion || count != 1 || inspections.Load() != 1 {
					t.Fatal("real media pipeline lost the server version or reinspected held output", e, required, observed, count, inspections.Load())
				}
			}
		})
	}
}
