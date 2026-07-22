package execution

import (
	"testing"

	domain "agent-compose/pkg/model"
)

func TestAgentConfigFromDefinitionPreservesPiModel(t *testing.T) {
	config := AgentConfigFromDefinition(domain.AgentDefinition{
		ID:       " pi-reviewer ",
		Provider: "pi-agent",
		Model:    " openai/gpt-5.4 ",
		EnvItems: []domain.SandboxEnvVar{{Name: "OPENCODE_MODEL", Value: "ignored"}},
	}, "codex")

	if config.Provider != "pi" || config.AgentDefinitionID != "pi-reviewer" || config.Model != "openai/gpt-5.4" {
		t.Fatalf("AgentConfigFromDefinition returned %#v", config)
	}
}
