package aisecurity

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// InspectionPolicy opts into versioned, bounded classification. A classification
// is evaluated against existing category allowlists; it is not a claim that a
// credential works, a business identifier was issued, or an injection is absent.
type InspectionPolicy struct {
	Version      string           `json:"version"`
	BuiltinPacks []string         `json:"builtinPacks"`
	Rules        []InspectionRule `json:"rules"`
}

type InspectionRule struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Category string `json:"category"`
	Pattern  string `json:"pattern"`
}

type compiledInspectionRule struct {
	category string
	pattern  *regexp.Regexp
}

var credentialShapes = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\bauthorization["']?\s*[:=]\s*["']?(?:bearer|basic)\s+[a-z0-9+/_.=-]{8,}`),
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|client[_-]?secret|access[_-]?token)["']?\s*[:=]\s*["']?[a-z0-9+/_.=-]{12,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
}
var cnBusinessShape = regexp.MustCompile(`[0-9A-HJ-NPQRTUWXY]{18}`)

func compileInspection(p *InspectionPolicy) ([]compiledInspectionRule, error) {
	if p == nil {
		return nil, nil
	}
	if p.Version != InspectionVersion || !list(p.BuiltinPacks, 2, func(v string) bool { return v == "credential-v1" || v == "cn-business-v1" }) || len(p.Rules) > 32 {
		return nil, ErrConfig
	}
	seen := map[string]bool{}
	rules := make([]compiledInspectionRule, 0, len(p.Rules))
	for _, r := range p.Rules {
		if !addID(seen, r.ID) || !validID(r.Version) || !slices.Contains([]string{"email", "phone", "national_id", "financial_account", "credential", "business"}, r.Category) || len(r.Pattern) == 0 || len(r.Pattern) > 256 || !utf8.ValidString(r.Pattern) {
			return nil, ErrConfig
		}
		re, err := regexp.Compile(r.Pattern) // Go's RE2 engine has no backtracking.
		if err != nil || re.MatchString("") {
			return nil, ErrConfig
		}
		rules = append(rules, compiledInspectionRule{r.Category, re})
	}
	return rules, nil
}

func inspectAdditional(candidates []string, p *InspectionPolicy) ([]string, string) {
	rules, err := compileInspection(p)
	if err != nil {
		return nil, "inspection_profile_invalid"
	}
	if p == nil {
		return nil, ""
	}
	found := []string{}
	for _, text := range candidates {
		for _, r := range rules {
			if r.pattern.MatchString(text) {
				found = append(found, r.category)
			}
		}
		if slices.Contains(p.BuiltinPacks, "credential-v1") {
			for _, re := range credentialShapes {
				if re.MatchString(text) {
					found = append(found, "credential")
					break
				}
			}
		}
		if slices.Contains(p.BuiltinPacks, "cn-business-v1") {
			// GB 32100-2015's base-31 checksum checks syntax, not registry status.
			for offset, matches := 0, 0; offset < len(text); {
				loc := cnBusinessShape.FindStringIndex(text[offset:])
				if loc == nil {
					break
				}
				start, end := offset+loc[0], offset+loc[1]
				matches++
				if matches > 4096 {
					return nil, "content_inspection_incomplete"
				}
				if (start == 0 || !asciiWord(text[start-1])) && (end == len(text) || !asciiWord(text[end])) && validCNBusinessID(text[start:end]) {
					found = append(found, "business")
					break
				}
				offset = end
			}
		}
	}
	return unique(found), ""
}

func asciiWord(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func validCNBusinessID(value string) bool {
	const alphabet = "0123456789ABCDEFGHJKLMNPQRTUWXY"
	if len(value) != 18 {
		return false
	}
	sum, weight := 0, 1
	for i := 0; i < 17; i++ {
		n := strings.IndexByte(alphabet, value[i])
		if n < 0 {
			return false
		}
		sum += n * weight
		weight = weight * 3 % 31
	}
	return value[17] == alphabet[(31-sum%31)%31]
}
