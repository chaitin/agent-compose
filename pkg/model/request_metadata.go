package model

import "context"

// RequestMetadata is the request-scoped metadata an execution needs from the
// request that started it: trusted ingress headers and the caller's trace
// context. Executions that outlive their request run under the daemon root
// context and restore this metadata onto it instead of retaining the transport
// context, so every such path carries the same set.
type RequestMetadata struct {
	TrustedHeaders []TrustedHeader
	TraceContext   TraceContext
}

// RequestMetadataFromContext captures the request metadata stored in ctx.
func RequestMetadataFromContext(ctx context.Context) RequestMetadata {
	return RequestMetadata{
		TrustedHeaders: TrustedHeadersFromContext(ctx),
		TraceContext:   TraceContextFromContext(ctx),
	}
}

// NewContextWithRequestMetadata stores metadata in ctx.
func NewContextWithRequestMetadata(ctx context.Context, metadata RequestMetadata) context.Context {
	ctx = NewContextWithTrustedHeaders(ctx, metadata.TrustedHeaders)
	return NewContextWithTraceContext(ctx, metadata.TraceContext)
}
