package llms

import (
	"context"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// AgentModelSource identifies the configuration layer that selected a model.
type AgentModelSource string

// Agent model sources distinguish persisted intent from runtime defaults.
const (
	AgentModelSourceProject         AgentModelSource = "project"
	AgentModelSourceAgentEnv        AgentModelSource = "agent_env"
	AgentModelSourceDaemonDefault   AgentModelSource = "daemon_default"
	AgentModelSourceProviderDefault AgentModelSource = "provider_default"
	AgentModelSourceUnresolved      AgentModelSource = "unresolved"
)

// AgentModelResolution describes the model preview for a new agent run.
type AgentModelResolution struct {
	Model  string
	Source AgentModelSource
}

// ResolveAgentModels previews the model each agent would select against one
// read-only snapshot of the daemon LLM catalog. It reports only where a model
// comes from: a model id is opaque and is never matched to a connection, and an
// empty model leaves the final selection to the agent provider or its upstream.
// It fails only when the catalog cannot be loaded.
func ResolveAgentModels(ctx context.Context, store CatalogStore, agents []domain.AgentDefinition) ([]AgentModelResolution, error) {
	catalog, err := LoadCatalog(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("load llm catalog for agent model preview: %w", err)
	}
	resolutions := make([]AgentModelResolution, 0, len(agents))
	for _, agent := range agents {
		resolutions = append(resolutions, resolveAgentModel(catalog, agent))
	}
	return resolutions, nil
}

// resolveAgentModel applies the preview precedence: the agent's own declaration,
// then its own environment, then the configured catalog default, then nothing.
func resolveAgentModel(catalog *Catalog, agent domain.AgentDefinition) AgentModelResolution {
	if model := strings.TrimSpace(agent.Model); model != "" {
		return AgentModelResolution{Model: model, Source: AgentModelSourceProject}
	}
	if model := agentEnvironmentModel(domain.NormalizeAgentKind(agent.Provider), agent.EnvItems); model != "" {
		return AgentModelResolution{Model: model, Source: AgentModelSourceAgentEnv}
	}
	if model := catalog.DefaultModel(); model != "" {
		return AgentModelResolution{Model: model, Source: AgentModelSourceDaemonDefault}
	}
	// No daemon layer supplies a model. The catalog treats model ids as opaque
	// and has no per-provider default, so the preview cannot claim the selection
	// is unresolved: the provider or its upstream owns the final choice.
	return AgentModelResolution{Source: AgentModelSourceProviderDefault}
}

// agentEnvironmentModel reads the model an agent declares in its own
// environment, using the provider-specific keys that agent CLI understands.
func agentEnvironmentModel(provider string, items []domain.SandboxEnvVar) string {
	switch provider {
	case "codex":
		return firstNonEmptyTrimmed(EnvItemValue(items, "CODEX_MODEL"), EnvItemValue(items, "LLM_MODEL"))
	case "claude":
		return firstNonEmptyTrimmed(EnvItemValue(items, "ANTHROPIC_MODEL"), EnvItemValue(items, "CLAUDE_MODEL"), EnvItemValue(items, "LLM_MODEL"))
	case "opencode":
		return firstNonEmptyTrimmed(EnvItemValue(items, "OPENCODE_MODEL"), EnvItemValue(items, "LLM_MODEL"))
	default:
		return ""
	}
}
