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

type agentExecResponse struct {
	Provider   string `json:"provider"`
	SessionID  string `json:"sessionId"`
	StopReason string `json:"stopReason"`
	FinalText  string `json:"finalText"`
	JSON       any    `json:"json"`
	Transcript string `json:"transcript"`
	Stderr     string `json:"stderr"`
}

type runtimeCommandRequestJSON = executorpkg.RuntimeCommandRequestJSON

func normalizeAgentKind(agent string) string {
	return executorpkg.NormalizeAgentKind(agent)
}

func hostAgentSystemPromptPath(session *Session) string {
	return executorpkg.HostAgentSystemPromptPath(session)
}

func writeAgentSystemPromptFile(session *Session, systemPrompt string) error {
	return executorpkg.WriteAgentSystemPromptFile(session, systemPrompt)
}

func parseAgentExecResult(agent string, result ExecResult) (AgentRunResult, error) {
	return executorpkg.ParseAgentExecResult(agent, result)
}

func agentTraceEvents(transcript string, createdAt time.Time) []SessionEvent {
	return executorpkg.AgentTraceEvents(transcript, createdAt)
}

func validateLoaderCommandRequest(request LoaderCommandRequest) error {
	return executorpkg.ValidateLoaderCommandRequest(request)
}

func loaderCommandContext(ctx context.Context, timeoutMs int64) (context.Context, context.CancelFunc) {
	return executorpkg.LoaderCommandContext(ctx, timeoutMs)
}

func loaderCommandCellSource(request LoaderCommandRequest) string {
	return executorpkg.LoaderCommandCellSource(request)
}

func runtimeCommandRequestPayload(config *appconfig.Config, request LoaderCommandRequest, guestCellDir string) runtimeCommandRequestJSON {
	return executorpkg.RuntimeCommandRequestPayload(config, request, guestCellDir)
}

func buildLoaderCommandExecSpec(config *appconfig.Config, session *Session, guestRequestPath string) ExecSpec {
	return executorpkg.BuildLoaderCommandExecSpec(config, session, guestRequestPath)
}

func parseCommandExecResult(result ExecResult) (RuntimeCommandResult, error) {
	return executorpkg.ParseCommandExecResult(result)
}

func mirrorRuntimeCommandArtifacts(hostCellDir string, result RuntimeCommandResult) error {
	return executorpkg.MirrorRuntimeCommandArtifacts(hostCellDir, result)
}

func summarizeAgentExecFailure(result ExecResult) string {
	return executorpkg.SummarizeAgentExecFailure(result)
}

func stripAgentResultPayload(raw string) string {
	return executorpkg.StripAgentResultPayload(raw)
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

func jupyterTargetReachable(proxyState ProxyState, timeout time.Duration) bool {
	return sessions.JupyterTargetReachable(proxyState, timeout)
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
