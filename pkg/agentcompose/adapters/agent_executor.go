package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

type AgentExecutor struct {
	config  *appconfig.Config
	store   *sandboxstore.Store
	streams *sandboxes.StreamBroker
	runner  *AgentRunner
}

func NewAgentExecutor(config *appconfig.Config, store *sandboxstore.Store, streams *sandboxes.StreamBroker, runner *AgentRunner) *AgentExecutor {
	return &AgentExecutor{config: config, store: store, streams: streams, runner: runner}
}

func (e *AgentExecutor) PrepareSandboxAgentEnvironment(ctx context.Context, session *domain.Sandbox, agent execution.AgentConfig, definition *domain.AgentDefinition) error {
	if e == nil || e.runner == nil {
		return fmt.Errorf("agent runner is required")
	}
	return e.runner.PrepareSandboxAgentEnvironment(ctx, session, agent, definition)
}

func (e *AgentExecutor) PrepareSandboxAgentEnvironmentFromTags(ctx context.Context, session *domain.Sandbox) error {
	if e == nil || e.runner == nil {
		return fmt.Errorf("agent runner is required")
	}
	return e.runner.PrepareSandboxAgentEnvironmentFromTags(ctx, session)
}

// GuestFileWriterFor exposes the runner's guest file push capability to
// callers outside this package (e.g. runs.WriteCapabilityGuide) that only
// hold an AgentExecutor. nil (safe to call as-is) if either is unset or
// the sandbox's driver has a shared filesystem and needs no push.
func (e *AgentExecutor) GuestFileWriterFor(session *domain.Sandbox) execution.GuestFileWriterFunc {
	if e == nil || e.runner == nil {
		return nil
	}
	return e.runner.guestFileWriterFor(session)
}

// ExecuteAgentRequest runs one agent turn as a notebook cell: it records the
// user event and a running cell, streams the agent's output into the cell,
// and finishes the cell with the parsed result. With request.Detach set the
// agent runs behind the guest supervisor; a daemon shutdown then returns
// runs.ErrRunDisconnected and leaves the cell running for ResumeAgentRequest.
func (e *AgentExecutor) ExecuteAgentRequest(ctx context.Context, session *domain.Sandbox, request execution.ExecuteAgentRequest) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
	agent := domain.NormalizeAgentKind(request.Agent)
	if agent == "" {
		agent = "codex"
	}
	model := strings.TrimSpace(request.Model)
	message := strings.TrimSpace(request.Message)
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
	if request.History == execution.AgentHistoryNotebook {
		if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
			return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, fmt.Errorf("create agent cell state dir: %w", err)
		}
	}
	startedAt := time.Now().UTC()
	userEvent := domain.SandboxEvent{ID: uuid.NewString(), Type: "agent.user", Level: "info", Message: message, CreatedAt: startedAt}
	if request.History == execution.AgentHistoryNotebook {
		if err := e.store.AddEvent(ctx, session.Summary.ID, userEvent); err != nil {
			return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, err
		}
		if e.streams != nil {
			e.streams.PublishEventAdded(session.Summary.ID, userEvent)
		}
	}

	cell := domain.NotebookCell{
		ID:        cellID,
		Type:      execution.CellTypeAgent,
		Source:    message,
		CreatedAt: startedAt,
		Agent:     agent,
		Running:   true,
	}
	if request.History == execution.AgentHistoryNotebook {
		if err := e.store.AddCell(ctx, session, cell); err != nil {
			return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, err
		}
		if e.streams != nil {
			e.streams.PublishCellStarted(session.Summary.ID, cell)
		}
	}

	run := &agentCellRun{
		e: e, ctx: ctx, session: session, agent: agent, message: message,
		hostCellDir: hostCellDir, userEvent: userEvent, stream: request.Stream,
		cancelExec: execCancel, cell: cell, history: request.History,
	}
	if request.Stream.OnStart != nil {
		if err := request.Stream.OnStart(cell); err != nil {
			return run.fail(err, domain.ExecResult{}, domain.AgentRunResult{})
		}
	}

	var detach *AgentRunDetach
	if request.Detach != nil {
		detach = &AgentRunDetach{
			ExecutionID:   request.Detach.ExecutionID,
			CursorPath:    agentExecutionCursorPath(e.config, request.RunID, hostCellDir, request.History),
			OnStart:       request.Detach.OnStart,
			JournalResume: journalStreamResume(request.Stream),
		}
	}
	execSession := cloneSessionForAgentExecution(session, request.ProviderEnvItems)
	execResult, result, err := e.runner.ExecuteAgentRun(execCtx, AgentRunRequest{
		Session:           execSession,
		Agent:             agent,
		AgentDefinitionID: request.AgentDefinitionID,
		Model:             model,
		RunID:             request.RunID,
		Message:           message,
		Input:             request.Input,
		OutputSchemaJSON:  request.OutputSchemaJSON,
		Detach:            detach,
	}, run.writeChunk)
	return run.finish(execResult, result, err)
}

