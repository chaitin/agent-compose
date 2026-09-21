package execution

import (
	"encoding/json"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestIntegrationAgentTelemetryExecEnvironment(t *testing.T) {
	cfg := &appconfig.Config{AgentTelemetry: appconfig.AgentTelemetryConfig{Endpoint: "http://collector:4318", Headers: map[string]string{"authorization": "secret"}}}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}, EnvItems: []domain.SandboxEnvVar{{Name: "AGENT_COMPOSE_TELEMETRY", Value: "stale"}}}
	spec := BuildRuntimeCommandExecSpec(cfg, sandbox, "/request.json", "/root")
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
	restarted := BuildSandboxExecEnv(cfg, sandbox, "/root")
	if value, ok := restarted["AGENT_COMPOSE_TELEMETRY"]; !ok || value != "" {
		t.Fatal("disabled telemetry did not shadow stale guest config")
	}
}
