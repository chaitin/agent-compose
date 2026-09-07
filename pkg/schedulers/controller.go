package schedulers

import (
	"context"
	"fmt"
	"github.com/chaitin/agent-compose/pkg/events/webhooks"
	domain "github.com/chaitin/agent-compose/pkg/model"
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
	SchedulerStore
	EventDeliveryStore

	ListSchedulers(ctx context.Context) ([]domain.Scheduler, error)
	GetScheduler(ctx context.Context, schedulerID string) (domain.Scheduler, error)
	ReplaceSchedulerTriggers(ctx context.Context, schedulerID string, triggers []domain.SchedulerTrigger) ([]domain.SchedulerTrigger, error)
	SetSchedulerTriggerNextFireAt(ctx context.Context, schedulerID, triggerID string, nextFireAt time.Time) error
	SetSchedulerEnabled(ctx context.Context, schedulerID string, enabled bool) error
	SetSchedulerTriggerEnabled(ctx context.Context, schedulerID, triggerID string, enabled bool) error
	AddSchedulerEvent(ctx context.Context, event domain.SchedulerEvent) error
}

type ControllerNotifier interface {
	Notify(reason string)
}

type ControllerPublisher interface {
	Publish(event domain.SchedulerTopicEvent) bool
}

type ControllerArtifacts interface {
	RunDir(schedulerID, runID string) string
	Write(dir, name, content string) error
}

type FSArtifacts struct {
	DataRoot string
}

func (a FSArtifacts) RunDir(schedulerID, runID string) string {
	parts := []string{a.DataRoot, "schedulers", strings.TrimSpace(schedulerID), "runs"}
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
	Engine       SchedulerEngine
	HostFactory  RunHostFactory
	Notifier     ControllerNotifier
	Publisher    ControllerPublisher
	Artifacts    ControllerArtifacts
	Wake         chan struct{}
	RunTimeout   func(time.Duration) time.Duration
	ReserveSlots func(event domain.SchedulerTopicEvent, count int) ([]*webhooks.Reservation, bool)
	Schedulers   map[string]domain.Scheduler
	Running      map[string]int
	Now          func() time.Time
	NewID        func() string
}

type Controller struct {
	deps ControllerDependencies

	startOnce       sync.Once
	mu              sync.RWMutex
	schedulers      map[string]domain.Scheduler
	running         map[string]int
	runExecutor     *RunExecutor
	invocations     *InvocationExecutor
	schedulerRuns   *SchedulerRunSupervisor
	scheduler       *Scheduler
	eventDispatcher *EventDispatcher
}

// SchedulerRuns returns the capability that owns scheduler-run lifecycle and state.
func (c *Controller) SchedulerRuns() *SchedulerRunSupervisor {
	return c.schedulerRuns
}

func NewController(deps ControllerDependencies) *Controller {
	if deps.RootCtx == nil {
		deps.RootCtx = context.Background()
	}
	if deps.Wake == nil {
		deps.Wake = make(chan struct{}, 1)
	}
	if deps.Schedulers == nil {
		deps.Schedulers = map[string]domain.Scheduler{}
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
		deps:       deps,
		schedulers: deps.Schedulers,
		running:    deps.Running,
	}
	c.init()
	return c
}

