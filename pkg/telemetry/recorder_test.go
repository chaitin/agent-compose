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

func newSpanRecorder(t *testing.T) (*Recorder, *tracetest.SpanRecorder) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer provider: %v", err)
		}
	})
	recorder, err := NewRecorder(provider, nil)
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	return recorder, spans
}

func TestRecorderDisabledStartsNoopSpans(t *testing.T) {
	ctx := context.Background()
	recorder, err := NewRecorder(nil, nil)
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	gotCtx, span := recorder.Start(ctx, SpanInvokeAgent, trace.WithAttributes(AttrRunID.String("run-1")))
	if gotCtx != ctx {
		t.Fatal("disabled recorder replaced the context")
	}
	if trace.SpanContextFromContext(gotCtx).IsValid() {
		t.Fatal("disabled recorder installed a span context")
	}
	// Attribute and status calls on a disabled span must be safe no-ops.
	span.SetAttributes(AttrRunID.String("run-1"))
	EndSpan(span, errors.New("boom"))
	// Metric recordings without a meter provider must be safe no-ops too.
	recorder.RecordRun(ctx, RunMeasurement{Duration: 0, Status: "succeeded"})
	recorder.RecordSandboxCreate(ctx, "docker", 0, nil)
	recorder.RecordDriverOperation(ctx, SpanSandboxExec, "docker", errors.New("boom"))
}

func TestRecorderExportsSpanAttributes(t *testing.T) {
	recorder, spans := newSpanRecorder(t)
	_, span := recorder.Start(context.Background(), SpanInvokeAgent, trace.WithAttributes(
		AttrRunID.String("run-1"),
		AttrProjectID.String("project-1"),
	))
	EndSpan(span, nil)

	ended := spans.Ended()
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
	recorder, spans := newSpanRecorder(t)
	_, span := recorder.Start(context.Background(), SpanSandboxExec)
	EndSpan(span, errors.New("prompt content must not be exported"))

	ended := spans.Ended()
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
