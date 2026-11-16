package aisecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

type testTaskController struct {
	mu                      sync.Mutex
	grant                   gatewaygovernance.TaskGrant
	cancelled               bool
	admissions, completions int
	steps                   map[string]string
}

func newTestTaskController() *testTaskController {
	expiry := time.Now().Add(time.Hour)
	return &testTaskController{grant: gatewaygovernance.TaskGrant{Allowed: true, Required: true, TaskID: "task-one", Purpose: "support", ScopeDigest: strings.Repeat("a", 64), ExpiresAt: &expiry, Categories: []string{}, SourceDocumentIDs: []string{}, Sources: []gatewaygovernance.TaskSource{}, ModelIDs: []string{"enterprise", "public"}, ToolIDs: []string{"ticket"}, DocumentIDs: []string{"doc"}, ToolScopes: []gatewaygovernance.TaskToolScope{{ToolID: "ticket", Actions: []string{"close"}, Targets: []string{"ticket-17"}}}}}
}
func (s *testTaskController) call(ctx context.Context, op TaskOperation) (gatewaygovernance.TaskGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelled || op.Request.TaskID != s.grant.TaskID {
		return gatewaygovernance.TaskGrant{}, errors.New("revoked task")
	}
	if op.Request.Kind == "model" && !slices.Contains(s.grant.ModelIDs, op.Request.ModelID) {
		return gatewaygovernance.TaskGrant{}, errors.New("model scope")
	}
	if op.Request.Kind == "tool" {
		allowed := false
		for _, scope := range s.grant.ToolScopes {
			if scope.ToolID == op.Request.ToolID && slices.Contains(s.grant.ToolIDs, scope.ToolID) && slices.Contains(scope.Actions, op.Request.Action) && slices.Contains(scope.Targets, op.Request.Target) {
				allowed = true
			}
		}
		if !allowed {
			return gatewaygovernance.TaskGrant{}, errors.New("tool task scope")
		}
	}
	replayed := false
	if op.Stage == "admit" {
		binding := taskBinding(ctx, op.Request)
		if s.steps == nil {
			s.steps = map[string]string{}
		}
		if previous, ok := s.steps[binding.TaskStepID]; ok {
			if previous != binding.TaskStepDigest {
				return gatewaygovernance.TaskGrant{}, errors.New("step changed")
			}
			replayed = true
		} else {
			s.steps[binding.TaskStepID] = binding.TaskStepDigest
			s.admissions++
		}
		s.grant.Admitted = true
	}
	if op.Stage == "complete" {
		if op.Audit == nil || op.Audit.Stage != "completed" {
			return gatewaygovernance.TaskGrant{}, errors.New("no completion proof")
		}
		s.completions++
		s.grant.Categories = unique(append(append([]string{}, s.grant.Categories...), op.Decision.Categories...))
		for _, source := range op.Request.sourceEvidence {
			if !slices.Contains(s.grant.Sources, source) {
				s.grant.Sources = append(s.grant.Sources, source)
				s.grant.SourceDocumentIDs = unique(append(s.grant.SourceDocumentIDs, source.DocumentID))
			}
		}
	}
	g := s.grant
	g.Replayed = replayed
	g.Categories = append([]string{}, g.Categories...)
	g.Sources = append([]gatewaygovernance.TaskSource{}, g.Sources...)
	g.SourceDocumentIDs = append([]string{}, g.SourceDocumentIDs...)
	return g, nil
}
func (s *testTaskController) cancel() { s.mu.Lock(); s.cancelled = true; s.mu.Unlock() }