func (c *Controller) init() {
	if c.runExecutor == nil {
		c.runExecutor = NewRunExecutor(RunExecutorDependencies{
			Store:                      c.deps.Store,
			Engine:                     c.deps.Engine,
			HostFactory:                c.deps.HostFactory,
			ArtifactsDir:               c.RunArtifactsDir,
			WriteArtifact:              c.WriteRunArtifact,
			EnterRun:                   c.EnterRun,
			LeaveRun:                   c.LeaveRun,
			AddSchedulerEvent:          c.AddSchedulerEvent,
			UpdateTriggerEventDelivery: c.UpdateTriggerEventDelivery,
			Notify:                     c.notify,
			Refresh:                    c.Refresh,
		})
	}
	if c.invocations == nil {
		c.invocations = NewInvocationExecutor(InvocationExecutorDependencies{
			Engine:      c.deps.Engine,
			HostFactory: c.deps.HostFactory,
			EnterRun:    c.EnterRun,
			LeaveRun:    c.LeaveRun,
			NewID:       c.deps.NewID,
		})
	}
	if c.schedulerRuns == nil {
		runStore, _ := c.deps.Store.(schedulerRunStore)
		c.schedulerRuns = newSchedulerRunSupervisor(schedulerRunSupervisorDependencies{
			RootCtx:             c.deps.RootCtx,
			Store:               runStore,
			LoadSchedulerForRun: c.LoadSchedulerForRun,
			Prepare:             c.Prepare,
			Execute:             c.Execute,
			RunTimeout:          c.runTimeout,
		})
	}
	if c.scheduler == nil {
		c.scheduler = NewScheduler(SchedulerDependencies{
			RootCtx:       c.deps.RootCtx,
			Wake:          c.deps.Wake,
			Store:         c.deps.Store,
			Snapshot:      c.CachedSchedulersMap,
			ReplaceCached: c.ReplaceCachedSchedulers,
			Run:           c.schedulerRuns.runTrigger,
			RunTimeout:    c.runTimeout,
		})
	}
	if c.eventDispatcher == nil {
		c.eventDispatcher = NewEventDispatcher(EventDispatcherDependencies{
			RootCtx:      c.deps.RootCtx,
			Store:        c.deps.Store,
			Targets:      func(topic string) []EventTarget { return CollectEventTargets(c.SnapshotSchedulers(), topic) },
			IsBusy:       c.AnyTargetBusy,
			ReserveSlots: c.deps.ReserveSlots,
			Run:          c.schedulerRuns.runTrigger,
			Prepare:      c.Prepare,
			Execute:      c.schedulerRuns.runPrepared,
			Abort:        c.Abort,
			RunTimeout:   c.runTimeout,
			EnterRun:     c.EnterRun,
			LeaveRun:     c.LeaveRun,
		})
	}
}

func (c *Controller) Start() {
	if c == nil {
		return
	}
	c.startOnce.Do(func() {
		if err := c.Refresh(c.deps.RootCtx); err != nil {
			slog.Warn("failed to refresh schedulers on startup", "error", err)
		}
		go c.scheduler.Loop()
		go c.EventLoop()
	})
}

func (c *Controller) ScheduleLoop() {
	c.scheduler.Loop()
}

func (c *Controller) Refresh(ctx context.Context) error {
	items, err := c.deps.Store.ListSchedulers(ctx)
	if err != nil {
		return err
	}
	if err := c.initializeCronSchedules(ctx, items); err != nil {
		return err
	}
	next := make(map[string]domain.Scheduler, len(items))
	for _, item := range items {
		next[item.Summary.ID] = CloneScheduler(item)
	}
	c.mu.Lock()
	clear(c.schedulers)
	for id, item := range next {
		c.schedulers[id] = item
	}
	c.mu.Unlock()
	c.WakeScheduler()
	return nil
}

func (c *Controller) Validate(ctx context.Context, runtime, script string) (SchedulerValidationResult, error) {
	return c.deps.Engine.Validate(ctx, runtime, script)
}

func (c *Controller) SetSchedulerEnabled(ctx context.Context, schedulerID string, enabled bool) (domain.Scheduler, error) {
	if err := c.deps.Store.SetSchedulerEnabled(ctx, schedulerID, enabled); err != nil {
		return domain.Scheduler{}, err
	}
	if err := c.Refresh(ctx); err != nil {
		return domain.Scheduler{}, err
	}
	c.notify("scheduler_updated")
	return c.deps.Store.GetScheduler(ctx, schedulerID)
}

func (c *Controller) SetSchedulerTriggerEnabled(ctx context.Context, schedulerID, triggerID string, enabled bool) (domain.Scheduler, error) {
	if err := c.deps.Store.SetSchedulerTriggerEnabled(ctx, schedulerID, triggerID, enabled); err != nil {
		return domain.Scheduler{}, err
	}
	if err := c.Refresh(ctx); err != nil {
		return domain.Scheduler{}, err
	}
	c.notify("scheduler_updated")
	return c.deps.Store.GetScheduler(ctx, schedulerID)
}

