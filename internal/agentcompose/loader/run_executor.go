package loader

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

type LoaderRunExecutor struct {
	host RunExecutorHost
}

type RunExecutorHost interface {
	EnterRun(loader Definition) bool
	LeaveRun(loaderID string)
	RunArtifactsDir(loaderID, runID string) string
	WriteRunArtifact(dir, name, content string) error
	CreateLoaderRun(ctx context.Context, run RunSummary) error
	UpdateLoaderRun(ctx context.Context, run RunSummary) error
	UpdateLoaderLastError(ctx context.Context, loaderID, errorText string) error
	AddLoaderEvent(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error
	UpdateTriggerEventDelivery(ctx context.Context, run RunSummary)
	NotifyDashboard(reason string)
	Refresh(ctx context.Context) error
	Engine() LoaderEngine
	NewRunHost(loader Definition, run *RunSummary, payloadJSON string) LoaderHost
	CleanupRunHost(ctx context.Context, host LoaderHost)
}

type RunOptions struct {
	RetryWhenBusy  bool
	AlreadyEntered bool
}

type PreparedRun struct {
	Loader      Definition
	Trigger     *Trigger
	Run         RunSummary
	PayloadJSON string
}

var ErrRunBusyForRetry = fmt.Errorf("loader is already running")

func NewLoaderRunExecutor(host RunExecutorHost) *LoaderRunExecutor {
	return &LoaderRunExecutor{host: host}
}

func (e *LoaderRunExecutor) Run(ctx context.Context, loader Definition, trigger *Trigger, payloadJSON, source string, options RunOptions, triggerEventAck ...func(context.Context) error) (RunSummary, error) {
	prepared, err := e.Prepare(ctx, loader, trigger, payloadJSON, source, options)
	if err != nil {
		return RunSummary{}, err
	}
	if len(triggerEventAck) > 0 && triggerEventAck[0] != nil {
		if err := triggerEventAck[0](ctx); err != nil {
			slog.Warn("failed to mark loader topic event published", "topic", source, "error", err)
		}
	}
	return e.Execute(ctx, prepared)
}

func (e *LoaderRunExecutor) Prepare(ctx context.Context, loader Definition, trigger *Trigger, payloadJSON, source string, options RunOptions) (PreparedRun, error) {
	m := e.host
	payloadJSON, err := NormalizeJSONDocument(payloadJSON)
	if err != nil {
		if options.AlreadyEntered {
			m.LeaveRun(loader.Summary.ID)
		}
		return PreparedRun{}, err
	}
	now := time.Now().UTC()
	run := RunSummary{
		ID:               uuid.NewString(),
		LoaderID:         loader.Summary.ID,
		TriggerSource:    strings.TrimSpace(source),
		Status:           RunStatusRunning,
		StartedAt:        now,
		PayloadJSON:      payloadJSON,
		SourceScriptHash: SourceSHA(loader.Script),
		ArtifactsDir:     m.RunArtifactsDir(loader.Summary.ID, ""),
	}
	if trigger != nil {
		run.TriggerID = trigger.ID
		run.TriggerKind = trigger.Kind
	}
	run.ArtifactsDir = m.RunArtifactsDir(loader.Summary.ID, run.ID)

	entered := options.AlreadyEntered
	if !entered && !m.EnterRun(loader) {
		if options.RetryWhenBusy {
			return PreparedRun{}, ErrRunBusyForRetry
		}
		if err := os.MkdirAll(run.ArtifactsDir, 0o755); err != nil {
			return PreparedRun{}, fmt.Errorf("create loader run artifacts dir: %w", err)
		}
		_ = m.WriteRunArtifact(run.ArtifactsDir, "payload.json", payloadJSON)
		run.Status = RunStatusSkipped
		run.CompletedAt = now
		run.Error = "loader is already running"
		if err := m.CreateLoaderRun(ctx, run); err != nil {
			return PreparedRun{}, err
		}
		m.UpdateTriggerEventDelivery(ctx, run)
		m.NotifyDashboard("loader_run_updated")
		_ = m.UpdateLoaderLastError(ctx, loader.Summary.ID, run.Error)
		_ = m.AddLoaderEvent(ctx, loader.Summary.ID, run.ID, run.TriggerID, "loader.run.skipped", "warn", run.Error, nil, "", "", "")
		_ = m.WriteRunArtifact(run.ArtifactsDir, "error.txt", run.Error)
		return PreparedRun{Loader: loader, Trigger: trigger, Run: run, PayloadJSON: payloadJSON}, nil
	}

	if err := os.MkdirAll(run.ArtifactsDir, 0o755); err != nil {
		m.LeaveRun(loader.Summary.ID)
		return PreparedRun{}, fmt.Errorf("create loader run artifacts dir: %w", err)
	}
	_ = m.WriteRunArtifact(run.ArtifactsDir, "payload.json", payloadJSON)

	if err := m.CreateLoaderRun(ctx, run); err != nil {
		m.LeaveRun(loader.Summary.ID)
		return PreparedRun{}, err
	}
	m.UpdateTriggerEventDelivery(ctx, run)
	m.NotifyDashboard("loader_run_updated")
	_ = m.AddLoaderEvent(ctx, loader.Summary.ID, run.ID, run.TriggerID, "loader.run.started", "info", "loader run started", map[string]any{"source": run.TriggerSource}, "", "", "")
	return PreparedRun{Loader: loader, Trigger: trigger, Run: run, PayloadJSON: payloadJSON}, nil
}

func (e *LoaderRunExecutor) Execute(ctx context.Context, prepared PreparedRun) (RunSummary, error) {
	m := e.host
	if prepared.Run.Status == RunStatusSkipped {
		return prepared.Run, nil
	}
	defer m.LeaveRun(prepared.Loader.Summary.ID)
	run := prepared.Run
	host := m.NewRunHost(prepared.Loader, &run, prepared.PayloadJSON)
	execution, execErr := m.Engine().Execute(ctx, LoaderExecutionRequest{
		Runtime:     prepared.Loader.Summary.Runtime,
		Script:      prepared.Loader.Script,
		Trigger:     prepared.Trigger,
		PayloadJSON: prepared.PayloadJSON,
	}, host)

	writeCtx := context.WithoutCancel(ctx)
	m.CleanupRunHost(writeCtx, host)

	completedAt := time.Now().UTC()
	run.CompletedAt = completedAt
	run.DurationMs = completedAt.Sub(run.StartedAt).Milliseconds()
	if execErr != nil {
		run.Status = RunStatusFailed
		run.Error = execErr.Error()
		_ = m.WriteRunArtifact(run.ArtifactsDir, "error.txt", run.Error)
		_ = m.UpdateLoaderLastError(writeCtx, prepared.Loader.Summary.ID, run.Error)
		_ = m.AddLoaderEvent(writeCtx, prepared.Loader.Summary.ID, run.ID, run.TriggerID, "loader.run.failed", "error", run.Error, nil, "", "", "")
	} else {
		run.Status = RunStatusSucceeded
		run.ResultJSON = execution.ResultJSON
		if execution.ResultJSON != "" {
			_ = m.WriteRunArtifact(run.ArtifactsDir, "result.json", execution.ResultJSON)
		}
		_ = m.UpdateLoaderLastError(writeCtx, prepared.Loader.Summary.ID, "")
		_ = m.AddLoaderEvent(writeCtx, prepared.Loader.Summary.ID, run.ID, run.TriggerID, "loader.run.completed", "info", "loader run completed", map[string]any{"resultJson": execution.ResultJSON}, "", "", "")
	}
	if err := m.UpdateLoaderRun(writeCtx, run); err != nil {
		return RunSummary{}, err
	}
	m.UpdateTriggerEventDelivery(writeCtx, run)
	m.NotifyDashboard("loader_run_updated")
	if err := m.Refresh(writeCtx); err != nil {
		slog.Warn("failed to refresh loaders after run", "loader_id", prepared.Loader.Summary.ID, "error", err)
	}
	return run, nil
}

func (e *LoaderRunExecutor) Abort(ctx context.Context, prepared PreparedRun, reason string) {
	m := e.host
	if prepared.Run.Status == RunStatusSkipped {
		return
	}
	defer m.LeaveRun(prepared.Loader.Summary.ID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "loader run aborted before execution"
	}
	run := prepared.Run
	completedAt := time.Now().UTC()
	run.Status = RunStatusFailed
	run.CompletedAt = completedAt
	run.DurationMs = completedAt.Sub(run.StartedAt).Milliseconds()
	run.Error = reason
	_ = m.WriteRunArtifact(run.ArtifactsDir, "error.txt", run.Error)
	_ = m.UpdateLoaderLastError(ctx, prepared.Loader.Summary.ID, run.Error)
	_ = m.AddLoaderEvent(ctx, prepared.Loader.Summary.ID, run.ID, run.TriggerID, "loader.run.failed", "error", run.Error, nil, "", "", "")
	if err := m.UpdateLoaderRun(ctx, run); err != nil {
		slog.Warn("failed to abort prepared loader run", "loader_id", prepared.Loader.Summary.ID, "run_id", run.ID, "error", err)
	}
	m.UpdateTriggerEventDelivery(ctx, run)
	m.NotifyDashboard("loader_run_updated")
}
