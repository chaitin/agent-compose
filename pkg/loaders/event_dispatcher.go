package loaders

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"agent-compose/pkg/events/webhooks"
	domain "agent-compose/pkg/model"
)

type EventDeliveryStore interface {
	UpsertEventDelivery(ctx context.Context, delivery domain.EventDelivery) error
}

type EventDispatcherDependencies struct {
	RootCtx      context.Context
	Store        EventDeliveryStore
	Targets      func(topic string) []EventTarget
	IsBusy       func(targets []EventTarget) bool
	ReserveSlots func(event domain.LoaderTopicEvent, count int) ([]*webhooks.Reservation, bool)
	Run          func(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options RunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error)
	Prepare      func(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options RunOptions) (PreparedRun, error)
	Execute      func(ctx context.Context, prepared PreparedRun) (domain.LoaderRunSummary, error)
	Abort        func(ctx context.Context, prepared PreparedRun, reason string)
	RunTimeout   func(time.Duration) time.Duration
	EnterRun     func(loader domain.Loader) bool
	LeaveRun     func(loaderID string)
}

type EventDispatcher struct {
	deps EventDispatcherDependencies
}

func NewEventDispatcher(deps EventDispatcherDependencies) *EventDispatcher {
	if deps.RootCtx == nil {
		deps.RootCtx = context.Background()
	}
	return &EventDispatcher{deps: deps}
}

