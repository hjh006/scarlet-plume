package aisecurity

import (
	"context"
	"errors"
)

type gatewayIdentityKey struct{}
type gatewayCredentialHashKey struct{}

func (h *runtimeHandler) authenticate(ctx context.Context, tokenHash string) (Identity, error) {
	var identity Identity
	var err error
	if h.options.Authenticate != nil {
		identity, err = h.options.Authenticate(ctx, tokenHash)
	} else {
		var ok bool
		identity, ok = h.identities[tokenHash]
		if !ok {
			err = errors.New("credential unknown")
		}
	}
	if err != nil || !validID(identity.ApplicationID) || !validID(identity.PrincipalID) {
		return Identity{}, errors.New("credential unknown or revoked")
	}
	if expected, ok := ctx.Value(gatewayIdentityKey{}).(Identity); ok && expected != identity {
		return Identity{}, errors.New("authenticated identity changed")
	}
	return identity, nil
}
