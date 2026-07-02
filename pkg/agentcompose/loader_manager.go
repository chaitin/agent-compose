package agentcompose

import (
	loaderdomain "agent-compose/internal/agentcompose/loader"
	appconfig "agent-compose/pkg/config"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"bytes"
	"context"
	"encoding/json"
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

	runExecutor        *LoaderRunExecutor
	eventDispatcher    *LoaderEventDispatcher
	sessionRunner      *LoaderSessionRunner
	projectAgentRunner ProjectAgentRunner

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
	m := &LoaderManager{
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
	}
	m.initLoaderComponents()
	return m, nil
}

func (m *LoaderManager) initLoaderComponents() {
	if m == nil {
		return
	}
	if m.runExecutor == nil {
		m.runExecutor = NewLoaderRunExecutor(m)
	}
	if m.eventDispatcher == nil {
		m.eventDispatcher = NewLoaderEventDispatcher(m)
	}
	if m.sessionRunner == nil {
		m.sessionRunner = NewLoaderSessionRunner(m)
	}
	if m.projectAgentRunner == nil {
		m.projectAgentRunner = NewServiceProjectAgentRunner(m)
	}
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
	m.initLoaderComponents()
	for {
		select {
		case <-m.rootCtx.Done():
			return
		case event, ok := <-m.bus.Events():
			if !ok {
				return
			}
			m.eventDispatcher.Dispatch(event)
		}
	}
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

func (m *LoaderManager) reserveEventQueueSlots(event LoaderTopicEvent, count int) ([]*webhookQueueReservation, bool) {
	m.initLoaderComponents()
	return m.eventDispatcher.reserveQueueSlots(event, count)
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

var errLoaderRunBusyForRetry = loaderdomain.ErrRunBusyForRetry

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

func (h *loaderRunHost) addLinkedLoaderEvent(ctx context.Context, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	event, err := h.manager.addLoaderEventRecord(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
	if err != nil {
		return err
	}
	h.addEventSessionLink(ctx, event, linkedSessionID, event.Type)
	return nil
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
		CreatedAt:     event.CreatedAt,
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
			_ = h.addLinkedLoaderEvent(ctx, "loader.session.stop_failed", "error", err.Error(), map[string]any{"sessionId": sessionID}, sessionID, "", "")
			continue
		}
		_ = h.addLinkedLoaderEvent(ctx, "loader.session.stopped", "info", "loader command session stopped after run", map[string]any{"sessionId": sessionID}, sessionID, "", "")
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
		_ = h.addLinkedLoaderEvent(ctx, eventType, "info", "loader session ready", map[string]any{"sessionId": session.Summary.ID}, session.Summary.ID, "", "")
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
	_ = h.addLinkedLoaderEvent(ctx, eventName, level, firstNonEmpty(result.Text, fmt.Sprintf("%s completed", result.Agent)), result, result.SessionID, result.CellID, result.AgentSessionID)
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
		_ = h.addLinkedLoaderEvent(ctx, "loader.session.stop_failed", "error", shutdownErr.Error(), map[string]any{"sessionId": session.Summary.ID}, session.Summary.ID, "", "")
	} else {
		_ = h.addLinkedLoaderEvent(ctx, "loader.session.stopped", "info", "loader session stopped after agent run", map[string]any{"sessionId": session.Summary.ID}, session.Summary.ID, "", "")
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
	run, execErr, err := h.manager.projectAgentRunnerComponent().RunProjectAgent(ctx, &agentcomposev2.RunAgentRequest{
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
	_ = h.addLinkedLoaderEvent(ctx, eventName, level, firstNonEmpty(result.Text, fmt.Sprintf("%s completed", result.Agent)), result, result.SessionID, result.CellID, result.AgentSessionID)
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
		_ = h.addLinkedLoaderEvent(ctx, eventType, "info", "loader command session ready", map[string]any{"sessionId": session.Summary.ID}, session.Summary.ID, "", "")
	}
	h.trackCommandSession(session.Summary.ID, cleanupSession)

	result, err := h.manager.executor.ExecuteLoaderCommand(ctx, session, request)
	if err != nil {
		_ = h.addLinkedLoaderEvent(ctx, "loader.command.failed", "error", err.Error(), loaderCommandEventPayload(request, result), result.SessionID, result.CellID, "")
		return result, err
	}
	level := "info"
	if !result.Success {
		level = "error"
	}
	_ = h.addLinkedLoaderEvent(ctx, "loader.command.completed", level, firstNonEmpty(result.Output, result.Stdout, result.Stderr, "loader command completed"), loaderCommandEventPayload(request, result), result.SessionID, result.CellID, "")
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
