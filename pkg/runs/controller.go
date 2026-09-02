package runs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/capabilities"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/images"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/volumes"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

var (
	ErrInvalidRequest     = errors.New("invalid run request")
	ErrRunAgentStreamSend = errors.New("run agent stream send failed")
)

type AgentExecutor interface {
	PrepareSandboxAgentEnvironment(context.Context, *domain.Sandbox, execution.AgentConfig, *domain.AgentDefinition) error
	PrepareSandboxAgentEnvironmentFromTags(context.Context, *domain.Sandbox) error
	ExecuteAgentRequest(context.Context, *domain.Sandbox, execution.ExecuteAgentRequest) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error)
}

type Runtime interface {
	ExecStream(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error)
}

type InteractionRuntime interface {
	OpenInteraction(context.Context, *domain.Sandbox, domain.VMState, driverpkg.RuntimeStartSpec) (driverpkg.RuntimeInteraction, error)
}

type RuntimeProvider func(*domain.Sandbox) (Runtime, error)

type SandboxDriver interface {
	StartSandboxVM(context.Context, *domain.Sandbox) error
	StopSandboxVM(context.Context, *domain.Sandbox) error
	RemoveSandboxVM(context.Context, *domain.Sandbox) error
}

type TopicPublisher interface {
	Publish(domain.SchedulerTopicEvent) bool
}

type DashboardNotifier interface {
	Notify(reason string)
}

type CapabilitySandboxIndexer interface {
	IndexSandbox(*domain.Sandbox, []domain.TrustedHeader)
	RevokeSandbox(string)
}

type VolumeResolver interface {
	ResolveMounts(ctx context.Context, specs []domain.VolumeMountSpec, options volumes.ResolveOptions) ([]domain.SandboxVolumeMount, []string, error)
}

type ControllerStore interface {
	Store
	PreparationStore
	TriggerResolverStore
	workspaces.Store
}

type TriggerResolverStore interface {
	ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error)
	GetScheduler(context.Context, string) (domain.Scheduler, error)
}

type stickyBindingStore interface {
	GetSchedulerBinding(context.Context, string, string) (domain.SchedulerBinding, bool, error)
	CompareAndSwapSchedulerBinding(context.Context, *domain.SchedulerBinding, domain.SchedulerBinding) (bool, error)
}

// SandboxRuntimeStore is the subset of sandbox runtime persistence the run
// controller needs: sandbox lifecycle plus VM, proxy, and Jupyter-port state.
// Keeping it narrow decouples the controller from the concrete
// sandboxstore.Store (and its full method set) and lets tests substitute a
// fake. It is distinct from SandboxStore in statuses.go, which exposes only
// domain-typed lookups for status listing.
type SandboxRuntimeStore interface {
	CreateSandboxWithOptions(ctx context.Context, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *sandboxstore.SandboxWorkspace, envItems []sandboxstore.SandboxEnvVar, tags []sandboxstore.SandboxTag, options sandboxstore.CreateSandboxOptions) (*sandboxstore.Sandbox, error)
	GetSandbox(ctx context.Context, id string) (*sandboxstore.Sandbox, error)
	UpdateSandbox(ctx context.Context, sandbox *sandboxstore.Sandbox) error
	RemoveSandbox(ctx context.Context, id string) error
	AddEvent(ctx context.Context, sandboxID string, event sandboxstore.SandboxEvent) error
	GetVMState(id string) (sandboxstore.VMState, error)
	GetProxyState(id string) (sandboxstore.ProxyState, error)
	SaveProxyState(id string, state sandboxstore.ProxyState) error
	AllocateHostPortForJupyter() (int, error)
}

type Controller struct {
	config              *appconfig.Config
	store               SandboxRuntimeStore
	configDB            ControllerStore
	workspaceEnsurer    workspaces.WorkspaceEnsurer
	driver              SandboxDriver
	executor            AgentExecutor
	runtime             RuntimeProvider
	images              images.Backend
	schedulerEngine     schedulers.SchedulerEngine
	cap                 capabilities.Provider
	volumes             VolumeResolver
	streams             *sandboxes.StreamBroker
	bus                 TopicPublisher
	dashboard           DashboardNotifier
	capTokens           CapabilitySandboxIndexer
	runLogs             *RunLogHub
	lifecycleLocks      *sandboxes.LifecycleLocks
	removal             SandboxRemoval
	completion          *CompletionManager
	interactiveSessions *InteractiveSessionManager
}

