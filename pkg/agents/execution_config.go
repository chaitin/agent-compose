package agents

import (
	"strings"

	"agent-compose/pkg/model"
)

type AgentExecutionConfig struct {
	Provider          string
	AgentDefinitionID string
	Model             string
	EnvItems          []SessionEnvVar
}

func AgentExecutionConfigFromDefinition(agent AgentDefinition, fallbackProvider string) AgentExecutionConfig {
	provider := normalizeAgentKind(agent.Provider)
	if provider == "" {
		provider = normalizeAgentKind(fallbackProvider)
	}
	modelName := strings.TrimSpace(agent.Model)
	if provider == model.AgentProviderOpenCode {
		modelName = strings.TrimSpace(model.SessionEnvMap(agent.EnvItems)["OPENCODE_MODEL"])
	}
	return AgentExecutionConfig{
		Provider:          provider,
		AgentDefinitionID: strings.TrimSpace(agent.ID),
		Model:             modelName,
		EnvItems:          append([]SessionEnvVar(nil), agent.EnvItems...),
	}
}
