//go:build !windows

package endpointnative

import (
	"errors"
	"testing"
)

func TestClipboardSessionUnsupportedOSNeverSimulatesNativeObservation(t *testing.T) {
	if _, err := CurrentClipboardSession(); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unsupported identity reported native success")
	}
	called := false
	if err := WatchClipboardSession(t.Context(), func() bool { called = true; return true }, func(ClipboardSessionEvent) error { called = true; return nil }); !errors.Is(err, ErrUnsupported) || called {
		t.Fatal("unsupported OS executed native callbacks")
	}
	if r, err := ClearClipboardIfCurrent(t.Context(), ClipboardMatch{}); !errors.Is(err, ErrUnsupported) || r.Attempted {
		t.Fatal("unsupported OS simulated clipboard clearing")
	}
}
