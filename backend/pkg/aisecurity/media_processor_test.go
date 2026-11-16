package aisecurity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMediaPrivateHTTPScannerBindingAndActualOCRGateway(t *testing.T) {
	binary, err := exec.LookPath("tesseract")
	if err != nil {
		t.Skip("local OCR binary unavailable")
	}
	processor, err := NewCommandMediaProcessor(CommandMediaOptions{Tesseract: binary})
	if err != nil {
		t.Fatal(err)
	}
	const token = "synthetic-media-inspector-token-000001"
	handler, err := NewMediaInspectionHandler(processor, token)
	if err != nil {
		t.Fatal(err)
	}
	scanner := httptest.NewServer(handler)
	defer scanner.Close()
	client, err := NewHTTPMediaProcessor(scanner.URL+"/v1/inspect", token, true)
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile("testdata/media-email.png")
	if err != nil {
		t.Fatal(err)
	}
	asset, err := canonicalMedia("image/png", image, testMediaPolicy())
	if err != nil {
		t.Fatal(err)
	}
	report, err := client(t.Context(), asset)
	if err != nil || !strings.Contains(report.Text, "person@example.com") {
		t.Fatal("actual OCR/HTTP binding failed", report, err)
	}
	wrong, _ := NewHTTPMediaProcessor(scanner.URL+"/v1/inspect", strings.Repeat("x", 40), true)
	if _, err := wrong(t.Context(), asset); err == nil {
		t.Fatal("scanner accepted wrong credential")
	}
	bad := asset
	bad.Digest = strings.Repeat("a", 64)
	if _, err := client(t.Context(), bad); err == nil {
		t.Fatal("scanner accepted changed bytes")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client(cancelled, asset); err == nil {
		t.Fatal("cancelled inspection continued")
	}
	if _, err := NewHTTPMediaProcessor(scanner.URL+"/v1/inspect", token, false); err == nil {
		t.Fatal("plaintext production scanner accepted")
	}
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{"object": "chat.completion", "model": "enterprise", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(asset.Data)}}}}, "finish_reason": "stop"}}}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer upstream.Close()
	c := fixture()
	c.Models[0].Media = testMediaPolicy()
	c.Models[0].ResponseFormat = "openai_chat"
	for _, categories := range []*[]string{&c.Policy.AllowedInputCategories, &c.Policy.AllowedOutputCategories, &c.Models[0].AllowedInputCategories, &c.Models[0].AllowedOutputCategories} {
		*categories = append(*categories, "image")
	}
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, upstream.URL, nil, nil)
	options := runtime.(*runtimeHandler).options
	options.MediaProcessor = client
	gateway, err := NewGateway(GatewayOptions{Runtime: options})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(mediaInput(asset.Data, "image/png")), &body)
	body["model"] = "enterprise"
	body["stream"] = true
	raw, _ := json.Marshal(body)
	response := gatewayRequest(gateway, "/v1/chat/completions", aliceToken, string(raw), "")
	if response.Code != 403 || hits.Load() != 0 || strings.Contains(response.Body.String(), "person@example.com") {
		t.Fatal("image input PII reached provider/stream", response.Code, hits.Load())
	}
	response = gatewayRequest(gateway, "/v1/chat/completions", aliceToken, `{"model":"enterprise","messages":[{"role":"user","content":"safe"}]}`, "")
	if response.Code != 403 || hits.Load() != 1 || strings.Contains(response.Body.String(), base64.StdEncoding.EncodeToString(asset.Data)) {
		t.Fatal("image output PII released", response.Code, hits.Load(), response.Body.String())
	}
}

func TestCommandMediaProcessorActualASR(t *testing.T) {
	model, wav := os.Getenv("PLUME_MEDIA_ASR_TEST_MODEL"), os.Getenv("PLUME_MEDIA_ASR_TEST_WAV")
	if model == "" || wav == "" {
		t.Skip("explicit local speech model and synthetic WAV fixture required")
	}
	binary, err := exec.LookPath("whisper-cli")
	if err != nil {
		t.Fatal(err)
	}
	processor, err := NewCommandMediaProcessor(CommandMediaOptions{Whisper: binary, WhisperModel: model, WhisperModelSHA256: os.Getenv("PLUME_MEDIA_ASR_TEST_MODEL_SHA256")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := canonicalMedia("audio/wav", raw, testMediaPolicy())
	if err != nil {
		t.Fatal(err)
	}
	report, err := processor(t.Context(), asset)
	if err != nil || !report.Complete || !strings.Contains(strings.ToLower(report.Text), "confidential") || report.Digest != asset.Digest {
		t.Fatal("actual synthetic speech recognition failed", report, err)
	}
	payload, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_audio", "input_audio": map[string]string{"data": base64.StdEncoding.EncodeToString(asset.Data), "format": "wav"}}}}}})
	c := fixture()
	c.Models[0].Media = testMediaPolicy()
	c.Models[0].ResponseFormat = "openai_chat"
	c.Policy.MaxInputBytes = 2 << 20
	c.Policy.BlockedInputPatterns = []string{"(?i)confidential"}
	for _, categories := range []*[]string{&c.Policy.AllowedInputCategories, &c.Policy.AllowedOutputCategories, &c.Models[0].AllowedInputCategories, &c.Models[0].AllowedOutputCategories} {
		*categories = append(*categories, "audio", "unclassified_media")
	}
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = io.WriteString(w, `{}`) }))
	defer upstream.Close()
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, upstream.URL, nil, nil)
	options := runtime.(*runtimeHandler).options
	options.MediaProcessor = processor
	handler, err := NewRuntime(options)
	if err != nil {
		t.Fatal(err)
	}
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = string(payload)
	response := callRuntime(t, handler, aliceToken, wire(r))
	if response.Code != 403 || hits.Load() != 0 {
		t.Fatal("spoken blocked pattern reached provider", response.Code, response.Body.String(), hits.Load())
	}
	bad := CommandMediaOptions{Whisper: binary, WhisperModel: model, WhisperModelSHA256: strings.Repeat("0", 64)}
	if _, err := NewCommandMediaProcessor(bad); err == nil {
		t.Fatal("unpinned speech model accepted")
	}
}
