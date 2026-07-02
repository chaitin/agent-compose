package agentcompose

import "agent-compose/pkg/agentcompose/domain"

type (
	SessionTag              = domain.SessionTag
	SessionEnvVar           = domain.SessionEnvVar
	SessionSummary          = domain.SessionSummary
	SessionListOptions      = domain.SessionListOptions
	SessionListResult       = domain.SessionListResult
	SessionWorkspace        = domain.SessionWorkspace
	Session                 = domain.Session
	WorkspaceConfig         = domain.WorkspaceConfig
	NotebookCell            = domain.NotebookCell
	AgentResumeInfo         = domain.AgentResumeInfo
	ExecChunk               = domain.ExecChunk
	SessionEvent            = domain.SessionEvent
	AgentRun                = domain.AgentRun
	ExecResult              = domain.ExecResult
	RuntimeCommandArtifacts = domain.RuntimeCommandArtifacts
	RuntimeCommandResult    = domain.RuntimeCommandResult
	ExecStreamWriter        = domain.ExecStreamWriter
	VMState                 = domain.VMState
	ProxyState              = domain.ProxyState
	ExecSpec                = domain.ExecSpec
	AgentRunResult          = domain.AgentRunResult
)

func sessionEnvMap(groups ...[]SessionEnvVar) map[string]string {
	return domain.SessionEnvMap(groups...)
}

func restoreSessionTransientFields(dst, src *Session) {
	domain.RestoreSessionTransientFields(dst, src)
}
