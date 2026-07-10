package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/volumes"
	"agent-compose/pkg/workspaces"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

var (
	ErrInvalidRequest     = errors.New("invalid run request")
	ErrRunAgentStreamSend = errors.New("run agent stream send failed")
)

type AgentExecutor interface {
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
	Publish(domain.LoaderTopicEvent) bool
}

type DashboardNotifier interface {
	Notify(reason string)
}

type CapabilitySandboxIndexer interface {
	IndexSandbox(*domain.Sandbox)
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
	GetLoader(context.Context, string) (domain.Loader, error)
}

type stickyBindingStore interface {
	GetLoaderBinding(context.Context, string, string) (domain.LoaderBinding, bool, error)
	UpsertLoaderBinding(context.Context, domain.LoaderBinding) error
}

// SandboxRuntimeStore is the subset of sandbox runtime persistence the run
// controller needs: sandbox lifecycle plus VM, proxy, and Jupyter-port state.
// Keeping it narrow decouples the controller from the concrete
// sessionstore.Store (and its full method set) and lets tests substitute a
// fake. It is distinct from SandboxStore in statuses.go, which exposes only
// domain-typed lookups for status listing.
type SandboxRuntimeStore interface {
	CreateSandboxWithOptions(ctx context.Context, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *sessionstore.SandboxWorkspace, envItems []sessionstore.SandboxEnvVar, tags []sessionstore.SandboxTag, options sessionstore.CreateSandboxOptions) (*sessionstore.Sandbox, error)
	GetSandbox(ctx context.Context, id string) (*sessionstore.Sandbox, error)
	UpdateSandbox(ctx context.Context, sandbox *sessionstore.Sandbox) error
	RemoveSandbox(ctx context.Context, id string) error
	AddEvent(ctx context.Context, sandboxID string, event sessionstore.SandboxEvent) error
	GetVMState(id string) (sessionstore.VMState, error)
	GetProxyState(id string) (sessionstore.ProxyState, error)
	SaveProxyState(id string, state sessionstore.ProxyState) error
	AllocateHostPortForJupyter() (int, error)
}

type Controller struct {
	config       *appconfig.Config
	store        SandboxRuntimeStore
	configDB     ControllerStore
	driver       SandboxDriver
	executor     AgentExecutor
	runtime      RuntimeProvider
	images       images.Backend
	loaderEngine loaders.LoaderEngine
	cap          capabilities.Provider
	volumes      VolumeResolver
	streams      *sessions.StreamBroker
	bus          TopicPublisher
	dashboard    DashboardNotifier
	capTokens    CapabilitySandboxIndexer
	runLogs      *RunLogHub
}

type llmFacadeTokenDeleter interface {
	DeleteLLMFacadeToken(context.Context, string) error
}

type llmFacadeStore interface {
	llms.LLMResolverStore
	SaveLLMFacadeToken(context.Context, llms.FacadeToken) error
}

type ControllerDependencies struct {
	Config       *appconfig.Config
	Store        SandboxRuntimeStore
	ConfigDB     ControllerStore
	Driver       SandboxDriver
	Executor     AgentExecutor
	Runtime      RuntimeProvider
	Images       images.Backend
	LoaderEngine loaders.LoaderEngine
	Cap          capabilities.Provider
	Volumes      VolumeResolver
	Streams      *sessions.StreamBroker
	Bus          TopicPublisher
	Dashboard    DashboardNotifier
	CapTokens    CapabilitySandboxIndexer
	RunLogs      *RunLogHub
}

func NewController(deps ControllerDependencies) *Controller {
	return &Controller{
		config:       deps.Config,
		store:        deps.Store,
		configDB:     deps.ConfigDB,
		driver:       deps.Driver,
		executor:     deps.Executor,
		runtime:      deps.Runtime,
		images:       deps.Images,
		loaderEngine: deps.LoaderEngine,
		cap:          deps.Cap,
		volumes:      deps.Volumes,
		streams:      deps.Streams,
		bus:          deps.Bus,
		dashboard:    deps.Dashboard,
		capTokens:    deps.CapTokens,
		runLogs:      deps.RunLogs,
	}
}

type RunAgentRequest struct {
	ProjectID              string
	AgentName              string
	Prompt                 string
	Command                string
	Source                 string
	SchedulerID            string
	TriggerID              string
	ClientRequestID        string
	Env                    []*agentcomposev2.EnvVarSpec
	SandboxID              string
	Volumes                []domain.VolumeMountSpec
	Driver                 string
	OutputSchemaJSON       string
	CleanupPolicy          agentcomposev2.RunSandboxCleanupPolicy
	Jupyter                *agentcomposev2.RunJupyterSpec
	StickyBindingLoaderID  string
	StickyBindingTriggerID string
}

type StreamSink struct {
	SendStarted func(run domain.ProjectRunRecord, createdAt time.Time) error
	SendChunk   func(runID string, chunk domain.ExecChunk, createdAt time.Time) error
}

