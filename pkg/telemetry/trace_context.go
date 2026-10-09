package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// ContextWithRemoteTraceContext stores the caller's W3C trace context in ctx as
// a remote parent, so a span started from the result continues the caller's
// trace. It is a no-op when ctx already carries a valid span context (for
// example a live request span), because the propagator would otherwise replace
// the recording span with a non-recording remote parent.
//
// It exists for paths that outlive their request - detached runs restore only
// the request-scoped W3C strings, not the transport context.
func ContextWithRemoteTraceContext(ctx context.Context, traceparent, tracestate string) context.Context {
	if traceparent == "" || trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": traceparent}
	if tracestate != "" {
		carrier["tracestate"] = tracestate
	}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}

// TraceparentFromContext returns the W3C traceparent of the span ctx currently
// carries, or "" when there is none. Guest providers consume this value as their
// inbound parent, so their spans join the daemon span instead of starting a
// second trace.
func TraceparentFromContext(ctx context.Context) string {
	return TraceparentForSpanContext(trace.SpanContextFromContext(ctx))
}

// TracestateFromContext returns the W3C tracestate of the span ctx currently
// carries, or "" when there is none.
func TracestateFromContext(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceState().String()
}

// TraceparentForSpanContext formats spanContext as a W3C Trace Context version
// 00 traceparent, or "" when spanContext is invalid.
func TraceparentForSpanContext(spanContext trace.SpanContext) string {
	if !spanContext.IsValid() {
		return ""
	}
	return "00-" + spanContext.TraceID().String() + "-" + spanContext.SpanID().String() + "-" + spanContext.TraceFlags().String()
}
