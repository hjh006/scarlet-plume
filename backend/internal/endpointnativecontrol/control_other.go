//go:build !windows

package endpointnativecontrol

import "context"

func platformRead(context.Context, Target) (State, error) { return State{}, ErrUnsupported }
func platformChange(context.Context, Target, State, State) (State, error) {
	return State{}, ErrUnsupported
}
