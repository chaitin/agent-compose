package agentcompose

import (
	"agent-compose/pkg/agentcompose/domain"
	"agent-compose/pkg/agentcompose/execution"
	"agent-compose/pkg/agentcompose/llms"
	"agent-compose/pkg/agentcompose/loaders"
	"agent-compose/pkg/agentcompose/sessions"
	appconfig "agent-compose/pkg/config"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/samber/do/v2"
)

const defaultLoaderCommandMaxOutputBytes = int64(1024 * 1024)

type Executor struct {
	config   *appconfig.Config
	store    *Store
	configDB *ConfigStore
	runtimes RuntimeProvider
	streams  *sessions.StreamBroker
}

func NewExecutor(di do.Injector) (*Executor, error) {
	return &Executor{
		config:   do.MustInvoke[*appconfig.Config](di),
		store:    do.MustInvoke[*Store](di),
		configDB: do.MustInvoke[*ConfigStore](di),
		runtimes: do.MustInvoke[RuntimeProvider](di),
		streams:  do.MustInvoke[*sessions.StreamBroker](di),
	}, nil
}

func (e *Executor) ExecuteCell(ctx context.Context, session *Session, cellType, source string) (NotebookCell, error) {
	return e.executeCell(ctx, session, cellType, source, execution.CellExecutionStream{})
}

func (e *Executor) ExecuteCellStream(ctx context.Context, session *Session, cellType, source string, stream execution.CellExecutionStream) (NotebookCell, error) {
	return e.executeCell(ctx, session, cellType, source, stream)
}

func (e *Executor) ExecuteAgent(ctx context.Context, session *Session, agent, message string) (NotebookCell, SessionEvent, SessionEvent, error) {
	return e.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{Agent: agent, Message: message})
}

func (e *Executor) ExecuteAgentStream(ctx context.Context, session *Session, agent, message string, stream execution.AgentExecutionStream) (NotebookCell, SessionEvent, SessionEvent, error) {
	return e.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{Agent: agent, Message: message, Stream: stream})
}

func (e *Executor) ExecuteAgentWithTimeout(ctx context.Context, session *Session, agent, message string, timeout time.Duration) (NotebookCell, SessionEvent, SessionEvent, error) {
	return e.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{Agent: agent, Message: message, Timeout: timeout})
}

func (e *Executor) ExecuteAgentRequest(ctx context.Context, session *Session, request execution.ExecuteAgentRequest) (NotebookCell, SessionEvent, SessionEvent, error) {
	return e.executeAgent(ctx, session, request)
}

