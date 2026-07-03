package agentcompose

import (
	"context"
	"time"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

type LoaderRunExecutor struct {
	manager *LoaderManager
	core    *loaders.RunExecutor
}

func NewLoaderRunExecutor(manager *LoaderManager) *LoaderRunExecutor {
	executor := &LoaderRunExecutor{manager: manager}
	executor.core = loaders.NewRunExecutor(loaders.RunExecutorDependencies{
		Store:         manager.configDB,
		Engine:        manager.engine,
		ArtifactsDir:  manager.runArtifactsDir,
		WriteArtifact: manager.writeRunArtifact,
		EnterRun:      manager.enterRun,
		LeaveRun:      manager.leaveRun,
		AddLoaderEvent: func(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
			return manager.addLoaderEvent(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
		},
		UpdateTriggerEventDelivery: manager.updateTriggerEventDelivery,
		Notify:                     manager.notifyDashboard,
		Refresh:                    manager.Refresh,
		HostFactory: func(loader domain.Loader, run *domain.LoaderRunSummary, triggerEvent loaders.TriggerEventMetadata) loaders.RunHost {
			return manager.newLoaderRuntimeHost(loader, run, triggerEvent)
		},
	})
	return executor
}

func (e *LoaderRunExecutor) Run(ctx context.Context, loader Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options loaderRunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error) {
	return e.core.Run(ctx, loader, trigger, payloadJSON, source, loaders.RunOptions{
		RetryWhenBusy:  options.retryWhenBusy,
		AlreadyEntered: options.alreadyEntered,
	}, triggerEventAck...)
}

func (e *LoaderRunExecutor) Prepare(ctx context.Context, loader Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options loaderRunOptions) (preparedLoaderRun, error) {
	prepared, err := e.core.Prepare(ctx, loader, trigger, payloadJSON, source, loaders.RunOptions{
		RetryWhenBusy:  options.retryWhenBusy,
		AlreadyEntered: options.alreadyEntered,
	})
	return preparedLoaderRunFromCore(prepared), err
}

func (e *LoaderRunExecutor) Execute(ctx context.Context, prepared preparedLoaderRun) (domain.LoaderRunSummary, error) {
	return e.core.Execute(ctx, prepared.toCore())
}

func (e *LoaderRunExecutor) Abort(ctx context.Context, prepared preparedLoaderRun, reason string) {
	e.core.Abort(ctx, prepared.toCore(), reason)
}

func (m *LoaderManager) runExecutorComponent() *LoaderRunExecutor {
	m.initLoaderComponents()
	return m.runExecutor
}

func (m *LoaderManager) runLoader(ctx context.Context, loader Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, automatic bool, options loaderRunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error) {
	_ = automatic
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

func (h *loaderRunHost) CleanupCommandSessions(ctx context.Context) {
	h.cleanupCommandSessions(ctx)
}

func (m *LoaderManager) loaderRunTimeout(override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if m != nil && m.config != nil && m.config.LoaderRunTimeout > 0 {
		return m.config.LoaderRunTimeout
	}
	return 20 * time.Minute
}
