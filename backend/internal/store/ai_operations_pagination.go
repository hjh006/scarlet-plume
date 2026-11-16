package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
)

// The cursor is a page selector, not a credential. Every query still enforces
// membership and workspace/task scope; the fingerprint prevents accidental reuse
// after switching workspaces, tasks or filters.
type aiOperationsCursor struct {
	Version int    `json:"v"`
	Before  int64  `json:"before"`
	Scope   string `json:"scope"`
}

func aiOperationsScope(values ...any) string {
	b, _ := json.Marshal(values)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func aiOperationsBefore(cursor, scope string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	if len(cursor) > 512 {
		return 0, ErrInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrInvalid
	}
	var c aiOperationsCursor
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || c.Before < 1 || c.Scope != scope {
		return 0, ErrInvalid
	}
	return c.Before, nil
}

func aiOperationsNext(before int64, scope string) string {
	b, _ := json.Marshal(aiOperationsCursor{Version: 1, Before: before, Scope: scope})
	return base64.RawURLEncoding.EncodeToString(b)
}
