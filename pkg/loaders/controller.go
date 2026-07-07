package loaders

import (
	"agent-compose/pkg/events/webhooks"
	domain "agent-compose/pkg/model"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ControllerStore interface {
	RunStore
	TriggerLoopStore
	EventDeliveryStore

	ListSchedulerExecutions(ctx context.Context) ([]domain.Loader, error)
	GetSchedulerExecution(ctx context.Context, executionID string) (domain.Loader, error)
	CreateSchedulerExecution(ctx context.Context, item domain.Loader) (domain.Loader, error)
	UpdateSchedulerExecution(ctx context.Context, item domain.Loader) (domain.Loader, error)
	DeleteSchedulerExecution(ctx context.Context, executionID string) error
	ReplaceSchedulerExecutionTriggers(ctx context.Context, executionID string, triggers []domain.LoaderTrigger) ([]domain.LoaderTrigger, error)
	SetSchedulerExecutionEnabled(ctx context.Context, executionID string, enabled bool) error
	SetSchedulerExecutionTriggerEnabled(ctx context.Context, executionID, triggerID string, enabled bool) error
	AddSchedulerExecutionEvent(ctx context.Context, event domain.LoaderEvent) error
}

type ControllerNotifier interface {
	Notify(reason string)
}

type ControllerPublisher interface {
	Publish(event domain.LoaderTopicEvent) bool
}

type ControllerArtifacts interface {
	RunDir(loaderID, runID string) string
	Write(dir, name, content string) error
}

type FSArtifacts struct {
	DataRoot string
}

func (a FSArtifacts) RunDir(loaderID, runID string) string {
	parts := []string{a.DataRoot, "loaders", strings.TrimSpace(loaderID), "runs"}
	if strings.TrimSpace(runID) != "" {
		parts = append(parts, strings.TrimSpace(runID))
	}
	return filepath.Join(parts...)
}

func (a FSArtifacts) Write(dir, name, content string) error {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(content) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(strings.TrimSpace(content)+"\n"), 0o644)
}

type ControllerDependencies struct {
	RootCtx      context.Context
	Store        ControllerStore
	Engine       LoaderEngine
	HostFactory  RunHostFactory
	Notifier     ControllerNotifier
	Publisher    ControllerPublisher
	Artifacts    ControllerArtifacts
	Wake         chan struct{}
	RunTimeout   func(time.Duration) time.Duration
	ReserveSlots func(event domain.LoaderTopicEvent, count int) ([]*webhooks.Reservation, bool)
	Loaders      map[string]domain.Loader
	Running      map[string]int
	Now          func() time.Time
	NewID        func() string
}

type Controller struct {
	deps ControllerDependencies

	startOnce       sync.Once
	mu              sync.RWMutex
	loaders         map[string]domain.Loader
	running         map[string]int
	runExecutor     *RunExecutor
	scheduler       *triggerLoop
	eventDispatcher *EventDispatcher
}

func NewController(deps ControllerDependencies) *Controller {
	if deps.RootCtx == nil {
		deps.RootCtx = context.Background()
	}
	if deps.Wake == nil {
		deps.Wake = make(chan struct{}, 1)
	}
	if deps.Loaders == nil {
		deps.Loaders = map[string]domain.Loader{}
	}
	if deps.Running == nil {
		deps.Running = map[string]int{}
	}
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}
	if deps.NewID == nil {
		deps.NewID = uuid.NewString
	}
	c := &Controller{
		deps:    deps,
		loaders: deps.Loaders,
		running: deps.Running,
	}
	c.init()
	return c
}

