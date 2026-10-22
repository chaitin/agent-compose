package runs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// interactiveDetach is the detached-execution part of an interactive run.
type interactiveDetach struct {
	ExecutionID string
	Kind        string
	// RunCtx is the run context: it survives daemon shutdown and carries the
	// shutdown signal, so the detached attach can tell a shutdown (disconnect)
	// from a user stop (stop the task).
	RunCtx context.Context
}

// interactiveRunDetach decides whether an interactive run goes behind the
// guest supervisor. Only runs that already outlive their client do (the
// detach disconnect policy): a run bound to its client's connection still
// ends with it. A TTY session stays direct because the supervisor has no
// terminal to offer.
func (c *Controller) interactiveRunDetach(runContext context.Context, runCtx interactionRunContext, start RunAttachInput, mode RunAttachMode) *interactiveDetach {
	if start.DisconnectPolicy != AttachDisconnectDetach || start.TTY {
		return nil
	}
	sandbox := runCtx.Sandbox
	if c.runtime == nil || c.store == nil || sandbox == nil {
		return nil
	}
	runtime, err := c.runtime(sandbox)
	if err != nil {
		return nil
	}
	if _, ok := runtime.(InteractionRuntime); !ok {
		return nil
	}
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return nil
	}
	if !c.detachSupported(runContext, runtime, sandbox, vmState) {
		return nil
	}
	kind := domain.DetachKindInteractiveCommand
	if mode == RunAttachModePrompt {
		kind = domain.DetachKindInteractivePrompt
	}
	return &interactiveDetach{ExecutionID: newDetachExecutionID(runCtx.Run), Kind: kind, RunCtx: runContext}
}

// interactiveDetachStart is what starting a detached interactive task needs
// beyond the run itself.
type interactiveDetachStart struct {
	Runtime      Runtime
	Interaction  InteractionRuntime
	VMState      domain.VMState
	ArtifactsDir string
	TaskArgs     []string
	RawCommand   bool
	Env          map[string]string
	Origin       string
}

// startInteractiveDetached records the run as detached and starts its task
// behind the supervisor, returning the attach interaction.
func (c *Controller) startInteractiveDetached(runCtx interactionRunContext, in interactiveDetachStart) (driverpkg.RuntimeInteraction, error) {
	detach := runCtx.Detach
	deadline, _ := detach.RunCtx.Deadline()
	if err := c.persistDetachedRunFields(context.WithoutCancel(detach.RunCtx), runCtx.Run, detach.ExecutionID, detach.Kind, deadline); err != nil {
		return nil, fmt.Errorf("persist detached run fields: %w", err)
	}
	task := c.interactiveDetachedTask(runCtx.Run, runCtx.Sandbox, in, detach.ExecutionID)
	task.TaskTimeout = remainingTimeout(detach.RunCtx)
	return startDetachedInteraction(detach.RunCtx, detachedInteractionOptions{
		Task:        task,
		Interaction: in.Interaction,
		OperationID: runCtx.Run.RunID,
		Origin:      in.Origin,
		RawCommand:  in.RawCommand,
	})
}

func (c *Controller) interactiveDetachedTask(run domain.ProjectRunRecord, sandbox *domain.Sandbox, in interactiveDetachStart, executionID string) DetachedTask {
	env := execution.BuildSandboxExecEnv(context.Background(), c.config, sandbox, c.config.GuestHomePath)
	for key, value := range in.Env {
		env[key] = value
	}
	env["AGENT_COMPOSE_RUN_ID"] = run.RunID
	env["AGENT_COMPOSE_PROJECT_ID"] = run.ProjectID
	artifactsDir := in.ArtifactsDir
	return DetachedTask{
		Config:      c.config,
		Runtime:     in.Runtime,
		Sandbox:     sandbox,
		VMState:     in.VMState,
		ExecutionID: executionID,
		TaskArgs:    in.TaskArgs,
		Env:         env,
		PersistCursor: func(offset uint64) error {
			return writeDetachCursor(artifactsDir, offset)
		},
	}
}

func interactiveCommandFile(artifactsDir string) string {
	return filepath.Join(artifactsDir, "interactive-command.txt")
}

// writeInteractiveCommandText keeps the command of a detached interactive
// command run, which is otherwise only in the start request, for the daemon
// that finishes the run.
func writeInteractiveCommandText(artifactsDir, commandText string) error {
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(interactiveCommandFile(artifactsDir), []byte(commandText), 0o600)
}

func readInteractiveCommandText(artifactsDir string) string {
	data, err := os.ReadFile(interactiveCommandFile(artifactsDir))
	if err != nil {
		return ""
	}
	return string(data)
}

