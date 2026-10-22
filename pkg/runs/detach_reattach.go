package runs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ReattachDetachedRun resumes following a detached run left running by a
// daemon shutdown or restart. It re-attaches to the guest journal, appends any
// output that arrived while the daemon was gone, and completes the run when the
// task exits. A run whose sandbox is no longer running is marked failed.
//
// ctx is the run context, owned by the caller (RunSupervisor): it must not be
// cancelled by daemon shutdown, only by a user stop, and it must carry the
// shutdown signal via WithDaemonShutdown so the follower disconnects instead
// of stopping the task. The persisted deadline is re-applied here.
func (c *Controller) ReattachDetachedRun(ctx context.Context, run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	if !run.Detach || run.ExecutionID == "" {
		return run, nil
	}
	transitionCtx := context.WithoutCancel(ctx)
	sandbox, err := c.store.GetSandbox(transitionCtx, run.SandboxID)
	if err != nil {
		return run, err
	}
	if sandbox.Summary.VMStatus != domain.VMStatusRunning {
		c.deleteRetainedFacadeTokens(transitionCtx, run)
		transition := TransitionRequest{
			RunID:        run.RunID,
			SandboxID:    run.SandboxID,
			ExitCode:     1,
			Output:       run.Output,
			ArtifactsDir: run.ArtifactsDir,
			LogsPath:     run.LogsPath,
			Error:        "detached execution sandbox is no longer running",
		}
		return c.completeProjectRunError(transitionCtx, ctx, transition, errors.New(transition.Error))
	}
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return run, err
	}
	runtime, err := c.runtime(sandbox)
	if err != nil {
		return run, err
	}

	runCtx := ctx
	if !run.Deadline.IsZero() {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithDeadline(runCtx, run.Deadline)
		defer cancel()
	}
	switch run.DetachKind {
	case domain.DetachKindAgent:
		return c.reattachDetachedAgentRun(transitionCtx, runCtx, run, sandbox)
	case domain.DetachKindInteractivePrompt, domain.DetachKindInteractiveCommand:
		return c.reattachInteractiveRun(transitionCtx, runCtx, run, sandbox)
	}

	artifactsDir := projectRunCommandArtifactsDir(run, sandbox)
	logsPath := run.LogsPath
	if logsPath == "" {
		// Match the runtime's transcript path so re-attach appends to the same
		// file the original run was writing, keeping pre-restart output intact.
		logsPath = projectRunCommandTranscriptPath(run, sandbox)
	}
	var sendErr error
	writer := func(chunk domain.ExecChunk) {
		if sendErr != nil {
			return
		}
		filtered, visible := execution.FilterCommandStreamChunk(chunk)
		if !visible {
			return
		}
		offset, err := appendProjectRunLogChunk(logsPath, filtered)
		if err != nil {
			sendErr = err
			return
		}
		c.publishRunLogChunk(run.RunID, filtered, offset)
	}

	env := execution.BuildSandboxExecEnv(runCtx, c.config, sandbox, c.config.GuestHomePath)
	env["AGENT_COMPOSE_RUN_ID"] = run.RunID
	env["AGENT_COMPOSE_PROJECT_ID"] = run.ProjectID

	result := followDetachedCommand(runCtx, DetachedTask{
		Config:      c.config,
		Runtime:     runtime,
		Sandbox:     sandbox,
		VMState:     vmState,
		ExecutionID: run.ExecutionID,
		Env:         env,
		Cursor:      readDetachCursor(artifactsDir),
		Writer:      writer,
		PersistCursor: func(offset uint64) error {
			return writeDetachCursor(artifactsDir, offset)
		},
	})
	if errors.Is(result.Err, ErrRunDisconnected) {
		// Shut down again while following; leave the run running for the next
		// reconciliation.
		return run, nil
	}
	execErr := result.Err
	if pullErr := execution.SyncGuestDirToHost(transitionCtx, filepath.Join(c.config.GuestStateRoot, "runs", run.RunID), artifactsDir, guestDirReaderFor(runtime, sandbox, vmState)); pullErr != nil {
		execErr = errors.Join(execErr, fmt.Errorf("pull command artifacts: %w", pullErr))
	}

	transition, execErr := finishProjectRunCommand(projectRunCommandOutcome{
		Run:                run,
		Sandbox:            sandbox,
		CommandText:        readProjectRunCommandText(artifactsDir),
		ArtifactsDir:       artifactsDir,
		LogsPath:           logsPath,
		Result:             result.Result,
		ExecErr:            execErr,
		SendErr:            sendErr,
		ResultFileFallback: true,
	})
	if execErr != nil || transition.ExitCode != 0 {
		return c.completeProjectRunError(transitionCtx, runCtx, transition, execErr)
	}
	transition.Status = domain.ProjectRunStatusSucceeded
	return c.completeProjectRun(transitionCtx, transition)
}

