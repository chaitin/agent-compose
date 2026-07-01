package agentcompose

import (
	"context"
	"time"

	appconfig "agent-compose/pkg/config"
	executorpkg "agent-compose/pkg/executor"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/settings"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const agentResultPrefix = executorpkg.AgentResultPrefix
const commandResultPrefix = executorpkg.CommandResultPrefix
const agentSystemPromptFileName = "system-prompt.txt"

func normalizeAgentKind(agent string) string {
	return executorpkg.NormalizeAgentKind(agent)
}

func hostAgentSystemPromptPath(session *Session) string {
	return executorpkg.HostAgentSystemPromptPath(session)
}

func writeAgentSystemPromptFile(session *Session, systemPrompt string) error {
	return executorpkg.WriteAgentSystemPromptFile(session, systemPrompt)
}

func agentTraceEvents(transcript string, createdAt time.Time) []SessionEvent {
	return executorpkg.AgentTraceEvents(transcript, createdAt)
}

func summarizeAgentExecFailure(result ExecResult) string {
	return executorpkg.SummarizeAgentExecFailure(result)
}

func buildAgentExecSpec(config *appconfig.Config, session *Session, agent, model, promptPath, schemaPath string) ExecSpec {
	return executorpkg.BuildAgentExecSpec(config, session, agent, model, promptPath, schemaPath)
}

func summarizeAgentResult(result AgentRunResult) string {
	return executorpkg.SummarizeAgentResult(result)
}

func (e *Executor) resolveAgentSystemPrompt(ctx context.Context, session *Session, agentDefinitionID string) (string, error) {
	return e.componentExecutor().ResolveAgentSystemPrompt(ctx, session, agentDefinitionID)
}

func (e *Executor) executeAgentRun(ctx context.Context, session *Session, agent, agentDefinitionID, model, runID, message, outputSchemaJSON string, stream ExecStreamWriter) (ExecResult, AgentRunResult, error) {
	return e.componentExecutor().ExecuteAgentRun(ctx, session, agent, agentDefinitionID, model, runID, message, outputSchemaJSON, stream)
}

func toProtoSessionDetail(session *Session) *agentcomposev1.SessionDetail {
	return sessions.ToProtoSessionDetail(session)
}

func toProtoGlobalEnvConfig(items []SessionEnvVar) *agentcomposev1.GlobalEnvConfigResponse {
	return settings.ToProtoGlobalEnvConfig(items)
}

func toSessionWorkspaceSnapshot(item WorkspaceConfig) *SessionWorkspace {
	return &SessionWorkspace{ID: item.ID, Name: item.Name, Type: item.Type, ConfigJSON: item.ConfigJSON}
}

func toProtoSessionWorkspace(item *SessionWorkspace) *agentcomposev1.SessionWorkspaceSnapshot {
	return sessions.ToProtoSessionWorkspace(item)
}

func toProtoWorkspaceConfig(item WorkspaceConfig) *agentcomposev1.WorkspaceConfig {
	return &agentcomposev1.WorkspaceConfig{
		Id:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJson: item.ConfigJSON,
		Comment:    item.Comment,
		CreatedAt:  item.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:  item.UpdatedAt.Format(time.RFC3339Nano),
	}
}

func toProtoCell(cell NotebookCell) *agentcomposev1.NotebookCell {
	return sessions.ToProtoCell(cell)
}

func toProtoAgentRun(cell NotebookCell) *agentcomposev1.AgentRun {
	return sessions.ToProtoAgentRun(cell)
}

func fromProtoCellType(cellType agentcomposev1.CellType) string {
	return sessions.FromProtoCellType(cellType)
}

func toProtoCellType(cellType string) agentcomposev1.CellType {
	return sessions.ToProtoCellType(cellType)
}

func toProtoEvent(event SessionEvent) *agentcomposev1.SessionEvent {
	return sessions.ToProtoEvent(event)
}
