package capabilities

import (
	"testing"

	domain "agent-compose/pkg/model"
)

func TestAgentOctoBusServersRejectsMalformedConfig(t *testing.T) {
	_, err := AgentOctoBusServers(domain.AgentDefinition{ConfigJSON: "{"})
	if err == nil {
		t.Fatal("AgentOctoBusServers returned nil error for malformed config")
	}
}
