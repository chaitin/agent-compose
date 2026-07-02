package agentcompose

import (
	agentdomain "agent-compose/internal/agentcompose/agent"
	"time"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const (
	defaultAgentProvider = agentdomain.DefaultProvider

	agentSessionTagSource    = agentdomain.SessionTagSource
	agentSessionTagSourceVal = agentdomain.SessionTagSourceValue
	agentSessionTagID        = agentdomain.SessionTagID
	agentSessionTagName      = agentdomain.SessionTagName
)

type AgentDefinition = agentdomain.Definition
type AgentDefinitionListOptions = agentdomain.ListOptions
type AgentDefinitionListResult = agentdomain.ListResult
type AgentValidationResult = agentdomain.ValidationResult

func normalizeAgentDefinition(item AgentDefinition, assignDefaults bool) (AgentDefinition, error) {
	return agentdomain.NormalizeDefinition(item, assignDefaults)
}

func isJSONObject(raw string) bool {
	return agentdomain.IsJSONObject(raw)
}

func agentDefinitionTags(agent AgentDefinition) []*agentcomposev1.SessionTag {
	return agentdomain.DefinitionTags(agent)
}

func sessionHasAgentTag(session *Session, agentID string) bool {
	return agentdomain.SessionHasAgentTag(session, agentID)
}

func toProtoAgentDefinition(item AgentDefinition, workspace *WorkspaceConfig, validation AgentValidationResult, current AgentCurrentRunSummary, latest *AgentLatestRunSummary) *agentcomposev1.AgentDefinition {
	return agentdomain.ToProtoDefinition(item, workspace, validation, current, latest)
}

func toProtoEnvItems(items []SessionEnvVar) []*agentcomposev1.SessionEnvVar {
	return agentdomain.ToProtoEnvItems(items)
}

func toProtoAgentWorkFiles(workspaceID string, workspace *WorkspaceConfig) *agentcomposev1.AgentWorkFiles {
	return agentdomain.ToProtoWorkFiles(workspaceID, workspace)
}

func agentWorkspaceSummary(workspace WorkspaceConfig) string {
	return agentdomain.WorkspaceSummary(workspace)
}

func toProtoAgentCurrentRunSummary(item AgentCurrentRunSummary) *agentcomposev1.AgentCurrentRunSummary {
	return agentdomain.ToProtoCurrentRunSummary(item)
}

func formatProtoTime(value time.Time) string {
	return agentdomain.FormatProtoTime(value)
}

type AgentCurrentRunSummary = agentdomain.CurrentRunSummary
type AgentLatestRunSummary = agentdomain.LatestRunSummary
