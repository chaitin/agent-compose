package adapters

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/telemetry"
)

type recordingDriverRuntime struct {
	execErr error
}

func (r *recordingDriverRuntime) EnsureSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ProxyState) (driverpkg.SandboxVMInfo, error) {
	return driverpkg.SandboxVMInfo{}, nil
}

func (r *recordingDriverRuntime) StopSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) (bool, error) {
	return false, nil
}

func (r *recordingDriverRuntime) RemoveSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) error {
	return nil
}

func (r *recordingDriverRuntime) Exec(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ExecSpec) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, nil
}

func (r *recordingDriverRuntime) ExecStream(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ExecSpec, driverpkg.ExecStreamWriter) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{Output: "ok", Success: true}, r.execErr
}

func newDriverSpanRecorder(t *testing.T) (*telemetry.Tracer, *tracetest.SpanRecorder) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer provider: %v", err)
		}
	})
	return telemetry.NewTracer(provider), recorder
}

func TestDriverRuntimeAdapterExportsOperationSpans(t *testing.T) {
	tracer, recorder := newDriverSpanRecorder(t)
	adapter := driverRuntimeAdapter{runtime: &recordingDriverRuntime{}, tracer: tracer}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}}
	vmState := domain.VMState{Driver: driverpkg.RuntimeDriverDocker}

	if _, err := adapter.EnsureSandbox(context.Background(), session, vmState, domain.ProxyState{}); err != nil {
		t.Fatalf("EnsureSandbox returned error: %v", err)
	}
	if _, err := adapter.ExecStream(context.Background(), session, vmState, domain.ExecSpec{Command: "sh"}, nil); err != nil {
		t.Fatalf("ExecStream returned error: %v", err)
	}

	spans := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range recorder.Ended() {
		spans[span.Name()] = span
	}
	for _, name := range []string{telemetry.SpanSandboxEnsure, telemetry.SpanSandboxExec} {
		span, ok := spans[name]
		if !ok {
			t.Fatalf("no %s span recorded (got %#v)", name, spans)
		}
		attributes := map[attribute.Key]string{}
		for _, kv := range span.Attributes() {
			attributes[kv.Key] = kv.Value.AsString()
		}
		if attributes[telemetry.AttrSandboxID] != "sandbox-1" || attributes[telemetry.AttrDriver] != driverpkg.RuntimeDriverDocker {
			t.Fatalf("%s attributes = %#v", name, attributes)
		}
		if span.Status().Code == codes.Error {
			t.Fatalf("%s marked failed on a successful operation", name)
		}
	}
}

func TestDriverRuntimeAdapterMarksFailedOperations(t *testing.T) {
	tracer, recorder := newDriverSpanRecorder(t)
	adapter := driverRuntimeAdapter{runtime: &recordingDriverRuntime{execErr: errors.New("driver failure")}, tracer: tracer}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}}
	vmState := domain.VMState{Driver: driverpkg.RuntimeDriverDocker}

	if _, err := adapter.ExecStream(context.Background(), session, vmState, domain.ExecSpec{Command: "sh"}, nil); err == nil {
		t.Fatal("ExecStream returned no error")
	}
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	if ended[0].Status().Code != codes.Error {
		t.Fatalf("status = %v, want error", ended[0].Status().Code)
	}
}
