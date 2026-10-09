package telemetry

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func newTestTracer(t *testing.T) (*Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer provider: %v", err)
		}
	})
	return NewTracer(provider), recorder
}

func TestTracerDisabledStartsNoopSpans(t *testing.T) {
	ctx := context.Background()
	gotCtx, span := NewTracer(nil).Start(ctx, SpanInvokeAgent, trace.WithAttributes(AttrRunID.String("run-1")))
	if gotCtx != ctx {
		t.Fatal("disabled tracer replaced the context")
	}
	if trace.SpanContextFromContext(gotCtx).IsValid() {
		t.Fatal("disabled tracer installed a span context")
	}
	// Attribute and status calls on a disabled span must be safe no-ops.
	span.SetAttributes(AttrRunID.String("run-1"))
	EndSpan(span, errors.New("boom"))
}

func TestTracerExportsSpanAttributes(t *testing.T) {
	tracer, recorder := newTestTracer(t)
	_, span := tracer.Start(context.Background(), SpanInvokeAgent, trace.WithAttributes(
		AttrRunID.String("run-1"),
		AttrProjectID.String("project-1"),
	))
	EndSpan(span, nil)

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	if ended[0].Name() != SpanInvokeAgent {
		t.Fatalf("span name = %q, want %q", ended[0].Name(), SpanInvokeAgent)
	}
	if ended[0].Status().Code != codes.Unset {
		t.Fatalf("status = %v, want unset for a successful operation", ended[0].Status().Code)
	}
	attrs := map[attribute.Key]string{}
	for _, kv := range ended[0].Attributes() {
		attrs[kv.Key] = kv.Value.AsString()
	}
	if attrs[AttrRunID] != "run-1" || attrs[AttrProjectID] != "project-1" {
		t.Fatalf("attributes = %#v", attrs)
	}
}

func TestEndSpanMarksFailuresWithoutExportingErrorText(t *testing.T) {
	tracer, recorder := newTestTracer(t)
	_, span := tracer.Start(context.Background(), SpanSandboxExec)
	EndSpan(span, errors.New("prompt content must not be exported"))

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	if ended[0].Status().Code != codes.Error {
		t.Fatalf("status = %v, want error", ended[0].Status().Code)
	}
	if ended[0].Status().Description != "" {
		t.Fatalf("status description = %q, want empty", ended[0].Status().Description)
	}
}