// RunNowRequest describes a manually triggered scheduler run.
type RunNowRequest struct {
	SchedulerID string
	TriggerID   string
	PayloadJSON string
	Timeout     time.Duration
}

func (c *Controller) RunNow(ctx context.Context, req RunNowRequest) (domain.SchedulerRunSummary, error) {
	scheduler, trigger, err := c.LoadSchedulerForRun(ctx, req.SchedulerID, req.TriggerID)
	if err != nil {
		return domain.SchedulerRunSummary{}, err
	}
	runCtx, cancel := context.WithTimeout(c.deps.RootCtx, effectiveSchedulerRunTimeout(scheduler, req.Timeout, c.deps.RunTimeout))
	defer cancel()
	return c.Run(runCtx, RunTriggerRequest{Scheduler: scheduler, Trigger: trigger, PayloadJSON: req.PayloadJSON, Source: "manual"})
}

func (c *Controller) Run(ctx context.Context, req RunTriggerRequest, triggerEventAck ...func(context.Context) error) (domain.SchedulerRunSummary, error) {
	return c.schedulerRuns.runTrigger(ctx, req, triggerEventAck...)
}

func (c *Controller) Prepare(ctx context.Context, req RunTriggerRequest) (PreparedRun, error) {
	return c.runExecutor.Prepare(ctx, req)
}

func (c *Controller) Execute(ctx context.Context, prepared PreparedRun) (domain.SchedulerRunSummary, error) {
	return c.runExecutor.Execute(ctx, prepared)
}

func (c *Controller) Abort(ctx context.Context, prepared PreparedRun, reason string) {
	c.runExecutor.Abort(ctx, prepared, reason)
}

func (c *Controller) Publish(topic string, payload map[string]any) {
	if c.deps.Publisher == nil {
		return
	}
	_ = c.deps.Publisher.Publish(domain.SchedulerTopicEvent{
		Topic:     strings.TrimSpace(topic),
		Payload:   payload,
		CreatedAt: c.now(),
	})
}

