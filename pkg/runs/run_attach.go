package runs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/internal/projects"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/telemetry"
)

// startedRunAttachContext bundles the started run's state and the first
// attach frame executeStartedProjectRunAttach needs to run the interactive
// attach loop.
type startedRunAttachContext struct {
	Run      domain.ProjectRunRecord
	Request  RunAgentRequest
	Warnings []string
	Start    RunAttachInput
	Mode     RunAttachMode
}

func (c *Controller) executeStartedProjectRunAttach(ctx context.Context, attach startedRunAttachContext, receive RunAttachReceiver, send RunAttachSender) (record domain.ProjectRunRecord, execErr error, err error) {
	// The attach path runs the same run lifecycle as a streamed run, so it joins
	// the caller's trace and carries the same invoke_agent span.
	runStartedAt := time.Now()
	ctx, span := c.startRunSpan(ctx, attach.Run.RunID, attach.Request.ProjectID, attach.Request.AgentName)
	defer func() { c.endRunSpan(ctx, span, runStartedAt, record, errors.Join(err, execErr)) }()
	run := attach.Run
	req := attach.Request
	warnings := attach.Warnings
	start := attach.Start
	mode := attach.Mode
	coordinator := NewCoordinator(c.configDB, projects.StableProjectRunID)
	transitionCtx := context.WithoutCancel(ctx)
	prepared, err := c.prepareProjectRun(ctx, run, req.Env)
	if err != nil {
		run, markErr := c.completeProjectRunError(transitionCtx, ctx, TransitionRequest{
			RunID: run.RunID,
			Error: fmt.Sprintf("workspace preparation failed: %v", err),
		}, err)
		return withRunWarnings(run, warnings), err, markErr
	}
	sandboxResult, err := c.ensureProjectRunSandbox(ctx, run, prepared, req)
	if err != nil {
		transition := TransitionRequest{
			RunID: run.RunID,
			Error: fmt.Sprintf("sandbox start failed: %v", err),
		}
		if sandboxResult.Sandbox != nil {
			transition.SandboxID = sandboxResult.Sandbox.Summary.ID
		}
		run, markErr := c.completeProjectRunError(transitionCtx, ctx, transition, err)
		return withRunWarnings(run, warnings), err, markErr
	}
	span.SetAttributes(telemetry.AttrSandboxID.String(sandboxResult.Sandbox.Summary.ID))
	warnings = append(warnings, sandboxResult.Warnings...)
	run, err = coordinator.MarkRunning(transitionCtx, run.RunID, sandboxResult.Sandbox.Summary.ID)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	run = withRunWarnings(run, warnings)
	var transition TransitionRequest
	runCtx := interactionRunContext{Coordinator: coordinator, Run: run, Sandbox: sandboxResult.Sandbox, Request: req}
	switch mode {
	case RunAttachModePrompt:
		transition, execErr = c.runPromptInteraction(ctx, runCtx, start, receive, send)
	default:
		transition, execErr = c.runCommandInteraction(ctx, runCtx, start, receive, send)
	}
	if execErr != nil || transition.ExitCode != 0 {
		run, err = c.completeProjectRunError(transitionCtx, ctx, transition, execErr)
		if err != nil {
			return domain.ProjectRunRecord{}, nil, err
		}
		run = withRunWarnings(run, warnings)
		_ = send(runAttachResultResponse(run, transition, false))
		return run, execErr, nil
	}
	transition.Status = domain.ProjectRunStatusSucceeded
	run, err = c.completeProjectRun(transitionCtx, transition)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	run = withRunWarnings(run, warnings)
	if err := send(runAttachResultResponse(run, transition, true)); err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	return run, nil, nil
}

// interactionRunContext bundles the coordinator, run, target sandbox, and
// originating request shared by the prompt/command attach interaction paths.
type interactionRunContext struct {
	Coordinator *Coordinator
	Run         domain.ProjectRunRecord
	Sandbox     *domain.Sandbox
	Request     RunAgentRequest
}