// reattachInteractiveRun resumes a detached interactive run after a daemon
// restart. It recreates the run's interactive session so clients can attach
// again by run id, feeds the session's input to the task, publishes the
// task's output to the session's subscribers, and finishes the run when the
// task exits.
func (c *Controller) reattachInteractiveRun(transitionCtx, runCtx context.Context, run domain.ProjectRunRecord, sandbox *domain.Sandbox) (domain.ProjectRunRecord, error) {
	fail := func(err error) (domain.ProjectRunRecord, error) {
		c.deleteRetainedFacadeTokens(transitionCtx, run)
		transition := TransitionRequest{RunID: run.RunID, SandboxID: run.SandboxID, ExitCode: 1, LogsPath: run.LogsPath, Error: fmt.Sprintf("re-attach interactive run: %v", err)}
		return c.completeProjectRunError(transitionCtx, runCtx, transition, err)
	}
	runtime, err := c.runtime(sandbox)
	if err != nil {
		return fail(err)
	}
	interactionRuntime, ok := runtime.(InteractionRuntime)
	if !ok {
		return fail(fmt.Errorf("%w: runtime driver cannot attach interactively", domain.ErrUnsupported))
	}
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return fail(err)
	}
	session, err := c.interactiveSessions.Create(run.RunID)
	if err != nil {
		return fail(err)
	}
	sessionState := InteractiveSessionCompleted
	defer func() { _ = c.interactiveSessions.Remove(run.RunID, sessionState) }()
	if err := session.transition(InteractiveSessionDetached); err != nil {
		return fail(err)
	}

	artifactsDir := projectRunCommandArtifactsDir(run, sandbox)
	in := interactiveDetachStart{Runtime: runtime, Interaction: interactionRuntime, VMState: vmState, ArtifactsDir: artifactsDir}
	task := c.interactiveDetachedTask(run, sandbox, in, run.ExecutionID)
	task.Cursor = readDetachCursor(artifactsDir)
	interaction, err := openDetachedInteraction(runCtx, detachedInteractionOptions{Task: task, Interaction: interactionRuntime, OperationID: run.RunID, Origin: "run_attach_resume"})
	if errors.Is(err, ErrRunDisconnected) {
		return run, nil
	}
	if err != nil {
		return fail(err)
	}
	interaction, err = c.interactiveSessions.BindRuntime(run.RunID, interaction)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = interaction.CloseSend() }()

	publish := func(output RunAttachOutput) error {
		session.Publish(output)
		return nil
	}
	logsPath := firstNonEmpty(run.LogsPath, filepath.Join(artifactsDir, "transcript.txt"))
	var transition TransitionRequest
	var execErr error
	if run.DetachKind == domain.DetachKindInteractivePrompt {
		transition, execErr = c.resumePromptInteraction(runCtx, commandResume{Run: run, Sandbox: sandbox, LogsPath: logsPath, Interaction: interaction}, session.Receive(), publish)
	} else {
		transition, execErr = resumeCommandInteraction(runCtx, c, commandResume{
			Run: run, Sandbox: sandbox, LogsPath: logsPath, Command: readInteractiveCommandText(artifactsDir), Interaction: interaction,
		}, session.Receive(), publish)
	}
	if errors.Is(execErr, ErrRunDisconnected) {
		return run, nil
	}
	if run.DetachKind == domain.DetachKindInteractivePrompt {
		c.deleteRunFacadeTokens(transitionCtx, run.RunID)
	}
	if execErr != nil || transition.ExitCode != 0 {
		sessionState = InteractiveSessionFailed
		if runCtx.Err() != nil {
			sessionState = InteractiveSessionCanceled
		}
		finished, err := c.completeProjectRunError(transitionCtx, runCtx, transition, execErr)
		if err == nil {
			session.Publish(runAttachResultResponse(finished, transition, false))
		}
		return finished, err
	}
	transition.Status = domain.ProjectRunStatusSucceeded
	finished, err := c.completeProjectRun(transitionCtx, transition)
	if err == nil {
		session.Publish(runAttachResultResponse(finished, transition, true))
	}
	return finished, err
}

// resumePromptInteraction continues the prompt loop of a detached session.
// The start frame and first message were sent by the daemon that started the
// run; this one only pumps later input and projects the remaining output.
func (c *Controller) resumePromptInteraction(ctx context.Context, in commandResume, receive RunAttachReceiver, send RunAttachSender) (TransitionRequest, error) {
	run, sandbox, logsPath, interaction := in.Run, in.Sandbox, in.LogsPath, in.Interaction
	transition := TransitionRequest{RunID: run.RunID, SandboxID: sandbox.Summary.ID, LogsPath: logsPath}
	projector := newPersistentPromptAttachProjector(context.WithoutCancel(ctx), persistentPromptAttachProjectorDeps{Run: run, Sandbox: sandbox, LogsPath: logsPath, Hub: c.runLogs, EventStore: c.configDB})
	input := &promptWrapperInput{interaction: interaction}
	// Whether a turn was in flight is not known here. The guest runner reads
	// its input in order, so a message sent during a turn waits for it.
	turnReady := make(chan struct{}, 1)
	releasePromptTurn(turnReady)
	inputCtx, cancelInput := context.WithCancel(ctx)
	defer cancelInput()
	go pumpRunPromptAttachInput(inputCtx, receive, promptInputPump{Input: input, TurnReady: turnReady, OnHumanMessage: projector.AppendHumanMessageFrame})
	return receivePromptInteractionFrames(run, sandbox, transition, promptInteractionReceiveState{Interaction: interaction, Projector: projector, TurnReady: turnReady}, send)
}

// commandResume is what resuming a detached interactive session needs;
// Command is only used by command sessions.
type commandResume struct {
	Run         domain.ProjectRunRecord
	Sandbox     *domain.Sandbox
	LogsPath    string
	Command     string
	Interaction driverpkg.RuntimeInteraction
}

func resumeCommandInteraction(_ context.Context, c *Controller, in commandResume, receive RunAttachReceiver, send RunAttachSender) (TransitionRequest, error) {
	go pumpRunAttachInput(receive, in.Interaction)
	return c.receiveCommandInteractionFrames(commandInteractionReceive{
		Run: in.Run, Sandbox: in.Sandbox, LogsPath: in.LogsPath, Command: in.Command, Interaction: in.Interaction,
	}, send)
}

// deleteRunFacadeTokens retires the per-run facade tokens a detached
// interactive prompt run kept across a daemon restart.
func (c *Controller) deleteRunFacadeTokens(ctx context.Context, runID string) {
	store, ok := c.configDB.(interface {
		DeleteLLMFacadeTokensForRun(context.Context, string, string) error
	})
	if !ok || strings.TrimSpace(runID) == "" {
		return
	}
	_ = store.DeleteLLMFacadeTokensForRun(ctx, runID, "agent")
}
