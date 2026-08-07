package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/images"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/sandboxes"
	"agent-compose/pkg/schedulers"
	"agent-compose/pkg/storage/sandboxstore"
	"agent-compose/pkg/volumes"
	"agent-compose/pkg/workspaces"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
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
	config           *appconfig.Config
	store            SandboxRuntimeStore
	configDB         ControllerStore
	workspaceEnsurer workspaces.WorkspaceEnsurer
	driver           SandboxDriver
	executor         AgentExecutor
	runtime          RuntimeProvider
	images           images.Backend
	schedulerEngine  schedulers.SchedulerEngine
	cap              capabilities.Provider
	volumes          VolumeResolver
	streams          *sandboxes.StreamBroker
	bus              TopicPublisher
	dashboard        DashboardNotifier
	capTokens        CapabilitySandboxIndexer
	runLogs          *RunLogHub
	lifecycleLocks   *sandboxes.LifecycleLocks
	removal          SandboxRemoval
	completion       *CompletionManager
}

type llmFacadeTokenDeleter interface {
	DeleteLLMFacadeToken(context.Context, string) error
}

type llmFacadeStore interface {
	llms.LLMResolverStore
	SaveLLMFacadeToken(context.Context, llms.FacadeToken) error
}

type ControllerDependencies struct {
	Config           *appconfig.Config
	Store            SandboxRuntimeStore
	ConfigDB         ControllerStore
	WorkspaceEnsurer workspaces.WorkspaceEnsurer
	Driver           SandboxDriver
	Executor         AgentExecutor
	Runtime          RuntimeProvider
	Images           images.Backend
	SchedulerEngine  schedulers.SchedulerEngine
	Cap              capabilities.Provider
	Volumes          VolumeResolver
	Streams          *sandboxes.StreamBroker
	Bus              TopicPublisher
	Dashboard        DashboardNotifier
	CapTokens        CapabilitySandboxIndexer
	RunLogs          *RunLogHub
	LifecycleLocks   *sandboxes.LifecycleLocks
	Removal          SandboxRemoval
	Completion       *CompletionManager
}

type SandboxRemoval interface {
	Remove(context.Context, string, bool) (sandboxes.RemovalResult, error)
}