// commandInteractionSession is the runtime interaction session
// openCommandInteraction hands back to runCommandInteraction once the
// runtime dependencies are validated, start artifacts are written, and the
// interaction is open.
type commandInteractionSession struct {
	Run         domain.ProjectRunRecord
	Interaction driverpkg.RuntimeInteraction
	LogsPath    string
}

func (c *Controller) openCommandInteraction(ctx context.Context, runCtx interactionRunContext, commandText string, start RunAttachInput) (commandInteractionSession, error) {
	run := runCtx.Run
	sandbox := runCtx.Sandbox
	req := runCtx.Request
	artifactsDir := projectRunCommandArtifactsDir(run, sandbox)
	logsPath := filepath.Join(artifactsDir, "transcript.txt")
	if c.store == nil || c.runtime == nil {
		return commandInteractionSession{}, fmt.Errorf("command runtime dependencies are required")
	}
	appconfig.ApplyDefaultGuestPaths(c.config)
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return commandInteractionSession{}, err
	}
	runtime, err := c.runtime(sandbox)
	if err != nil {
		return commandInteractionSession{}, err
	}
	interactionRuntime, ok := runtime.(InteractionRuntime)
	if !ok {
		return commandInteractionSession{}, fmt.Errorf("%w: command attach is unsupported by this runtime driver", domain.ErrUnsupported)
	}
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return commandInteractionSession{}, err
	}
	run, err = markProjectRunInteractionArtifacts(ctx, runCtx, logsPath, artifactsDir)
	if err != nil {
		return commandInteractionSession{}, err
	}
	spec := driverpkg.RuntimeStartSpec{
		OperationID: run.RunID,
		Kind:        driverpkg.RuntimeOperationCommand,
		Origin:      "run_attach",
		Command: &driverpkg.RuntimeCommandSpec{
			Command: "bash",
			Args:    []string{"-lc", commandText},
			Env:     execEnvMap(req.Env),
			Cwd:     c.config.GuestWorkspacePath,
		},
		Cwd:         c.config.GuestWorkspacePath,
		Env:         execEnvMap(req.Env),
		AttachStdin: start.AttachStdin,
		TTY:         start.TTY,
		Rows:        start.Rows,
		Cols:        start.Cols,
	}
	interaction, err := interactionRuntime.OpenInteraction(ctx, sandbox, vmState, spec)
	if err != nil {
		return commandInteractionSession{}, err
	}
	interaction, err = c.interactiveSessions.BindRuntime(run.RunID, interaction)
	if err != nil {
		return commandInteractionSession{}, err
	}
	return commandInteractionSession{Run: run, Interaction: interaction, LogsPath: logsPath}, nil
}

