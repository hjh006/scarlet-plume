package runtimesecurity

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/dataformat"
)

const MaxJSONCaptureBytes = 64 << 10

// InspectJSON runs at the authorized capture source. It returns bounded field
// statistics, never scalar values or dynamic object keys. Unrecognized keys
// become "field" and that loss of path precision remains explicit.
func InspectJSON(raw []byte, location string) ([]DataField, []string) {
	if len(raw) > MaxJSONCaptureBytes {
		return nil, []string{"json_size_limit"}
	}
	if location != "request" && location != "response" && location != "database" {
		return nil, []string{"json_invalid"}
	}
	var root any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&root) != nil || !json.Valid(raw) {
		return nil, []string{"json_invalid"}
	}
	type key struct{ path, category, method string }
	counts := map[key]int64{}
	observed := map[string]int64{}
	warnings := []string{}
	var walk func(any, string, string, int)
	walk = func(value any, path, name string, depth int) {
		if depth > 8 {
			warnings = append(warnings, "json_depth_limit")
			return
		}
		switch v := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				part := k
				if !staticJSONKey(k) {
					part = "field"
					warnings = append(warnings, "json_dynamic_keys")
				}
				next := part
				if path != "" {
					next = path + "." + part
				}
				walk(v[k], next, k, depth+1)
			}
		case []any:
			if path == "" {
				path = "items"
			}
			for _, item := range v {
				walk(item, path+"[]", name, depth+1)
			}
		case string, json.Number, bool:
			if path == "" {
				path = "value"
			}
			if !fieldPath.MatchString(path) || len(path) > 240 {
				warnings = append(warnings, "json_depth_limit")
				return
			}
			if _, exists := observed[path]; !exists && len(observed) >= MaxCaptureItems {
				warnings = append(warnings, "json_field_limit")
				return
			}
			observed[path]++
			if category := collector.MetadataSensitivity(name, ""); category != "" {
				if category != "credential" {
					counts[key{path, category, "metadata"}]++
				} else if text, ok := v.(string); ok && strings.TrimSpace(text) != "" {
					counts[key{path, category, "metadata"}]++
				}
			}
			text, ok := v.(string)
			if number, isNumber := v.(json.Number); isNumber {
				text, ok = string(number), true
			}
			if ok {
				for _, category := range FormatCategories(text) {
					counts[key{path, category, "format"}]++
				}
			}
		}
	}
	walk(root, "", "", 0)
	fields := make([]DataField, 0, len(counts))
	for k, count := range counts {
		fields = append(fields, DataField{Name: k.path, Location: location, Category: k.category, Method: k.method, Count: count, Observed: observed[k.path]})
	}
	slices.SortFunc(fields, func(a, b DataField) int {
		return strings.Compare(a.Name+"\x00"+a.Category+"\x00"+a.Method, b.Name+"\x00"+b.Category+"\x00"+b.Method)
	})
	if len(fields) > MaxCaptureItems {
		fields = fields[:MaxCaptureItems]
		warnings = append(warnings, "json_field_limit")
	}
	slices.Sort(warnings)
	warnings = slices.Compact(warnings)
	return fields, warnings
}

func staticJSONKey(key string) bool {
	// A short explicit vocabulary avoids treating arbitrary map keys as schema.
	return slices.Contains([]string{"data", "items", "results", "records", "contacts", "customers", "users", "profile", "account", "contact", "attributes", "list", "response", "request", "payload", "details", "billing", "address", "nested", "value", "contact_value", "contactValue", "id", "email", "email_address", "emailAddress", "phone", "mobile", "phone_number", "customer_phone", "national_id", "nationalId", "id_card", "identity_card", "bank_account", "bank_account_number", "card", "card_number", "name", "full_name", "password", "secret", "token", "ip_address"}, key)
}

// FormatCategories confirms only format properties. Checksum-valid identifiers
// do not establish a real person, account ownership, use or statutory grade.
func FormatCategories(value string) []string {
	return dataformat.Categories(value)
}
