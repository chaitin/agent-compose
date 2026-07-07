package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/images"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/workspaces"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

var (
	ErrInvalidRequest     = errors.New("invalid run request")
	ErrRunAgentStreamSend = errors.New("run agent stream send failed")
)

type AgentExecutor interface {
	ExecuteAgentRequest(context.Context, *domain.Session, execution.ExecuteAgentRequest) (domain.NotebookCell, domain.SessionEvent, domain.SessionEvent, error)
}

type Runtime interface {
	ExecStream(context.Context, *domain.Session, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error)
}

type RuntimeProvider func(*domain.Session) (Runtime, error)

type SessionDriver interface {
	StartSessionVM(context.Context, *domain.Session) error
	StopSessionVM(context.Context, *domain.Session) error
}

type TopicPublisher interface {
	Publish(domain.LoaderTopicEvent) bool
}

type DashboardNotifier interface {
	Notify(reason string)
}

type ControllerStore interface {
	Store
	PreparationStore
	TriggerResolverStore
	workspaces.Store
}

type TriggerResolverStore interface {
	ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error)
	GetSchedulerExecution(context.Context, string) (domain.Loader, error)
}

// SessionRuntimeStore is the subset of session runtime persistence the run
// controller needs: session lifecycle plus VM, proxy, and Jupyter-port state.
// Keeping it narrow decouples the controller from the concrete
// sessionstore.Store (and its full method set) and lets tests substitute a
// fake. It is distinct from SessionStore in statuses.go, which exposes only
// domain-typed lookups for status listing.
type SessionRuntimeStore interface {
	CreateSessionWithOptions(ctx context.Context, title, baseWorkspace, driver, guestImage, workspaceID, triggerSource string, workspace *sessionstore.SessionWorkspace, envItems []sessionstore.SessionEnvVar, tags []sessionstore.SessionTag, options sessionstore.CreateSessionOptions) (*sessionstore.Session, error)
	GetSession(ctx context.Context, id string) (*sessionstore.Session, error)
	UpdateSession(ctx context.Context, session *sessionstore.Session) error
	RemoveSession(ctx context.Context, id string) error
	AddEvent(ctx context.Context, sessionID string, event sessionstore.SessionEvent) error
	GetVMState(id string) (sessionstore.VMState, error)
	GetProxyState(id string) (sessionstore.ProxyState, error)
	SaveProxyState(id string, state sessionstore.ProxyState) error
	AllocateHostPortForJupyter() (int, error)
}

type Controller struct {
	config       *appconfig.Config
	store        SessionRuntimeStore
	configDB     ControllerStore
	driver       SessionDriver
	executor     AgentExecutor
	runtime      RuntimeProvider
	images       images.Backend
	loaderEngine loaders.LoaderEngine
	cap          capabilities.Provider
	streams      *sessions.StreamBroker
	bus          TopicPublisher
	dashboard    DashboardNotifier
}

type ControllerDependencies struct {
	Config       *appconfig.Config
	Store        SessionRuntimeStore
	ConfigDB     ControllerStore
	Driver       SessionDriver
	Executor     AgentExecutor
	Runtime      RuntimeProvider
	Images       images.Backend
	LoaderEngine loaders.LoaderEngine
	Cap          capabilities.Provider
	Streams      *sessions.StreamBroker
	Bus          TopicPublisher
	Dashboard    DashboardNotifier
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
		streams:      deps.Streams,
		bus:          deps.Bus,
		dashboard:    deps.Dashboard,
	}
}

type RunAgentRequest struct {
	ProjectID        string
	AgentName        string
	Prompt           string
	Command          string
	Source           string
	SchedulerID      string
	TriggerID        string
	ClientRequestID  string
	Env              []*agentcomposev2.EnvVarSpec
	SessionID        string
	Driver           string
	OutputSchemaJSON string
	CleanupPolicy    agentcomposev2.RunSessionCleanupPolicy
	Jupyter          *agentcomposev2.RunJupyterSpec
}

