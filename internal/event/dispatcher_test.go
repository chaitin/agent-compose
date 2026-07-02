//go:build event_sqlite_legacy

package event

import (
	"context"
	"testing"
	"time"
)

func TestEventDispatcherPublishesPendingEvents(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	bus := &LoaderBus{ch: make(chan LoaderTopicEvent, 4)}
	dispatcher := NewEventDispatcher(ctx, store, bus)

	created, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:         "webhook.dispatch.test",
		Source:        TopicEventSourceWebhook,
		CorrelationID: "corr-dispatch",
		PayloadJSON:   `{"eventId":"evt-test","correlationId":"corr-dispatch","body":{"value":1}}`,
		CreatedAt:     time.Now().UTC().Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	dispatcher.dispatchOnce(ctx, 10)

	loaded, err := store.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent before consume returned error: %v", err)
	}
	if loaded.DispatchStatus != TopicEventDispatchPublishing {
		t.Fatalf("dispatch status before consume = %q, want publishing", loaded.DispatchStatus)
	}

	select {
	case event := <-bus.Events():
		if event.Topic != created.Topic {
			t.Fatalf("event topic = %q, want %q", event.Topic, created.Topic)
		}
		if event.Payload["correlationId"] != "corr-dispatch" {
			t.Fatalf("event payload = %#v", event.Payload)
		}
		if event.EventID != created.ID {
			t.Fatalf("event id = %q, want %q", event.EventID, created.ID)
		}
		if event.Ack == nil {
			t.Fatalf("event ack was nil")
		}
		if event.NoSubscriberAck == nil {
			t.Fatalf("event no subscriber ack was nil")
		}
	default:
		t.Fatalf("expected published loader topic event")
	}

	loaded, err = store.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if loaded.DispatchStatus != TopicEventDispatchPublishing {
		t.Fatalf("dispatch status after consume without ack = %q, want publishing", loaded.DispatchStatus)
	}
	if !loaded.DispatchedAt.IsZero() {
		t.Fatalf("dispatched at = %s, want zero", loaded.DispatchedAt)
	}
}

func TestEventDispatcherKeepsPendingWhenBusFull(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	bus := &LoaderBus{ch: make(chan LoaderTopicEvent, 1)}
	if !bus.Publish(LoaderTopicEvent{Topic: "preloaded", CreatedAt: time.Now().UTC()}) {
		t.Fatalf("failed to preload bus")
	}
	dispatcher := NewEventDispatcher(ctx, store, bus)
	created, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:         "webhook.dispatch.full",
		Source:        TopicEventSourceWebhook,
		CorrelationID: "corr-full",
		PayloadJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	dispatcher.dispatchOnce(ctx, 10)

	loaded, err := store.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if loaded.DispatchStatus != TopicEventDispatchRetrying {
		t.Fatalf("dispatch status = %q, want retrying", loaded.DispatchStatus)
	}
	if loaded.LastError == "" || loaded.NextAttemptAt.IsZero() {
		t.Fatalf("retry metadata missing: %#v", loaded)
	}
}

func TestEventDispatcherIgnoresStaleClaimAck(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	bus := &LoaderBus{ch: make(chan LoaderTopicEvent, 4)}
	dispatcher := NewEventDispatcher(ctx, store, bus)

	created, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:         "webhook.dispatch.stale",
		Source:        TopicEventSourceWebhook,
		CorrelationID: "corr-stale",
		PayloadJSON:   `{}`,
		CreatedAt:     time.Now().UTC().Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	dispatcher.dispatchOnce(ctx, 10)

	var delivered LoaderTopicEvent
	select {
	case delivered = <-bus.Events():
		if delivered.Ack == nil {
			t.Fatalf("event ack was nil")
		}
	default:
		t.Fatalf("expected published loader topic event")
	}

	claimed, err := store.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if claimed.ClaimID == "" {
		t.Fatalf("claim id was empty after dispatch: %#v", claimed)
	}
	expiredAt := time.Now().UTC().Add(-time.Second)
	if _, err := store.db.ExecContext(ctx, `UPDATE event SET claim_until = ? WHERE id = ?`, expiredAt.UnixMilli(), created.ID); err != nil {
		t.Fatalf("expire claim: %v", err)
	}
	if ok, err := store.ClaimEvent(ctx, created.ID, "fresh-claim", time.Now().UTC(), time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("fresh ClaimEvent returned error: %v", err)
	} else if !ok {
		t.Fatalf("fresh ClaimEvent returned false")
	}

	if err := delivered.Ack(ctx); err != nil {
		t.Fatalf("stale ack returned error: %v", err)
	}
	loaded, err := store.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent after stale ack returned error: %v", err)
	}
	if loaded.ClaimID != "fresh-claim" || loaded.DispatchStatus != TopicEventDispatchPublishing {
		t.Fatalf("stale ack changed active claim: %#v", loaded)
	}
	if !loaded.DispatchedAt.IsZero() {
		t.Fatalf("stale ack set dispatched_at = %s", loaded.DispatchedAt)
	}
}
