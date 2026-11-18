package store

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func TestGatewayCatalogDatabaseClockCoversCurrentPathsAndRetainsReceipts(t *testing.T) {
	for _, boundary := range []string{"principal", "required_handling", "declared_handling"} {
		t.Run(boundary, func(t *testing.T) {
			f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
			var now time.Time
			if e := f.pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); e != nil {
				t.Fatal(e)
			}
			expires := now.Add(6 * time.Second)
			c := aisecurity.DefaultConfig()
			c.Applications = []aisecurity.Application{{ID: in.ApplicationID, Enabled: true, PrincipalIDs: []string{in.PrincipalID}, ModelIDs: []string{in.ModelID}, ToolIDs: []string{"tool"}}}
			c.Principals = []aisecurity.Principal{{ID: in.PrincipalID, Enabled: true, ApplicationIDs: []string{in.ApplicationID}}}
			c.Models = []aisecurity.Model{{ID: in.ModelID, Enabled: true, EndpointRef: "model", Provider: "openai", Region: "us-east-1", ResponseFormat: "openai_chat", AllowedApplicationIDs: []string{in.ApplicationID}}}
			c.Tools = []aisecurity.Tool{{ID: "tool", Enabled: true, EndpointRef: "tool", AllowedApplicationIDs: []string{in.ApplicationID}, AllowedPrincipalIDs: []string{in.PrincipalID}, Actions: []string{"close"}, Targets: []string{"ticket-17"}}}
			if boundary == "principal" {
				c.Policy.RequireAuthorizationLeases = true
				c.Principals[0].AuthorizationExpiresAt = &expires
			} else {
				c.Policy.RequireDataHandling = boundary == "required_handling"
				h := &aisecurity.DataHandling{TrainingUse: "prohibited", EvidenceRef: "synthetic-approved-handling", AuthorizationExpiresAt: expires}
				c.Models[0].Handling, c.Tools[0].Handling = h, h
			}
			if _, e := f.repo.SaveAISecurityConfiguration(t.Context(), f.ws.ID, f.user.ID, 0, c); e != nil {
				t.Fatal(e)
			}
			cfg := gatewayTestConfig(1_000_000, 10000, 1_000_000)
			cfg.CatalogDigest = aisecurity.ConfigDigest(c)
			cfg.Applications[0].CachePolicy = GatewayCachePolicy{Enabled: true, TTLSeconds: 60, MaxEntries: 2, MaxBytes: 4096, KeyRef: "env:SCARLET_GATEWAY_TEST_CACHE_KEY", KeyVersion: "clock-v1"}
			if _, e := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 1, cfg); e != nil {
				t.Fatal(e)
			}
			in.CatalogDigest = cfg.CatalogDigest
			t.Setenv("SCARLET_GATEWAY_TEST_CACHE_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x4c}, 32)))
			put := gatewayV3CacheWrite(t, f, in, "clock-prime-cache-0001")
			var storedExpiry time.Time
			if e := f.pool.QueryRow(t.Context(), `SELECT expires_at FROM plume.ai_gateway_cache WHERE workspace_id=$1`, f.ws.ID).Scan(&storedExpiry); e != nil || !storedExpiry.Equal(expires) {
				t.Fatal("cache retention exceeded the catalog lease", storedExpiry, expires, e)
			}
			if hit, e := f.repo.GetGatewayCache(t.Context(), put.CacheScope); e != nil || !hit.Hit {
				t.Fatal("current cache failed", e)
			}
			if got, e := f.repo.ResolveGateway(t.Context(), GatewayResolveInput{WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash}); e != nil || !slices.Contains(got.ToolIDs, "tool") {
				t.Fatal("current discovery failed", got, e)
			}

			task, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, taskTestInput(t, f, in))
			if e != nil {
				t.Fatal(e)
			}
			fact := taskTestFact(t, f, task, in, "clock-recorded-step-0001")
			admission := taskTestAuthorize(t, f, in, fact)
			taskIn := in
			taskIn.TaskContext, taskIn.RequestID, taskIn.RequestDigest = fact.TaskContext, fact.TaskStepID, fact.TaskStepDigest
			receipt := taskTestSettlement(t, f, taskIn, admission, true, "succeeded")
			receipt.Audit.Decision.Categories = []string{}
			raw, _ := json.Marshal(receipt.Audit)
			complete := gatewaygovernance.TaskCompleteInput{TaskCheckInput: fact, Audit: raw}

			unknownIn := in
			unknownIn.RequestID, unknownIn.RequestDigest = "clock-unknown-step-0001", gatewayDigest("clock-unknown-step-0001")
			unresolved, e := f.repo.AuthorizeGateway(t.Context(), unknownIn)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(t.Context(), `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM $1::timestamptz-clock_timestamp()))+0.025)`, expires); e != nil {
				t.Fatal(e)
			}
			identity, e := f.repo.ResolveGateway(t.Context(), GatewayResolveInput{WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash})
			if boundary == "principal" {
				if !errors.Is(e, ErrForbidden) {
					t.Fatal("expired principal still authenticated", e)
				}
			} else if e != nil || slices.Contains(identity.ToolIDs, "tool") {
				t.Fatal("expired destination still discoverable", identity, e)
			}
			for _, kind := range []string{"model", "tool", "discovery"} {
				check := gatewaygovernance.TaskCheckInput{WorkspaceSlug: in.WorkspaceSlug, TokenHash: in.TokenHash, ApplicationID: in.ApplicationID, PrincipalID: in.PrincipalID, CatalogDigest: in.CatalogDigest, Kind: kind, ModelID: in.ModelID, ToolID: "tool", Action: "close", Target: "ticket-17"}
				if kind == "discovery" && boundary != "principal" {
					continue
				}
				if _, e = f.repo.CheckGatewayTask(t.Context(), check); !errors.Is(e, ErrForbidden) {
					t.Fatal("expired authority accepted current check", kind, e)
				}
				if kind != "discovery" {
					denied := in
					denied.Kind, denied.ToolID, denied.Action, denied.Target = kind, "tool", "close", "ticket-17"
					denied.RequestID, denied.RequestDigest = "clock-rejected-"+kind, gatewayDigest("clock-rejected-"+kind)
					if _, e = f.repo.AuthorizeGateway(t.Context(), denied); !errors.Is(e, ErrForbidden) {
						t.Fatal("expired authority admitted", kind, e)
					}
				}
			}
			if _, e = f.repo.GetGatewayCache(t.Context(), put.CacheScope); !errors.Is(e, ErrForbidden) {
				t.Fatal("expired authority read cache", e)
			}
			if e = f.repo.PutGatewayCache(t.Context(), put); !errors.Is(e, ErrForbidden) {
				t.Fatal("expired authority wrote cache", e)
			}
			if _, e = f.repo.PruneExpiredGatewayCache(t.Context()); e != nil {
				t.Fatal(e)
			}
			var remaining int
			if e = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM plume.ai_gateway_cache WHERE workspace_id=$1`, f.ws.ID).Scan(&remaining); e != nil || remaining != 0 {
				t.Fatal("expired catalog ciphertext was not pruned", remaining, e)
			}
			if _, e = f.repo.AdmitGatewayTask(t.Context(), fact); !errors.Is(e, ErrForbidden) {
				t.Fatal("expired authority admitted/replayed task", e)
			}

			// Already dispatched/recorded work remains billable and completable. This
			// does not grant another call, reset the nonce, or release a held output.
			if e = f.repo.SettleGateway(t.Context(), receipt); e != nil {
				t.Fatal("expiry lost actual successful receipt", e)
			}
			if _, e = f.repo.CompleteGatewayTask(t.Context(), complete); e != nil {
				t.Fatal("expiry blocked exact receipt completion", e)
			}
			unknown := gatewayTestSettlement(unknownIn, unresolved, true, "unknown")
			unknown.Audit.Decision.Outcome = "deny"
			if e = f.repo.SettleGateway(t.Context(), unknown); e != nil {
				t.Fatal("expiry lost unknown accounting", e)
			}
			var state string
			var reserved int64
			if e = f.pool.QueryRow(t.Context(), `SELECT state,reserved_micros FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND id=$2`, f.ws.ID, unresolved.ReservationID).Scan(&state, &reserved); e != nil || state != "unknown" || reserved != unresolved.MaxCostMicros {
				t.Fatal("unknown work refunded or rewritten", state, reserved, e)
			}
		})
	}
}

func TestGatewayToolSourceClockUsesPersistedAncestorUnionOnly(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	var now time.Time
	if e := f.pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); e != nil {
		t.Fatal(e)
	}
	expires, principalExpires := now.Add(3*time.Second), now.Add(time.Hour)
	c := aisecurity.DefaultConfig()
	c.Policy.RequireAuthorizationLeases = true
	c.Applications = []aisecurity.Application{{ID: "app", Enabled: true, PrincipalIDs: []string{"principal"}, DocumentIDs: []string{"doc"}, ModelIDs: []string{"model"}, ToolIDs: []string{"tool"}}}
	c.Principals = []aisecurity.Principal{{ID: "principal", Enabled: true, ApplicationIDs: []string{"app"}, AuthorizationExpiresAt: &principalExpires}}
	c.Documents = []aisecurity.Document{{ID: "doc", Enabled: true, Version: "v1", ACLVersion: "acl1", KnowledgeBaseID: "kb", AllowedPrincipalIDs: []string{"principal"}, AuthorizationExpiresAt: &expires}}
	c.Tools = []aisecurity.Tool{{ID: "tool", Enabled: true, EndpointRef: "tool", AllowedApplicationIDs: []string{"app"}, AllowedPrincipalIDs: []string{"principal"}, Actions: []string{"close"}, Targets: []string{"ticket-17"}}}
	c.Models = []aisecurity.Model{{ID: "model", Enabled: true, EndpointRef: "model", Provider: "openai", Region: "us-east-1", AllowedApplicationIDs: []string{"app"}}}
	if _, e := f.repo.SaveAISecurityConfiguration(t.Context(), f.ws.ID, f.user.ID, 0, c); e != nil {
		t.Fatal(e)
	}
	cfg := gatewayTestConfig(1_000_000, 10000, 1_000_000)
	cfg.CatalogDigest = aisecurity.ConfigDigest(c)
	if _, e := f.repo.SaveGatewayConfig(t.Context(), f.ws.ID, f.user.ID, 1, cfg); e != nil {
		t.Fatal(e)
	}
	in.CatalogDigest = cfg.CatalogDigest
	input := taskTestInput(t, f, in)
	parent, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	fact := taskTestFact(t, f, parent, in, "source-clock-rag-step-0001")
	fact.Kind, fact.ModelID, fact.DocumentIDs = "rag", "", []string{"doc"}
	req := in
	req.Kind, req.ModelID = "rag", ""
	req.TaskContext, req.RequestID, req.RequestDigest = fact.TaskContext, fact.TaskStepID, fact.TaskStepDigest
	grant := taskTestAuthorize(t, f, req, fact)
	receipt := taskTestSettlement(t, f, req, grant, true, "succeeded")
	source := gatewaygovernance.TaskSource{DocumentID: "doc", Version: "v1", ACLVersion: "acl1", ContentDigest: gatewayDigest("synthetic original source")}
	receipt.Audit.Decision.SourceDigests = []string{gatewayDigest(source)}
	receipt.Audit.Decision.AllowedDocumentIDs = []string{"doc"}
	if e = f.repo.SettleGateway(t.Context(), receipt); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(receipt.Audit)
	if _, e = f.repo.CompleteGatewayTask(t.Context(), gatewaygovernance.TaskCompleteInput{TaskCheckInput: fact, Audit: raw, Sources: []gatewaygovernance.TaskSource{source}}); e != nil {
		t.Fatal(e)
	}
	input.ParentTaskID, input.MaxDelegationDepth = parent.ID, 1
	input.MaxSteps = 50
	child, e := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input)
	if e != nil {
		t.Fatal(e)
	}
	tool := taskTestFact(t, f, child, in, "source-clock-child-tool-0001")
	tool.Kind, tool.ModelID, tool.ToolID, tool.Action, tool.Target = "tool", "", "tool", "close", "ticket-17"
	if g, e := f.repo.CheckGatewayTask(t.Context(), tool); e != nil || len(g.Sources) != 1 {
		t.Fatal("source was not inherited before expiry", g, e)
	}
	if _, e = f.pool.Exec(t.Context(), `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM $1::timestamptz-clock_timestamp()))+0.025)`, expires); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.CheckGatewayTask(t.Context(), tool); !errors.Is(e, ErrForbidden) {
		t.Fatal("expired ancestor source was omitted by a tool", e)
	}
	if _, e = f.repo.AdmitGatewayTask(t.Context(), tool); !errors.Is(e, ErrForbidden) {
		t.Fatal("expired ancestor source admitted a tool", e)
	}
	if _, e = f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input); !errors.Is(e, ErrForbidden) {
		t.Fatal("expired ancestor source must block a new delegation", e)
	}
	input.ParentTaskID = ""
	if _, e = f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, input); e != nil {
		t.Fatal("an unused catalog document must not block creation of an unrelated root task", e)
	}
	noSource := tool
	noSource.TaskContext = gatewaygovernance.TaskContext{}
	if g, e := f.repo.CheckGatewayTask(t.Context(), noSource); e != nil || !g.Allowed {
		t.Fatal("unused app document blocked a tool without source context", e)
	}
}
