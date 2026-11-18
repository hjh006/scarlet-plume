package aisecurity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func sseCompletion(model string, parts []string, tail string) string {
	var out strings.Builder
	for i, text := range parts {
		fmt.Fprintf(&out, "data: {\"id\":\"chunk-id\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", model, text)
		_ = i
	}
	fmt.Fprintf(&out, "data: {\"id\":\"chunk-id\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"chunk-id\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n", model, model)
	out.WriteString(tail)
	return out.String()
}

func TestStrictHoldStreamUsesActualWireByteBudget(t *testing.T) {
	const limit = 1024
	const wireBudget = limit*8 + 65536
	model := Model{ID: "enterprise"}
	completion := strings.ReplaceAll(sseCompletion(model.ID, []string{"ordinary reply"}, "data: [DONE]\n\n"), "\n", "\r\n")
	padding := wireBudget - len(completion)
	completion += strings.Repeat("\r\n", padding/2) + strings.Repeat("\n", padding%2)
	for _, suffix := range []string{"", "\r\n"} {
		raw, err := assembleModelStream(strings.NewReader(completion+suffix), model, limit, false, func() bool { return true })
		if suffix == "" {
			if err != nil || !bytes.Contains(raw, []byte("ordinary reply")) {
				t.Fatalf("complete stream within the budget: %v", err)
			}
		} else if err == nil || len(raw) != 0 {
			t.Fatal("stream exceeding the wire budget returned a completed response")
		}
	}
}

func TestStrictHoldSSEChecksCompleteDecodedTextAndFailsClosed(t *testing.T) {
	cases := map[string]string{
		"safe":            sseCompletion("enterprise", []string{"approved ", "answer"}, "data: [DONE]\n\n"),
		"cross_chunk_pii": sseCompletion("enterprise", []string{"person@", "example.com"}, "data: [DONE]\n\n"),
		"escaped_pattern": strings.Replace(sseCompletion("enterprise", []string{"private ", "instruction"}, "data: [DONE]\n\n"), `private `, `\u0070rivate `, 1),
		"eof":             sseCompletion("enterprise", []string{"approved"}, ""),
		"model":           sseCompletion("wrong-model", []string{"approved"}, "data: [DONE]\n\n"),
		"tool":            strings.Replace(sseCompletion("enterprise", []string{"approved"}, "data: [DONE]\n\n"), `"delta":{"content":"approved"}`, `"delta":{"tool_calls":[{"function":{"name":"delete_all"}}]}`, 1),
		"usage":           strings.Replace(sseCompletion("enterprise", []string{"approved"}, "data: [DONE]\n\n"), `"total_tokens":3`, `"total_tokens":-1`, 1),
		"trailing":        sseCompletion("enterprise", []string{"approved"}, "data: [DONE]\n\ndata: {}\n\n"),
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(raw), `"stream":true`) || !strings.Contains(string(raw), `"include_usage":true`) {
					t.Error("not an actual upstream stream", string(raw))
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, response)
			}))
			defer up.Close()
			c := fixture()
			c.Models[0].ResponseFormat = "openai_chat"
			c.Policy.BlockedOutputPatterns = []string{"private instruction"}
			rt, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
			g, err := NewGateway(GatewayOptions{Runtime: rt.(*runtimeHandler).options})
			if err != nil {
				t.Fatal(err)
			}
			out := gatewayRequest(g, "/v1/chat/completions", aliceToken, `{"model":"enterprise","messages":[{"role":"user","content":"request"}],"stream":true}`, "")
			if name == "safe" {
				if out.Code != 200 || out.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(out.Body.String(), "data: [DONE]") || !strings.Contains(out.Body.String(), "approved answer") {
					t.Fatal(out.Code, out.Body.String())
				}
			} else if out.Code < 400 || strings.Contains(out.Body.String(), "[DONE]") || strings.Contains(out.Body.String(), "person@") {
				t.Fatal(out.Code, out.Body.String())
			}
		})
	}
}

