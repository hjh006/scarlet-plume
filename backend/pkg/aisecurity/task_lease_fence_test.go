package aisecurity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func changeTestTaskLease(s *testTaskController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The leaf business scope is unchanged: this also models an ancestor lease
	// update whose full chain digest must fence the unchanged leaf.
	s.grant.LeaseChainDigest = strings.Repeat("c", 64)
}

func leaseModelRequest(id string) Request {
	r := testRequest("model")
	r.TaskID, r.RequestID, r.ModelID, r.Input = "task-one", id, "enterprise", `{"prompt":"safe"}`
	return r
}

func TestTaskLeaseFirstGrantPinsWithoutChangingFunctionalStepDigest(t *testing.T) {
	tasks := newTestTaskController()
	r := leaseModelRequest("lease-functional-step-0001")
	functional := RequestDigest(r)
	initial := taskBinding(context.Background(), r)
	if initial.LeaseProtocolVersion != gatewaygovernance.TaskLeaseProtocolVersion || initial.LeaseChainDigest != "" {
		t.Fatal("initial check did not negotiate the mandatory lease protocol", initial)
	}
	bound, err := ApplyTaskGrant(r, tasks.grant)
	if err != nil || TaskLeaseDigest(bound) == "" || RequestDigest(bound) != functional {
		t.Fatal("trusted grant changed the logical step or failed to pin", err)
	}
	if got := taskBinding(context.Background(), bound); got.LeaseChainDigest != tasks.grant.LeaseChainDigest || got.TaskStepDigest != functional {
		t.Fatal("direct runtime lost the trusted pin", got)
	}
	changeTestTaskLease(tasks)
	if _, err := ApplyTaskGrant(bound, tasks.grant); err == nil {
		t.Fatal("an existing request was repinned to the new ancestor epoch")
	}
	for _, mutate := range []func(*gatewaygovernance.TaskGrant){
		func(g *gatewaygovernance.TaskGrant) { g.LeaseChainDigest = "" },
		func(g *gatewaygovernance.TaskGrant) { g.LeaseProtocolVersion = "" },
		func(g *gatewaygovernance.TaskGrant) { g.LeaseProtocolVersion = "future-unimplemented" },
	} {
		g := tasks.grant
		mutate(&g)
		if _, err := ApplyTaskGrant(r, g); err == nil {
			t.Fatal("missing or unsupported lease grant accepted")
		}
	}
}

func TestTaskLeaseStaleApprovalSnapshotCannotExecuteWithFreshCredential(t *testing.T) {
	for _, state := range []string{"stale", "legacy", "current"} {
		t.Run(state, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				_, _ = io.WriteString(w, `{"closed":true}`)
			}))
			defer up.Close()
			c := fixture()
			c.Applications[0].RequireTask = true
			tasks := newTestTaskController()
			r := testRequest("tool")
			r.TaskID, r.ToolID, r.Action, r.Target = "task-one", "ticket", "close", "ticket-17"
			r.Arguments = map[string]string{"resolution": "fixed"}
			approve(&c, &r)
			if state != "legacy" {
				c.Approvals[0].LeaseChainDigest = tasks.grant.LeaseChainDigest
				c.Approvals[0].LeaseProtocolVersion = gatewaygovernance.TaskLeaseProtocolVersion
			}
			if state != "current" {
				changeTestTaskLease(tasks)
			}
			h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
			o := &h.(*runtimeHandler).options
			o.TaskControl = tasks.call
			// A legitimate new bearer resolves to the same identity. The retained
			// old catalog snapshot still carries its formerly approved action.
			o.Authenticate = func(_ context.Context, hash string) (Identity, error) {
				if hash != tokenHash(bobToken) {
					return Identity{}, errors.New("old credential rejected")
				}
				return Identity{ApplicationID: "app", PrincipalID: "alice"}, nil
			}
			out := callRuntime(t, h, bobToken, wire(r))
			if state == "current" {
				if out.Code != 200 || hits.Load() != 1 {
					t.Fatal("fresh matching task approval failed", out.Code, out.Body.String(), hits.Load())
				}
			} else if out.Code != 403 || hits.Load() != 0 || !strings.Contains(out.Body.String(), "tool_approval_required_or_mismatched") {
				t.Fatal("stale or unpinned approval reached tool", out.Code, out.Body.String(), hits.Load())
			}
		})
	}
}

