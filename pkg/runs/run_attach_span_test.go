package runs

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/telemetry"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func newAttachSpanRecorder(t *testing.T) (*telemetry.Recorder, *tracetest.SpanRecorder) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer provider: %v", err)
		}
	})
	recorder, err := telemetry.NewRecorder(provider, nil)
	if err != nil {
		t.Fatalf("NewRecorder returned error: %v", err)
	}
	return recorder, spans
}

// TestRunsControllerAttachRunSpanJoinsCallerTrace covers the attach path's half
// of the trace contract: a new-run attach opens the same invoke_agent span as a
// streamed run and hands the span-carrying context to the runtime interaction.
func TestRunsControllerAttachRunSpanJoinsCallerTrace(t *testing.T) {
	wantTraceID, err := trace.TraceIDFromHex(runSpanTraceIDHex)
	if err != nil {
		t.Fatal(err)
	}
	controller, _, runtime := newTestRunAttachController(t, []driverpkg.RuntimeOutputFrame{
		{Type: driverpkg.RuntimeOutputStarted},
		{Type: driverpkg.RuntimeOutputResult, Result: &driverpkg.RuntimeResult{OperationID: "run-attach", ExitCode: 0, Success: true}},
	})
	telemetryRecorder, spanRecorder := newAttachSpanRecorder(t)
	controller.recorder = telemetryRecorder

	ctx := domain.NewContextWithTraceContext(context.Background(), domain.TraceContext{Traceparent: runSpanTraceparent})
	requests := []*agentcomposev2.AttachAgentRunRequest{{
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
			Request: &agentcomposev2.RunAgentRequest{
				ProjectId:       "project-1",
				AgentName:       "worker",
				Command:         "echo hello",
				Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
				ClientRequestId: "attach-span-request",
			},
			Mode:        agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND,
			AttachStdin: true,
		}},
	}}
	err = controller.RunProjectCommandAttach(ctx, recvAttachAgentRunRequests(requests), func(RunAttachOutput) error { return nil })
	if err != nil {
		t.Fatalf("RunProjectCommandAttach returned error: %v", err)
	}

	runSpan := findEndedSpan(t, spanRecorder, telemetry.SpanInvokeAgent)
	if runSpan.SpanContext().TraceID() != wantTraceID {
		t.Fatalf("attach run trace id = %s, want caller trace %s", runSpan.SpanContext().TraceID(), wantTraceID)
	}
	var sandboxID string
	for _, attr := range runSpan.Attributes() {
		if attr.Key == telemetry.AttrSandboxID {
			sandboxID = attr.Value.AsString()
		}
	}
	if sandboxID == "" {
		t.Fatal("attach run span has no sandbox id")
	}
	if runtime.openCtx == nil {
		t.Fatal("runtime interaction received no context")
	}
	interactionSpan := trace.SpanContextFromContext(runtime.openCtx)
	if interactionSpan.SpanID() != runSpan.SpanContext().SpanID() || interactionSpan.TraceID() != wantTraceID {
		t.Fatalf("interaction span = %v, want the attach run span %v", interactionSpan, runSpan.SpanContext())
	}
}
