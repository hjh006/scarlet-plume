//go:build !windows

package endpointsecure

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedHostNeverCreatesPlaintextFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "protected-store")
	if _, err := Open(path); !errors.Is(err, ErrUnavailable) {
		t.Fatal("non-Windows DPAPI store accepted")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsupported host created a fallback directory")
	}
}
