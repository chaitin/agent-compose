package execution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

const (
	testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	testTracestate  = "vendor=value"
)

func TestIntegrationAgentTelemetryExecEnvironment(t *testing.T) {
	cfg := &appconfig.Config{AgentTelemetry: appconfig.AgentTelemetryConfig{Endpoint: "http://collector:4318", Headers: map[string]string{"authorization": "secret"}}}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}, EnvItems: []domain.SandboxEnvVar{{Name: "AGENT_COMPOSE_TELEMETRY", Value: "stale"}}}
	spec := BuildRuntimeCommandExecSpec(context.Background(), cfg, sandbox, "/request.json", "/root")
	var payload struct {
		Endpoint   string
		Headers    map[string]string
		Attributes map[string]string
	}
	if err := json.Unmarshal([]byte(spec.Env["AGENT_COMPOSE_TELEMETRY"]), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Endpoint != cfg.AgentTelemetry.Endpoint || payload.Headers["authorization"] != "secret" || payload.Attributes["agent_compose.sandbox.id"] != "sandbox-1" {
		t.Fatalf("unexpected telemetry payload: %+v", payload)
	}
	persisted, err := json.Marshal(sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "secret") || sandbox.EnvItems[0].Value != "stale" {
		t.Fatal("execution persisted telemetry or mutated caller state")
	}
	cfg.AgentTelemetry = appconfig.AgentTelemetryConfig{}
	restarted := BuildSandboxExecEnv(context.Background(), cfg, sandbox, "/root")
	if value, ok := restarted["AGENT_COMPOSE_TELEMETRY"]; !ok || value != "" {
		t.Fatal("disabled telemetry did not shadow stale guest config")
	}
}

func TestAgentTelemetryRelaysTraceContext(t *testing.T) {
	cfg := &appconfig.Config{AgentTelemetry: appconfig.AgentTelemetryConfig{Endpoint: "http://collector:4318"}}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}}
	const withoutTraceContext = `{"endpoint":"http://collector:4318","attributes":{"agent_compose.sandbox.id":"sandbox-1"}}`

	t.Run("valid trace context is forwarded", func(t *testing.T) {
		ctx := domain.NewContextWithTraceContext(context.Background(), domain.TraceContext{Traceparent: testTraceparent, Tracestate: testTracestate})
		env := BuildSandboxExecEnv(ctx, cfg, sandbox, "/root")
		var payload struct {
			Traceparent string            `json:"traceparent"`
			Tracestate  string            `json:"tracestate"`
			Attributes  map[string]string `json:"attributes"`
		}
		if err := json.Unmarshal([]byte(env["AGENT_COMPOSE_TELEMETRY"]), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Traceparent != testTraceparent || payload.Tracestate != testTracestate {
			t.Fatalf("trace context was not forwarded: %+v", payload)
		}
	})

	t.Run("absent trace context keeps the previous payload shape", func(t *testing.T) {
		env := BuildSandboxExecEnv(context.Background(), cfg, sandbox, "/root")
		if got := env["AGENT_COMPOSE_TELEMETRY"]; got != withoutTraceContext {
			t.Fatalf("payload changed without a trace context:\n got %s\nwant %s", got, withoutTraceContext)
		}
	})

	t.Run("malformed traceparent is omitted", func(t *testing.T) {
		for _, traceparent := range []string{
			"not-a-traceparent",
			"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
			testTraceparent + "-extra",
		} {
			ctx := domain.NewContextWithTraceContext(context.Background(), domain.TraceContext{Traceparent: traceparent})
			env := BuildSandboxExecEnv(ctx, cfg, sandbox, "/root")
			if got := env["AGENT_COMPOSE_TELEMETRY"]; got != withoutTraceContext {
				t.Fatalf("malformed traceparent %q was forwarded: %s", traceparent, got)
			}
		}
	})

	t.Run("malformed tracestate does not drop a valid traceparent", func(t *testing.T) {
		want := `{"endpoint":"http://collector:4318","traceparent":"` + testTraceparent + `","attributes":{"agent_compose.sandbox.id":"sandbox-1"}}`
		for _, tracestate := range []string{"bad\x00state", strings.Repeat("a", 600)} {
			ctx := domain.NewContextWithTraceContext(context.Background(), domain.TraceContext{Traceparent: testTraceparent, Tracestate: tracestate})
			env := BuildSandboxExecEnv(ctx, cfg, sandbox, "/root")
			if got := env["AGENT_COMPOSE_TELEMETRY"]; got != want {
				t.Fatalf("tracestate %q changed the traceparent payload:\n got %s\nwant %s", tracestate, got, want)
			}
		}
	})
}

func TestValidTraceparent(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{"version 00", testTraceparent, true},
		{"surrounding whitespace", "  " + testTraceparent + "  ", true},
		{"empty", "", false},
		{"too short", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7", false},
		{"extra field", testTraceparent + "-extra", false},
		{"uppercase hex", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01", false},
		{"zero trace id", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", false},
		{"zero span id", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", false},
		{"reserved version", "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false},
		{"unsupported version", "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false},
		{"missing separator", "00x4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false},
		{"non hex span", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902bz-01", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validTraceparent(test.value); (got != "") != test.valid {
				t.Fatalf("validTraceparent(%q) = %q, want valid=%v", test.value, got, test.valid)
			}
		})
	}
}
