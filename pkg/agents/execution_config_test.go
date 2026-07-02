package agents

import (
	"strings"
	"testing"
)

func TestAgentExecutionConfigFromDefinition(t *testing.T) {
	tests := []struct {
		name             string
		agent            AgentDefinition
		fallbackProvider string
		wantProvider     string
		wantModel        string
	}{
		{
			name: "uses normalized provider and model",
			agent: AgentDefinition{
				ID:       " agent-1 ",
				Provider: "claude-code",
				Model:    " claude-test ",
			},
			wantProvider: "claude",
			wantModel:    "claude-test",
		},
		{
			name: "uses normalized fallback provider",
			agent: AgentDefinition{
				ID:    "agent-2",
				Model: "fallback-model",
			},
			fallbackProvider: "gemini-cli",
			wantProvider:     "gemini",
			wantModel:        "fallback-model",
		},
		{
			name: "opencode model comes from OPENCODE_MODEL env",
			agent: AgentDefinition{
				ID:       "agent-3",
				Provider: "open-code",
				Model:    "ignored-model",
				EnvItems: []SessionEnvVar{
					{Name: "OPENCODE_MODEL", Value: "openai/old"},
					{Name: " OPENCODE_MODEL ", Value: "anthropic/claude-sonnet-4-5"},
				},
			},
			wantProvider: "opencode",
			wantModel:    "anthropic/claude-sonnet-4-5",
		},
		{
			name: "opencode with no env model returns empty model",
			agent: AgentDefinition{
				ID:       "agent-4",
				Provider: "opencode",
				Model:    "ignored-model",
			},
			wantProvider: "opencode",
			wantModel:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AgentExecutionConfigFromDefinition(tt.agent, tt.fallbackProvider)
			if got.Provider != tt.wantProvider {
				t.Fatalf("Provider = %q, want %q", got.Provider, tt.wantProvider)
			}
			if got.Model != tt.wantModel {
				t.Fatalf("Model = %q, want %q", got.Model, tt.wantModel)
			}
			wantAgentDefinitionID := strings.TrimSpace(tt.agent.ID)
			if got.AgentDefinitionID != wantAgentDefinitionID {
				t.Fatalf("AgentDefinitionID = %q, want %q", got.AgentDefinitionID, wantAgentDefinitionID)
			}
		})
	}
}

func TestAgentExecutionConfigFromDefinitionCopiesEnvItems(t *testing.T) {
	agent := AgentDefinition{
		ID:       "agent-copy",
		Provider: "codex",
		EnvItems: []SessionEnvVar{
			{Name: "A", Value: "1"},
		},
	}
	got := AgentExecutionConfigFromDefinition(agent, "")
	agent.EnvItems[0].Value = "mutated"
	if got.EnvItems[0].Value != "1" {
		t.Fatalf("EnvItems were not copied: %#v", got.EnvItems)
	}
}
