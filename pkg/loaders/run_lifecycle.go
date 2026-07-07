package loaders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	domain "agent-compose/pkg/model"

	"github.com/google/uuid"
)

type RunStore interface {
	CreateLoaderRun(ctx context.Context, run domain.LoaderRunSummary) error
	UpdateLoaderRun(ctx context.Context, run domain.LoaderRunSummary) error
	UpdateSchedulerExecutionLastError(ctx context.Context, executionID, lastError string) error
}

type RunHost interface {
	LoaderHost
	CleanupCommandSessions(ctx context.Context)
}

type RunHostFactory func(loader domain.Loader, run *domain.LoaderRunSummary, triggerEvent TriggerEventMetadata) RunHost

type RunOptions struct {
	RetryWhenBusy  bool
	AlreadyEntered bool
}

type PreparedRun struct {
	Loader      domain.Loader
	Trigger     *domain.LoaderTrigger
	Run         domain.LoaderRunSummary
	PayloadJSON string
}

type RunExecutorDependencies struct {
	Store                      RunStore
	Engine                     LoaderEngine
	HostFactory                RunHostFactory
	ArtifactsDir               func(loaderID, runID string) string
	WriteArtifact              func(dir, name, content string) error
	EnterRun                   func(loader domain.Loader) bool
	LeaveRun                   func(loaderID string)
	AddSchedulerExecutionEvent func(ctx context.Context, executionID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error
	UpdateTriggerEventDelivery func(ctx context.Context, run domain.LoaderRunSummary)
	Notify                     func(reason string)
	Refresh                    func(ctx context.Context) error
}

type RunExecutor struct {
	deps RunExecutorDependencies
}

var ErrRunBusyForRetry = errors.New("loader is already running")

func NewRunExecutor(deps RunExecutorDependencies) *RunExecutor {
	return &RunExecutor{deps: deps}
}

func (e *RunExecutor) Run(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options RunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error) {
	prepared, err := e.Prepare(ctx, loader, trigger, payloadJSON, source, options)
	if err != nil {
		return domain.LoaderRunSummary{}, err
	}
	if len(triggerEventAck) > 0 && triggerEventAck[0] != nil {
		if err := triggerEventAck[0](ctx); err != nil {
			slog.Warn("failed to mark loader topic event published", "topic", source, "error", err)
		}
	}
	return e.Execute(ctx, prepared)
}

func (e *RunExecutor) Prepare(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options RunOptions) (PreparedRun, error) {
	payloadJSON, err := domain.NormalizeJSONDocument(payloadJSON)
	if err != nil {
		if options.AlreadyEntered {
			e.leaveRun(loader.Summary.ID)
		}
		return PreparedRun{}, err
	}
	now := time.Now().UTC()
	run := domain.LoaderRunSummary{
		ID:               uuid.NewString(),
		LoaderID:         loader.Summary.ID,
		TriggerSource:    strings.TrimSpace(source),
		Status:           domain.LoaderRunStatusRunning,
		StartedAt:        now,
		PayloadJSON:      payloadJSON,
		SourceScriptHash: domain.LoaderSourceSHA(loader.Script),
		ArtifactsDir:     e.artifactsDir(loader.Summary.ID, ""),
	}
	if trigger != nil {
		run.TriggerID = trigger.ID
		run.TriggerKind = trigger.Kind
	}
	run.ArtifactsDir = e.artifactsDir(loader.Summary.ID, run.ID)

	entered := options.AlreadyEntered
	if !entered && !e.enterRun(loader) {
		if options.RetryWhenBusy {
			return PreparedRun{}, ErrRunBusyForRetry
		}
		if err := os.MkdirAll(run.ArtifactsDir, 0o755); err != nil {
			return PreparedRun{}, fmt.Errorf("create loader run artifacts dir: %w", err)
		}
		_ = e.writeArtifact(run.ArtifactsDir, "payload.json", payloadJSON)
		run.Status = domain.LoaderRunStatusSkipped
		run.CompletedAt = now
		run.Error = "loader is already running"
		if err := e.deps.Store.CreateLoaderRun(ctx, run); err != nil {
			return PreparedRun{}, err
		}
		e.updateTriggerEventDelivery(ctx, run)
		e.notify("loader_run_updated")
		_ = e.deps.Store.UpdateSchedulerExecutionLastError(ctx, loader.Summary.ID, run.Error)
		_ = e.addSchedulerExecutionEvent(ctx, loader.Summary.ID, run.ID, run.TriggerID, "loader.run.skipped", "warn", run.Error, nil, "", "", "")
		_ = e.writeArtifact(run.ArtifactsDir, "error.txt", run.Error)
		return PreparedRun{Loader: loader, Trigger: trigger, Run: run, PayloadJSON: payloadJSON}, nil
	}

	if err := os.MkdirAll(run.ArtifactsDir, 0o755); err != nil {
		e.leaveRun(loader.Summary.ID)
		return PreparedRun{}, fmt.Errorf("create loader run artifacts dir: %w", err)
	}
	_ = e.writeArtifact(run.ArtifactsDir, "payload.json", payloadJSON)

	if err := e.deps.Store.CreateLoaderRun(ctx, run); err != nil {
		e.leaveRun(loader.Summary.ID)
		return PreparedRun{}, err
	}
	e.updateTriggerEventDelivery(ctx, run)
	e.notify("loader_run_updated")
	_ = e.addSchedulerExecutionEvent(ctx, loader.Summary.ID, run.ID, run.TriggerID, "loader.run.started", "info", "loader run started", map[string]any{"source": run.TriggerSource}, "", "", "")
	return PreparedRun{Loader: loader, Trigger: trigger, Run: run, PayloadJSON: payloadJSON}, nil
}

func (e *RunExecutor) Execute(ctx context.Context, prepared PreparedRun) (domain.LoaderRunSummary, error) {
	if prepared.Run.Status == domain.LoaderRunStatusSkipped {
		return prepared.Run, nil
	}
	defer e.leaveRun(prepared.Loader.Summary.ID)
	run := prepared.Run
	host := e.deps.HostFactory(prepared.Loader, &run, ParseTriggerEventMetadata(prepared.PayloadJSON))
	execution, execErr := e.deps.Engine.Execute(ctx, LoaderExecutionRequest{
		Runtime:     prepared.Loader.Summary.Runtime,
		Script:      prepared.Loader.Script,
		Trigger:     prepared.Trigger,
		PayloadJSON: prepared.PayloadJSON,
	}, host)

	writeCtx := context.WithoutCancel(ctx)
	if host != nil {
		host.CleanupCommandSessions(writeCtx)
	}

	completedAt := time.Now().UTC()
	run.CompletedAt = completedAt
	run.DurationMs = completedAt.Sub(run.StartedAt).Milliseconds()
	if execErr != nil {
		run.Status = domain.LoaderRunStatusFailed
		run.Error = execErr.Error()
		_ = e.writeArtifact(run.ArtifactsDir, "error.txt", run.Error)
		_ = e.deps.Store.UpdateSchedulerExecutionLastError(writeCtx, prepared.Loader.Summary.ID, run.Error)
		_ = e.addSchedulerExecutionEvent(writeCtx, prepared.Loader.Summary.ID, run.ID, run.TriggerID, "loader.run.failed", "error", run.Error, nil, "", "", "")
	} else {
		run.Status = domain.LoaderRunStatusSucceeded
		run.ResultJSON = execution.ResultJSON
		if execution.ResultJSON != "" {
			_ = e.writeArtifact(run.ArtifactsDir, "result.json", execution.ResultJSON)
		}
		_ = e.deps.Store.UpdateSchedulerExecutionLastError(writeCtx, prepared.Loader.Summary.ID, "")
		_ = e.addSchedulerExecutionEvent(writeCtx, prepared.Loader.Summary.ID, run.ID, run.TriggerID, "loader.run.completed", "info", "loader run completed", map[string]any{"resultJson": execution.ResultJSON}, "", "", "")
	}
	if err := e.deps.Store.UpdateLoaderRun(writeCtx, run); err != nil {
		return domain.LoaderRunSummary{}, err
	}
	e.updateTriggerEventDelivery(writeCtx, run)
	e.notify("loader_run_updated")
	if e.deps.Refresh != nil {
		if err := e.deps.Refresh(writeCtx); err != nil {
			slog.Warn("failed to refresh loaders after run", "loader_id", prepared.Loader.Summary.ID, "error", err)
		}
	}
	return run, nil
}

func (e *RunExecutor) Abort(ctx context.Context, prepared PreparedRun, reason string) {
	if prepared.Run.Status == domain.LoaderRunStatusSkipped {
		return
	}
	defer e.leaveRun(prepared.Loader.Summary.ID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "loader run aborted before execution"
	}
	run := prepared.Run
	completedAt := time.Now().UTC()
	run.Status = domain.LoaderRunStatusFailed
	run.CompletedAt = completedAt
	run.DurationMs = completedAt.Sub(run.StartedAt).Milliseconds()
	run.Error = reason
	_ = e.writeArtifact(run.ArtifactsDir, "error.txt", run.Error)
	_ = e.deps.Store.UpdateSchedulerExecutionLastError(ctx, prepared.Loader.Summary.ID, run.Error)
	_ = e.addSchedulerExecutionEvent(ctx, prepared.Loader.Summary.ID, run.ID, run.TriggerID, "loader.run.failed", "error", run.Error, nil, "", "", "")
	if err := e.deps.Store.UpdateLoaderRun(ctx, run); err != nil {
		slog.Warn("failed to abort prepared loader run", "loader_id", prepared.Loader.Summary.ID, "run_id", run.ID, "error", err)
	}
	e.updateTriggerEventDelivery(ctx, run)
	e.notify("loader_run_updated")
}

func (e *RunExecutor) artifactsDir(loaderID, runID string) string {
	if e.deps.ArtifactsDir == nil {
		return ""
	}
	return e.deps.ArtifactsDir(loaderID, runID)
}

func (e *RunExecutor) writeArtifact(dir, name, content string) error {
	if e.deps.WriteArtifact == nil {
		return nil
	}
	return e.deps.WriteArtifact(dir, name, content)
}

func (e *RunExecutor) enterRun(loader domain.Loader) bool {
	if e.deps.EnterRun == nil {
		return true
	}
	return e.deps.EnterRun(loader)
}

func (e *RunExecutor) leaveRun(loaderID string) {
	if e.deps.LeaveRun != nil {
		e.deps.LeaveRun(loaderID)
	}
}

func (e *RunExecutor) addSchedulerExecutionEvent(ctx context.Context, executionID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	if e.deps.AddSchedulerExecutionEvent == nil {
		return nil
	}
	return e.deps.AddSchedulerExecutionEvent(ctx, executionID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
}

func (e *RunExecutor) updateTriggerEventDelivery(ctx context.Context, run domain.LoaderRunSummary) {
	if e.deps.UpdateTriggerEventDelivery != nil {
		e.deps.UpdateTriggerEventDelivery(ctx, run)
	}
}

func (e *RunExecutor) notify(reason string) {
	if e.deps.Notify != nil {
		e.deps.Notify(reason)
	}
}
