package agentcompose

import (
	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/events/webhooks"
	"agent-compose/pkg/images"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
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
	images     images.Backend
	llm        *LLMClient
	cap        capabilities.Provider
	bus        *loaders.Bus
	streams    *sessions.StreamBroker
	engine     loaders.LoaderEngine
	sessions   *SessionRPCBridge
	dashboard  *dashboard.Hub
	eventQueue *webhooks.RunQueue

	runExecutor        *LoaderRunExecutor
	eventDispatcher    *LoaderEventDispatcher
	scheduler          *loaders.Scheduler
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
	run                    *domain.LoaderRunSummary
	triggerEvent           loaders.TriggerEventMetadata
	commandSessionIDsMutex sync.Mutex
	runtimeHostCore        *loaders.RuntimeHost
}

type loaderTriggerEventMetadata = loaders.TriggerEventMetadata

type scheduledLoaderRun struct {
	loader      Loader
	trigger     domain.LoaderTrigger
	payloadJSON string
	source      string
}

type preparedLoaderRun struct {
	loader      Loader
	trigger     *domain.LoaderTrigger
	run         domain.LoaderRunSummary
	payloadJSON string
}

func scheduledLoaderRunFromCore(item loaders.ScheduledRun) scheduledLoaderRun {
	return scheduledLoaderRun{
		loader:      item.Loader,
		trigger:     item.Trigger,
		payloadJSON: item.PayloadJSON,
		source:      item.Source,
	}
}

func (r scheduledLoaderRun) toCore() loaders.ScheduledRun {
	return loaders.ScheduledRun{
		Loader:      r.loader,
		Trigger:     r.trigger,
		PayloadJSON: r.payloadJSON,
		Source:      r.source,
	}
}

func preparedLoaderRunFromCore(item loaders.PreparedRun) preparedLoaderRun {
	return preparedLoaderRun{
		loader:      item.Loader,
		trigger:     item.Trigger,
		run:         item.Run,
		payloadJSON: item.PayloadJSON,
	}
}

func (r preparedLoaderRun) toCore() loaders.PreparedRun {
	return loaders.PreparedRun{
		Loader:      r.loader,
		Trigger:     r.trigger,
		Run:         r.run,
		PayloadJSON: r.payloadJSON,
	}
}

