package valuefingerprint

import (
	"strings"
	"testing"
)

func TestFingerprintEqualityScopeAndExactVersion(t *testing.T) {
	key := strings.Repeat("19", 32)
	a, e := Create(key, "key-1", "workspace-a", "alice@example.invalid")
	if e != nil || !Valid(a) {
		t.Fatal(e)
	}
	same, _ := Create(key, "key-1", "workspace-a", "alice@example.invalid")
	other, _ := Create(key, "key-1", "workspace-b", "alice@example.invalid")
	different, _ := Create(key, "key-1", "workspace-a", "Alice@example.invalid")
	if a != same || a.Digest == other.Digest || a.Digest == different.Digest {
		t.Fatal("scope or exact-value semantics lost")
	}
	if _, e = Create("short", "key-1", "workspace-a", "value"); e == nil {
		t.Fatal("invalid key accepted")
	}
	if _, e = Create(key, "key-1", "workspace-a", strings.Repeat("x", 4097)); e == nil {
		t.Fatal("value budget ignored")
	}
}
