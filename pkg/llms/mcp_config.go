package llms

import (
	"encoding/json"
	"strings"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

type AgentConfigPayload struct {
	Jupyter *compose.JupyterSpec                        `json:"jupyter,omitempty"`
	MCPs    map[string]compose.NormalizedMCPServerSpec `json:"mcps,omitempty"`
}

func AgentMCPConfig(definition domain.AgentDefinition) map[string]compose.NormalizedMCPServerSpec {
	raw := strings.TrimSpace(definition.ConfigJSON)
	if raw == "" || raw == "{}" {
		return nil
	}
	var payload AgentConfigPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	if len(payload.MCPs) == 0 {
		return nil
	}
	return payload.MCPs
}