func (c *Controller) init() {
	if c.runExecutor == nil {
		c.runExecutor = NewRunExecutor(RunExecutorDependencies{
			Store:         c.deps.Store,
			Engine:        c.deps.Engine,
			HostFactory:   c.deps.HostFactory,
			ArtifactsDir:  c.RunArtifactsDir,
			WriteArtifact: c.WriteRunArtifact,
			EnterRun:      c.EnterRun,
			LeaveRun:      c.LeaveRun,
			AddLoaderEvent: func(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
				return c.AddLoaderEvent(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
			},
			UpdateTriggerEventDelivery: c.UpdateTriggerEventDelivery,
			Notify:                     c.notify,
			Refresh:                    c.Refresh,
		})
	}
	if c.scheduler == nil {
		c.scheduler = newTriggerLoop(TriggerLoopDependencies{
			RootCtx:       c.deps.RootCtx,
			Wake:          c.deps.Wake,
			Store:         c.deps.Store,
			Snapshot:      c.CachedLoadersMap,
			ReplaceCached: c.ReplaceCachedLoaders,
			Run: func(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options RunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error) {
				return c.Run(ctx, loader, trigger, payloadJSON, source, options, triggerEventAck...)
			},
			RunTimeout: c.runTimeout,
		})
	}
	if c.eventDispatcher == nil {
		c.eventDispatcher = NewEventDispatcher(EventDispatcherDependencies{
			RootCtx:      c.deps.RootCtx,
			Store:        c.deps.Store,
			Targets:      func(topic string) []EventTarget { return CollectEventTargets(c.SnapshotLoaders(), topic) },
			IsBusy:       c.AnyTargetBusy,
			ReserveSlots: c.deps.ReserveSlots,
			Run: func(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options RunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error) {
				return c.Run(ctx, loader, trigger, payloadJSON, source, options, triggerEventAck...)
			},
			Prepare:    c.Prepare,
			Execute:    c.Execute,
			Abort:      c.Abort,
			RunTimeout: c.runTimeout,
			EnterRun:   c.EnterRun,
			LeaveRun:   c.LeaveRun,
		})
	}
}

func (c *Controller) Start() {
	if c == nil {
		return
	}
	c.startOnce.Do(func() {
		if err := c.Refresh(c.deps.RootCtx); err != nil {
			slog.Warn("failed to refresh loaders on startup", "error", err)
		}
		go c.scheduler.Loop()
		go c.EventLoop()
	})
}

func (c *Controller) ScheduleLoop() {
	c.scheduler.Loop()
}

func (c *Controller) Refresh(ctx context.Context) error {
	items, err := c.deps.Store.ListSchedulerExecutions(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]domain.Loader, len(items))
	for _, item := range items {
		next[item.Summary.ID] = CloneLoader(item)
	}
	c.mu.Lock()
	clear(c.loaders)
	for id, item := range next {
		c.loaders[id] = item
	}
	c.mu.Unlock()
	c.WakeScheduler()
	return nil
}

func (c *Controller) Validate(ctx context.Context, runtime, script string) (LoaderValidationResult, error) {
	return c.deps.Engine.Validate(ctx, runtime, script)
}

func (c *Controller) CreateLoader(ctx context.Context, loader domain.Loader) (domain.Loader, error) {
	if strings.TrimSpace(loader.Summary.Runtime) == "" {
		loader.Summary.Runtime = domain.LoaderRuntimeScheduler
	}
	if strings.TrimSpace(loader.Script) == "" {
		loader.Script = domain.DefaultLoaderScript()
	}
	validation, err := c.deps.Engine.Validate(ctx, loader.Summary.Runtime, loader.Script)
	if err != nil {
		return domain.Loader{}, err
	}
	created, err := c.deps.Store.CreateSchedulerExecution(ctx, loader)
	if err != nil {
		return domain.Loader{}, err
	}
	if _, err := c.deps.Store.ReplaceSchedulerExecutionTriggers(ctx, created.Summary.ID, validation.Triggers); err != nil {
		_ = c.deps.Store.DeleteSchedulerExecution(ctx, created.Summary.ID)
		return domain.Loader{}, err
	}
	if err := c.Refresh(ctx); err != nil {
		return domain.Loader{}, err
	}
	c.notify("loader_updated")
	return c.deps.Store.GetSchedulerExecution(ctx, created.Summary.ID)
}

func (c *Controller) UpdateLoader(ctx context.Context, loader domain.Loader) (domain.Loader, error) {
	validation, err := c.deps.Engine.Validate(ctx, loader.Summary.Runtime, loader.Script)
	if err != nil {
		return domain.Loader{}, err
	}
	updated, err := c.deps.Store.UpdateSchedulerExecution(ctx, loader)
	if err != nil {
		return domain.Loader{}, err
	}
	if _, err := c.deps.Store.ReplaceSchedulerExecutionTriggers(ctx, updated.Summary.ID, validation.Triggers); err != nil {
		return domain.Loader{}, err
	}
	if err := c.Refresh(ctx); err != nil {
		return domain.Loader{}, err
	}
	c.notify("loader_updated")
	return c.deps.Store.GetSchedulerExecution(ctx, updated.Summary.ID)
}

func (c *Controller) DeleteLoader(ctx context.Context, loaderID string) error {
	if err := c.deps.Store.DeleteSchedulerExecution(ctx, loaderID); err != nil {
		return err
	}
	if err := c.Refresh(ctx); err != nil {
		return err
	}
	c.notify("loader_updated")
	return nil
}

func (c *Controller) SetLoaderEnabled(ctx context.Context, loaderID string, enabled bool) (domain.Loader, error) {
	if err := c.deps.Store.SetSchedulerExecutionEnabled(ctx, loaderID, enabled); err != nil {
		return domain.Loader{}, err
	}
	if err := c.Refresh(ctx); err != nil {
		return domain.Loader{}, err
	}
	c.notify("loader_updated")
	return c.deps.Store.GetSchedulerExecution(ctx, loaderID)
}

