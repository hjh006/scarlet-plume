package aisecurity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mediaOutputFixture(t *testing.T, model, mime string) ([]byte, MediaAsset) {
	t.Helper()
	data := testPNG()
	if mime == "image/jpeg" {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, image.NewRGBA(image.Rect(0, 0, 16, 16)), nil); err != nil {
			t.Fatal(err)
		}
		data = out.Bytes()
	} else if mime == "audio/wav" {
		var out bytes.Buffer
		out.WriteString("RIFF")
		_ = binary.Write(&out, binary.LittleEndian, uint32(36+320))
		out.WriteString("WAVEfmt ")
		_ = binary.Write(&out, binary.LittleEndian, uint32(16))
		for _, value := range []any{uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
			_ = binary.Write(&out, binary.LittleEndian, value)
		}
		out.WriteString("data")
		_ = binary.Write(&out, binary.LittleEndian, uint32(320))
		out.Write(make([]byte, 320))
		data = out.Bytes()
	}
	asset, err := canonicalMedia(mime, data, testMediaPolicy())
	if err != nil {
		t.Fatal(err)
	}
	message := map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}}}}
	if mime == "audio/wav" {
		message = map[string]any{"role": "assistant", "content": "approved text", "audio": map[string]any{"id": "provider-audio", "data": base64.StdEncoding.EncodeToString(data), "transcript": "provider transcript"}}
	}
	raw, err := json.Marshal(map[string]any{"object": "chat.completion", "model": model, "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": message}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw, asset
}

func mediaOutputConfig() Config {
	c := fixture()
	c.Policy.AllowedInputCategories = append(c.Policy.AllowedInputCategories, "image", "audio")
	c.Policy.AllowedOutputCategories = append(c.Policy.AllowedOutputCategories, "image", "audio")
	for i := range c.Models {
		c.Models[i].Media = testMediaPolicy()
		c.Models[i].ResponseFormat = "openai_chat"
		// Both explicitly approved destinations accept the fixture's inherited
		// confidential label; email remains denied at both destinations.
		c.Models[i].AllowedInputCategories = unique(append(c.Models[i].AllowedInputCategories, "confidential", "image", "audio"))
		c.Models[i].AllowedOutputCategories = unique(append(c.Models[i].AllowedOutputCategories, "confidential", "image", "audio"))
	}
	return c
}

func TestMediaOutputNormalizationNeverDiscardsAnInspectionReport(t *testing.T) {
	for _, mime := range []string{"image/png", "image/jpeg", "audio/wav"} {
		t.Run(mime, func(t *testing.T) {
			c := mediaOutputConfig()
			raw, asset := mediaOutputFixture(t, "enterprise", mime)
			var scans int
			processor := func(_ context.Context, observed MediaAsset) (MediaReport, error) {
				scans++
				if observed.Digest != asset.Digest || observed.MIMEType != asset.MIMEType || !bytes.Equal(observed.Data, asset.Data) {
					t.Fatal("recognition did not use the exact canonical output bytes")
				}
				text := "person@example.com"
				if scans > 1 {
					text = "later clean recognition"
				}
				return MediaReport{Digest: observed.Digest, MIMEType: observed.MIMEType, InspectionVersion: MediaInspectionVersion, Complete: true, Confidence: 99, Text: text}, nil
			}
			normalized, err := normalizeModelOutputWithMedia(t.Context(), raw, c.Models[0], processor)
			if err != nil || scans != 0 {
				t.Fatalf("normalization performed or lost an inspection: scans=%d err=%v", scans, err)
			}
			r := testRequest("model")
			r.ModelID, r.Input, r.Output = "enterprise", `{"prompt":"safe"}`, string(normalized)
			r, err = ApplyModelOutputInspection(t.Context(), r, normalized, c.Models[0], processor)
			if err != nil || scans != 1 || r.mediaOutputProjection == nil || !strings.Contains(*r.mediaOutputProjection, "person@example.com") {
				t.Fatalf("projection did not retain the unique recognition report: scans=%d err=%v", scans, err)
			}
			d := Evaluate(c, r, time.Now())
			if d.Outcome != "deny" || !slices.Contains(d.Categories, "email") || !slices.Equal(d.MediaDigests, []string{asset.Digest}) || d.MediaInspectionVersion != MediaInspectionVersion {
				t.Fatal("report categories, digest or detector binding were lost", d)
			}
		})
	}
}

