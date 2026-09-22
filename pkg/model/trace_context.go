package model

import "context"

type traceContextContextKey struct{}

// TraceContext is the W3C trace context a caller propagated on the request that
// started an execution. It is request-scoped metadata, like TrustedHeader: it
// is relayed to the sandbox that executes the request and is never persisted
// with sandbox, run, or project state.
type TraceContext struct {
	Traceparent string
	Tracestate  string
}

// NewContextWithTraceContext stores the caller's trace context in ctx.
func NewContextWithTraceContext(ctx context.Context, traceContext TraceContext) context.Context {
	return context.WithValue(ctx, traceContextContextKey{}, traceContext)
}

// TraceContextFromContext retrieves the trace context. It returns the zero
// value when no trace context was stored.
func TraceContextFromContext(ctx context.Context) TraceContext {
	traceContext, _ := ctx.Value(traceContextContextKey{}).(TraceContext)
	return traceContext
}