func (c *Controller) SetLoaderTriggerEnabled(ctx context.Context, loaderID, triggerID string, enabled bool) (domain.Loader, error) {
	if err := c.deps.Store.SetSchedulerExecutionTriggerEnabled(ctx, loaderID, triggerID, enabled); err != nil {
		return domain.Loader{}, err
	}
	if err := c.Refresh(ctx); err != nil {
		return domain.Loader{}, err
	}
	c.notify("loader_updated")
	return c.deps.Store.GetSchedulerExecution(ctx, loaderID)
}

func (c *Controller) RunNow(ctx context.Context, loaderID, triggerID, payloadJSON string, timeout time.Duration) (domain.LoaderRunSummary, error) {
	loader, trigger, err := c.LoadLoaderForRun(ctx, loaderID, triggerID)
	if err != nil {
		return domain.LoaderRunSummary{}, err
	}
	runCtx, cancel := context.WithTimeout(c.deps.RootCtx, c.runTimeout(timeout))
	defer cancel()
	return c.Run(runCtx, loader, trigger, payloadJSON, "manual", RunOptions{})
}

func (c *Controller) Run(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options RunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error) {
	return c.runExecutor.Run(ctx, loader, trigger, payloadJSON, source, options, triggerEventAck...)
}

func (c *Controller) Prepare(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options RunOptions) (PreparedRun, error) {
	return c.runExecutor.Prepare(ctx, loader, trigger, payloadJSON, source, options)
}

func (c *Controller) Execute(ctx context.Context, prepared PreparedRun) (domain.LoaderRunSummary, error) {
	return c.runExecutor.Execute(ctx, prepared)
}

func (c *Controller) Abort(ctx context.Context, prepared PreparedRun, reason string) {
	c.runExecutor.Abort(ctx, prepared, reason)
}

func (c *Controller) Publish(topic string, payload map[string]any) {
	if c.deps.Publisher == nil {
		return
	}
	_ = c.deps.Publisher.Publish(domain.LoaderTopicEvent{
		Topic:     strings.TrimSpace(topic),
		Payload:   payload,
		CreatedAt: c.now(),
	})
}

func (c *Controller) EventLoop() {
	bus, ok := c.deps.Publisher.(interface {
		Events() <-chan domain.LoaderTopicEvent
	})
	if !ok || bus == nil {
		return
	}
	for {
		select {
		case <-c.deps.RootCtx.Done():
			return
		case event, ok := <-bus.Events():
			if !ok {
				return
			}
			c.DispatchEvent(event)
		}
	}
}

func (c *Controller) DispatchEvent(event domain.LoaderTopicEvent) {
	c.eventDispatcher.Dispatch(event)
}

func (c *Controller) CollectDueScheduledRuns(now time.Time) []ScheduledRun {
	return c.scheduler.CollectDue(now)
}

func (c *Controller) DispatchScheduledRuns(jobs []ScheduledRun) {
	c.scheduler.Dispatch(jobs)
}

func (c *Controller) NextScheduledFireAt() (time.Time, bool) {
	return c.scheduler.NextFireAt()
}

func (c *Controller) WakeScheduler() {
	if c == nil || c.deps.Wake == nil {
		return
	}
	select {
	case c.deps.Wake <- struct{}{}:
	default:
	}
}

func (c *Controller) CachedLoadersMap() map[string]domain.Loader {
	c.mu.RLock()
	defer c.mu.RUnlock()
	items := make(map[string]domain.Loader, len(c.loaders))
	for id, item := range c.loaders {
		items[id] = CloneLoader(item)
	}
	return items
}

func (c *Controller) ReplaceCachedLoaders(updatedLoaders map[string]domain.Loader) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, item := range updatedLoaders {
		c.loaders[id] = CloneLoader(item)
	}
}

func (c *Controller) SnapshotLoaders() []domain.Loader {
	c.mu.RLock()
	defer c.mu.RUnlock()
	items := make([]domain.Loader, 0, len(c.loaders))
	for _, item := range c.loaders {
		items = append(items, CloneLoader(item))
	}
	return items
}

func (c *Controller) LoadLoaderForRun(ctx context.Context, loaderID, triggerID string) (domain.Loader, *domain.LoaderTrigger, error) {
	loader, err := c.deps.Store.GetSchedulerExecution(ctx, loaderID)
	if err != nil {
		return domain.Loader{}, nil, err
	}
	if strings.TrimSpace(triggerID) == "" {
		return loader, nil, nil
	}
	triggerID = strings.TrimSpace(triggerID)
	for _, item := range loader.Triggers {
		if item.ID == triggerID {
			current := item
			return loader, &current, nil
		}
	}
	id := strings.TrimSpace(loaderID) + "/" + triggerID
	return domain.Loader{}, nil, domain.ResourceError(domain.ErrNotFound, "loader trigger", id, fmt.Sprintf("loader trigger %s not found", id), nil)
}

