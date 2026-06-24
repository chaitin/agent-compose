package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/samber/do/v2"
)

type LoaderManager struct {
	config     *appconfig.Config
	rootCtx    context.Context
	store      *Store
	configDB   *ConfigStore
	driver     Driver
	executor   *Executor
	images     ImageBackend
	llm        *LLMClient
	cap        CapabilityProvider
	bus        *LoaderBus
	streams    *SessionStreamBroker
	engine     LoaderEngine
	sessions   *SessionRPCBridge
	dashboard  *DashboardOverviewHub
	eventQueue *WebhookRunQueue

	once         sync.Once
	mu           sync.RWMutex
	loaders      map[string]Loader
	running      map[string]int
	scheduleWake chan struct{}
}

type loaderRunHost struct {
	manager                *LoaderManager
	loader                 Loader
	run                    *LoaderRunSummary
	triggerEvent           loaderTriggerEventMetadata
	commandSessionIDs      map[string]struct{}
	commandSessionIDOrder  []string
	commandSessionIDsMutex sync.Mutex
	commandReusableSession *Session
}

type scheduledLoaderRun struct {
	loader      Loader
	trigger     LoaderTrigger
	payloadJSON string
	source      string
}

type eventLoaderTarget struct {
	loader  Loader
	trigger LoaderTrigger
}

type preparedLoaderRun struct {
	loader      Loader
	trigger     *LoaderTrigger
	run         LoaderRunSummary
	payloadJSON string
}

func NewLoaderManager(di do.Injector) (*LoaderManager, error) {
	rootCtx := do.MustInvoke[context.Context](di)
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	config := do.MustInvoke[*appconfig.Config](di)
	eventQueue, err := newWebhookRunQueueFromConfig(config)
	if err != nil {
		return nil, err
	}
	dashboard, _ := do.Invoke[*DashboardOverviewHub](di)
	return &LoaderManager{
		config:       config,
		rootCtx:      rootCtx,
		store:        do.MustInvoke[*Store](di),
		configDB:     do.MustInvoke[*ConfigStore](di),
		driver:       do.MustInvoke[Driver](di),
		executor:     do.MustInvoke[*Executor](di),
		images:       NewDockerImageBackend(),
		llm:          do.MustInvoke[*LLMClient](di),
		cap:          do.MustInvoke[capabilityIntegration](di),
		bus:          do.MustInvoke[*LoaderBus](di),
		streams:      do.MustInvoke[*SessionStreamBroker](di),
		engine:       do.MustInvoke[LoaderEngine](di),
		sessions:     do.MustInvoke[*SessionRPCBridge](di),
		dashboard:    dashboard,
		eventQueue:   eventQueue,
		loaders:      map[string]Loader{},
		running:      map[string]int{},
		scheduleWake: make(chan struct{}, 1),
	}, nil
}

func (m *LoaderManager) Start() {
	m.once.Do(func() {
		if err := m.Refresh(m.rootCtx); err != nil {
			slog.Warn("failed to refresh loaders on startup", "error", err)
		}
		go m.scheduleLoop()
		go m.eventLoop()
	})
}

func (m *LoaderManager) Refresh(ctx context.Context) error {
	items, err := m.configDB.ListLoaders(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]Loader, len(items))
	for _, item := range items {
		next[item.Summary.ID] = cloneLoader(item)
	}
	m.mu.Lock()
	m.loaders = next
	m.mu.Unlock()
	m.wakeScheduler()
	return nil
}

func (m *LoaderManager) Validate(ctx context.Context, runtime, script string) (LoaderValidationResult, error) {
	return m.engine.Validate(ctx, runtime, script)
}

func (m *LoaderManager) CreateLoader(ctx context.Context, loader Loader) (Loader, error) {
	if strings.TrimSpace(loader.Summary.Runtime) == "" {
		loader.Summary.Runtime = LoaderRuntimeScheduler
	}
	if strings.TrimSpace(loader.Script) == "" {
		loader.Script = defaultLoaderScript()
	}
	validation, err := m.engine.Validate(ctx, loader.Summary.Runtime, loader.Script)
	if err != nil {
		return Loader{}, err
	}
	created, err := m.configDB.CreateLoader(ctx, loader)
	if err != nil {
		return Loader{}, err
	}
	if _, err := m.configDB.ReplaceLoaderTriggers(ctx, created.Summary.ID, validation.Triggers); err != nil {
		_ = m.configDB.DeleteLoader(ctx, created.Summary.ID)
		return Loader{}, err
	}
	if err := m.Refresh(ctx); err != nil {
		return Loader{}, err
	}
	m.notifyDashboard("loader_updated")
	return m.configDB.GetLoader(ctx, created.Summary.ID)
}

func (m *LoaderManager) UpdateLoader(ctx context.Context, loader Loader) (Loader, error) {
	validation, err := m.engine.Validate(ctx, loader.Summary.Runtime, loader.Script)
	if err != nil {
		return Loader{}, err
	}
	updated, err := m.configDB.UpdateLoader(ctx, loader)
	if err != nil {
		return Loader{}, err
	}
	if _, err := m.configDB.ReplaceLoaderTriggers(ctx, updated.Summary.ID, validation.Triggers); err != nil {
		return Loader{}, err
	}
	if err := m.Refresh(ctx); err != nil {
		return Loader{}, err
	}
	m.notifyDashboard("loader_updated")
	return m.configDB.GetLoader(ctx, updated.Summary.ID)
}

func (m *LoaderManager) DeleteLoader(ctx context.Context, loaderID string) error {
	if err := m.configDB.DeleteLoader(ctx, loaderID); err != nil {
		return err
	}
	if err := m.Refresh(ctx); err != nil {
		return err
	}
	m.notifyDashboard("loader_updated")
	return nil
}

func (m *LoaderManager) SetLoaderEnabled(ctx context.Context, loaderID string, enabled bool) (Loader, error) {
	if err := m.configDB.SetLoaderEnabled(ctx, loaderID, enabled); err != nil {
		return Loader{}, err
	}
	if err := m.Refresh(ctx); err != nil {
		return Loader{}, err
	}
	m.notifyDashboard("loader_updated")
	return m.configDB.GetLoader(ctx, loaderID)
}

func (m *LoaderManager) SetLoaderTriggerEnabled(ctx context.Context, loaderID, triggerID string, enabled bool) (Loader, error) {
	if err := m.configDB.SetLoaderTriggerEnabled(ctx, loaderID, triggerID, enabled); err != nil {
		return Loader{}, err
	}
	if err := m.Refresh(ctx); err != nil {
		return Loader{}, err
	}
	m.notifyDashboard("loader_updated")
	return m.configDB.GetLoader(ctx, loaderID)
}

func (m *LoaderManager) RunNow(ctx context.Context, loaderID, triggerID, payloadJSON string, timeout time.Duration) (LoaderRunSummary, error) {
	loader, trigger, err := m.loadLoaderForRun(ctx, loaderID, triggerID)
	if err != nil {
		return LoaderRunSummary{}, err
	}
	parentCtx := m.rootCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(parentCtx, m.loaderRunTimeout(timeout))
	defer cancel()
	return m.runLoader(runCtx, loader, trigger, payloadJSON, "manual", false, loaderRunOptions{})
}

