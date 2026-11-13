package store

import (
	"net/netip"
	"slices"
	"strings"
	"unicode"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

var extendedRuntimeRules = []string{"over_collection", "credential_cleartext_transport", "subject_multi_ip", "subject_multi_region", "client_region_mismatch", "recipient_scope", "destination_scope", "volume_limit", "subject_repeated_access"}

func validateRuntimePolicyScope(p *RuntimeDetectionPolicy) error {
	if len(p.ScopeKey) > 80 || strings.IndexFunc(p.ScopeKey, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_.:-", r))
	}) >= 0 || p.SourceID < 0 || len(p.Route) > 200 || p.SubjectRef != "" && !runtimeScopeReference.MatchString(p.SubjectRef) || !classificationFilterValue(p.Method, "", "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE") || !classificationFilterValue(p.Direction, "", "request", "response") {
		return ErrInvalid
	}
	if p.ClientIP != "" {
		ip, e := netip.ParseAddr(p.ClientIP)
		if e != nil || ip.Zone() != "" || ip.IsUnspecified() {
			return ErrInvalid
		}
		p.ClientIP = ip.Unmap().String()
	}
	for _, set := range [][]string{p.AllowedCategories, p.AllowedCountries, p.AllowedClientCountries, p.AllowedRecipients, p.AllowedDomains} {
		if len(set) > 64 {
			return ErrInvalid
		}
		for _, s := range set {
			if len(s) < 1 || len(s) > 160 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
				return ErrInvalid
			}
		}
	}
	for _, set := range [][]string{p.AllowedCountries, p.AllowedClientCountries} {
		for _, country := range set {
			if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
				return ErrInvalid
			}
		}
	}
	for _, domain := range p.AllowedDomains {
		if len(domain) > 253 || domain != strings.ToLower(domain) || strings.ContainsAny(domain, " /:@\\") || strings.IndexFunc(domain, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-') }) >= 0 {
			return ErrInvalid
		}
	}
	for _, cat := range p.AllowedCategories {
		if !classificationFilterValue(cat, "email", "phone", "national_id", "financial_account", "credential", "personal_name", "address", "ip_address") {
			return ErrInvalid
		}
	}
	if p.Rule == "over_collection" && (!p.AllowlistConfigured || !classificationFilterValue(p.Direction, "request", "response")) {
		return ErrInvalid
	}
	if p.Rule == "client_region_mismatch" && len(p.AllowedClientCountries) == 0 || p.Rule == "recipient_scope" && len(p.AllowedRecipients) == 0 || p.Rule == "destination_scope" && len(p.AllowedCountries)+len(p.AllowedDomains) == 0 {
		return ErrInvalid
	}
	if p.Rule == "volume_limit" && (p.MaxVolume < 1 || p.MaxVolume > 1000000000000 || !classificationFilterValue(p.VolumeUnit, "records", "bytes")) {
		return ErrInvalid
	}
	return nil
}
func runtimeObservationInScope(p RuntimeDetectionPolicy, o runtimesecurity.Observation) bool {
	return (p.Method == "" || p.Method == o.Method) && (p.Route == "" || runtimesecurity.RouteKey(p.Route) == runtimesecurity.RouteKey(o.Route)) && (p.SubjectRef == "" || p.SubjectRef == o.SubjectRef) && (p.ClientIP == "" || p.ClientIP == o.ClientIP)
}
func extendedRuntimePolicySignal(p RuntimeDetectionPolicy, o runtimesecurity.Observation) (matched, observed int64, categories []string, handled bool) {
	handled = slices.Contains(extendedRuntimeRules, p.Rule)
	if !handled {
		return
	}
	switch p.Rule {
	case "over_collection":
		if !p.AllowlistConfigured || !slices.Contains(o.InspectedLocations, p.Direction) || incompleteRuntimeCapture(o) {
			return
		}
		observed = 1
		for _, f := range o.DataFields {
			if f.Location == p.Direction && f.Count > 0 && !slices.Contains(p.AllowedCategories, f.Category) {
				matched = 1
				categories = append(categories, f.Category)
			}
		}
	case "credential_cleartext_transport":
		if !classificationFilterValue(o.Transport, "http", "https") || incompleteRuntimeCapture(o) {
			return
		}
		for _, f := range o.DataFields {
			if f.Category == "credential" && f.Count > 0 && slices.Contains(o.InspectedLocations, f.Location) {
				observed++
				if o.Transport == "http" {
					matched++
					categories = append(categories, "credential")
				}
			}
		}
	case "client_region_mismatch":
		if o.Context == nil || o.Context.ClientCountry == "" {
			return
		}
		observed = 1
		if !slices.Contains(p.AllowedClientCountries, o.Context.ClientCountry) {
			matched = 1
		}
	case "recipient_scope":
		if o.Context == nil || o.Context.Recipient == "" {
			return
		}
		observed = 1
		if !slices.Contains(p.AllowedRecipients, o.Context.Recipient) {
			matched = 1
		}
	case "destination_scope":
		if o.Context == nil {
			return
		}
		if len(p.AllowedCountries) > 0 && o.Context.DestinationCountry == "" || len(p.AllowedDomains) > 0 && o.Context.DestinationDomain == "" {
			return
		}
		observed = 1
		if len(p.AllowedCountries) > 0 && !slices.Contains(p.AllowedCountries, o.Context.DestinationCountry) || len(p.AllowedDomains) > 0 && !slices.Contains(p.AllowedDomains, o.Context.DestinationDomain) {
			matched = 1
		}
	case "volume_limit":
		if o.Context == nil || o.Context.Volume == nil || o.Context.VolumeUnit != p.VolumeUnit {
			return
		}
		observed = 1
		matched = *o.Context.Volume
	case "subject_repeated_access":
		if o.SubjectRef != "" {
			matched = 1
			observed = 1
		}
	case "subject_multi_ip":
		if o.SubjectRef != "" && o.ClientIP != "" {
			observed = 1
		}
	case "subject_multi_region":
		if o.SubjectRef != "" && o.Context != nil && o.Context.ClientCountry != "" {
			observed = 1
		}
	}
	return
}
func runtimeRuleUsesSubject(rule string) bool {
	return slices.Contains([]string{"subject_bulk_access", "subject_multi_ip", "subject_multi_region", "subject_repeated_access"}, rule)
}
func runtimePolicyTitle(rule string) string {
	switch rule {
	case "over_collection":
		return "数据类型超出配置允许范围"
	case "credential_cleartext_transport":
		return "认证材料经未加密HTTP传输"
	case "subject_multi_ip":
		return "同一来源账号多IP访问"
	case "subject_multi_region":
		return "同一来源账号多地区访问"
	case "client_region_mismatch":
		return "访问地区偏离配置基线"
	case "recipient_scope":
		return "共享接收方偏离配置基线"
	case "destination_scope":
		return "共享或出境目的地偏离配置基线"
	case "volume_limit":
		return "已观测传输量超出配置阈值"
	case "subject_repeated_access":
		return "账号在配置窗口内多次访问"
	}
	return rule
}
