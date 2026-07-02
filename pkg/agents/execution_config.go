package agents

import (
	"context"
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

func AgentExecutionConfigForSession(ctx context.Context, configDB *ConfigStore, session *Session, requestedProvider string) AgentExecutionConfig {
	provider := normalizeAgentKind(requestedProvider)
	config := AgentExecutionConfig{Provider: provider}
	if session == nil || configDB == nil {
		return config
	}
	agentID := sessionTagValue(session.Summary.Tags, agentSessionTagID)
	if agentID == "" || !sessionHasAgentTag(session, agentID) {
		return config
	}
	agent, err := configDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return config
	}
	return AgentExecutionConfigFromDefinition(agent, provider)
}

func sessionTagValue(tags []SessionTag, name string) string {
	for _, tag := range tags {
		if strings.TrimSpace(tag.Name) == name {
			return strings.TrimSpace(tag.Value)
		}
	}
	return ""
}