func NewController(deps ControllerDependencies) *Controller {
	return &Controller{
		config:           deps.Config,
		store:            deps.Store,
		configDB:         deps.ConfigDB,
		workspaceEnsurer: deps.WorkspaceEnsurer,
		driver:           deps.Driver,
		executor:         deps.Executor,
		runtime:          deps.Runtime,
		images:           deps.Images,
		schedulerEngine:  deps.SchedulerEngine,
		cap:              deps.Cap,
		volumes:          deps.Volumes,
		streams:          deps.Streams,
		bus:              deps.Bus,
		dashboard:        deps.Dashboard,
		capTokens:        deps.CapTokens,
		runLogs:          deps.RunLogs,
		lifecycleLocks:   deps.LifecycleLocks,
		removal:          deps.Removal,
		completion:       deps.Completion,
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
	coordinator := NewCoordinator(c.configDB, domain.StableProjectRunID)
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
			return c.executeStartedProjectRun(execCtx, coordinator, run, req, warnings, stream)
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
	return c.RunProjectCommandAttachRegistered(ctx, receive, send, nil)
}

func (c *Controller) RunProjectCommandAttachRegistered(ctx context.Context, receive RunAttachReceiver, send RunAttachSender, onStarted func(string)) error {
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
	if onStarted != nil {
		onStarted(started.Run.RunID)
	}
	run, execErr, err := c.executeStartedProjectRunAttach(ctx, started.Run, req, started.Warnings, first, mode, receive, send)
	if err != nil {
		return err
	}
	if execErr != nil {
		return nil
	}
	_ = run
	return nil
}

func (c *Controller) executeStartedProjectRun(ctx context.Context, coordinator *Coordinator, run domain.ProjectRunRecord, req RunAgentRequest, warnings []string, stream *StreamSink) (domain.ProjectRunRecord, error, error) {
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
	if commandText != "" {
		transition, execErr := c.executeProjectRunCommand(ctx, run, sandboxResult.Sandbox, req, commandText, stream)
		if execErr != nil || transition.ExitCode != 0 {
			run, err = c.completeProjectRunError(transitionCtx, ctx, transition, execErr)
			if err != nil {
				return domain.ProjectRunRecord{}, nil, err
			}
			run = withRunWarnings(run, warnings)
			return run, execErr, nil
		}
		transition.Status = domain.ProjectRunStatusSucceeded
		run, err = c.completeProjectRun(transitionCtx, transition)
		if err != nil {
			return domain.ProjectRunRecord{}, nil, err
		}
		run = withRunWarnings(run, warnings)
		return run, nil, nil
	}
	agentConfig, err := c.projectRunAgentConfig(ctx, run)
	if err != nil {
		run, markErr := c.completeProjectRun(transitionCtx, TransitionRequest{
			RunID:     run.RunID,
			Status:    domain.ProjectRunStatusFailed,
			SandboxID: sandboxResult.Sandbox.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	if c.executor == nil {
		err = fmt.Errorf("executor is required")
		run, markErr := c.completeProjectRun(transitionCtx, TransitionRequest{
			RunID:     run.RunID,
			Status:    domain.ProjectRunStatusFailed,
			SandboxID: sandboxResult.Sandbox.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	cell, _, assistantEvent, execErr := c.executor.ExecuteAgentRequest(ctx, sandboxResult.Sandbox, execution.ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: run.AgentID,
		Model:             agentConfig.Model,
		RunID:             run.RunID,
		Message:           req.Prompt,
		OutputSchemaJSON:  req.OutputSchemaJSON,
		Stream:            projectRunAgentExecutionStream(transitionCtx, coordinator, run, sandboxResult.Sandbox, stream, c.runLogs),
	})
	transition := TransitionFromAgentCell(run, sandboxResult.Sandbox, cell, execErr)
	transition.TerminalEvents = projectAgentTerminalEvents(run, cell, assistantEvent, execErr)
	if execErr != nil || !cell.Success {
		run, err = c.completeProjectRunError(transitionCtx, ctx, transition, execErr)
		if err != nil {
			return domain.ProjectRunRecord{}, nil, err
		}
		run = withRunWarnings(run, warnings)
		return run, execErr, nil
	}
	transition.Status = domain.ProjectRunStatusSucceeded
	run, err = c.completeProjectRun(transitionCtx, transition)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	run = withRunWarnings(run, warnings)
	return run, nil, nil
}

func withRunWarnings(run domain.ProjectRunRecord, warnings []string) domain.ProjectRunRecord {
	run.Warnings = append([]string(nil), warnings...)
	return run
}

func markProjectRunTerminalError(ctx context.Context, coordinator *Coordinator, transition TransitionRequest, err error) (domain.ProjectRunRecord, error) {
	if errors.Is(err, context.Canceled) {
		return coordinator.MarkCanceled(ctx, transition)
	}
	return coordinator.MarkFailed(ctx, transition)
}

func (c *Controller) completeProjectRunError(ctx, executionCtx context.Context, transition TransitionRequest, err error) (domain.ProjectRunRecord, error) {
	transition.Status = domain.ProjectRunStatusFailed
	if errors.Is(err, context.Canceled) {
		transition.Status = domain.ProjectRunStatusCanceled
		if cause := context.Cause(executionCtx); cause != nil {
			transition.Error = cause.Error()
		}
	}
	return c.completeProjectRun(ctx, transition)
}

func (c *Controller) completeProjectRun(ctx context.Context, transition TransitionRequest) (domain.ProjectRunRecord, error) {
	manager, err := c.completionManager()
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if manager == nil {
		return c.completeProjectRunWithoutJournal(ctx, transition)
	}
	return manager.Complete(ctx, transition)
}

func (c *Controller) completeProjectRunWithoutJournal(ctx context.Context, transition TransitionRequest) (domain.ProjectRunRecord, error) {
	current, err := c.configDB.GetProjectRun(ctx, transition.RunID)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	action := CompletionCleanupAction(current.CleanupPolicy, current.SandboxID != "", current.SandboxCreated)
	if action != domain.ProjectRunCompletionActionNone {
		sandbox, loadErr := c.store.GetSandbox(ctx, current.SandboxID)
		if loadErr != nil && !completionSandboxMissing(loadErr) {
			return current, loadErr
		}
		if sandbox != nil {
			policy := agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION
			if action == domain.ProjectRunCompletionActionRemove {
				policy = agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION
			}
			if err := c.cleanupProjectRunSandboxByPolicy(ctx, SandboxResult{Sandbox: sandbox, Created: current.SandboxCreated}, policy); err != nil {
				current.Status = domain.ProjectRunStatusRunning
				current.CleanupError = err.Error()
				applyProjectRunTransitionFields(&current, transition)
				return c.configDB.UpdateProjectRun(ctx, current)
			}
		}
	}
	return NewCoordinator(c.configDB, domain.StableProjectRunID).TransitionRun(ctx, transition)
}

type controllerCompletionStopper struct{ controller *Controller }

func (s controllerCompletionStopper) Stop(ctx context.Context, sandbox *domain.Sandbox) error {
	return s.controller.stopProjectRunSandbox(ctx, sandbox)
}

type controllerCompletionRemoval struct{ controller *Controller }

func (r controllerCompletionRemoval) Remove(ctx context.Context, sandboxID string, _ bool) (sandboxes.RemovalResult, error) {
	sandbox, err := r.controller.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return sandboxes.RemovalResult{}, err
	}
	if err := r.controller.cleanupProjectRunSandboxByPolicy(ctx, SandboxResult{Sandbox: sandbox, Created: true}, agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION); err != nil {
		return sandboxes.RemovalResult{SandboxID: sandboxID}, err
	}
	return sandboxes.RemovalResult{SandboxID: sandboxID, Stopped: true, Removed: true}, nil
}

func (c *Controller) completionManager() (*CompletionManager, error) {
	if c.completion != nil {
		return c.completion, nil
	}
	store, ok := c.configDB.(CompletionStore)
	if !ok {
		return nil, nil
	}
	removal := c.removal
	if removal == nil {
		removal = controllerCompletionRemoval{controller: c}
	}
	c.completion = NewCompletionManager(store, c.store, controllerCompletionStopper{controller: c}, removal, slog.Default())
	return c.completion, nil
}

func (c *Controller) executeProjectRunCommand(ctx context.Context, run domain.ProjectRunRecord, sandbox *domain.Sandbox, req RunAgentRequest, commandText string, sink *StreamSink) (TransitionRequest, error) {
	artifactsDir := projectRunCommandArtifactsDir(run, sandbox)
	logsPath := filepath.Join(artifactsDir, "transcript.txt")
	transition := TransitionRequest{
		RunID:     run.RunID,
		SandboxID: sandbox.Summary.ID,
		LogsPath:  logsPath,
	}
	if c.store == nil || c.runtime == nil {
		err := fmt.Errorf("command runtime dependencies are required")
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	if sink != nil && sink.SendStarted != nil {
		if err := sink.SendStarted(run, time.Now().UTC()); err != nil {
			transition.ExitCode = 1
			transition.Error = fmt.Sprintf("command execution failed: %v", err)
			return transition, err
		}
	}
	appconfig.ApplyDefaultGuestPaths(c.config)
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	runtime, err := c.runtime(sandbox)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	guestArtifactsDir := filepath.Join(c.config.GuestStateRoot, "runs", run.RunID)
	runtimeRequest := execution.RuntimeCommandRequestPayloadFromCommand(
		c.config,
		"shell",
		"",
		nil,
		commandText,
		c.config.GuestWorkspacePath,
		execEnvMap(req.Env),
		0,
		0,
		guestArtifactsDir,
	)
	if err := execution.WriteJSONArtifact(filepath.Join(artifactsDir, "command-request.json"), runtimeRequest); err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
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
		if sink != nil && sink.SendChunk != nil {
			sendErr = sink.SendChunk(run.RunID, filtered, time.Now().UTC())
		}
	}
	execCtx, cancel := execution.ExecContext(ctx, 0)
	defer cancel()
	result, execErr := runtime.ExecStream(execCtx, sandbox, vmState, execution.BuildRuntimeCommandExecSpec(c.config, sandbox, filepath.Join(guestArtifactsDir, "command-request.json"), c.config.GuestHomePath), writer)
	if sendErr != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", sendErr)
		return transition, sendErr
	}
	if execErr != nil {
		result.ExitCode = execution.FirstNonZeroInt(result.ExitCode, 1)
		result.Success = false
		if strings.TrimSpace(result.Output) == "" {
			result.Output = firstNonEmpty(result.Stderr, result.Stdout, execErr.Error())
		}
		transition = transitionFromCommandResult(run, sandbox, commandText, result, execErr)
		transition.LogsPath = logsPath
		return transition, execErr
	}
	commandResult, err := execution.ParseCommandExecResult(result)
	if err != nil {
		transition.ExitCode = execution.FirstNonZeroInt(transition.ExitCode, 1)
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	if err := execution.MirrorRuntimeCommandArtifacts(artifactsDir, commandResult); err != nil {
		transition.ExitCode = execution.FirstNonZeroInt(commandResult.ExitCode, 1)
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	transition = transitionFromCommandResult(run, sandbox, commandText, execution.RuntimeCommandResultToExecResult(commandResult), nil)
	transition.LogsPath = logsPath
	return transition, nil
}

func (c *Controller) executeStartedProjectRunAttach(ctx context.Context, run domain.ProjectRunRecord, req RunAgentRequest, warnings []string, start RunAttachInput, mode RunAttachMode, receive RunAttachReceiver, send RunAttachSender) (domain.ProjectRunRecord, error, error) {
	coordinator := NewCoordinator(c.configDB, domain.StableProjectRunID)
	commandText := strings.TrimSpace(req.Command)
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
	warnings = append(warnings, sandboxResult.Warnings...)
	run, err = coordinator.MarkRunning(transitionCtx, run.RunID, sandboxResult.Sandbox.Summary.ID)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	run = withRunWarnings(run, warnings)
	var transition TransitionRequest
	var execErr error
	switch mode {
	case RunAttachModePrompt:
		transition, execErr = c.runPromptInteraction(ctx, coordinator, run, sandboxResult.Sandbox, req, start, receive, send)
	default:
		transition, execErr = c.runCommandInteraction(ctx, coordinator, run, sandboxResult.Sandbox, req, commandText, start, receive, send)
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

func (c *Controller) runCommandInteraction(ctx context.Context, coordinator *Coordinator, run domain.ProjectRunRecord, sandbox *domain.Sandbox, req RunAgentRequest, commandText string, start RunAttachInput, receive RunAttachReceiver, send RunAttachSender) (TransitionRequest, error) {
	artifactsDir := projectRunCommandArtifactsDir(run, sandbox)
	logsPath := filepath.Join(artifactsDir, "transcript.txt")
	transition := TransitionRequest{RunID: run.RunID, SandboxID: sandbox.Summary.ID, LogsPath: logsPath}
	if c.store == nil || c.runtime == nil {
		err := fmt.Errorf("command runtime dependencies are required")
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	appconfig.ApplyDefaultGuestPaths(c.config)
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	runtime, err := c.runtime(sandbox)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	interactionRuntime, ok := runtime.(InteractionRuntime)
	if !ok {
		err := fmt.Errorf("%w: command attach is unsupported by this runtime driver", domain.ErrUnsupported)
		transition.ExitCode = 1
		transition.Error = err.Error()
		return transition, err
	}
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	run, err = markProjectRunInteractionArtifacts(ctx, coordinator, run, sandbox, logsPath, artifactsDir)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
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
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	interaction = driverpkg.GuardRuntimeInteractionInput(interaction)
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
				return transitionFromRuntimeResult(run, sandbox, commandText, logsPath, accumulator.Result(result.ExitCode, result.Success), result, nil), nil
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
			return transitionFromRuntimeResult(run, sandbox, commandText, logsPath, accumulator.Result(result.ExitCode, result.Success), *result, errorFromRuntimeResult(*result)), nil
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

func markProjectRunInteractionArtifacts(ctx context.Context, coordinator *Coordinator, run domain.ProjectRunRecord, sandbox *domain.Sandbox, logsPath, artifactsDir string) (domain.ProjectRunRecord, error) {
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

func (c *Controller) runPromptInteraction(ctx context.Context, coordinator *Coordinator, run domain.ProjectRunRecord, sandbox *domain.Sandbox, req RunAgentRequest, _ RunAttachInput, receive RunAttachReceiver, send RunAttachSender) (transitionResult TransitionRequest, returnErr error) {
	artifactsDir := projectRunCommandArtifactsDir(run, sandbox)
	logsPath := filepath.Join(artifactsDir, "transcript.txt")
	transition := TransitionRequest{RunID: run.RunID, SandboxID: sandbox.Summary.ID, LogsPath: logsPath}
	if c.store == nil || c.runtime == nil {
		err := fmt.Errorf("prompt runtime dependencies are required")
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	appconfig.ApplyDefaultGuestPaths(c.config)
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	runtime, err := c.runtime(sandbox)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	interactionRuntime, ok := runtime.(InteractionRuntime)
	if !ok {
		err := fmt.Errorf("%w: prompt attach is unsupported by this runtime driver", domain.ErrUnsupported)
		transition.ExitCode = 1
		transition.Error = err.Error()
		return transition, err
	}
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	run, err = markProjectRunInteractionArtifacts(ctx, coordinator, run, sandbox, logsPath, artifactsDir)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	agentConfig, err := c.projectRunAgentConfig(ctx, run)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	if agentConfig.Provider != "codex" && agentConfig.Provider != "claude" && agentConfig.Provider != "opencode" && agentConfig.Provider != "pi" {
		err := fmt.Errorf("%w: prompt attach currently supports codex, claude, opencode, and pi providers only", domain.ErrUnsupported)
		transition.ExitCode = 1
		transition.Error = err.Error()
		return transition, err
	}
	systemPrompt, err := c.projectRunAgentSystemPrompt(ctx, run)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	if err := execution.WriteAgentSystemPromptFile(sandbox, systemPrompt); err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	schemaPath, err := execution.WriteAgentOutputSchemaFile(c.config, sandbox, agentConfig.Provider, req.OutputSchemaJSON)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	env := execution.BuildSandboxExecEnv(c.config, sandbox, c.config.GuestHomePath)
	managedEnv, err := c.ensurePromptAttachLLMFacadeEnv(ctx, sandbox, agentConfig, run.RunID)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	if len(managedEnv) > 0 {
		env = llms.MergeManagedExecEnv(env, managedEnv)
		if token := managedEnv["AGENT_COMPOSE_SANDBOX_TOKEN"]; token != "" {
			defer func() {
				if !errors.Is(returnErr, domain.ErrExecTerminationUnconfirmed) && !errors.Is(returnErr, driverpkg.ErrExecTerminationUnconfirmed) {
					c.deletePromptAttachLLMFacadeToken(context.WithoutCancel(ctx), token)
				}
			}()
		}
	}
	command := strings.Join([]string{
		"set -e",
		"cd " + execution.ShellQuote(c.config.GuestWorkspacePath),
		"mkdir -p " + execution.ShellQuote(c.config.GuestHomePath),
		"agent-compose-runtime stream",
	}, " && ")
	spec := driverpkg.RuntimeStartSpec{
		OperationID: run.RunID,
		Kind:        driverpkg.RuntimeOperationCommand,
		Origin:      "run_prompt_attach",
		Command: &driverpkg.RuntimeCommandSpec{
			Command: "sh",
			Args:    []string{"-lc", command},
			Env:     env,
			Cwd:     c.config.GuestWorkspacePath,
		},
		Cwd:         c.config.GuestWorkspacePath,
		Env:         env,
		AttachStdin: true,
		TTY:         false,
	}
	interaction, err := interactionRuntime.OpenInteraction(ctx, sandbox, vmState, spec)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	interaction = driverpkg.GuardRuntimeInteractionInput(interaction)
	defer func() { _ = interaction.CloseSend() }()
	projector := newPersistentPromptAttachProjector(context.WithoutCancel(ctx), run, sandbox, logsPath, c.runLogs, c.configDB)
	input := &promptWrapperInput{interaction: interaction}
	if err := input.Start(agentConfig, c.config, schemaPath); err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	inputCtx, cancelInput := context.WithCancel(ctx)
	defer cancelInput()
	turnReady := make(chan struct{}, 1)
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		if err := input.HumanMessage(prompt); err != nil {
			transition.ExitCode = 1
			transition.Error = fmt.Sprintf("agent execution failed: %v", err)
			return transition, err
		}
	} else {
		releasePromptTurn(turnReady)
	}
	go pumpRunPromptAttachInput(inputCtx, receive, input, turnReady, projector.AppendHumanMessageFrame)
	var promptTransition *TransitionRequest
	for {
		frame, err := interaction.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				result, waitErr := interaction.Wait()
				if waitErr != nil {
					transition.ExitCode = 1
					transition.Error = fmt.Sprintf("agent execution failed: %v", waitErr)
					return transition, waitErr
				}
				if promptTransition != nil {
					if result.ExitCode != 0 || !result.Success {
						promptTransition.ExitCode = execution.FirstNonZeroInt(result.ExitCode, promptTransition.ExitCode)
						promptTransition.Error = firstNonEmpty(promptTransition.Error, result.Error, "agent execution failed")
						return *promptTransition, promptWrapperTransitionError(*promptTransition)
					}
					if promptTransition.ExitCode != 0 || strings.TrimSpace(promptTransition.Error) != "" {
						return *promptTransition, promptWrapperTransitionError(*promptTransition)
					}
					return *promptTransition, nil
				}
				return transitionFromPromptRuntimeResult(run, sandbox, logsPath, result, errorFromRuntimeResult(result)), errorFromRuntimeResult(result)
			}
			transition.ExitCode = 1
			transition.Error = fmt.Sprintf("agent execution failed: %v", err)
			_ = send(runAttachErrorResponse("runtime_recv_error", err.Error(), true))
			return transition, err
		}
		switch frame.Type {
		case driverpkg.RuntimeOutputStarted:
			if err := send(runAttachStartedResponse(run, sandbox, warningsFromRun(run), frame.StartedAt)); err != nil {
				transition.ExitCode = 1
				transition.Error = fmt.Sprintf("agent execution failed: %v", err)
				return transition, err
			}
		case driverpkg.RuntimeOutputStdout:
			responses, nextTransition, err := projector.Project(frame.Data)
			if err != nil {
				_ = send(runAttachErrorResponse("runtime_stream_decode_error", err.Error(), true))
				transition.ExitCode = 1
				transition.Error = fmt.Sprintf("agent execution failed: %v", err)
				return transition, err
			}
			for _, resp := range responses {
				if err := send(resp); err != nil {
					transition.ExitCode = 1
					transition.Error = fmt.Sprintf("agent execution failed: %v", err)
					return transition, err
				}
				if resp.Kind == RunAttachOutputAgentTurnCompleted {
					releasePromptTurn(turnReady)
				}
			}
			if nextTransition != nil {
				promptTransition = nextTransition
			}
		case driverpkg.RuntimeOutputStderr:
			if err := projector.AppendStderr(string(frame.Data)); err != nil {
				transition.ExitCode = 1
				transition.Error = fmt.Sprintf("agent execution failed: %v", err)
				return transition, err
			}
			if err := send(runAttachOutputResponse(frame.Data, domain.StdioStderr, false)); err != nil {
				transition.ExitCode = 1
				transition.Error = fmt.Sprintf("agent execution failed: %v", err)
				return transition, err
			}
		case driverpkg.RuntimeOutputResult:
			result := frame.Result
			if result == nil {
				result = &driverpkg.RuntimeResult{OperationID: run.RunID, Success: true}
			}
			if promptTransition != nil {
				if result.ExitCode != 0 || !result.Success {
					promptTransition.ExitCode = execution.FirstNonZeroInt(result.ExitCode, promptTransition.ExitCode)
					promptTransition.Error = firstNonEmpty(promptTransition.Error, result.Error, "agent execution failed")
					return *promptTransition, promptWrapperTransitionError(*promptTransition)
				}
				if promptTransition.ExitCode != 0 || strings.TrimSpace(promptTransition.Error) != "" {
					return *promptTransition, promptWrapperTransitionError(*promptTransition)
				}
				return *promptTransition, nil
			}
			return transitionFromPromptRuntimeResult(run, sandbox, logsPath, *result, errorFromRuntimeResult(*result)), errorFromRuntimeResult(*result)
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

func (c *Controller) projectRunAgentConfig(ctx context.Context, run domain.ProjectRunRecord) (execution.AgentConfig, error) {
	agent, err := c.projectRunAgentDefinition(ctx, run)
	if err != nil {
		return execution.AgentConfig{}, err
	}
	config := execution.AgentConfigFromDefinition(agent, domain.DefaultAgentProvider)
	if config.Provider == "" {
		config.Provider = domain.DefaultAgentProvider
	}
	return config, nil
}

func (c *Controller) projectRunAgentSystemPrompt(ctx context.Context, run domain.ProjectRunRecord) (string, error) {
	if c == nil || c.configDB == nil || strings.TrimSpace(run.AgentID) == "" {
		return "", nil
	}
	agent, err := c.projectRunAgentDefinition(ctx, run)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(agent.SystemPrompt), nil
}

func (c *Controller) projectRunAgentDefinition(ctx context.Context, run domain.ProjectRunRecord) (domain.AgentDefinition, error) {
	project, err := c.configDB.GetProject(ctx, run.ProjectID)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("resolve project %s: %w", run.ProjectID, err)
	}
	revision, err := c.configDB.GetProjectRevision(ctx, run.ProjectID, run.ProjectRevision)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("resolve project revision %s/%d: %w", run.ProjectID, run.ProjectRevision, err)
	}
	agent, err := projects.AgentDefinitionFromRevision(project, revision, run.AgentName)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("resolve revision agent %s: %w", run.AgentID, err)
	}
	return agent, nil
}

func (c *Controller) ensurePromptAttachLLMFacadeEnv(ctx context.Context, sandbox *domain.Sandbox, agent execution.AgentConfig, runID string) (map[string]string, error) {
	store, ok := c.configDB.(llmFacadeStore)
	if !ok || c.config == nil || sandbox == nil {
		return nil, nil
	}
	switch domain.NormalizeAgentKind(agent.Provider) {
	case "claude":
		return ensurePromptAttachClaudeLLMFacadeEnv(ctx, c.config, store, sandbox, agent.Model, runID)
	case "opencode":
		return llms.EnsureOpenCodeFacadeConfig(ctx, c.config, store, sandbox, agent.Model, "agent", runID)
	case "pi":
		return llms.EnsurePiFacadeConfig(ctx, c.config, store, sandbox, agent.Model, "agent", runID)
	case "codex":
		return llms.EnsureCodexFacadeConfig(ctx, c.config, store, sandbox, agent.Model, "agent", runID)
	default:
		return nil, nil
	}
}

func (c *Controller) deletePromptAttachLLMFacadeToken(ctx context.Context, token string) {
	store, ok := c.configDB.(llmFacadeTokenDeleter)
	if !ok || strings.TrimSpace(token) == "" {
		return
	}
	_ = store.DeleteLLMFacadeToken(ctx, token)
}

func projectRunAgentExecutionStream(ctx context.Context, coordinator *Coordinator, run domain.ProjectRunRecord, sandbox *domain.Sandbox, sink *StreamSink, hub *RunLogHub) execution.AgentExecutionStream {
	return execution.AgentExecutionStream{
		OnStart: func(cell domain.NotebookCell) error {
			if coordinator != nil {
				logsPath := projectRunAgentCellOutputPath(sandbox, cell.ID)
				if strings.TrimSpace(logsPath) != "" {
					if _, err := coordinator.TransitionRun(ctx, TransitionRequest{
						RunID:    run.RunID,
						Status:   domain.ProjectRunStatusRunning,
						LogsPath: logsPath,
					}); err != nil {
						return err
					}
				}
			}
			if sink == nil || sink.SendStarted == nil {
				return nil
			}
			return sink.SendStarted(run, time.Now().UTC())
		},
		OnChunk: func(cellID string, chunk domain.ExecChunk) error {
			offset, err := appendProjectRunLogChunk(projectRunAgentCellOutputPath(sandbox, cellID), chunk)
			if err != nil {
				return err
			}
			publishRunLogChunk(hub, run.RunID, chunk, offset)
			if sink == nil || sink.SendChunk == nil {
				return nil
			}
			return sink.SendChunk(run.RunID, chunk, time.Now().UTC())
		},
	}
}

func transitionFromCommandResult(run domain.ProjectRunRecord, sandbox *domain.Sandbox, commandText string, result domain.ExecResult, execErr error) TransitionRequest {
	artifactsDir := projectRunCommandArtifactsDir(run, sandbox)
	req := TransitionRequest{
		RunID:        run.RunID,
		SandboxID:    sandbox.Summary.ID,
		ExitCode:     result.ExitCode,
		Output:       result.Output,
		ArtifactsDir: artifactsDir,
		LogsPath:     filepath.Join(artifactsDir, "output.txt"),
	}
	resultJSON, err := json.Marshal(map[string]any{
		"mode":     "command",
		"command":  commandText,
		"success":  result.Success,
		"exitCode": result.ExitCode,
	})
	if err == nil {
		req.ResultJSON = string(resultJSON)
	}
	if execErr != nil {
		req.ExitCode = execution.FirstNonZeroInt(req.ExitCode, 1)
		req.Error = fmt.Sprintf("command execution failed: %v", execErr)
		return req
	}
	if !result.Success {
		req.ExitCode = execution.FirstNonZeroInt(req.ExitCode, 1)
		req.Error = "command execution failed"
		if detail := firstNonEmpty(result.Stderr, result.Output, result.Stdout); strings.TrimSpace(detail) != "" {
			req.Error += ": " + strings.TrimSpace(detail)
		}
	}
	return req
}

func transitionFromRuntimeResult(run domain.ProjectRunRecord, sandbox *domain.Sandbox, commandText, logsPath string, accumulated domain.ExecResult, result driverpkg.RuntimeResult, execErr error) TransitionRequest {
	accumulated.ExitCode = result.ExitCode
	accumulated.Success = result.Success
	if strings.TrimSpace(result.Error) != "" {
		accumulated.Success = false
	}
	if execErr == nil && strings.TrimSpace(result.Error) != "" {
		execErr = errors.New(result.Error)
	}
	transition := transitionFromCommandResult(run, sandbox, commandText, accumulated, execErr)
	transition.LogsPath = logsPath
	return transition
}

func transitionFromPromptWrapperResult(run domain.ProjectRunRecord, sandbox *domain.Sandbox, logsPath string, payload []byte, finalText, stopReason, message string) TransitionRequest {
	transition := TransitionRequest{
		RunID:      run.RunID,
		SandboxID:  sandbox.Summary.ID,
		Output:     finalText,
		ResultJSON: string(payload),
		LogsPath:   logsPath,
	}
	if strings.TrimSpace(message) != "" {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %s", strings.TrimSpace(message))
		return transition
	}
	if strings.EqualFold(strings.TrimSpace(stopReason), "cancelled") {
		transition.ExitCode = 1
		transition.Error = "agent execution cancelled"
	}
	return transition
}

func promptWrapperTransitionError(transition TransitionRequest) error {
	if strings.EqualFold(strings.TrimSpace(transition.Error), "agent execution cancelled") {
		return context.Canceled
	}
	return errors.New(firstNonEmpty(transition.Error, "agent execution failed"))
}

func transitionFromPromptRuntimeResult(run domain.ProjectRunRecord, sandbox *domain.Sandbox, logsPath string, result driverpkg.RuntimeResult, execErr error) TransitionRequest {
	transition := TransitionRequest{
		RunID:     run.RunID,
		SandboxID: sandbox.Summary.ID,
		LogsPath:  logsPath,
		ExitCode:  result.ExitCode,
		Error:     result.Error,
	}
	if execErr != nil {
		transition.ExitCode = execution.FirstNonZeroInt(transition.ExitCode, 1)
		transition.Error = fmt.Sprintf("agent execution failed: %v", execErr)
	}
	return transition
}

func errorFromRuntimeResult(result driverpkg.RuntimeResult) error {
	if strings.TrimSpace(result.Error) == "" {
		return nil
	}
	return errors.New(result.Error)
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

type promptWrapperInput struct {
	interaction driverpkg.RuntimeInteraction
	seq         int
}

func (w *promptWrapperInput) Start(agent execution.AgentConfig, config *appconfig.Config, schemaPath string) error {
	frame := map[string]any{
		"v":         1,
		"seq":       w.nextSeq(),
		"type":      "start",
		"provider":  agent.Provider,
		"stateRoot": config.GuestStateRoot,
		"workspace": config.GuestWorkspacePath,
		"home":      config.GuestHomePath,
	}
	if strings.TrimSpace(agent.Model) != "" {
		frame["model"] = strings.TrimSpace(agent.Model)
	}
	if strings.TrimSpace(schemaPath) != "" {
		frame["outputSchemaFile"] = strings.TrimSpace(schemaPath)
	}
	return w.send(frame)
}

func (w *promptWrapperInput) HumanMessage(message string) error {
	return w.send(map[string]any{
		"v":       1,
		"seq":     w.nextSeq(),
		"type":    "human_message",
		"message": message,
	})
}

func (w *promptWrapperInput) EOF() error {
	return w.send(map[string]any{"v": 1, "seq": w.nextSeq(), "type": "eof"})
}

func (w *promptWrapperInput) Cancel(reason string) error {
	return w.send(map[string]any{"v": 1, "seq": w.nextSeq(), "type": "cancel", "message": reason})
}

func (w *promptWrapperInput) nextSeq() int {
	seq := w.seq
	w.seq++
	return seq
}

func (w *promptWrapperInput) send(frame map[string]any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return w.interaction.Send(driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdin, Data: data})
}

func pumpRunPromptAttachInput(ctx context.Context, receive RunAttachReceiver, input *promptWrapperInput, turnReady <-chan struct{}, onHumanMessage func(string, string) error) {
	defer func() { _ = input.interaction.CloseSend() }()
	for {
		req, err := receive()
		if err != nil {
			_ = input.EOF()
			return
		}
		switch req.Kind {
		case RunAttachInputHumanMessage:
			if !forwardPromptHumanMessage(ctx, input, turnReady, req.Text, req.ClientFrameID, onHumanMessage) {
				return
			}
		case RunAttachInputStdin:
			if !forwardPromptHumanMessage(ctx, input, turnReady, string(req.Data), req.ClientFrameID, onHumanMessage) {
				return
			}
		case RunAttachInputStdinEOF:
			_ = input.EOF()
			return
		case RunAttachInputCancel:
			_ = input.Cancel(req.Reason)
			return
		default:
			_ = input.Cancel("invalid run prompt attach frame")
			return
		}
	}
}

func forwardPromptHumanMessage(ctx context.Context, input *promptWrapperInput, turnReady <-chan struct{}, text, clientFrameID string, onHumanMessage func(string, string) error) bool {
	if turnReady != nil {
		select {
		case <-ctx.Done():
			return false
		case <-turnReady:
		}
	}
	if onHumanMessage != nil {
		if err := onHumanMessage(text, clientFrameID); err != nil {
			return false
		}
	}
	return input.HumanMessage(text) == nil
}

func releasePromptTurn(turnReady chan<- struct{}) {
	select {
	case turnReady <- struct{}{}:
	default:
	}
}

type promptAttachProjector struct {
	run                    domain.ProjectRunRecord
	sandbox                *domain.Sandbox
	logsPath               string
	runLogs                *RunLogHub
	mu                     sync.Mutex
	buffer                 []byte
	itemTexts              map[string]string
	loggedText             string
	turnText               string
	hasLoggedText          bool
	logEndsWithNewline     bool
	persistedAssistantTurn bool
	eventCtx               context.Context
	events                 structuredEventStore
	humanIndex             uint64
}

func newPersistentPromptAttachProjector(ctx context.Context, run domain.ProjectRunRecord, sandbox *domain.Sandbox, logsPath string, hub *RunLogHub, store any) *promptAttachProjector {
	projector := newPromptAttachProjector(run, sandbox, logsPath, hub)
	projector.eventCtx = ctx
	projector.events, _ = store.(structuredEventStore)
	return projector
}

func newPromptAttachProjector(run domain.ProjectRunRecord, sandbox *domain.Sandbox, logsPath string, hub *RunLogHub) *promptAttachProjector {
	return &promptAttachProjector{
		run:       run,
		sandbox:   sandbox,
		logsPath:  logsPath,
		runLogs:   hub,
		itemTexts: map[string]string{},
	}
}

func (p *promptAttachProjector) Project(data []byte) ([]RunAttachOutput, *TransitionRequest, error) {
	p.buffer = append(p.buffer, data...)
	lines := make([][]byte, 0)
	for {
		index := bytesIndexByte(p.buffer, '\n')
		if index < 0 {
			break
		}
		line := append([]byte(nil), p.buffer[:index]...)
		p.buffer = append([]byte(nil), p.buffer[index+1:]...)
		if strings.TrimSpace(string(line)) != "" {
			lines = append(lines, line)
		}
	}
	var responses []RunAttachOutput
	var transition *TransitionRequest
	for _, line := range lines {
		nextResponses, nextTransition, err := p.projectLine(line)
		if err != nil {
			return responses, transition, err
		}
		responses = append(responses, nextResponses...)
		if nextTransition != nil {
			transition = nextTransition
		}
	}
	return responses, transition, nil
}

func (p *promptAttachProjector) projectLine(line []byte) ([]RunAttachOutput, *TransitionRequest, error) {
	var frame struct {
		Type            string                      `json:"type"`
		Event           json.RawMessage             `json:"event"`
		FinalText       string                      `json:"finalText"`
		FinalTextSource domain.AgentFinalTextSource `json:"finalTextSource"`
		SandboxID       string                      `json:"sandboxId"`
		StopReason      string                      `json:"stopReason"`
		Code            string                      `json:"code"`
		Message         string                      `json:"message"`
		Provider        string                      `json:"provider"`
		Seq             uint64                      `json:"seq"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		return nil, nil, err
	}
	switch frame.Type {
	case "started":
		return []RunAttachOutput{runAttachAgentEventResponse("started", "", string(line))}, nil, nil
	case "agent_event":
		name, text := p.agentEventText(frame.Event)
		if err := p.appendLogText(text); err != nil {
			return nil, nil, err
		}
		return []RunAttachOutput{runAttachAgentEventResponse(firstNonEmpty(name, "agent_event"), text, string(frame.Event))}, nil, nil
	case "agent_turn_completed":
		if err := p.appendLogFinalText(frame.FinalText); err != nil {
			return nil, nil, err
		}
		if err := p.appendAssistantEvent(line, frame.Seq, agentTurnProjection{FinalText: frame.FinalText, FinalTextSource: frame.FinalTextSource, Provider: frame.Provider, StopReason: frame.StopReason}); err != nil {
			return nil, nil, err
		}
		return []RunAttachOutput{runAttachAgentTurnCompletedResponse(p.run, string(line), warningsFromRun(p.run))}, nil, nil
	case "result":
		if err := p.appendLogFinalText(frame.FinalText); err != nil {
			return nil, nil, err
		}
		transition := transitionFromPromptWrapperResult(p.run, p.sandbox, p.logsPath, line, frame.FinalText, frame.StopReason, "")
		transition.TerminalEvents = p.terminalTurnEvents(agentTurnProjection{FinalText: frame.FinalText, FinalTextSource: frame.FinalTextSource, Provider: frame.Provider, StopReason: frame.StopReason})
		return nil, &transition, nil
	case "error":
		message := firstNonEmpty(frame.Message, "runtime stream error")
		transition := transitionFromPromptWrapperResult(p.run, p.sandbox, p.logsPath, line, "", frame.StopReason, message)
		return []RunAttachOutput{runAttachErrorResponse(firstNonEmpty(frame.Code, "runtime_stream_error"), message, true)}, &transition, nil
	default:
		return []RunAttachOutput{runAttachAgentEventResponse(firstNonEmpty(frame.Type, "agent_event"), "", string(line))}, nil, nil
	}
}

func (p *promptAttachProjector) agentEventText(raw json.RawMessage) (string, string) {
	var event struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Item *struct {
			ID               string `json:"id"`
			Type             string `json:"type"`
			Text             string `json:"text"`
			AggregatedOutput string `json:"aggregated_output"`
			Command          string `json:"command"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return "agent_event", ""
	}
	name := firstNonEmpty(event.Type, "agent_event")
	if event.Text != "" {
		return name, event.Text
	}
	if event.Item == nil {
		return name, ""
	}
	key := firstNonEmpty(event.Item.ID, name)
	var text string
	switch event.Item.Type {
	case "agent_message", "reasoning":
		text = event.Item.Text
	case "command_execution":
		if event.Item.Command != "" {
			commandKey := key + ":command"
			if p.itemTexts[commandKey] == "" {
				p.itemTexts[commandKey] = event.Item.Command
				text += "\n$ " + event.Item.Command + "\n"
			}
		}
		text += event.Item.AggregatedOutput
	default:
		return name, ""
	}
	if text == "" {
		return name, ""
	}
	previous := p.itemTexts[key]
	p.itemTexts[key] = text
	if strings.HasPrefix(text, previous) {
		return name, text[len(previous):]
	}
	return name, text
}

func (p *promptAttachProjector) appendLogText(text string) error {
	if text == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.appendLogChunkLocked(domain.ExecChunk{Text: text}); err != nil {
		return err
	}
	p.loggedText += text
	p.turnText += text
	return nil
}

func (p *promptAttachProjector) appendLogFinalText(finalText string) error {
	if finalText == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if strings.HasPrefix(finalText, p.loggedText) {
		text := finalText[len(p.loggedText):]
		if text == "" {
			return nil
		}
		if err := p.appendLogChunkLocked(domain.ExecChunk{Text: text}); err != nil {
			return err
		}
		p.loggedText += text
		p.turnText += text
		return nil
	}
	if p.loggedText == "" {
		if err := p.appendLogChunkLocked(domain.ExecChunk{Text: finalText}); err != nil {
			return err
		}
		p.loggedText = finalText
		p.turnText += finalText
	}
	return nil
}

func (p *promptAttachProjector) AppendHumanMessage(message string) error {
	return p.AppendHumanMessageFrame(message, "")
}

func (p *promptAttachProjector) AppendHumanMessageFrame(message, clientFrameID string) error {
	text := promptAttachHumanLogText(message)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.persistedAssistantTurn = false
	if text != "" {
		if p.hasLoggedText && !p.logEndsWithNewline {
			text = "\n" + text
		}
		if err := p.appendLogChunkLocked(domain.ExecChunk{Text: text}); err != nil {
			return err
		}
	}
	if p.events == nil || strings.TrimSpace(message) == "" {
		return nil
	}
	p.humanIndex++
	_, _, err := p.events.AppendProjectRunEvent(p.eventContext(), domain.ProjectRunEventRecord{
		ID: attachedHumanEventID(p.run.RunID, clientFrameID, uint64(p.humanIndex), message), RunID: p.run.RunID, Kind: domain.ProjectRunEventKindUserMessage, Text: message, Agent: p.run.AgentName,
	})
	return err
}

func (p *promptAttachProjector) AppendStderr(text string) error {
	if text == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.appendLogChunkLocked(domain.ExecChunk{Text: text, Stream: domain.StdioStderr}); err != nil {
		return err
	}
	p.turnText += text
	return nil
}

func (p *promptAttachProjector) appendAssistantEvent(line []byte, seq uint64, turn agentTurnProjection) error {
	p.mu.Lock()
	store := p.events
	turn.Transcript = p.turnText
	p.mu.Unlock()
	if store == nil {
		return nil
	}
	events := attachedAgentTurnEvents(p.run, seq, line, turn)
	if len(events) > 0 {
		if _, _, err := store.AppendProjectRunEvents(p.eventContext(), events); err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turnText = ""
	p.persistedAssistantTurn = len(events) > 0
	return nil
}

func (p *promptAttachProjector) terminalTurnEvents(turn agentTurnProjection) []domain.ProjectRunEventRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.persistedAssistantTurn {
		return nil
	}
	turn.Transcript = p.turnText
	events := terminalPromptTurnEvents(p.run, turn)
	p.turnText = ""
	return events
}

func (p *promptAttachProjector) eventContext() context.Context {
	if p.eventCtx != nil {
		return p.eventCtx
	}
	return context.Background()
}

func (p *promptAttachProjector) appendLogChunkLocked(chunk domain.ExecChunk) error {
	offset, err := appendProjectRunLogChunk(p.logsPath, chunk)
	if err != nil {
		return err
	}
	if chunk.Text != "" {
		p.hasLoggedText = true
		p.logEndsWithNewline = strings.HasSuffix(chunk.Text, "\n")
	}
	publishRunLogChunk(p.runLogs, p.run.RunID, chunk, offset)
	return nil
}

func promptAttachHumanLogText(message string) string {
	if strings.TrimSpace(message) == "" {
		return ""
	}
	if strings.HasSuffix(message, "\n") {
		return message
	}
	return message + "\n"
}

func bytesIndexByte(data []byte, needle byte) int {
	for i, value := range data {
		if value == needle {
			return i
		}
	}
	return -1
}

func runAttachStartedResponse(run domain.ProjectRunRecord, sandbox *domain.Sandbox, warnings []string, startedAt time.Time) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputStarted, CreatedAt: startedAt, Run: run, SandboxID: sandbox.Summary.ID, Warnings: append([]string(nil), warnings...)}
}

func runAttachOutputResponse(data []byte, stream domain.StdioStream, tty bool) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputData, CreatedAt: time.Now().UTC(), Data: append([]byte(nil), data...), Stream: stream, TTY: tty}
}

func runAttachAgentEventResponse(name, text, payloadJSON string) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputAgentEvent, CreatedAt: time.Now().UTC(), Name: name, Text: text, PayloadJSON: payloadJSON}
}

func runAttachAgentTurnCompletedResponse(run domain.ProjectRunRecord, resultJSON string, warnings []string) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputAgentTurnCompleted, CreatedAt: time.Now().UTC(), Run: run, ResultJSON: resultJSON, Warnings: append([]string(nil), warnings...)}
}

func runAttachResultResponse(run domain.ProjectRunRecord, transition TransitionRequest, success bool) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputResult, CreatedAt: time.Now().UTC(), Run: run, ExitCode: transition.ExitCode, Success: success, Output: transition.Output, ResultJSON: transition.ResultJSON, Error: transition.Error}
}

