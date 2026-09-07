package execution

import (
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
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
	if provider == "opencode" && model == "" {
		// Compatibility fallback. `model:` used to be discarded outright for
		// opencode, so agents written against that behaviour carry their model
		// only as an OPENCODE_MODEL env item. Those keep working; a configured
		// `model:` now wins, as it does for every other provider.
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
