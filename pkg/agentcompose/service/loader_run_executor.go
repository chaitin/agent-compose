package agentcompose

import (
	"agent-compose/pkg/agentcompose/domain"
	"agent-compose/pkg/agentcompose/loaders"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

type LoaderRunExecutor struct {
	manager *LoaderManager
}

func NewLoaderRunExecutor(manager *LoaderManager) *LoaderRunExecutor {
	return &LoaderRunExecutor{manager: manager}
}

func (e *LoaderRunExecutor) Run(ctx context.Context, loader Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options loaderRunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error) {
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

func (e *LoaderRunExecutor) Prepare(ctx context.Context, loader Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options loaderRunOptions) (preparedLoaderRun, error) {
	m := e.manager
	payloadJSON, err := normalizeJSONDocument(payloadJSON)
	if err != nil {
		if options.alreadyEntered {
			m.leaveRun(loader.Summary.ID)
		}
		return preparedLoaderRun{}, err
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
		ArtifactsDir:     m.runArtifactsDir(loader.Summary.ID, ""),
	}
	if trigger != nil {
		run.TriggerID = trigger.ID
		run.TriggerKind = trigger.Kind
	}
	run.ArtifactsDir = m.runArtifactsDir(loader.Summary.ID, run.ID)

	entered := options.alreadyEntered
	if !entered && !m.enterRun(loader) {
		if options.retryWhenBusy {
			return preparedLoaderRun{}, errLoaderRunBusyForRetry
		}
		if err := os.MkdirAll(run.ArtifactsDir, 0o755); err != nil {
			return preparedLoaderRun{}, fmt.Errorf("create loader run artifacts dir: %w", err)
		}
		_ = m.writeRunArtifact(run.ArtifactsDir, "payload.json", payloadJSON)
		run.Status = domain.LoaderRunStatusSkipped
		run.CompletedAt = now
		run.Error = "loader is already running"
		if err := m.configDB.CreateLoaderRun(ctx, run); err != nil {
			return preparedLoaderRun{}, err
		}
		m.updateTriggerEventDelivery(ctx, run)
		m.notifyDashboard("loader_run_updated")
		_ = m.configDB.UpdateLoaderLastError(ctx, loader.Summary.ID, run.Error)
		_ = m.addLoaderEvent(ctx, loader.Summary.ID, run.ID, run.TriggerID, "loader.run.skipped", "warn", run.Error, nil, "", "", "")
		_ = m.writeRunArtifact(run.ArtifactsDir, "error.txt", run.Error)
		return preparedLoaderRun{loader: loader, trigger: trigger, run: run, payloadJSON: payloadJSON}, nil
	}

	if err := os.MkdirAll(run.ArtifactsDir, 0o755); err != nil {
		m.leaveRun(loader.Summary.ID)
		return preparedLoaderRun{}, fmt.Errorf("create loader run artifacts dir: %w", err)
	}
	_ = m.writeRunArtifact(run.ArtifactsDir, "payload.json", payloadJSON)

	if err := m.configDB.CreateLoaderRun(ctx, run); err != nil {
		m.leaveRun(loader.Summary.ID)
		return preparedLoaderRun{}, err
	}
	m.updateTriggerEventDelivery(ctx, run)
	m.notifyDashboard("loader_run_updated")
	_ = m.addLoaderEvent(ctx, loader.Summary.ID, run.ID, run.TriggerID, "loader.run.started", "info", "loader run started", map[string]any{"source": run.TriggerSource}, "", "", "")
	return preparedLoaderRun{loader: loader, trigger: trigger, run: run, payloadJSON: payloadJSON}, nil
}

func (e *LoaderRunExecutor) Execute(ctx context.Context, prepared preparedLoaderRun) (domain.LoaderRunSummary, error) {
	m := e.manager
	if prepared.run.Status == domain.LoaderRunStatusSkipped {
		return prepared.run, nil
	}
	defer m.leaveRun(prepared.loader.Summary.ID)
	run := prepared.run
	host := &loaderRunHost{manager: m, loader: prepared.loader, run: &run, triggerEvent: parseLoaderTriggerEventMetadata(prepared.payloadJSON)}
	execution, execErr := m.engine.Execute(ctx, loaders.LoaderExecutionRequest{
		Runtime:     prepared.loader.Summary.Runtime,
		Script:      prepared.loader.Script,
		Trigger:     prepared.trigger,
		PayloadJSON: prepared.payloadJSON,
	}, host)

	writeCtx := context.WithoutCancel(ctx)
	host.cleanupCommandSessions(writeCtx)

	completedAt := time.Now().UTC()
	run.CompletedAt = completedAt
	run.DurationMs = completedAt.Sub(run.StartedAt).Milliseconds()
	if execErr != nil {
		run.Status = domain.LoaderRunStatusFailed
		run.Error = execErr.Error()
		_ = m.writeRunArtifact(run.ArtifactsDir, "error.txt", run.Error)
		_ = m.configDB.UpdateLoaderLastError(writeCtx, prepared.loader.Summary.ID, run.Error)
		_ = m.addLoaderEvent(writeCtx, prepared.loader.Summary.ID, run.ID, run.TriggerID, "loader.run.failed", "error", run.Error, nil, "", "", "")
	} else {
		run.Status = domain.LoaderRunStatusSucceeded
		run.ResultJSON = execution.ResultJSON
		if execution.ResultJSON != "" {
			_ = m.writeRunArtifact(run.ArtifactsDir, "result.json", execution.ResultJSON)
		}
		_ = m.configDB.UpdateLoaderLastError(writeCtx, prepared.loader.Summary.ID, "")
		_ = m.addLoaderEvent(writeCtx, prepared.loader.Summary.ID, run.ID, run.TriggerID, "loader.run.completed", "info", "loader run completed", map[string]any{"resultJson": execution.ResultJSON}, "", "", "")
	}
	if err := m.configDB.UpdateLoaderRun(writeCtx, run); err != nil {
		return domain.LoaderRunSummary{}, err
	}
	m.updateTriggerEventDelivery(writeCtx, run)
	m.notifyDashboard("loader_run_updated")
	if err := m.Refresh(writeCtx); err != nil {
		slog.Warn("failed to refresh loaders after run", "loader_id", prepared.loader.Summary.ID, "error", err)
	}
	return run, nil
}

func (e *LoaderRunExecutor) Abort(ctx context.Context, prepared preparedLoaderRun, reason string) {
	m := e.manager
	if prepared.run.Status == domain.LoaderRunStatusSkipped {
		return
	}
	defer m.leaveRun(prepared.loader.Summary.ID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "loader run aborted before execution"
	}
	run := prepared.run
	completedAt := time.Now().UTC()
	run.Status = domain.LoaderRunStatusFailed
	run.CompletedAt = completedAt
	run.DurationMs = completedAt.Sub(run.StartedAt).Milliseconds()
	run.Error = reason
	_ = m.writeRunArtifact(run.ArtifactsDir, "error.txt", run.Error)
	_ = m.configDB.UpdateLoaderLastError(ctx, prepared.loader.Summary.ID, run.Error)
	_ = m.addLoaderEvent(ctx, prepared.loader.Summary.ID, run.ID, run.TriggerID, "loader.run.failed", "error", run.Error, nil, "", "", "")
	if err := m.configDB.UpdateLoaderRun(ctx, run); err != nil {
		slog.Warn("failed to abort prepared loader run", "loader_id", prepared.loader.Summary.ID, "run_id", run.ID, "error", err)
	}
	m.updateTriggerEventDelivery(ctx, run)
	m.notifyDashboard("loader_run_updated")
}

func (m *LoaderManager) runExecutorComponent() *LoaderRunExecutor {
	m.initLoaderComponents()
	return m.runExecutor
}

func (m *LoaderManager) runLoader(ctx context.Context, loader Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, automatic bool, options loaderRunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error) {
	return m.runExecutorComponent().Run(ctx, loader, trigger, payloadJSON, source, options, triggerEventAck...)
}

func (m *LoaderManager) prepareLoaderRun(ctx context.Context, loader Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options loaderRunOptions) (preparedLoaderRun, error) {
	return m.runExecutorComponent().Prepare(ctx, loader, trigger, payloadJSON, source, options)
}

func (m *LoaderManager) executePreparedLoaderRun(ctx context.Context, prepared preparedLoaderRun) (domain.LoaderRunSummary, error) {
	return m.runExecutorComponent().Execute(ctx, prepared)
}

func (m *LoaderManager) abortPreparedLoaderRun(ctx context.Context, prepared preparedLoaderRun, reason string) {
	m.runExecutorComponent().Abort(ctx, prepared, reason)
}
