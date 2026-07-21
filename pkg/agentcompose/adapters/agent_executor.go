package adapters

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/sessionstore"
)

type AgentExecutor struct {
	config  *appconfig.Config
	store   *sessionstore.Store
	streams *sessions.StreamBroker
	runner  *AgentRunner
}

func NewAgentExecutor(config *appconfig.Config, store *sessionstore.Store, streams *sessions.StreamBroker, runner *AgentRunner) *AgentExecutor {
	return &AgentExecutor{config: config, store: store, streams: streams, runner: runner}
}

func (e *AgentExecutor) ExecuteAgentRequest(ctx context.Context, session *domain.Sandbox, request execution.ExecuteAgentRequest) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
	agent := domain.NormalizeAgentKind(request.Agent)
	if agent == "" {
		agent = "codex"
	}
	model := strings.TrimSpace(request.Model)
	message := strings.TrimSpace(request.Message)
	stream := request.Stream
	if message == "" {
		return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, fmt.Errorf("message is required")
	}
	if err := e.runner.ValidateSessionRuntime(session); err != nil {
		return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, err
	}

	agentTimeout := e.config.AgentTimeout
	if request.Timeout > 0 {
		agentTimeout = request.Timeout
	}
	if agentTimeout <= 0 {
		agentTimeout = appconfig.DefaultAgentTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	cellID := uuid.NewString()
	hostCellDir := filepath.Join(execution.HostSandboxDir(session), "state", "cells", cellID)
	if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
		return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, fmt.Errorf("create agent cell state dir: %w", err)
	}
	startedAt := time.Now().UTC()
	userEvent := domain.SandboxEvent{ID: uuid.NewString(), Type: "agent.user", Level: "info", Message: message, CreatedAt: startedAt}
	if err := e.store.AddEvent(ctx, session.Summary.ID, userEvent); err != nil {
		return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, err
	}
	if e.streams != nil {
		e.streams.PublishEventAdded(session.Summary.ID, userEvent)
	}

	cell := domain.NotebookCell{
		ID:        cellID,
		Type:      execution.CellTypeAgent,
		Source:    message,
		CreatedAt: startedAt,
		Agent:     agent,
		Running:   true,
	}
	if err := e.store.AddCell(ctx, session, cell); err != nil {
		return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, err
	}
	if e.streams != nil {
		e.streams.PublishCellStarted(session.Summary.ID, cell)
	}

	var cellMu sync.Mutex
	var streamErrMu sync.Mutex
	var streamErr error
	var streamed execution.ExecStreamAccumulator
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
	persistFailedCell := func(finalErr error, execResult domain.ExecResult, result domain.AgentRunResult) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
		assistantEvent := domain.SandboxEvent{
			ID:        uuid.NewString(),
			Type:      "agent.assistant.failed",
			Level:     "error",
			CreatedAt: time.Now().UTC(),
			Message:   fmt.Sprintf("%s run failed: %v", agent, finalErr),
		}
		execResult = execution.MergeExecResults(execResult, streamed.Result(execution.FirstNonZeroInt(execResult.ExitCode, 1), false))
		execResult.ExitCode = execution.FirstNonZeroInt(execResult.ExitCode, 1)
		execResult.Success = false
		if strings.TrimSpace(execResult.Output) == "" {
			execResult.Output = assistantEvent.Message
		}
		if err := execution.WriteCellArtifacts(hostCellDir, message, execResult); err != nil {
			return domain.NotebookCell{}, userEvent, domain.SandboxEvent{}, err
		}
		threadArtifactPath := filepath.Join(hostCellDir, "agent-thread.json")
		resumeInfo := execution.CollectAgentResumeInfo(session, firstNonEmpty(result.Agent, cell.Agent, agent), result.ThreadID, threadArtifactPath)
		if err := execution.WriteAgentThreadArtifact(threadArtifactPath, resumeInfo); err != nil {
			return domain.NotebookCell{}, userEvent, domain.SandboxEvent{}, err
		}
		agentThreadID := strings.TrimSpace(result.ThreadID)
		if resumeInfo != nil && agentThreadID == "" {
			agentThreadID = resumeInfo.ThreadID
		}
		cellMu.Lock()
		cell.Stdout = execResult.Stdout
		cell.Stderr = execResult.Stderr
		cell.Output = execResult.Output
		cell.ExitCode = execResult.ExitCode
		cell.Success = false
		cell.Running = false
		cell.Agent = firstNonEmpty(result.Agent, cell.Agent, agent)
		cell.AgentThreadID = agentThreadID
		cell.StopReason = result.StopReason
		cell.AgentResume = resumeInfo
		failedCell := cell
		cellMu.Unlock()
		if addErr := e.store.AddCell(ctx, session, failedCell); addErr != nil {
			return domain.NotebookCell{}, userEvent, domain.SandboxEvent{}, addErr
		}
		if e.streams != nil {
			e.streams.PublishCellCompleted(session.Summary.ID, failedCell)
		}
		if addErr := e.store.AddEvent(ctx, session.Summary.ID, assistantEvent); addErr != nil {
			return domain.NotebookCell{}, userEvent, domain.SandboxEvent{}, addErr
		}
		if e.streams != nil {
			e.streams.PublishEventAdded(session.Summary.ID, assistantEvent)
		}
		return failedCell, userEvent, assistantEvent, finalErr
	}

	if stream.OnStart != nil {
		if err := stream.OnStart(cell); err != nil {
			return persistFailedCell(err, domain.ExecResult{}, domain.AgentRunResult{})
		}
	}

	streamWriter := func(chunk domain.ExecChunk) {
		filtered, visible := execution.FilterAgentStreamChunk(chunk)
		if !visible {
			return
		}
		isStderr := domain.NormalizeStdioStream(filtered.Stream) == domain.StdioStderr
		cellMu.Lock()
		streamed.WriteChunk(filtered)
		if isStderr {
			cell.Stderr += filtered.Text
		} else {
			cell.Stdout += filtered.Text
		}
		cell.Output += filtered.Text
		snapshot := cell
		cellMu.Unlock()
		persistErr := e.store.AddCell(ctx, session, snapshot)
		if persistErr != nil {
			setStreamErr(persistErr)
			return
		}
		if e.streams != nil {
			e.streams.PublishCellOutput(session.Summary.ID, snapshot.ID, filtered.Text, filtered.Stream)
		}
		if stream.OnChunk != nil {
			if err := stream.OnChunk(cellID, filtered); err != nil {
				setStreamErr(err)
			}
		}
	}

	execSession := cloneSessionForAgentExecution(session, request.ProviderEnvItems)
	execResult, result, err := e.runner.ExecuteAgentRun(execCtx, execSession, agent, request.AgentDefinitionID, model, request.RunID, message, request.OutputSchemaJSON, streamWriter)
	streamErrMu.Lock()
	deferredStreamErr := streamErr
	streamErrMu.Unlock()
	if deferredStreamErr != nil {
		return persistFailedCell(deferredStreamErr, execResult, result)
	}
	if err != nil {
		return persistFailedCell(err, execResult, result)
	}

	execResult = execution.MergeExecResults(execResult, streamed.Result(execResult.ExitCode, result.Success))
	if strings.TrimSpace(execResult.Output) == "" {
		execResult.Output = firstNonEmpty(result.DisplayOutput, result.Transcript, result.FinalText)
	}
	if err := execution.WriteCellArtifacts(hostCellDir, message, execResult); err != nil {
		return domain.NotebookCell{}, userEvent, domain.SandboxEvent{}, err
	}
	threadArtifactPath := filepath.Join(hostCellDir, "agent-thread.json")
	resumeInfo := execution.CollectAgentResumeInfo(session, firstNonEmpty(result.Agent, cell.Agent), result.ThreadID, threadArtifactPath)
	if err := execution.WriteAgentThreadArtifact(threadArtifactPath, resumeInfo); err != nil {
		return domain.NotebookCell{}, userEvent, domain.SandboxEvent{}, err
	}
	agentThreadID := strings.TrimSpace(result.ThreadID)
	if resumeInfo != nil && agentThreadID == "" {
		agentThreadID = resumeInfo.ThreadID
	}
	cellMu.Lock()
	cell.Stdout = execResult.Stdout
	cell.Stderr = execResult.Stderr
	cell.Output = execResult.Output
	cell.ExitCode = execResult.ExitCode
	cell.Success = result.Success
	cell.Running = false
	cell.Agent = firstNonEmpty(result.Agent, cell.Agent)
	cell.AgentThreadID = agentThreadID
	cell.StopReason = result.StopReason
	cell.AgentResume = resumeInfo
	cellSnapshot := cell
	cellMu.Unlock()
	if err := e.store.AddCell(ctx, session, cellSnapshot); err != nil {
		return domain.NotebookCell{}, userEvent, domain.SandboxEvent{}, err
	}
	if e.streams != nil {
		e.streams.PublishCellCompleted(session.Summary.ID, cellSnapshot)
	}

	assistantEvent := domain.SandboxEvent{ID: uuid.NewString(), Type: "agent.assistant", Level: "info", CreatedAt: time.Now().UTC(), Message: summarizeAgentResult(result)}
	if !cellSnapshot.Success {
		assistantEvent.Type = "agent.assistant.failed"
		assistantEvent.Level = "error"
	}
	for _, event := range execution.AgentTraceEvents(result.Transcript, assistantEvent.CreatedAt) {
		if err := e.store.AddEvent(ctx, session.Summary.ID, event); err != nil {
			return domain.NotebookCell{}, userEvent, domain.SandboxEvent{}, err
		}
		if e.streams != nil {
			e.streams.PublishEventAdded(session.Summary.ID, event)
		}
	}
	if err := e.store.AddEvent(ctx, session.Summary.ID, assistantEvent); err != nil {
		return domain.NotebookCell{}, userEvent, domain.SandboxEvent{}, err
	}
	if e.streams != nil {
		e.streams.PublishEventAdded(session.Summary.ID, assistantEvent)
	}
	return cellSnapshot, userEvent, assistantEvent, nil
}

func cloneSessionForAgentExecution(session *domain.Sandbox, providerEnvItems []domain.SandboxEnvVar) *domain.Sandbox {
	if session == nil {
		return nil
	}
	execSession := *session
	execSession.EnvItems = append([]domain.SandboxEnvVar(nil), session.EnvItems...)
	execSession.RuntimeEnvItems = append([]domain.SandboxEnvVar(nil), session.RuntimeEnvItems...)
	execSession.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), session.ProviderEnvItems...)
	execSession.ExecutionProviderEnvItems = append([]domain.SandboxEnvVar(nil), session.ExecutionProviderEnvItems...)
	execution.ApplyAgentProviderEnv(&execSession, providerEnvItems)
	return &execSession
}

func summarizeAgentResult(result domain.AgentRunResult) string {
	body := firstNonEmpty(result.FinalText, result.DisplayOutput, result.Transcript)
	if strings.TrimSpace(body) == "" {
		if result.Success {
			return fmt.Sprintf("%s finished without output", result.Agent)
		}
		return fmt.Sprintf("%s failed without output", result.Agent)
	}
	return body
}
