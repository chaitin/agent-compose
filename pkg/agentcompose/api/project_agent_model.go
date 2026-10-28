package api

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// ProjectAgentModelResolver supplies read-only model previews for project responses.
type ProjectAgentModelResolver interface {
	ResolveProjectAgentModels(context.Context, domain.ProjectRecord, []domain.ProjectAgentRecord) (map[string]llms.AgentModelResolution, error)
}

func (h *ProjectHandler) enrichProjectAgentModels(ctx context.Context, record domain.ProjectRecord, agents []domain.ProjectAgentRecord, project *agentcomposev2.Project) error {
	if h.agentModels == nil || project == nil {
		return nil
	}
	resolutions, err := h.agentModels.ResolveProjectAgentModels(ctx, record, agents)
	if err != nil {
		return ErrorForCode(connect.CodeInternal, ResourceProject, fmt.Errorf("resolve project agent models: %w", err))
	}
	for _, agent := range project.GetAgents() {
		resolution, ok := resolutions[agent.GetAgentName()]
		if !ok {
			continue
		}
		agent.ResolvedModel = resolution.Model
		agent.ModelSource = agentModelSourceToProto(resolution.Source)
	}
	return nil
}

func agentModelSourceToProto(source llms.AgentModelSource) agentcomposev2.AgentModelSource {
	switch source {
	case llms.AgentModelSourceProject:
		return agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_PROJECT
	case llms.AgentModelSourceAgentEnv:
		return agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_AGENT_ENV
	case llms.AgentModelSourceDaemonDefault:
		return agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_DAEMON_DEFAULT
	case llms.AgentModelSourceProviderDefault:
		return agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_PROVIDER_DEFAULT
	case llms.AgentModelSourceUnresolved:
		return agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_UNRESOLVED
	default:
		return agentcomposev2.AgentModelSource_AGENT_MODEL_SOURCE_UNSPECIFIED
	}
}