func (m *LoaderManager) Publish(topic string, payload map[string]any) {
	if m.bus == nil {
		return
	}
	m.bus.Publish(LoaderTopicEvent{
		Topic:     strings.TrimSpace(topic),
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
}

func (m *LoaderManager) notifyDashboard(reason string) {
	if m == nil || m.dashboard == nil {
		return
	}
	m.dashboard.Notify(reason)
}

func (m *LoaderManager) scheduleLoop() {
	for {
		jobs := m.collectDueScheduledRuns(time.Now().UTC())
		if len(jobs) > 0 {
			m.dispatchScheduledRuns(jobs)
			continue
		}

		nextFireAt, ok := m.nextScheduledFireAt()
		if !ok {
			select {
			case <-m.rootCtx.Done():
				return
			case <-m.scheduleWake:
				continue
			}
		}

		wait := time.Until(nextFireAt)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-m.rootCtx.Done():
			stopTimer(timer)
			return
		case <-m.scheduleWake:
			stopTimer(timer)
			continue
		case <-timer.C:
		}
	}
}

func (m *LoaderManager) dispatchScheduledRuns(jobs []scheduledLoaderRun) {
	for _, job := range jobs {
		runCtx, cancel := context.WithTimeout(m.rootCtx, m.loaderRunTimeout(0))
		go func(job scheduledLoaderRun) {
			defer cancel()
			if _, err := m.runLoader(runCtx, job.loader, &job.trigger, job.payloadJSON, job.source, true, loaderRunOptions{}); err != nil {
				slog.Warn("loader scheduled run failed", "loader_id", job.loader.Summary.ID, "trigger_id", job.trigger.ID, "trigger_kind", job.trigger.Kind, "error", err)
			}
		}(job)
	}
}

func (m *LoaderManager) nextScheduledFireAt() (time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var nextFireAt time.Time
	for _, loader := range m.loaders {
		if !loader.Summary.Enabled {
			continue
		}
		for _, trigger := range loader.Triggers {
			if !trigger.Enabled || !loaderTriggerUsesSchedule(trigger.Kind) || trigger.NextFireAt.IsZero() {
				continue
			}
			if nextFireAt.IsZero() || trigger.NextFireAt.Before(nextFireAt) {
				nextFireAt = trigger.NextFireAt
			}
		}
	}
	if nextFireAt.IsZero() {
		return time.Time{}, false
	}
	return nextFireAt, true
}

func (m *LoaderManager) wakeScheduler() {
	if m == nil || m.scheduleWake == nil {
		return
	}
	select {
	case m.scheduleWake <- struct{}{}:
	default:
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (m *LoaderManager) eventLoop() {
	for {
		select {
		case <-m.rootCtx.Done():
			return
		case event, ok := <-m.bus.Events():
			if !ok {
				return
			}
			payloadJSON, err := marshalJSONCompact(map[string]any{
				"topic":     event.Topic,
				"createdAt": event.CreatedAt.Format(time.RFC3339Nano),
				"payload":   event.Payload,
			})
			if err != nil {
				slog.Warn("failed to encode loader topic event payload", "topic", event.Topic, "error", err)
				continue
			}
			targets := m.collectEventLoaderTargets(event.Topic)
			targets = dedupeWebhookEventTargets(event, targets)
			if len(targets) == 0 {
				ack := event.NoSubscriberAck
				if ack == nil {
					ack = event.Ack
				}
				if ack != nil {
					if err := ack(m.rootCtx); err != nil {
						slog.Warn("failed to mark unmatched loader topic event published", "event_id", event.EventID, "topic", event.Topic, "error", err)
					}
				}
				continue
			}
			if m.eventShouldRetryForBusy(event, targets) {
				m.retryLoaderTopicEvent(event, "loader is already running")
				continue
			}
			reservations, ok := m.reserveEventQueueSlots(event, len(targets))
			if !ok {
				m.retryLoaderTopicEvent(event, "webhook queue is full")
				continue
			}
			if event.Source == TopicEventSourceWebhook {
				m.dispatchWebhookEventTargets(event, targets, payloadJSON, reservations)
				continue
			}
			for _, target := range targets {
				if event.EventID != "" {
					if err := m.configDB.UpsertEventDelivery(m.rootCtx, EventDelivery{
						EventID:   event.EventID,
						LoaderID:  target.loader.Summary.ID,
						TriggerID: target.trigger.ID,
						Status:    EventDeliveryStatusMatched,
					}); err != nil {
						slog.Warn("failed to record event delivery match", "event_id", event.EventID, "loader_id", target.loader.Summary.ID, "trigger_id", target.trigger.ID, "error", err)
					}
				}
				reservation := reservations[0]
				reservations = reservations[1:]
				runCtx, cancel := context.WithTimeout(m.rootCtx, m.loaderRunTimeout(0))
				go func(target eventLoaderTarget, payloadJSON string, topic string, ack func(context.Context) error, release func(), reservation *webhookQueueReservation) {
					defer cancel()
					defer reservation.Release()
					if _, err := m.runLoader(runCtx, target.loader, &target.trigger, payloadJSON, topic, true, loaderRunOptions{retryWhenBusy: event.Source == TopicEventSourceWebhook}, ack); err != nil {
						if errors.Is(err, errLoaderRunBusyForRetry) {
							m.retryLoaderTopicEvent(event, "loader is already running")
							return
						}
						slog.Warn("loader event run failed", "loader_id", target.loader.Summary.ID, "trigger_id", target.trigger.ID, "topic", topic, "error", err)
						if release != nil {
							release()
						}
					}
				}(target, payloadJSON, event.Topic, event.Ack, event.Release, reservation)
			}
		}
	}
}

func (m *LoaderManager) retryLoaderTopicEvent(event LoaderTopicEvent, reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "loader topic event retry requested"
	}
	if event.Retry != nil {
		if err := event.Retry(m.rootCtx, reason, time.Now().UTC().Add(time.Second)); err != nil {
			slog.Warn("failed to retry loader topic event", "event_id", event.EventID, "topic", event.Topic, "reason", reason, "error", err)
		}
		return
	}
	if event.Release != nil {
		event.Release()
	}
}

func (m *LoaderManager) dispatchWebhookEventTargets(event LoaderTopicEvent, targets []eventLoaderTarget, payloadJSON string, reservations []*webhookQueueReservation) {
	acquiredLoaderIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		if !m.enterRun(target.loader) {
			for _, loaderID := range acquiredLoaderIDs {
				m.leaveRun(loaderID)
			}
			for _, reservation := range reservations {
				reservation.Release()
			}
			m.retryLoaderTopicEvent(event, "loader is already running")
			return
		}
		acquiredLoaderIDs = append(acquiredLoaderIDs, target.loader.Summary.ID)
	}
	prepared := make([]preparedLoaderRun, 0, len(targets))
	for index, target := range targets {
		preparedRun, err := m.prepareLoaderRun(m.rootCtx, target.loader, &target.trigger, payloadJSON, event.Topic, loaderRunOptions{alreadyEntered: true})
		if err != nil {
			for _, item := range prepared {
				m.abortPreparedLoaderRun(context.WithoutCancel(m.rootCtx), item, err.Error())
			}
			for _, loaderID := range acquiredLoaderIDs[index+1:] {
				m.leaveRun(loaderID)
			}
			for _, reservation := range reservations {
				reservation.Release()
			}
			reason := err.Error()
			if errors.Is(err, errLoaderRunBusyForRetry) {
				reason = "loader is already running"
			}
			m.retryLoaderTopicEvent(event, reason)
			return
		}
		prepared = append(prepared, preparedRun)
	}
	if event.Ack != nil {
		if err := event.Ack(m.rootCtx); err != nil {
			slog.Warn("failed to mark loader topic event published", "event_id", event.EventID, "topic", event.Topic, "error", err)
		}
	}
	for index, item := range prepared {
		var reservation *webhookQueueReservation
		if index < len(reservations) {
			reservation = reservations[index]
		}
		runCtx, cancel := context.WithTimeout(m.rootCtx, m.loaderRunTimeout(0))
		go func(item preparedLoaderRun, reservation *webhookQueueReservation) {
			defer cancel()
			defer reservation.Release()
			if _, err := m.executePreparedLoaderRun(runCtx, item); err != nil {
				slog.Warn("loader event run failed", "loader_id", item.loader.Summary.ID, "trigger_id", item.run.TriggerID, "topic", event.Topic, "error", err)
			}
		}(item, reservation)
	}
}

func (m *LoaderManager) collectEventLoaderTargets(topic string) []eventLoaderTarget {
	targets := make([]eventLoaderTarget, 0)
	for _, loader := range m.snapshotLoaders() {
		if !loader.Summary.Enabled {
			continue
		}
		for _, trigger := range loader.Triggers {
			if !trigger.Enabled || trigger.Kind != LoaderTriggerKindEvent || !loaderTriggerTopicMatches(trigger.Topic, topic) {
				continue
			}
			targets = append(targets, eventLoaderTarget{
				loader:  loader,
				trigger: trigger,
			})
		}
	}
	return targets
}

func dedupeWebhookEventTargets(event LoaderTopicEvent, targets []eventLoaderTarget) []eventLoaderTarget {
	if event.Source != TopicEventSourceWebhook || len(targets) <= 1 {
		return targets
	}
	seen := map[string]struct{}{}
	deduped := make([]eventLoaderTarget, 0, len(targets))
	for _, target := range targets {
		loaderID := strings.TrimSpace(target.loader.Summary.ID)
		if loaderID == "" {
			deduped = append(deduped, target)
			continue
		}
		if _, ok := seen[loaderID]; ok {
			continue
		}
		seen[loaderID] = struct{}{}
		deduped = append(deduped, target)
	}
	return deduped
}

func (m *LoaderManager) eventShouldRetryForBusy(event LoaderTopicEvent, targets []eventLoaderTarget) bool {
	if event.Source != TopicEventSourceWebhook || len(targets) == 0 {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, target := range targets {
		loaderID := strings.TrimSpace(target.loader.Summary.ID)
		if normalizeLoaderConcurrencyPolicy(target.loader.Summary.ConcurrencyPolicy) != LoaderConcurrencyPolicyParallel && m.running[loaderID] > 0 {
			return true
		}
	}
	return false
}

func (m *LoaderManager) reserveEventQueueSlots(event LoaderTopicEvent, count int) ([]*webhookQueueReservation, bool) {
	if count <= 0 {
		return nil, true
	}
	if event.Source != TopicEventSourceWebhook {
		return noopWebhookQueueReservations(count), true
	}
	if m.eventQueue == nil {
		queue, err := newWebhookRunQueueFromConfig(m.config)
		if err != nil {
			slog.Warn("failed to initialize webhook queue config", "error", err)
			queue = &WebhookRunQueue{running: map[string]int{}}
		}
		m.eventQueue = queue
	}
	reservations := make([]*webhookQueueReservation, 0, count)
	for i := 0; i < count; i++ {
		reservation, ok := m.eventQueue.Reserve(event)
		if !ok {
			for _, reserved := range reservations {
				reserved.Release()
			}
			return nil, false
		}
		reservations = append(reservations, reservation)
	}
	return reservations, true
}

func (m *LoaderManager) collectDueScheduledRuns(now time.Time) []scheduledLoaderRun {
	m.mu.Lock()
	defer m.mu.Unlock()

	jobs := make([]scheduledLoaderRun, 0)
	for id, loader := range m.loaders {
		if !loader.Summary.Enabled {
			continue
		}
		updated := false
		for index := range loader.Triggers {
			trigger := &loader.Triggers[index]
			if !trigger.Enabled || !loaderTriggerUsesSchedule(trigger.Kind) || trigger.NextFireAt.IsZero() || trigger.NextFireAt.After(now) {
				continue
			}
			nextFireAt, err := loaderTriggerNextFireAt(now, *trigger, true)
			if err != nil {
				slog.Warn("failed to compute next loader schedule", "loader_id", loader.Summary.ID, "trigger_id", trigger.ID, "trigger_kind", trigger.Kind, "error", err)
				continue
			}
			trigger.LastFiredAt = now
			trigger.NextFireAt = nextFireAt
			source := loaderTriggerSource(*trigger)
			jobs = append(jobs, scheduledLoaderRun{
				loader:      cloneLoader(loader),
				trigger:     *trigger,
				payloadJSON: "",
				source:      source,
			})
			updated = true
		}
		if updated {
			m.loaders[id] = cloneLoader(loader)
		}
	}
	for _, job := range jobs {
		if err := m.configDB.MarkLoaderTriggerFired(m.rootCtx, job.loader.Summary.ID, job.trigger.ID, job.trigger.LastFiredAt, job.trigger.NextFireAt); err != nil {
			slog.Warn("failed to persist loader fire state", "loader_id", job.loader.Summary.ID, "trigger_id", job.trigger.ID, "trigger_kind", job.trigger.Kind, "error", err)
		}
	}
	return jobs
}

func (m *LoaderManager) loadLoaderForRun(ctx context.Context, loaderID, triggerID string) (Loader, *LoaderTrigger, error) {
	loader, err := m.configDB.GetLoader(ctx, loaderID)
	if err != nil {
		return Loader{}, nil, err
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
	return Loader{}, nil, fmt.Errorf("loader trigger %s/%s not found", loaderID, triggerID)
}

type loaderTriggerEventMetadata struct {
	EventID       string
	Sequence      int64
	CorrelationID string
}

type loaderRunOptions struct {
	retryWhenBusy  bool
	alreadyEntered bool
}

var errLoaderRunBusyForRetry = errors.New("loader is already running")

func (m *LoaderManager) runLoader(ctx context.Context, loader Loader, trigger *LoaderTrigger, payloadJSON, source string, automatic bool, options loaderRunOptions, triggerEventAck ...func(context.Context) error) (LoaderRunSummary, error) {
	prepared, err := m.prepareLoaderRun(ctx, loader, trigger, payloadJSON, source, options)
	if err != nil {
		return LoaderRunSummary{}, err
	}
	if len(triggerEventAck) > 0 && triggerEventAck[0] != nil {
		if err := triggerEventAck[0](ctx); err != nil {
			slog.Warn("failed to mark loader topic event published", "topic", source, "error", err)
		}
	}
	return m.executePreparedLoaderRun(ctx, prepared)
}

func (m *LoaderManager) prepareLoaderRun(ctx context.Context, loader Loader, trigger *LoaderTrigger, payloadJSON, source string, options loaderRunOptions) (preparedLoaderRun, error) {
	payloadJSON, err := normalizeJSONDocument(payloadJSON)
	if err != nil {
		if options.alreadyEntered {
			m.leaveRun(loader.Summary.ID)
		}
		return preparedLoaderRun{}, err
	}
	now := time.Now().UTC()
	run := LoaderRunSummary{
		ID:               uuid.NewString(),
		LoaderID:         loader.Summary.ID,
		TriggerSource:    strings.TrimSpace(source),
		Status:           LoaderRunStatusRunning,
		StartedAt:        now,
		PayloadJSON:      payloadJSON,
		SourceScriptHash: loaderSourceSHA(loader.Script),
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
		run.Status = LoaderRunStatusSkipped
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

func (m *LoaderManager) executePreparedLoaderRun(ctx context.Context, prepared preparedLoaderRun) (LoaderRunSummary, error) {
	if prepared.run.Status == LoaderRunStatusSkipped {
		return prepared.run, nil
	}
	defer m.leaveRun(prepared.loader.Summary.ID)
	run := prepared.run
	host := &loaderRunHost{manager: m, loader: prepared.loader, run: &run, triggerEvent: parseLoaderTriggerEventMetadata(prepared.payloadJSON)}
	execution, execErr := m.engine.Execute(ctx, LoaderExecutionRequest{
		Runtime:     prepared.loader.Summary.Runtime,
		Script:      prepared.loader.Script,
		Trigger:     prepared.trigger,
		PayloadJSON: prepared.payloadJSON,
	}, host)

	// Use a cancel-free context for all post-run bookkeeping so cleanup and
	// status writes still persist when the run context has hit its deadline.
	writeCtx := context.WithoutCancel(ctx)
	host.cleanupCommandSessions(writeCtx)

	completedAt := time.Now().UTC()
	run.CompletedAt = completedAt
	run.DurationMs = completedAt.Sub(run.StartedAt).Milliseconds()
	if execErr != nil {
		run.Status = LoaderRunStatusFailed
		run.Error = execErr.Error()
		_ = m.writeRunArtifact(run.ArtifactsDir, "error.txt", run.Error)
		_ = m.configDB.UpdateLoaderLastError(writeCtx, prepared.loader.Summary.ID, run.Error)
		_ = m.addLoaderEvent(writeCtx, prepared.loader.Summary.ID, run.ID, run.TriggerID, "loader.run.failed", "error", run.Error, nil, "", "", "")
	} else {
		run.Status = LoaderRunStatusSucceeded
		run.ResultJSON = execution.ResultJSON
		if execution.ResultJSON != "" {
			_ = m.writeRunArtifact(run.ArtifactsDir, "result.json", execution.ResultJSON)
		}
		_ = m.configDB.UpdateLoaderLastError(writeCtx, prepared.loader.Summary.ID, "")
		_ = m.addLoaderEvent(writeCtx, prepared.loader.Summary.ID, run.ID, run.TriggerID, "loader.run.completed", "info", "loader run completed", map[string]any{"resultJson": execution.ResultJSON}, "", "", "")
	}
	if err := m.configDB.UpdateLoaderRun(writeCtx, run); err != nil {
		return LoaderRunSummary{}, err
	}
	m.updateTriggerEventDelivery(writeCtx, run)
	m.notifyDashboard("loader_run_updated")
	if err := m.Refresh(writeCtx); err != nil {
		slog.Warn("failed to refresh loaders after run", "loader_id", prepared.loader.Summary.ID, "error", err)
	}
	return run, nil
}

func (m *LoaderManager) abortPreparedLoaderRun(ctx context.Context, prepared preparedLoaderRun, reason string) {
	if prepared.run.Status == LoaderRunStatusSkipped {
		return
	}
	defer m.leaveRun(prepared.loader.Summary.ID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "loader run aborted before execution"
	}
	run := prepared.run
	completedAt := time.Now().UTC()
	run.Status = LoaderRunStatusFailed
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

func (m *LoaderManager) updateTriggerEventDelivery(ctx context.Context, run LoaderRunSummary) {
	if m == nil || m.configDB == nil {
		return
	}
	metadata := parseLoaderTriggerEventMetadata(run.PayloadJSON)
	if metadata.EventID == "" || run.LoaderID == "" || run.TriggerID == "" {
		return
	}
	status := EventDeliveryStatusRunStarted
	errText := ""
	switch run.Status {
	case LoaderRunStatusSucceeded:
		status = EventDeliveryStatusRunSucceeded
	case LoaderRunStatusFailed:
		status = EventDeliveryStatusRunFailed
		errText = run.Error
	case LoaderRunStatusSkipped:
		status = EventDeliveryStatusSkipped
		errText = run.Error
	}
	if err := m.configDB.UpsertEventDelivery(ctx, EventDelivery{
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

func (m *LoaderManager) loaderRunTimeout(override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if m != nil && m.config != nil && m.config.LoaderRunTimeout > 0 {
		return m.config.LoaderRunTimeout
	}
	return 20 * time.Minute
}

func (m *LoaderManager) enterRun(loader Loader) bool {
	loaderID := strings.TrimSpace(loader.Summary.ID)
	policy := normalizeLoaderConcurrencyPolicy(loader.Summary.ConcurrencyPolicy)
	m.mu.Lock()
	defer m.mu.Unlock()
	if policy != LoaderConcurrencyPolicyParallel && m.running[loaderID] > 0 {
		return false
	}
	m.running[loaderID]++
	return true
}

func (m *LoaderManager) leaveRun(loaderID string) {
	loaderID = strings.TrimSpace(loaderID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running[loaderID] <= 1 {
		delete(m.running, loaderID)
		return
	}
	m.running[loaderID]--
}

func (m *LoaderManager) addLoaderEvent(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	_, err := m.addLoaderEventRecord(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
	return err
}

func (m *LoaderManager) addLoaderEventRecord(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) (LoaderEvent, error) {
	payloadJSON, err := marshalJSONCompact(payload)
	if err != nil {
		return LoaderEvent{}, err
	}
	event := LoaderEvent{
		ID:                   uuid.NewString(),
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
		CreatedAt:            time.Now().UTC(),
	}
	if err := m.configDB.AddLoaderEvent(ctx, event); err != nil {
		return LoaderEvent{}, err
	}
	return event, nil
}

func (h *loaderRunHost) Log(ctx context.Context, message string, payload any) error {
	return h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.log", "info", message, payload, "", "", "")
}

func (h *loaderRunHost) PublishEvent(ctx context.Context, topic string, payloadJSON string) (TopicEventRecord, error) {
	if h.manager == nil || h.manager.configDB == nil {
		return TopicEventRecord{}, fmt.Errorf("event store is unavailable")
	}
	topic = strings.TrimSpace(topic)
	if err := validateLoaderPublishTopic(topic); err != nil {
		return TopicEventRecord{}, err
	}
	payloadJSON, err := normalizeJSONDocument(payloadJSON)
	if err != nil {
		return TopicEventRecord{}, err
	}
	if !jsonObjectDocument(payloadJSON) {
		return TopicEventRecord{}, fmt.Errorf("scheduler.event.publish payload must be an object")
	}
	payload := map[string]any{}
	_ = json.Unmarshal([]byte(payloadJSON), &payload)
	eventID := "evt_" + uuid.NewString()
	correlationID := stringFromMap(payload, "correlationId")
	if correlationID == "" {
		correlationID = stringFromMap(payload, "correlation_id")
	}
	if correlationID == "" {
		correlationID = h.triggerEvent.CorrelationID
	}
	if correlationID == "" {
		correlationID = eventID
	}
	parentEventID := h.triggerEvent.EventID
	if explicitParent := stringFromMap(payload, "parentEventId"); explicitParent != "" {
		parentEventID = explicitParent
	}
	envelope := map[string]any{
		"eventId":       eventID,
		"sequence":      int64(0),
		"source":        TopicEventSourceLoader,
		"provider":      stringFromMap(payload, "provider"),
		"topic":         topic,
		"correlationId": correlationID,
		"body":          payload,
	}
	if parentEventID != "" {
		envelope["parentEventId"] = parentEventID
	}
	envelopeJSON, err := marshalJSONCompact(envelope)
	if err != nil {
		return TopicEventRecord{}, err
	}
	created, err := h.manager.configDB.CreateEvent(ctx, TopicEventRecord{
		ID:             eventID,
		Topic:          topic,
		Source:         TopicEventSourceLoader,
		Provider:       stringFromMap(payload, "provider"),
		CorrelationID:  correlationID,
		PayloadHash:    topicEventPayloadSHA256(envelopeJSON),
		PayloadJSON:    envelopeJSON,
		DispatchStatus: TopicEventDispatchPending,
		ParentEventID:  parentEventID,
		PublisherType:  TopicEventSourceLoader,
		PublisherID:    h.loader.Summary.ID,
		PublisherRunID: h.run.ID,
	})
	if err != nil {
		_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.event.publish.failed", "error", err.Error(), map[string]any{"topic": topic}, "", "", "")
		return TopicEventRecord{}, err
	}
	envelope["sequence"] = created.Sequence
	if envelopeJSON, err = marshalJSONCompact(envelope); err == nil {
		_ = h.manager.configDB.UpdateEventPayload(ctx, created.ID, envelopeJSON)
		created.PayloadJSON = envelopeJSON
	}
	_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.event.published", "info", "loader event published", map[string]any{
		"eventId":       created.ID,
		"sequence":      created.Sequence,
		"topic":         created.Topic,
		"correlationId": created.CorrelationID,
	}, "", "", "")
	return created, nil
}

func (h *loaderRunHost) StateGet(ctx context.Context, key string) (string, bool, error) {
	return h.manager.configDB.GetLoaderState(ctx, h.loader.Summary.ID, key)
}

func (h *loaderRunHost) StateSet(ctx context.Context, key, valueJSON string) error {
	return h.manager.configDB.SetLoaderState(ctx, h.loader.Summary.ID, key, valueJSON)
}

func (h *loaderRunHost) StateDelete(ctx context.Context, key string) error {
	return h.manager.configDB.DeleteLoaderState(ctx, h.loader.Summary.ID, key)
}

func (h *loaderRunHost) CallSessionRPC(ctx context.Context, method, requestJSON string) (string, error) {
	if h.manager == nil || h.manager.sessions == nil {
		return "", fmt.Errorf("session rpc bridge is unavailable")
	}
	method = strings.TrimSpace(method)
	requestJSON = strings.TrimSpace(requestJSON)
	responseJSON, err := h.manager.sessions.CallJSONWithSource(ctx, method, requestJSON, SessionTypeScript+":"+h.loader.Summary.ID)
	linkedSessionID := loaderSessionRPCLinkedSessionID(method, requestJSON, responseJSON)
	if err != nil {
		event, _ := h.manager.addLoaderEventRecord(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID,
			"loader.session.rpc.failed", "error", firstNonEmpty(err.Error(), fmt.Sprintf("%s failed", method)),
			map[string]any{"method": method, "requestJson": requestJSON}, linkedSessionID, "", "")
		h.addEventSessionLink(ctx, event, linkedSessionID, "session_rpc_failed")
		return "", err
	}
	event, _ := h.manager.addLoaderEventRecord(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID,
		"loader.session.rpc.completed", "info", fmt.Sprintf("%s completed", method),
		map[string]any{"method": method, "requestJson": requestJSON, "responseJson": responseJSON}, linkedSessionID, "", "")
	h.addEventSessionLink(ctx, event, linkedSessionID, "session_rpc_completed")
	return responseJSON, nil
}

func (h *loaderRunHost) addEventSessionLink(ctx context.Context, event LoaderEvent, sessionID, relation string) {
	if h == nil || h.manager == nil || h.manager.configDB == nil || strings.TrimSpace(sessionID) == "" || h.triggerEvent.EventID == "" {
		return
	}
	if err := h.manager.configDB.AddEventSessionLink(ctx, EventSessionLink{
		EventID:       h.triggerEvent.EventID,
		SessionID:     sessionID,
		Relation:      relation,
		LoaderID:      h.loader.Summary.ID,
		RunID:         h.run.ID,
		TriggerID:     h.run.TriggerID,
		LoaderEventID: event.ID,
	}); err != nil {
		slog.Warn("failed to add event session link", "event_id", h.triggerEvent.EventID, "session_id", sessionID, "run_id", h.run.ID, "error", err)
	}
}

func (h *loaderRunHost) trackCommandSession(sessionID string, cleanup bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !cleanup {
		return
	}
	h.commandSessionIDsMutex.Lock()
	defer h.commandSessionIDsMutex.Unlock()
	if h.commandSessionIDs == nil {
		h.commandSessionIDs = map[string]struct{}{}
	}
	if _, ok := h.commandSessionIDs[sessionID]; ok {
		return
	}
	h.commandSessionIDs[sessionID] = struct{}{}
	h.commandSessionIDOrder = append(h.commandSessionIDOrder, sessionID)
}

func (h *loaderRunHost) cleanupCommandSessions(ctx context.Context) {
	h.commandSessionIDsMutex.Lock()
	sessionIDs := append([]string(nil), h.commandSessionIDOrder...)
	h.commandSessionIDs = nil
	h.commandSessionIDOrder = nil
	h.commandSessionIDsMutex.Unlock()
	for _, sessionID := range sessionIDs {
		if err := h.manager.shutdownLoaderSession(ctx, sessionID); err != nil {
			slog.Warn("failed to stop loader command session after run", "loader_id", h.loader.Summary.ID, "session_id", sessionID, "error", err)
			_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.session.stop_failed", "error", err.Error(), map[string]any{"sessionId": sessionID}, sessionID, "", "")
			continue
		}
		_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.session.stopped", "info", "loader command session stopped after run", map[string]any{"sessionId": sessionID}, sessionID, "", "")
	}
}

func (m *LoaderManager) loaderAgentDefinition(ctx context.Context, loader Loader) (*AgentDefinition, error) {
	agentID := strings.TrimSpace(loader.Summary.AgentID)
	if agentID == "" {
		return nil, nil
	}
	agent, err := m.configDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("loader agent definition %s: %w", agentID, err)
	}
	if !agent.Enabled {
		return nil, fmt.Errorf("loader agent definition %s is disabled", agentID)
	}
	return &agent, nil
}

func (h *loaderRunHost) Agent(ctx context.Context, prompt string, request LoaderAgentRequest) (LoaderAgentResult, error) {
	if h.useProjectManagedAgentRun(request) {
		return h.ProjectAgent(ctx, prompt, request)
	}
	session, eventType, err := h.manager.ensureLoaderSession(ctx, h.loader, request)
	if err != nil {
		return LoaderAgentResult{}, err
	}
	if eventType != "" {
		_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, eventType, "info", "loader session ready", map[string]any{"sessionId": session.Summary.ID}, session.Summary.ID, "", "")
	}

	agentConfig := agentExecutionConfig{Provider: normalizeAgentKind(request.Agent)}
	var agentDefinitionID string
	if agentConfig.Provider == "" {
		agentDefinition, err := h.manager.loaderAgentDefinition(ctx, h.loader)
		if err != nil {
			return LoaderAgentResult{}, err
		}
		if agentDefinition != nil {
			agentConfig = agentExecutionConfigFromDefinition(*agentDefinition, "")
			agentDefinitionID = strings.TrimSpace(agentDefinition.ID)
		}
	}
	if agentDefinitionID == "" {
		agentDefinitionID = strings.TrimSpace(h.loader.Summary.AgentID)
	}
	if agentConfig.Provider == "" {
		agentConfig.Provider = normalizeAgentKind(h.loader.Summary.DefaultAgent)
	}
	if agentConfig.Provider == "" {
		agentConfig.Provider = "codex"
	}

	cell, _, _, execErr := h.manager.executor.ExecuteAgentRequest(ctx, session, ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: agentDefinitionID,
		Model:             agentConfig.Model,
		RunID:             h.run.ID,
		Message:           prompt,
		Timeout:           request.Timeout,
		OutputSchemaJSON:  request.OutputSchema,
	})
	finalText := firstNonEmpty(cell.Output, cell.Stdout, cell.Stderr)
	jsonValue, jsonErr := loaderJSONResult(finalText, request.OutputSchema, "agent finalText")
	if jsonErr != nil && execErr == nil {
		execErr = jsonErr
	}
	result := LoaderAgentResult{
		Text:           finalText,
		Output:         cell.Output,
		FinalText:      finalText,
		JSON:           jsonValue,
		SessionID:      session.Summary.ID,
		CellID:         cell.ID,
		Agent:          firstNonEmpty(cell.Agent, agentConfig.Provider),
		AgentSessionID: cell.AgentSessionID,
		StopReason:     cell.StopReason,
		Success:        cell.Success,
		ExitCode:       cell.ExitCode,
	}
	level := "info"
	eventName := "loader.agent.completed"
	if execErr != nil {
		level = "error"
		eventName = "loader.agent.failed"
		result.Text = firstNonEmpty(result.Text, execErr.Error())
	}
	_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, eventName, level, firstNonEmpty(result.Text, fmt.Sprintf("%s completed", result.Agent)), result, result.SessionID, result.CellID, result.AgentSessionID)
	h.manager.Publish("agent-compose.agent.completed", map[string]any{
		"sessionId":      result.SessionID,
		"cellId":         result.CellID,
		"agent":          result.Agent,
		"agentSessionId": result.AgentSessionID,
		"success":        result.Success,
		"stopReason":     result.StopReason,
		"source":         "loader",
		"loaderId":       h.loader.Summary.ID,
	})
	shutdownErr := h.manager.shutdownLoaderSession(ctx, session.Summary.ID)
	if shutdownErr != nil {
		slog.Warn("failed to stop loader session after agent run", "loader_id", h.loader.Summary.ID, "session_id", session.Summary.ID, "error", shutdownErr)
		_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.session.stop_failed", "error", shutdownErr.Error(), map[string]any{"sessionId": session.Summary.ID}, session.Summary.ID, "", "")
	} else {
		_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.session.stopped", "info", "loader session stopped after agent run", map[string]any{"sessionId": session.Summary.ID}, session.Summary.ID, "", "")
	}
	if execErr != nil {
		return result, execErr
	}
	return result, nil
}

func (h *loaderRunHost) useProjectManagedAgentRun(request LoaderAgentRequest) bool {
	if h == nil || h.manager == nil {
		return false
	}
	if strings.TrimSpace(h.loader.Summary.ManagedProjectID) == "" || strings.TrimSpace(h.loader.Summary.ManagedAgentName) == "" {
		return false
	}
	if strings.TrimSpace(request.Agent) != "" || request.Timeout > 0 {
		return false
	}
	return !loaderAgentRequestOverridesSession(request, true)
}

func (h *loaderRunHost) ProjectAgent(ctx context.Context, prompt string, request LoaderAgentRequest) (LoaderAgentResult, error) {
	runService := h.manager.projectRunService()
	run, execErr, err := runService.runProjectAgent(ctx, &agentcomposev2.RunAgentRequest{
		ProjectId:        h.loader.Summary.ManagedProjectID,
		AgentName:        h.loader.Summary.ManagedAgentName,
		Prompt:           prompt,
		Source:           agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER,
		SchedulerId:      h.loader.Summary.ManagedSchedulerID,
		TriggerId:        h.run.TriggerID,
		OutputSchemaJson: request.OutputSchema,
		ClientRequestId:  firstNonEmpty(h.run.ID, uuid.NewString()),
	}, nil)
	if err != nil {
		return LoaderAgentResult{}, err
	}
	result, jsonErr := loaderAgentResultFromProjectRun(run, request.OutputSchema)
	if jsonErr != nil && execErr == nil {
		execErr = jsonErr
	}
	level := "info"
	eventName := "loader.agent.completed"
	if execErr != nil || run.Status != ProjectRunStatusSucceeded {
		level = "error"
		eventName = "loader.agent.failed"
		result.Text = firstNonEmpty(result.Text, run.Error, execErrString(execErr))
	}
	_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, eventName, level, firstNonEmpty(result.Text, fmt.Sprintf("%s completed", result.Agent)), result, result.SessionID, result.CellID, result.AgentSessionID)
	h.manager.Publish("agent-compose.agent.completed", map[string]any{
		"sessionId":      result.SessionID,
		"cellId":         result.CellID,
		"agent":          result.Agent,
		"agentSessionId": result.AgentSessionID,
		"success":        result.Success,
		"stopReason":     result.StopReason,
		"source":         "loader",
		"loaderId":       h.loader.Summary.ID,
		"loaderRunId":    h.run.ID,
		"projectId":      run.ProjectID,
		"projectRunId":   run.RunID,
	})
	if execErr != nil {
		return result, execErr
	}
	return result, nil
}

func (m *LoaderManager) projectRunService() *Service {
	if m == nil {
		return &Service{}
	}
	return &Service{
		config:   m.config,
		store:    m.store,
		configDB: m.configDB,
		driver:   m.driver,
		executor: m.executor,
		images:   m.images,
		streams:  m.streams,
	}
}

func loaderAgentResultFromProjectRun(run ProjectRunRecord, outputSchemaJSON string) (LoaderAgentResult, error) {
	metadata := projectRunResultMetadata(run.ResultJSON)
	text := firstNonEmpty(run.Output, run.Error)
	jsonValue, jsonErr := loaderJSONResult(text, outputSchemaJSON, "project run output")
	return LoaderAgentResult{
		Text:           text,
		Output:         run.Output,
		FinalText:      run.Output,
		JSON:           jsonValue,
		SessionID:      run.SessionID,
		CellID:         metadata.CellID,
		Agent:          firstNonEmpty(metadata.Agent, run.AgentName),
		AgentSessionID: metadata.AgentSessionID,
		StopReason:     metadata.StopReason,
		Success:        run.Status == ProjectRunStatusSucceeded,
		ExitCode:       run.ExitCode,
	}, jsonErr
}

type projectRunResultFields struct {
	CellID         string `json:"cellId"`
	Agent          string `json:"agent"`
	AgentSessionID string `json:"agentSessionId"`
	StopReason     string `json:"stopReason"`
}

func projectRunResultMetadata(resultJSON string) projectRunResultFields {
	var metadata projectRunResultFields
	_ = json.Unmarshal([]byte(resultJSON), &metadata)
	return metadata
}

func execErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func loaderJSONResult(text, outputSchemaJSON, sourceName string) (any, error) {
	if strings.TrimSpace(outputSchemaJSON) == "" {
		return nil, nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON for outputSchema: %w", sourceName, err)
	}
	return parsed, nil
}

func (h *loaderRunHost) Command(ctx context.Context, request LoaderCommandRequest) (LoaderCommandResult, error) {
	cleanupSession := loaderCommandRequestRequiresCleanup(h.loader, request)
	agentRequest := LoaderAgentRequest{
		SessionPolicy: request.SessionPolicy,
		Title:         request.Title,
		Driver:        request.Driver,
		GuestImage:    request.GuestImage,
		WorkspaceID:   request.WorkspaceID,
		SessionEnv:    request.SessionEnv,
	}
	session, eventType, err := h.ensureCommandSession(ctx, agentRequest, cleanupSession)
	if err != nil {
		_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.command.failed", "error", err.Error(), loaderCommandEventPayload(request, LoaderCommandResult{}), "", "", "")
		return LoaderCommandResult{}, err
	}
	if eventType != "" {
		_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, eventType, "info", "loader command session ready", map[string]any{"sessionId": session.Summary.ID}, session.Summary.ID, "", "")
	}
	h.trackCommandSession(session.Summary.ID, cleanupSession)

	result, err := h.manager.executor.ExecuteLoaderCommand(ctx, session, request)
	if err != nil {
		_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.command.failed", "error", err.Error(), loaderCommandEventPayload(request, result), result.SessionID, result.CellID, "")
		return result, err
	}
	level := "info"
	if !result.Success {
		level = "error"
	}
	_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.command.completed", level, firstNonEmpty(result.Output, result.Stdout, result.Stderr, "loader command completed"), loaderCommandEventPayload(request, result), result.SessionID, result.CellID, "")
	return result, nil
}

func (h *loaderRunHost) ensureCommandSession(ctx context.Context, request LoaderAgentRequest, cleanupSession bool) (*Session, string, error) {
	if cleanupSession {
		h.commandSessionIDsMutex.Lock()
		session := h.commandReusableSession
		h.commandSessionIDsMutex.Unlock()
		if session != nil {
			if loaded, err := h.manager.store.GetSession(ctx, session.Summary.ID); err == nil && loaded.Summary.VMStatus == VMStatusRunning {
				return loaded, "", nil
			}
		}
	}
	session, eventType, err := h.manager.ensureLoaderCommandSession(ctx, h.loader, request)
	if err != nil {
		return nil, "", err
	}
	if cleanupSession {
		h.commandSessionIDsMutex.Lock()
		h.commandReusableSession = session
		h.commandSessionIDsMutex.Unlock()
	}
	return session, eventType, nil
}

func (h *loaderRunHost) LLM(ctx context.Context, prompt string, request LoaderLLMRequest) (LoaderLLMResult, error) {
	if h.manager == nil || h.manager.llm == nil {
		return LoaderLLMResult{}, fmt.Errorf("llm client is unavailable")
	}
	result, err := h.manager.llm.Generate(ctx, prompt, request.Model, request.OutputSchema)
	if err != nil {
		_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.llm.failed", "error", err.Error(), map[string]any{"model": strings.TrimSpace(request.Model)}, "", "", "")
		return LoaderLLMResult{}, err
	}
	response := LoaderLLMResult{
		Text:         result.Text,
		Model:        result.Model,
		ResponseID:   result.ResponseID,
		FinishReason: result.FinishReason,
	}
	_ = h.manager.addLoaderEvent(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, "loader.llm.completed", "info", firstNonEmpty(response.Text, "llm completed"), response, "", "", "")
	return response, nil
}

func (m *LoaderManager) shutdownLoaderSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	stopCtx := context.WithoutCancel(ctx)
	session, err := m.store.GetSession(stopCtx, sessionID)
	if err != nil {
		return err
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return nil
	}
	if err := m.driver.StopSessionVM(stopCtx, session); err != nil {
		return err
	}
	session.Summary.VMStatus = VMStatusStopped
	if err := m.store.UpdateSession(stopCtx, session); err != nil {
		return err
	}
	m.streams.PublishSessionUpdated(&session.Summary)
	event := SessionEvent{ID: uuid.NewString(), Type: "session.stopped", Level: "info", Message: "session stopped", CreatedAt: time.Now().UTC()}
	_ = m.store.AddEvent(stopCtx, session.Summary.ID, event)
	m.streams.PublishEventAdded(session.Summary.ID, event)
	loaded, err := m.store.GetSession(stopCtx, session.Summary.ID)
	if err != nil {
		return err
	}
	m.Publish("agent-compose.session.stopped", sessionTopicPayload(loaded, "loader"))
	return nil
}

func (m *LoaderManager) ensureLoaderSession(ctx context.Context, loader Loader, request LoaderAgentRequest) (*Session, string, error) {
	return m.ensureLoaderSessionWithOptions(ctx, loader, request, true)
}

func (m *LoaderManager) ensureLoaderCommandSession(ctx context.Context, loader Loader, request LoaderAgentRequest) (*Session, string, error) {
	return m.ensureLoaderSessionWithOptions(ctx, loader, request, false)
}

func (m *LoaderManager) ensureLoaderSessionWithOptions(ctx context.Context, loader Loader, request LoaderAgentRequest, titleOverridesSession bool) (*Session, string, error) {
	agentDefinition, err := m.loaderAgentDefinition(ctx, loader)
	if err != nil {
		return nil, "", err
	}
	effectivePolicy := normalizeLoaderSessionPolicy(loader.Summary.SessionPolicy)
	if strings.TrimSpace(request.SessionPolicy) != "" {
		effectivePolicy = normalizeLoaderSessionPolicy(request.SessionPolicy)
	}
	hasOverrides := loaderAgentRequestOverridesSession(request, titleOverridesSession)
	forceNew := effectivePolicy == LoaderSessionPolicyNew || hasOverrides
	if !forceNew {
		if binding, ok, err := m.configDB.GetLoaderBinding(ctx, loader.Summary.ID); err != nil {
			return nil, "", err
		} else if ok {
			session, eventType, err := m.loadOrResumeLoaderSession(ctx, binding.SessionID)
			if err == nil {
				return session, eventType, nil
			}
			slog.Warn("failed to reuse loader sticky session, creating a new one", "loader_id", loader.Summary.ID, "session_id", binding.SessionID, "error", err)
		}
	}

	envItems, err := m.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return nil, "", err
	}
	if agentDefinition != nil {
		envItems = mergeEnvItems(envItems, agentDefinition.EnvItems)
	}
	envItems = mergeEnvItems(envItems, loader.EnvItems)
	envItems = mergeEnvItems(envItems, request.SessionEnv)
	providerEnvItems := envItems
	envItems = filterPersistedRuntimeEnv(envItems)
	capabilityVars, capabilityTags := buildCapabilityGatewaySessionVars(capabilityGatewayProxyTarget(m.cap), loader.Summary.CapsetIDs)
	envItems = mergeEnvItems(envItems, capabilityVars)
	tags := []SessionTag{{Name: "origin", Value: "loader"}, {Name: "loader_id", Value: loader.Summary.ID}, {Name: "loader_name", Value: loader.Summary.Name}}
	tags = append(tags, capabilityTags...)

	var workspaceSnapshot *SessionWorkspace
	workspaceID := firstNonEmpty(strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(loader.Summary.WorkspaceID))
	if agentDefinition != nil {
		workspaceID = firstNonEmpty(strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(loader.Summary.WorkspaceID), strings.TrimSpace(agentDefinition.WorkspaceID))
	}
	if workspaceID != "" {
		workspaceConfig, err := m.configDB.GetWorkspaceConfig(ctx, workspaceID)
		if err != nil {
			return nil, "", err
		}
		workspaceSnapshot = toSessionWorkspaceSnapshot(workspaceConfig)
	}

	driverValue := firstNonEmpty(strings.TrimSpace(request.Driver), strings.TrimSpace(loader.Summary.Driver))
	if agentDefinition != nil {
		driverValue = firstNonEmpty(strings.TrimSpace(request.Driver), strings.TrimSpace(loader.Summary.Driver), strings.TrimSpace(agentDefinition.Driver))
	}
	driver, err := driverpkg.ResolveSessionRuntimeDriver(driverValue, m.config.RuntimeDriver)
	if err != nil {
		return nil, "", err
	}
	agentGuestImage := ""
	if agentDefinition != nil {
		agentGuestImage = agentDefinition.GuestImage
	}
	guestImage := driverpkg.ResolveSessionGuestImage(request.GuestImage, loader.Summary.GuestImage, agentGuestImage, driverpkg.DefaultGuestImageForDriver(m.config, driver))
	title := firstNonEmpty(strings.TrimSpace(request.Title), strings.TrimSpace(loader.Summary.Name), defaultLoaderName(time.Now().UTC()))
	if agentDefinition != nil {
		tags = append(tags, sessionTagsFromProto(agentDefinitionTags(*agentDefinition))...)
	}
	session, err := m.store.CreateSession(ctx,
		title,
		"",
		driver,
		guestImage,
		workspaceID,
		SessionTypeScript+":"+loader.Summary.ID,
		workspaceSnapshot,
		envItems,
		tags,
	)
	if err != nil {
		return nil, "", err
	}
	session.ProviderEnvItems = providerEnvItems
	if err := prepareSessionWorkspace(ctx, m.config, m.configDB, session); err != nil {
		session.Summary.VMStatus = VMStatusFailed
		_ = m.store.UpdateSession(ctx, session)
		return nil, "", err
	}
	writeCapabilityGuide(ctx, m.cap, m.store, m.streams, session, loader.Summary.CapsetIDs)
	if err := m.driver.StartSessionVM(ctx, session); err != nil {
		session.Summary.VMStatus = VMStatusFailed
		_ = m.store.UpdateSession(ctx, session)
		return nil, "", err
	}
	session.Summary.VMStatus = VMStatusRunning
	if err := m.store.UpdateSession(ctx, session); err != nil {
		return nil, "", err
	}
	m.streams.PublishSessionUpdated(&session.Summary)
	event := SessionEvent{ID: uuid.NewString(), Type: "session.created", Level: "info", Message: fmt.Sprintf("session started with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage), CreatedAt: time.Now().UTC()}
	_ = m.store.AddEvent(ctx, session.Summary.ID, event)
	m.streams.PublishEventAdded(session.Summary.ID, event)
	if effectivePolicy == LoaderSessionPolicySticky {
		_ = m.configDB.UpsertLoaderBinding(ctx, LoaderBinding{LoaderID: loader.Summary.ID, SessionID: session.Summary.ID})
	}
	loaded, err := m.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, "", err
	}
	restoreSessionTransientFields(loaded, session)
	m.Publish("agent-compose.session.created", map[string]any{
		"sessionId":     loaded.Summary.ID,
		"title":         loaded.Summary.Title,
		"driver":        loaded.Summary.Driver,
		"triggerSource": loaded.Summary.TriggerSource,
		"source":        "loader",
		"loaderId":      loader.Summary.ID,
	})
	return loaded, "loader.session.created", nil
}

func sessionTagsFromProto(items []*agentcomposev1.SessionTag) []SessionTag {
	if len(items) == 0 {
		return nil
	}
	result := make([]SessionTag, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		result = append(result, SessionTag{Name: item.GetName(), Value: item.GetValue()})
	}
	return result
}

func loaderAgentRequestOverridesSession(request LoaderAgentRequest, includeTitle bool) bool {
	return (includeTitle && strings.TrimSpace(request.Title) != "") ||
		strings.TrimSpace(request.Driver) != "" ||
		strings.TrimSpace(request.GuestImage) != "" ||
		strings.TrimSpace(request.WorkspaceID) != "" ||
		len(normalizeEnvItems(request.SessionEnv)) > 0
}

func loaderCommandRequestRequiresCleanup(loader Loader, request LoaderCommandRequest) bool {
	effectivePolicy := normalizeLoaderSessionPolicy(loader.Summary.SessionPolicy)
	if strings.TrimSpace(request.SessionPolicy) != "" {
		effectivePolicy = normalizeLoaderSessionPolicy(request.SessionPolicy)
	}
	return effectivePolicy == LoaderSessionPolicyNew || loaderCommandRequestOverridesSession(request)
}

func loaderCommandRequestOverridesSession(request LoaderCommandRequest) bool {
	return strings.TrimSpace(request.Driver) != "" ||
		strings.TrimSpace(request.GuestImage) != "" ||
		strings.TrimSpace(request.WorkspaceID) != "" ||
		len(normalizeEnvItems(request.SessionEnv)) > 0
}

func (m *LoaderManager) loadOrResumeLoaderSession(ctx context.Context, sessionID string) (*Session, string, error) {
	session, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, "", err
	}
	if session.Summary.VMStatus == VMStatusRunning {
		return session, "", nil
	}
	if err := prepareSessionWorkspace(ctx, m.config, m.configDB, session); err != nil {
		return nil, "", err
	}
	writeCapabilityGuide(ctx, m.cap, m.store, m.streams, session, sessionCapabilityCapsets(session))
	if err := m.driver.StartSessionVM(ctx, session); err != nil {
		return nil, "", err
	}
	session.Summary.VMStatus = VMStatusRunning
	if err := m.store.UpdateSession(ctx, session); err != nil {
		return nil, "", err
	}
	m.streams.PublishSessionUpdated(&session.Summary)
	event := SessionEvent{ID: uuid.NewString(), Type: "session.resumed", Level: "info", Message: fmt.Sprintf("session resumed with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage), CreatedAt: time.Now().UTC()}
	_ = m.store.AddEvent(ctx, session.Summary.ID, event)
	m.streams.PublishEventAdded(session.Summary.ID, event)
	loaded, err := m.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, "", err
	}
	restoreSessionTransientFields(loaded, session)
	m.Publish("agent-compose.session.resumed", map[string]any{
		"sessionId": loaded.Summary.ID,
		"title":     loaded.Summary.Title,
		"driver":    loaded.Summary.Driver,
		"source":    "loader",
	})
	return loaded, "loader.session.resumed", nil
}

func (m *LoaderManager) runArtifactsDir(loaderID, runID string) string {
	parts := []string{m.config.DataRoot, "loaders", strings.TrimSpace(loaderID), "runs"}
	if strings.TrimSpace(runID) != "" {
		parts = append(parts, strings.TrimSpace(runID))
	}
	return filepath.Join(parts...)
}

func (m *LoaderManager) writeRunArtifact(dir, name, content string) error {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(content) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(strings.TrimSpace(content)+"\n"), 0o644)
}

func cloneLoader(item Loader) Loader {
	cloned := item
	if item.Triggers != nil {
		cloned.Triggers = append([]LoaderTrigger(nil), item.Triggers...)
	}
	if item.EnvItems != nil {
		cloned.EnvItems = append([]SessionEnvVar(nil), item.EnvItems...)
	}
	return cloned
}

func (m *LoaderManager) snapshotLoaders() []Loader {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Loader, 0, len(m.loaders))
	for _, item := range m.loaders {
		items = append(items, cloneLoader(item))
	}
	return items
}

