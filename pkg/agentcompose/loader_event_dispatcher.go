package agentcompose

import (
	loaderdomain "agent-compose/internal/agentcompose/loader"
	"context"
	"log/slog"
	"strings"
	"time"
)

type LoaderEventDispatcher struct {
	manager *LoaderManager
	inner   *loaderdomain.LoaderEventDispatcher
}

func NewLoaderEventDispatcher(manager *LoaderManager) *LoaderEventDispatcher {
	return &LoaderEventDispatcher{manager: manager, inner: loaderdomain.NewLoaderEventDispatcher(manager)}
}

func (d *LoaderEventDispatcher) Dispatch(event LoaderTopicEvent) {
	d.inner.Dispatch(event)
}

func (d *LoaderEventDispatcher) reserveQueueSlots(event LoaderTopicEvent, count int) ([]*webhookQueueReservation, bool) {
	reservations, ok := d.manager.reserveQueueSlots(event, count)
	if !ok {
		return nil, false
	}
	return reservations, true
}

func (m *LoaderManager) RootContext() context.Context {
	return m.rootCtx
}

func (m *LoaderManager) SnapshotLoaders() []Loader {
	return m.snapshotLoaders()
}

func (m *LoaderManager) IsRunBusy(loader Loader) bool {
	loaderID := strings.TrimSpace(loader.Summary.ID)
	if normalizeLoaderConcurrencyPolicy(loader.Summary.ConcurrencyPolicy) == LoaderConcurrencyPolicyParallel {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running[loaderID] > 0
}

func (m *LoaderManager) ReserveQueueSlots(event LoaderTopicEvent, count int) ([]loaderdomain.QueueReservation, bool) {
	reservations, ok := m.reserveQueueSlots(event, count)
	if !ok {
		return nil, false
	}
	result := make([]loaderdomain.QueueReservation, 0, len(reservations))
	for _, reservation := range reservations {
		result = append(result, reservation)
	}
	return result, true
}

func (m *LoaderManager) reserveQueueSlots(event LoaderTopicEvent, count int) ([]*webhookQueueReservation, bool) {
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
			queue = &WebhookRunQueue{}
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

func (m *LoaderManager) RecordMatched(event LoaderTopicEvent, target loaderdomain.EventTarget) {
	if event.EventID == "" {
		return
	}
	if err := m.configDB.UpsertEventDelivery(m.rootCtx, EventDelivery{
		EventID:   event.EventID,
		LoaderID:  target.Loader.Summary.ID,
		TriggerID: target.Trigger.ID,
		Status:    EventDeliveryStatusMatched,
	}); err != nil {
		slog.Warn("failed to record event delivery match", "event_id", event.EventID, "loader_id", target.Loader.Summary.ID, "trigger_id", target.Trigger.ID, "error", err)
	}
}

func (m *LoaderManager) RunLoader(ctx context.Context, loader Loader, trigger *LoaderTrigger, payloadJSON, source string, options loaderdomain.RunOptions, triggerEventAck ...func(context.Context) error) (LoaderRunSummary, error) {
	return m.runLoader(ctx, loader, trigger, payloadJSON, source, true, loaderRunOptions{retryWhenBusy: options.RetryWhenBusy, alreadyEntered: options.AlreadyEntered}, triggerEventAck...)
}

func (m *LoaderManager) PrepareLoaderRun(ctx context.Context, loader Loader, trigger *LoaderTrigger, payloadJSON, source string, options loaderdomain.RunOptions) (loaderdomain.PreparedRun, error) {
	prepared, err := m.prepareLoaderRun(ctx, loader, trigger, payloadJSON, source, loaderRunOptions{retryWhenBusy: options.RetryWhenBusy, alreadyEntered: options.AlreadyEntered})
	if err != nil {
		return loaderdomain.PreparedRun{}, err
	}
	return toDomainPreparedRun(prepared), nil
}

func (m *LoaderManager) ExecutePreparedRun(ctx context.Context, prepared loaderdomain.PreparedRun) (LoaderRunSummary, error) {
	return m.executePreparedLoaderRun(ctx, fromDomainPreparedRun(prepared))
}

func (m *LoaderManager) AbortPreparedRun(ctx context.Context, prepared loaderdomain.PreparedRun, reason string) {
	m.abortPreparedLoaderRun(ctx, fromDomainPreparedRun(prepared), reason)
}

func (m *LoaderManager) LoaderRunTimeout(override time.Duration) time.Duration {
	return m.loaderRunTimeout(override)
}
