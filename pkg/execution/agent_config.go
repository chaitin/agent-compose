package execution

import (
	"strings"

	domain "agent-compose/pkg/model"
)

type AgentConfig struct {
	Provider          string
	AgentDefinitionID string
	Model             string
	EnvItems          []domain.SandboxEnvVar
}

func AgentConfigFromDefinition(agent domain.AgentDefinition, fallbackProvider string) AgentConfig {
	provider := domain.NormalizeAgentKind(agent.Provider)
	if provider == "" {
		provider = domain.NormalizeAgentKind(fallbackProvider)
	}
	model := strings.TrimSpace(agent.Model)
	if provider == "opencode" {
		model = strings.TrimSpace(domain.SandboxEnvMap(agent.EnvItems)["OPENCODE_MODEL"])
	}
	return AgentConfig{
		Provider:          provider,
		AgentDefinitionID: strings.TrimSpace(agent.ID),
		Model:             model,
		EnvItems:          append([]domain.SandboxEnvVar(nil), agent.EnvItems...),
	}
}

func ApplyAgentProviderEnv(session *domain.Sandbox, agentEnv []domain.SandboxEnvVar) {
	if session == nil || len(agentEnv) == 0 {
		return
	}
	providerEnv := session.ProviderEnvItems
	if len(providerEnv) == 0 && session.ProviderEnvOverrideNames == nil {
		providerEnv = session.EnvItems
	}
	session.ProviderEnvItems = domain.MergeEnvItems(agentEnv, providerEnv)
}

func SessionTagValue(tags []domain.SandboxTag, name string) string {
	for _, tag := range tags {
		if strings.TrimSpace(tag.Name) == name {
			return strings.TrimSpace(tag.Value)
		}
	}
	return ""
}