// ResumeAgentRequest re-attaches to a detached agent run that an earlier
// daemon left running and finishes its cell exactly as ExecuteAgentRequest
// would have. The returned user event is empty: it was recorded when the run
// started.
func (e *AgentExecutor) ResumeAgentRequest(ctx context.Context, session *domain.Sandbox, request execution.ResumeAgentRequest) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
	cell, err := e.resumeAgentCell(ctx, session, request)
	if err != nil {
		return domain.NotebookCell{}, domain.SandboxEvent{}, domain.SandboxEvent{}, err
	}
	agent := domain.NormalizeAgentKind(cell.Agent)
	if agent == "" {
		agent = "codex"
	}
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()
	hostCellDir := filepath.Join(execution.HostSandboxDir(session), "state", "cells", cell.ID)
	run := &agentCellRun{
		e: e, ctx: ctx, session: session, agent: agent, message: cell.Source,
		hostCellDir: hostCellDir, stream: request.Stream, cancelExec: execCancel, cell: cell, history: request.History,
	}
	if request.JournalResume != nil {
		run.resumed = true
		run.streamed.Restore(domain.ExecResult{Stdout: cell.Stdout, Stderr: cell.Stderr, Output: cell.Output})
		if err := run.stdout.Restore(request.JournalResume.Decoder); err != nil {
			return run.fail(err, domain.ExecResult{}, domain.AgentRunResult{})
		}
	}
	execResult, result, err := e.runner.ResumeAgentRun(execCtx, AgentResumeRequest{
		Session: session, Agent: agent, RunID: request.RunID,
		Detach: &AgentRunDetach{ExecutionID: request.ExecutionID, CursorPath: agentExecutionCursorPath(e.config, request.RunID, hostCellDir, request.History), JournalResume: request.JournalResume},
	}, run.writeChunk)
	return run.finish(execResult, result, err)
}

func agentCellCursorPath(hostCellDir string) string {
	return filepath.Join(hostCellDir, "journal-cursor")
}

// agentCellRun is the cell-side state of one agent turn, shared by the fresh
// and resumed paths: the running cell, the streamed output, and how the cell
// is finished.
type agentCellRun struct {
	e           *AgentExecutor
	ctx         context.Context
	session     *domain.Sandbox
	agent       string
	message     string
	hostCellDir string
	userEvent   domain.SandboxEvent
	stream      execution.AgentExecutionStream
	history     execution.AgentHistoryMode
	cancelExec  context.CancelFunc

	cellMu   sync.Mutex
	cell     domain.NotebookCell
	streamed execution.ExecStreamAccumulator
	stdout   execution.AgentOutputDecoder
	// transcript rebuilds the text transcript from agent events; nil until
	// the runtime reports one (an older runtime mirrors text to stderr).
	transcript *runlog.Merger
	resumed    bool

	streamErrMu sync.Mutex
	streamErr   error
}

func (r *agentCellRun) setStreamErr(err error) {
	if err == nil {
		return
	}
	r.streamErrMu.Lock()
	defer r.streamErrMu.Unlock()
	if r.streamErr == nil {
		r.streamErr = err
		r.cancelExec()
	}
}

func (r *agentCellRun) deferredStreamErr() error {
	r.streamErrMu.Lock()
	defer r.streamErrMu.Unlock()
	return r.streamErr
}