type RunAttachReceiver func() (*agentcomposev2.RunAttachRequest, error)
type RunAttachSender func(*agentcomposev2.RunAttachResponse) error

func PrepareStreamingHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Set("Cache-Control", "no-cache, no-transform")
	headers.Set("X-Accel-Buffering", "no")
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
		TriggerID:       req.TriggerID,
		Prompt:          req.Prompt,
		Driver:          req.Driver,
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
	start := first.GetStart()
	if start == nil {
		return fmt.Errorf("%w: first run attach frame must be start", ErrInvalidRequest)
	}
	mode := start.GetMode()
	req := runAgentRequestFromAttachStart(start)
	commandText := strings.TrimSpace(req.Command)
	if mode == agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_UNSPECIFIED && commandText != "" {
		mode = agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND
	}
	if mode == agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_UNSPECIFIED && strings.TrimSpace(req.Prompt) != "" {
		mode = agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT
	}
	if mode != agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND && mode != agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT {
		return fmt.Errorf("%w: run attach command mode is required", ErrInvalidRequest)
	}
	if mode == agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND && commandText == "" {
		return fmt.Errorf("%w: run attach command is required", ErrInvalidRequest)
	}
	if mode == agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT && strings.TrimSpace(req.Prompt) == "" {
		return fmt.Errorf("%w: run attach prompt is required", ErrInvalidRequest)
	}
	started, err := c.StartProjectRun(ctx, req)
	if err != nil {
		return err
	}
	run, execErr, err := c.executeStartedProjectRunAttach(ctx, started.Run, req, started.Warnings, start, mode, receive, send)
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
		run, markErr := markProjectRunTerminalError(transitionCtx, coordinator, transition, err)
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
		run, markErr := markProjectRunTerminalError(transitionCtx, coordinator, transition, err)
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	warnings = append(warnings, sandboxResult.Warnings...)
	if err := ctx.Err(); err != nil {
		run, markErr := coordinator.MarkCanceled(transitionCtx, TransitionRequest{
			RunID:     run.RunID,
			SandboxID: sandboxResult.Sandbox.Summary.ID,
			Error:     err.Error(),
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	if current, loadErr := c.configDB.GetProjectRun(transitionCtx, run.RunID); loadErr == nil && StatusIsTerminal(current.Status) {
		run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, current, sandboxResult, req.CleanupPolicy)
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
			run, err = markProjectRunTerminalError(transitionCtx, coordinator, transition, execErr)
			if err != nil {
				return domain.ProjectRunRecord{}, nil, err
			}
			run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
			run = withRunWarnings(run, warnings)
			return run, execErr, nil
		}
		run, err = coordinator.MarkSucceeded(transitionCtx, transition)
		if err != nil {
			return domain.ProjectRunRecord{}, nil, err
		}
		run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, nil, nil
	}
	agentConfig, err := c.projectRunAgentConfig(ctx, run)
	if err != nil {
		run, markErr := coordinator.MarkFailed(transitionCtx, TransitionRequest{
			RunID:     run.RunID,
			SandboxID: sandboxResult.Sandbox.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	if c.executor == nil {
		err = fmt.Errorf("executor is required")
		run, markErr := coordinator.MarkFailed(transitionCtx, TransitionRequest{
			RunID:     run.RunID,
			SandboxID: sandboxResult.Sandbox.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	cell, _, _, execErr := c.executor.ExecuteAgentRequest(ctx, sandboxResult.Sandbox, execution.ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: run.ManagedAgentID,
		Model:             agentConfig.Model,
		RunID:             run.RunID,
		Message:           req.Prompt,
		OutputSchemaJSON:  req.OutputSchemaJSON,
		Stream:            projectRunAgentExecutionStream(transitionCtx, coordinator, run, sandboxResult.Sandbox, stream, c.runLogs),
	})
	transition := TransitionFromAgentCell(run, sandboxResult.Sandbox, cell, execErr)
	if execErr != nil || !cell.Success {
		run, err = markProjectRunTerminalError(transitionCtx, coordinator, transition, execErr)
		if err != nil {
			return domain.ProjectRunRecord{}, nil, err
		}
		run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, execErr, nil
	}
	run, err = coordinator.MarkSucceeded(transitionCtx, transition)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
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

func (c *Controller) executeStartedProjectRunAttach(ctx context.Context, run domain.ProjectRunRecord, req RunAgentRequest, warnings []string, start *agentcomposev2.RunAttachStart, mode agentcomposev2.AttachRunMode, receive RunAttachReceiver, send RunAttachSender) (domain.ProjectRunRecord, error, error) {
	coordinator := NewCoordinator(c.configDB, domain.StableProjectRunID)
	commandText := strings.TrimSpace(req.Command)
	transitionCtx := context.WithoutCancel(ctx)
	prepared, err := c.prepareProjectRun(ctx, run, req.Env)
	if err != nil {
		run, markErr := markProjectRunTerminalError(transitionCtx, coordinator, TransitionRequest{
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
		run, markErr := markProjectRunTerminalError(transitionCtx, coordinator, transition, err)
		if sandboxResult.Sandbox != nil {
			run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
		}
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
	case agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT:
		transition, execErr = c.runPromptInteraction(ctx, coordinator, run, sandboxResult.Sandbox, req, start, receive, send)
	default:
		transition, execErr = c.runCommandInteraction(ctx, coordinator, run, sandboxResult.Sandbox, req, commandText, start, receive, send)
	}
	if execErr != nil || transition.ExitCode != 0 {
		run, err = markProjectRunTerminalError(transitionCtx, coordinator, transition, execErr)
		if err != nil {
			return domain.ProjectRunRecord{}, nil, err
		}
		run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		_ = send(runAttachResultResponse(run, transition, false))
		return run, execErr, nil
	}
	run, err = coordinator.MarkSucceeded(transitionCtx, transition)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	run = c.cleanupProjectRunSandbox(transitionCtx, coordinator, run, sandboxResult, req.CleanupPolicy)
	run = withRunWarnings(run, warnings)
	if err := send(runAttachResultResponse(run, transition, true)); err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	return run, nil, nil
}

func (c *Controller) runCommandInteraction(ctx context.Context, coordinator *Coordinator, run domain.ProjectRunRecord, sandbox *domain.Sandbox, req RunAgentRequest, commandText string, start *agentcomposev2.RunAttachStart, receive RunAttachReceiver, send RunAttachSender) (TransitionRequest, error) {
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
	size := start.GetTerminalSize()
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
		AttachStdin: start.GetAttachStdin(),
		TTY:         start.GetTty(),
		Rows:        size.GetRows(),
		Cols:        size.GetCols(),
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
			stream := driverOutputStreamToRunProto(frame.Type)
			chunk := domain.ExecChunk{Text: string(frame.Data), Stream: protoStreamToRunDomain(stream)}
			accumulator.WriteChunk(chunk)
			offset, err := appendProjectRunLogChunk(logsPath, chunk)
			if err != nil {
				transition.ExitCode = 1
				transition.Error = fmt.Sprintf("command execution failed: %v", err)
				return transition, err
			}
			c.publishRunLogChunk(run.RunID, chunk, offset)
			if err := send(runAttachOutputResponse(frame.Data, stream, start.GetTty())); err != nil {
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

func (c *Controller) runPromptInteraction(ctx context.Context, coordinator *Coordinator, run domain.ProjectRunRecord, sandbox *domain.Sandbox, req RunAgentRequest, start *agentcomposev2.RunAttachStart, receive RunAttachReceiver, send RunAttachSender) (TransitionRequest, error) {
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
	if agentConfig.Provider != "codex" {
		err := fmt.Errorf("%w: prompt attach currently supports codex provider only", domain.ErrUnsupported)
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
			defer c.deletePromptAttachLLMFacadeToken(context.WithoutCancel(ctx), token)
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
	projector := newPromptAttachProjector(run, sandbox, logsPath, c.runLogs)
	input := &promptWrapperInput{interaction: interaction}
	if err := input.Start(agentConfig, c.config, schemaPath); err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %v", err)
		return transition, err
	}
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		if err := input.HumanMessage(prompt); err != nil {
			transition.ExitCode = 1
			transition.Error = fmt.Sprintf("agent execution failed: %v", err)
			return transition, err
		}
	}
	go pumpRunPromptAttachInput(receive, input, projector.AppendHumanMessage)
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
						return *promptTransition, errors.New(promptTransition.Error)
					}
					if promptTransition.ExitCode != 0 || strings.TrimSpace(promptTransition.Error) != "" {
						return *promptTransition, errors.New(firstNonEmpty(promptTransition.Error, "agent execution failed"))
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
			}
			if nextTransition != nil {
				promptTransition = nextTransition
			}
		case driverpkg.RuntimeOutputStderr:
			chunk := domain.ExecChunk{Text: string(frame.Data), Stream: domain.StdioStderr}
			offset, err := appendProjectRunLogChunk(logsPath, chunk)
			if err != nil {
				transition.ExitCode = 1
				transition.Error = fmt.Sprintf("agent execution failed: %v", err)
				return transition, err
			}
			c.publishRunLogChunk(run.RunID, chunk, offset)
			if err := send(runAttachOutputResponse(frame.Data, agentcomposev2.StdioStream_STDIO_STREAM_STDERR, false)); err != nil {
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
					return *promptTransition, errors.New(promptTransition.Error)
				}
				if promptTransition.ExitCode != 0 || strings.TrimSpace(promptTransition.Error) != "" {
					return *promptTransition, errors.New(firstNonEmpty(promptTransition.Error, "agent execution failed"))
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
	agent, err := c.configDB.GetAgentDefinition(ctx, run.ManagedAgentID)
	if err != nil {
		return execution.AgentConfig{}, fmt.Errorf("resolve managed agent definition %s: %w", run.ManagedAgentID, err)
	}
	config := execution.AgentConfigFromDefinition(agent, domain.DefaultAgentProvider)
	if config.Provider == "" {
		config.Provider = domain.DefaultAgentProvider
	}
	return config, nil
}

func (c *Controller) projectRunAgentSystemPrompt(ctx context.Context, run domain.ProjectRunRecord) (string, error) {
	if c == nil || c.configDB == nil || strings.TrimSpace(run.ManagedAgentID) == "" {
		return "", nil
	}
	agent, err := c.configDB.GetAgentDefinition(ctx, run.ManagedAgentID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(agent.SystemPrompt), nil
}

func (c *Controller) ensurePromptAttachLLMFacadeEnv(ctx context.Context, sandbox *domain.Sandbox, agent execution.AgentConfig, runID string) (map[string]string, error) {
	store, ok := c.configDB.(llmFacadeStore)
	if !ok || c.config == nil || sandbox == nil || domain.NormalizeAgentKind(agent.Provider) != "codex" {
		return nil, nil
	}
	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, c.config, store, sandbox.Summary.ID, llms.ProviderFamilyOpenAI, agent.Model, "", promptAttachSandboxProviderEnvItems(sandbox))
	if err != nil {
		if errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrFailedPrecondition) {
			return nil, nil
		}
		return nil, err
	}
	baseURL := llms.GuestRuntimeBaseURL(c.config, sandbox)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	tokenValue, token, err := llms.NewFacadeToken(sandbox.Summary.ID, target.Model.Name, target.Provider.ID, llms.APIProtocolResponses, "agent", runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/openai/v1"
	if err := llms.WriteCodexRuntimeConfig(sandbox, target.Model.Name, openAIBaseURL, llms.APIProtocolResponses); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            openAIBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolResponses,
		"OPENAI_API_KEY":              tokenValue,
		"OPENAI_BASE_URL":             openAIBaseURL,
	}, nil
}

func (c *Controller) deletePromptAttachLLMFacadeToken(ctx context.Context, token string) {
	store, ok := c.configDB.(llmFacadeTokenDeleter)
	if !ok || strings.TrimSpace(token) == "" {
		return
	}
	_ = store.DeleteLLMFacadeToken(ctx, token)
}

func promptAttachSandboxProviderEnvItems(sandbox *domain.Sandbox) []domain.SandboxEnvVar {
	if sandbox == nil {
		return nil
	}
	if len(sandbox.ProviderEnvItems) > 0 {
		return sandbox.ProviderEnvItems
	}
	return sandbox.EnvItems
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

func runAgentRequestFromAttachStart(start *agentcomposev2.RunAttachStart) RunAgentRequest {
	msg := start.GetRequest()
	if msg == nil {
		return RunAgentRequest{}
	}
	return RunAgentRequest{
		ProjectID:        msg.GetProjectId(),
		AgentName:        msg.GetAgentName(),
		Prompt:           msg.GetPrompt(),
		Command:          msg.GetCommand(),
		Source:           projectRunSourceFromAttachProto(msg.GetSource()),
		SchedulerID:      msg.GetSchedulerId(),
		TriggerID:        msg.GetTriggerId(),
		ClientRequestID:  msg.GetClientRequestId(),
		Env:              msg.GetEnv(),
		SandboxID:        msg.GetSandboxId(),
		Driver:           msg.GetDriver(),
		OutputSchemaJSON: msg.GetOutputSchemaJson(),
		CleanupPolicy:    msg.GetCleanupPolicy(),
		Jupyter:          msg.GetJupyter(),
	}
}

func projectRunSourceFromAttachProto(source agentcomposev2.RunSource) string {
	switch source {
	case agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER:
		return domain.ProjectRunSourceScheduler
	case agentcomposev2.RunSource_RUN_SOURCE_API:
		return domain.ProjectRunSourceAPI
	case agentcomposev2.RunSource_RUN_SOURCE_MANUAL:
		return domain.ProjectRunSourceManual
	default:
		return domain.ProjectRunSourceManual
	}
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

func runtimeInputFrameFromRunAttach(req *agentcomposev2.RunAttachRequest) (driverpkg.RuntimeInputFrame, bool) {
	switch frame := req.GetFrame().(type) {
	case *agentcomposev2.RunAttachRequest_Stdin:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdin, Data: frame.Stdin.GetData()}, true
	case *agentcomposev2.RunAttachRequest_StdinEof:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdinEOF}, true
	case *agentcomposev2.RunAttachRequest_Resize:
		size := frame.Resize.GetTerminalSize()
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputResize, Rows: size.GetRows(), Cols: size.GetCols()}, true
	case *agentcomposev2.RunAttachRequest_Signal:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputSignal, Signal: driverpkg.RuntimeSignal(strings.TrimSpace(frame.Signal.GetSignal()))}, true
	case *agentcomposev2.RunAttachRequest_Cancel:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputCancel, Message: frame.Cancel.GetReason()}, true
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

func pumpRunPromptAttachInput(receive RunAttachReceiver, input *promptWrapperInput, onHumanMessage func(string) error) {
	defer func() { _ = input.interaction.CloseSend() }()
	for {
		req, err := receive()
		if err != nil {
			_ = input.EOF()
			return
		}
		switch frame := req.GetFrame().(type) {
		case *agentcomposev2.RunAttachRequest_HumanMessage:
			text := frame.HumanMessage.GetText()
			if err := input.HumanMessage(text); err != nil {
				return
			}
			if onHumanMessage != nil {
				if err := onHumanMessage(text); err != nil {
					return
				}
			}
		case *agentcomposev2.RunAttachRequest_Stdin:
			text := string(frame.Stdin.GetData())
			if err := input.HumanMessage(text); err != nil {
				return
			}
			if onHumanMessage != nil {
				if err := onHumanMessage(text); err != nil {
					return
				}
			}
		case *agentcomposev2.RunAttachRequest_StdinEof:
			_ = input.EOF()
			return
		case *agentcomposev2.RunAttachRequest_Cancel:
			_ = input.Cancel(frame.Cancel.GetReason())
			return
		default:
			_ = input.Cancel("invalid run prompt attach frame")
			return
		}
	}
}

type promptAttachProjector struct {
	run        domain.ProjectRunRecord
	sandbox    *domain.Sandbox
	logsPath   string
	runLogs    *RunLogHub
	mu         sync.Mutex
	buffer     []byte
	itemTexts  map[string]string
	loggedText string
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

func (p *promptAttachProjector) Project(data []byte) ([]*agentcomposev2.RunAttachResponse, *TransitionRequest, error) {
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
	var responses []*agentcomposev2.RunAttachResponse
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

func (p *promptAttachProjector) projectLine(line []byte) ([]*agentcomposev2.RunAttachResponse, *TransitionRequest, error) {
	var frame struct {
		Type       string          `json:"type"`
		Event      json.RawMessage `json:"event"`
		FinalText  string          `json:"finalText"`
		SandboxID  string          `json:"sandboxId"`
		StopReason string          `json:"stopReason"`
		Code       string          `json:"code"`
		Message    string          `json:"message"`
		Provider   string          `json:"provider"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		return nil, nil, err
	}
	switch frame.Type {
	case "started":
		return []*agentcomposev2.RunAttachResponse{runAttachAgentEventResponse("started", "", string(line))}, nil, nil
	case "agent_event":
		name, text := p.agentEventText(frame.Event)
		if err := p.appendLogText(text); err != nil {
			return nil, nil, err
		}
		return []*agentcomposev2.RunAttachResponse{runAttachAgentEventResponse(firstNonEmpty(name, "agent_event"), text, string(frame.Event))}, nil, nil
	case "agent_turn_completed":
		if err := p.appendLogFinalText(frame.FinalText); err != nil {
			return nil, nil, err
		}
		return []*agentcomposev2.RunAttachResponse{runAttachAgentTurnCompletedResponse(p.run, string(line), warningsFromRun(p.run))}, nil, nil
	case "result":
		if err := p.appendLogFinalText(frame.FinalText); err != nil {
			return nil, nil, err
		}
		transition := transitionFromPromptWrapperResult(p.run, p.sandbox, p.logsPath, line, frame.FinalText, frame.StopReason, "")
		return nil, &transition, nil
	case "error":
		message := firstNonEmpty(frame.Message, "runtime stream error")
		transition := transitionFromPromptWrapperResult(p.run, p.sandbox, p.logsPath, line, "", frame.StopReason, message)
		return []*agentcomposev2.RunAttachResponse{runAttachErrorResponse(firstNonEmpty(frame.Code, "runtime_stream_error"), message, true)}, &transition, nil
	default:
		return []*agentcomposev2.RunAttachResponse{runAttachAgentEventResponse(firstNonEmpty(frame.Type, "agent_event"), "", string(line))}, nil, nil
	}
}

func (p *promptAttachProjector) agentEventText(raw json.RawMessage) (string, string) {
	var event struct {
		Type string `json:"type"`
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
	if err := p.appendLogChunkLocked(text); err != nil {
		return err
	}
	p.loggedText += text
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
		if err := p.appendLogChunkLocked(text); err != nil {
			return err
		}
		p.loggedText += text
		return nil
	}
	if p.loggedText == "" {
		if err := p.appendLogChunkLocked(finalText); err != nil {
			return err
		}
		p.loggedText = finalText
	}
	return nil
}

func (p *promptAttachProjector) AppendHumanMessage(message string) error {
	text := promptAttachHumanLogText(message)
	if text == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.appendLogChunkLocked(text)
}

func (p *promptAttachProjector) appendLogChunkLocked(text string) error {
	chunk := domain.ExecChunk{Text: text}
	offset, err := appendProjectRunLogChunk(p.logsPath, chunk)
	if err != nil {
		return err
	}
	publishRunLogChunk(p.runLogs, p.run.RunID, chunk, offset)
	return nil
}

func promptAttachHumanLogText(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
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

func runAttachStartedResponse(run domain.ProjectRunRecord, sandbox *domain.Sandbox, warnings []string, startedAt time.Time) *agentcomposev2.RunAttachResponse {
	resp := newRunAttachResponse()
	if !startedAt.IsZero() {
		resp.CreatedAt = startedAt.UTC().Format(time.RFC3339Nano)
	}
	resp.Frame = &agentcomposev2.RunAttachResponse_Started{Started: &agentcomposev2.AttachStarted{
		OperationId: run.RunID,
		RunId:       run.RunID,
		SandboxId:   sandbox.Summary.ID,
		Run:         projectRunSummaryForAttach(run),
		Warnings:    append([]string(nil), warnings...),
	}}
	return resp
}

func runAttachOutputResponse(data []byte, stream agentcomposev2.StdioStream, tty bool) *agentcomposev2.RunAttachResponse {
	resp := newRunAttachResponse()
	resp.Frame = &agentcomposev2.RunAttachResponse_Output{Output: &agentcomposev2.AttachOutput{
		Data:   append([]byte(nil), data...),
		Stream: stream,
		Tty:    tty,
		Transcript: &agentcomposev2.TranscriptEvent{
			Stream:    stream,
			Text:      string(data),
			CreatedAt: resp.CreatedAt,
		},
	}}
	return resp
}

func runAttachAgentEventResponse(name, text, payloadJSON string) *agentcomposev2.RunAttachResponse {
	resp := newRunAttachResponse()
	resp.Frame = &agentcomposev2.RunAttachResponse_AgentEvent{AgentEvent: &agentcomposev2.AttachAgentEvent{
		Name:        name,
		Text:        text,
		PayloadJson: payloadJSON,
		CreatedAt:   resp.CreatedAt,
	}}
	return resp
}

func runAttachAgentTurnCompletedResponse(run domain.ProjectRunRecord, resultJSON string, warnings []string) *agentcomposev2.RunAttachResponse {
	resp := newRunAttachResponse()
	resp.Frame = &agentcomposev2.RunAttachResponse_AgentTurnCompleted{AgentTurnCompleted: &agentcomposev2.AttachAgentTurnCompleted{
		RunId:      run.RunID,
		ResultJson: resultJSON,
		Warnings:   append([]string(nil), warnings...),
	}}
	return resp
}

func runAttachResultResponse(run domain.ProjectRunRecord, transition TransitionRequest, success bool) *agentcomposev2.RunAttachResponse {
	resp := newRunAttachResponse()
	resp.Frame = &agentcomposev2.RunAttachResponse_Result{Result: &agentcomposev2.AttachResult{
		ExitCode:   int32(transition.ExitCode),
		Success:    success,
		Run:        projectRunSummaryForAttach(run),
		Output:     transition.Output,
		ResultJson: transition.ResultJSON,
		Error:      transition.Error,
	}}
	return resp
}

func runAttachErrorResponse(code, message string, terminal bool) *agentcomposev2.RunAttachResponse {
	resp := newRunAttachResponse()
	resp.Frame = &agentcomposev2.RunAttachResponse_Error{Error: &agentcomposev2.AttachError{
		Code:     code,
		Message:  message,
		Terminal: terminal,
	}}
	return resp
}

func newRunAttachResponse() *agentcomposev2.RunAttachResponse {
	return &agentcomposev2.RunAttachResponse{
		ServerFrameId: uuid.NewString(),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func driverOutputStreamToRunProto(frameType driverpkg.RuntimeOutputFrameType) agentcomposev2.StdioStream {
	if frameType == driverpkg.RuntimeOutputStderr {
		return agentcomposev2.StdioStream_STDIO_STREAM_STDERR
	}
	return agentcomposev2.StdioStream_STDIO_STREAM_STDOUT
}

func protoStreamToRunDomain(stream agentcomposev2.StdioStream) domain.StdioStream {
	if stream == agentcomposev2.StdioStream_STDIO_STREAM_STDERR {
		return domain.StdioStderr
	}
	return domain.StdioStdout
}

func warningsFromRun(run domain.ProjectRunRecord) []string {
	return append([]string(nil), run.Warnings...)
}

func projectRunSummaryForAttach(run domain.ProjectRunRecord) *agentcomposev2.RunSummary {
	return &agentcomposev2.RunSummary{
		RunId:       run.RunID,
		ProjectId:   run.ProjectID,
		ProjectName: run.ProjectName,
		AgentName:   run.AgentName,
		Status:      projectRunStatusForAttach(run.Status),
		SandboxId:   run.SandboxID,
		Warnings:    append([]string(nil), run.Warnings...),
	}
}

func projectRunStatusForAttach(status string) agentcomposev2.RunStatus {
	switch NormalizeStatus(status) {
	case domain.ProjectRunStatusPending:
		return agentcomposev2.RunStatus_RUN_STATUS_PENDING
	case domain.ProjectRunStatusRunning:
		return agentcomposev2.RunStatus_RUN_STATUS_RUNNING
	case domain.ProjectRunStatusSucceeded:
		return agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED
	case domain.ProjectRunStatusFailed:
		return agentcomposev2.RunStatus_RUN_STATUS_FAILED
	case domain.ProjectRunStatusCanceled:
		return agentcomposev2.RunStatus_RUN_STATUS_CANCELED
	default:
		return agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED
	}
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

func resolveRunJupyterOptions(base sessionstore.CreateSandboxOptions, override *agentcomposev2.RunJupyterSpec) (sessionstore.CreateSandboxOptions, error) {
	result := base
	if override == nil {
		return result, nil
	}
	if override.GetGuestPort() > 65535 {
		return sessionstore.CreateSandboxOptions{}, fmt.Errorf("%w: jupyter guest_port must be 0 or a valid TCP port between 1 and 65535", ErrInvalidRequest)
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
	jupyterOptions, err := resolveRunJupyterOptions(prepared.Jupyter, req.Jupyter)
	if err != nil {
		return SandboxResult{}, err
	}
	tags := SandboxTags(run)
	capabilityVars, capabilityTags := capabilities.BuildGatewaySandboxVars(capabilities.ProxyTarget(c.cap), prepared.CapsetIDs)
	tags = append(tags, capabilityTags...)
	stickyLoaderID := strings.TrimSpace(req.StickyBindingLoaderID)
	stickyTriggerID := strings.TrimSpace(req.StickyBindingTriggerID)
	bindingStore, hasBindingStore := c.configDB.(stickyBindingStore)
	boundSandbox := false
	warnings := []string(nil)
	if stickyLoaderID != "" && strings.TrimSpace(req.SandboxID) == "" {
		if !hasBindingStore {
			return SandboxResult{}, fmt.Errorf("sticky sandbox binding store is required")
		}
		binding, found, err := bindingStore.GetLoaderBinding(ctx, stickyLoaderID, stickyTriggerID)
		if err != nil {
			return SandboxResult{}, fmt.Errorf("load sticky sandbox binding: %w", err)
		}
		if found {
			req.SandboxID = binding.SandboxID
			boundSandbox = true
		}
	}
	if sandboxID := strings.TrimSpace(req.SandboxID); sandboxID != "" {
		if len(req.Volumes) > 0 {
			return SandboxResult{}, fmt.Errorf("%w: run volumes cannot be combined with an existing sandbox", ErrInvalidRequest)
		}
		sandbox, err := c.store.GetSandbox(ctx, sandboxID)
		if err != nil {
			if !boundSandbox {
				return SandboxResult{}, fmt.Errorf("load sandbox %s: %w", sandboxID, err)
			}
			warnings = append(warnings, fmt.Sprintf("sticky sandbox %s is unavailable; creating a replacement", sandboxID))
		} else {
			if sandbox.Summary.VMStatus != domain.VMStatusRunning {
				if err := c.applyJupyterOptionsToSandbox(sandbox.Summary.ID, jupyterOptions); err != nil {
					return SandboxResult{Sandbox: sandbox}, err
				}
				driver, err := driverpkg.ResolveSandboxRuntimeDriver(sandbox.Summary.Driver, c.config.RuntimeDriver)
				if err != nil {
					return SandboxResult{}, err
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
			if err := c.startProjectRunSandbox(ctx, sandbox, "sandbox.resumed", "sandbox resumed for project run"); err != nil {
				return SandboxResult{Sandbox: sandbox}, err
			}
			return SandboxResult{Sandbox: sandbox, Warnings: warnings}, nil
		}
	}

	workspaceID := ""
	if prepared.Workspace != nil {
		workspaceID = strings.TrimSpace(prepared.Workspace.ID)
	}
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(run.Driver, c.config.RuntimeDriver)
	if err != nil {
		return SandboxResult{}, err
	}
	guestImage := driverpkg.ResolveSandboxGuestImage(run.ImageRef, driverpkg.DefaultGuestImageForDriver(c.config, driver))
	if err := images.EnsureDriverImage(ctx, c.config, c.images, images.EnsureRequest{
		Driver:      driver,
		ImageRef:    guestImage,
		ProjectName: run.ProjectName,
		AgentName:   run.AgentName,
	}); err != nil {
		return SandboxResult{}, err
	}
	volumeMounts, volumeWarnings, err := c.resolveProjectRunVolumeMounts(ctx, prepared, req)
	if err != nil {
		return SandboxResult{}, err
	}
	jupyterOptions.VolumeMounts = volumeMounts
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
	sandbox.ProviderEnvItems = prepared.ProviderEnvItems
	if err := c.startProjectRunSandbox(ctx, sandbox, "sandbox.created", "sandbox started for project run"); err != nil {
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, err
	}
	if stickyLoaderID != "" {
		if !hasBindingStore {
			return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("sticky sandbox binding store is required")
		}
		if err := bindingStore.UpsertLoaderBinding(ctx, domain.LoaderBinding{LoaderID: stickyLoaderID, TriggerID: stickyTriggerID, SandboxID: sandbox.Summary.ID}); err != nil {
			return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("persist sticky sandbox binding: %w", err)
		}
	}
	volumeWarnings = append(warnings, volumeWarnings...)
	return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, nil
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

func (c *Controller) applyJupyterOptionsToSandbox(sandboxID string, options sessionstore.CreateSandboxOptions) error {
	proxyState, err := c.store.GetProxyState(sandboxID)
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
		if proxyState.HostPort == 0 {
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
	return c.store.SaveProxyState(sandboxID, proxyState)
}

func (c *Controller) startProjectRunSandbox(ctx context.Context, sandbox *domain.Sandbox, eventType, eventMessage string) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is required")
	}
	if err := workspaces.PrepareSessionWorkspace(ctx, c.config, c.configDB, sandbox); err != nil {
		sandbox.Summary.VMStatus = domain.VMStatusFailed
		_ = c.store.UpdateSandbox(ctx, sandbox)
		return err
	}
	writeCapabilityGuide(ctx, c.cap, c.store, c.streams, sandbox, capabilities.SandboxCapsets(sandbox))
	if sandbox.Summary.VMStatus != domain.VMStatusRunning {
		if err := c.driver.StartSandboxVM(ctx, sandbox); err != nil {
			sandbox.Summary.VMStatus = domain.VMStatusFailed
			_ = c.store.UpdateSandbox(ctx, sandbox)
			return err
		}
	}
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
		c.bus.Publish(domain.LoaderTopicEvent{
			Topic:     topic,
			Payload:   loaders.SessionTopicPayload(sandbox, "project-run"),
			CreatedAt: time.Now().UTC(),
		})
	}
}

func (c *Controller) cleanupProjectRunSandbox(ctx context.Context, coordinator *Coordinator, run domain.ProjectRunRecord, sandboxResult SandboxResult, policy agentcomposev2.RunSandboxCleanupPolicy) domain.ProjectRunRecord {
	sandbox := sandboxResult.Sandbox
	if !CleanupPolicyStopsSandbox(policy) || sandbox == nil {
		return run
	}
	cleanupErr := c.cleanupProjectRunSandboxByPolicy(ctx, sandboxResult, policy)
	if cleanupErr == nil {
		return run
	}
	updated, err := coordinator.TransitionRun(ctx, TransitionRequest{
		RunID:        run.RunID,
		Status:       run.Status,
		SandboxID:    run.SandboxID,
		CleanupError: cleanupErr.Error(),
	})
	if err != nil {
		return run
	}
	return updated
}

func (c *Controller) cleanupProjectRunSandboxByPolicy(ctx context.Context, sandboxResult SandboxResult, policy agentcomposev2.RunSandboxCleanupPolicy) error {
	sandbox := sandboxResult.Sandbox
	if CleanupPolicyRemovesSandbox(policy) && sandboxResult.Created {
		if err := c.stopProjectRunSandbox(ctx, sandbox); err != nil {
			return err
		}
		if c.store == nil {
			return fmt.Errorf("sandbox store is required")
		}
		if c.driver == nil {
			return fmt.Errorf("sandbox driver is required")
		}
		if err := c.driver.RemoveSandboxVM(ctx, sandbox); err != nil {
			return err
		}
		if err := c.store.RemoveSandbox(ctx, sandbox.Summary.ID); err != nil {
			return err
		}
		if c.dashboard != nil {
			c.dashboard.Notify("sandbox_removed")
		}
		return nil
	}
	return c.stopProjectRunSandbox(ctx, sandbox)
}

func (c *Controller) stopProjectRunSandbox(ctx context.Context, sandbox *domain.Sandbox) error {
	if c.store == nil {
		return fmt.Errorf("sandbox store is required")
	}
	loaded, err := c.store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		return err
	}
	if loaded.Summary.VMStatus != domain.VMStatusRunning {
		return nil
	}
	if c.driver == nil {
		return fmt.Errorf("sandbox driver is required")
	}
	if err := c.driver.StopSandboxVM(ctx, loaded); err != nil {
		return err
	}
	loaded.Summary.VMStatus = domain.VMStatusStopped
	if err := c.store.UpdateSandbox(ctx, loaded); err != nil {
		return err
	}
	event := domain.SandboxEvent{ID: uuid.NewString(), Type: "sandbox.stopped", Level: "info", Message: "sandbox stopped", CreatedAt: time.Now().UTC()}
	_ = c.store.AddEvent(ctx, loaded.Summary.ID, event)
	if c.streams != nil {
		c.streams.PublishSandboxUpdated(&loaded.Summary)
		c.streams.PublishEventAdded(loaded.Summary.ID, event)
	}
	return nil
}

func writeCapabilityGuide(ctx context.Context, provider capabilities.Provider, store SandboxRuntimeStore, streams *sessions.StreamBroker, sandbox *domain.Sandbox, capsetIDs []string) {
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
		guide, err := provider.CapabilityGuide(ctx, id)
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

func recordCapabilityGuideWarning(ctx context.Context, store SandboxRuntimeStore, streams *sessions.StreamBroker, sandboxID, message string) {
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