func TestTrustedSourceActualInjectionInspectionAndBinding(t *testing.T) {
	for _, variant := range []string{"safe-multiline", "json-escaped-secret", "pii", "media-pii"} {
		t.Run(variant, func(t *testing.T) {
			content := "Approved\nsynthetic source"
			if variant == "json-escaped-secret" {
				content = `{"body":"\u0073\u0065cret"}`
			}
			if strings.Contains(variant, "pii") {
				content = "person@example.com"
			}
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				var p modelPayload
				if json.NewDecoder(r.Body).Decode(&p) != nil || len(p.Messages) != 2 {
					t.Error("source was not actually injected")
				}
				var text string
				_ = json.Unmarshal(p.Messages[len(p.Messages)-1].Content, &text)
				start := strings.Index(text, "[")
				var source []struct{ DocumentID, Version, ACLVersion, ContentDigest, Content string }
				if start < 0 || json.Unmarshal([]byte(text[start:]), &source) != nil || len(source) != 1 || source[0].Content != content || source[0].ACLVersion != "acl1" {
					t.Error("source bytes/identity not forwarded faithfully")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"approved answer"},"finish_reason":"stop"}]}`)
			}))
			defer up.Close()
			c := fixture()
			c.Policy.RequirePinnedSourceContent = true
			c.Policy.BlockedInputPatterns = []string{"(?i)secret"}
			c.Models[0].ResponseFormat = "openai_chat"
			sum := sha256.Sum256([]byte(content))
			c.Documents[0].IntegrityDigest = hex.EncodeToString(sum[:])
			var records []AuditRecord
			runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, func(_ context.Context, a AuditRecord) error { records = append(records, a); return nil }, func(context.Context) (map[string]LocalDocument, error) {
				return map[string]LocalDocument{"doc": {ID: "doc", Version: "v1", Content: content}}, nil
			})
			r := testRequest("model")
			r.ModelID = "enterprise"
			r.DocumentIDs = []string{"doc"}
			r.Input = `{"prompt":"safe caller"}`
			if variant == "media-pii" {
				c.Models[0].Media = testMediaPolicy()
				c.Policy.AllowedInputCategories = append(c.Policy.AllowedInputCategories, "image")
				c.Models[0].AllowedInputCategories = append(c.Models[0].AllowedInputCategories, "image")
				r.Input = mediaInput(testPNG(), "image/png")
				runtime.(*runtimeHandler).options.MediaProcessor = fakeMedia("approved picture", 99)
			}
			response := callRuntime(t, runtime, aliceToken, wire(r))
			if variant == "safe-multiline" {
				if response.Code != 200 || hits.Load() != 1 {
					t.Fatal(response.Code, response.Body.String(), hits.Load())
				}
				if len(records[len(records)-1].Decision.SourceDigests) != 1 || records[0].Decision.RequestDigest != RequestDigest(r) {
					t.Fatal("actual source not bound without mutating caller input", records)
				}
			} else if response.Code != 403 || hits.Load() != 0 {
				t.Fatal("unsafe source reached provider", response.Code, response.Body.String(), hits.Load())
			}
		})
	}
}

func TestTaskRAGLabelsAndSourcesSurviveOmittedDocumentIDs(t *testing.T) {
	c := fixture()
	c.Applications[0].RequireTask = true
	c.Documents[0].Categories = []string{}
	c.Policy.AllowedInputCategories = []string{"email"}
	c.Policy.AllowedOutputCategories = []string{"email"}
	c.Models[1].AllowedInputCategories = []string{}
	c.Models[1].AllowedOutputCategories = []string{}
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"reply":"safe"}`)
	}))
	defer up.Close()
	tasks := newTestTaskController()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, func(context.Context) (map[string]LocalDocument, error) {
		return map[string]LocalDocument{"doc": {ID: "doc", Version: "v1", Content: "person@example.com"}}, nil
	})
	h.(*runtimeHandler).options.TaskControl = tasks.call
	r := testRequest("rag")
	r.TaskID = "task-one"
	r.DocumentIDs = []string{"doc"}
	if response := callRuntime(t, h, aliceToken, wire(r)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	tasks.mu.Lock()
	g := tasks.grant
	tasks.mu.Unlock()
	if !slices.Contains(g.Categories, "email") || len(g.Sources) != 1 {
		t.Fatal("retrieved source taint not persisted", g)
	}
	r = testRequest("model")
	r.RequestID = "following-model-step-1234"
	r.TaskID = "task-one"
	r.ModelID = "public"
	r.Input = `{"prompt":"innocent wrapper"}`
	if response := callRuntime(t, h, aliceToken, wire(r)); response.Code != 403 || hits.Load() != 0 {
		t.Fatal("omitting document ids washed task taint", response.Code, response.Body.String(), hits.Load())
	}
}