func TestTaskLeaseAncestorChangeSuppressesDirectAndGatewayOutput(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct-runtime", true: "gateway"}[gateway], func(t *testing.T) {
			tasks := newTestTaskController()
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				changeTestTaskLease(tasks)
				_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"private old lease output"},"finish_reason":"stop"}]}`)
			}))
			defer up.Close()
			c := fixture()
			c.Applications[0].RequireTask = true
			c.Models[0].ResponseFormat = "openai_chat"
			h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
			h.(*runtimeHandler).options.TaskControl = tasks.call
			if gateway {
				var err error
				h, err = NewGateway(GatewayOptions{Runtime: h.(*runtimeHandler).options})
				if err != nil {
					t.Fatal(err)
				}
			}
			out := callRuntime(t, h, aliceToken, wire(leaseModelRequest("lease-ancestor-inflight-0001")))
			if out.Code < 400 || strings.Contains(out.Body.String(), "private old lease output") {
				t.Fatal("ancestor lease change repinned an in-flight request", out.Code, out.Body.String())
			}
		})
	}
}

func TestTaskLeaseMissingPrivatePinCannotAdmitOrComplete(t *testing.T) {
	tasks := newTestTaskController()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return fixture(), nil }, "http://127.0.0.1:1", nil, nil)
	h.(*runtimeHandler).options.TaskControl = tasks.call
	r := leaseModelRequest("lease-unpinned-step-0001")
	for _, stage := range []string{"admit", "complete"} {
		if _, _, err := h.(*runtimeHandler).task(t.Context(), r, Decision{}, stage, nil); err == nil {
			t.Fatal("legacy request without a trusted grant reached", stage)
		}
	}
	if tasks.admissions != 0 || tasks.completions != 0 {
		t.Fatal("missing pin consumed a step")
	}
}

func TestTaskLeasePublicRequestsCannotSupplyTrustedPins(t *testing.T) {
	tasks := newTestTaskController()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return fixture(), nil }, "http://127.0.0.1:1", nil, nil)
	h.(*runtimeHandler).options.TaskControl = tasks.call
	g, err := NewGateway(GatewayOptions{Runtime: h.(*runtimeHandler).options})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(wire(leaseModelRequest("lease-forged-public-0001")))
	var forged map[string]any
	if err := json.Unmarshal(raw, &forged); err != nil {
		t.Fatal(err)
	}
	forged["leaseChainDigest"], forged["leaseProtocolVersion"] = tasks.grant.LeaseChainDigest, gatewaygovernance.TaskLeaseProtocolVersion
	for _, handler := range []http.Handler{h, g} {
		if out := callRuntime(t, handler, aliceToken, forged); out.Code != 400 {
			t.Fatal("public caller supplied a trusted execution pin", out.Code, out.Body.String())
		}
	}
}

func TestTaskLeaseDirectWatchCancelsWithoutRepinning(t *testing.T) {
	tasks := newTestTaskController()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return fixture(), nil }, "http://127.0.0.1:1", nil, nil)
	h.(*runtimeHandler).options.TaskControl = tasks.call
	r, err := ApplyTaskGrant(leaseModelRequest("lease-watch-inflight-0001"), tasks.grant)
	if err != nil {
		t.Fatal(err)
	}
	in, stop := h.(*runtimeHandler).watchTask(httptest.NewRequest(http.MethodPost, "/v1/execute", nil), r, Decision{CatalogDigest: ConfigDigest(fixture())})
	defer stop()
	changeTestTaskLease(tasks)
	select {
	case <-in.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("direct runtime watch accepted a replacement lease for the old request")
	}
}

func TestTaskLeaseSSEReplayUsesExecutionPinWithPartialDecision(t *testing.T) {
	tasks := newTestTaskController()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, "http://127.0.0.1:1", nil, nil)
	h.(*runtimeHandler).options.TaskControl = tasks.call
	g, err := NewGateway(GatewayOptions{Runtime: h.(*runtimeHandler).options})
	if err != nil {
		t.Fatal(err)
	}
	r := leaseModelRequest("lease-stream-replay-0001")
	binding := taskBinding(t.Context(), r)
	binding.LeaseChainDigest = tasks.grant.LeaseChainDigest
	ctx := context.WithValue(t.Context(), gatewayTaskExecutionKey{}, &gatewayTaskExecution{Binding: binding, ScopeDigest: tasks.grant.ScopeDigest})
	changeTestTaskLease(tasks)
	in := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	out := httptest.NewRecorder()
	// A held checked result must retain its original execution pin rather than
	// accepting a new grant when the gateway converts it to SSE.
	g.(*gatewayHandler).replayModelStream(out, in, &gatewayBuffer{checkedOutput: &gatewayCheckedOutput{values: ctx, request: r, decision: Decision{CatalogDigest: ConfigDigest(c)}}}, "private stream output", "stop", r.ModelID, r.RequestID)
	if out.Code != 403 || strings.Contains(out.Body.String(), "private stream output") || strings.Contains(out.Body.String(), "[DONE]") {
		t.Fatal("SSE replay adopted a new lease", out.Code, out.Body.String())
	}
}

func TestTaskLeaseCachePartitionAndFallbackKeepCapturedEpoch(t *testing.T) {
	t.Run("cache-partition", func(t *testing.T) {
		tasks := newTestTaskController()
		var hits atomic.Int32
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"checked output"},"finish_reason":"stop"}]}`)
		}))
		defer up.Close()
		c := fixture()
		c.Documents[0].Categories = nil
		c.Models[0].ResponseFormat = "openai_chat"
		h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
		o := h.(*runtimeHandler).options
		o.TaskControl, o.Cache = tasks.call, &testModelCache{data: map[ModelCacheScope][]byte{}}
		g, err := NewGateway(GatewayOptions{Runtime: o})
		if err != nil {
			t.Fatal(err)
		}
		for i, id := range []string{"lease-cache-first-0001", "lease-cache-new-epoch-0001", "lease-cache-new-epoch-0002"} {
			if i == 1 {
				changeTestTaskLease(tasks)
			}
			raw, _ := json.Marshal(wire(leaseModelRequest(id)))
			if out := gatewayRequest(g, "/v1/execute", aliceToken, string(raw), ""); out.Code != 200 {
				t.Fatal(out.Code, out.Body.String())
			}
		}
		if hits.Load() != 2 {
			t.Fatal("cache reused old epoch or missed current epoch", hits.Load())
		}
	})
	t.Run("fallback", func(t *testing.T) {
		tasks := newTestTaskController()
		var first, second atomic.Int32
		p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			first.Add(1)
			changeTestTaskLease(tasks)
			w.WriteHeader(503)
		}))
		defer p.Close()
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { second.Add(1); w.WriteHeader(500) }))
		defer s.Close()
		c := fixture()
		c.Models[0].ResponseFormat, c.Models[1].ResponseFormat = "openai_chat", "openai_chat"
		h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, p.URL, nil, nil)
		o := h.(*runtimeHandler).options
		o.TaskControl, o.Targets["othermodel"] = tasks.call, Target{URL: s.URL}
		g, err := NewGateway(GatewayOptions{Runtime: o, Fallbacks: map[string][]string{"enterprise": {"public"}}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(wire(leaseModelRequest("lease-fallback-step-0001")))
		out := gatewayRequest(g, "/v1/execute", aliceToken, string(raw), "")
		if out.Code < 400 || first.Load() != 1 || second.Load() != 0 {
			t.Fatal("fallback crossed lease epochs", out.Code, out.Body.String(), first.Load(), second.Load())
		}
	})
}