type testModelCache struct {
	mu   sync.Mutex
	data map[ModelCacheScope][]byte
}

func (c *testModelCache) Get(_ context.Context, s ModelCacheScope) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.data[s]
	return append([]byte(nil), v...), ok, nil
}
func (c *testModelCache) Put(_ context.Context, s ModelCacheScope, v []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[s] = append([]byte(nil), v...)
	return nil
}
func TestCacheHitsStillConsumeNonceInspectOutputAndReauthorize(t *testing.T) {
	var hits, admissions atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"approved"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	rt, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	o := rt.(*runtimeHandler).options
	o.Cache = &testModelCache{data: map[ModelCacheScope][]byte{}}
	var enabled atomic.Bool
	enabled.Store(true)
	o.Authenticate = func(context.Context, string) (Identity, error) {
		if !enabled.Load() {
			return Identity{}, fmt.Errorf("revoked")
		}
		return Identity{ApplicationID: "app", PrincipalID: "alice"}, nil
	}
	g, err := NewGateway(GatewayOptions{Runtime: o, BeforeExecute: func(context.Context, GatewayAuthorization) error { admissions.Add(1); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"enterprise","messages":[{"role":"user","content":"request"}]}`
	for _, id := range []string{"cache-request-first-000", "cache-request-second-00"} {
		out := gatewayRequest(g, "/v1/chat/completions", aliceToken, body, id)
		if out.Code != 200 {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	if hits.Load() != 1 || admissions.Load() != 1 {
		t.Fatal("cache bypassed or repeated provider", hits.Load(), admissions.Load())
	}
	if out := gatewayRequest(g, "/v1/chat/completions", aliceToken, body, "cache-request-second-00"); out.Code != 409 {
		t.Fatal("cache nonce replay", out.Code)
	}
	cache := o.Cache.(*testModelCache)
	cache.mu.Lock()
	for key := range cache.data {
		cache.data[key] = []byte(`{"model":"enterprise","output":"person@example.com","finishReason":"stop"}`)
	}
	cache.mu.Unlock()
	if out := gatewayRequest(g, "/v1/chat/completions", aliceToken, body, "cache-poison-request-00"); out.Code != 403 || strings.Contains(out.Body.String(), "person@") {
		t.Fatal("unchecked cached output", out.Code, out.Body.String())
	}
	enabled.Store(false)
	if out := gatewayRequest(g, "/v1/chat/completions", aliceToken, body, "cache-revoked-request-00"); out.Code != 401 {
		t.Fatal("revoked cache released", out.Code)
	}
}
func TestExplicitFallbackReevaluatesEveryActualProviderAndNeverPolicyRetries(t *testing.T) {
	for _, mode := range []string{"transient", "region", "output_policy"} {
		t.Run(mode, func(t *testing.T) {
			var primary, secondary, admissions atomic.Int32
			events := []AuditRecord{}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primary.Add(1)
				if mode != "output_policy" {
					w.WriteHeader(503)
					io.WriteString(w, `{"error":"unavailable"}`)
					return
				}
				io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"person@example.com"},"finish_reason":"stop"}]}`)
			}))
			defer up.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondary.Add(1)
				io.WriteString(w, `{"object":"chat.completion","model":"backup-model","choices":[{"index":0,"message":{"role":"assistant","content":"backup approved"},"finish_reason":"stop"}]}`)
			}))
			defer backup.Close()
			c := fixture()
			c.Models[0].ResponseFormat = "openai_chat"
			m := c.Models[0]
			m.ID = "backup-model"
			m.EndpointRef = "backup"
			m.Provider = "alternate"
			if mode == "region" {
				m.Region = "US"
			}
			c.Models = append(c.Models, m)
			c.Applications[0].ModelIDs = append(c.Applications[0].ModelIDs, m.ID)
			rt, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, func(_ context.Context, a AuditRecord) error { events = append(events, a); return nil }, nil)
			o := rt.(*runtimeHandler).options
			o.Targets["backup"] = Target{URL: backup.URL}
			g, err := NewGateway(GatewayOptions{Runtime: o, Fallbacks: map[string][]string{"enterprise": {"backup-model"}}, BeforeExecute: func(_ context.Context, f GatewayAuthorization) error { admissions.Add(1); return nil }})
			if err != nil {
				t.Fatal(err)
			}
			out := gatewayRequest(g, "/v1/chat/completions", aliceToken, `{"model":"enterprise","messages":[{"role":"user","content":"request"}]}`, "")
			if mode == "transient" {
				if out.Code != 200 || secondary.Load() != 1 || admissions.Load() != 2 || len(events) != 6 {
					t.Fatal(out.Code, out.Body.String(), secondary.Load(), admissions.Load(), len(events))
				}
			} else if out.Code < 400 || secondary.Load() != 0 || admissions.Load() != 1 {
				t.Fatal("unsafe fallback", out.Code, secondary.Load(), admissions.Load())
			}
		})
	}
}

type cancellingWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *cancellingWriter) Write(raw []byte) (int, error) {
	n, e := w.ResponseRecorder.Write(raw)
	w.cancel()
	return n, e
}
func TestStrictHoldStreamDisconnectNeverEmitsDone(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sseCompletion("enterprise", []string{"approved answer"}, "data: [DONE]\n\n"))
	}))
	defer up.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	rt, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	g, err := NewGateway(GatewayOptions{Runtime: rt.(*runtimeHandler).options})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"enterprise","messages":[{"role":"user","content":"request"}],"stream":true}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	out := &cancellingWriter{httptest.NewRecorder(), cancel}
	g.ServeHTTP(out, req)
	if out.Code != 200 || strings.Contains(out.Body.String(), "[DONE]") || strings.Contains(out.Body.String(), "approved answer") {
		t.Fatal(out.Code, out.Body.String())
	}
}
func TestStrictHoldRevocationDuringUpstreamSuppressesAllChunks(t *testing.T) {
	var revoked atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		revoked.Store(true)
		io.WriteString(w, sseCompletion("enterprise", []string{"approved answer"}, "data: [DONE]\n\n"))
	}))
	defer up.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	rt, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	o := rt.(*runtimeHandler).options
	o.Authenticate = func(context.Context, string) (Identity, error) {
		if revoked.Load() {
			return Identity{}, fmt.Errorf("revoked")
		}
		return Identity{ApplicationID: "app", PrincipalID: "alice"}, nil
	}
	g, err := NewGateway(GatewayOptions{Runtime: o})
	if err != nil {
		t.Fatal(err)
	}
	out := gatewayRequest(g, "/v1/chat/completions", aliceToken, `{"model":"enterprise","messages":[{"role":"user","content":"request"}],"stream":true}`, "")
	if out.Code < 400 || strings.Contains(out.Body.String(), "[DONE]") || strings.Contains(out.Body.String(), "approved answer") {
		t.Fatal(out.Code, out.Body.String())
	}
}