func TestTaskKillCancelsInFlightProviderAndSuppressesOutput(t *testing.T) {
	tasks := newTestTaskController()
	started, cancelled := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer up.Close()
	c := fixture()
	c.Applications[0].RequireTask = true
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	h.(*runtimeHandler).options.TaskControl = tasks.call
	r := testRequest("model")
	r.TaskID = "task-one"
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"safe"}`
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- callRuntime(t, h, aliceToken, wire(r)) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}
	tasks.cancel()
	select {
	case <-cancelled:
	case <-time.After(4 * time.Second):
		t.Fatal("task kill did not cancel active HTTP request")
	}
	select {
	case response := <-result:
		if response.Code < 400 {
			t.Fatal("cancelled task released a success", response.Code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled request did not terminate")
	}
}

type countingEnterpriseCache struct {
	cache      testModelCache
	gets, puts atomic.Int32
}

func (c *countingEnterpriseCache) Get(ctx context.Context, s ModelCacheScope) ([]byte, bool, error) {
	c.gets.Add(1)
	return c.cache.Get(ctx, s)
}
func (c *countingEnterpriseCache) Put(ctx context.Context, s ModelCacheScope, raw []byte) error {
	c.puts.Add(1)
	return c.cache.Put(ctx, s, raw)
}

func TestTaskCacheRequiresAdmissionAndRestrictedSourcesNeverPersist(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "task-cache", true: "zero-retention"}[restricted], func(t *testing.T) {
			c := fixture()
			c.Applications[0].RequireTask = true
			c.Documents[0].Categories = nil
			c.Models[0].ResponseFormat = "openai_chat"
			if restricted {
				c.Documents[0].UsePolicy = &DataUsePolicy{AllowedPurposes: []string{"support"}, MaxRetentionHours: 0}
				c.Models[0].Handling = &DataHandling{TrainingUse: "prohibited", EvidenceRef: "operator-reviewed", AuthorizationExpiresAt: time.Now().Add(time.Hour)}
			}
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"approved"},"finish_reason":"stop"}]}`)
			}))
			defer up.Close()
			tasks := newTestTaskController()
			cache := &countingEnterpriseCache{cache: testModelCache{data: map[ModelCacheScope][]byte{}}}
			h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, func(context.Context) (map[string]LocalDocument, error) {
				return map[string]LocalDocument{"doc": {ID: "doc", Version: "v1", Content: "approved source"}}, nil
			})
			o := h.(*runtimeHandler).options
			o.TaskControl = tasks.call
			o.Cache = cache
			g, e := NewGateway(GatewayOptions{Runtime: o})
			if e != nil {
				t.Fatal(e)
			}
			for i, id := range []string{"task-cache-step-first-000", "task-cache-step-second-00"} {
				r := testRequest("model")
				r.RequestID = id
				r.TaskID = "task-one"
				r.ModelID = "enterprise"
				r.Input = `{"prompt":"safe"}`
				if restricted && i == 0 {
					r.DocumentIDs = []string{"doc"}
				}
				raw, _ := json.Marshal(wire(r))
				if out := gatewayRequest(g, "/v1/execute", aliceToken, string(raw), ""); out.Code != 200 {
					t.Fatal(out.Code, out.Body.String())
				}
			}
			if restricted {
				if cache.gets.Load() != 0 || cache.puts.Load() != 0 || hits.Load() != 2 {
					t.Fatal("zero-retention source reached persistence", cache.gets.Load(), cache.puts.Load(), hits.Load())
				}
			} else if cache.gets.Load() != 2 || cache.puts.Load() != 1 || hits.Load() != 1 {
				t.Fatal("task cache bypassed/doubled provider", cache.gets.Load(), cache.puts.Load(), hits.Load())
			}
			tasks.mu.Lock()
			admitted, completed := tasks.admissions, tasks.completions
			tasks.mu.Unlock()
			if admitted != 2 || completed != 2 {
				t.Fatal("cache skipped task step controls", admitted, completed)
			}
		})
	}
}