func (c *Controller) runCommandInteraction(ctx context.Context, runCtx interactionRunContext, start RunAttachInput, receive RunAttachReceiver, send RunAttachSender) (TransitionRequest, error) {
	run := runCtx.Run
	sandbox := runCtx.Sandbox
	commandText := strings.TrimSpace(runCtx.Request.Command)
	session, err := c.openCommandInteraction(ctx, runCtx, commandText, start)
	if err != nil {
		logsPath := filepath.Join(projectRunCommandArtifactsDir(run, sandbox), "transcript.txt")
		return TransitionRequest{RunID: run.RunID, SandboxID: sandbox.Summary.ID, LogsPath: logsPath, ExitCode: 1, Error: fmt.Sprintf("command execution failed: %v", err)}, err
	}
	transition := TransitionRequest{RunID: run.RunID, SandboxID: sandbox.Summary.ID}
	run = session.Run
	logsPath := session.LogsPath
	transition.LogsPath = logsPath
	interaction := session.Interaction
	defer func() { _ = interaction.CloseSend() }()
	go pumpRunAttachInput(receive, interaction)
	accumulator := execution.ExecStreamAccumulator{}
	for {
		frame, err := interaction.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				result, waitErr := interaction.Wait()
				if waitErr != nil {
					transition.ExitCode = 1
					transition.Error = fmt.Sprintf("command execution failed: %v", waitErr)
					return transition, waitErr
				}
				return transitionFromRuntimeResult(run, sandbox, runtimeOutcome{Command: commandText, LogsPath: logsPath, Accumulated: accumulator.Result(result.ExitCode, result.Success), Result: result}), nil
			}
			transition.ExitCode = 1
			transition.Error = fmt.Sprintf("command execution failed: %v", err)
			_ = send(runAttachErrorResponse("runtime_recv_error", err.Error(), true))
			return transition, err
		}
		switch frame.Type {
		case driverpkg.RuntimeOutputStarted:
			if err := send(runAttachStartedResponse(run, sandbox, warningsFromRun(run), frame.StartedAt)); err != nil {
				transition.ExitCode = 1
				transition.Error = fmt.Sprintf("command execution failed: %v", err)
				return transition, err
			}
		case driverpkg.RuntimeOutputStdout, driverpkg.RuntimeOutputStderr:
			stream := driverOutputStreamToRun(frame.Type)
			chunk := domain.ExecChunk{Text: string(frame.Data), Stream: stream}
			accumulator.WriteChunk(chunk)
			offset, err := appendProjectRunLogChunk(logsPath, chunk)
			if err != nil {
				transition.ExitCode = 1
				transition.Error = fmt.Sprintf("command execution failed: %v", err)
				return transition, err
			}
			c.publishRunLogChunk(run.RunID, chunk, offset)
			if err := send(runAttachOutputResponse(frame.Data, stream, start.TTY)); err != nil {
				transition.ExitCode = 1
				transition.Error = fmt.Sprintf("command execution failed: %v", err)
				return transition, err
			}
		case driverpkg.RuntimeOutputResult:
			result := frame.Result
			if result == nil {
				result = &driverpkg.RuntimeResult{OperationID: run.RunID, Success: true}
			}
			return transitionFromRuntimeResult(run, sandbox, runtimeOutcome{Command: commandText, LogsPath: logsPath, Accumulated: accumulator.Result(result.ExitCode, result.Success), Result: *result, Err: errorFromRuntimeResult(*result)}), nil
		case driverpkg.RuntimeOutputError:
			code := "runtime_error"
			message := "runtime interaction failed"
			if frame.Error != nil {
				code = firstNonEmpty(frame.Error.Code, code)
				message = firstNonEmpty(frame.Error.Message, message)
			}
			_ = send(runAttachErrorResponse(code, message, true))
			transition.ExitCode = 1
			transition.Error = message
			return transition, errors.New(message)
		}
	}
}

func markProjectRunInteractionArtifacts(ctx context.Context, runCtx interactionRunContext, logsPath, artifactsDir string) (domain.ProjectRunRecord, error) {
	coordinator := runCtx.Coordinator
	run := runCtx.Run
	sandbox := runCtx.Sandbox
	if coordinator == nil || sandbox == nil {
		return run, nil
	}
	return coordinator.TransitionRun(context.WithoutCancel(ctx), TransitionRequest{
		RunID:        run.RunID,
		Status:       domain.ProjectRunStatusRunning,
		SandboxID:    sandbox.Summary.ID,
		LogsPath:     logsPath,
		ArtifactsDir: artifactsDir,
	})
}

func pumpRunAttachInput(receive RunAttachReceiver, interaction driverpkg.RuntimeInteraction) {
	defer func() { _ = interaction.CloseSend() }()
	for {
		req, err := receive()
		if err != nil {
			return
		}
		frame, ok := runtimeInputFrameFromRunAttach(req)
		if !ok {
			_ = interaction.Send(driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputCancel, Message: "invalid run attach frame"})
			return
		}
		if err := interaction.Send(frame); err != nil {
			return
		}
	}
}

func runtimeInputFrameFromRunAttach(req RunAttachInput) (driverpkg.RuntimeInputFrame, bool) {
	switch req.Kind {
	case RunAttachInputStdin:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdin, Data: req.Data}, true
	case RunAttachInputStdinEOF:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdinEOF}, true
	case RunAttachInputResize:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputResize, Rows: req.Rows, Cols: req.Cols}, true
	case RunAttachInputSignal:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputSignal, Signal: driverpkg.RuntimeSignal(strings.TrimSpace(req.Signal))}, true
	case RunAttachInputCancel:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputCancel, Message: req.Reason}, true
	default:
		return driverpkg.RuntimeInputFrame{}, false
	}
}
