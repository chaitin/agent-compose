package loaders

import "context"

// LoaderRunHost exposes the loader host method set to packages that still carry
// compatibility tests from the pre-split agentcompose package.
type LoaderRunHost = loaderRunHost

// LoaderTriggerEventMetadata exposes trigger lineage metadata for compatibility tests.
type LoaderTriggerEventMetadata = loaderTriggerEventMetadata

func NewLoaderRunHost(manager *LoaderManager, loader Loader, run *LoaderRunSummary, triggerEvent LoaderTriggerEventMetadata) *LoaderRunHost {
	return &loaderRunHost{
		manager:      manager,
		loader:       loader,
		run:          run,
		triggerEvent: triggerEvent,
	}
}

func (m *LoaderManager) EventQueue() *WebhookRunQueue {
	if m == nil {
		return nil
	}
	return m.eventQueue
}

func (m *LoaderManager) Bus() *LoaderBus {
	if m == nil {
		return nil
	}
	return m.bus
}

func (m *LoaderManager) SetEngine(engine LoaderEngine) {
	if m == nil {
		return
	}
	m.engine = engine
}

func (m *LoaderManager) RunEventLoop() {
	if m == nil {
		return
	}
	m.eventLoop()
}

func (m *LoaderManager) ReserveEventQueueSlots(event LoaderTopicEvent, count int) ([]*webhookQueueReservation, bool) {
	if m == nil {
		return nil, false
	}
	return m.reserveEventQueueSlots(event, count)
}

func (m *LoaderManager) SetRunningCounts(running map[string]int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running = map[string]int{}
	for loaderID, count := range running {
		m.running[loaderID] = count
	}
}

func (m *LoaderManager) HasStartupState() bool {
	return m != nil && m.rootCtx != nil && m.scheduleWake != nil
}

func (m *LoaderManager) AddLoaderEventForCompat(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	if m == nil {
		return nil
	}
	return m.addLoaderEvent(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
}

func (h *loaderRunHost) AddLinkedLoaderEvent(ctx context.Context, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	if h == nil {
		return nil
	}
	return h.addLinkedLoaderEvent(ctx, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
}

func ValidateLoaderPublishTopic(topic string) error {
	return validateLoaderPublishTopic(topic)
}

func JSONObjectDocument(payloadJSON string) bool {
	return jsonObjectDocument(payloadJSON)
}

func Int64FromMap(values map[string]any, key string) int64 {
	return int64FromMap(values, key)
}

func NormalizeLoaderRuntime(runtime string) (string, error) {
	return normalizeLoaderRuntime(runtime)
}

func NormalizeLoaderTriggerKind(kind string) (string, error) {
	return normalizeLoaderTriggerKind(kind)
}

func NormalizeLoaderSessionPolicy(policy string) string {
	return normalizeLoaderSessionPolicy(policy)
}

func NormalizeLoaderConcurrencyPolicy(policy string) string {
	return normalizeLoaderConcurrencyPolicy(policy)
}

func NormalizeLoaderRunStatus(status string) string {
	return normalizeLoaderRunStatus(status)
}

func LoaderSessionRPCLinkedSessionID(method, requestJSON, responseJSON string) string {
	return loaderSessionRPCLinkedSessionID(method, requestJSON, responseJSON)
}

func LoaderSessionIDFromJSON(raw string) string {
	return loaderSessionIDFromJSON(raw)
}

func CellTopicPayload(sessionID string, cell NotebookCell, source string) map[string]any {
	return cellTopicPayload(sessionID, cell, source)
}

func LoaderCommandEventPayload(request LoaderCommandRequest, result LoaderCommandResult) map[string]any {
	return loaderCommandEventPayload(request, result)
}