func (c *Controller) UpdateTriggerEventDelivery(ctx context.Context, run domain.LoaderRunSummary) {
	if c == nil || c.deps.Store == nil {
		return
	}
	metadata := ParseTriggerEventMetadata(run.PayloadJSON)
	if metadata.EventID == "" || run.LoaderID == "" || run.TriggerID == "" {
		return
	}
	status := domain.EventDeliveryStatusRunStarted
	errText := ""
	switch run.Status {
	case domain.LoaderRunStatusSucceeded:
		status = domain.EventDeliveryStatusRunSucceeded
	case domain.LoaderRunStatusFailed:
		status = domain.EventDeliveryStatusRunFailed
		errText = run.Error
	case domain.LoaderRunStatusSkipped:
		status = domain.EventDeliveryStatusSkipped
		errText = run.Error
	}
	if err := c.deps.Store.UpsertEventDelivery(ctx, domain.EventDelivery{
		EventID:   metadata.EventID,
		LoaderID:  run.LoaderID,
		TriggerID: run.TriggerID,
		RunID:     run.ID,
		Status:    status,
		Error:     errText,
	}); err != nil {
		slog.Warn("failed to update event delivery", "event_id", metadata.EventID, "loader_id", run.LoaderID, "trigger_id", run.TriggerID, "run_id", run.ID, "error", err)
	}
}

func (c *Controller) EnterRun(loader domain.Loader) bool {
	loaderID := strings.TrimSpace(loader.Summary.ID)
	policy := domain.NormalizeLoaderConcurrencyPolicy(loader.Summary.ConcurrencyPolicy)
	c.mu.Lock()
	defer c.mu.Unlock()
	if policy != domain.LoaderConcurrencyPolicyParallel && c.running[loaderID] > 0 {
		return false
	}
	c.running[loaderID]++
	return true
}

func (c *Controller) LeaveRun(loaderID string) {
	loaderID = strings.TrimSpace(loaderID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[loaderID] <= 1 {
		delete(c.running, loaderID)
		return
	}
	c.running[loaderID]--
}

func (c *Controller) AnyTargetBusy(targets []EventTarget) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return AnyTargetBusy(targets, c.running)
}

func (c *Controller) AddLoaderEvent(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	_, err := c.AddLoaderEventRecord(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
	return err
}

func (c *Controller) AddLoaderEventRecord(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) (domain.LoaderEvent, error) {
	payloadJSON, err := domain.MarshalJSONCompact(payload)
	if err != nil {
		return domain.LoaderEvent{}, err
	}
	event := domain.LoaderEvent{
		ID:                   c.newID(),
		LoaderID:             strings.TrimSpace(loaderID),
		RunID:                strings.TrimSpace(runID),
		TriggerID:            strings.TrimSpace(triggerID),
		Type:                 strings.TrimSpace(eventType),
		Level:                firstNonEmpty(strings.TrimSpace(level), "info"),
		Message:              strings.TrimSpace(message),
		PayloadJSON:          payloadJSON,
		LinkedSessionID:      strings.TrimSpace(linkedSessionID),
		LinkedCellID:         strings.TrimSpace(linkedCellID),
		LinkedAgentSessionID: strings.TrimSpace(linkedAgentSessionID),
		CreatedAt:            c.now(),
	}
	if err := c.deps.Store.AddSchedulerExecutionEvent(ctx, event); err != nil {
		return domain.LoaderEvent{}, err
	}
	return event, nil
}

func (c *Controller) RunArtifactsDir(loaderID, runID string) string {
	if c.deps.Artifacts == nil {
		return ""
	}
	return c.deps.Artifacts.RunDir(loaderID, runID)
}

func (c *Controller) WriteRunArtifact(dir, name, content string) error {
	if c.deps.Artifacts == nil {
		return nil
	}
	return c.deps.Artifacts.Write(dir, name, content)
}

func (c *Controller) notify(reason string) {
	if c.deps.Notifier != nil {
		c.deps.Notifier.Notify(reason)
	}
}

func (c *Controller) runTimeout(override time.Duration) time.Duration {
	if c.deps.RunTimeout != nil {
		return c.deps.RunTimeout(override)
	}
	if override > 0 {
		return override
	}
	return 20 * time.Minute
}

func (c *Controller) now() time.Time {
	if c.deps.Now == nil {
		return time.Now().UTC()
	}
	return c.deps.Now().UTC()
}

func (c *Controller) newID() string {
	if c.deps.NewID == nil {
		return uuid.NewString()
	}
	return c.deps.NewID()
}
