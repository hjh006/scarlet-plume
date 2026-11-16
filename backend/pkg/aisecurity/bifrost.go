package aisecurity

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// normalizeBifrostEnvelope discards one known Bifrost transport metadata field.
// It runs only under the gateway's trusted fixed-profile context. All remaining
// fields still pass the unchanged strict openai_chat model response adapter.
func normalizeBifrostEnvelope(raw []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if strictJSON(bytes.NewReader(raw), &envelope) != nil || envelope == nil {
		return nil, errors.New("ambiguous Bifrost response")
	}
	delete(envelope, "extra_fields")
	return json.Marshal(envelope)
}

const BifrostOSSVersion = "transports/v2.5.1"
const BifrostOSSCommit = "7cb75fb2f8276ca48d0792c857fecb67b8879e2f"
const BifrostOSSImage = "maximhq/bifrost:v2.5.1@sha256:5ec9873acaf1687556b6e4dd854b92b539d9debf87a5a683b66338648b84d3c8"

// BifrostProfile is a deliberately restricted single OpenAI-compatible provider
// deployment. Multiple approved models may share this fixed destination/key.
// Arbitrary Bifrost resources, caching, routing and request headers are not accepted.
type BifrostProfile struct {
	PrivateEndpointApproved bool     `json:"privateEndpointApproved,omitempty"`
	Provider                string   `json:"provider"`
	Region                  string   `json:"region"`
	EndpointRef             string   `json:"endpointRef"`
	BaseURL                 string   `json:"baseUrl"`
	APIKeyEnv               string   `json:"apiKeyEnv"`
	Models                  []string `json:"models"`
	Port                    int      `json:"port"`
}

func BifrostRuntimeURL(p BifrostProfile) string {
	return "http://127.0.0.1:" + portString(p.Port) + "/openai/v1/chat/completions"
}

func portString(port int) string { raw, _ := json.Marshal(port); return string(raw) }

func ValidateBifrostProfile(p BifrostProfile, synthetic bool) error {
	if !validID(p.Provider) || !validID(p.Region) || !synthetic && !strings.HasPrefix(p.EndpointRef, "env:SCARLET_GATEWAY_") {
		return errors.New("fixed provider identity, region and endpoint reference required")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || p.Port < 1024 || p.Port > 65535 || !regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,79}$`).MatchString(p.APIKeyEnv) || len(p.Models) < 1 || len(p.Models) > 128 {
		return errors.New("invalid fixed Bifrost profile")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if !synthetic || u.Scheme != "http" || !(ip != nil && ip.IsLoopback() || u.Hostname() == "mock-provider" || u.Hostname() == "host.docker.internal") {
			return errors.New("production provider requires HTTPS")
		}
	}
	seen := map[string]bool{}
	for _, model := range p.Models {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,159}$`).MatchString(model) || seen[model] {
			return errors.New("invalid or duplicate fixed model")
		}
		seen[model] = true
	}
	return nil
}

func ValidateBifrostCatalog(p BifrostProfile, c Config, targets map[string]Target) error {
	if ValidateConfig(c) != nil {
		return ErrConfig
	}
	for _, model := range c.Models {
		if !model.Enabled {
			continue
		}
		upstream := model.UpstreamModel
		if upstream == "" {
			upstream = model.ID
		}
		if model.ResponseFormat != "openai_chat" || model.Provider != p.Provider || model.Region != p.Region || !slices.Contains(p.Models, upstream) || targets[model.EndpointRef].URL != BifrostRuntimeURL(p) {
			return errors.New("catalog model is outside the fixed Bifrost deployment")
		}
	}
	return nil
}

// BifrostConfig emits only fields verified against the fixed OSS schema. One key
// plus max_retries=0 also prevents the key-pool rotation allowed at zero retries.
func BifrostConfig(p BifrostProfile, synthetic bool) ([]byte, error) {
	if err := ValidateBifrostProfile(p, synthetic); err != nil {
		return nil, err
	}
	plugins := []map[string]any{}
	for _, name := range []string{"governance", "semantic_cache", "logging", "telemetry", "otel", "prompts"} {
		plugins = append(plugins, map[string]any{"name": name, "enabled": false})
	}
	return json.MarshalIndent(map[string]any{
		"source_of_truth": "config.json", "disable_onboarding": true,
		"client":    map[string]any{"enable_logging": false, "disable_content_logging": true, "allow_per_request_content_storage_override": false, "allow_per_request_raw_override": false, "allow_direct_keys": false, "enforce_auth_on_inference": false, "dump_errors_in_console_logs": false},
		"providers": map[string]any{"openai": map[string]any{"keys": []any{map[string]any{"name": "scarlet-fixed-provider", "value": "env." + p.APIKeyEnv, "models": p.Models, "weight": 1, "enabled": true}}, "network_config": map[string]any{"base_url": strings.TrimRight(p.BaseURL, "/"), "max_retries": 0, "default_request_timeout_in_seconds": 8, "allow_private_network": synthetic || p.PrivateEndpointApproved}, "send_back_raw_request": false, "send_back_raw_response": false, "store_raw_request_response": false}},
		"plugins":   plugins, "config_store": map[string]any{"enabled": false}, "logs_store": map[string]any{"enabled": false},
	}, "", "  ")
}

// ValidateBifrostCatalogProfiles binds every enabled catalog model to exactly
// one explicit provider process; Bifrost never chooses a provider implicitly.
func ValidateBifrostCatalogProfiles(profiles []BifrostProfile, c Config, targets map[string]Target) error {
	if ValidateConfig(c) != nil || len(profiles) < 1 || len(profiles) > 8 {
		return ErrConfig
	}
	ports := map[int]bool{}
	refs := map[string]bool{}
	for _, p := range profiles {
		if ports[p.Port] || refs[p.EndpointRef] {
			return errors.New("duplicate Bifrost process binding")
		}
		ports[p.Port] = true
		refs[p.EndpointRef] = true
	}
	for _, m := range c.Models {
		if !m.Enabled {
			continue
		}
		found := 0
		for _, p := range profiles {
			upstream := m.UpstreamModel
			if upstream == "" {
				upstream = m.ID
			}
			if m.ResponseFormat == "openai_chat" && m.Provider == p.Provider && m.Region == p.Region && slices.Contains(p.Models, upstream) && targets[m.EndpointRef].URL == BifrostRuntimeURL(p) {
				found++
			}
		}
		if found != 1 {
			return errors.New("catalog model outside explicit Bifrost process bindings")
		}
	}
	return nil
}
