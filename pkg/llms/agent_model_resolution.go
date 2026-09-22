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

// resolveAgentModel reports the model a new run would use, applying exactly the
// precedence PrepareAgentLLM applies: the agent's own declaration, then the
// model named by an upstream the agent declared itself, then the configured
// catalog default, then nothing.
//
// The agent's environment is consulted only when direct mode would actually
// engage, which is what directUpstreamFromAgentEnv decides. Reading a model out
// of the environment in managed mode would report a model the run will not use,
// because a managed run never consults the agent's environment.
func resolveAgentModel(catalog *Catalog, agent domain.AgentDefinition) AgentModelResolution {
	if model := strings.TrimSpace(agent.Model); model != "" {
		return AgentModelResolution{Model: model, Source: AgentModelSourceProject}
	}
	if dialect, err := DialectFor(domain.NormalizeAgentKind(agent.Provider)); err == nil {
		if upstream, declared := directUpstreamFromAgentEnv(agent.EnvItems, dialect); declared && upstream.Model != "" {
			return AgentModelResolution{Model: upstream.Model, Source: AgentModelSourceAgentEnv}
		}
	}
	if model := catalog.DefaultModel(); model != "" {
		return AgentModelResolution{Model: model, Source: AgentModelSourceDaemonDefault}
	}
	// No daemon layer supplies a model. The catalog treats model ids as opaque
	// and has no per-provider default, so the preview cannot claim the selection
	// is unresolved: the provider or its upstream owns the final choice.
	return AgentModelResolution{Source: AgentModelSourceProviderDefault}
}