func (e *Executor) ExecuteLoaderCommand(ctx context.Context, session *Session, request domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	appconfig.ApplyDefaultGuestPaths(e.config)
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return domain.LoaderCommandResult{}, fmt.Errorf("session is not running")
	}
	if err := loaders.ValidateCommandRequest(request); err != nil {
		return domain.LoaderCommandResult{}, err
	}

	ctx, cancel := loaders.CommandContext(ctx, request.TimeoutMs)
	defer cancel()
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	cellID := uuid.NewString()
	hostCellDir := filepath.Join(execution.HostSessionDir(session), "state", "cells", cellID)
	if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
		return domain.LoaderCommandResult{}, fmt.Errorf("create loader command cell state dir: %w", err)
	}
	guestCellDir := guestCellStateDir(e.config, cellID)
	source := loaders.CommandCellSource(request)
	startedAt := time.Now().UTC()
	cell := NotebookCell{
		ID:        cellID,
		Type:      execution.CellTypeShell,
		Source:    source,
		CreatedAt: startedAt,
		Running:   true,
	}
	execSession, facadeToken, err := e.prepareLoaderCommandLLMFacadeEnv(ctx, session, request, cellID)
	if err != nil {
		return domain.LoaderCommandResult{}, err
	}
	if e.configDB != nil && facadeToken != "" {
		defer func() { _ = e.configDB.DeleteLLMFacadeToken(context.WithoutCancel(ctx), facadeToken) }()
	}
	if err := e.store.AddCell(ctx, session, cell); err != nil {
		return domain.LoaderCommandResult{}, err
	}
	e.streams.PublishCellStarted(session.Summary.ID, cell)

	artifacts := map[string]string{
		"cellDir": hostCellDir,
		"stdout":  filepath.Join(hostCellDir, "stdout.txt"),
		"stderr":  filepath.Join(hostCellDir, "stderr.txt"),
		"output":  filepath.Join(hostCellDir, "output.txt"),
		"request": filepath.Join(hostCellDir, "command-request.json"),
		"result":  filepath.Join(hostCellDir, "command-result.json"),
	}
	buildLoaderCommandResult := func(result ExecResult) domain.LoaderCommandResult {
		return domain.LoaderCommandResult{
			Stdout:    result.Stdout,
			Stderr:    result.Stderr,
			Output:    result.Output,
			ExitCode:  result.ExitCode,
			Success:   result.Success,
			SessionID: session.Summary.ID,
			CellID:    cellID,
			Artifacts: artifacts,
		}
	}

	var cellMu sync.Mutex
	var streamErrMu sync.Mutex
	var streamErr error
	var streamed execStreamAccumulator
	setStreamErr := func(err error) {
		if err == nil {
			return
		}
		streamErrMu.Lock()
		if streamErr == nil {
			streamErr = err
			execCancel()
		}
		streamErrMu.Unlock()
	}
	persistFailedCell := func(execResult ExecResult, finalErr error) (domain.LoaderCommandResult, error) {
		recovered := execution.MergeExecResults(execResult, streamed.result(execution.FirstNonZeroInt(execResult.ExitCode, 1), false))
		recovered = execution.RecoverExecResultFromCellArtifacts(hostCellDir, recovered)
		recovered.ExitCode = execution.FirstNonZeroInt(recovered.ExitCode, execResult.ExitCode, 1)
		recovered.Success = false
		if strings.TrimSpace(recovered.Output) == "" {
			recovered.Output = firstNonEmpty(recovered.Stderr, recovered.Stdout, finalErr.Error())
		}
		if err := execution.WriteCellArtifacts(hostCellDir, source, recovered); err != nil {
			return buildLoaderCommandResult(recovered), err
		}
		cellMu.Lock()
		cell.Stdout = recovered.Stdout
		cell.Stderr = recovered.Stderr
		cell.Output = recovered.Output
		cell.ExitCode = recovered.ExitCode
		cell.Success = false
		cell.Running = false
		failedCell := cell
		cellMu.Unlock()
		if err := e.store.AddCell(ctx, session, failedCell); err != nil {
			return buildLoaderCommandResult(recovered), err
		}
		e.streams.PublishCellCompleted(session.Summary.ID, failedCell)
		event := SessionEvent{
			ID:        uuid.NewString(),
			Type:      "kernel.cell.failed",
			Level:     "error",
			Message:   firstNonEmpty(recovered.Stderr, fmt.Sprintf("loader command failed with exit code %d", recovered.ExitCode), finalErr.Error()),
			CreatedAt: time.Now().UTC(),
		}
		_ = e.store.AddEvent(ctx, session.Summary.ID, event)
		e.streams.PublishEventAdded(session.Summary.ID, event)
		return buildLoaderCommandResult(recovered), finalErr
	}

	runtimeRequest := runtimeCommandRequestPayload(e.config, request, guestCellDir)
	hostRequestPath := filepath.Join(hostCellDir, "command-request.json")
	if err := execution.WriteJSONArtifact(hostRequestPath, runtimeRequest); err != nil {
		return domain.LoaderCommandResult{}, fmt.Errorf("write loader command request artifact: %w", err)
	}

	vmState, err := e.store.GetVMState(session.Summary.ID)
	if err != nil {
		return domain.LoaderCommandResult{}, err
	}
	runtime, err := e.runtimes.ForSession(session)
	if err != nil {
		return domain.LoaderCommandResult{}, err
	}
	streamWriter := func(chunk ExecChunk) {
		if chunk.Text == "" {
			return
		}
		cellMu.Lock()
		streamed.writeChunk(chunk)
		if chunk.IsStderr {
			cell.Stderr += chunk.Text
		} else {
			cell.Stdout += chunk.Text
		}
		cell.Output += chunk.Text
		snapshot := cell
		cellMu.Unlock()
		if err := e.store.AddCell(ctx, session, snapshot); err != nil {
			setStreamErr(err)
			return
		}
		e.streams.PublishCellOutput(session.Summary.ID, snapshot.ID, chunk.Text, chunk.IsStderr)
	}
	execResult, err := runtime.ExecStream(execCtx, execSession, vmState, buildLoaderCommandExecSpec(e.config, execSession, filepath.Join(guestCellDir, "command-request.json")), streamWriter)
	streamErrMu.Lock()
	deferredStreamErr := streamErr
	streamErrMu.Unlock()
	if deferredStreamErr != nil {
		return persistFailedCell(execResult, deferredStreamErr)
	}
	if err != nil {
		return persistFailedCell(execResult, err)
	}
	commandResult, err := execution.ParseCommandExecResult(execResult)
	if err != nil {
		return persistFailedCell(execResult, err)
	}
	if err := mirrorRuntimeCommandArtifacts(hostCellDir, commandResult); err != nil {
		return persistFailedCell(execResult, err)
	}

	cell.Stdout = commandResult.Stdout
	cell.Stderr = commandResult.Stderr
	cell.Output = commandResult.Output
	cell.ExitCode = commandResult.ExitCode
	cell.Success = commandResult.Success
	cell.Running = false
	if err := e.store.AddCell(ctx, session, cell); err != nil {
		return domain.LoaderCommandResult{}, err
	}
	e.streams.PublishCellCompleted(session.Summary.ID, cell)

	eventLevel := "info"
	eventType := "kernel.cell.succeeded"
	eventMessage := "executed loader command in agent-compose guest"
	if !commandResult.Success {
		eventLevel = "error"
		eventType = "kernel.cell.failed"
		eventMessage = firstNonEmpty(commandResult.Stderr, fmt.Sprintf("loader command failed with exit code %d", commandResult.ExitCode))
	}
	event := SessionEvent{
		ID:        uuid.NewString(),
		Type:      eventType,
		Level:     eventLevel,
		Message:   eventMessage,
		CreatedAt: time.Now().UTC(),
	}
	_ = e.store.AddEvent(ctx, session.Summary.ID, event)
	e.streams.PublishEventAdded(session.Summary.ID, event)

	return domain.LoaderCommandResult{
		Stdout:          commandResult.Stdout,
		Stderr:          commandResult.Stderr,
		Output:          commandResult.Output,
		ExitCode:        commandResult.ExitCode,
		Success:         commandResult.Success,
		StdoutTruncated: commandResult.StdoutTruncated,
		StderrTruncated: commandResult.StderrTruncated,
		OutputTruncated: commandResult.OutputTruncated,
		SessionID:       session.Summary.ID,
		CellID:          cellID,
		Artifacts:       artifacts,
	}, nil
}

