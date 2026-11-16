package aisecurity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func detectorFixture() Config {
	c := fixture()
	c.Policy.RequiredInspectionVersion = InspectionVersion
	c.Policy.Inspection = &InspectionPolicy{Version: InspectionVersion, BuiltinPacks: []string{"credential-v1", "cn-business-v1"}, Rules: []InspectionRule{{ID: "contract", Version: "v1", Category: "business", Pattern: `INTERNAL-[A-Z]{3}-[0-9]{4}`}}}
	return c
}

func TestVersionedInspectionClassifiesDecodedContentWithoutClaimingValidity(t *testing.T) {
	c := detectorFixture()
	for _, tc := range []struct{ text, category string }{
		{`Authorization: Bearer synthetic-credential-0001`, "credential"},
		{`{"note":"Authorization: \u0042earer synthetic-credential-0001"}`, "credential"},
		{`{"authoriz\u0061tion":"Bearer synthetic-credential-0001"}`, "credential"},
		{`{"api_key":"synthetic-credential-0001"}`, "credential"},
		{`-----BEGIN PRIVATE KEY-----`, "credential"},
		{`eyJhbGciOiJub25lIn0.eyJzdWIiOiJzeW50aGV0aWMifQ.c3ludGhldGljLW9ubHk`, "credential"}, // Synthetic alg=none and fake signature; never an authentication token. gitleaks:allow
		{`INTERNAL-ABC-2027`, "business"},
		{`91350100M000100Y43`, "business"},
	} {
		found, code := inspect(tc.text, 65536, nil, c.Policy.Inspection)
		if code != "" || !slices.Contains(found, tc.category) {
			t.Fatalf("classification failed for synthetic %s: %v %s", tc.category, found, code)
		}
	}
	for _, text := range []string{"91350100M000100Y44", "X91350100M000100Y43X", "authorization overview", "ordinary business text"} {
		found, code := inspect(text, 65536, nil, c.Policy.Inspection)
		if code != "" || slices.Contains(found, "business") || slices.Contains(found, "credential") {
			t.Fatalf("nonmatch classified: %v %s", found, code)
		}
	}
	if !validCNBusinessID("91350100M000100Y43") || validCNBusinessID("91350100M000100Y44") {
		t.Fatal("base-31 check digit ignored")
	}
}

func TestInspectionConfigRejectsUnknownOrUnboundedRulesAndBindsVersions(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Policy.RequiredInspectionVersion = "" },
		func(c *Config) { c.Policy.RequiredInspectionVersion = LegacyInspectionVersion },
		func(c *Config) { c.Policy.Inspection.Version = "future" },
		func(c *Config) { c.Policy.Inspection.BuiltinPacks = []string{"unknown"} },
		func(c *Config) { c.Policy.Inspection.BuiltinPacks = []string{"credential-v1", "credential-v1"} },
		func(c *Config) { c.Policy.Inspection.Rules[0].Category = "prompt_injection" },
		func(c *Config) { c.Policy.Inspection.Rules[0].Pattern = `(?<=unsafe)` },
		func(c *Config) { c.Policy.Inspection.Rules[0].Pattern = `a*` },
		func(c *Config) { c.Policy.Inspection.Rules[0].Pattern = strings.Repeat("a", 257) },
		func(c *Config) { c.Policy.Inspection.Rules = make([]InspectionRule, 33) },
		func(c *Config) {
			c.Policy.Inspection.Rules = append(c.Policy.Inspection.Rules, c.Policy.Inspection.Rules[0])
		},
	} {
		c := detectorFixture()
		mutate(&c)
		if ValidateConfig(c) == nil {
			t.Fatal("unsafe profile accepted")
		}
	}
	c := detectorFixture()
	before := ConfigDigest(c)
	c.Policy.Inspection.Rules[0].Version = "v2"
	if ValidateConfig(c) != nil || ConfigDigest(c) == before {
		t.Fatal("version change did not invalidate digest-bound authorization and cache")
	}
	c = fixture()
	c.Policy.RequiredInspectionVersion = LegacyInspectionVersion
	if ValidateConfig(c) != nil {
		t.Fatal("legacy configuration without new detectors became invalid")
	}
}

