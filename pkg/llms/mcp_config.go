package llms

import (
	"encoding/json"
	"strings"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

type AgentConfigPayload struct {
	Jupyter    *compose.JupyterSpec                       `json:"jupyter,omitempty"`
	MCPServers map[string]compose.NormalizedMCPServerSpec `json:"mcp_servers,omitempty"`
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
	if len(payload.MCPServers) == 0 {
		return nil
	}
	return payload.MCPServers
}