func (d *EventDispatcher) Dispatch(event domain.LoaderTopicEvent) {
	payloadJSON, err := domain.MarshalJSONCompact(map[string]any{
		"topic":     event.Topic,
		"createdAt": event.CreatedAt.Format(time.RFC3339Nano),
		"payload":   event.Payload,
	})
	if err != nil {
		slog.Warn("failed to encode loader topic event payload", "topic", event.Topic, "error", err)
		return
	}
	targets := d.collectTargets(event.Topic)
	targets = DedupeWebhookEventTargets(event, targets)
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

func (d *EventDispatcher) AckNoSubscriber(event domain.LoaderTopicEvent) {
	d.ackNoSubscriber(event)
}

func (d *EventDispatcher) Retry(event domain.LoaderTopicEvent, reason string) {
	d.retry(event, reason)
}

func (d *EventDispatcher) ackNoSubscriber(event domain.LoaderTopicEvent) {
	ack := event.NoSubscriberAck
	if ack == nil {
		ack = event.Ack
	}
	if ack != nil {
		if err := ack(d.rootCtx()); err != nil {
			slog.Warn("failed to mark unmatched loader topic event published", "event_id", event.EventID, "topic", event.Topic, "error", err)
		}
	}
}

func (d *EventDispatcher) dispatchTargets(event domain.LoaderTopicEvent, targets []EventTarget, payloadJSON string, reservations []*webhooks.Reservation) {
	for _, target := range targets {
		d.recordMatched(event, target)
		reservation := reservations[0]
		reservations = reservations[1:]
		runCtx, cancel := context.WithTimeout(d.rootCtx(), d.runTimeout(0))
		go func(target EventTarget, payloadJSON string, topic string, ack func(context.Context) error, release func(), reservation *webhooks.Reservation) {
			defer cancel()
			defer reservation.Release()
			if _, err := d.deps.Run(runCtx, target.Loader, &target.Trigger, payloadJSON, topic, RunOptions{RetryWhenBusy: event.Source == domain.TopicEventSourceWebhook}, ack); err != nil {
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

func (d *EventDispatcher) dispatchWebhookTargets(event domain.LoaderTopicEvent, targets []EventTarget, payloadJSON string, reservations []*webhooks.Reservation) {
	acquiredLoaderIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		if !d.enterRun(target.Loader) {
			for _, loaderID := range acquiredLoaderIDs {
				d.leaveRun(loaderID)
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
		preparedRun, err := d.deps.Prepare(d.rootCtx(), target.Loader, &target.Trigger, payloadJSON, event.Topic, RunOptions{AlreadyEntered: true})
		if err != nil {
			for _, item := range prepared {
				d.deps.Abort(context.WithoutCancel(d.rootCtx()), item, err.Error())
			}
			for _, loaderID := range acquiredLoaderIDs[index+1:] {
				d.leaveRun(loaderID)
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
		if err := event.Ack(d.rootCtx()); err != nil {
			slog.Warn("failed to mark loader topic event published", "event_id", event.EventID, "topic", event.Topic, "error", err)
		}
	}
	for index, item := range prepared {
		var reservation *webhooks.Reservation
		if index < len(reservations) {
			reservation = reservations[index]
		}
		runCtx, cancel := context.WithTimeout(d.rootCtx(), d.runTimeout(0))
		go func(item PreparedRun, reservation *webhooks.Reservation) {
			defer cancel()
			defer reservation.Release()
			if _, err := d.deps.Execute(runCtx, item); err != nil {
				slog.Warn("loader event run failed", "loader_id", item.Loader.Summary.ID, "trigger_id", item.Run.TriggerID, "topic", event.Topic, "error", err)
			}
		}(item, reservation)
	}
}

func (d *EventDispatcher) recordMatched(event domain.LoaderTopicEvent, target EventTarget) {
	if event.EventID == "" || d.deps.Store == nil {
		return
	}
	if err := d.deps.Store.UpsertEventDelivery(d.rootCtx(), domain.EventDelivery{
		EventID:   event.EventID,
		LoaderID:  target.Loader.Summary.ID,
		TriggerID: target.Trigger.ID,
		Status:    domain.EventDeliveryStatusMatched,
	}); err != nil {
		slog.Warn("failed to record event delivery match", "event_id", event.EventID, "loader_id", target.Loader.Summary.ID, "trigger_id", target.Trigger.ID, "error", err)
	}
}

func (d *EventDispatcher) retry(event domain.LoaderTopicEvent, reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "loader topic event retry requested"
	}
	if event.Retry != nil {
		if err := event.Retry(d.rootCtx(), reason, time.Now().UTC().Add(time.Second)); err != nil {
			slog.Warn("failed to retry loader topic event", "event_id", event.EventID, "topic", event.Topic, "reason", reason, "error", err)
		}
		return
	}
	if event.Release != nil {
		event.Release()
	}
}

func (d *EventDispatcher) collectTargets(topic string) []EventTarget {
	if d.deps.Targets == nil {
		return nil
	}
	return d.deps.Targets(topic)
}

func (d *EventDispatcher) shouldRetryForBusy(event domain.LoaderTopicEvent, targets []EventTarget) bool {
	if event.Source != domain.TopicEventSourceWebhook || len(targets) == 0 || d.deps.IsBusy == nil {
		return false
	}
	return d.deps.IsBusy(targets)
}

func (d *EventDispatcher) reserveQueueSlots(event domain.LoaderTopicEvent, count int) ([]*webhooks.Reservation, bool) {
	if count <= 0 {
		return nil, true
	}
	if d.deps.ReserveSlots == nil {
		return webhooks.NoopReservations(count), true
	}
	return d.deps.ReserveSlots(event, count)
}

func (d *EventDispatcher) rootCtx() context.Context {
	if d.deps.RootCtx == nil {
		return context.Background()
	}
	return d.deps.RootCtx
}

func (d *EventDispatcher) runTimeout(override time.Duration) time.Duration {
	if d.deps.RunTimeout == nil {
		return 20 * time.Minute
	}
	return d.deps.RunTimeout(override)
}

func (d *EventDispatcher) enterRun(loader domain.Loader) bool {
	if d.deps.EnterRun == nil {
		return true
	}
	return d.deps.EnterRun(loader)
}

func (d *EventDispatcher) leaveRun(loaderID string) {
	if d.deps.LeaveRun != nil {
		d.deps.LeaveRun(loaderID)
	}
}
