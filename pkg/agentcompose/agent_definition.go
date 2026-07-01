package agentcompose

import (
	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/agentcompose/domain"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const (
	defaultAgentProvider = domain.DefaultAgentProvider

	agentSessionTagSource    = domain.AgentSessionTagSource
	agentSessionTagSourceVal = domain.AgentSessionTagSourceVal
	agentSessionTagID        = domain.AgentSessionTagID
	agentSessionTagName      = domain.AgentSessionTagName
)

type (
	AgentDefinition            = domain.AgentDefinition
	AgentDefinitionListOptions = domain.AgentDefinitionListOptions
	AgentDefinitionListResult  = domain.AgentDefinitionListResult
	AgentCurrentRunSummary     = domain.AgentCurrentRunSummary
	AgentLatestRunSummary      = domain.AgentLatestRunSummary
)

type AgentValidationResult struct {
	Availability agentcomposev1.AgentAvailabilityStatus
	Health       agentcomposev1.AgentHealthStatus
	Warnings     []string
	Errors       []string
}

func normalizeAgentDefinition(item AgentDefinition, assignDefaults bool) (AgentDefinition, error) {
	return domain.NormalizeAgentDefinition(item, assignDefaults)
}

func agentDefinitionTags(agent AgentDefinition) []*agentcomposev1.SessionTag {
	return api.AgentDefinitionTagsToProto(agent)
}

func sessionHasAgentTag(session *Session, agentID string) bool {
	return domain.SessionHasAgentTag(session, agentID)
}

func toProtoAgentDefinition(item AgentDefinition, workspace *WorkspaceConfig, validation AgentValidationResult, current AgentCurrentRunSummary, latest *AgentLatestRunSummary) *agentcomposev1.AgentDefinition {
	return api.AgentDefinitionToProto(item, workspace, validation.Availability, validation.Health, current, latest)
}

func toProtoEnvItems(items []SessionEnvVar) []*agentcomposev1.SessionEnvVar {
	return api.EnvItemsToProto(items)
}