func TestTaskCachedOutputRevokedAfterCompletionNeverReleases(t *testing.T) {
	c := fixture()
	c.Applications[0].RequireTask = true
	c.Documents[0].Categories = nil
	c.Models[0].ResponseFormat = "openai_chat"
	tasks := newTestTaskController()
	cache := &testModelCache{data: map[ModelCacheScope][]byte{}}
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"checked answer"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	o := h.(*runtimeHandler).options
	o.Cache = cache
	o.TaskControl = func(ctx context.Context, op TaskOperation) (gatewaygovernance.TaskGrant, error) {
		g, e := tasks.call(ctx, op)
		if op.Stage == "complete" && op.Audit != nil && op.Audit.Execution.CacheHit {
			tasks.cancel()
		}
		return g, e
	}
	g, e := NewGateway(GatewayOptions{Runtime: o})
	if e != nil {
		t.Fatal(e)
	}
	for i, id := range []string{"task-cache-first-00001", "task-cache-second-0001"} {
		r := testRequest("model")
		r.TaskID = "task-one"
		r.RequestID = id
		r.ModelID = "enterprise"
		r.Input = `{"prompt":"safe"}`
		raw, _ := json.Marshal(wire(r))
		out := gatewayRequest(g, "/v1/execute", aliceToken, string(raw), "")
		if i == 0 && out.Code != 200 || i == 1 && (out.Code != 403 || strings.Contains(out.Body.String(), "checked answer")) {
			t.Fatal("late task revoke leaked cache", i, out.Code, out.Body.String())
		}
	}
	if hits.Load() != 1 {
		t.Fatal("cache miss in revocation test", hits.Load())
	}
}

func TestTaskMCPDiscoveryAndCallShareExactScope(t *testing.T) {
	c := fixture()
	c.Applications[0].RequireTask = true
	c.Tools[0].HighImpact = false
	c.Tools[0].Targets = []string{"ticket-17", "ticket-99"}
	var hits atomic.Int32
	g := mcpFacadeFixture(t, &c, &hits)
	tasks := newTestTaskController()
	g.(*gatewayHandler).runtime.options.TaskControl = tasks.call
	for _, method := range []string{"server/discover", "tools/list"} {
		if out := mcpFacadeRequest(g, aliceToken, method, 1, nil, nil); out.Code != 403 {
			t.Fatal("taskless metadata exposed", method, out.Code, out.Body.String())
		}
	}
	header := func(r *http.Request) { r.Header.Set("X-Scarlet-Task-Id", "task-one") }
	if out := mcpFacadeRequest(g, aliceToken, "tools/list", 1, nil, header); out.Code != 200 || !strings.Contains(out.Body.String(), "ticket-17") || strings.Contains(out.Body.String(), "ticket-99") {
		t.Fatal("metadata expanded task scope", out.Code, out.Body.String())
	}
	meta := mcpMeta()
	meta[gatewayMCPControlMeta] = map[string]any{"requestId": "task-mcp-step-denied-01"}
	p := map[string]any{"name": "close-ticket", "arguments": map[string]string{"ticketId": "ticket-99", "resolution": "fixed"}, "_meta": meta}
	if out := mcpFacadeRequest(g, aliceToken, "tools/call", 2, p, header); out.Code != 403 || hits.Load() != 0 {
		t.Fatal("task target bypassed", out.Code, out.Body.String(), hits.Load())
	}
	meta[gatewayMCPControlMeta] = map[string]any{"requestId": "task-mcp-step-allowed-01"}
	p["arguments"] = map[string]string{"ticketId": "ticket-17", "resolution": "fixed"}
	if out := mcpFacadeRequest(g, aliceToken, "tools/call", 3, p, header); out.Code != 200 || hits.Load() != 1 {
		t.Fatal("approved task MCP action failed", out.Code, out.Body.String(), hits.Load())
	}
}