// mergeStreamed combines the exec's aggregated result with what was streamed.
// The aggregate normally wins; once the transcript was rebuilt from agent
// events, only the streamed side carries it, so the streamed side wins.
func (r *agentCellRun) mergeStreamed(execResult domain.ExecResult, exitCode int, success bool) domain.ExecResult {
	r.cellMu.Lock()
	streamed := r.streamed.Result(exitCode, success)
	r.cellMu.Unlock()
	if r.transcript == nil && !r.resumed {
		return execution.MergeExecResults(execResult, streamed)
	}
	merged := execution.MergeExecResults(streamed, execResult)
	merged.ExitCode = execution.FirstNonZeroInt(execResult.ExitCode, exitCode)
	merged.Success = execResult.Success || success
	return merged
}

// finish turns the agent exec outcome into the finished cell. A disconnected
// detached run is not finished: the cell stays running for the next daemon.
func (r *agentCellRun) finish(execResult domain.ExecResult, result domain.AgentRunResult, err error) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
	if errors.Is(err, runs.ErrRunDisconnected) {
		r.cellMu.Lock()
		cell := r.cell
		r.cellMu.Unlock()
		return cell, r.userEvent, domain.SandboxEvent{}, err
	}
	r.flushChunks()
	if streamErr := r.deferredStreamErr(); streamErr != nil {
		return r.fail(streamErr, execResult, result)
	}
	if err != nil {
		return r.fail(err, execResult, result)
	}
	return r.succeed(execResult, result)
}

func (r *agentCellRun) threadID(agent, threadID string) string {
	return execution.ResolveAgentThreadID(r.ctx, execution.AgentThreadIDRequest{
		Config: r.e.config, Sandbox: r.session, Agent: agent, ThreadID: threadID, ReadGuestFile: r.e.runner.guestFileReaderFor(r.session),
	})
}

func (r *agentCellRun) fail(finalErr error, execResult domain.ExecResult, result domain.AgentRunResult) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
	e, session, ctx := r.e, r.session, r.ctx
	assistantEvent := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      "agent.assistant.failed",
		Level:     "error",
		CreatedAt: time.Now().UTC(),
		Message:   fmt.Sprintf("%s run failed: %v", r.agent, finalErr),
	}
	execResult = r.mergeStreamed(execResult, execution.FirstNonZeroInt(execResult.ExitCode, 1), false)
	execResult.ExitCode = execution.FirstNonZeroInt(execResult.ExitCode, 1)
	execResult.Success = false
	if strings.TrimSpace(execResult.Output) == "" {
		execResult.Output = assistantEvent.Message
	}
	if r.history == execution.AgentHistoryRunLog {
		return r.completeRunStep(execResult, result, finalErr, assistantEvent)
	}
	if err := execution.WriteCellArtifacts(r.hostCellDir, execResult); err != nil {
		return domain.NotebookCell{}, r.userEvent, domain.SandboxEvent{}, err
	}
	r.cellMu.Lock()
	cellAgent := firstNonEmpty(result.Agent, r.cell.Agent, r.agent)
	r.cellMu.Unlock()
	agentThreadID := r.threadID(cellAgent, result.ThreadID)
	r.cellMu.Lock()
	r.cell.Stdout = execResult.Stdout
	r.cell.Stderr = execResult.Stderr
	r.cell.Output = execResult.Output
	r.cell.ExitCode = execResult.ExitCode
	r.cell.Success = false
	r.cell.Running = false
	r.cell.Agent = cellAgent
	r.cell.AgentThreadID = agentThreadID
	r.cell.StopReason = result.StopReason
	failedCell := r.cell
	r.cellMu.Unlock()
	if addErr := e.store.AddCell(ctx, session, failedCell); addErr != nil {
		return domain.NotebookCell{}, r.userEvent, domain.SandboxEvent{}, addErr
	}
	if e.streams != nil {
		e.streams.PublishCellCompleted(session.Summary.ID, failedCell)
	}
	if addErr := e.store.AddEvent(ctx, session.Summary.ID, assistantEvent); addErr != nil {
		return domain.NotebookCell{}, r.userEvent, domain.SandboxEvent{}, addErr
	}
	if e.streams != nil {
		e.streams.PublishEventAdded(session.Summary.ID, assistantEvent)
	}
	return failedCell, r.userEvent, assistantEvent, finalErr
}

