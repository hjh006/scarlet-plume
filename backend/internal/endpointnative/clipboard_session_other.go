//go:build !windows

package endpointnative

import "context"

func CurrentClipboardSession() (ClipboardSessionIdentity, error) {
	return ClipboardSessionIdentity{}, ErrUnsupported
}
func WatchClipboardSession(ctx context.Context, shouldCapture func() bool, emit func(ClipboardSessionEvent) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUnsupported
}
func ClearClipboardIfCurrent(ctx context.Context, m ClipboardMatch) (ClipboardClearResult, error) {
	return ClipboardClearResult{Status: "unsupported", Reason: "unsupported_os"}, ErrUnsupported
}
