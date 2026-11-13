//go:build !windows

package endpointnative

import "context"

// Watch does not emulate native Windows observations on other platforms.
func Watch(ctx context.Context, emit func(Observation) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUnsupported
}