func TestMediaOutputSingleInspectionAcrossExecutionPaths(t *testing.T) {
	for _, path := range []string{"runtime", "gateway-execute", "gateway-chat", "http-inspector", "fallback", "task-fallback", "bifrost-envelope"} {
		for _, report := range []string{"safe", "first-positive-second-clean", "error", "partial", "digest", "mime", "version", "confidence"} {
			t.Run(path+"/"+report, func(t *testing.T) {
				c := mediaOutputConfig()
				model := "enterprise"
				fallback := path == "fallback" || path == "task-fallback"
				if fallback {
					model = "public"
				}
				raw, asset := mediaOutputFixture(t, model, "image/png")
				if path == "bifrost-envelope" {
					var body map[string]any
					_ = json.Unmarshal(raw, &body)
					body["extra_fields"] = map[string]string{"untrusted": "transport-metadata"}
					raw, _ = json.Marshal(body)
				}
				var hits, primaryHits, scans atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					hits.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(raw)
				}))
				defer up.Close()
				target := up.URL
				if fallback {
					primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { primaryHits.Add(1); w.WriteHeader(503) }))
					defer primary.Close()
					target = primary.URL
				}
				var audits []AuditRecord
				rt, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, target, func(_ context.Context, record AuditRecord) error { audits = append(audits, record); return nil }, nil)
				o := rt.(*runtimeHandler).options
				o.MediaProcessor = func(_ context.Context, observed MediaAsset) (MediaReport, error) {
					n := scans.Add(1)
					if observed.Digest != asset.Digest || mediaHash(observed.Data) != asset.Digest || observed.MIMEType != asset.MIMEType {
						t.Error("scanner received unbound output bytes")
					}
					out := MediaReport{Digest: observed.Digest, MIMEType: observed.MIMEType, InspectionVersion: MediaInspectionVersion, Complete: true, Confidence: 99, Text: "approved recognition"}
					switch report {
					case "first-positive-second-clean":
						if n == 1 {
							out.Text = "person@example.com"
						}
					case "error":
						return out, errors.New("scanner unavailable")
					case "partial":
						out.Complete = false
					case "digest":
						out.Digest = strings.Repeat("a", 64)
					case "mime":
						out.MIMEType = "audio/wav"
					case "version":
						out.InspectionVersion = "unrecognized-version"
					case "confidence":
						out.Confidence = math.NaN()
					}
					return out, nil
				}
				if path == "http-inspector" {
					const token = "synthetic-media-inspection-receipt-token-01"
					inspection, err := NewMediaInspectionHandler(o.MediaProcessor, token)
					if err != nil {
						t.Fatal(err)
					}
					scanner := httptest.NewServer(inspection)
					defer scanner.Close()
					o.MediaProcessor, err = NewHTTPMediaProcessor(scanner.URL+"/v1/inspect", token, true)
					if err != nil {
						t.Fatal(err)
					}
				}
				cache := &countingEnterpriseCache{cache: testModelCache{data: map[ModelCacheScope][]byte{}}}
				o.Cache = cache
				options := GatewayOptions{Runtime: o}
				if fallback {
					o.Targets["othermodel"] = Target{URL: up.URL}
					options.Fallbacks = map[string][]string{"enterprise": {"public"}}
				}
				if path == "task-fallback" {
					c.Applications[0].RequireTask = true
					o.TaskControl = newTestTaskController().call
				}
				if path == "bifrost-envelope" {
					u, _ := url.Parse(up.URL)
					port, _ := strconv.Atoi(u.Port())
					profile := BifrostProfile{Provider: "openai", Region: "CN", EndpointRef: "env:SCARLET_GATEWAY_ENDPOINT", BaseURL: "https://provider.example/v1", APIKeyEnv: "SCARLET_GATEWAY_PROVIDER_KEY", Models: []string{"enterprise", "public"}, Port: port}
					for i := range c.Models {
						c.Models[i].Provider = profile.Provider
						o.Targets[c.Models[i].EndpointRef] = Target{URL: BifrostRuntimeURL(profile)}
					}
					options.Bifrost = &profile
				}
				options.Runtime = o
				r := testRequest("model")
				r.ModelID, r.Input = "enterprise", `{"prompt":"safe"}`
				if path == "task-fallback" {
					r.TaskID = "task-one"
				}
				var response *httptest.ResponseRecorder
				if path == "runtime" {
					handler, err := NewRuntime(o)
					if err != nil {
						t.Fatal(err)
					}
					response = callRuntime(t, handler, aliceToken, wire(r))
				} else {
					handler, err := NewGateway(options)
					if err != nil {
						t.Fatal(err)
					}
					endpoint := "/v1/execute"
					body, _ := json.Marshal(wire(r))
					if path == "gateway-chat" || path == "bifrost-envelope" {
						endpoint, body = "/v1/chat/completions", []byte(`{"model":"enterprise","messages":[{"role":"user","content":"safe"}]}`)
					}
					response = gatewayRequest(handler, endpoint, aliceToken, string(body), "")
				}
				if scans.Load() != 1 || hits.Load() != 1 || fallback && primaryHits.Load() != 1 || cache.gets.Load() != 0 || cache.puts.Load() != 0 {
					t.Fatalf("unexpected recognition, attempt or media-cache traversal: scans=%d provider=%d primary=%d cache=%d/%d HTTP=%d %s", scans.Load(), hits.Load(), primaryHits.Load(), cache.gets.Load(), cache.puts.Load(), response.Code, response.Body.String())
				}
				if report == "safe" {
					if response.Code != 200 || !strings.Contains(response.Body.String(), base64.StdEncoding.EncodeToString(asset.Data)) {
						t.Fatal("checked output missing", response.Code, response.Body.String())
					}
					last := audits[len(audits)-1].Decision
					if !slices.Equal(last.MediaDigests, []string{asset.Digest}) || last.MediaInspectionVersion != MediaInspectionVersion || !slices.Contains(last.Categories, "image") {
						t.Fatal("output evidence was not bound to the unique report", last)
					}
				} else if response.Code < 400 || strings.Contains(response.Body.String(), base64.StdEncoding.EncodeToString(asset.Data)) || strings.Contains(response.Body.String(), "[DONE]") {
					t.Fatal("denied or incompletely inspected output released", response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestStrictHoldNeverReleasesUnsupportedMediaStream(t *testing.T) {
	c := mediaOutputConfig()
	var scans atomic.Int32
	encoded := base64.StdEncoding.EncodeToString(testPNG())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"object\":\"chat.completion.chunk\",\"model\":\"enterprise\",\"choices\":[{\"index\":0,\"delta\":{\"content\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"data:image/png;base64," + encoded + "\"}}]},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
	}))
	defer up.Close()
	rt, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	o := rt.(*runtimeHandler).options
	o.MediaProcessor = func(ctx context.Context, a MediaAsset) (MediaReport, error) {
		scans.Add(1)
		return fakeMedia("approved", 99)(ctx, a)
	}
	handler, err := NewGateway(GatewayOptions{Runtime: o})
	if err != nil {
		t.Fatal(err)
	}
	response := gatewayRequest(handler, "/v1/chat/completions", aliceToken, `{"model":"enterprise","messages":[{"role":"user","content":"safe"}],"stream":true}`, "")
	if response.Code < 400 || scans.Load() != 0 || strings.Contains(response.Body.String(), encoded) || strings.Contains(response.Body.String(), "[DONE]") || response.Header().Get("Content-Type") == "text/event-stream" {
		t.Fatal("unsupported stream bypassed strict hold", response.Code, scans.Load(), response.Body.String())
	}
}