type llmFacadeTokenDeleter interface {
	DeleteLLMFacadeToken(context.Context, string) error
}

type llmFacadeStore interface {
	llms.LLMResolverStore
	SaveLLMFacadeToken(context.Context, llms.FacadeToken) error
}

type ControllerDependencies struct {
	Config              *appconfig.Config
	Store               SandboxRuntimeStore
	ConfigDB            ControllerStore
	WorkspaceEnsurer    workspaces.WorkspaceEnsurer
	Driver              SandboxDriver
	Executor            AgentExecutor
	Runtime             RuntimeProvider
	Images              images.Backend
	SchedulerEngine     schedulers.SchedulerEngine
	Cap                 capabilities.Provider
	Volumes             VolumeResolver
	Streams             *sandboxes.StreamBroker
	Bus                 TopicPublisher
	Dashboard           DashboardNotifier
	CapTokens           CapabilitySandboxIndexer
	RunLogs             *RunLogHub
	LifecycleLocks      *sandboxes.LifecycleLocks
	Removal             SandboxRemoval
	Completion          *CompletionManager
	InteractiveSessions *InteractiveSessionManager
}

type SandboxRemoval interface {
	Remove(context.Context, string, bool) (sandboxes.RemovalResult, error)
}

func NewController(deps ControllerDependencies) *Controller {
	interactiveSessions := deps.InteractiveSessions
	if interactiveSessions == nil {
		interactiveSessions = NewInteractiveSessionManager()
	}
	return &Controller{
		config:              deps.Config,
		store:               deps.Store,
		configDB:            deps.ConfigDB,
		workspaceEnsurer:    deps.WorkspaceEnsurer,
		driver:              deps.Driver,
		executor:            deps.Executor,
		runtime:             deps.Runtime,
		images:              deps.Images,
		schedulerEngine:     deps.SchedulerEngine,
		cap:                 deps.Cap,
		volumes:             deps.Volumes,
		streams:             deps.Streams,
		bus:                 deps.Bus,
		dashboard:           deps.Dashboard,
		capTokens:           deps.CapTokens,
		runLogs:             deps.RunLogs,
		lifecycleLocks:      deps.LifecycleLocks,
		removal:             deps.Removal,
		completion:          deps.Completion,
		interactiveSessions: interactiveSessions,
	}
}

type RunAgentRequest struct {
	ProjectID                string
	AgentName                string
	Prompt                   string
	Command                  string
	Source                   string
	SchedulerID              string
	SchedulerRunID           string
	TriggerID                string
	PayloadJSON              string
	ClientRequestID          string
	Env                      []*agentcomposev2.EnvVarSpec
	SandboxID                string
	Volumes                  []domain.VolumeMountSpec
	Driver                   string
	OutputSchemaJSON         string
	CleanupPolicy            agentcomposev2.RunSandboxCleanupPolicy
	Jupyter                  *agentcomposev2.RunJupyterSpec
	StickyBindingSchedulerID string
	StickyBindingTriggerID   string
	StickyBindingConfigHash  string
	Interactive              bool
}

type StreamSink struct {
	SendStarted func(run domain.ProjectRunRecord, createdAt time.Time) error
	SendChunk   func(runID string, chunk domain.ExecChunk, createdAt time.Time) error
}

type StartedProjectRun struct {
	Run      domain.ProjectRunRecord
	Execute  func(context.Context, *StreamSink) (domain.ProjectRunRecord, error, error)
	Warnings []string
}

