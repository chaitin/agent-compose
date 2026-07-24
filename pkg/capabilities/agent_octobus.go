package capabilities

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

// AgentOctoBusServers decodes the project OctoBus servers selected for a
// managed agent. Empty configuration is valid for legacy definitions.
func AgentOctoBusServers(definition domain.AgentDefinition) (map[string]compose.NormalizedOctoBusServerSpec, error) {
	raw := strings.TrimSpace(definition.ConfigJSON)
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	var config struct {
		OctoBusServers map[string]compose.NormalizedOctoBusServerSpec `json:"octobus_servers"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return nil, fmt.Errorf("decode agent definition octobus servers: %w", err)
	}
	return config.OctoBusServers, nil
}