func runAttachErrorResponse(code, message string, terminal bool) RunAttachOutput {
	return RunAttachOutput{Kind: RunAttachOutputError, CreatedAt: time.Now().UTC(), Code: code, Error: message, Terminal: terminal}
}

func driverOutputStreamToRun(frameType driverpkg.RuntimeOutputFrameType) domain.StdioStream {
	if frameType == driverpkg.RuntimeOutputStderr {
		return domain.StdioStderr
	}
	return domain.StdioStdout
}

func warningsFromRun(run domain.ProjectRunRecord) []string {
	return append([]string(nil), run.Warnings...)
}

func projectRunCommandArtifactsDir(run domain.ProjectRunRecord, sandbox *domain.Sandbox) string {
	return filepath.Join(execution.HostSandboxDir(sandbox), "state", "runs", run.RunID)
}

func projectRunAgentCellOutputPath(sandbox *domain.Sandbox, cellID string) string {
	cellID = strings.TrimSpace(cellID)
	if sandbox == nil || cellID == "" {
		return ""
	}
	return filepath.Join(execution.HostSandboxDir(sandbox), "state", "cells", cellID, "output.txt")
}

func (c *Controller) publishRunLogChunk(runID string, chunk domain.ExecChunk, offset uint64) {
	if c == nil {
		return
	}
	publishRunLogChunk(c.runLogs, runID, chunk, offset)
}

