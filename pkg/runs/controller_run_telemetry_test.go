package runs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/telemetry"
)

const runSpanTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

const runSpanTraceIDHex = "4bf92f3577b34da6a3ce929d0e0e4736"

// newTracedRunController builds a controller whose run telemetry exports spans
// and metrics to in-memory readers. The runtime and volume resolver are
// injected so a test can observe the context the run hands to each layer.
func newTracedRunController(t *testing.T, runtime Runtime, volumeResolver VolumeResolver) (*Controller, *tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		RuntimeDriver:      driverpkg.RuntimeDriverDocker,
		DefaultImage:       "guest:latest",
		DockerDefaultImage: "guest:latest",
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	configDB := &fakeControllerStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		projectAgent: domain.ProjectAgentRecord{
			ProjectID: "project-1", AgentName: "worker", ID: "agent-1", Driver: driverpkg.RuntimeDriverDocker, Image: "guest:latest",
		},
		managed: domain.AgentDefinition{
			ID: "agent-1", Enabled: true, Driver: driverpkg.RuntimeDriverDocker, GuestImage: "guest:latest", ProjectID: "project-1", AgentName: "worker",
		},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: `{"agents":[{"name":"worker","provider":"codex"}]}`},
		agent:    domain.AgentDefinition{ID: "agent-1", Provider: "codex"},
		runs:     map[string]domain.ProjectRunRecord{},
	}
	spans := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	metricReader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricReader))
	t.Cleanup(func() {
		if err := tracerProvider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer provider: %v", err)
		}
		if err := meterProvider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown meter provider: %v", err)
		}
	})
	recorder, err := telemetry.NewRecorder(tracerProvider, meterProvider)
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	controller := NewController(ControllerDependencies{
		Config:           config,
		Store:            store,
		ConfigDB:         configDB,
		WorkspaceEnsurer: &controllerWorkspaceEnsurer{},
		Driver:           &fakeControllerDriver{store: store},
		Executor:         &fakeControllerExecutor{},
		Runtime:          func(*domain.Sandbox) (Runtime, error) { return runtime, nil },
		Images:           fakeControllerImages{},
		Volumes:          volumeResolver,
		Recorder:         recorder,
	})
	return controller, spans, metricReader
}

func commandRunStream() *StreamSink {
	return &StreamSink{
		SendStarted: func(domain.ProjectRunRecord, time.Time) error { return nil },
		SendChunk:   func(string, domain.ExecChunk, time.Time) error { return nil },
	}
}

func commandRunRequest() RunAgentRequest {
	return RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Command:         "echo command",
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "run-telemetry-request",
	}
}

// childSpanRuntime records the span context the run hands to the runtime layer
// and opens the driver operation span the real adapter would open.
type childSpanRuntime struct {
	fakeControllerRuntime
	recorder *telemetry.Recorder
	received chan trace.SpanContext
}

func (r *childSpanRuntime) ExecStream(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, spec domain.ExecSpec, writer domain.ExecStreamWriter) (domain.ExecResult, error) {
	r.received <- trace.SpanContextFromContext(ctx)
	_, span := r.recorder.Start(ctx, telemetry.SpanSandboxExec)
	telemetry.EndSpan(span, nil)
	return r.fakeControllerRuntime.ExecStream(ctx, session, vmState, spec, writer)
}

func TestRunsControllerRunSpanContainsRuntimeChildSpans(t *testing.T) {
	runtime := &childSpanRuntime{received: make(chan trace.SpanContext, 1)}
	controller, spans, _ := newTracedRunController(t, runtime, nil)
	runtime.recorder = controller.recorder

	if _, execErr, err := controller.RunProjectAgent(context.Background(), commandRunRequest(), commandRunStream()); err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v", err, execErr)
	}

	runSpan := findEndedSpan(t, spans, telemetry.SpanInvokeAgent)
	execSpan := findEndedSpan(t, spans, telemetry.SpanSandboxExec)
	if execSpan.Parent().SpanID() != runSpan.SpanContext().SpanID() || execSpan.SpanContext().TraceID() != runSpan.SpanContext().TraceID() {
		t.Fatalf("exec span parent = %v, want run span %v", execSpan.Parent(), runSpan.SpanContext())
	}
	select {
	case received := <-runtime.received:
		if received.SpanID() != runSpan.SpanContext().SpanID() {
			t.Fatalf("runtime received span %v, want the run span %v", received, runSpan.SpanContext())
		}
	default:
		t.Fatal("runtime never received the run context")
	}
}

