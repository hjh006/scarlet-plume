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

// The source changes immediately after the inner runtime's final freshness
// read. The gateway still holds the checked bytes for response conversion.
func TestGatewayReleaseKeepsGenerationSourceSnapshot(t *testing.T) {
	for _, mode := range []string{"raw", "chat", "sse"} {
		t.Run(mode, func(t *testing.T) {
			const secret = "answer derived from the original source"
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				if mode == "sse" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, `data: {"object":"chat.completion.chunk","model":"enterprise","choices":[{"index":0,"delta":{"content":"`+secret+`"},"finish_reason":null}]}`+"\n\n")
					_, _ = io.WriteString(w, `data: {"object":"chat.completion.chunk","model":"enterprise","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`+"\n\ndata: [DONE]\n\n")
					return
				}
				_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"`+secret+`"},"finish_reason":"stop"}]}`)
			}))
			defer up.Close()
			c := fixture()
			c.Models[0].ResponseFormat = "openai_chat"
			var completed, changed atomic.Bool
			docs := func(context.Context) (map[string]LocalDocument, error) {
				text := "original approved synthetic source"
				if changed.Load() {
					text = "replaced synthetic source with the same declared version"
				}
				if completed.Load() {
					changed.Store(true)
				}
				return map[string]LocalDocument{"doc": {ID: "doc", Version: "v1", ACLVersion: "acl1", Content: text}}, nil
			}
			runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, func(_ context.Context, a AuditRecord) error {

				if a.Stage == "completed" && a.Execution.Outcome == "succeeded" {
					completed.Store(true)
				}
				return nil
			}, docs)
			handler, e := NewGateway(GatewayOptions{Runtime: runtime.(*runtimeHandler).options})
			if e != nil {
				t.Fatal(e)
			}
			r := testRequest("model")
			r.ModelID = "enterprise"
			r.DocumentIDs = []string{"doc"}
			r.Input = `{"messages":[{"role":"user","content":"synthetic request"}]}`
			if mode == "sse" {
				r.Input = strings.TrimSuffix(r.Input, "}") + `,"stream":true}`
			}
			in := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			in.Header.Set("Authorization", "Bearer "+aliceToken)
			in.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			handler.(*gatewayHandler).execute(out, in, Identity{ApplicationID: "app", PrincipalID: "alice"}, wire(r), mode != "raw")
			if !completed.Load() || !changed.Load() || hits.Load() != 1 {
				t.Fatal("test did not reach the held-result release boundary", out.Code, out.Body.String(), hits.Load())
			}
			if out.Code < 400 || strings.Contains(out.Body.String(), secret) || strings.Contains(out.Body.String(), "data: [DONE]") {
				t.Fatal("gateway released old output under a replacement source snapshot", out.Code, out.Body.String())
			}
		})
	}
}

func TestGatewayReleaseRechecksAncestorLeaseAfterInnerRuntime(t *testing.T) {
	for _, chat := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw", true: "chat"}[chat], func(t *testing.T) {
			const secret = "old epoch held result"
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"`+secret+`"},"finish_reason":"stop"}]}`)
			}))
			defer up.Close()
			c := fixture()
			c.Models[0].ResponseFormat = "openai_chat"
			c.Applications[0].RequireTask = true
			tasks := newTestTaskController()
			var completed, changed atomic.Bool
			runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, func(_ context.Context, a AuditRecord) error {
				if a.Stage == "completed" && a.Execution.Outcome == "succeeded" {
					completed.Store(true)
				}
				return nil
			}, nil)
			options := runtime.(*runtimeHandler).options
			options.TaskControl = func(ctx context.Context, op TaskOperation) (gatewaygovernance.TaskGrant, error) {
				grant, err := tasks.call(ctx, op)
				if err == nil && op.Stage == "check" && completed.Load() && changed.CompareAndSwap(false, true) {
					changeTestTaskLease(tasks)
				}
				return grant, err
			}
			handler, e := NewGateway(GatewayOptions{Runtime: options})
			if e != nil {
				t.Fatal(e)
			}
			r := leaseModelRequest("release-old-epoch-0001")
			in := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			in.Header.Set("Authorization", "Bearer "+aliceToken)
			in.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			handler.(*gatewayHandler).execute(out, in, Identity{ApplicationID: "app", PrincipalID: "alice"}, wire(r), chat)
			if !completed.Load() || !changed.Load() {
				t.Fatal("test did not reach held output after the last inner check", out.Code, out.Body.String())
			}
			if out.Code < 400 || strings.Contains(out.Body.String(), secret) {
				t.Fatal("gateway released an obsolete ancestor lease result", out.Code, out.Body.String())
			}

		})
	}
}