func publishRunLogChunk(hub *RunLogHub, runID string, chunk domain.ExecChunk, offset uint64) {
	if hub == nil {
		return
	}
	_ = hub.Publish(RunLogEvent{
		RunID:     runID,
		Data:      chunk.Text,
		Offset:    offset,
		CreatedAt: time.Now().UTC(),
	})
}

func appendProjectRunLogChunk(path string, chunk domain.ExecChunk) (uint64, error) {
	path = strings.TrimSpace(path)
	if path == "" || chunk.Text == "" {
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("create run log dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open run log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("seek run log %s: %w", path, err)
	}
	n, err := file.WriteString(chunk.Text)
	if err != nil {
		return 0, fmt.Errorf("append run log %s: %w", path, err)
	}
	return uint64(offset) + uint64(n), nil
}

func execEnvMap(items []*agentcomposev2.EnvVarSpec) map[string]string {
	if len(items) == 0 {
		return nil
	}
	env := make(map[string]string)
	for _, item := range items {
		name := strings.TrimSpace(item.GetName())
		if name == "" {
			continue
		}
		env[name] = item.GetValue()
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func (c *Controller) prepareProjectRun(ctx context.Context, run domain.ProjectRunRecord, requestEnv []*agentcomposev2.EnvVarSpec) (Preparation, error) {
	return PrepareProjectRun(ctx, c.configDB, projectRunWorkspaceResolver{controller: c}, run, requestEnv)
}

func resolveRunJupyterOptions(base sandboxstore.CreateSandboxOptions, override *agentcomposev2.RunJupyterSpec) (sandboxstore.CreateSandboxOptions, error) {
	result := base
	if override == nil {
		return result, nil
	}
	if override.GetGuestPort() > 65535 {
		return sandboxstore.CreateSandboxOptions{}, fmt.Errorf("%w: jupyter guest_port must be 0 or a valid TCP port between 1 and 65535", ErrInvalidRequest)
	}
	if override.GetEnabled() || override.GetExpose() {
		result.JupyterEnabled = true
	}
	if override.GetGuestPort() != 0 {
		result.JupyterGuestPort = int(override.GetGuestPort())
	}
	if override.GetExpose() {
		result.JupyterExpose = true
	}
	return result, nil
}

func (c *Controller) ensureProjectRunSandbox(ctx context.Context, run domain.ProjectRunRecord, prepared Preparation, req RunAgentRequest) (SandboxResult, error) {
	if c == nil || c.config == nil || c.store == nil || c.driver == nil {
		return SandboxResult{}, fmt.Errorf("sandbox runtime dependencies are required")
	}
	jupyterOptions, err := resolveRunJupyterOptions(prepared.SandboxOptions, req.Jupyter)
	if err != nil {
		return SandboxResult{}, err
	}
	stickySchedulerID := strings.TrimSpace(req.StickyBindingSchedulerID)
	stickyTriggerID := strings.TrimSpace(req.StickyBindingTriggerID)
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(run.Driver, c.config.RuntimeDriver)
	if err != nil {
		return SandboxResult{}, err
	}
	driverValidated := false
	if stickySchedulerID == "" || strings.TrimSpace(req.StickyBindingConfigHash) != "" {
		if err := c.validateSandboxRuntimeDriver(driver); err != nil {
			return SandboxResult{}, err
		}
		driverValidated = true
	}
	guestImage := driverpkg.ResolveSandboxGuestImage(run.ImageRef, driverpkg.DefaultGuestImageForDriver(c.config, driver))
	var volumeMounts []domain.SandboxVolumeMount
	var volumeWarnings []string
	volumesResolved := false
	if strings.TrimSpace(req.SandboxID) == "" && strings.TrimSpace(req.StickyBindingConfigHash) != "" {
		volumeMounts, volumeWarnings, err = c.resolveProjectRunVolumeMounts(ctx, prepared, req)
		if err != nil {
			return SandboxResult{}, err
		}
		jupyterOptions.VolumeMounts = volumeMounts
		volumesResolved = true
	}
	stickyConfigHash, err := stickyProjectRunConfigHash(req.StickyBindingConfigHash, run, prepared, driver, guestImage, volumeMounts, jupyterOptions)
	if err != nil {
		return SandboxResult{}, fmt.Errorf("hash sticky project sandbox configuration: %w", err)
	}
	tags := SandboxTags(run)
	agentConfig := execution.AgentConfig{Provider: domain.DefaultAgentProvider}
	if prepared.AgentDefinition != nil {
		agentConfig = execution.AgentConfigFromDefinition(*prepared.AgentDefinition, domain.DefaultAgentProvider)
		tags = append(tags,
			domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: prepared.AgentDefinition.ID},
			domain.SandboxTag{Name: domain.AgentSandboxTagName, Value: prepared.AgentDefinition.Name},
		)
	}
	tags = append(tags, domain.SandboxTag{Name: domain.AgentSandboxTagProvider, Value: agentConfig.Provider})
	capabilityVars, capabilityTags := capabilities.BuildGatewaySandboxVars(capabilities.ProxyTarget(c.cap), prepared.CapsetIDs)
	tags = append(tags, capabilityTags...)
	trustedHeaders := domain.TrustedHeadersFromContext(ctx)
	bindingStore, hasBindingStore := c.configDB.(stickyBindingStore)
	var previousStickyBinding *domain.SchedulerBinding
	boundSandbox := false
	warnings := []string(nil)
	if stickySchedulerID != "" && strings.TrimSpace(req.SandboxID) == "" {
		if !hasBindingStore {
			return SandboxResult{}, fmt.Errorf("sticky sandbox binding store is required")
		}
		sandboxID, binding, bindingWarnings, err := c.resolveStickySchedulerBinding(ctx, bindingStore, stickySchedulerID, stickyTriggerID, stickyConfigHash)
		if err != nil {
			return SandboxResult{}, err
		}
		warnings = append(warnings, bindingWarnings...)
		previousStickyBinding = binding
		if sandboxID != "" {
			req.SandboxID = sandboxID
			boundSandbox = true
		}
	}
	if sandboxID := strings.TrimSpace(req.SandboxID); sandboxID != "" {
		unlock := c.lifecycleLocks.Lock(sandboxID)
		locked := true
		defer func() {
			if locked {
				unlock()
			}
		}()
		if len(req.Volumes) > 0 {
			return SandboxResult{}, fmt.Errorf("%w: run volumes cannot be combined with an existing sandbox", ErrInvalidRequest)
		}
		if boundSandbox && previousStickyBinding != nil {
			current, found, err := bindingStore.GetSchedulerBinding(ctx, stickySchedulerID, stickyTriggerID)
			if err != nil {
				return SandboxResult{}, fmt.Errorf("revalidate sticky sandbox binding: %w", err)
			}
			if !found || !schedulers.SchedulerBindingsMatch(current, *previousStickyBinding) {
				return SandboxResult{}, fmt.Errorf("sticky sandbox binding changed concurrently")
			}
		}
		sandbox, err := c.store.GetSandbox(ctx, sandboxID)
		if err != nil {
			if !boundSandbox {
				return SandboxResult{}, fmt.Errorf("load sandbox %s: %w", sandboxID, err)
			}
			if !driverValidated {
				if validateErr := c.validateSandboxRuntimeDriver(driver); validateErr != nil {
					return SandboxResult{}, validateErr
				}
				driverValidated = true
			}
			retiring := schedulers.RetiringSchedulerBinding(*previousStickyBinding, stickyConfigHash)
			claimed, claimErr := bindingStore.CompareAndSwapSchedulerBinding(ctx, previousStickyBinding, retiring)
			if claimErr != nil {
				return SandboxResult{}, fmt.Errorf("claim unavailable sticky sandbox %s retirement: %w", sandboxID, claimErr)
			}
			if !claimed {
				return SandboxResult{}, fmt.Errorf("sticky sandbox binding changed concurrently")
			}
			previousStickyBinding = &retiring
			warnings = append(warnings, fmt.Sprintf("sticky sandbox %s is unavailable; creating a replacement", sandboxID))
			unlock()
			locked = false
		} else {
			if err := validateProjectRunSandboxOwnership(sandbox, run); err != nil {
				return SandboxResult{}, err
			}
			if pendingRunID, pending, err := c.pendingCompletionForSandbox(ctx, sandboxID); err != nil {
				return SandboxResult{}, err
			} else if pending && pendingRunID != run.RunID {
				return SandboxResult{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("sandbox %s has pending completion for run %s", sandboxID, pendingRunID), nil)
			}
			if sandbox.Summary.VMStatus == domain.VMStatusDeleting {
				return SandboxResult{Sandbox: sandbox}, fmt.Errorf("sandbox %s is being deleted", sandboxID)
			}
			driver, err := driverpkg.ResolveSandboxRuntimeDriver(sandbox.Summary.Driver, c.config.RuntimeDriver)
			if err != nil {
				return SandboxResult{}, err
			}
			if err := c.validateSandboxRuntimeDriver(driver); err != nil {
				return SandboxResult{Sandbox: sandbox}, err
			}
			if _, err := NewCoordinator(c.configDB, domain.StableProjectRunID).BindSandbox(ctx, run.RunID, sandboxID, false); err != nil {
				return SandboxResult{Sandbox: sandbox}, fmt.Errorf("bind reused sandbox to project run: %w", err)
			}
			if sandbox.Summary.VMStatus != domain.VMStatusRunning {
				if err := c.applyJupyterOptionsToSandbox(sandbox, jupyterOptions); err != nil {
					return SandboxResult{Sandbox: sandbox}, err
				}
				guestImage := driverpkg.ResolveSandboxGuestImage(sandbox.Summary.GuestImage, driverpkg.DefaultGuestImageForDriver(c.config, driver))
				if err := images.EnsureDriverImage(ctx, c.config, c.images, images.EnsureRequest{
					Driver:      driver,
					ImageRef:    guestImage,
					ProjectName: run.ProjectName,
					AgentName:   run.AgentName,
				}); err != nil {
					return SandboxResult{Sandbox: sandbox}, err
				}
			}
			sandbox.EnvItems = domain.MergeEnvItems(sandbox.EnvItems, capabilityVars)
			sandbox.Summary.Tags = MergeSandboxTags(sandbox.Summary.Tags, tags)
			if err := c.startProjectRunSandbox(ctx, sandbox, "sandbox.resumed", "sandbox resumed for project run", trustedHeaders); err != nil {
				return SandboxResult{Sandbox: sandbox}, err
			}
			return SandboxResult{Sandbox: sandbox, Warnings: warnings}, nil
		}
	}

	workspaceID := ""
	if prepared.Workspace != nil {
		workspaceID = strings.TrimSpace(prepared.Workspace.ID)
	}
	if !driverValidated {
		if err := c.validateSandboxRuntimeDriver(driver); err != nil {
			return SandboxResult{}, err
		}
	}
	if !volumesResolved {
		volumeMounts, volumeWarnings, err = c.resolveProjectRunVolumeMounts(ctx, prepared, req)
		if err != nil {
			return SandboxResult{}, err
		}
		jupyterOptions.VolumeMounts = volumeMounts
	}
	if err := images.EnsureDriverImage(ctx, c.config, c.images, images.EnsureRequest{
		Driver:      driver,
		ImageRef:    guestImage,
		ProjectName: run.ProjectName,
		AgentName:   run.AgentName,
	}); err != nil {
		return SandboxResult{}, err
	}
	sandbox, err := c.store.CreateSandboxWithOptions(ctx,
		SandboxTitle(run),
		"",
		driver,
		guestImage,
		workspaceID,
		domain.SandboxTypeManual,
		prepared.Workspace,
		domain.MergeEnvItems(prepared.EnvItems, capabilityVars),
		tags,
		jupyterOptions,
	)
	if err != nil {
		return SandboxResult{}, err
	}
	if _, err := NewCoordinator(c.configDB, domain.StableProjectRunID).BindSandbox(ctx, run.RunID, sandbox.Summary.ID, true); err != nil {
		bindErr := fmt.Errorf("bind created sandbox to project run: %w", err)
		if c.removal == nil {
			return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, bindErr
		}
		_, removeErr := c.removal.Remove(context.WithoutCancel(ctx), sandbox.Summary.ID, true)
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, errors.Join(bindErr, removeErr)
	}
	llms.SetSandboxProviderEnvItems(sandbox, prepared.ProviderEnvItems)
	if err := c.ensureProjectRunSandboxWorkspace(ctx, sandbox); err != nil {
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, err
	}
	if c.executor == nil {
		sandbox.Summary.VMStatus = domain.VMStatusFailed
		_ = c.store.UpdateSandbox(ctx, sandbox)
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("agent executor is required")
	}
	if err := c.executor.PrepareSandboxAgentEnvironment(ctx, sandbox, agentConfig, prepared.AgentDefinition); err != nil {
		sandbox.Summary.VMStatus = domain.VMStatusFailed
		_ = c.store.UpdateSandbox(ctx, sandbox)
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, err
	}
	if err := c.startProjectRunSandboxRuntime(ctx, sandbox, "sandbox.created", "sandbox started for project run", trustedHeaders); err != nil {
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, err
	}
	if stickySchedulerID != "" {
		if !hasBindingStore {
			return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("sticky sandbox binding store is required")
		}
		claimed, err := bindingStore.CompareAndSwapSchedulerBinding(ctx, previousStickyBinding, domain.SchedulerBinding{SchedulerID: stickySchedulerID, TriggerID: stickyTriggerID, SandboxID: sandbox.Summary.ID, SandboxConfigHash: stickyConfigHash})
		if err != nil {
			if stopErr := c.stopProjectRunSandbox(ctx, sandbox); stopErr != nil {
				return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, errors.Join(fmt.Errorf("persist sticky sandbox binding: %w", err), fmt.Errorf("retire unbound sticky sandbox: %w", stopErr))
			}
			return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("persist sticky sandbox binding: %w", err)
		}
		if !claimed {
			if err := c.stopProjectRunSandbox(ctx, sandbox); err != nil {
				return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("retire unclaimed sticky sandbox: %w", err)
			}
			winner, compatible, err := loadCompatibleStickySchedulerBinding(ctx, bindingStore, stickySchedulerID, stickyTriggerID, stickyConfigHash)
			if err != nil {
				return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("load concurrently claimed sticky sandbox: %w", err)
			}
			if compatible {
				reuseRequest := req
				reuseRequest.SandboxID = winner.SandboxID
				reuseRequest.Volumes = nil
				reuseRequest.StickyBindingSchedulerID = ""
				reuseRequest.StickyBindingTriggerID = ""
				reuseRequest.StickyBindingConfigHash = ""
				result, reuseErr := c.ensureProjectRunSandbox(ctx, run, prepared, reuseRequest)
				result.Warnings = append(append(warnings, volumeWarnings...), result.Warnings...)
				if reuseErr != nil {
					return result, fmt.Errorf("reuse concurrently claimed sticky sandbox: %w", reuseErr)
				}
				return result, nil
			}
			return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("sticky sandbox binding changed concurrently")
		}
	}
	volumeWarnings = append(warnings, volumeWarnings...)
	return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, nil
}

func (c *Controller) pendingCompletionForSandbox(ctx context.Context, sandboxID string) (string, bool, error) {
	store, ok := c.configDB.(CompletionStore)
	if !ok {
		return "", false, nil
	}
	return store.ProjectRunCompletionForSandbox(ctx, sandboxID)
}

func (c *Controller) validateSandboxRuntimeDriver(driver string) error {
	err := driverpkg.ValidateCompiledRuntimeDriver(driver)
	if errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
		return domain.ClassifyError(domain.ErrUnsupported, "", err)
	}
	return err
}

func (c *Controller) resolveProjectRunVolumeMounts(ctx context.Context, prepared Preparation, req RunAgentRequest) ([]domain.SandboxVolumeMount, []string, error) {
	specs := prepared.Volumes
	if len(req.Volumes) > 0 {
		specs = req.Volumes
	}
	if len(specs) == 0 {
		return nil, nil, nil
	}
	if c.volumes == nil {
		return nil, nil, fmt.Errorf("volume resolver is required")
	}
	return c.volumes.ResolveMounts(ctx, specs, volumes.ResolveOptions{
		ProjectRoot:    prepared.ProjectRoot,
		ProjectVolumes: prepared.ProjectVolumes,
	})
}

func (c *Controller) applyJupyterOptionsToSandbox(sandbox *domain.Sandbox, options sandboxstore.CreateSandboxOptions) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is required")
	}
	proxyState, err := c.store.GetProxyState(sandbox.Summary.ID)
	if err != nil {
		return err
	}
	if !options.JupyterEnabled && !options.JupyterExpose && options.JupyterGuestPort == 0 {
		return nil
	}
	proxyState.Enabled = proxyState.Enabled || options.JupyterEnabled || options.JupyterExpose
	proxyState.Exposed = proxyState.Exposed || options.JupyterExpose
	if options.JupyterGuestPort != 0 {
		proxyState.GuestPort = options.JupyterGuestPort
	}
	if proxyState.Enabled {
		if proxyState.GuestPort == 0 {
			proxyState.GuestPort = c.config.JupyterGuestPort
		}
		driver, err := driverpkg.ResolveSandboxRuntimeDriver(sandbox.Summary.Driver, c.config.RuntimeDriver)
		if err != nil {
			return err
		}
		if driver != driverpkg.RuntimeDriverDocker && proxyState.HostPort == 0 {
			hostPort, err := c.store.AllocateHostPortForJupyter()
			if err != nil {
				return err
			}
			proxyState.HostPort = hostPort
		}
		if strings.TrimSpace(proxyState.Token) == "" {
			proxyState.Token = uuid.NewString()
		}
		if strings.TrimSpace(proxyState.JupyterURL) == "" {
			proxyState.JupyterURL = proxyState.ProxyPath
		}
	}
	return c.store.SaveProxyState(sandbox.Summary.ID, proxyState)
}