func TestTaskFallbackConsumesOneLogicalStepAndBindsEveryAttempt(t *testing.T) {
	var primary, secondary atomic.Int32
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { primary.Add(1); w.WriteHeader(503) }))
	defer p.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondary.Add(1)
		_, _ = io.WriteString(w, `{"object":"chat.completion","model":"public","choices":[{"index":0,"message":{"role":"assistant","content":"approved"},"finish_reason":"stop"}]}`)
	}))
	defer s.Close()
	c := fixture()
	c.Applications[0].RequireTask = true
	c.Documents[0].Categories = nil
	c.Models[0].ResponseFormat = "openai_chat"
	c.Models[1].ResponseFormat = "openai_chat"
	tasks := newTestTaskController()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, p.URL, nil, nil)
	o := h.(*runtimeHandler).options
	o.TaskControl = tasks.call
	o.Targets["othermodel"] = Target{URL: s.URL}
	var attempts []GatewayAuthorization
	g, e := NewGateway(GatewayOptions{Runtime: o, Fallbacks: map[string][]string{"enterprise": {"public"}}, BeforeExecute: func(_ context.Context, f GatewayAuthorization) error { attempts = append(attempts, f); return nil }})
	if e != nil {
		t.Fatal(e)
	}
	r := testRequest("model")
	r.TaskID = "task-one"
	r.ModelID = "enterprise"
	r.Input = `{"prompt":"safe"}`
	raw, _ := json.Marshal(wire(r))
	out := gatewayRequest(g, "/v1/execute", aliceToken, string(raw), "")
	if out.Code != 200 || primary.Load() != 1 || secondary.Load() != 1 || len(attempts) != 2 {
		t.Fatal("fallback execution failed", out.Code, out.Body.String(), primary.Load(), secondary.Load(), len(attempts))
	}
	if attempts[0].TaskContext != attempts[1].TaskContext || attempts[0].TaskStepID != r.RequestID || attempts[0].TaskStepDigest != RequestDigest(r) {
		t.Fatal("fallback lost logical task binding", attempts)
	}
	tasks.mu.Lock()
	steps := tasks.admissions
	tasks.mu.Unlock()
	if steps != 1 {
		t.Fatal("fallback consumed more than one logical step", steps)
	}
}

