package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func callerSpanContext(t *testing.T, traceparent, tracestate string) trace.SpanContext {
	t.Helper()
	carrier := propagation.MapCarrier{"traceparent": traceparent}
	if tracestate != "" {
		carrier["tracestate"] = tracestate
	}
	spanContext := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), carrier))
	if !spanContext.IsValid() {
		t.Fatalf("test traceparent %q is invalid", traceparent)
	}
	return spanContext
}

func TestContextWithRemoteTraceContextBecomesParent(t *testing.T) {
	const tracestate = "vendor=value"
	want := callerSpanContext(t, testTraceparent, tracestate)
	ctx := ContextWithRemoteTraceContext(context.Background(), testTraceparent, tracestate)

	tracer, recorder := newTestTracer(t)
	_, span := tracer.Start(ctx, "child")
	span.End()

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	parent := ended[0].Parent()
	if !parent.IsRemote() || parent.TraceID() != want.TraceID() || parent.SpanID() != want.SpanID() {
		t.Fatalf("parent = %v, want remote caller context %v", parent, want)
	}
	if ended[0].SpanContext().TraceID() != want.TraceID() {
		t.Fatalf("child trace id = %s, want %s", ended[0].SpanContext().TraceID(), want.TraceID())
	}
}

func TestContextWithRemoteTraceContextIgnoresMalformedValues(t *testing.T) {
	for _, traceparent := range []string{
		"not-a-traceparent",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		testTraceparent + "-extra",
	} {
		ctx := ContextWithRemoteTraceContext(context.Background(), traceparent, "")
		if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
			t.Fatalf("malformed traceparent %q installed span context %v", traceparent, spanContext)
		}
	}
}

func TestContextWithRemoteTraceContextKeepsActiveSpan(t *testing.T) {
	tracer, recorder := newTestTracer(t)
	ctx, active := tracer.Start(context.Background(), "active")
	activeContext := active.SpanContext()
	ctx = ContextWithRemoteTraceContext(ctx, testTraceparent, "")

	_, child := tracer.Start(ctx, "child")
	child.End()
	active.End()

	for _, span := range recorder.Ended() {
		if span.Name() != "child" {
			continue
		}
		parent := span.Parent()
		if parent.IsRemote() || parent.SpanID() != activeContext.SpanID() || parent.TraceID() != activeContext.TraceID() {
			t.Fatalf("child parent = %v, want the active daemon span %v", parent, activeContext)
		}
		return
	}
	t.Fatal("child span was not recorded")
}

func TestTraceparentFromContextFormatsActiveSpan(t *testing.T) {
	tracer, _ := newTestTracer(t)
	ctx, span := tracer.Start(context.Background(), "span")
	defer span.End()

	spanContext := span.SpanContext()
	want := "00-" + spanContext.TraceID().String() + "-" + spanContext.SpanID().String() + "-" + spanContext.TraceFlags().String()
	if got := TraceparentFromContext(ctx); got != want {
		t.Fatalf("traceparent = %q, want %q", got, want)
	}
	if got := TraceparentFromContext(context.Background()); got != "" {
		t.Fatalf("traceparent without a span = %q, want empty", got)
	}
	if got := TraceparentForSpanContext(trace.SpanContext{}); got != "" {
		t.Fatalf("traceparent for an invalid span context = %q, want empty", got)
	}
	if got := TracestateFromContext(ctx); got != "" {
		t.Fatalf("tracestate for a root span = %q, want empty", got)
	}
}