func (c *Controller) startProjectRunSandbox(ctx context.Context, sandbox *domain.Sandbox, eventType, eventMessage string, trustedHeaders []domain.TrustedHeader) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is required")
	}
	if sandbox.Summary.VMStatus == domain.VMStatusDeleting {
		return fmt.Errorf("sandbox %s is being deleted", sandbox.Summary.ID)
	}
	if err := c.ensureProjectRunSandboxWorkspace(ctx, sandbox); err != nil {
		return err
	}
	if err := c.prepareFreshStartAgentEnvironment(ctx, sandbox); err != nil {
		sandbox.Summary.VMStatus = domain.VMStatusFailed
		_ = c.store.UpdateSandbox(ctx, sandbox)
		return err
	}
	return c.startProjectRunSandboxRuntime(ctx, sandbox, eventType, eventMessage, trustedHeaders)
}

func (c *Controller) ensureProjectRunSandboxWorkspace(ctx context.Context, sandbox *domain.Sandbox) error {
	if err := c.workspaceEnsurer.Ensure(ctx, sandbox); err != nil {
		sandbox.Summary.VMStatus = domain.VMStatusFailed
		_ = c.store.UpdateSandbox(ctx, sandbox)
		return err
	}
	return nil
}

func (c *Controller) prepareFreshStartAgentEnvironment(ctx context.Context, sandbox *domain.Sandbox) error {
	if sandbox.Summary.VMStatus == domain.VMStatusRunning {
		return nil
	}
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return err
	}
	if !vmState.StartedAt.IsZero() && !domain.SandboxRuntimeReleaseIntentional(sandbox) {
		return nil
	}
	if c.executor == nil {
		return fmt.Errorf("agent executor is required")
	}
	return c.executor.PrepareSandboxAgentEnvironmentFromTags(ctx, sandbox)
}