func (c *Controller) EventLoop() {
	bus, ok := c.deps.Publisher.(interface {
		Events() <-chan domain.SchedulerTopicEvent
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

func (c *Controller) DispatchEvent(event domain.SchedulerTopicEvent) {
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

func (c *Controller) CachedSchedulersMap() map[string]domain.Scheduler {
	c.mu.RLock()
	defer c.mu.RUnlock()
	items := make(map[string]domain.Scheduler, len(c.schedulers))
	for id, item := range c.schedulers {
		items[id] = CloneScheduler(item)
	}
	return items
}

func (c *Controller) ReplaceCachedSchedulers(updatedSchedulers map[string]domain.Scheduler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, item := range updatedSchedulers {
		c.schedulers[id] = CloneScheduler(item)
	}
}

func (c *Controller) SnapshotSchedulers() []domain.Scheduler {
	c.mu.RLock()
	defer c.mu.RUnlock()
	items := make([]domain.Scheduler, 0, len(c.schedulers))
	for _, item := range c.schedulers {
		items = append(items, CloneScheduler(item))
	}
	return items
}

func (c *Controller) LoadSchedulerForRun(ctx context.Context, schedulerID, triggerID string) (domain.Scheduler, *domain.SchedulerTrigger, error) {
	scheduler, err := c.deps.Store.GetScheduler(ctx, schedulerID)
	if err != nil {
		return domain.Scheduler{}, nil, err
	}
	if strings.TrimSpace(triggerID) == "" {
		return scheduler, nil, nil
	}
	triggerID = strings.TrimSpace(triggerID)
	for _, item := range scheduler.Triggers {
		if item.ID == triggerID {
			current := item
			return scheduler, &current, nil
		}
	}
	id := strings.TrimSpace(schedulerID) + "/" + triggerID
	return domain.Scheduler{}, nil, domain.ResourceError(domain.ErrNotFound, "scheduler trigger", id, fmt.Sprintf("scheduler trigger %s not found", id), nil)
}

func (c *Controller) UpdateTriggerEventDelivery(ctx context.Context, run domain.SchedulerRunSummary) {
	if c == nil || c.deps.Store == nil {
		return
	}
	metadata := ParseTriggerEventMetadata(run.PayloadJSON)
	if metadata.EventID == "" || run.SchedulerID == "" || run.TriggerID == "" {
		return
	}
	status := domain.EventDeliveryStatusRunStarted
	errText := ""
	switch run.Status {
	case domain.SchedulerRunStatusSucceeded:
		status = domain.EventDeliveryStatusRunSucceeded
	case domain.SchedulerRunStatusFailed:
		status = domain.EventDeliveryStatusRunFailed
		errText = run.Error
	case domain.SchedulerRunStatusCanceled:
		status = domain.EventDeliveryStatusRunFailed
		errText = run.Error
	case domain.SchedulerRunStatusSkipped:
		status = domain.EventDeliveryStatusSkipped
		errText = run.Error
	}
	if err := c.deps.Store.UpsertEventDelivery(ctx, domain.EventDelivery{
		EventID:     metadata.EventID,
		SchedulerID: run.SchedulerID,
		TriggerID:   run.TriggerID,
		RunID:       run.ID,
		Status:      status,
		Error:       errText,
	}); err != nil {
		slog.Warn("failed to update event delivery", "event_id", metadata.EventID, "scheduler_id", run.SchedulerID, "trigger_id", run.TriggerID, "run_id", run.ID, "error", err)
	}
}

func (c *Controller) EnterRun(scheduler domain.Scheduler) bool {
	schedulerID := strings.TrimSpace(scheduler.Summary.ID)
	policy := NormalizeConcurrencyPolicy(scheduler.Summary.ConcurrencyPolicy)
	c.mu.Lock()
	defer c.mu.Unlock()
	if policy != domain.SchedulerConcurrencyPolicyParallel && c.running[schedulerID] > 0 {
		return false
	}
	c.running[schedulerID]++
	return true
}

func (c *Controller) LeaveRun(schedulerID string) {
	schedulerID = strings.TrimSpace(schedulerID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[schedulerID] <= 1 {
		delete(c.running, schedulerID)
		return
	}
	c.running[schedulerID]--
}

func (c *Controller) AnyTargetBusy(targets []EventTarget) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return AnyTargetBusy(targets, c.running)
}

func (c *Controller) AddSchedulerEvent(ctx context.Context, event SchedulerEventInput) error {
	_, err := c.AddSchedulerEventRecord(ctx, event)
	return err
}

func (c *Controller) AddSchedulerEventRecord(ctx context.Context, input SchedulerEventInput) (domain.SchedulerEvent, error) {
	payloadJSON, err := domain.MarshalJSONCompact(input.Payload)
	if err != nil {
		return domain.SchedulerEvent{}, err
	}
	event := domain.SchedulerEvent{
		ID:                  c.newID(),
		SchedulerID:         strings.TrimSpace(input.SchedulerID),
		RunID:               strings.TrimSpace(input.RunID),
		TriggerID:           strings.TrimSpace(input.TriggerID),
		Type:                strings.TrimSpace(input.EventType),
		Level:               firstNonEmpty(strings.TrimSpace(input.Level), "info"),
		Message:             strings.TrimSpace(input.Message),
		PayloadJSON:         payloadJSON,
		LinkedSandboxID:     strings.TrimSpace(input.LinkedSandboxID),
		LinkedCellID:        strings.TrimSpace(input.LinkedCellID),
		LinkedAgentThreadID: strings.TrimSpace(input.LinkedAgentThreadID),
		CreatedAt:           c.now(),
	}
	if err := c.deps.Store.AddSchedulerEvent(ctx, event); err != nil {
		return domain.SchedulerEvent{}, err
	}
	return event, nil
}

func (c *Controller) RunArtifactsDir(schedulerID, runID string) string {
	if c.deps.Artifacts == nil {
		return ""
	}
	return c.deps.Artifacts.RunDir(schedulerID, runID)
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
