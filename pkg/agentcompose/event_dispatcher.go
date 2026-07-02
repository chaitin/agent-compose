package agentcompose

import (
	"agent-compose/internal/agentcompose/events"
	"context"
	"time"
)

type EventDispatcher struct {
	inner    *events.Dispatcher
	interval time.Duration
}

func NewEventDispatcher(rootCtx context.Context, configDB *ConfigStore, bus *LoaderBus) *EventDispatcher {
	return &EventDispatcher{
		inner:    events.NewDispatcher(rootCtx, configDB, loaderTopicEventBus{bus: bus}),
		interval: 500 * time.Millisecond,
	}
}

func (d *EventDispatcher) Start() {
	if d == nil || d.inner == nil {
		return
	}
	d.inner.SetInterval(d.interval)
	d.inner.Start()
}

func (d *EventDispatcher) dispatchOnce(ctx context.Context, limit int) {
	if d == nil || d.inner == nil {
		return
	}
	d.inner.DispatchOnce(ctx, limit)
}

type loaderTopicEventBus struct {
	bus *LoaderBus
}

func (b loaderTopicEventBus) Publish(event events.LoaderTopicEvent) bool {
	if b.bus == nil {
		return false
	}
	return b.bus.Publish(LoaderTopicEvent{
		EventID:         event.EventID,
		Topic:           event.Topic,
		Source:          event.Source,
		Provider:        event.Provider,
		Payload:         event.Payload,
		CreatedAt:       event.CreatedAt,
		Ack:             event.Ack,
		NoSubscriberAck: event.NoSubscriberAck,
		Retry:           event.Retry,
		Release:         event.Release,
	})
}

var _ events.Store = (*ConfigStore)(nil)
var _ events.Bus = loaderTopicEventBus{}
