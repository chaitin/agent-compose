package agents

import (
	"context"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
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

func TestAgentExecutionConfigForSession(t *testing.T) {
	ctx := context.Background()
	configDB := newTestConfigStore(t)
	agent, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
		ID:       "agent-session",
		Name:     "Agent Session",
		Provider: "open-code",
		Model:    "ignored-model",
		EnvItems: []SessionEnvVar{
			{Name: "ANTHROPIC_API_KEY", Value: "agent-key", Secret: true},
			{Name: "OPENCODE_MODEL", Value: "anthropic/claude-sonnet-4-5"},
		},
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}

	tests := []struct {
		name      string
		configDB  *ConfigStore
		session   *Session
		requested string
		want      AgentExecutionConfig
	}{
		{
			name:      "nil session returns requested provider",
			configDB:  configDB,
			requested: "gemini-cli",
			want:      AgentExecutionConfig{Provider: "gemini"},
		},
		{
			name:     "missing agent tag returns requested provider",
			configDB: configDB,
			session: &model.Session{Summary: model.SessionSummary{Tags: []model.SessionTag{
				{Name: agentSessionTagID, Value: agent.ID},
			}}},
			requested: "claude-code",
			want:      AgentExecutionConfig{Provider: "claude"},
		},
		{
			name:      "missing definition returns requested provider",
			configDB:  configDB,
			session:   agentSession("missing-agent"),
			requested: "codex",
			want:      AgentExecutionConfig{Provider: "codex"},
		},
		{
			name:      "nil config store returns requested provider",
			configDB:  nil,
			session:   agentSession(agent.ID),
			requested: "gemini",
			want:      AgentExecutionConfig{Provider: "gemini"},
		},
		{
			name:      "tagged session uses stored agent definition",
			configDB:  configDB,
			session:   agentSession(agent.ID),
			requested: "codex",
			want: AgentExecutionConfig{
				Provider:          "opencode",
				AgentDefinitionID: agent.ID,
				Model:             "anthropic/claude-sonnet-4-5",
				EnvItems:          agent.EnvItems,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AgentExecutionConfigForSession(ctx, tt.configDB, tt.session, tt.requested)
			if got.Provider != tt.want.Provider || got.AgentDefinitionID != tt.want.AgentDefinitionID || got.Model != tt.want.Model {
				t.Fatalf("config = %#v, want %#v", got, tt.want)
			}
			if len(tt.want.EnvItems) == 0 {
				if len(got.EnvItems) != 0 {
					t.Fatalf("EnvItems = %#v, want empty", got.EnvItems)
				}
				return
			}
			if env := model.SessionEnvMap(got.EnvItems); env["ANTHROPIC_API_KEY"] != "agent-key" || env["OPENCODE_MODEL"] != tt.want.Model {
				t.Fatalf("EnvItems = %#v", got.EnvItems)
			}
			got.EnvItems[0].Value = "mutated"
			again := AgentExecutionConfigForSession(ctx, tt.configDB, tt.session, tt.requested)
			if env := model.SessionEnvMap(again.EnvItems); env["ANTHROPIC_API_KEY"] != "agent-key" {
				t.Fatalf("EnvItems were not copied: %#v", again.EnvItems)
			}
		})
	}
}

func newTestConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	store, err := storage.NewConfigStoreFromConfig(&appconfig.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}
	return store
}

func agentSession(agentID string) *Session {
	return &model.Session{Summary: model.SessionSummary{Tags: []model.SessionTag{
		{Name: agentSessionTagSource, Value: agentSessionTagSourceVal},
		{Name: agentSessionTagID, Value: agentID},
	}}}
}
