package events

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"

	_ "modernc.org/sqlite"
)

func TestDispatcherDispatchOnceAckRetryAndDecodeWorkflows(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := &dispatcherCoverageStore{events: []domain.TopicEventRecord{
		{ID: "event-1", Topic: "topic.one", PayloadJSON: `{"ok":true}`, CreatedAt: now},
		{ID: "event-bad", Topic: "topic.bad", PayloadJSON: `{`, CreatedAt: now},
	}}
	bus := &dispatcherCoverageBus{}
	dispatcher := NewDispatcher(ctx, store, bus)
	dispatcher.SetInterval(time.Millisecond)
	dispatcher.DispatchOnce(ctx, 10)
	if len(bus.events) != 1 || bus.events[0].Topic != "topic.one" {
		t.Fatalf("bus events = %#v", bus.events)
	}
	if err := bus.events[0].Ack(ctx); err != nil {
		t.Fatalf("Ack returned error: %v", err)
	}
	if store.published != 1 || store.released == 0 {
		t.Fatalf("store = %#v", store)
	}

	store.events = []domain.TopicEventRecord{{ID: "event-2", Topic: "topic.two", PayloadJSON: `{"retry":true}`}}
	bus.publishOK = false
	dispatcher.DispatchOnce(ctx, 10)
	if store.retrying == 0 {
		t.Fatalf("expected retry release, store=%#v", store)
	}
	dispatcher.setInFlight("event-3")
	if !dispatcher.isInFlight("event-3") {
		t.Fatalf("expected in-flight event")
	}
	dispatcher.clearInFlight("event-3")
	if dispatcher.isInFlight("event-3") {
		t.Fatalf("expected in-flight event cleared")
	}
	(*Dispatcher)(nil).Start()
	(*Dispatcher)(nil).DispatchOnce(ctx, 10)
	(*Dispatcher)(nil).SetInterval(time.Second)
}

func TestDispatcherCarriesPersistedPublisherProject(t *testing.T) {
	ctx := context.Background()
	store := &dispatcherCoverageStore{events: []domain.TopicEventRecord{
		{ID: "event-project", Topic: "workflow.x.ready", Source: domain.TopicEventSourceScheduler, PublisherProjectID: "project-a", PayloadJSON: `{}`},
	}}
	bus := &dispatcherCoverageBus{}
	NewDispatcher(ctx, store, bus).DispatchOnce(ctx, 10)
	if len(bus.events) != 1 || bus.events[0].PublisherProjectID != "project-a" {
		t.Fatalf("bus events = %#v, want publisher project-a", bus.events)
	}
}

func TestNormalizeTopicEventScanHelpers(t *testing.T) {
	if sql := SelectTopicEventSQL(); sql == "" {
		t.Fatalf("SelectTopicEventSQL returned empty")
	}
	_, err := ScanTopicEvent(func(dest ...any) error { return errors.New("scan") })
	if err == nil {
		t.Fatalf("expected scan error")
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open returned error: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE event (
		sequence INTEGER, id TEXT, topic TEXT, source TEXT, provider TEXT, intent TEXT, correlation_id TEXT,
		idempotency_key TEXT, delivery_id TEXT, payload_hash TEXT, payload_json TEXT, dispatch_status TEXT,
		parent_event_id TEXT, publisher_type TEXT, publisher_id TEXT, publisher_run_id TEXT, publisher_project_id TEXT, replay_of_event_id TEXT,
		claim_id TEXT, claim_until INTEGER, attempt_count INTEGER, next_attempt_at INTEGER, last_error TEXT,
		dead_letter_at INTEGER, created_at INTEGER, dispatched_at INTEGER
	)`); err != nil {
		t.Fatalf("create event table: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO event (
		sequence, id, topic, source, provider, intent, correlation_id, idempotency_key, delivery_id,
		payload_hash, payload_json, dispatch_status, parent_event_id, publisher_type, publisher_id,
		publisher_run_id, publisher_project_id, replay_of_event_id, claim_id, claim_until, attempt_count, next_attempt_at,
		last_error, dead_letter_at, created_at, dispatched_at
	) VALUES (1, 'event-1', 'runtime.topic', 'source', 'provider', 'intent', 'corr', 'idem', 'delivery',
		'hash', '{"ok":true}', 'pending', '', 'scheduler', 'scheduler-1', 'run-1', 'project-1', '', 'claim-1',
		1700000000, 2, 1700000001, '', 0, 1700000002, 1700000003)`); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	rows, err := db.QueryContext(context.Background(), SelectTopicEventSQL())
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer func() { _ = rows.Close() }()
	items, err := ScanTopicEvents(rows)
	if err != nil {
		t.Fatalf("ScanTopicEvents returned error: %v", err)
	}
	if len(items) != 1 || items[0].ID != "event-1" || items[0].AttemptCount != 2 || items[0].ClaimUntil.IsZero() || items[0].PublisherProjectID != "project-1" {
		t.Fatalf("scanned events = %#v", items)
	}
}

func TestIntegrationDispatcherDispatchOnceAckRetryAndDecodeWorkflows(t *testing.T) {
	TestDispatcherDispatchOnceAckRetryAndDecodeWorkflows(t)
}

func TestE2EDispatcherDispatchOnceAckRetryAndDecodeWorkflows(t *testing.T) {
	TestDispatcherDispatchOnceAckRetryAndDecodeWorkflows(t)
}

type dispatcherCoverageStore struct {
	events    []domain.TopicEventRecord
	published int
	released  int
	retrying  int
}

func (s *dispatcherCoverageStore) ListDispatchableEvents(context.Context, time.Time, int) ([]domain.TopicEventRecord, error) {
	return s.events, nil
}

func (s *dispatcherCoverageStore) ClaimEvent(context.Context, string, string, time.Time, time.Time) (bool, error) {
	return true, nil
}

func (s *dispatcherCoverageStore) ReleaseEventClaim(_ context.Context, req ReleaseEventClaimRequest) error {
	s.released++
	if req.Status == domain.TopicEventDispatchRetrying {
		s.retrying++
	}
	return nil
}

func (s *dispatcherCoverageStore) MarkEventPublished(context.Context, string, string, time.Time) error {
	s.published++
	return nil
}

func (s *dispatcherCoverageStore) MarkEventNoSubscriber(context.Context, string, string, time.Time) error {
	return nil
}

type dispatcherCoverageBus struct {
	events    []domain.SchedulerTopicEvent
	publishOK bool
}

func (b *dispatcherCoverageBus) Publish(event domain.SchedulerTopicEvent) bool {
	if b.publishOK == false && len(b.events) > 0 {
		return false
	}
	b.events = append(b.events, event)
	return true
}
