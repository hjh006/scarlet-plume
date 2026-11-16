package store

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestAIOperationsCursorBindsWorkspaceTaskAndFilters(t *testing.T) {
	scope := aiOperationsScope("runtime-evidence", int64(7), int64(3), "model", "allow")
	cursor := aiOperationsNext(123, scope)
	if before, err := aiOperationsBefore(cursor, scope); err != nil || before != 123 {
		t.Fatal(before, err)
	}
	for _, foreign := range []string{
		aiOperationsScope("runtime-evidence", int64(8), int64(3), "model", "allow"),
		aiOperationsScope("runtime-evidence", int64(7), int64(4), "model", "allow"),
		aiOperationsScope("runtime-evidence", int64(7), int64(3), "tool", "allow"),
		aiOperationsScope("runtime-evidence", int64(7), int64(3), "model", "deny"),
		aiOperationsScope("task-steps", int64(7), "task-1"),
	} {
		if _, err := aiOperationsBefore(cursor, foreign); !errors.Is(err, ErrInvalid) {
			t.Fatal("cross-scope cursor accepted")
		}
	}
	for _, raw := range []string{"1", "not-base64", aiOperationsNext(0, scope), base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"before":1,"scope":"` + scope + `"}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"before":1,"scope":"` + scope + `","extra":true}`))} {
		if _, err := aiOperationsBefore(raw, scope); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid cursor accepted", raw)
		}
	}
	if _, err := aiOperationsBefore(aiOperationsNext(1, aiOperationsScope("task-steps", 7, "task-1")), aiOperationsScope("task-steps", 7, "task-2")); !errors.Is(err, ErrInvalid) {
		t.Fatal("task cursor reused")
	}
}