type StreamSink struct {
	SendStarted func(run domain.ProjectRunRecord, createdAt time.Time) error
	SendChunk   func(runID string, chunk domain.ExecChunk, createdAt time.Time) error
}

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
	if strings.TrimSpace(req.SessionID) != "" && strings.TrimSpace(req.Driver) != "" {
		return StartedProjectRun{}, fmt.Errorf("%w: run driver cannot be combined with an existing sandbox", ErrInvalidRequest)
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
	sessionResult, err := c.ensureProjectRunSession(ctx, run, prepared, req)
	if err != nil {
		transition := TransitionRequest{
			RunID: run.RunID,
			Error: fmt.Sprintf("session start failed: %v", err),
		}
		if sessionResult.Session != nil {
			transition.SessionID = sessionResult.Session.Summary.ID
		}
		run, markErr := markProjectRunTerminalError(transitionCtx, coordinator, transition, err)
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = c.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	if err := ctx.Err(); err != nil {
		run, markErr := coordinator.MarkCanceled(transitionCtx, TransitionRequest{
			RunID:     run.RunID,
			SessionID: sessionResult.Session.Summary.ID,
			Error:     err.Error(),
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = c.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	if current, loadErr := c.configDB.GetProjectRun(transitionCtx, run.RunID); loadErr == nil && StatusIsTerminal(current.Status) {
		run = c.cleanupProjectRunSession(transitionCtx, coordinator, current, sessionResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, context.Canceled, nil
	}
	run, err = coordinator.MarkRunning(transitionCtx, run.RunID, sessionResult.Session.Summary.ID)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	run = withRunWarnings(run, warnings)
	if commandText != "" {
		transition, execErr := c.executeProjectRunCommand(ctx, run, sessionResult.Session, req, commandText, stream)
		if execErr != nil || transition.ExitCode != 0 {
			run, err = markProjectRunTerminalError(transitionCtx, coordinator, transition, execErr)
			if err != nil {
				return domain.ProjectRunRecord{}, nil, err
			}
			run = c.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult, req.CleanupPolicy)
			run = withRunWarnings(run, warnings)
			return run, execErr, nil
		}
		run, err = coordinator.MarkSucceeded(transitionCtx, transition)
		if err != nil {
			return domain.ProjectRunRecord{}, nil, err
		}
		run = c.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, nil, nil
	}
	agentConfig, err := c.projectRunAgentConfig(ctx, run)
	if err != nil {
		run, markErr := coordinator.MarkFailed(transitionCtx, TransitionRequest{
			RunID:     run.RunID,
			SessionID: sessionResult.Session.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = c.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	if c.executor == nil {
		err = fmt.Errorf("executor is required")
		run, markErr := coordinator.MarkFailed(transitionCtx, TransitionRequest{
			RunID:     run.RunID,
			SessionID: sessionResult.Session.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return domain.ProjectRunRecord{}, nil, markErr
		}
		run = c.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, err, nil
	}
	cell, _, _, execErr := c.executor.ExecuteAgentRequest(ctx, sessionResult.Session, execution.ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: run.ManagedAgentID,
		Model:             agentConfig.Model,
		RunID:             run.RunID,
		Message:           req.Prompt,
		OutputSchemaJSON:  req.OutputSchemaJSON,
		Stream:            projectRunAgentExecutionStream(transitionCtx, coordinator, run, sessionResult.Session, stream),
	})
	transition := TransitionFromAgentCell(run, sessionResult.Session, cell, execErr)
	if execErr != nil || !cell.Success {
		run, err = markProjectRunTerminalError(transitionCtx, coordinator, transition, execErr)
		if err != nil {
			return domain.ProjectRunRecord{}, nil, err
		}
		run = c.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult, req.CleanupPolicy)
		run = withRunWarnings(run, warnings)
		return run, execErr, nil
	}
	run, err = coordinator.MarkSucceeded(transitionCtx, transition)
	if err != nil {
		return domain.ProjectRunRecord{}, nil, err
	}
	run = c.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult, req.CleanupPolicy)
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

func (c *Controller) executeProjectRunCommand(ctx context.Context, run domain.ProjectRunRecord, session *domain.Session, req RunAgentRequest, commandText string, sink *StreamSink) (TransitionRequest, error) {
	artifactsDir := projectRunCommandArtifactsDir(run, session)
	logsPath := filepath.Join(artifactsDir, "transcript.txt")
	transition := TransitionRequest{
		RunID:     run.RunID,
		SessionID: session.Summary.ID,
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
	vmState, err := c.store.GetVMState(session.Summary.ID)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	runtime, err := c.runtime(session)
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
		if err := appendProjectRunLogChunk(logsPath, filtered); err != nil {
			sendErr = err
			return
		}
		if sink != nil && sink.SendChunk != nil {
			sendErr = sink.SendChunk(run.RunID, filtered, time.Now().UTC())
		}
	}
	execCtx, cancel := execution.ExecContext(ctx, 0)
	defer cancel()
	result, execErr := runtime.ExecStream(execCtx, session, vmState, execution.BuildRuntimeCommandExecSpec(c.config, session, filepath.Join(guestArtifactsDir, "command-request.json"), c.config.GuestHomePath), writer)
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
		transition = transitionFromCommandResult(run, session, commandText, result, execErr)
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
	transition = transitionFromCommandResult(run, session, commandText, execution.RuntimeCommandResultToExecResult(commandResult), nil)
	transition.LogsPath = logsPath
	return transition, nil
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

func projectRunAgentExecutionStream(ctx context.Context, coordinator *Coordinator, run domain.ProjectRunRecord, session *domain.Session, sink *StreamSink) execution.AgentExecutionStream {
	return execution.AgentExecutionStream{
		OnStart: func(cell domain.NotebookCell) error {
			if coordinator != nil {
				logsPath := projectRunAgentCellOutputPath(session, cell.ID)
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
			if err := appendProjectRunLogChunk(projectRunAgentCellOutputPath(session, cellID), chunk); err != nil {
				return err
			}
			if sink == nil || sink.SendChunk == nil {
				return nil
			}
			return sink.SendChunk(run.RunID, chunk, time.Now().UTC())
		},
	}
}

func transitionFromCommandResult(run domain.ProjectRunRecord, session *domain.Session, commandText string, result domain.ExecResult, execErr error) TransitionRequest {
	artifactsDir := projectRunCommandArtifactsDir(run, session)
	req := TransitionRequest{
		RunID:        run.RunID,
		SessionID:    session.Summary.ID,
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

func projectRunCommandArtifactsDir(run domain.ProjectRunRecord, session *domain.Session) string {
	return filepath.Join(execution.HostSessionDir(session), "state", "runs", run.RunID)
}

func projectRunAgentCellOutputPath(session *domain.Session, cellID string) string {
	cellID = strings.TrimSpace(cellID)
	if session == nil || cellID == "" {
		return ""
	}
	return filepath.Join(execution.HostSessionDir(session), "state", "cells", cellID, "output.txt")
}

func appendProjectRunLogChunk(path string, chunk domain.ExecChunk) error {
	path = strings.TrimSpace(path)
	if path == "" || chunk.Text == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create run log dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open run log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(chunk.Text); err != nil {
		return fmt.Errorf("append run log %s: %w", path, err)
	}
	return nil
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

func resolveRunJupyterOptions(base sessionstore.CreateSessionOptions, override *agentcomposev2.RunJupyterSpec) (sessionstore.CreateSessionOptions, error) {
	result := base
	if override == nil {
		return result, nil
	}
	if override.GetGuestPort() > 65535 {
		return sessionstore.CreateSessionOptions{}, fmt.Errorf("%w: jupyter guest_port must be 0 or a valid TCP port between 1 and 65535", ErrInvalidRequest)
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

func (c *Controller) ensureProjectRunSession(ctx context.Context, run domain.ProjectRunRecord, prepared Preparation, req RunAgentRequest) (SessionResult, error) {
	if c == nil || c.config == nil || c.store == nil || c.driver == nil {
		return SessionResult{}, fmt.Errorf("session runtime dependencies are required")
	}
	jupyterOptions, err := resolveRunJupyterOptions(prepared.Jupyter, req.Jupyter)
	if err != nil {
		return SessionResult{}, err
	}
	tags := SessionTags(run)
	capabilityVars, capabilityTags := capabilities.BuildGatewaySessionVars(capabilities.ProxyTarget(c.cap), prepared.CapsetIDs)
	tags = append(tags, capabilityTags...)
	if sessionID := strings.TrimSpace(req.SessionID); sessionID != "" {
		session, err := c.store.GetSession(ctx, sessionID)
		if err != nil {
			return SessionResult{}, fmt.Errorf("load session %s: %w", sessionID, err)
		}
		if session.Summary.VMStatus != domain.VMStatusRunning {
			if err := c.applyJupyterOptionsToSession(session.Summary.ID, jupyterOptions); err != nil {
				return SessionResult{Session: session}, err
			}
			driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, c.config.RuntimeDriver)
			if err != nil {
				return SessionResult{}, err
			}
			guestImage := driverpkg.ResolveSessionGuestImage(session.Summary.GuestImage, driverpkg.DefaultGuestImageForDriver(c.config, driver))
			if err := images.EnsureDriverImage(ctx, c.config, c.images, images.EnsureRequest{
				Driver:      driver,
				ImageRef:    guestImage,
				ProjectName: run.ProjectName,
				AgentName:   run.AgentName,
			}); err != nil {
				return SessionResult{Session: session}, err
			}
		}
		session.EnvItems = domain.MergeEnvItems(session.EnvItems, capabilityVars)
		session.Summary.Tags = MergeSessionTags(session.Summary.Tags, tags)
		if err := c.startProjectRunSession(ctx, session, "session.resumed", "session resumed for project run"); err != nil {
			return SessionResult{Session: session}, err
		}
		return SessionResult{Session: session}, nil
	}

	workspaceID := ""
	if prepared.Workspace != nil {
		workspaceID = strings.TrimSpace(prepared.Workspace.ID)
	}
	driver, err := driverpkg.ResolveSessionRuntimeDriver(run.Driver, c.config.RuntimeDriver)
	if err != nil {
		return SessionResult{}, err
	}
	guestImage := driverpkg.ResolveSessionGuestImage(run.ImageRef, driverpkg.DefaultGuestImageForDriver(c.config, driver))
	if err := images.EnsureDriverImage(ctx, c.config, c.images, images.EnsureRequest{
		Driver:      driver,
		ImageRef:    guestImage,
		ProjectName: run.ProjectName,
		AgentName:   run.AgentName,
	}); err != nil {
		return SessionResult{}, err
	}
	session, err := c.store.CreateSessionWithOptions(ctx,
		SessionTitle(run),
		"",
		driver,
		guestImage,
		workspaceID,
		domain.SessionTypeManual,
		prepared.Workspace,
		domain.MergeEnvItems(prepared.EnvItems, capabilityVars),
		tags,
		jupyterOptions,
	)
	if err != nil {
		return SessionResult{}, err
	}
	session.ProviderEnvItems = prepared.ProviderEnvItems
	if err := c.startProjectRunSession(ctx, session, "session.created", "session started for project run"); err != nil {
		return SessionResult{Session: session, Created: true}, err
	}
	return SessionResult{Session: session, Created: true}, nil
}

func (c *Controller) applyJupyterOptionsToSession(sessionID string, options sessionstore.CreateSessionOptions) error {
	proxyState, err := c.store.GetProxyState(sessionID)
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
	return c.store.SaveProxyState(sessionID, proxyState)
}

func (c *Controller) startProjectRunSession(ctx context.Context, session *domain.Session, eventType, eventMessage string) error {
	if session == nil {
		return fmt.Errorf("session is required")
	}
	if err := workspaces.PrepareSessionWorkspace(ctx, c.config, c.configDB, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = c.store.UpdateSession(ctx, session)
		return err
	}
	writeCapabilityGuide(ctx, c.cap, c.store, c.streams, session, capabilities.SessionCapsets(session))
	if session.Summary.VMStatus != domain.VMStatusRunning {
		if err := c.driver.StartSessionVM(ctx, session); err != nil {
			session.Summary.VMStatus = domain.VMStatusFailed
			_ = c.store.UpdateSession(ctx, session)
			return err
		}
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := c.store.UpdateSession(ctx, session); err != nil {
		return err
	}
	c.publishProjectRunSessionStarted(ctx, session, eventType, eventMessage)
	loaded, err := c.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return err
	}
	domain.RestoreSessionTransientFields(loaded, session)
	*session = *loaded
	return nil
}

func (c *Controller) publishProjectRunSessionStarted(ctx context.Context, session *domain.Session, eventType, message string) {
	if c.streams != nil {
		c.streams.PublishSessionUpdated(&session.Summary)
	}
	if c.dashboard != nil {
		c.dashboard.Notify("session_updated")
	}
	event := domain.SessionEvent{
		ID:        uuid.NewString(),
		Type:      eventType,
		Level:     "info",
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
	_ = c.store.AddEvent(ctx, session.Summary.ID, event)
	if c.streams != nil {
		c.streams.PublishEventAdded(session.Summary.ID, event)
	}
	if c.bus != nil {
		topic := "agent-compose.session.created"
		if eventType == "session.resumed" {
			topic = "agent-compose.session.resumed"
		}
		c.bus.Publish(domain.LoaderTopicEvent{
			Topic:     topic,
			Payload:   loaders.SessionTopicPayload(session, "project-run"),
			CreatedAt: time.Now().UTC(),
		})
	}
}

func (c *Controller) cleanupProjectRunSession(ctx context.Context, coordinator *Coordinator, run domain.ProjectRunRecord, sessionResult SessionResult, policy agentcomposev2.RunSessionCleanupPolicy) domain.ProjectRunRecord {
	session := sessionResult.Session
	if !CleanupPolicyStopsSession(policy) || session == nil {
		return run
	}
	cleanupErr := c.cleanupProjectRunSessionByPolicy(ctx, sessionResult, policy)
	if cleanupErr == nil {
		return run
	}
	updated, err := coordinator.TransitionRun(ctx, TransitionRequest{
		RunID:        run.RunID,
		Status:       run.Status,
		SessionID:    run.SessionID,
		CleanupError: cleanupErr.Error(),
	})
	if err != nil {
		return run
	}
	return updated
}

func (c *Controller) cleanupProjectRunSessionByPolicy(ctx context.Context, sessionResult SessionResult, policy agentcomposev2.RunSessionCleanupPolicy) error {
	session := sessionResult.Session
	if CleanupPolicyRemovesSession(policy) && sessionResult.Created {
		if err := c.stopProjectRunSession(ctx, session); err != nil {
			return err
		}
		if c.store == nil {
			return fmt.Errorf("session store is required")
		}
		if err := c.store.RemoveSession(ctx, session.Summary.ID); err != nil {
			return err
		}
		if c.dashboard != nil {
			c.dashboard.Notify("session_removed")
		}
		return nil
	}
	return c.stopProjectRunSession(ctx, session)
}

func (c *Controller) stopProjectRunSession(ctx context.Context, session *domain.Session) error {
	if c.store == nil {
		return fmt.Errorf("session store is required")
	}
	loaded, err := c.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return err
	}
	if loaded.Summary.VMStatus != domain.VMStatusRunning {
		return nil
	}
	if c.driver == nil {
		return fmt.Errorf("session driver is required")
	}
	if err := c.driver.StopSessionVM(ctx, loaded); err != nil {
		return err
	}
	loaded.Summary.VMStatus = domain.VMStatusStopped
	if err := c.store.UpdateSession(ctx, loaded); err != nil {
		return err
	}
	event := domain.SessionEvent{ID: uuid.NewString(), Type: "session.stopped", Level: "info", Message: "session stopped", CreatedAt: time.Now().UTC()}
	_ = c.store.AddEvent(ctx, loaded.Summary.ID, event)
	if c.streams != nil {
		c.streams.PublishSessionUpdated(&loaded.Summary)
		c.streams.PublishEventAdded(loaded.Summary.ID, event)
	}
	return nil
}

func writeCapabilityGuide(ctx context.Context, provider capabilities.Provider, store SessionRuntimeStore, streams *sessions.StreamBroker, session *domain.Session, capsetIDs []string) {
	ids := capabilities.NormalizeCapsetIDs(capsetIDs)
	if len(ids) == 0 || provider == nil || session == nil {
		return
	}
	catalogPath := capabilities.SessionGuidePath(session)
	if catalogPath == "" {
		return
	}
	var b strings.Builder
	rendered := false
	for _, id := range ids {
		guide, err := provider.CapabilityGuide(ctx, id)
		if err != nil {
			slog.Warn("capability guide render skipped", "capset", id, "session_id", session.Summary.ID, "error", err)
			recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, fmt.Sprintf("capability guide render skipped for capset %s", id))
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
		slog.Warn("capability guide dir create failed", "session_id", session.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, "capability guide directory create failed")
		return
	}
	if err := os.WriteFile(catalogPath, []byte(content), 0o644); err != nil {
		slog.Warn("capability guide write failed", "session_id", session.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, "capability guide write failed")
	}
}

func recordCapabilityGuideWarning(ctx context.Context, store SessionRuntimeStore, streams *sessions.StreamBroker, sessionID, message string) {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	event := domain.SessionEvent{
		ID:        uuid.NewString(),
		Type:      "capability.guide.warning",
		Level:     "warning",
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.AddEvent(ctx, sessionID, event); err != nil {
		slog.Warn("capability guide warning event failed", "session_id", sessionID, "error", err)
		return
	}
	if streams != nil {
		streams.PublishEventAdded(sessionID, event)
	}
}
