package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Store interface {
	ListDispatchableEvents(ctx context.Context, now time.Time, limit int) ([]TopicEventRecord, error)
	ClaimEvent(ctx context.Context, eventID, claimID string, now, until time.Time) (bool, error)
	ReleaseEventClaim(ctx context.Context, eventID, claimID, status, lastError string, nextAttemptAt time.Time) error
	MarkEventPublished(ctx context.Context, eventID, claimID string, dispatchedAt time.Time) error
	MarkEventNoSubscriber(ctx context.Context, eventID, claimID string, dispatchedAt time.Time) error
}

type Bus interface {
	Publish(event LoaderTopicEvent) bool
}

type LoaderTopicEvent struct {
	EventID         string
	Topic           string
	Source          string
	Provider        string
	Payload         map[string]any
	CreatedAt       time.Time
	Ack             func(context.Context) error
	NoSubscriberAck func(context.Context) error
	Retry           func(context.Context, string, time.Time) error
	Release         func()
}

type Dispatcher struct {
	rootCtx  context.Context
	store    Store
	bus      Bus
	interval time.Duration
	once     sync.Once
	mu       sync.Mutex
	inFlight map[string]struct{}
}

func NewDispatcher(rootCtx context.Context, store Store, bus Bus) *Dispatcher {
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return &Dispatcher{
		rootCtx:  rootCtx,
		store:    store,
		bus:      bus,
		interval: 500 * time.Millisecond,
		inFlight: map[string]struct{}{},
	}
}

func (d *Dispatcher) Start() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		go d.loop()
	})
}

func (d *Dispatcher) SetInterval(interval time.Duration) {
	if d == nil || interval <= 0 {
		return
	}
	d.interval = interval
}

func (d *Dispatcher) DispatchOnce(ctx context.Context, limit int) {
	d.dispatchOnce(ctx, limit)
}

func (d *Dispatcher) loop() {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-d.rootCtx.Done():
			return
		case <-timer.C:
			d.dispatchOnce(d.rootCtx, 100)
			timer.Reset(d.interval)
		}
	}
}

func (d *Dispatcher) dispatchOnce(ctx context.Context, limit int) {
	if d == nil || d.store == nil || d.bus == nil {
		return
	}
	now := time.Now().UTC()
	items, err := d.store.ListDispatchableEvents(ctx, now, limit)
	if err != nil {
		slog.Warn("failed to list pending topic events", "error", err)
		return
	}
	for _, item := range items {
		if d.isInFlight(item.ID) {
			continue
		}
		claimID := "claim_" + uuid.NewString()
		claimed, err := d.store.ClaimEvent(ctx, item.ID, claimID, now, now.Add(30*time.Second))
		if err != nil {
			slog.Warn("failed to claim event", "event_id", item.ID, "error", err)
			continue
		}
		if !claimed {
			continue
		}
		if !d.publishOne(ctx, item, claimID) {
			return
		}
	}
}

func (d *Dispatcher) publishOne(ctx context.Context, item TopicEventRecord, claimID string) bool {
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(item.PayloadJSON), &payload); err != nil {
		slog.Warn("failed to decode topic event payload", "event_id", item.ID, "topic", item.Topic, "error", err)
		_ = d.store.ReleaseEventClaim(ctx, item.ID, claimID, TopicEventDispatchDeadLetter, err.Error(), time.Time{})
		return true
	}
	d.setInFlight(item.ID)
	if ok := d.bus.Publish(LoaderTopicEvent{
		EventID:   item.ID,
		Topic:     item.Topic,
		Source:    item.Source,
		Provider:  item.Provider,
		Payload:   payload,
		CreatedAt: item.CreatedAt,
		Ack: func(ctx context.Context) error {
			defer d.clearInFlight(item.ID)
			return d.store.MarkEventPublished(ctx, item.ID, claimID, time.Now().UTC())
		},
		NoSubscriberAck: func(ctx context.Context) error {
			defer d.clearInFlight(item.ID)
			return d.store.MarkEventNoSubscriber(ctx, item.ID, claimID, time.Now().UTC())
		},
		Retry: func(ctx context.Context, reason string, nextAttemptAt time.Time) error {
			defer d.clearInFlight(item.ID)
			return d.store.ReleaseEventClaim(ctx, item.ID, claimID, TopicEventDispatchRetrying, reason, nextAttemptAt)
		},
		Release: func() {
			d.clearInFlight(item.ID)
		},
	}); !ok {
		d.clearInFlight(item.ID)
		_ = d.store.ReleaseEventClaim(ctx, item.ID, claimID, TopicEventDispatchRetrying, "loader bus is full", time.Now().UTC().Add(time.Second))
		return false
	}
	return true
}

func (d *Dispatcher) isInFlight(eventID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.inFlight[eventID]
	return ok
}

func (d *Dispatcher) setInFlight(eventID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inFlight[eventID] = struct{}{}
}

func (d *Dispatcher) clearInFlight(eventID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.inFlight, eventID)
}
