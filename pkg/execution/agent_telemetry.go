package execution

import (
	"encoding/json"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ApplyAgentTelemetryEnv passes daemon-owned telemetry settings to this execution
// only. It never persists collector credentials in sandbox configuration.
func ApplyAgentTelemetryEnv(config *appconfig.Config, sandbox *domain.Sandbox, env map[string]string) {
	// An empty value also shadows stale values in a resumed guest's environment.
	env["AGENT_COMPOSE_TELEMETRY"] = ""
	if config.AgentTelemetry.Endpoint == "" {
		return
	}
	payload := struct {
		appconfig.AgentTelemetryConfig
		Attributes map[string]string `json:"attributes"`
	}{config.AgentTelemetry, map[string]string{"agent_compose.sandbox.id": sandbox.Summary.ID}}
	// The payload contains only strings, booleans and maps of strings; JSON
	// marshaling cannot fail for these types.
	encoded, _ := json.Marshal(payload) //nolint:errchkjson // Only strings, booleans and string maps; see invariant above.
	env["AGENT_COMPOSE_TELEMETRY"] = string(encoded)
}
