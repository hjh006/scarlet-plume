package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func TestGatewayHighImpactApprovalInvalidatedWithoutCatalogChangeCannotExecute(t *testing.T) {
	for _, mode := range []string{"current", "current_mcp_facade", "invalidated_before", "invalidated_at_admission", "invalidated_after_dispatch", "expired_at_admission", "principal_expired_at_admission", "handling_expired_at_admission", "reviewer_demoted", "arguments_changed", "parent_cancelled_at_admission", "parent_cancelled_after_dispatch"} {
		t.Run(mode, func(t *testing.T) {
			f := newIdentityHTTPFixture(t)
			c := aisecurity.DefaultConfig()
			c.Applications = []aisecurity.Application{{ID: "app", Enabled: true, PrincipalIDs: []string{"principal"}, ToolIDs: []string{"tool"}}}
			c.Principals = []aisecurity.Principal{{ID: "principal", Enabled: true, ApplicationIDs: []string{"app"}}}
			c.Tools = []aisecurity.Tool{{ID: "tool", Enabled: true, EndpointRef: "tool", AllowedApplicationIDs: []string{"app"}, AllowedPrincipalIDs: []string{"principal"}, Actions: []string{"update"}, Targets: []string{"record-17"}, AllowedArguments: map[string][]string{"status": {"reviewed", "pending"}}, RequiredArguments: []string{"status"}, HighImpact: true, Protocol: "mcp_http", ProtocolVersion: aisecurity.MCPVersion, TargetArgument: "recordId"}}
			clockBase := time.Now().UTC()
			deadline := clockBase.Add(3 * time.Second)
			if mode == "principal_expired_at_admission" {
				c.Policy.RequireAuthorizationLeases = true
				c.Principals[0].AuthorizationExpiresAt = &deadline
			}
			if mode == "handling_expired_at_admission" {
				c.Policy.RequireDataHandling = true
				c.Tools[0].Handling = &aisecurity.DataHandling{TrainingUse: "prohibited", EvidenceRef: "synthetic-handling", AuthorizationExpiresAt: deadline}
			}
			saved, err := f.repo.SaveAISecurityConfiguration(t.Context(), f.ws.ID, f.owner.ID, 0, c)
			if err != nil {
				t.Fatal(err)
			}
			name := f.ws.Slug + "-reviewer"
			if err = f.repo.ProvisionReviewer(t.Context(), f.ws.Slug, name, "unused"); err != nil {
				t.Fatal(err)
			}
			reviewer, err := f.repo.FindUser(t.Context(), name)
			if err != nil {
				t.Fatal(err)
			}
			pack, err := f.repo.ImportAISecurityRegressionPack(t.Context(), f.ws.ID, f.owner.ID, "", store.BaselineAISecurityRegressionPack())
			if err != nil {
				t.Fatal(err)
			}
			evaluation, err := f.repo.RunAISecurityRegression(t.Context(), f.ws.ID, f.owner.ID, pack.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.repo.PublishAISecurity(t.Context(), f.ws.ID, reviewer.ID, saved.Revision, evaluation.ID, "Independent synthetic high-impact gate publication"); err != nil {
				t.Fatal(err)
			}
			cfg := store.GatewayConfig{Enabled: true, CatalogDigest: aisecurity.ConfigDigest(c), Applications: []store.GatewayApplication{{ID: "app", Enabled: true, Name: "Synthetic tool app", PrincipalID: "principal", ToolIDs: []string{"tool", "legacy-other"}, BudgetMicros: 100, RequestsPerMinute: 100, TokensPerMinute: 10000}}}
			hasParent := strings.HasPrefix(mode, "parent_")
			cfg.Applications[0].RequireTask = hasParent
			if _, err = f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.owner.ID, 0, cfg); err != nil {
				t.Fatal(err)
			}
			credential, token, err := f.repo.CreateGatewayCredential(t.Context(), f.ws.ID, f.owner.ID, "app", "Synthetic current approval client")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			request := aisecurity.Request{RequestID: "current-approval-mcp-request", Kind: "tool", ApplicationID: "app", PrincipalID: "principal", ToolID: "tool", Action: "update", Target: "record-17", Arguments: map[string]string{"status": "reviewed"}, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
			var root store.GatewayTask
			if hasParent {
				input := store.GatewayTaskInput{ApplicationID: "app", PrincipalID: "principal", CredentialID: credential.ID, Purpose: "support", CatalogDigest: cfg.CatalogDigest, ToolIDs: []string{"tool"}, ToolScopes: []gatewaygovernance.TaskToolScope{{ToolID: "tool", Actions: []string{"update"}, Targets: []string{"record-17"}}}, MaxSteps: 3, BudgetMicros: 100, MaxDelegationDepth: 1, ExpiresAt: now.Add(time.Hour)}
				root, err = f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.owner.ID, input)
				if err != nil {
					t.Fatal(err)
				}
				input.ParentTaskID, input.MaxDelegationDepth = root.ID, 0
				leaf, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.owner.ID, input)
				if e != nil {
					t.Fatal(e)
				}
				request.TaskID = leaf.ID
			}
			if mode == "expired_at_admission" {
				request.ExpiresAt = now.Add(2 * time.Second)
			}
			proposal, err := f.repo.RequestAISecurityApproval(t.Context(), f.ws.ID, f.owner.ID, saved.Revision, request)
			if err != nil {
				t.Fatal(err)
			}
			approval, err := f.repo.ReviewAISecurityApproval(t.Context(), f.ws.ID, reviewer.ID, proposal.ID, "approved", "Independent exact-action approval")
			if err != nil {
				t.Fatal(err)
			}
			loaded, _, err := f.repo.AISecurityRuntimeConfig(t.Context(), f.ws.ID, f.owner.ID)
			if err != nil || len(loaded.Approvals) != 1 {
				t.Fatal("approved runtime snapshot missing", err)
			}
			request.ApprovalID = fmt.Sprint(approval.ID)
			if mode == "current" {
				oldNode := store.GatewayAuthorizeInput{WorkspaceSlug: f.ws.Slug, TokenHash: sessionHash(token), ApplicationID: request.ApplicationID, PrincipalID: request.PrincipalID, CatalogDigest: cfg.CatalogDigest, Kind: "tool", ToolID: request.ToolID, Action: request.Action, Target: request.Target, RequestID: request.RequestID, RequestDigest: aisecurity.RequestDigest(request)}
				if _, e := f.repo.AuthorizeGateway(t.Context(), oldNode); !errors.Is(e, store.ErrForbidden) {
					t.Fatal("old node without approvalId acquired high-impact authority", e)
				}
				pending, e := f.repo.RequestAISecurityApproval(t.Context(), f.ws.ID, f.owner.ID, saved.Revision, aisecurity.Request{RequestID: request.RequestID, Kind: request.Kind, ApplicationID: request.ApplicationID, PrincipalID: request.PrincipalID, ToolID: request.ToolID, Action: request.Action, Target: request.Target, Arguments: request.Arguments, IssuedAt: request.IssuedAt, ExpiresAt: request.ExpiresAt})
				if e != nil {
					t.Fatal(e)
				}
				oldNode.ApprovalID = fmt.Sprint(pending.ID)
				if _, e = f.repo.AuthorizeGateway(t.Context(), oldNode); !errors.Is(e, store.ErrForbidden) {
					t.Fatal("pending approval granted authority", e)
				}
				oldNode.ApprovalID, oldNode.ToolID = request.ApprovalID, "legacy-other"
				if _, e = f.repo.AuthorizeGateway(t.Context(), oldNode); !errors.Is(e, store.ErrForbidden) {
					t.Fatal("missing central tool fell back to legacy authority", e)
				}
				oldNode.ToolID, oldNode.ApprovalID = request.ToolID, request.ApprovalID
				oldNode.RequestID = "different-admission-nonce"
				if _, e = f.repo.AuthorizeGateway(t.Context(), oldNode); !errors.Is(e, store.ErrForbidden) {
					t.Fatal("a current approval authorized a different execution nonce", e)
				}
			}
			if mode == "reviewer_demoted" {
				if _, e := f.pool.Exec(t.Context(), `UPDATE plume.memberships SET role='viewer' WHERE workspace_id=$1 AND user_id=$2`, f.ws.ID, reviewer.ID); e != nil {
					t.Fatal(e)
				}
			}
			invalidate := func() error {
				if hasParent {
					_, e := f.repo.SetGatewayTaskStatus(t.Context(), f.ws.ID, f.owner.ID, root.ID, store.GatewayTaskStatusInput{ExpectedRevision: root.Revision, Status: "cancelled", Reason: "Synthetic ancestor revocation"})
					return e
				}
				_, e := f.repo.SaveAISecurityConfiguration(t.Context(), f.ws.ID, f.owner.ID, saved.Revision, c)
				return e
			}
			if mode == "invalidated_before" {
				changed, e := f.repo.SaveAISecurityConfiguration(t.Context(), f.ws.ID, f.owner.ID, saved.Revision, c)
				if e != nil || aisecurity.ConfigDigest(changed.Configuration) != aisecurity.ConfigDigest(loaded) {
					t.Fatal("same catalog invalidation fixture failed", e)
				}
				var status string
				if e = f.pool.QueryRow(t.Context(), `SELECT status FROM plume.ai_security_approvals WHERE workspace_id=$1 AND id=$2`, f.ws.ID, approval.ID).Scan(&status); e != nil || status != "invalidated" {
					t.Fatal("approval did not actually become invalidated", status, e)
				}
			}
			control := strings.Repeat("synthetic-live-approval-control-", 2)
			t.Setenv("SCARLET_GATEWAY_CONTROL_TOKENS", gatewayHTTPJSON(t, map[string]string{f.ws.Slug: control}))
			server := httptest.NewServer(f.handler)
			defer server.Close()
			controller, err := aisecurity.NewGatewayController(aisecurity.GatewayControllerOptions{URL: server.URL, Token: control, WorkspaceSlug: f.ws.Slug, Catalog: func(context.Context) (aisecurity.Config, error) { return loaded, nil }, AllowLoopback: true})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			tool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if strings.HasSuffix(mode, "after_dispatch") {
					if e := invalidate(); e != nil {
						t.Error(e)
					}
				}
				var in struct {
					ID string `json:"id"`
				}
				body, _ := io.ReadAll(r.Body)
				if json.Unmarshal(body, &in) != nil {
					t.Error("actual MCP request invalid")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": in.ID, "result": map[string]any{"resultType": "complete", "content": []any{map[string]string{"type": "text", "text": "synthetic reviewed update completed"}}}})
			}))
			defer tool.Close()
			journal, err := aisecurity.OpenReplayJournal(filepath.Join(t.TempDir(), "replay"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			runtimeOptions := aisecurity.RuntimeOptions{Catalog: func(context.Context) (aisecurity.Config, error) { return loaded, nil }, Authenticate: controller.Authenticate, TaskControl: controller.TaskControl, Targets: map[string]aisecurity.Target{"tool": {URL: tool.URL}}, Journal: journal, Audit: controller.Audit, AllowLoopback: true}
			if mode == "expired_at_admission" {
				runtimeOptions.Now = func() time.Time { return now }
			}
			if mode == "principal_expired_at_admission" || mode == "handling_expired_at_admission" {
				runtimeOptions.Now = func() time.Time { return clockBase }
			}
			handler, err := aisecurity.NewGateway(aisecurity.GatewayOptions{Runtime: runtimeOptions, Tools: []aisecurity.GatewayTool{{Name: "reviewed-update", ToolID: "tool", Action: "update"}}, BeforeExecute: func(ctx context.Context, fact aisecurity.GatewayAuthorization) error {
				if strings.HasSuffix(mode, "at_admission") && !strings.Contains(mode, "expired_at_admission") {
					if e := invalidate(); e != nil {
						return e
					}
				}
				if mode == "expired_at_admission" {
					if _, e := f.pool.Exec(ctx, `SELECT pg_sleep(2.1)`); e != nil {
						return e
					}
				}
				if mode == "principal_expired_at_admission" || mode == "handling_expired_at_admission" {
					if _, e := f.pool.Exec(ctx, `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM $1::timestamptz-clock_timestamp()))+0.025)`, deadline); e != nil {
						return e
					}
				}
				return controller.BeforeExecute(ctx, fact)
			}})
			if err != nil {
				t.Fatal(err)
			}
			wire := aisecurity.RuntimeRequest{TaskID: request.TaskID, RequestID: request.RequestID, Kind: request.Kind, ToolID: request.ToolID, Action: request.Action, Target: request.Target, Arguments: request.Arguments, ApprovalID: request.ApprovalID, IssuedAt: request.IssuedAt, ExpiresAt: request.ExpiresAt}
			if mode == "arguments_changed" {
				wire.Arguments = map[string]string{"status": "pending"}
			}
			req := httptest.NewRequest("POST", "/v1/execute", strings.NewReader(gatewayHTTPJSON(t, wire)))
			if mode == "current_mcp_facade" {
				body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "reviewed-update", "arguments": map[string]string{"recordId": "record-17", "status": "reviewed"}, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": aisecurity.MCPVersion, "io.modelcontextprotocol/clientCapabilities": map[string]any{}, "org.scarletplume/control": map[string]any{"requestId": request.RequestID, "issuedAt": request.IssuedAt, "expiresAt": request.ExpiresAt, "approvalId": request.ApprovalID}}}}
				req = httptest.NewRequest("POST", "/mcp", strings.NewReader(gatewayHTTPJSON(t, body)))
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("MCP-Protocol-Version", aisecurity.MCPVersion)
				req.Header.Set("Mcp-Method", "tools/call")
				req.Header.Set("Mcp-Name", "reviewed-update")
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if strings.HasSuffix(mode, "after_dispatch") {
				var state, outcome string
				var attempted bool
				if out.Code < 400 || calls.Load() != 1 || strings.Contains(out.Body.String(), "synthetic reviewed update completed") {
					t.Fatal("revocation released or retried an executed tool", out.Code, calls.Load())
				}
				if e := f.pool.QueryRow(t.Context(), `SELECT state,last_audit->'execution'->>'outcome',(last_audit->'execution'->>'attempted')::boolean FROM plume.ai_gateway_reservations WHERE workspace_id=$1`, f.ws.ID).Scan(&state, &outcome, &attempted); e != nil || state != "unknown" || !attempted || outcome != "blocked_output" && outcome != "unknown" {
					t.Fatal("revocation erased execution facts or refunded unknown work", state, outcome, attempted, e)
				}
			} else if mode != "current" && mode != "current_mcp_facade" {
				if out.Code != 403 || calls.Load() != 0 {
					t.Fatal("invalidated approval executed through an old approved snapshot", out.Code, calls.Load())
				}
			} else if out.Code != 200 || calls.Load() != 1 {
				t.Fatal("current independent approval did not execute", out.Code, calls.Load(), out.Body.String())
			}
		})
	}
}
