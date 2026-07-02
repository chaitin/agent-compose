package loaders

import "context"

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

func (h *loaderRunHost) AddLinkedLoaderEvent(ctx context.Context, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	if h == nil {
		return nil
	}
	return h.addLinkedLoaderEvent(ctx, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
}
