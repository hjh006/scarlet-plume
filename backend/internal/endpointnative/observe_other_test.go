//go:build !windows

package endpointnative

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupportedPlatformDoesNotObserve(t *testing.T) {
	called := false
	if err := Watch(context.Background(), func(Observation) error { called = true; return nil }); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected unsupported platform, got %v", err)
	}
	if called {
		t.Fatal("unsupported observer emitted a synthetic native event")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Watch(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not preserved")
	}
}