func normalizeJSONDocument(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(raw)); err != nil {
		return "", fmt.Errorf("normalize json document: %w", err)
	}
	return compact.String(), nil
}

func marshalJSONCompact(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode json payload: %w", err)
	}
	return string(data), nil
}

func parseLoaderTriggerEventMetadata(payloadJSON string) loaderTriggerEventMetadata {
	payloadJSON = strings.TrimSpace(payloadJSON)
	if payloadJSON == "" {
		return loaderTriggerEventMetadata{}
	}
	var envelope struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &envelope); err != nil {
		return loaderTriggerEventMetadata{}
	}
	return loaderTriggerEventMetadata{
		EventID:       stringFromMap(envelope.Payload, "eventId"),
		CorrelationID: stringFromMap(envelope.Payload, "correlationId"),
		Sequence:      int64FromMap(envelope.Payload, "sequence"),
	}
}

func validateLoaderPublishTopic(topic string) error {
	if err := validateTopicEventName(topic); err != nil {
		return err
	}
	if strings.HasPrefix(topic, "runtime.") || strings.HasPrefix(topic, "workflow.") || strings.HasPrefix(topic, "external.") {
		return nil
	}
	return fmt.Errorf("loader event topic must use runtime.*, workflow.*, or external.* prefix")
}

func jsonObjectDocument(payloadJSON string) bool {
	var payload map[string]any
	return json.Unmarshal([]byte(payloadJSON), &payload) == nil && payload != nil
}

func stringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func int64FromMap(values map[string]any, key string) int64 {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

func loaderSessionRPCLinkedSessionID(method, requestJSON, responseJSON string) string {
	if value := loaderSessionIDFromJSON(responseJSON); value != "" {
		return value
	}
	if strings.TrimSpace(method) == "ListSessions" {
		return ""
	}
	return loaderSessionIDFromJSON(requestJSON)
}

func loaderSessionIDFromJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	if value, ok := payload["sessionId"].(string); ok {
		return strings.TrimSpace(value)
	}
	sessionValue, ok := payload["session"].(map[string]any)
	if !ok {
		return ""
	}
	summaryValue, ok := sessionValue["summary"].(map[string]any)
	if !ok {
		return ""
	}
	if value, ok := summaryValue["sessionId"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
