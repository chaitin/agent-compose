package runs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/telemetry"
)

func TestRunsControllerRunSpanJoinsCallerTrace(t *testing.T) {
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	wantTraceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}
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
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer provider: %v", err)
		}
	})
	telemetryRecorder, err := telemetry.NewRecorder(provider, nil)
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
		Runtime:          func(*domain.Sandbox) (Runtime, error) { return &fakeControllerRuntime{}, nil },
		Images:           fakeControllerImages{},
		Recorder:         telemetryRecorder,
	})
	ctx := domain.NewContextWithTraceContext(context.Background(), domain.TraceContext{Traceparent: traceparent})
	stream := &StreamSink{
		SendStarted: func(domain.ProjectRunRecord, time.Time) error { return nil },
		SendChunk:   func(string, domain.ExecChunk, time.Time) error { return nil },
	}
	run, execErr, err := controller.RunProjectAgent(ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Command:         "echo command",
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "span-request",
	}, stream)
	if err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
	}

	var runSpan sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == telemetry.SpanInvokeAgent {
			runSpan = span
		}
	}
	if runSpan == nil {
		t.Fatalf("no %s span recorded", telemetry.SpanInvokeAgent)
	}
	if runSpan.SpanContext().TraceID() != wantTraceID {
		t.Fatalf("run trace id = %s, want caller trace %s", runSpan.SpanContext().TraceID(), wantTraceID)
	}
	if !runSpan.Parent().IsRemote() || runSpan.Parent().TraceID() != wantTraceID {
		t.Fatalf("run parent = %v, want the remote caller context", runSpan.Parent())
	}
	attributes := map[attribute.Key]string{}
	for _, kv := range runSpan.Attributes() {
		attributes[kv.Key] = kv.Value.AsString()
	}
	if attributes[telemetry.AttrRunID] != run.RunID ||
		attributes[telemetry.AttrProjectID] != "project-1" ||
		attributes[telemetry.AttrAgentName] != "worker" ||
		attributes[telemetry.AttrSandboxID] != run.SandboxID {
		t.Fatalf("run span attributes = %#v", attributes)
	}
	if runSpan.Status().Code == codes.Error {
		t.Fatalf("successful run marked failed: %v", runSpan.Status())
	}
}