func TestDetectorsEnforceGlobalAndModelToolSourceAllowlists(t *testing.T) {
	for _, surface := range []string{"input", "output", "source", "media_input", "media_output", "tool"} {
		t.Run(surface, func(t *testing.T) {
			c := detectorFixture()
			r := testRequest("model")
			r.ModelID, r.Input = "enterprise", `{"prompt":"ordinary"}`
			secret := "Authorization: Bearer synthetic-credential-0001"
			switch surface {
			case "input":
				r.Input = secret
			case "output":
				r.Output = secret
			case "source":
				r.sourceInputProjection = &secret
			case "media_input":
				r.mediaInputProjection = &secret
			case "media_output":
				r.mediaOutputProjection = &secret
			case "tool":
				r.Kind, r.ToolID, r.Action, r.Target = "tool", "ticket", "close", "ticket-17"
				r.ModelID, r.Input = "", ""
				r.Arguments = map[string]string{"resolution": secret}
				c.Tools[0].AllowedArguments["resolution"] = []string{secret}
				c.Tools[0].HighImpact = false
			}
			if d := Evaluate(c, r, time.Now()); d.Outcome != "deny" || !slices.Contains(d.Categories, "credential") {
				t.Fatal("detector bypass", d)
			}
			// Granting the global category alone must not grant a model/tool target.
			c.Policy.AllowedInputCategories = append(c.Policy.AllowedInputCategories, "credential")
			c.Policy.AllowedOutputCategories = append(c.Policy.AllowedOutputCategories, "credential")
			c.Tools[0].AllowedInputCategories = []string{"confidential"}
			if d := Evaluate(c, r, time.Now()); d.Outcome != "deny" {
				t.Fatal("target allowlist bypass", d)
			}
		})
	}
}

func TestRuntimeDetectorPacksInspectStreamCacheFallbackAndToolOutput(t *testing.T) {
	for _, mode := range []string{"stream", "cache", "fallback", "tool"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int32
			secret := "Authorization: Bearer synthetic-credential-0001"
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if mode == "stream" {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, sseCompletion("enterprise", []string{"Authorization: ", "Bearer synthetic-credential-0001"}, "data: [DONE]\n\n"))
				} else if mode == "fallback" {
					w.WriteHeader(503)
				} else if mode == "tool" {
					json.NewEncoder(w).Encode(map[string]string{"result": secret})
				} else {
					io.WriteString(w, `{"object":"chat.completion","model":"enterprise","choices":[{"index":0,"message":{"role":"assistant","content":"approved"},"finish_reason":"stop"}]}`)
				}
			}))
			defer up.Close()
			c := detectorFixture()
			c.Models[0].ResponseFormat = "openai_chat"
			c.Tools[0].HighImpact = false
			rt, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
			o := rt.(*runtimeHandler).options
			if mode == "tool" {
				r := testRequest("tool")
				r.ToolID, r.Action, r.Target, r.Arguments = "ticket", "close", "ticket-17", map[string]string{"resolution": "fixed"}
				out := callRuntime(t, rt, aliceToken, wire(r))
				if out.Code != 403 || strings.Contains(out.Body.String(), "synthetic-credential") {
					t.Fatal("tool output leaked", out.Code, out.Body.String())
				}
				return
			}
			if mode == "cache" {
				o.Cache = &testModelCache{data: map[ModelCacheScope][]byte{}}
			}
			if mode == "fallback" {
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.WriteString(w, `{"object":"chat.completion","model":"backup-model","choices":[{"index":0,"message":{"role":"assistant","content":"Authorization: Bearer synthetic-credential-0001"},"finish_reason":"stop"}]}`)
				}))
				defer backup.Close()
				m := c.Models[0]
				m.ID, m.EndpointRef = "backup-model", "backup"
				c.Models = append(c.Models, m)
				c.Applications[0].ModelIDs = append(c.Applications[0].ModelIDs, m.ID)
				o.Targets["backup"] = Target{URL: backup.URL}

			}
			options := GatewayOptions{Runtime: o}
			if mode == "fallback" {
				options.Fallbacks = map[string][]string{"enterprise": {"backup-model"}}
			}
			g, err := NewGateway(options)
			if err != nil {
				t.Fatal(err)
			}
			body := `{"model":"enterprise","messages":[{"role":"user","content":"request"}]}`
			if mode == "stream" {
				body = strings.TrimSuffix(body, "}") + `,"stream":true}`
			}
			if mode == "cache" {
				if out := gatewayRequest(g, "/v1/chat/completions", aliceToken, body, "detector-cache-first-0001"); out.Code != 200 {
					t.Fatal(out.Code, out.Body.String())
				}
				cache := o.Cache.(*testModelCache)
				cache.mu.Lock()
				for key := range cache.data {
					cache.data[key] = []byte(`{"model":"enterprise","output":"Authorization: Bearer synthetic-credential-0001","finishReason":"stop"}`)
				}
				cache.mu.Unlock()
			}
			out := gatewayRequest(g, "/v1/chat/completions", aliceToken, body, "detector-output-check-0001")
			if out.Code != 403 || strings.Contains(out.Body.String(), "synthetic-credential") || strings.Contains(out.Body.String(), "[DONE]") {
				t.Fatal("protected output leaked", mode, out.Code, out.Body.String())
			}
			if hits.Load() != 1 {
				t.Fatal("unexpected upstream execution count", hits.Load())
			}
		})
	}
}
