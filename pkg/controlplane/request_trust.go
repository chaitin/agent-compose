package controlplane

import "context"

type trustedLocalRequestKey struct{}

// WithTrustedLocalRequest records that the daemon verified a local transport
// peer. Callers must only use it after authenticating the peer at the listener.
func WithTrustedLocalRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, trustedLocalRequestKey{}, true)
}

// IsTrustedLocalRequest reports whether the daemon verified the request's
// transport peer as local and privileged.
func IsTrustedLocalRequest(ctx context.Context) bool {
	trusted, _ := ctx.Value(trustedLocalRequestKey{}).(bool)
	return trusted
}