func TestModelSourceHistoryKeepsRetentionContractForLaterTool(t *testing.T) {
	c := fixture()
	c.Applications[0].RequireTask = true
	c.Documents[0].Categories = nil
	c.Documents[0].UsePolicy = &DataUsePolicy{AllowedPurposes: []string{"support"}, MaxRetentionHours: 0}
	c.Models[0].Handling = &DataHandling{TrainingUse: "prohibited", EvidenceRef: "reviewed", AuthorizationExpiresAt: time.Now().Add(time.Hour)}
	c.Tools[0].HighImpact = false
	c.Tools[0].Handling = &DataHandling{RetentionHours: 24, TrainingUse: "prohibited", EvidenceRef: "reviewed", AuthorizationExpiresAt: time.Now().Add(time.Hour)}
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"reply":"approved"}`)
	}))
	defer up.Close()
	tasks := newTestTaskController()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, func(context.Context) (map[string]LocalDocument, error) {
		return map[string]LocalDocument{"doc": {ID: "doc", Version: "v1", Content: "approved private source"}}, nil
	})
	h.(*runtimeHandler).options.TaskControl = tasks.call
	r := testRequest("model")
	r.TaskID = "task-one"
	r.ModelID = "enterprise"
	r.DocumentIDs = []string{"doc"}
	r.Input = `{"prompt":"safe"}`
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	tasks.mu.Lock()
	sources := len(tasks.grant.Sources)
	tasks.mu.Unlock()
	if sources != 1 {
		t.Fatal("direct model source not persisted")
	}
	r = testRequest("tool")
	r.TaskID = "task-one"
	r.RequestID = "following-tool-step-0001"
	r.ToolID = "ticket"
	r.Action = "close"
	r.Target = "ticket-17"
	r.Arguments = map[string]string{"resolution": "fixed"}
	if out := callRuntime(t, h, aliceToken, wire(r)); out.Code != 403 || hits.Load() != 1 || !strings.Contains(out.Body.String(), "source_retention_denied") {
		t.Fatal("omitted document ids erased retention contract", out.Code, out.Body.String(), hits.Load())
	}
}

func TestSourceACLPrecedesResolverIncludingFreshHistory(t *testing.T) {
	for _, variant := range []string{"unknown", "app-scope", "doc-acl", "principal-expired"} {
		t.Run(variant, func(t *testing.T) {
			c := fixture()
			r := testRequest("model")
			r.ModelID = "enterprise"
			r.DocumentIDs = []string{"doc"}
			r.Input = `{"prompt":"safe"}`
			var reads atomic.Int32
			h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, "http://127.0.0.1:1", nil, nil)
			handler := h.(*runtimeHandler)
			handler.options.ResolveDocuments = func(context.Context, Identity, []string) (map[string]LocalDocument, error) {
				reads.Add(1)
				return nil, errors.New("must never be called")
			}
			switch variant {
			case "unknown":
				r.DocumentIDs = []string{"unknown"}
			case "app-scope":
				c.Applications[0].DocumentIDs = nil
			case "doc-acl":
				c.Documents[0].AllowedPrincipalIDs = nil
			case "principal-expired":
				expired := time.Now().Add(-time.Second)
				c.Principals[0].AuthorizationExpiresAt = &expired
			}
			if _, _, e := handler.sourceSnapshot(t.Context(), r, c, r.DocumentIDs); e == nil || reads.Load() != 0 {
				t.Fatal("unapproved source was queried", e, reads.Load())
			}
			r.sourceEvidence = []SourceEvidence{{DocumentID: r.DocumentIDs[0], Version: "v1", ACLVersion: "acl1", ContentDigest: strings.Repeat("b", 64)}}
			if handler.sourcesCurrent(t.Context(), r, c) || reads.Load() != 0 {
				t.Fatal("freshness queried unauthorized historical source", reads.Load())
			}
		})
	}
}

func TestCentralRequireTaskDeniesBeforeAnySourceOrProviderWork(t *testing.T) {
	for _, kind := range []string{"model", "rag"} {
		t.Run(kind, func(t *testing.T) {
			c := fixture()
			c.Applications[0].RequireTask = false
			var sourceCalls, providerCalls, checks atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { providerCalls.Add(1); _, _ = io.WriteString(w, `{}`) }))
			defer up.Close()
			h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, func(context.Context) (map[string]LocalDocument, error) {
				sourceCalls.Add(1)
				return map[string]LocalDocument{"doc": {ID: "doc", Version: "v1", Content: "approved"}}, nil
			})
			h.(*runtimeHandler).options.TaskControl = func(_ context.Context, op TaskOperation) (gatewaygovernance.TaskGrant, error) {
				checks.Add(1)
				return gatewaygovernance.TaskGrant{Allowed: true, Required: true}, nil
			}
			r := testRequest(kind)
			r.DocumentIDs = []string{"doc"}
			if kind == "model" {
				r.ModelID = "enterprise"
				r.Input = `{"prompt":"safe"}`
			}
			out := callRuntime(t, h, aliceToken, wire(r))
			if out.Code != 403 || checks.Load() == 0 || sourceCalls.Load() != 0 || providerCalls.Load() != 0 {
				t.Fatal("central-only task requirement was late", out.Code, sourceCalls.Load(), providerCalls.Load(), checks.Load())
			}
		})
	}
}

func TestTaskGrantRejectsTruncatedOrAmbiguousHistoricalSources(t *testing.T) {
	controller := newTestTaskController()
	r := testRequest("model")
	r.TaskID = "task-one"
	g := controller.grant
	source := gatewaygovernance.TaskSource{DocumentID: "doc", Version: "v1", ACLVersion: "acl1", ContentDigest: strings.Repeat("b", 64)}
	g.Sources = []gatewaygovernance.TaskSource{source}
	if _, e := ApplyTaskGrant(r, g); e == nil {
		t.Fatal("source metadata lost its ID binding")
	}
	g.SourceDocumentIDs = []string{"doc"}
	if _, e := ApplyTaskGrant(r, g); e != nil {
		t.Fatal(e)
	}
	g.Sources = append(g.Sources, source)
	g.SourceDocumentIDs = append(g.SourceDocumentIDs, "other")
	if _, e := ApplyTaskGrant(r, g); e == nil {
		t.Fatal("duplicate source was accepted for another document")
	}
	g = controller.grant
	g.Categories = []string{"untrusted:free-form"}
	if _, e := ApplyTaskGrant(r, g); e == nil {
		t.Fatal("invalid category receipt accepted")
	}
}

func TestGatewayQuoteUsesActualSourceAndCanonicalMediaWire(t *testing.T) {
	for _, variant := range []string{"escaped-source-wire", "canonical-jpeg-wire"} {
		t.Run(variant, func(t *testing.T) {
			var wireBytes atomic.Int64
			var fact GatewayAuthorization
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				wireBytes.Store(int64(len(raw)))
				_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"approved"},"finish_reason":"stop"}]}`)
			}))
			defer up.Close()
			c := fixture()
			c.Policy.MaxInputBytes = 16 << 20
			c.Models[0].ResponseFormat = "openai_chat"
			c.Documents[0].Categories = nil
			content := strings.Repeat(strings.Repeat("\\", 255)+" ", 128)
			sum := sha256.Sum256([]byte(content))
			c.Documents[0].IntegrityDigest = hex.EncodeToString(sum[:])
			h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, func(context.Context) (map[string]LocalDocument, error) {
				data := map[string]LocalDocument{}
				for _, doc := range c.Documents {
					data[doc.ID] = LocalDocument{ID: doc.ID, Version: doc.Version, Content: content}
				}
				return data, nil
			})
			o := h.(*runtimeHandler).options
			r := testRequest("model")
			r.ModelID = "enterprise"
			r.Input = `{"prompt":"safe"}`
			if variant == "escaped-source-wire" {
				original := c.Documents[0]
				c.Documents = nil
				c.Applications[0].DocumentIDs = nil
				for i := 0; i < 12; i++ {
					doc := original
					doc.ID = fmt.Sprintf("source-%d", i)
					c.Documents = append(c.Documents, doc)
					c.Applications[0].DocumentIDs = append(c.Applications[0].DocumentIDs, doc.ID)
					r.DocumentIDs = append(r.DocumentIDs, doc.ID)
				}
			} else {
				image := image.NewRGBA(image.Rect(0, 0, 128, 128))
				random := rand.New(rand.NewSource(42))
				for i := range image.Pix {
					image.Pix[i] = byte(random.Intn(256))
				}
				var encoded bytes.Buffer
				_ = jpeg.Encode(&encoded, image, &jpeg.Options{Quality: 10})
				r.Input = mediaInput(encoded.Bytes(), "image/jpeg")
				c.Models[0].Media = testMediaPolicy()
				c.Policy.AllowedInputCategories = append(c.Policy.AllowedInputCategories, "image")
				c.Policy.AllowedOutputCategories = append(c.Policy.AllowedOutputCategories, "image")
				c.Models[0].AllowedInputCategories = append(c.Models[0].AllowedInputCategories, "image")
				c.Models[0].AllowedOutputCategories = append(c.Models[0].AllowedOutputCategories, "image")
				o.MediaProcessor = fakeMedia("approved image", 99)
			}
			g, e := NewGateway(GatewayOptions{Runtime: o, BeforeExecute: func(_ context.Context, f GatewayAuthorization) error { fact = f; return nil }})
			if e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(wire(r))
			out := gatewayRequest(g, "/v1/execute", aliceToken, string(raw), "")
			if out.Code != 200 || fact.InputBytes != wireBytes.Load() || fact.InputBytes <= int64(len(r.Input)) {
				t.Fatal("quote did not cover actual serialized bytes", out.Code, out.Body.String(), fact.InputBytes, wireBytes.Load(), len(r.Input))
			}
			if variant == "escaped-source-wire" && (fact.InputBytes <= 1<<20 || len(fact.SourceDigests) != 12) {
				t.Fatal("large source wire lost source pins", fact.InputBytes, fact.SourceDigests)
			}
		})
	}
}