func TestGatewayReleasesCheckedOutputLargerThanOneMiB(t *testing.T) {
	imageData := image.NewNRGBA(image.Rect(0, 0, 700, 700))
	random := rand.New(rand.NewSource(1))
	for i := 0; i < len(imageData.Pix); i += 4 {
		imageData.Pix[i] = byte(random.Intn(256))
		imageData.Pix[i+1] = byte(random.Intn(256))
		imageData.Pix[i+2] = byte(random.Intn(256))
		imageData.Pix[i+3] = 255
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, imageData); err != nil {
		t.Fatal(err)
	}
	if pngData.Len() <= 1<<20 {
		t.Fatal("fixture must exceed old buffer ceiling", pngData.Len())
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData.Bytes())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "chat.completion", "model": "enterprise", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURI}}}}, "finish_reason": "stop"}}})
	}))
	defer up.Close()
	c := fixture()
	c.Models[0].ResponseFormat = "openai_chat"
	c.Models[0].Media = testMediaPolicy()
	c.Models[0].Media.MaxAssetBytes = 2 << 20
	c.Models[0].Media.MaxTotalBytes = 4 << 20
	c.Policy.MaxOutputBytes = 4 << 20
	for _, categories := range []*[]string{&c.Policy.AllowedInputCategories, &c.Policy.AllowedOutputCategories, &c.Models[0].AllowedInputCategories, &c.Models[0].AllowedOutputCategories} {
		*categories = append(*categories, "image")
	}
	rt, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	options := rt.(*runtimeHandler).options
	options.MediaProcessor = fakeMedia("approved pixels", 99)
	g, err := NewGateway(GatewayOptions{Runtime: options})
	if err != nil {
		t.Fatal(err)
	}
	out := gatewayRequest(g, "/v1/chat/completions", aliceToken, `{"model":"enterprise","messages":[{"role":"user","content":"request"}]}`, "")
	var response struct {
		Choices []struct {
			Message struct {
				Content []struct {
					Type     string `json:"type"`
					ImageURL *struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &response) != nil || len(response.Choices) != 1 {
		t.Fatalf("large checked output truncated: status=%d bytes=%d", out.Code, out.Body.Len())
	}
	received := ""
	for _, part := range response.Choices[0].Message.Content {
		if part.Type == "image_url" && part.ImageURL != nil {
			received = part.ImageURL.URL
		}
	}
	if received != dataURI {
		t.Fatalf("checked PNG changed or missing: expected bytes=%d received bytes=%d", len(dataURI), len(received))
	}
}

func TestPublicChatForwardsTypedAudioAndStreamOptions(t *testing.T) {
	for _, mode := range []string{"audio", "stream-options"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				var payload struct {
					Model      string   `json:"model"`
					Modalities []string `json:"modalities"`
					Audio      *struct {
						Voice  string `json:"voice"`
						Format string `json:"format"`
					} `json:"audio"`
					Stream        bool `json:"stream"`
					StreamOptions *struct {
						IncludeUsage bool `json:"include_usage"`
					} `json:"stream_options"`
				}
				if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Model != "actual-upstream" {
					t.Error("upstream selector or payload changed")
				}
				if mode == "audio" {
					if len(payload.Modalities) != 2 || payload.Audio == nil || payload.Audio.Voice != "alloy" || payload.Audio.Format != "wav" {
						t.Errorf("audio request options lost: %+v", payload)
					}
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"object":"chat.completion","model":"actual-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"approved answer"},"finish_reason":"stop"}]}`)
					return
				}
				if !payload.Stream || payload.StreamOptions == nil || !payload.StreamOptions.IncludeUsage {
					t.Error("stream usage options lost")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sseCompletion("actual-upstream", []string{"approved answer"}, "data: [DONE]\n\n"))
			}))
			defer up.Close()
			c := fixture()
			c.Models[0].ResponseFormat = "openai_chat"
			c.Models[0].UpstreamModel = "actual-upstream"
			c.Models[0].Media = testMediaPolicy()
			rt, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
			g, err := NewGateway(GatewayOptions{Runtime: rt.(*runtimeHandler).options})
			if err != nil {
				t.Fatal(err)
			}
			extra := `,"modalities":["text","audio"],"audio":{"voice":"alloy","format":"wav"}`
			if mode == "stream-options" {
				extra = `,"stream":true,"stream_options":{"include_usage":false}`
			}
			out := gatewayRequest(g, "/v1/chat/completions", aliceToken, `{"model":"enterprise","messages":[{"role":"user","content":"request"}]`+extra+`}`, "")
			if out.Code != 200 || hits.Load() != 1 {
				t.Fatal(out.Code, out.Body.String(), hits.Load())
			}
			if mode == "stream-options" && !strings.Contains(out.Body.String(), "data: [DONE]") {
				t.Fatal("complete SSE missing")
			}
		})
	}
}