func TestRunsControllerDetachedRunSpanJoinsCallerTrace(t *testing.T) {
	wantTraceID, err := trace.TraceIDFromHex(runSpanTraceIDHex)
	if err != nil {
		t.Fatal(err)
	}
	controller, spans, _ := newTracedRunController(t, &fakeControllerRuntime{}, nil)
	requestCtx := domain.NewContextWithTraceContext(context.Background(), domain.TraceContext{Traceparent: runSpanTraceparent})
	started, err := controller.StartProjectRun(requestCtx, commandRunRequest())
	if err != nil {
		t.Fatalf("StartProjectRun returned error: %v", err)
	}
	if _, execErr, err := started.Execute(context.Background(), commandRunStream()); err != nil || execErr != nil {
		t.Fatalf("detached Execute err=%v execErr=%v", err, execErr)
	}

	runSpan := findEndedSpan(t, spans, telemetry.SpanInvokeAgent)
	if runSpan.SpanContext().TraceID() != wantTraceID {
		t.Fatalf("detached run trace id = %s, want caller trace %s", runSpan.SpanContext().TraceID(), wantTraceID)
	}
	if !runSpan.Parent().IsRemote() || runSpan.Parent().TraceID() != wantTraceID {
		t.Fatalf("detached run parent = %v, want the remote caller context", runSpan.Parent())
	}
}

func TestRunsControllerRecordsRunMetrics(t *testing.T) {
	controller, _, metricReader := newTracedRunController(t, &fakeControllerRuntime{}, nil)
	if _, execErr, err := controller.RunProjectAgent(context.Background(), commandRunRequest(), commandRunStream()); err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v", err, execErr)
	}

	metrics := collectRunMetrics(t, metricReader)
	duration, ok := metrics[telemetry.MetricRunDuration].Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("run duration data = %T, want histogram", metrics[telemetry.MetricRunDuration].Data)
	}
	if len(duration.DataPoints) != 1 || duration.DataPoints[0].Count != 1 {
		t.Fatalf("run duration data points = %#v, want one observation", duration.DataPoints)
	}
	count, ok := metrics[telemetry.MetricRunCount].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("run count data = %T, want sum", metrics[telemetry.MetricRunCount].Data)
	}
	if len(count.DataPoints) != 1 || count.DataPoints[0].Value != 1 {
		t.Fatalf("run count data points = %#v, want a single completed run", count.DataPoints)
	}
	if got := metricAttribute(count.DataPoints[0].Attributes, telemetry.AttrRunStatus); got != domain.ProjectRunStatusSucceeded {
		t.Fatalf("run status = %q, want %q", got, domain.ProjectRunStatusSucceeded)
	}
	if got := metricAttribute(count.DataPoints[0].Attributes, telemetry.AttrDriver); got != driverpkg.RuntimeDriverDocker {
		t.Fatalf("run driver = %q, want %q (config default)", got, driverpkg.RuntimeDriverDocker)
	}
}

func TestRunsControllerTracesVolumePreparation(t *testing.T) {
	resolver := &fakeVolumeResolver{mounts: []domain.SandboxVolumeMount{{Type: domain.VolumeMountTypeBind, Source: "/host/data", Target: "/data"}}}
	controller, spans, _ := newTracedRunController(t, &fakeControllerRuntime{}, resolver)
	request := commandRunRequest()
	request.Volumes = []domain.VolumeMountSpec{{Type: domain.VolumeMountTypeBind, Source: "/host/data", Target: "/data"}}
	if _, execErr, err := controller.RunProjectAgent(context.Background(), request, commandRunStream()); err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v", err, execErr)
	}

	volumeSpan := findEndedSpan(t, spans, telemetry.SpanVolumePrepare)
	if volumeSpan.Parent().SpanID() != findEndedSpan(t, spans, telemetry.SpanInvokeAgent).SpanContext().SpanID() {
		t.Fatalf("volume span parent = %v, want the run span", volumeSpan.Parent())
	}
	volumeAttributes := attribute.NewSet(volumeSpan.Attributes()...)
	mountCount, ok := volumeAttributes.Value(telemetry.AttrVolumeMountCount)
	if !ok || mountCount.AsInt64() != 1 {
		t.Fatalf("volume mount count = %v (present %t), want 1", mountCount, ok)
	}
	if len(resolver.specs) != 1 {
		t.Fatalf("resolved specs = %#v, want one mount", resolver.specs)
	}
}

func findEndedSpan(t *testing.T, spans *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans.Ended() {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("no %s span recorded", name)
	return nil
}

func collectRunMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var resourceMetrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &resourceMetrics); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	collected := map[string]metricdata.Metrics{}
	for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
		for _, metric := range scopeMetrics.Metrics {
			collected[metric.Name] = metric
		}
	}
	return collected
}

func metricAttribute(attributes attribute.Set, key attribute.Key) string {
	value, ok := attributes.Value(key)
	if !ok {
		return ""
	}
	return value.AsString()
}
