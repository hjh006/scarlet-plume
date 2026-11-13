package runtimesecurity

import (
	"bytes"
	"encoding/json"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"slices"
	"strings"
)

// CaptureValueFingerprints is opt-in at the authorized capture source. It emits
// only keyed equality tokens for format-validated scalar fields already covered
// by InspectJSON. Missing historical tokens cannot be reconstructed on platform.
func CaptureValueFingerprints(raw []byte, location, keyHex, keyID, scope string) []ValueFingerprint {
	out := []ValueFingerprint{}
	if keyHex == "" || len(raw) > MaxJSONCaptureBytes {
		return out
	}
	fields, _ := InspectJSON(raw, location)
	allowed := map[string]bool{}
	for _, f := range fields {
		if f.Method == "format" {
			allowed[f.Name+"\x00"+f.Category] = true
		}
	}
	var root any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&root) != nil || !json.Valid(raw) {
		return out
	}
	seen := map[string]bool{}
	var walk func(any, string, int)
	walk = func(value any, path string, depth int) {
		if depth > 8 || len(out) >= 32 {
			return
		}
		switch v := value.(type) {
		case map[string]any:
			keys := []string{}
			for key := range v {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			for _, key := range keys {
				if !staticJSONKey(key) {
					continue
				}
				next := key
				if path != "" {
					next = path + "." + key
				}
				walk(v[key], next, depth+1)
			}
		case []any:
			if path == "" {
				path = "items"
			}
			for _, item := range v {
				walk(item, path+"[]", depth+1)
			}
		case string, json.Number:
			text, ok := v.(string)
			if !ok {
				text = string(v.(json.Number))
			}
			if path == "" {
				path = "value"
			}
			for _, category := range FormatCategories(text) {
				if !allowed[path+"\x00"+category] {
					continue
				}
				token, e := valuefingerprint.Create(keyHex, keyID, scope, text)
				if e != nil {
					continue
				}
				identity := path + "\x00" + category + "\x00" + token.Digest
				if seen[identity] {
					continue
				}
				seen[identity] = true
				out = append(out, ValueFingerprint{KeyID: token.KeyID, Scope: token.Scope, Version: token.Version, Digest: token.Digest, Locator: path, Category: category})
				if len(out) >= 32 {
					return
				}
			}
		}
	}
	walk(root, "", 0)
	slices.SortFunc(out, func(a, b ValueFingerprint) int {
		return strings.Compare(a.Locator+":"+a.Category+":"+a.Digest, b.Locator+":"+b.Category+":"+b.Digest)
	})
	return out
}