func NewLoaderManager(di do.Injector) (*LoaderManager, error) {
	rootCtx := do.MustInvoke[context.Context](di)
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	config := do.MustInvoke[*appconfig.Config](di)
	eventQueue, err := webhooks.NewRunQueueFromConfig(config)
	if err != nil {
		return nil, err
	}
	dashboard, _ := do.Invoke[*dashboard.Hub](di)
	m := &LoaderManager{
		config:       config,
		rootCtx:      rootCtx,
		store:        do.MustInvoke[*Store](di),
		configDB:     do.MustInvoke[*ConfigStore](di),
		driver:       do.MustInvoke[Driver](di),
		executor:     do.MustInvoke[*Executor](di),
		images:       images.NewDockerBackend(),
		llm:          do.MustInvoke[*LLMClient](di),
		cap:          do.MustInvoke[capabilityIntegration](di),
		bus:          do.MustInvoke[*loaders.Bus](di),
		streams:      do.MustInvoke[*sessions.StreamBroker](di),
		engine:       do.MustInvoke[loaders.LoaderEngine](di),
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
	if m.scheduler == nil {
		m.scheduler = loaders.NewScheduler(loaders.SchedulerDependencies{
			RootCtx:       m.rootCtx,
			Wake:          m.scheduleWake,
			Store:         m.configDB,
			Snapshot:      m.cachedLoadersMap,
			ReplaceCached: m.replaceCachedLoaders,
			Run: func(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options loaders.RunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error) {
				return m.runLoader(ctx, loader, trigger, payloadJSON, source, true, loaderRunOptions{retryWhenBusy: options.RetryWhenBusy, alreadyEntered: options.AlreadyEntered}, triggerEventAck...)
			},
			RunTimeout: m.loaderRunTimeout,
		})
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

func (m *LoaderManager) Validate(ctx context.Context, runtime, script string) (loaders.LoaderValidationResult, error) {
	return m.engine.Validate(ctx, runtime, script)
}

func (m *LoaderManager) CreateLoader(ctx context.Context, loader Loader) (Loader, error) {
	if strings.TrimSpace(loader.Summary.Runtime) == "" {
		loader.Summary.Runtime = domain.LoaderRuntimeScheduler
	}
	if strings.TrimSpace(loader.Script) == "" {
		loader.Script = domain.DefaultLoaderScript()
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

func (m *LoaderManager) RunNow(ctx context.Context, loaderID, triggerID, payloadJSON string, timeout time.Duration) (domain.LoaderRunSummary, error) {
	loader, trigger, err := m.loadLoaderForRun(ctx, loaderID, triggerID)
	if err != nil {
		return domain.LoaderRunSummary{}, err
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
	m.bus.Publish(domain.LoaderTopicEvent{
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
	m.initLoaderComponents()
	m.scheduler.Loop()
}

func (m *LoaderManager) dispatchScheduledRuns(jobs []scheduledLoaderRun) {
	m.initLoaderComponents()
	coreJobs := make([]loaders.ScheduledRun, 0, len(jobs))
	for _, job := range jobs {
		coreJobs = append(coreJobs, job.toCore())
	}
	m.scheduler.Dispatch(coreJobs)
}

func (m *LoaderManager) nextScheduledFireAt() (time.Time, bool) {
	m.initLoaderComponents()
	return m.scheduler.NextFireAt()
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
	loaders.StopTimer(timer)
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

func (m *LoaderManager) reserveEventQueueSlots(event domain.LoaderTopicEvent, count int) ([]*webhooks.Reservation, bool) {
	m.initLoaderComponents()
	return m.eventDispatcher.reserveQueueSlots(event, count)
}

func (m *LoaderManager) collectDueScheduledRuns(now time.Time) []scheduledLoaderRun {
	m.initLoaderComponents()
	coreJobs := m.scheduler.CollectDue(now)
	jobs := make([]scheduledLoaderRun, 0, len(coreJobs))
	for _, item := range coreJobs {
		jobs = append(jobs, scheduledLoaderRunFromCore(item))
	}
	return jobs
}

func (m *LoaderManager) cachedLoadersMap() map[string]domain.Loader {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make(map[string]domain.Loader, len(m.loaders))
	for id, item := range m.loaders {
		items[id] = cloneLoader(item)
	}
	return items
}

func (m *LoaderManager) replaceCachedLoaders(updatedLoaders map[string]domain.Loader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, item := range updatedLoaders {
		m.loaders[id] = cloneLoader(item)
	}
}

func (m *LoaderManager) loadLoaderForRun(ctx context.Context, loaderID, triggerID string) (Loader, *domain.LoaderTrigger, error) {
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
	id := strings.TrimSpace(loaderID) + "/" + triggerID
	return Loader{}, nil, resourceError(ErrNotFound, "loader trigger", id, fmt.Sprintf("loader trigger %s not found", id), nil)
}

type loaderRunOptions struct {
	retryWhenBusy  bool
	alreadyEntered bool
}

func (m *LoaderManager) updateTriggerEventDelivery(ctx context.Context, run domain.LoaderRunSummary) {
	if m == nil || m.configDB == nil {
		return
	}
	metadata := parseLoaderTriggerEventMetadata(run.PayloadJSON)
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
	if err := m.configDB.UpsertEventDelivery(ctx, domain.EventDelivery{
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

func (m *LoaderManager) enterRun(loader Loader) bool {
	loaderID := strings.TrimSpace(loader.Summary.ID)
	policy := domain.NormalizeLoaderConcurrencyPolicy(loader.Summary.ConcurrencyPolicy)
	m.mu.Lock()
	defer m.mu.Unlock()
	if policy != domain.LoaderConcurrencyPolicyParallel && m.running[loaderID] > 0 {
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

func (m *LoaderManager) addLoaderEventRecord(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) (domain.LoaderEvent, error) {
	payloadJSON, err := marshalJSONCompact(payload)
	if err != nil {
		return domain.LoaderEvent{}, err
	}
	event := domain.LoaderEvent{
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
		return domain.LoaderEvent{}, err
	}
	return event, nil
}

func (h *loaderRunHost) Log(ctx context.Context, message string, payload any) error {
	return h.runtimeHost().Log(ctx, message, payload)
}

func (h *loaderRunHost) PublishEvent(ctx context.Context, topic string, payloadJSON string) (domain.TopicEventRecord, error) {
	return h.runtimeHost().PublishEvent(ctx, topic, payloadJSON)
}

func (h *loaderRunHost) StateGet(ctx context.Context, key string) (string, bool, error) {
	return h.runtimeHost().StateGet(ctx, key)
}

func (h *loaderRunHost) StateSet(ctx context.Context, key, valueJSON string) error {
	return h.runtimeHost().StateSet(ctx, key, valueJSON)
}

func (h *loaderRunHost) StateDelete(ctx context.Context, key string) error {
	return h.runtimeHost().StateDelete(ctx, key)
}

func (h *loaderRunHost) CallSessionRPC(ctx context.Context, method, requestJSON string) (string, error) {
	return h.runtimeHost().CallSessionRPC(ctx, method, requestJSON)
}

func (h *loaderRunHost) addLinkedLoaderEvent(ctx context.Context, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	event, err := h.manager.addLoaderEventRecord(ctx, h.loader.Summary.ID, h.run.ID, h.run.TriggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
	if err != nil {
		return err
	}
	h.addEventSessionLink(ctx, event, linkedSessionID, event.Type)
	return nil
}

func (h *loaderRunHost) addEventSessionLink(ctx context.Context, event domain.LoaderEvent, sessionID, relation string) {
	if h == nil || h.manager == nil || h.manager.configDB == nil || strings.TrimSpace(sessionID) == "" || h.triggerEvent.EventID == "" {
		return
	}
	if err := h.manager.configDB.AddEventSessionLink(ctx, domain.EventSessionLink{
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

func (h *loaderRunHost) cleanupCommandSessions(ctx context.Context) {
	h.runtimeHost().CleanupCommandSessions(ctx)
}

func (m *LoaderManager) loaderAgentDefinition(ctx context.Context, loader Loader) (*domain.AgentDefinition, error) {
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

func (h *loaderRunHost) Agent(ctx context.Context, prompt string, request domain.LoaderAgentRequest) (domain.LoaderAgentResult, error) {
	return h.runtimeHost().Agent(ctx, prompt, request)
}

func (h *loaderRunHost) ProjectAgent(ctx context.Context, prompt string, request domain.LoaderAgentRequest) (domain.LoaderAgentResult, error) {
	return h.runtimeHost().ProjectAgent(ctx, prompt, request)
}

func (h *loaderRunHost) Command(ctx context.Context, request domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	return h.runtimeHost().Command(ctx, request)
}

func (h *loaderRunHost) LLM(ctx context.Context, prompt string, request domain.LoaderLLMRequest) (domain.LoaderLLMResult, error) {
	return h.runtimeHost().LLM(ctx, prompt, request)
}

func loaderAgentRequestOverridesSession(request domain.LoaderAgentRequest, includeTitle bool) bool {
	return (includeTitle && strings.TrimSpace(request.Title) != "") ||
		strings.TrimSpace(request.Driver) != "" ||
		strings.TrimSpace(request.GuestImage) != "" ||
		strings.TrimSpace(request.WorkspaceID) != "" ||
		len(domain.NormalizeEnvItems(request.SessionEnv)) > 0
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
	return loaders.CloneLoader(item)
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

func marshalJSONCompact(value any) (string, error) {
	return domain.MarshalJSONCompact(value)
}

func parseLoaderTriggerEventMetadata(payloadJSON string) loaders.TriggerEventMetadata {
	return loaders.ParseTriggerEventMetadata(payloadJSON)
}

func validateLoaderPublishTopic(topic string) error {
	return loaders.ValidatePublishTopic(topic)
}

func jsonObjectDocument(payloadJSON string) bool {
	return loaders.IsJSONObject(payloadJSON)
}

func int64FromMap(values map[string]any, key string) int64 {
	return loaders.Int64FromMap(values, key)
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
