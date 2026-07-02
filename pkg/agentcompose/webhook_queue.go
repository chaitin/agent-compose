package agentcompose

import (
	eventsdomain "agent-compose/internal/agentcompose/events"
	appconfig "agent-compose/internal/config"
)

type WebhookRunQueue struct {
	inner *eventsdomain.WebhookRunQueue

	defaultWorkers int
	running        map[string]int
}

type webhookQueueReservation = eventsdomain.WebhookQueueReservation

func noopWebhookQueueReservations(count int) []*webhookQueueReservation {
	return eventsdomain.NoopWebhookQueueReservations(count)
}

func newWebhookRunQueueFromConfig(config *appconfig.Config) (*WebhookRunQueue, error) {
	inner, err := eventsdomain.NewWebhookRunQueueFromConfig(config)
	if err != nil {
		return nil, err
	}
	return &WebhookRunQueue{inner: inner}, nil
}

func (q *WebhookRunQueue) Reserve(event LoaderTopicEvent) (*webhookQueueReservation, bool) {
	inner := q.ensureInner()
	if inner == nil {
		return &webhookQueueReservation{}, true
	}
	return inner.Reserve(eventsdomain.WebhookQueueEvent{
		Topic:    event.Topic,
		Provider: event.Provider,
		Payload:  event.Payload,
	})
}

func (q *WebhookRunQueue) ensureInner() *eventsdomain.WebhookRunQueue {
	if q == nil {
		return nil
	}
	if q.inner == nil {
		q.inner = &eventsdomain.WebhookRunQueue{DefaultWorkers: q.defaultWorkers}
	}
	return q.inner
}