func (c *Controller) startProjectRunSandboxRuntime(ctx context.Context, sandbox *domain.Sandbox, eventType, eventMessage string, trustedHeaders []domain.TrustedHeader) error {
	writeCapabilityGuide(ctx, c.cap, c.store, c.streams, sandbox, capabilities.SandboxCapsets(sandbox))
	if sandbox.Summary.VMStatus != domain.VMStatusRunning {
		if err := c.driver.StartSandboxVM(ctx, sandbox); err != nil {
			sandbox.Summary.VMStatus = domain.VMStatusFailed
			_ = c.store.UpdateSandbox(ctx, sandbox)
			return err
		}
	}
	sandbox.StoppedRuntime = nil
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	if err := c.store.UpdateSandbox(ctx, sandbox); err != nil {
		return err
	}
	c.publishProjectRunSandboxStarted(ctx, sandbox, eventType, eventMessage)
	loaded, err := c.store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		return err
	}
	domain.RestoreSandboxTransientFields(loaded, sandbox)
	*sandbox = *loaded
	if c.capTokens != nil {
		c.capTokens.IndexSandbox(loaded, trustedHeaders)
	}
	return nil
}

func (c *Controller) publishProjectRunSandboxStarted(ctx context.Context, sandbox *domain.Sandbox, eventType, message string) {
	if c.streams != nil {
		c.streams.PublishSandboxUpdated(&sandbox.Summary)
	}
	if c.dashboard != nil {
		c.dashboard.Notify("sandbox_updated")
	}
	event := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      eventType,
		Level:     "info",
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
	_ = c.store.AddEvent(ctx, sandbox.Summary.ID, event)
	if c.streams != nil {
		c.streams.PublishEventAdded(sandbox.Summary.ID, event)
	}
	if c.bus != nil {
		topic := "agent-compose.sandbox.created"
		if eventType == "sandbox.resumed" {
			topic = "agent-compose.sandbox.resumed"
		}
		c.bus.Publish(domain.SchedulerTopicEvent{
			Topic:     topic,
			Payload:   schedulers.SessionTopicPayload(sandbox, "project-run"),
			CreatedAt: time.Now().UTC(),
		})
	}
}