func (c *Controller) StartProjectRun(ctx context.Context, req RunAgentRequest) (StartedProjectRun, error) {
	if c.configDB == nil {
		return StartedProjectRun{}, fmt.Errorf("config store is required")
	}
	trustedHeaders := domain.TrustedHeadersFromContext(ctx)
	commandText := strings.TrimSpace(req.Command)
	if commandText != "" && (strings.TrimSpace(req.Prompt) != "" || strings.TrimSpace(req.TriggerID) != "") {
		return StartedProjectRun{}, fmt.Errorf("%w: run requires only one of command, prompt, or trigger", ErrInvalidRequest)
	}
	req.SandboxID = strings.TrimSpace(req.SandboxID)
	if strings.TrimSpace(req.SandboxID) != "" && strings.TrimSpace(req.Driver) != "" {
		return StartedProjectRun{}, fmt.Errorf("%w: run driver cannot be combined with an existing sandbox", ErrInvalidRequest)
	}
	if strings.TrimSpace(req.SandboxID) != "" && len(req.Volumes) > 0 {
		return StartedProjectRun{}, fmt.Errorf("%w: run volumes cannot be combined with an existing sandbox", ErrInvalidRequest)
	}
	resolved, err := c.resolveTriggerForManualRun(ctx, req)
	if err != nil {
		return StartedProjectRun{}, err
	}
	req = resolved.Request
	warnings := resolved.Warnings
	coordinator := NewCoordinator(c.configDB, projects.StableProjectRunID)
	run, err := coordinator.BeginRun(ctx, StartRequest{
		ProjectID:       req.ProjectID,
		AgentName:       req.AgentName,
		Source:          req.Source,
		SchedulerID:     req.SchedulerID,
		SchedulerRunID:  req.SchedulerRunID,
		TriggerID:       req.TriggerID,
		Prompt:          req.Prompt,
		Driver:          req.Driver,
		CleanupPolicy:   CleanupPolicyFromProto(req.CleanupPolicy),
		ClientRequestID: req.ClientRequestID,
	})
	if err != nil {
		return StartedProjectRun{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	run = withRunWarnings(run, warnings)
	return StartedProjectRun{
		Run:      run,
		Warnings: warnings,
		Execute: func(execCtx context.Context, stream *StreamSink) (domain.ProjectRunRecord, error, error) {
			// Async runs execute from the daemon root context. Restore only the
			// request metadata they need instead of retaining the transport context.
			execCtx = domain.NewContextWithTrustedHeaders(execCtx, trustedHeaders)
			return c.executeStartedProjectRun(execCtx, startedProjectRunContext{
				Coordinator: coordinator,
				Run:         run,
				Request:     req,
				Warnings:    warnings,
			}, stream)
		},
	}, nil
}

func (c *Controller) RunProjectAgent(ctx context.Context, req RunAgentRequest, stream *StreamSink) (domain.ProjectRunRecord, error, error) {
	started, err := c.StartProjectRun(ctx, req)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	return started.Execute(ctx, stream)
}

func (c *Controller) RunProjectCommandAttach(ctx context.Context, receive RunAttachReceiver, send RunAttachSender) error {
	return c.RunProjectCommandAttachRegistered(ctx, ctx, receive, send, nil)
}

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
	started, err := c.StartProjectRun(ctx, req)
	if err != nil {
		return err
	}
	session, releaseInput, err := c.interactiveSessions.CreateAttached(started.Run.RunID)
	if err != nil {
		return err
	}
	sessionTerminalState := InteractiveSessionCompleted
	defer func() { _ = c.interactiveSessions.Remove(started.Run.RunID, sessionTerminalState) }()
	inputCtx, cancelInput := context.WithCancel(inputCtx)
	defer cancelInput()
	inputReleased := startRunAttachInputForwarder(inputCtx, receive, session, first.DisconnectPolicy, releaseInput)
	if onStarted != nil {
		onStarted(started.Run.RunID, inputReleased)
	}
	run, execErr, err := c.executeStartedProjectRunAttach(ctx, startedRunAttachContext{
		Run:      started.Run,
		Request:  req,
		Warnings: started.Warnings,
		Start:    first,
		Mode:     mode,
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

func (c *Controller) attachExistingInteractiveSession(ctx context.Context, runID string, receive RunAttachReceiver, send RunAttachSender) error {
	runID = strings.TrimSpace(runID)
	if c.configDB == nil {
		return fmt.Errorf("config store is required")
	}
	run, err := c.configDB.GetProjectRun(ctx, runID)
	if err != nil {
		return err
	}
	if StatusIsTerminal(run.Status) {
		return fmt.Errorf("%w: run %s is terminal", domain.ErrFailedPrecondition, runID)
	}
	attachment, err := c.interactiveSessions.Attach(runID)
	if err != nil {
		return interactiveSessionDomainError(err)
	}
	defer attachment.Close()
	session, err := c.interactiveSessions.Get(runID)
	if err != nil {
		return err
	}
	outputs, unsubscribe, err := session.Subscribe()
	if err != nil {
		return err
	}
	defer unsubscribe()
	receiveErr := make(chan error, 1)
	go func() {
		for {
			input, err := receive()
			if err != nil {
				if errors.Is(err, io.EOF) {
					receiveErr <- nil
					return
				}
				receiveErr <- err
				return
			}
			if input.Kind == RunAttachInputStart {
				receiveErr <- fmt.Errorf("%w: duplicate run attach start frame", ErrInvalidRequest)
				return
			}
			if err := attachment.Send(ctx, input); err != nil {
				receiveErr <- err
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-receiveErr:
			return err
		case output, ok := <-outputs:
			if !ok {
				return nil
			}
			if err := send(output); err != nil {
				return err
			}
		}
	}
}

func interactiveSessionDomainError(err error) error {
	switch {
	case errors.Is(err, ErrInteractiveSessionNotFound):
		return fmt.Errorf("%w: %w", domain.ErrNotFound, err)
	case errors.Is(err, ErrInteractiveSessionClosed):
		return fmt.Errorf("%w: %w", domain.ErrFailedPrecondition, err)
	case errors.Is(err, ErrInteractiveSessionAttached):
		return fmt.Errorf("%w: %w", domain.ErrConflict, err)
	default:
		return err
	}
}

func newInteractiveRunOutputSender(session *InteractiveSession, policy AttachDisconnectPolicy, send RunAttachSender) RunAttachSender {
	detached := false
	return func(output RunAttachOutput) error {
		session.Publish(output)
		if detached {
			return nil
		}
		err := send(output)
		if err != nil && policy == AttachDisconnectDetach {
			detached = true
			return nil
		}
		return err
	}
}

func forwardRunAttachInputs(ctx context.Context, receive RunAttachReceiver, session *InteractiveSession, policy AttachDisconnectPolicy) {
	sentEOF := false
	for {
		input, err := receive()
		if err != nil {
			// A clean half-close means the client will send nothing more, so
			// forward it as stdin EOF: prompt sessions otherwise block on
			// session input forever and never close the runtime turn. Detached
			// clients keep the run open for a later attach, so leave their
			// input stream alone.
			if errors.Is(err, io.EOF) && !sentEOF && policy != AttachDisconnectDetach {
				_ = session.Send(ctx, RunAttachInput{Kind: RunAttachInputStdinEOF})
			}
			return
		}
		if input.Kind == RunAttachInputStdinEOF {
			sentEOF = true
		}
		if err := session.Send(ctx, input); err != nil {
			return
		}
	}
}

func startRunAttachInputForwarder(ctx context.Context, receive RunAttachReceiver, session *InteractiveSession, policy AttachDisconnectPolicy, release func()) <-chan struct{} {
	released := make(chan struct{})
	go func() {
		defer close(released)
		defer release()
		forwardRunAttachInputs(ctx, receive, session, policy)
	}()
	return released
}

// startedProjectRunContext bundles the run-scoped state StartProjectRun
// captures at begin-run time, needed once the caller invokes Execute to
// actually run it.
type startedProjectRunContext struct {
	Coordinator *Coordinator
	Run         domain.ProjectRunRecord
	Request     RunAgentRequest
	Warnings    []string
}

func (c *Controller) executeStartedProjectRun(ctx context.Context, started startedProjectRunContext, stream *StreamSink) (domain.ProjectRunRecord, error, error) {
	coordinator := started.Coordinator
	run := started.Run
	req := started.Request
	warnings := started.Warnings
	commandText := strings.TrimSpace(req.Command)
	transitionCtx := context.WithoutCancel(ctx)
	prepared, err := c.prepareProjectRun(ctx, run, req.Env)
	if err != nil {
		transition := TransitionRequest{
			RunID: run.RunID,
			Error: fmt.Sprintf("workspace preparation failed: %v", err),
		}
		run, markErr := c.completeProjectRunError(transitionCtx, ctx, transition, err)
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = withRunWarnings(run, warnings)
		return run, err, nil
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
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	warnings = append(warnings, sandboxResult.Warnings...)
	if err := ctx.Err(); err != nil {
		stopReason := err.Error()
		if cause := context.Cause(ctx); cause != nil {
			stopReason = cause.Error()
		}
		run, markErr := c.completeProjectRun(transitionCtx, TransitionRequest{
			RunID:     run.RunID,
			Status:    domain.ProjectRunStatusCanceled,
			SandboxID: sandboxResult.Sandbox.Summary.ID,
			Error:     stopReason,
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	if current, loadErr := c.configDB.GetProjectRun(transitionCtx, run.RunID); loadErr == nil && StatusIsTerminal(current.Status) {
		run = current
		run = withRunWarnings(run, warnings)
		return run, context.Canceled, nil
	}
	run, err = coordinator.MarkRunning(transitionCtx, run.RunID, sandboxResult.Sandbox.Summary.ID)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	run = withRunWarnings(run, warnings)
	sandboxed := sandboxedProjectRun{
		TransitionCtx: transitionCtx,
		Coordinator:   coordinator,
		Run:           run,
		Sandbox:       sandboxResult.Sandbox,
		Request:       req,
		Warnings:      warnings,
		Stream:        stream,
	}
	if commandText != "" {
		return c.completeProjectRunCommand(ctx, sandboxed, commandText)
	}
	return c.completeProjectRunAgent(ctx, sandboxed)
}

// sandboxedProjectRun bundles the run-scoped state shared by the command and
// agent execution branches once ensureProjectRunSandbox has produced a ready
// sandbox: the transition-safe context, coordinator, run, sandbox, original
// request, accumulated warnings, and output stream.
type sandboxedProjectRun struct {
	TransitionCtx context.Context
	Coordinator   *Coordinator
	Run           domain.ProjectRunRecord
	Sandbox       *domain.Sandbox
	Request       RunAgentRequest
	Warnings      []string
	Stream        *StreamSink
}

func (c *Controller) completeProjectRunCommand(ctx context.Context, sandboxed sandboxedProjectRun, commandText string) (domain.ProjectRunRecord, error, error) {
	run, warnings := sandboxed.Run, sandboxed.Warnings
	transition, execErr := c.executeProjectRunCommand(ctx, projectRunCommandExecution{
		Run:         run,
		Sandbox:     sandboxed.Sandbox,
		Request:     sandboxed.Request,
		CommandText: commandText,
		Sink:        sandboxed.Stream,
	})
	if execErr != nil || transition.ExitCode != 0 {
		run, err := c.completeProjectRunError(sandboxed.TransitionCtx, ctx, transition, execErr)
		if err != nil {
			return domain.ProjectRunRecord{}, nil, err
		}
		return withRunWarnings(run, warnings), execErr, nil
	}
	transition.Status = domain.ProjectRunStatusSucceeded
	run, err := c.completeProjectRun(sandboxed.TransitionCtx, transition)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	return withRunWarnings(run, warnings), nil, nil
}

func (c *Controller) completeProjectRunAgent(ctx context.Context, sandboxed sandboxedProjectRun) (domain.ProjectRunRecord, error, error) {
	run, warnings, req := sandboxed.Run, sandboxed.Warnings, sandboxed.Request
	agentConfig, err := c.projectRunAgentConfig(ctx, run)
	if err != nil {
		run, markErr := c.completeProjectRun(sandboxed.TransitionCtx, TransitionRequest{
			RunID:     run.RunID,
			Status:    domain.ProjectRunStatusFailed,
			SandboxID: sandboxed.Sandbox.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		return withRunWarnings(run, warnings), err, nil
	}
	if c.executor == nil {
		err = fmt.Errorf("executor is required")
		run, markErr := c.completeProjectRun(sandboxed.TransitionCtx, TransitionRequest{
			RunID:     run.RunID,
			Status:    domain.ProjectRunStatusFailed,
			SandboxID: sandboxed.Sandbox.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		return withRunWarnings(run, warnings), err, nil
	}
	cell, _, assistantEvent, execErr := c.executor.ExecuteAgentRequest(ctx, sandboxed.Sandbox, execution.ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: run.AgentID,
		Model:             agentConfig.Model,
		RunID:             run.RunID,
		Message:           req.Prompt,
		OutputSchemaJSON:  req.OutputSchemaJSON,
		Stream: projectRunAgentExecutionStream(sandboxed.TransitionCtx, agentExecutionStreamRequest{
			Coordinator: sandboxed.Coordinator,
			Run:         run,
			Sandbox:     sandboxed.Sandbox,
			Sink:        sandboxed.Stream,
			Hub:         c.runLogs,
		}),
	})
	transition := TransitionFromAgentCell(run, sandboxed.Sandbox, cell, execErr)
	transition.TerminalEvents = projectAgentTerminalEvents(run, cell, assistantEvent, execErr)
	if execErr != nil || !cell.Success {
		run, err = c.completeProjectRunError(sandboxed.TransitionCtx, ctx, transition, execErr)
		if err != nil {
			return domain.ProjectRunRecord{}, nil, err
		}
		return withRunWarnings(run, warnings), execErr, nil
	}
	transition.Status = domain.ProjectRunStatusSucceeded
	run, err = c.completeProjectRun(sandboxed.TransitionCtx, transition)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	return withRunWarnings(run, warnings), nil, nil
}
