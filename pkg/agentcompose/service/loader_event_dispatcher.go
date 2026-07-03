package agentcompose

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"agent-compose/pkg/events/webhooks"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

type LoaderEventDispatcher struct {
	manager *LoaderManager
}

func NewLoaderEventDispatcher(manager *LoaderManager) *LoaderEventDispatcher {
	return &LoaderEventDispatcher{manager: manager}
}

func (d *LoaderEventDispatcher) Dispatch(event domain.LoaderTopicEvent) {
	payloadJSON, err := marshalJSONCompact(map[string]any{
		"topic":     event.Topic,
		"createdAt": event.CreatedAt.Format(time.RFC3339Nano),
		"payload":   event.Payload,
	})
	if err != nil {
		slog.Warn("failed to encode loader topic event payload", "topic", event.Topic, "error", err)
		return
	}
	targets := d.collectTargets(event.Topic)
	targets = loaders.DedupeWebhookEventTargets(event, targets)
	if len(targets) == 0 {
		d.ackNoSubscriber(event)
		return
	}
	if d.shouldRetryForBusy(event, targets) {
		d.retry(event, "loader is already running")
		return
	}
	reservations, ok := d.reserveQueueSlots(event, len(targets))
	if !ok {
		d.retry(event, "webhook queue is full")
		return
	}
	if event.Source == domain.TopicEventSourceWebhook {
		d.dispatchWebhookTargets(event, targets, payloadJSON, reservations)
		return
	}
	d.dispatchTargets(event, targets, payloadJSON, reservations)
}

func (d *LoaderEventDispatcher) ackNoSubscriber(event domain.LoaderTopicEvent) {
	m := d.manager
	ack := event.NoSubscriberAck
	if ack == nil {
		ack = event.Ack
	}
	if ack != nil {
		if err := ack(m.rootCtx); err != nil {
			slog.Warn("failed to mark unmatched loader topic event published", "event_id", event.EventID, "topic", event.Topic, "error", err)
		}
	}
}

func (d *LoaderEventDispatcher) dispatchTargets(event domain.LoaderTopicEvent, targets []loaders.EventTarget, payloadJSON string, reservations []*webhooks.Reservation) {
	m := d.manager
	for _, target := range targets {
		d.recordMatched(event, target)
		reservation := reservations[0]
		reservations = reservations[1:]
		runCtx, cancel := context.WithTimeout(m.rootCtx, m.loaderRunTimeout(0))
		go func(target loaders.EventTarget, payloadJSON string, topic string, ack func(context.Context) error, release func(), reservation *webhooks.Reservation) {
			defer cancel()
			defer reservation.Release()
			if _, err := m.runLoader(runCtx, target.Loader, &target.Trigger, payloadJSON, topic, true, loaderRunOptions{retryWhenBusy: event.Source == domain.TopicEventSourceWebhook}, ack); err != nil {
				if errors.Is(err, errLoaderRunBusyForRetry) {
					d.retry(event, "loader is already running")
					return
				}
				slog.Warn("loader event run failed", "loader_id", target.Loader.Summary.ID, "trigger_id", target.Trigger.ID, "topic", topic, "error", err)
				if release != nil {
					release()
				}
			}
		}(target, payloadJSON, event.Topic, event.Ack, event.Release, reservation)
	}
}

func (d *LoaderEventDispatcher) dispatchWebhookTargets(event domain.LoaderTopicEvent, targets []loaders.EventTarget, payloadJSON string, reservations []*webhooks.Reservation) {
	m := d.manager
	acquiredLoaderIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		if !m.enterRun(target.Loader) {
			for _, loaderID := range acquiredLoaderIDs {
				m.leaveRun(loaderID)
			}
			for _, reservation := range reservations {
				reservation.Release()
			}
			d.retry(event, "loader is already running")
			return
		}
		acquiredLoaderIDs = append(acquiredLoaderIDs, target.Loader.Summary.ID)
	}
	prepared := make([]preparedLoaderRun, 0, len(targets))
	for index, target := range targets {
		preparedRun, err := m.prepareLoaderRun(m.rootCtx, target.Loader, &target.Trigger, payloadJSON, event.Topic, loaderRunOptions{alreadyEntered: true})
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
			d.retry(event, reason)
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
		var reservation *webhooks.Reservation
		if index < len(reservations) {
			reservation = reservations[index]
		}
		runCtx, cancel := context.WithTimeout(m.rootCtx, m.loaderRunTimeout(0))
		go func(item preparedLoaderRun, reservation *webhooks.Reservation) {
			defer cancel()
			defer reservation.Release()
			if _, err := m.executePreparedLoaderRun(runCtx, item); err != nil {
				slog.Warn("loader event run failed", "loader_id", item.loader.Summary.ID, "trigger_id", item.run.TriggerID, "topic", event.Topic, "error", err)
			}
		}(item, reservation)
	}
}

func (d *LoaderEventDispatcher) recordMatched(event domain.LoaderTopicEvent, target loaders.EventTarget) {
	if event.EventID == "" {
		return
	}
	if err := d.manager.configDB.UpsertEventDelivery(d.manager.rootCtx, domain.EventDelivery{
		EventID:   event.EventID,
		LoaderID:  target.Loader.Summary.ID,
		TriggerID: target.Trigger.ID,
		Status:    domain.EventDeliveryStatusMatched,
	}); err != nil {
		slog.Warn("failed to record event delivery match", "event_id", event.EventID, "loader_id", target.Loader.Summary.ID, "trigger_id", target.Trigger.ID, "error", err)
	}
}

func (d *LoaderEventDispatcher) retry(event domain.LoaderTopicEvent, reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "loader topic event retry requested"
	}
	if event.Retry != nil {
		if err := event.Retry(d.manager.rootCtx, reason, time.Now().UTC().Add(time.Second)); err != nil {
			slog.Warn("failed to retry loader topic event", "event_id", event.EventID, "topic", event.Topic, "reason", reason, "error", err)
		}
		return
	}
	if event.Release != nil {
		event.Release()
	}
}

func (d *LoaderEventDispatcher) collectTargets(topic string) []loaders.EventTarget {
	return loaders.CollectEventTargets(d.manager.snapshotLoaders(), topic)
}

func (d *LoaderEventDispatcher) shouldRetryForBusy(event domain.LoaderTopicEvent, targets []loaders.EventTarget) bool {
	if event.Source != domain.TopicEventSourceWebhook || len(targets) == 0 {
		return false
	}
	m := d.manager
	m.mu.RLock()
	defer m.mu.RUnlock()
	return loaders.AnyTargetBusy(targets, m.running)
}

func (d *LoaderEventDispatcher) reserveQueueSlots(event domain.LoaderTopicEvent, count int) ([]*webhooks.Reservation, bool) {
	m := d.manager
	if count <= 0 {
		return nil, true
	}
	if event.Source != domain.TopicEventSourceWebhook {
		return webhooks.NoopReservations(count), true
	}
	if m.eventQueue == nil {
		queue, err := webhooks.NewRunQueueFromConfig(m.config)
		if err != nil {
			slog.Warn("failed to initialize webhook queue config", "error", err)
			queue = webhooks.NewRunQueue(0)
		}
		m.eventQueue = queue
	}
	reservations := make([]*webhooks.Reservation, 0, count)
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
