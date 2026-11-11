package runtimesecurity

import (
	"net/url"
	"regexp"
	"strings"
)

var securityPatterns = []struct {
	kind    string
	pattern *regexp.Regexp
}{
	{"sql_injection", regexp.MustCompile(`(?i)(\bunion\s+(all\s+)?select\b|\b(or|and)\s+['"0-9][^\n]{0,40}=|;\s*(drop|delete|select|insert|update)\b|\b(sleep|benchmark)\s*\()`)},
	{"xss", regexp.MustCompile(`(?i)(<\s*script\b|javascript\s*:|\bon(error|load)\s*=)`)},
	{"path_traversal", regexp.MustCompile(`(?i)(\.\.[/\\]|%2e%2e(%2f|%5c|/)|%252e%252e)`)},
}

// InspectSecurity observes request patterns locally. It never returns text,
// positions, URLs or headers. A zero count explicitly records an inspected scope.
func InspectSecurity(raw []byte) []SecuritySignal {
	if len(raw) > MaxJSONCaptureBytes {
		return nil
	}
	text := strings.ToValidUTF8(string(raw), "")
	if decoded, err := url.QueryUnescape(text); err == nil {
		text = decoded
	}
	result := make([]SecuritySignal, 0, len(securityPatterns))
	for _, p := range securityPatterns {
		count := int64(0)
		if p.pattern.MatchString(text) {
			count = 1
		}
		result = append(result, SecuritySignal{Kind: p.kind, Location: "request", Count: count, Observed: 1})
	}
	return result
}