func TestGatewayReleaseRechecksCredentialAfterCacheAndFallback(t *testing.T) {
	for _, mode := range []string{"provider", "cache-hit", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			var hits, completedChecks atomic.Int32
			var completed, revoked, armed atomic.Bool
			armed.Store(mode != "cache-hit")
			const secret = "checked result withheld after credential retirement"
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				var payload struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if mode == "fallback" && payload.Model == "enterprise" {
					w.WriteHeader(503)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"object": "chat.completion", "model": payload.Model, "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": secret}, "finish_reason": "stop"}}})
			}))
			defer up.Close()
			c := fixture()
			c.Models[0].ResponseFormat = "openai_chat"
			c.Models[1].ResponseFormat = "openai_chat"
			c.Models[1].AllowedInputCategories = append([]string{}, c.Models[0].AllowedInputCategories...)
			c.Models[1].AllowedOutputCategories = append([]string{}, c.Models[0].AllowedOutputCategories...)
			r := testRequest("model")
			r.ModelID = "enterprise"
			r.Input = `{"messages":[{"role":"user","content":"safe"}]}`
			var records []AuditRecord
			runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, func(_ context.Context, a AuditRecord) error {
				records = append(records, a)
				if armed.Load() && a.Stage == "completed" && (a.Execution.Outcome == "succeeded" || a.Execution.CacheHit) {
					completed.Store(true)
				}
				return nil
			}, nil)
			options := runtime.(*runtimeHandler).options
			options.Targets["othermodel"] = Target{URL: up.URL}
			options.Authenticate = func(context.Context, string) (Identity, error) {
				if revoked.Load() {
					return Identity{}, errors.New("credential revoked")
				}
				if completed.Load() {
					threshold := int32(1)
					if mode == "cache-hit" {
						threshold = 2
					}
					if completedChecks.Add(1) == threshold {
						revoked.Store(true)
					}
				}
				return Identity{ApplicationID: "app", PrincipalID: "alice"}, nil
			}
			if mode == "cache-hit" {
				options.Cache = &testModelCache{data: map[ModelCacheScope][]byte{}}
			}
			gatewayOptions := GatewayOptions{Runtime: options}
			if mode == "fallback" {
				gatewayOptions.Fallbacks = map[string][]string{"enterprise": {"public"}}
			}
			handler, e := NewGateway(gatewayOptions)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "cache-hit" {
				prime := r
				prime.RequestID = "release-cache-prime-0001"
				in := httptest.NewRequest("POST", "/v1/chat/completions", nil)
				in.Header.Set("Content-Type", "application/json")
				in.Header.Set("Authorization", "Bearer "+aliceToken)
				out := httptest.NewRecorder()
				handler.(*gatewayHandler).execute(out, in, Identity{ApplicationID: "app", PrincipalID: "alice"}, wire(prime), true)
				if out.Code != 200 {
					t.Fatal("cache priming", out.Code, out.Body.String())
				}
				hits.Store(0)
				records = nil
				armed.Store(true)
			}
			in := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			in.Header.Set("Authorization", "Bearer "+aliceToken)
			in.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			handler.(*gatewayHandler).execute(out, in, Identity{ApplicationID: "app", PrincipalID: "alice"}, wire(r), true)
			if !completed.Load() || !revoked.Load() || out.Code < 400 || strings.Contains(out.Body.String(), secret) {

				t.Fatal("held provider/cache result released after retirement", out.Code, out.Body.String())
			}
			wantHits := int32(1)
			if mode == "cache-hit" {
				wantHits = 0
			}
			if mode == "fallback" {
				wantHits = 2
			}
			if hits.Load() != wantHits {
				t.Fatal("unexpected extra provider attempt", hits.Load(), wantHits)
			}
			success, failed, cacheHits := 0, 0, 0
			for _, a := range records {
				if a.Stage != "completed" {
					continue
				}
				if a.Execution.Attempted && a.Execution.Outcome == "succeeded" {
					success++
				}
				if a.Execution.Attempted && a.Execution.Outcome == "failed" {
					failed++
				}
				if a.Execution.CacheHit {
					cacheHits++
				}
			}
			if mode == "cache-hit" {
				if success != 0 || failed != 0 || cacheHits != 1 {
					t.Fatal("cache receipt changed into provider execution")
				}
			} else if success != 1 || mode == "fallback" && failed != 1 {
				t.Fatal("output suppression erased prior execution/unknown charge facts", success, failed)
			}
		})
	}
}

func TestGatewayReleaseMediaInputIsNotInspectedAgainForSSE(t *testing.T) {
	var scans atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseCompletion("enterprise", []string{"approved checked text"}, "data: [DONE]\n\n"))
	}))
	defer up.Close()
	c := mediaOutputConfig()
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	options := runtime.(*runtimeHandler).options
	options.MediaProcessor = func(_ context.Context, a MediaAsset) (MediaReport, error) {
		scans.Add(1)
		return MediaReport{Digest: a.Digest, MIMEType: a.MIMEType, InspectionVersion: MediaInspectionVersion, Complete: true, Confidence: 100, Text: "synthetic clean image"}, nil
	}
	handler, e := NewGateway(GatewayOptions{Runtime: options})
	if e != nil {
		t.Fatal(e)
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(mediaInput(testPNG(), "image/png")), &payload)
	payload["model"] = "enterprise"
	payload["stream"] = true
	raw, _ := json.Marshal(payload)
	out := gatewayRequest(handler, "/v1/chat/completions", aliceToken, string(raw), "release-media-sse-0001")
	if out.Code != 200 || !strings.Contains(out.Body.String(), "approved checked text") || !strings.Contains(out.Body.String(), "data: [DONE]") {
		t.Fatal("valid checked media stream failed", out.Code, out.Body.String())
	}
	if scans.Load() != 1 {
		t.Fatal("replay obtained new recognition instead of retaining the generated request's media evidence", scans.Load())
	}
}

