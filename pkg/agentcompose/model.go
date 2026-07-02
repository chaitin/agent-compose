package agentcompose

import (
	sessiondomain "agent-compose/internal/agentcompose/session"
	agentworkspace "agent-compose/internal/agentcompose/workspace"
)

const (
	VMStatusPending = sessiondomain.VMStatusPending
	VMStatusRunning = sessiondomain.VMStatusRunning
	VMStatusStopped = sessiondomain.VMStatusStopped
	VMStatusFailed  = sessiondomain.VMStatusFailed

	SessionTypeManual = sessiondomain.TypeManual
	SessionTypeScript = sessiondomain.TypeScript
)

type SessionTag = sessiondomain.Tag
type SessionEnvVar = sessiondomain.EnvVar

func sessionEnvMap(groups ...[]SessionEnvVar) map[string]string {
	return sessiondomain.EnvMap(groups...)
}

type SessionSummary = sessiondomain.Summary
type SessionListOptions = sessiondomain.ListOptions
type SessionListResult = sessiondomain.ListResult
type SessionWorkspace = sessiondomain.Workspace
type Session = sessiondomain.Session

func restoreSessionTransientFields(dst, src *Session) {
	sessiondomain.RestoreTransientFields(dst, src)
}

type WorkspaceConfig = agentworkspace.Config
type NotebookCell = sessiondomain.NotebookCell
type AgentResumeInfo = sessiondomain.AgentResumeInfo
type ExecChunk = sessiondomain.ExecChunk
type SessionEvent = sessiondomain.Event
type AgentRun = sessiondomain.AgentRun
type ExecResult = sessiondomain.ExecResult
type RuntimeCommandArtifacts = sessiondomain.RuntimeCommandArtifacts
type RuntimeCommandResult = sessiondomain.RuntimeCommandResult
type ExecStreamWriter = sessiondomain.ExecStreamWriter
type VMState = sessiondomain.VMState
type ProxyState = sessiondomain.ProxyState
type ExecSpec = sessiondomain.ExecSpec
type AgentRunResult = sessiondomain.AgentRunResult