func (r *agentCellRun) succeed(execResult domain.ExecResult, result domain.AgentRunResult) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
	e, session, ctx := r.e, r.session, r.ctx
	execResult = r.mergeStreamed(execResult, execResult.ExitCode, result.Success)
	if strings.TrimSpace(execResult.Output) == "" {
		execResult.Output = firstNonEmpty(result.DisplayOutput, result.Transcript, result.FinalText)
	}
	if r.history == execution.AgentHistoryRunLog {
		assistant := domain.SandboxEvent{ID: uuid.NewString(), Type: "agent.assistant", Level: "info", CreatedAt: time.Now().UTC(), Message: agentAssistantMessage(result)}
		return r.completeRunStep(execResult, result, nil, assistant)
	}
	if err := execution.WriteCellArtifacts(r.hostCellDir, execResult); err != nil {
		return domain.NotebookCell{}, r.userEvent, domain.SandboxEvent{}, err
	}
	r.cellMu.Lock()
	cellAgent := firstNonEmpty(result.Agent, r.cell.Agent)
	r.cellMu.Unlock()
	agentThreadID := r.threadID(cellAgent, result.ThreadID)
	r.cellMu.Lock()
	r.cell.Stdout = execResult.Stdout
	r.cell.Stderr = execResult.Stderr
	r.cell.Output = execResult.Output
	r.cell.ExitCode = execResult.ExitCode
	r.cell.Success = result.Success
	r.cell.Running = false
	r.cell.Agent = cellAgent
	r.cell.AgentThreadID = agentThreadID
	r.cell.StopReason = result.StopReason
	cellSnapshot := r.cell
	r.cellMu.Unlock()
	if err := e.store.AddCell(ctx, session, cellSnapshot); err != nil {
		return domain.NotebookCell{}, r.userEvent, domain.SandboxEvent{}, err
	}
	if e.streams != nil {
		e.streams.PublishCellCompleted(session.Summary.ID, cellSnapshot)
	}

	assistantEvent := domain.SandboxEvent{ID: uuid.NewString(), Type: "agent.assistant", Level: "info", CreatedAt: time.Now().UTC(), Message: agentAssistantMessage(result)}
	if !cellSnapshot.Success {
		assistantEvent.Type = "agent.assistant.failed"
		assistantEvent.Level = "error"
	}
	if err := e.store.AddEvent(ctx, session.Summary.ID, assistantEvent); err != nil {
		return domain.NotebookCell{}, r.userEvent, domain.SandboxEvent{}, err
	}
	if e.streams != nil {
		e.streams.PublishEventAdded(session.Summary.ID, assistantEvent)
	}
	return cellSnapshot, r.userEvent, assistantEvent, nil
}

func cloneSessionForAgentExecution(session *domain.Sandbox, providerEnvItems []domain.SandboxEnvVar) *domain.Sandbox {
	if session == nil {
		return nil
	}
	execSession := *session
	execSession.EnvItems = append([]domain.SandboxEnvVar(nil), session.EnvItems...)
	execSession.RuntimeEnvItems = append([]domain.SandboxEnvVar(nil), session.RuntimeEnvItems...)
	execSession.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), session.ProviderEnvItems...)
	execution.ApplyAgentProviderEnv(&execSession, providerEnvItems)
	return &execSession
}

func agentAssistantMessage(result domain.AgentRunResult) string {
	if finalText := strings.TrimSpace(result.FinalText); finalText != "" && result.FinalTextSource == domain.AgentFinalTextSourceProviderMessage {
		return finalText
	}
	if !result.Success {
		return fmt.Sprintf("%s failed without output", result.Agent)
	}
	return ""
}

// A fresh structured consumer owns journal acknowledgement; it starts at zero.
func journalStreamResume(stream execution.AgentExecutionStream) *execution.AgentJournalResume {
	if stream.OnRuntimeOutput == nil {
		return nil
	}
	return &execution.AgentJournalResume{}
}