type releaseHookWriter struct {
	*httptest.ResponseRecorder
	afterFlush func()
}

func (w *releaseHookWriter) Flush() {
	w.ResponseRecorder.Flush()
	if w.afterFlush != nil {
		w.afterFlush()
	}
}
func TestGatewayReleaseSSERechecksBetweenEveryReleasedChunk(t *testing.T) {
	var revoked atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseCompletion("enterprise", []string{"content must stay withheld"}, "data: [DONE]\n\n"))
	}))
	defer up.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	options := runtime.(*runtimeHandler).options
	options.Authenticate = func(context.Context, string) (Identity, error) {
		if revoked.Load() {
			return Identity{}, errors.New("revoked")
		}
		return Identity{ApplicationID: "app", PrincipalID: "alice"}, nil
	}
	handler, e := NewGateway(GatewayOptions{Runtime: options})
	if e != nil {
		t.Fatal(e)
	}
	in := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"enterprise","messages":[{"role":"user","content":"safe"}],"stream":true}`))
	in.Header.Set("Content-Type", "application/json")
	in.Header.Set("Authorization", "Bearer "+aliceToken)
	out := &releaseHookWriter{ResponseRecorder: httptest.NewRecorder(), afterFlush: func() { revoked.Store(true) }}
	handler.ServeHTTP(out, in)
	if out.Code != 200 || !revoked.Load() || !strings.Contains(out.Body.String(), `"role":"assistant"`) || strings.Contains(out.Body.String(), "content must stay withheld") || strings.Contains(out.Body.String(), "[DONE]") {
		t.Fatal("later stream frame bypassed the original release snapshot", out.Code, out.Body.String())
	}
}

func TestGatewayReleaseKeepsTrustedExecutionValuesAndOuterCancellation(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"checked"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	c.Applications[0].RequireTask = true
	tasks := newTestTaskController()
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	var revoked atomic.Bool
	var cancelDuringCheck context.CancelFunc
	options := runtime.(*runtimeHandler).options
	options.TaskControl = tasks.call
	options.Authenticate = func(_ context.Context, hash string) (Identity, error) {
		if cancelDuringCheck != nil {
			cancelDuringCheck()
		}
		if hash != tokenHash(aliceToken) || revoked.Load() {
			return Identity{}, errors.New("original credential required")
		}
		return Identity{ApplicationID: "app", PrincipalID: "alice"}, nil
	}
	handler, e := NewGateway(GatewayOptions{Runtime: options})
	if e != nil {
		t.Fatal(e)
	}
	gateway := handler.(*gatewayHandler)
	in := httptest.NewRequest("POST", "/v1/execute", nil)
	in.Header.Set("Authorization", "Bearer "+aliceToken)
	in.Header.Set("Content-Type", "application/json")
	b := &gatewayBuffer{header: http.Header{}}
	r := leaseModelRequest("release-values-0001")
	gateway.executeAttempt(b, in, Identity{ApplicationID: "app", PrincipalID: "alice"}, wire(r), c)
	if b.status != 200 || b.checkedOutput == nil {
		t.Fatal("actual trusted snapshot not captured", b.status, b.body.String())
	}
	// This is the MCP wrapper's outer context: it has no internal credential hash,
	// attempt ID, lease pointer or runtime watch. A normal completed result still
	// uses the original values after the inner runtime's watch has been stopped.
	if !gateway.checkedOutputCurrent(t.Context(), b) {
		t.Fatal("normal inner watch cancellation or missing outer values rejected checked output")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if gateway.checkedOutputCurrent(cancelled, b) {
		t.Fatal("outer cancellation ignored")
	}
	expired, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	if gateway.checkedOutputCurrent(expired, b) {
		t.Fatal("outer deadline ignored")
	}
	during, stopDuring := context.WithCancel(t.Context())
	cancelDuringCheck = stopDuring
	if gateway.checkedOutputCurrent(during, b) {
		t.Fatal("outer cancellation during authorization check ignored")
	}
	cancelDuringCheck = nil
	revoked.Store(true)
	if gateway.checkedOutputCurrent(t.Context(), b) {
		t.Fatal("original execution credential revocation ignored")
	}
	revoked.Store(false)
	changeTestTaskLease(tasks)
	if gateway.checkedOutputCurrent(t.Context(), b) {
		t.Fatal("original ancestor execution lease replaced by wrapper values")
	}
}
