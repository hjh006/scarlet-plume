//go:build !windows

package endpointnativecontrol

import (
	"errors"
	"testing"
)

func TestOtherPlatformNeverEmulatesWindowsControl(t *testing.T) {
	target := fixedTarget(USBWrite)
	m, err := New([]Target{target})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Capture(t.Context(), target.TargetRef); !errors.Is(err, ErrUnsupported) {
		t.Fatal("non-Windows native control pretended to run")
	}
}
