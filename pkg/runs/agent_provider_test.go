package runs

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestRunProviderOverridesItsAgentDefinition(t *testing.T) {
	definition := execution.AgentConfig{Provider: "codex", AgentDefinitionID: "agent-1", Model: "gpt-x", EnvItems: []domain.SandboxEnvVar{{Name: "CODEX_HOME", Value: "/x"}}}
	if got := withRunAgentProvider(definition, ""); got.Model != "gpt-x" || len(got.EnvItems) != 1 {
		t.Fatalf("no override changed the config: %+v", got)
	}
	// Naming the definition's own provider is no override.
	if got := withRunAgentProvider(definition, "Codex"); got.Model != "gpt-x" || len(got.EnvItems) != 1 {
		t.Fatalf("same provider changed the config: %+v", got)
	}
	got := withRunAgentProvider(definition, "claude-code")
	if got.Provider != "claude" || got.AgentDefinitionID != "agent-1" || got.Model != "" || len(got.EnvItems) != 0 {
		t.Fatalf("another provider kept the definition's model or environment: %+v", got)
	}
}