type leaseChangeOnCacheHit struct {
	testModelCache
	onHit func()
}

func (c *leaseChangeOnCacheHit) Get(ctx context.Context, scope ModelCacheScope) ([]byte, bool, error) {
	raw, hit, err := c.testModelCache.Get(ctx, scope)
	if hit && c.onHit != nil {
		c.onHit()
	}
	return raw, hit, err
}

func TestTaskLeaseChangeAfterCacheHitSuppressesStoredOutput(t *testing.T) {
	tasks := newTestTaskController()
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"private cached answer"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	c := fixture()
	c.Documents[0].Categories = nil
	c.Models[0].ResponseFormat = "openai_chat"
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	o := h.(*runtimeHandler).options
	o.TaskControl = tasks.call
	o.Cache = &leaseChangeOnCacheHit{testModelCache: testModelCache{data: map[ModelCacheScope][]byte{}}, onHit: func() { changeTestTaskLease(tasks) }}
	g, err := NewGateway(GatewayOptions{Runtime: o})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"lease-late-cache-first-0001", "lease-late-cache-second-001"} {
		raw, _ := json.Marshal(wire(leaseModelRequest(id)))
		out := gatewayRequest(g, "/v1/execute", aliceToken, string(raw), "")
		if i == 0 && out.Code != 200 || i == 1 && (out.Code < 400 || strings.Contains(out.Body.String(), "private cached answer")) {
			t.Fatal("cache hit released content after its lease changed", i, out.Code, out.Body.String(), "providerHits", hits.Load(), "cacheHeader", out.Header().Get("X-Scarlet-Cache"))
		}
	}
	if hits.Load() != 1 {
		t.Fatal("test did not exercise a stored cache hit", hits.Load())
	}
}

