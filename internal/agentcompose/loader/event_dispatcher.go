package loader

import (
	"agent-compose/internal/agentcompose/events"
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

type LoaderEventDispatcher struct {
	host EventDispatcherHost
}

type EventDispatcherHost interface {
	RootContext() context.Context
	SnapshotLoaders() []Definition
	IsRunBusy(loader Definition) bool
	ReserveQueueSlots(event TopicEvent, count int) ([]QueueReservation, bool)
	RecordMatched(event TopicEvent, target EventTarget)
	RunLoader(ctx context.Context, loader Definition, trigger *Trigger, payloadJSON, source string, options RunOptions, triggerEventAck ...func(context.Context) error) (RunSummary, error)
	EnterRun(loader Definition) bool
	LeaveRun(loaderID string)
	PrepareLoaderRun(ctx context.Context, loader Definition, trigger *Trigger, payloadJSON, source string, options RunOptions) (PreparedRun, error)
	ExecutePreparedRun(ctx context.Context, prepared PreparedRun) (RunSummary, error)
	AbortPreparedRun(ctx context.Context, prepared PreparedRun, reason string)
	LoaderRunTimeout(override time.Duration) time.Duration
}

type QueueReservation interface {
	Release()
}

type EventTarget struct {
	Loader  Definition
	Trigger Trigger
}

func NewLoaderEventDispatcher(host EventDispatcherHost) *LoaderEventDispatcher {
	return &LoaderEventDispatcher{host: host}
}

func (d *LoaderEventDispatcher) Dispatch(event TopicEvent) {
	payloadJSON, err := MarshalJSONCompact(map[string]any{
		"topic":     event.Topic,
		"createdAt": event.CreatedAt.Format(time.RFC3339Nano),
		"payload":   event.Payload,
	})
	if err != nil {
		slog.Warn("failed to encode loader topic event payload", "topic", event.Topic, "error", err)
		return
	}
	targets := d.collectTargets(event.Topic)
	targets = dedupeWebhookEventTargets(event, targets)
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
	if event.Source == events.TopicEventSourceWebhook {
		d.dispatchWebhookTargets(event, targets, payloadJSON, reservations)
		return
	}
	d.dispatchTargets(event, targets, payloadJSON, reservations)
}

func (d *LoaderEventDispatcher) ackNoSubscriber(event TopicEvent) {
	ack := event.NoSubscriberAck
	if ack == nil {
		ack = event.Ack
	}
	if ack != nil {
		if err := ack(d.host.RootContext()); err != nil {
			slog.Warn("failed to mark unmatched loader topic event published", "event_id", event.EventID, "topic", event.Topic, "error", err)
		}
	}
}

func dedupeWebhookEventTargets(event TopicEvent, targets []EventTarget) []EventTarget {
	if event.Source != events.TopicEventSourceWebhook || len(targets) <= 1 {
		return targets
	}
	seen := map[string]struct{}{}
	deduped := make([]EventTarget, 0, len(targets))
	for _, target := range targets {
		loaderID := strings.TrimSpace(target.Loader.Summary.ID)
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

func (d *LoaderEventDispatcher) dispatchTargets(event TopicEvent, targets []EventTarget, payloadJSON string, reservations []QueueReservation) {
	m := d.host
	for _, target := range targets {
		m.RecordMatched(event, target)
		reservation := reservations[0]
		reservations = reservations[1:]
		runCtx, cancel := context.WithTimeout(m.RootContext(), m.LoaderRunTimeout(0))
		go func(target EventTarget, payloadJSON string, topic string, ack func(context.Context) error, release func(), reservation QueueReservation) {
			defer cancel()
			defer reservation.Release()
			if _, err := m.RunLoader(runCtx, target.Loader, &target.Trigger, payloadJSON, topic, RunOptions{RetryWhenBusy: event.Source == events.TopicEventSourceWebhook}, ack); err != nil {
				if errors.Is(err, ErrRunBusyForRetry) {
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

func (d *LoaderEventDispatcher) dispatchWebhookTargets(event TopicEvent, targets []EventTarget, payloadJSON string, reservations []QueueReservation) {
	m := d.host
	acquiredLoaderIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		if !m.EnterRun(target.Loader) {
			for _, loaderID := range acquiredLoaderIDs {
				m.LeaveRun(loaderID)
			}
			for _, reservation := range reservations {
				reservation.Release()
			}
			d.retry(event, "loader is already running")
			return
		}
		acquiredLoaderIDs = append(acquiredLoaderIDs, target.Loader.Summary.ID)
	}
	prepared := make([]PreparedRun, 0, len(targets))
	for index, target := range targets {
		preparedRun, err := m.PrepareLoaderRun(m.RootContext(), target.Loader, &target.Trigger, payloadJSON, event.Topic, RunOptions{AlreadyEntered: true})
		if err != nil {
			for _, item := range prepared {
				m.AbortPreparedRun(context.WithoutCancel(m.RootContext()), item, err.Error())
			}
			for _, loaderID := range acquiredLoaderIDs[index+1:] {
				m.LeaveRun(loaderID)
			}
			for _, reservation := range reservations {
				reservation.Release()
			}
			reason := err.Error()
			if errors.Is(err, ErrRunBusyForRetry) {
				reason = "loader is already running"
			}
			d.retry(event, reason)
			return
		}
		prepared = append(prepared, preparedRun)
	}
	if event.Ack != nil {
		if err := event.Ack(m.RootContext()); err != nil {
			slog.Warn("failed to mark loader topic event published", "event_id", event.EventID, "topic", event.Topic, "error", err)
		}
	}
	for index, item := range prepared {
		var reservation QueueReservation
		if index < len(reservations) {
			reservation = reservations[index]
		}
		runCtx, cancel := context.WithTimeout(m.RootContext(), m.LoaderRunTimeout(0))
		go func(item PreparedRun, reservation QueueReservation) {
			defer cancel()
			defer reservation.Release()
			if _, err := m.ExecutePreparedRun(runCtx, item); err != nil {
				slog.Warn("loader event run failed", "loader_id", item.Loader.Summary.ID, "trigger_id", item.Run.TriggerID, "topic", event.Topic, "error", err)
			}
		}(item, reservation)
	}
}

func (d *LoaderEventDispatcher) retry(event TopicEvent, reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "loader topic event retry requested"
	}
	if event.Retry != nil {
		if err := event.Retry(d.host.RootContext(), reason, time.Now().UTC().Add(time.Second)); err != nil {
			slog.Warn("failed to retry loader topic event", "event_id", event.EventID, "topic", event.Topic, "reason", reason, "error", err)
		}
		return
	}
	if event.Release != nil {
		event.Release()
	}
}

func (d *LoaderEventDispatcher) collectTargets(topic string) []EventTarget {
	targets := make([]EventTarget, 0)
	for _, loader := range d.host.SnapshotLoaders() {
		if !loader.Summary.Enabled {
			continue
		}
		for _, trigger := range loader.Triggers {
			if !trigger.Enabled || trigger.Kind != TriggerKindEvent || !TriggerTopicMatches(trigger.Topic, topic) {
				continue
			}
			targets = append(targets, EventTarget{
				Loader:  loader,
				Trigger: trigger,
			})
		}
	}
	return targets
}

func (d *LoaderEventDispatcher) shouldRetryForBusy(event TopicEvent, targets []EventTarget) bool {
	if event.Source != events.TopicEventSourceWebhook || len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if d.host.IsRunBusy(target.Loader) {
			return true
		}
	}
	return false
}

func (d *LoaderEventDispatcher) reserveQueueSlots(event TopicEvent, count int) ([]QueueReservation, bool) {
	return d.host.ReserveQueueSlots(event, count)
}
