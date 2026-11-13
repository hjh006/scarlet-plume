package store

import (
	"net/http"
	"slices"
	"strings"
	"unicode"
)

type APIValidationSensitiveField struct {
	Path     string `json:"path"`
	Category string `json:"category"`
}
type APIValidationResponseHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type APIValidationProbePolicy struct {
	AuthRequired    bool                          `json:"authRequired,omitempty"`
	SensitiveFields []APIValidationSensitiveField `json:"sensitiveFields,omitempty"`
	ResponseHeaders []APIValidationResponseHeader `json:"responseHeaders,omitempty"`
}

// NormalizeAPIProbePolicy is shared with the source-side runner. It accepts
// only bounded, reviewable policy metadata, never arbitrary headers or secrets.
func NormalizeAPIProbePolicy(p *APIValidationProbePolicy, rules []string, origin string) bool {
	for _, rule := range rules {
		if !slices.Contains([]string{"bola", "weak_credential", "auth_required", "sensitive_exposure", "response_headers"}, rule) {
			return false
		}
	}
	if len(p.SensitiveFields) > 8 || len(p.ResponseHeaders) > 8 {
		return false
	}
	fields := map[string]bool{}
	for i := range p.SensitiveFields {
		f := &p.SensitiveFields[i]
		f.Path = strings.TrimSpace(f.Path)
		parts := strings.Split(f.Path, ".")
		if len(parts) > 8 || len(f.Path) < 1 || len(f.Path) > 120 || fields[f.Path] || !slices.Contains([]string{"email", "phone", "national_id", "financial_account"}, f.Category) {
			return false
		}
		for _, part := range parts {
			if part == "" || strings.IndexFunc(part, func(r rune) bool {
				return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
			}) >= 0 {
				return false
			}
		}
		fields[f.Path] = true
	}
	headers := map[string]bool{}
	for i := range p.ResponseHeaders {
		h := &p.ResponseHeaders[i]
		h.Name = http.CanonicalHeaderKey(strings.TrimSpace(h.Name))
		h.Value = strings.TrimSpace(h.Value)
		if !slices.Contains([]string{"X-Content-Type-Options", "Cache-Control", "Referrer-Policy", "Content-Security-Policy", "Strict-Transport-Security"}, h.Name) || headers[h.Name] || h.Value == "" || len(h.Value) > 256 || strings.IndexFunc(h.Value, func(r rune) bool { return unicode.IsControl(r) || r > 126 }) >= 0 || h.Name == "Strict-Transport-Security" && !strings.HasPrefix(origin, "https://") {
			return false
		}
		headers[h.Name] = true
	}
	slices.SortFunc(p.SensitiveFields, func(a, b APIValidationSensitiveField) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(p.ResponseHeaders, func(a, b APIValidationResponseHeader) int { return strings.Compare(a.Name, b.Name) })
	return (!slices.Contains(rules, "auth_required") || p.AuthRequired) && (!slices.Contains(rules, "sensitive_exposure") || p.AuthRequired && len(p.SensitiveFields) > 0) && (!slices.Contains(rules, "response_headers") || len(p.ResponseHeaders) > 0)
}

func validExtendedValidationProof(p APIValidationProbePolicy, r APIValidationRuleResult) bool {
	if r.AnonymousStatus != 0 && (r.AnonymousStatus < 100 || r.AnonymousStatus > 599) || r.SensitiveChecked < 0 || r.SensitiveChecked > 8 || r.OwnerSensitiveCount < 0 || r.OwnerSensitiveCount > r.SensitiveChecked || r.AnonymousSensitiveCount < 0 || r.AnonymousSensitiveCount > r.SensitiveChecked || r.HeaderChecked < 0 || r.HeaderChecked > 8 || len(r.HeaderMismatches) > 8 {
		return false
	}
	if r.PolicyDigest != "" && !digestPattern.MatchString(r.PolicyDigest) {
		return false
	}
	if !slices.Contains([]string{"auth_required", "sensitive_exposure", "response_headers"}, r.Rule) {
		return true
	}
	if r.PolicyConfirmed && r.PolicyDigest != assessmentDigest(p) {
		return false
	}
	for _, name := range r.HeaderMismatches {
		if !slices.ContainsFunc(p.ResponseHeaders, func(h APIValidationResponseHeader) bool { return h.Name == name }) {
			return false
		}
	}
	if r.Outcome == "inconclusive" {
		return true
	}
	if !r.PolicyConfirmed {
		return false
	}
	switch r.Rule {
	case "auth_required":
		return p.AuthRequired
	case "sensitive_exposure":
		return p.AuthRequired && len(p.SensitiveFields) > 0 && r.SensitiveChecked == len(p.SensitiveFields)
	case "response_headers":
		if r.HeaderChecked != len(p.ResponseHeaders) || r.HeaderChecked < 1 {
			return false
		}
		seen := map[string]bool{}
		for _, name := range r.HeaderMismatches {
			if seen[name] || !slices.ContainsFunc(p.ResponseHeaders, func(h APIValidationResponseHeader) bool { return h.Name == name }) {
				return false
			}
			seen[name] = true
		}
		return true
	}
	return false
}