func (e *Executor) prepareLoaderCommandLLMFacadeEnv(ctx context.Context, session *Session, request domain.LoaderCommandRequest, runID string) (*Session, string, error) {
	if e == nil || e.config == nil || e.configDB == nil || session == nil {
		return session, "", nil
	}
	agent, model := loaderCommandLLMFacadeAgentModel(request.Env)
	if agent == "" {
		return session, "", nil
	}

	execSession := *session
	execSession.EnvItems = append([]SessionEnvVar(nil), session.EnvItems...)
	execSession.RuntimeEnvItems = append([]SessionEnvVar(nil), session.RuntimeEnvItems...)
	execSession.ProviderEnvItems = append([]SessionEnvVar(nil), session.ProviderEnvItems...)
	if len(execSession.ProviderEnvItems) == 0 {
		globalEnv, err := e.configDB.ListGlobalEnv(ctx)
		if err != nil {
			return nil, "", err
		}
		providerEnv := domain.MergeEnvItems(globalEnv, session.EnvItems)
		providerEnv = domain.MergeEnvItems(providerEnv, request.SessionEnv)
		execSession.ProviderEnvItems = providerEnv
	}

	managedEnv, err := ensureSessionLLMFacadeConfig(ctx, e.config, e.configDB, &execSession, agent, model, llmFacadeTokenSourceLoaderCommand, runID)
	if err != nil {
		return nil, "", err
	}
	if len(managedEnv) > 0 {
		execSession.RuntimeEnvItems = domain.MergeEnvItems(execSession.RuntimeEnvItems, llms.EnvItemsFromMap(managedEnv, false))
	}
	return &execSession, managedEnv["AGENT_COMPOSE_SESSION_TOKEN"], nil
}

