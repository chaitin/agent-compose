package execution

import (
	"context"
	"encoding/json"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// The exec command path reaches the guest environment through
// BuildRuntimeCommandExecSpec, while the agent path reaches it through
// RunAgent. Both share BuildSandboxExecEnv, so this asserts the trace context
// survives the exec call site too rather than trusting the shared helper alone.
func TestRuntimeCommandExecSpecRelaysTraceContext(t *testing.T) {
	cfg := &appconfig.Config{AgentTelemetry: appconfig.AgentTelemetryConfig{Endpoint: "http://collector:4318"}}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}}

	ctx := domain.NewContextWithTraceContext(context.Background(), domain.TraceContext{Traceparent: testTraceparent, Tracestate: testTracestate})
	spec := BuildRuntimeCommandExecSpec(ctx, cfg, sandbox, "/request.json", "/root")
	var payload struct {
		Traceparent string `json:"traceparent"`
		Tracestate  string `json:"tracestate"`
	}
	if err := json.Unmarshal([]byte(spec.Env["AGENT_COMPOSE_TELEMETRY"]), &payload); err != nil {
		t.Fatalf("decode telemetry payload: %v", err)
	}
	if payload.Traceparent != testTraceparent || payload.Tracestate != testTracestate {
		t.Fatalf("exec spec trace context = %+v, want %s / %s", payload, testTraceparent, testTracestate)
	}

	// An exec without a caller trace context keeps the previous payload shape.
	withoutTraceContext := BuildRuntimeCommandExecSpec(context.Background(), cfg, sandbox, "/request.json", "/root")
	const want = `{"endpoint":"http://collector:4318","attributes":{"agent_compose.sandbox.id":"sandbox-1"}}`
	if got := withoutTraceContext.Env["AGENT_COMPOSE_TELEMETRY"]; got != want {
		t.Fatalf("payload without a trace context changed:\n got %s\nwant %s", got, want)
	}
}
