package executor

import (
	"context"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/model"
	"agent-compose/pkg/runtimes"
	"agent-compose/pkg/storage"
)

const (
	VMStatusRunning = model.VMStatusRunning

	CellTypeShell      = model.CellTypeShell
	CellTypeJavaScript = model.CellTypeJavaScript
	CellTypePython     = model.CellTypePython
	CellTypeAgent      = model.CellTypeAgent
)

type SessionEnvVar = model.SessionEnvVar
type SessionTag = model.SessionTag
type Session = model.Session
type SessionEvent = model.SessionEvent
type NotebookCell = model.NotebookCell
type AgentResumeInfo = model.AgentResumeInfo
type ExecChunk = model.ExecChunk
type ExecResult = model.ExecResult
type ExecSpec = model.ExecSpec
type AgentRunResult = model.AgentRunResult
type RuntimeCommandResult = model.RuntimeCommandResult
type LoaderCommandRequest = model.LoaderCommandRequest
type LoaderCommandResult = model.LoaderCommandResult
type CellExecutionStream = model.CellExecutionStream
type AgentExecutionStream = model.AgentExecutionStream
type ExecuteAgentRequest = model.ExecuteAgentRequest
type ExecStreamWriter = model.ExecStreamWriter
type VMState = model.VMState

type Store = storage.Store
type ConfigStore = storage.ConfigStore
type RuntimeProvider = runtimes.RuntimeProvider

type StreamPublisher interface {
	PublishCellStarted(sessionID string, cell model.NotebookCell)
	PublishCellOutput(sessionID, cellID, chunk string, isStderr bool)
	PublishCellCompleted(sessionID string, cell model.NotebookCell)
	PublishEventAdded(sessionID string, event model.SessionEvent)
}

type LLMFacadeEnvPreparer func(ctx context.Context, config *appconfig.Config, configDB *storage.ConfigStore, session *model.Session, agent, modelName, source, runID string) (map[string]string, error)

const (
	LLMFacadeTokenSourceAgent         = "agent"
	LLMFacadeTokenSourceLoaderCommand = "loader_command"
)
