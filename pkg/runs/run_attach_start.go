package runs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func (c *Controller) RunProjectCommandAttachRegistered(ctx, inputCtx context.Context, receive RunAttachReceiver, send RunAttachSender, onStarted func(string, <-chan struct{})) error {
	if receive == nil || send == nil {
		return fmt.Errorf("run attach stream is required")
	}
	first, err := receive()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: run attach start frame is required", ErrInvalidRequest)
		}
		return err
	}
	if first.Kind != RunAttachInputStart {
		return fmt.Errorf("%w: first run attach frame must be start", ErrInvalidRequest)
	}
	if first.RunID != "" {
		if len(first.Request.InputFiles) > 0 {
			return fmt.Errorf("%w: files cannot be appended on reattach", ErrInvalidRequest)
		}
		return c.attachExistingInteractiveSession(ctx, first.RunID, receive, send)
	}
	mode := first.Mode
	req := first.Request
	commandText := strings.TrimSpace(req.Command)
	if mode == RunAttachModeUnspecified && commandText != "" {
		mode = RunAttachModeCommand
	}
	if mode == RunAttachModeUnspecified && strings.TrimSpace(req.Prompt) != "" {
		mode = RunAttachModePrompt
	}
	if mode != RunAttachModeCommand && mode != RunAttachModePrompt {
		return fmt.Errorf("%w: run attach command mode is required", ErrInvalidRequest)
	}
	if mode == RunAttachModeCommand && commandText == "" {
		return fmt.Errorf("%w: run attach command is required", ErrInvalidRequest)
	}
	if mode == RunAttachModePrompt && strings.TrimSpace(req.Prompt) == "" {
		return fmt.Errorf("%w: run attach prompt is required", ErrInvalidRequest)
	}
	req.fileInputStream = true
	started, err := c.StartProjectRun(ctx, req)
	if err != nil {
		return err
	}
	if started.Run.InputPreparation != nil && !started.Run.InputAccepted {
		input := replayRunInput(started.Run.InputPreparation)
		if input.State == domain.RunInputStateReady {
			return send(RunAttachOutput{Kind: RunAttachOutputInputReady, Run: started.Run, Input: input})
		}
		return send(RunAttachOutput{Kind: RunAttachOutputError, Run: started.Run, Input: input, Code: "input_not_ready", Error: "run input preparation requires reconciliation", Terminal: true})
	}
	if started.Run.Status == domain.ProjectRunStatusRunning {
		// The run is already running (a retry with the same idempotency key
		// after it detached and survived a restart): tell the caller which run
		// it is and close, without opening a second interactive session. The
		// onStarted callback still fires with an already-released input so
		// startInteractiveRun can return the run it resolved to.
		if onStarted != nil {
			released := make(chan struct{})
			close(released)
			onStarted(started.Run.RunID, released)
		}
		return send(RunAttachOutput{
			Kind:      RunAttachOutputStarted,
			CreatedAt: time.Now().UTC(),
			Run:       started.Run,
			SandboxID: started.Run.SandboxID,
			Warnings:  append([]string(nil), started.Warnings...),
		})
	}
	session, releaseInput, err := c.interactiveSessions.CreateAttached(started.Run.RunID)
	if err != nil {
		return err
	}
	session.fileInput = len(req.InputFiles) > 0
	sessionTerminalState := InteractiveSessionCompleted
	defer func() { _ = c.interactiveSessions.Remove(started.Run.RunID, sessionTerminalState) }()
	inputCtx, cancelInput := context.WithCancel(inputCtx)
	defer cancelInput()
	inputReleased := startRunAttachInputForwarder(inputCtx, receive, session, first.DisconnectPolicy, releaseInput)
	if onStarted != nil {
		onStarted(started.Run.RunID, inputReleased)
	}
	run, execErr, err := c.executeStartedProjectRunAttach(ctx, startedRunAttachContext{
		Run:          started.Run,
		Request:      req,
		Warnings:     started.Warnings,
		Start:        first,
		Mode:         mode,
		ReceiveInput: session.receiveContext,
	}, session.Receive(), newInteractiveRunOutputSender(session, first.DisconnectPolicy, send))
	if err != nil {
		if ctx.Err() != nil {
			sessionTerminalState = InteractiveSessionCanceled
		} else {
			sessionTerminalState = InteractiveSessionFailed
		}
		return err
	}
	if execErr != nil {
		if ctx.Err() != nil {
			sessionTerminalState = InteractiveSessionCanceled
		} else {
			sessionTerminalState = InteractiveSessionFailed
		}
		return nil
	}
	_ = run
	return nil
}
