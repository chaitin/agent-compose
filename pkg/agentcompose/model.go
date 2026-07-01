package agentcompose

import "agent-compose/pkg/model"

const (
	VMStatusPending = model.VMStatusPending
	VMStatusRunning = model.VMStatusRunning
	VMStatusStopped = model.VMStatusStopped
	VMStatusFailed  = model.VMStatusFailed

	SessionTypeManual = model.SessionTypeManual
	SessionTypeScript = model.SessionTypeScript
)

type SessionTag = model.SessionTag
type SessionEnvVar = model.SessionEnvVar
type SessionSummary = model.SessionSummary
type SessionListOptions = model.SessionListOptions
type SessionListResult = model.SessionListResult
type SessionWorkspace = model.SessionWorkspace
type Session = model.Session
type WorkspaceConfig = model.WorkspaceConfig
type NotebookCell = model.NotebookCell
type AgentResumeInfo = model.AgentResumeInfo
type ExecChunk = model.ExecChunk
type SessionEvent = model.SessionEvent
type AgentRun = model.AgentRun
type ExecResult = model.ExecResult
type RuntimeCommandArtifacts = model.RuntimeCommandArtifacts
type RuntimeCommandResult = model.RuntimeCommandResult
type ExecStreamWriter = model.ExecStreamWriter
type VMState = model.VMState
type ProxyState = model.ProxyState
type ExecSpec = model.ExecSpec
type AgentRunResult = model.AgentRunResult

func sessionEnvMap(groups ...[]SessionEnvVar) map[string]string {
	return model.SessionEnvMap(groups...)
}