func TestTaskLeaseCacheControllerCarriesTrustedCallContext(t *testing.T) {
	tasks := newTestTaskController()
	c := fixture()
	c.Documents[0].Categories = nil
	c.Models[0].ResponseFormat = "openai_chat"
	r := leaseModelRequest("lease-cache-transport-0001")
	var gets, puts atomic.Int32
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, in *http.Request) {
		var scope gatewaygovernance.CacheScope
		switch in.URL.Path {
		case "/api/v1/internal/ai-gateway/cache/get":
			gets.Add(1)
			if err := json.NewDecoder(in.Body).Decode(&scope); err != nil {
				t.Error(err)
			}
		case "/api/v1/internal/ai-gateway/cache/put":
			puts.Add(1)
			var put gatewaygovernance.CachePut
			if err := json.NewDecoder(in.Body).Decode(&put); err != nil {
				t.Error(err)
			}
			scope = put.CacheScope
		default:
			t.Error("unexpected control operation", in.URL.Path)
			w.WriteHeader(404)
			return
		}
		if scope.TaskID != r.TaskID || scope.TaskStepID != r.RequestID || scope.TaskStepDigest != RequestDigest(r) || scope.LeaseChainDigest != tasks.grant.LeaseChainDigest || scope.LeaseProtocolVersion != gatewaygovernance.TaskLeaseProtocolVersion {
			t.Error("cache transport lost trusted current step/epoch", scope.TaskContext)
			w.WriteHeader(403)
			return
		}
		if strings.HasSuffix(in.URL.Path, "/get") {
			_, _ = io.WriteString(w, `{"hit":false}`)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer control.Close()
	client, err := NewGatewayController(GatewayControllerOptions{URL: control.URL, Token: strings.Repeat("x", 32), WorkspaceSlug: "workspace", Catalog: func(context.Context) (Config, error) { return c, nil }, AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"checked output"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	h, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	o := h.(*runtimeHandler).options
	o.TaskControl, o.Cache = tasks.call, client
	g, err := NewGateway(GatewayOptions{Runtime: o, BeforeExecute: func(ctx context.Context, fact GatewayAuthorization) error {
		if fact.LeaseChainDigest != tasks.grant.LeaseChainDigest {
			return errors.New("authorization epoch missing")
		}
		chain := ctx.Value(gatewayFallbackKey{}).(*gatewayFallbackChain)
		chain.PreviousReservationID = strings.Repeat("a", 32)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(wire(r))
	if out := gatewayRequest(g, "/v1/execute", aliceToken, string(raw), ""); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	if gets.Load() != 1 || puts.Load() != 1 {
		t.Fatal("cache controller transport not exercised", gets.Load(), puts.Load())
	}
}
