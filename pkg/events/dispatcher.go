package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	domain "agent-compose/pkg/model"
)

type Store interface {
	ListDispatchableEvents(context.Context, time.Time, int) ([]domain.TopicEventRecord, error)
	ClaimEvent(context.Context, string, string, time.Time, time.Time) (bool, error)
	ReleaseEventClaim(context.Context, string, string, string, string, time.Time) error
	MarkEventPublished(context.Context, string, string, time.Time) error
	MarkEventNoSubscriber(context.Context, string, string, time.Time) error
}

type Bus interface {
	Publish(domain.LoaderTopicEvent) bool
}

type Dispatcher struct {
	rootCtx  context.Context
	configDB Store
	bus      Bus
	interval time.Duration
	once     sync.Once
	mu       sync.Mutex
	inFlight map[string]struct{}
}

func NewDispatcher(rootCtx context.Context, configDB Store, bus Bus) *Dispatcher {
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	return &Dispatcher{
		rootCtx:  rootCtx,
		configDB: configDB,
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

func (d *Dispatcher) loop() {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-d.rootCtx.Done():
			return
		case <-timer.C:
			d.DispatchOnce(d.rootCtx, 100)
			timer.Reset(d.interval)
		}
	}
}

func (d *Dispatcher) DispatchOnce(ctx context.Context, limit int) {
	if d == nil || d.configDB == nil || d.bus == nil {
		return
	}
	now := time.Now().UTC()
	items, err := d.configDB.ListDispatchableEvents(ctx, now, limit)
	if err != nil {
		slog.Warn("failed to list pending topic events", "error", err)
		return
	}
	for _, item := range items {
		if d.isInFlight(item.ID) {
			continue
		}
		claimID := "claim_" + uuid.NewString()
		claimed, err := d.configDB.ClaimEvent(ctx, item.ID, claimID, now, now.Add(30*time.Second))
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

func (d *Dispatcher) publishOne(ctx context.Context, item domain.TopicEventRecord, claimID string) bool {
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(item.PayloadJSON), &payload); err != nil {
		slog.Warn("failed to decode topic event payload", "event_id", item.ID, "topic", item.Topic, "error", err)
		_ = d.configDB.ReleaseEventClaim(ctx, item.ID, claimID, domain.TopicEventDispatchDeadLetter, err.Error(), time.Time{})
		return true
	}
	d.setInFlight(item.ID)
	if ok := d.bus.Publish(domain.LoaderTopicEvent{
		EventID:   item.ID,
		Topic:     item.Topic,
		Source:    item.Source,
		Provider:  item.Provider,
		Payload:   payload,
		CreatedAt: item.CreatedAt,
		Ack: func(ctx context.Context) error {
			defer d.clearInFlight(item.ID)
			return d.configDB.MarkEventPublished(ctx, item.ID, claimID, time.Now().UTC())
		},
		NoSubscriberAck: func(ctx context.Context) error {
			defer d.clearInFlight(item.ID)
			return d.configDB.MarkEventNoSubscriber(ctx, item.ID, claimID, time.Now().UTC())
		},
		Retry: func(ctx context.Context, reason string, nextAttemptAt time.Time) error {
			defer d.clearInFlight(item.ID)
			return d.configDB.ReleaseEventClaim(ctx, item.ID, claimID, domain.TopicEventDispatchRetrying, reason, nextAttemptAt)
		},
		Release: func() {
			d.clearInFlight(item.ID)
		},
	}); !ok {
		d.clearInFlight(item.ID)
		_ = d.configDB.ReleaseEventClaim(ctx, item.ID, claimID, domain.TopicEventDispatchRetrying, "loader bus is full", time.Now().UTC().Add(time.Second))
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

func (d *Dispatcher) SetInterval(interval time.Duration) {
	if d == nil || interval <= 0 {
		return
	}
	d.interval = interval
}
