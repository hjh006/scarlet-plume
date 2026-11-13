package datasafety

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// ParsePolicy rejects misspelled or extra settings instead of silently disabling protection.
func ParsePolicy(raw []byte) (Policy, error) {
	var p Policy
	if len(raw) > 16<<10 || !utf8.Valid(raw) {
		return p, ErrContext
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.Validate() != nil {
		return Policy{}, ErrContext
	}
	return p, nil
}