// agentResumer is the optional AgentExecutor capability to finish a detached
// agent run started by an earlier daemon.
type agentResumer interface {
	ResumeAgentRequest(context.Context, *domain.Sandbox, execution.ResumeAgentRequest) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error)
}

// reattachDetachedAgentRun resumes a detached agent run: the executor
// re-attaches to the journal, appends the missed output to the run's cell
// log, and finishes the cell; the run is then finished from the cell as a
// fresh run would be.
func (c *Controller) reattachDetachedAgentRun(transitionCtx, runCtx context.Context, run domain.ProjectRunRecord, sandbox *domain.Sandbox) (domain.ProjectRunRecord, error) {
	fail := func(reason string) (domain.ProjectRunRecord, error) {
		c.deleteRetainedFacadeTokens(transitionCtx, run)
		transition := TransitionRequest{RunID: run.RunID, SandboxID: run.SandboxID, ExitCode: 1, Output: run.Output, LogsPath: run.LogsPath, Error: reason}
		return c.completeProjectRunError(transitionCtx, runCtx, transition, errors.New(reason))
	}
	resumer, ok := c.executor.(agentResumer)
	if !ok {
		return fail("detached agent run cannot be resumed: executor does not support resume")
	}
	cellID := agentCellIDFromLogsPath(run.LogsPath)
	if cellID == "" {
		return fail("detached agent run cannot be resumed: run has no agent cell")
	}
	cell, _, assistantEvent, execErr := resumer.ResumeAgentRequest(runCtx, sandbox, execution.ResumeAgentRequest{
		RunID:       run.RunID,
		CellID:      cellID,
		ExecutionID: run.ExecutionID,
		Stream: projectRunAgentExecutionStream(transitionCtx, agentExecutionStreamRequest{
			Run:     run,
			Sandbox: sandbox,
			Hub:     c.runLogs,
		}),
	})
	if errors.Is(execErr, ErrRunDisconnected) {
		// Shut down again while following; leave the run running.
		return run, nil
	}
	if execErr != nil && !errors.Is(execErr, domain.ErrExecTerminationUnconfirmed) {
		// The re-attach failed before ResumeAgentRun could retire the tokens,
		// and the run is finalized as failed below. An unconfirmed termination
		// may still leave the task running, so its tokens stay.
		c.deleteRetainedFacadeTokens(transitionCtx, run)
	}
	return c.finishProjectRunAgent(transitionCtx, runCtx, agentRunOutcome{
		Run: run, Sandbox: sandbox, Cell: cell, Assistant: assistantEvent, ExecErr: execErr,
	})
}

// deleteRetainedFacadeTokens retires the per-run facade tokens a detached agent
// or prompt run kept across a daemon restart when the re-attach is abandoned
// and the run is finalized as failed instead. Command runs issue no per-run
// tokens, so this is a no-op for them.
func (c *Controller) deleteRetainedFacadeTokens(ctx context.Context, run domain.ProjectRunRecord) {
	switch run.DetachKind {
	case domain.DetachKindAgent, domain.DetachKindInteractivePrompt:
		c.deleteRunFacadeTokens(context.WithoutCancel(ctx), run.RunID)
	}
}

// agentCellIDFromLogsPath recovers the agent cell id from the run's log path,
// which an agent run points at its cell's output file
// (<sandbox>/state/cells/<cell-id>/output.txt) when the cell starts.
func agentCellIDFromLogsPath(logsPath string) string {
	logsPath = strings.TrimSpace(logsPath)
	if logsPath == "" || filepath.Base(logsPath) != "output.txt" {
		return ""
	}
	cellDir := filepath.Dir(logsPath)
	if filepath.Base(filepath.Dir(cellDir)) != "cells" {
		return ""
	}
	return filepath.Base(cellDir)
}
