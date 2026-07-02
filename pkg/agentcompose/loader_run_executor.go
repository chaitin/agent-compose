package agentcompose

import (
	loaderdomain "agent-compose/internal/agentcompose/loader"
	"context"
)

type LoaderRunExecutor struct {
	inner *loaderdomain.LoaderRunExecutor
}

func NewLoaderRunExecutor(manager *LoaderManager) *LoaderRunExecutor {
	return &LoaderRunExecutor{inner: loaderdomain.NewLoaderRunExecutor(manager)}
}

func (e *LoaderRunExecutor) Run(ctx context.Context, loader Loader, trigger *LoaderTrigger, payloadJSON, source string, options loaderRunOptions, triggerEventAck ...func(context.Context) error) (LoaderRunSummary, error) {
	return e.inner.Run(ctx, loader, trigger, payloadJSON, source, toDomainRunOptions(options), triggerEventAck...)
}

func (e *LoaderRunExecutor) Prepare(ctx context.Context, loader Loader, trigger *LoaderTrigger, payloadJSON, source string, options loaderRunOptions) (preparedLoaderRun, error) {
	prepared, err := e.inner.Prepare(ctx, loader, trigger, payloadJSON, source, toDomainRunOptions(options))
	if err != nil {
		return preparedLoaderRun{}, err
	}
	return fromDomainPreparedRun(prepared), nil
}

func (e *LoaderRunExecutor) Execute(ctx context.Context, prepared preparedLoaderRun) (LoaderRunSummary, error) {
	return e.inner.Execute(ctx, toDomainPreparedRun(prepared))
}

func (e *LoaderRunExecutor) Abort(ctx context.Context, prepared preparedLoaderRun, reason string) {
	e.inner.Abort(ctx, toDomainPreparedRun(prepared), reason)
}

func toDomainRunOptions(options loaderRunOptions) loaderdomain.RunOptions {
	return loaderdomain.RunOptions{
		RetryWhenBusy:  options.retryWhenBusy,
		AlreadyEntered: options.alreadyEntered,
	}
}

func toDomainPreparedRun(prepared preparedLoaderRun) loaderdomain.PreparedRun {
	return loaderdomain.PreparedRun{
		Loader:      prepared.loader,
		Trigger:     prepared.trigger,
		Run:         prepared.run,
		PayloadJSON: prepared.payloadJSON,
	}
}

func fromDomainPreparedRun(prepared loaderdomain.PreparedRun) preparedLoaderRun {
	return preparedLoaderRun{
		loader:      prepared.Loader,
		trigger:     prepared.Trigger,
		run:         prepared.Run,
		payloadJSON: prepared.PayloadJSON,
	}
}

func (m *LoaderManager) runExecutorComponent() *LoaderRunExecutor {
	m.initLoaderComponents()
	return m.runExecutor
}

func (m *LoaderManager) runLoader(ctx context.Context, loader Loader, trigger *LoaderTrigger, payloadJSON, source string, automatic bool, options loaderRunOptions, triggerEventAck ...func(context.Context) error) (LoaderRunSummary, error) {
	return m.runExecutorComponent().Run(ctx, loader, trigger, payloadJSON, source, options, triggerEventAck...)
}

func (m *LoaderManager) prepareLoaderRun(ctx context.Context, loader Loader, trigger *LoaderTrigger, payloadJSON, source string, options loaderRunOptions) (preparedLoaderRun, error) {
	return m.runExecutorComponent().Prepare(ctx, loader, trigger, payloadJSON, source, options)
}

func (m *LoaderManager) executePreparedLoaderRun(ctx context.Context, prepared preparedLoaderRun) (LoaderRunSummary, error) {
	return m.runExecutorComponent().Execute(ctx, prepared)
}

func (m *LoaderManager) abortPreparedLoaderRun(ctx context.Context, prepared preparedLoaderRun, reason string) {
	m.runExecutorComponent().Abort(ctx, prepared, reason)
}

func (m *LoaderManager) EnterRun(loader Loader) bool {
	return m.enterRun(loader)
}

func (m *LoaderManager) LeaveRun(loaderID string) {
	m.leaveRun(loaderID)
}

func (m *LoaderManager) RunArtifactsDir(loaderID, runID string) string {
	return m.runArtifactsDir(loaderID, runID)
}

func (m *LoaderManager) WriteRunArtifact(dir, name, content string) error {
	return m.writeRunArtifact(dir, name, content)
}

func (m *LoaderManager) CreateLoaderRun(ctx context.Context, run LoaderRunSummary) error {
	return m.configDB.CreateLoaderRun(ctx, run)
}

func (m *LoaderManager) UpdateLoaderRun(ctx context.Context, run LoaderRunSummary) error {
	return m.configDB.UpdateLoaderRun(ctx, run)
}

func (m *LoaderManager) UpdateLoaderLastError(ctx context.Context, loaderID, errorText string) error {
	return m.configDB.UpdateLoaderLastError(ctx, loaderID, errorText)
}

func (m *LoaderManager) AddLoaderEvent(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	return m.addLoaderEvent(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
}

func (m *LoaderManager) UpdateTriggerEventDelivery(ctx context.Context, run LoaderRunSummary) {
	m.updateTriggerEventDelivery(ctx, run)
}

func (m *LoaderManager) NotifyDashboard(reason string) {
	m.notifyDashboard(reason)
}

func (m *LoaderManager) Engine() LoaderEngine {
	return m.engine
}

func (m *LoaderManager) NewRunHost(loader Loader, run *LoaderRunSummary, payloadJSON string) LoaderHost {
	return &loaderRunHost{manager: m, loader: loader, run: run, triggerEvent: parseLoaderTriggerEventMetadata(payloadJSON)}
}

func (m *LoaderManager) CleanupRunHost(ctx context.Context, host LoaderHost) {
	if typed, ok := host.(*loaderRunHost); ok {
		typed.cleanupCommandSessions(ctx)
	}
}