func loaderCommandLLMFacadeAgentModel(env map[string]string) (string, string) {
	return llms.LoaderCommandFacadeAgentModel(env)
}

func (e *Executor) executeCell(ctx context.Context, session *Session, cellType, source string, stream execution.CellExecutionStream) (NotebookCell, error) {
	appconfig.ApplyDefaultGuestPaths(e.config)
	source = strings.TrimSpace(source)
	if source == "" {
		return NotebookCell{}, fmt.Errorf("source is required")
	}

	cellType, err := execution.NormalizeCellType(cellType)
	if err != nil {
		return NotebookCell{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, e.config.SessionStartTimeout)
	defer cancel()
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	vmState, err := e.store.GetVMState(session.Summary.ID)
	if err != nil {
		return NotebookCell{}, err
	}
	runtime, err := e.runtimes.ForSession(session)
	if err != nil {
		return NotebookCell{}, err
	}

	cellID := uuid.NewString()
	hostCellDir := filepath.Join(filepath.Dir(session.Summary.WorkspacePath), "state", "cells", cellID)
	if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
		return NotebookCell{}, fmt.Errorf("create cell state dir: %w", err)
	}

	guestCellDir := guestCellStateDir(e.config, cellID)
	scriptName, command, args := execution.CellExecSpec(cellType, guestCellDir)
	hostScriptPath := filepath.Join(hostCellDir, scriptName)
	if err := os.WriteFile(hostScriptPath, []byte(source), 0o644); err != nil {
		return NotebookCell{}, fmt.Errorf("write cell script: %w", err)
	}

	startedAt := time.Now().UTC()
	startedCell := NotebookCell{
		ID:        cellID,
		Type:      cellType,
		Source:    source,
		CreatedAt: startedAt,
		Running:   true,
	}
	if stream.OnStart != nil {
		if err := stream.OnStart(startedCell); err != nil {
			return NotebookCell{}, err
		}
	}
	e.streams.PublishCellStarted(session.Summary.ID, startedCell)

	var streamErrMu sync.Mutex
	var streamErr error
	streamWriter := func(chunk ExecChunk) {
		e.streams.PublishCellOutput(session.Summary.ID, cellID, chunk.Text, chunk.IsStderr)
		if stream.OnChunk != nil {
			if err := stream.OnChunk(cellID, chunk); err != nil {
				streamErrMu.Lock()
				if streamErr == nil {
					streamErr = err
					execCancel()
				}
				streamErrMu.Unlock()
			}
		}
	}
	result, err := runtime.ExecStream(execCtx, session, vmState, ExecSpec{
		Command: command,
		Args:    args,
		Cwd:     e.config.GuestWorkspacePath,
	}, streamWriter)
	streamErrMu.Lock()
	deferredStreamErr := streamErr
	streamErrMu.Unlock()
	if deferredStreamErr != nil {
		return NotebookCell{}, deferredStreamErr
	}
	if err != nil {
		return NotebookCell{}, err
	}

	if err := execution.WriteCellArtifacts(hostCellDir, source, result); err != nil {
		return NotebookCell{}, err
	}

	cell := NotebookCell{
		ID:        cellID,
		Type:      cellType,
		Source:    source,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		Output:    result.Output,
		ExitCode:  result.ExitCode,
		Success:   result.Success,
		CreatedAt: startedAt,
	}
	if err := e.store.AddCell(ctx, session, cell); err != nil {
		return NotebookCell{}, err
	}
	e.streams.PublishCellCompleted(session.Summary.ID, cell)

	eventLevel := "info"
	eventType := "kernel.cell.succeeded"
	eventMessage := fmt.Sprintf("executed %s cell in agent-compose guest", cellType)
	if !result.Success {
		eventLevel = "error"
		eventType = "kernel.cell.failed"
		eventMessage = firstNonEmpty(result.Stderr, fmt.Sprintf("%s cell failed with exit code %d", cellType, result.ExitCode))
	}
	event := SessionEvent{
		ID:        uuid.NewString(),
		Type:      eventType,
		Level:     eventLevel,
		Message:   eventMessage,
		CreatedAt: time.Now().UTC(),
	}
	_ = e.store.AddEvent(ctx, session.Summary.ID, event)
	e.streams.PublishEventAdded(session.Summary.ID, event)
	return cell, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type execStreamAccumulator struct {
	execution.ExecStreamAccumulator
}

func (a *execStreamAccumulator) writeChunk(chunk ExecChunk) {
	a.WriteChunk(chunk)
}

func (a *execStreamAccumulator) result(exitCode int, success bool) ExecResult {
	return a.Result(exitCode, success)
}

func guestCellStateDir(config *appconfig.Config, cellID string) string {
	return filepath.Join(config.GuestStateRoot, "cells", cellID)
}

func guestSessionHome(config *appconfig.Config) string {
	return config.GuestHomePath
}

func (e *Executor) executeAgent(ctx context.Context, session *Session, request execution.ExecuteAgentRequest) (NotebookCell, SessionEvent, SessionEvent, error) {
	agent := request.Agent
	model := strings.TrimSpace(request.Model)
	message := request.Message
	stream := request.Stream
	message = strings.TrimSpace(message)
	if message == "" {
		return NotebookCell{}, SessionEvent{}, SessionEvent{}, fmt.Errorf("message is required")
	}
	agent = domain.NormalizeAgentKind(agent)
	if agent == "" {
		agent = "codex"
	}

	agentTimeout := e.config.AgentTimeout
	if request.Timeout > 0 {
		agentTimeout = request.Timeout
	}
	if agentTimeout <= 0 {
		agentTimeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	cellID := uuid.NewString()
	hostCellDir := filepath.Join(execution.HostSessionDir(session), "state", "cells", cellID)
	if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
		return NotebookCell{}, SessionEvent{}, SessionEvent{}, fmt.Errorf("create agent cell state dir: %w", err)
	}
	startedAt := time.Now().UTC()
	userEvent := SessionEvent{ID: uuid.NewString(), Type: "agent.user", Level: "info", Message: message, CreatedAt: startedAt}
	if err := e.store.AddEvent(ctx, session.Summary.ID, userEvent); err != nil {
		return NotebookCell{}, SessionEvent{}, SessionEvent{}, err
	}
	e.streams.PublishEventAdded(session.Summary.ID, userEvent)

	cell := NotebookCell{
		ID:        cellID,
		Type:      execution.CellTypeAgent,
		Source:    message,
		CreatedAt: startedAt,
		Agent:     domain.NormalizeAgentKind(agent),
		Running:   true,
	}
	if err := e.store.AddCell(ctx, session, cell); err != nil {
		return NotebookCell{}, SessionEvent{}, SessionEvent{}, err
	}
	e.streams.PublishCellStarted(session.Summary.ID, cell)

	var cellMu sync.Mutex
	var streamErrMu sync.Mutex
	var streamErr error
	var streamed execStreamAccumulator
	setStreamErr := func(err error) {
		if err == nil {
			return
		}
		streamErrMu.Lock()
		if streamErr == nil {
			streamErr = err
			execCancel()
		}
		streamErrMu.Unlock()
	}
	persistFailedCell := func(finalErr error, execResult ExecResult, result AgentRunResult) (NotebookCell, SessionEvent, SessionEvent, error) {
		assistantEvent := SessionEvent{
			ID:        uuid.NewString(),
			Type:      "agent.assistant.failed",
			Level:     "error",
			CreatedAt: time.Now().UTC(),
			Message:   fmt.Sprintf("%s run failed: %v", domain.NormalizeAgentKind(agent), finalErr),
		}
		execResult = execution.MergeExecResults(execResult, streamed.result(execution.FirstNonZeroInt(execResult.ExitCode, 1), false))
		execResult.ExitCode = execution.FirstNonZeroInt(execResult.ExitCode, 1)
		execResult.Success = false
		if strings.TrimSpace(execResult.Output) == "" {
			execResult.Output = assistantEvent.Message
		}
		if err := execution.WriteCellArtifacts(hostCellDir, message, execResult); err != nil {
			return NotebookCell{}, userEvent, SessionEvent{}, err
		}
		resumeInfo := execution.CollectAgentResumeInfo(session, firstNonEmpty(result.Agent, cell.Agent, agent), result.SessionID, filepath.Join(hostCellDir, "agent-session.json"))
		if err := execution.WriteAgentSessionArtifact(filepath.Join(hostCellDir, "agent-session.json"), resumeInfo); err != nil {
			return NotebookCell{}, userEvent, SessionEvent{}, err
		}
		agentSessionID := strings.TrimSpace(result.SessionID)
		if resumeInfo != nil && agentSessionID == "" {
			agentSessionID = resumeInfo.SessionID
		}
		cellMu.Lock()
		cell.Stdout = execResult.Stdout
		cell.Stderr = execResult.Stderr
		cell.Output = execResult.Output
		cell.ExitCode = execResult.ExitCode
		cell.Success = false
		cell.Running = false
		cell.Agent = firstNonEmpty(result.Agent, cell.Agent, domain.NormalizeAgentKind(agent))
		cell.AgentSessionID = agentSessionID
		cell.StopReason = result.StopReason
		cell.AgentResume = resumeInfo
		failedCell := cell
		cellMu.Unlock()
		if addErr := e.store.AddCell(ctx, session, failedCell); addErr != nil {
			return NotebookCell{}, userEvent, SessionEvent{}, addErr
		}
		e.streams.PublishCellCompleted(session.Summary.ID, failedCell)
		if addErr := e.store.AddEvent(ctx, session.Summary.ID, assistantEvent); addErr != nil {
			return NotebookCell{}, userEvent, SessionEvent{}, addErr
		}
		e.streams.PublishEventAdded(session.Summary.ID, assistantEvent)
		return failedCell, userEvent, assistantEvent, finalErr
	}

	if stream.OnStart != nil {
		if err := stream.OnStart(cell); err != nil {
			return persistFailedCell(err, ExecResult{}, AgentRunResult{})
		}
	}

	streamWriter := func(chunk ExecChunk) {
		cellMu.Lock()
		streamed.writeChunk(chunk)
		if chunk.IsStderr {
			cell.Stderr += chunk.Text
		} else {
			cell.Stdout += chunk.Text
		}
		cell.Output += chunk.Text
		snapshot := cell
		persistErr := e.store.AddCell(ctx, session, snapshot)
		cellMu.Unlock()
		if persistErr != nil {
			setStreamErr(persistErr)
			return
		}
		e.streams.PublishCellOutput(session.Summary.ID, snapshot.ID, chunk.Text, chunk.IsStderr)
		if stream.OnChunk != nil {
			if err := stream.OnChunk(cellID, chunk); err != nil {
				setStreamErr(err)
			}
		}
	}

	execSession := cloneSessionForAgentExecution(session, request.ProviderEnvItems)
	execResult, result, err := e.executeAgentRun(execCtx, execSession, agent, request.AgentDefinitionID, model, request.RunID, message, request.OutputSchemaJSON, streamWriter)
	streamErrMu.Lock()
	deferredStreamErr := streamErr
	streamErrMu.Unlock()
	if deferredStreamErr != nil {
		return persistFailedCell(deferredStreamErr, execResult, result)
	}
	if err != nil {
		return persistFailedCell(err, execResult, result)
	}

	execResult = execution.MergeExecResults(execResult, streamed.result(execResult.ExitCode, result.Success))
	if strings.TrimSpace(execResult.Output) == "" {
		execResult.Output = firstNonEmpty(result.DisplayOutput, result.Transcript, result.FinalText)
	}
	if err := execution.WriteCellArtifacts(hostCellDir, message, execResult); err != nil {
		return NotebookCell{}, userEvent, SessionEvent{}, err
	}
	resumeInfo := execution.CollectAgentResumeInfo(session, firstNonEmpty(result.Agent, cell.Agent), result.SessionID, filepath.Join(hostCellDir, "agent-session.json"))
	if err := execution.WriteAgentSessionArtifact(filepath.Join(hostCellDir, "agent-session.json"), resumeInfo); err != nil {
		return NotebookCell{}, userEvent, SessionEvent{}, err
	}
	agentSessionID := strings.TrimSpace(result.SessionID)
	if resumeInfo != nil && agentSessionID == "" {
		agentSessionID = resumeInfo.SessionID
	}
	cellMu.Lock()
	cell.Stdout = execResult.Stdout
	cell.Stderr = execResult.Stderr
	cell.Output = execResult.Output
	cell.ExitCode = execResult.ExitCode
	cell.Success = result.Success
	cell.Running = false
	cell.Agent = firstNonEmpty(result.Agent, cell.Agent)
	cell.AgentSessionID = agentSessionID
	cell.StopReason = result.StopReason
	cell.AgentResume = resumeInfo
	cellSnapshot := cell
	cellMu.Unlock()
	if err := e.store.AddCell(ctx, session, cellSnapshot); err != nil {
		return NotebookCell{}, userEvent, SessionEvent{}, err
	}
	e.streams.PublishCellCompleted(session.Summary.ID, cellSnapshot)

	assistantEvent := SessionEvent{ID: uuid.NewString(), Type: "agent.assistant", Level: "info", CreatedAt: time.Now().UTC(), Message: summarizeAgentResult(result)}
	if !cellSnapshot.Success {
		assistantEvent.Type = "agent.assistant.failed"
		assistantEvent.Level = "error"
	}
	for _, event := range agentTraceEvents(result.Transcript, assistantEvent.CreatedAt) {
		if err := e.store.AddEvent(ctx, session.Summary.ID, event); err != nil {
			return NotebookCell{}, userEvent, SessionEvent{}, err
		}
		e.streams.PublishEventAdded(session.Summary.ID, event)
	}
	if err := e.store.AddEvent(ctx, session.Summary.ID, assistantEvent); err != nil {
		return NotebookCell{}, userEvent, SessionEvent{}, err
	}
	e.streams.PublishEventAdded(session.Summary.ID, assistantEvent)
	return cellSnapshot, userEvent, assistantEvent, nil
}

func cloneSessionForAgentExecution(session *Session, providerEnvItems []SessionEnvVar) *Session {
	if session == nil {
		return nil
	}
	execSession := *session
	execSession.EnvItems = append([]SessionEnvVar(nil), session.EnvItems...)
	execSession.RuntimeEnvItems = append([]SessionEnvVar(nil), session.RuntimeEnvItems...)
	execSession.ProviderEnvItems = append([]SessionEnvVar(nil), session.ProviderEnvItems...)
	applyAgentProviderEnv(&execSession, providerEnvItems)
	return &execSession
}