func writeCapabilityGuide(ctx context.Context, provider capabilities.Provider, store SandboxRuntimeStore, streams *sandboxes.StreamBroker, sandbox *domain.Sandbox, capsetIDs []string) {
	ids := capabilities.NormalizeCapsetIDs(capsetIDs)
	if len(ids) == 0 || provider == nil || sandbox == nil {
		return
	}
	catalogPath := capabilities.SandboxGuidePath(sandbox)
	if catalogPath == "" {
		return
	}
	var b strings.Builder
	rendered := false
	for _, id := range ids {
		guide, err := capabilities.CapabilityGuideForScope(ctx, provider, capabilities.GuideScopeFromSandbox(sandbox), id)
		if err != nil {
			slog.Warn("capability guide render skipped", "capset", id, "sandbox_id", sandbox.Summary.ID, "error", err)
			recordCapabilityGuideWarning(ctx, store, streams, sandbox.Summary.ID, fmt.Sprintf("capability guide render skipped for capset %s", id))
			continue
		}
		if rendered {
			b.WriteString("\n\n")
		}
		b.Write(guide)
		rendered = true
	}
	if !rendered {
		return
	}
	content := b.String()
	if preamble := capabilities.GuidePreamble(capabilities.ProxyTarget(provider)); preamble != "" {
		content = preamble + content
	}
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0o755); err != nil {
		slog.Warn("capability guide dir create failed", "sandbox_id", sandbox.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, store, streams, sandbox.Summary.ID, "capability guide directory create failed")
		return
	}
	if err := os.WriteFile(catalogPath, []byte(content), 0o644); err != nil {
		slog.Warn("capability guide write failed", "sandbox_id", sandbox.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, store, streams, sandbox.Summary.ID, "capability guide write failed")
	}
}

func recordCapabilityGuideWarning(ctx context.Context, store SandboxRuntimeStore, streams *sandboxes.StreamBroker, sandboxID, message string) {
	if store == nil || strings.TrimSpace(sandboxID) == "" {
		return
	}
	event := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      "capability.guide.warning",
		Level:     "warning",
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.AddEvent(ctx, sandboxID, event); err != nil {
		slog.Warn("capability guide warning event failed", "sandbox_id", sandboxID, "error", err)
		return
	}
	if streams != nil {
		streams.PublishEventAdded(sandboxID, event)
	}
}
