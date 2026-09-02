package configstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"

	modernsqlite "modernc.org/sqlite"
)

func TestTopicEventPayloadWithSequencePreservesRawBodyValues(t *testing.T) {
	payload := `{"sequence":0,"body":{"large":9007199254740993}}`
	sequenced, changed, err := topicEventPayloadWithSequence(payload, 7)
	if err != nil {
		t.Fatalf("topicEventPayloadWithSequence returned error: %v", err)
	}
	if !changed || sequenced != `{"body":{"large":9007199254740993},"sequence":7}` {
		t.Fatalf("topicEventPayloadWithSequence = %s changed=%v", sequenced, changed)
	}

	plain := `{"body":{"large":9007199254740993}}`
	sequenced, changed, err = topicEventPayloadWithSequence(plain, 7)
	if err != nil || changed || sequenced != plain {
		t.Fatalf("payload without sequence = %s changed=%v err=%v", sequenced, changed, err)
	}
}

func TestCreateEventAcceptsIdempotentPayloadWithSequencePlaceholder(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	item := domain.TopicEventRecord{
		ID:             "event-original",
		Topic:          "webhook.github.push",
		Source:         domain.TopicEventSourceWebhook,
		IdempotencyKey: "delivery-1",
		PayloadJSON:    `{"sequence":0,"body":{"branch":"main"}}`,
		DispatchStatus: domain.TopicEventDispatchPending,
	}
	created, err := store.CreateEvent(ctx, item)
	if err != nil {
		t.Fatalf("create original event: %v", err)
	}

	item.ID = "event-duplicate"
	duplicate, err := store.CreateEvent(ctx, item)
	if err != nil {
		t.Fatalf("create idempotent duplicate: %v", err)
	}
	if duplicate.ID != created.ID || duplicate.PayloadHash != created.PayloadHash {
		t.Fatalf("duplicate = %#v, want original %#v", duplicate, created)
	}
}

func TestCreateEventPayloadConflictCarriesExistingEvent(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	created, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID:             "event-original",
		Topic:          "webhook.github.push",
		Source:         domain.TopicEventSourceWebhook,
		IdempotencyKey: "delivery-1",
		PayloadJSON:    `{"sequence":0,"body":{"branch":"main"}}`,
		DispatchStatus: domain.TopicEventDispatchPending,
	})
	if err != nil {
		t.Fatalf("create original event: %v", err)
	}

	_, err = store.CreateEvent(ctx, domain.TopicEventRecord{
		ID:             "event-conflict",
		Topic:          created.Topic,
		Source:         domain.TopicEventSourceWebhook,
		IdempotencyKey: created.IdempotencyKey,
		PayloadJSON:    `{"sequence":0,"body":{"branch":"changed"}}`,
		DispatchStatus: domain.TopicEventDispatchPending,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("conflict error = %v, want ErrConflict", err)
	}
	var conflict *domain.TopicEventIdempotencyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflict error type = %T, want TopicEventIdempotencyConflictError", err)
	}
	if conflict.Existing.ID != created.ID || conflict.Existing.Sequence != created.Sequence {
		t.Fatalf("conflicting existing event = %#v, want %#v", conflict.Existing, created)
	}
}

func TestListDescendantEventIDsHonorsExplicitLimitAboveDefault(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin event fixture transaction: %v", err)
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO event(
		id, topic, source, correlation_id, payload_hash, payload_json,
		dispatch_status, parent_event_id, created_at
	) VALUES(?, 'webhook.test', 'webhook', '', 'hash', '{}', 'pending', ?, 1)`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare event fixture insert: %v", err)
	}
	defer func() { _ = statement.Close() }()
	if _, err := statement.ExecContext(ctx, "event-1", ""); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert root event: %v", err)
	}
	for index := 2; index <= 1001; index++ {
		if _, err := statement.ExecContext(ctx, "event-"+strconv.Itoa(index), "event-1"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert child event %d: %v", index, err)
		}
	}
	if err := statement.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatalf("close event fixture statement: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit event fixture: %v", err)
	}

	ids, err := store.ListDescendantEventIDs(ctx, "event-1", 1001)
	if err != nil {
		t.Fatalf("ListDescendantEventIDs returned error: %v", err)
	}
	if len(ids) != 1001 {
		t.Fatalf("descendant event count=%d, want 1001", len(ids))
	}
}

func TestCreateEventReturnsSequencedRecordWhenContextIsCanceledAfterCommit(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "events.db")
	setup, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open setup database: %v", err)
	}
	if err := storagesqlite.Migrate(context.Background(), setup); err != nil {
		_ = setup.Close()
		t.Fatalf("migrate setup database: %v", err)
	}
	if err := setup.Close(); err != nil {
		t.Fatalf("close setup database: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	driverName := "sqlite_cancel_after_event_commit:" + databasePath
	sql.Register(driverName, &cancelAfterEventCommitDriver{
		delegate: &modernsqlite.Driver{},
		cancel:   cancel,
	})
	database, err := sql.Open(driverName, databasePath)
	if err != nil {
		t.Fatalf("open instrumented database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close instrumented database: %v", err)
		}
	})

	created, err := FromDB(database).CreateEvent(ctx, domain.TopicEventRecord{
		ID:             "event-committed",
		Topic:          "webhook.github.push",
		Source:         domain.TopicEventSourceWebhook,
		PayloadJSON:    `{"sequence":0,"body":{"branch":"main"}}`,
		DispatchStatus: domain.TopicEventDispatchPending,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error after commit: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("context was not canceled after COMMIT")
	}
	if created.ID != "event-committed" || created.Sequence == 0 {
		t.Fatalf("CreateEvent returned %#v", created)
	}
	wantSequence := `"sequence":` + strconv.FormatInt(created.Sequence, 10)
	if !strings.Contains(created.PayloadJSON, wantSequence) {
		t.Fatalf("CreateEvent payload = %s, want %s", created.PayloadJSON, wantSequence)
	}

	var storedPayload string
	if err := database.QueryRowContext(context.Background(), `SELECT payload_json FROM event WHERE id = ?`, created.ID).Scan(&storedPayload); err != nil {
		t.Fatalf("query committed event: %v", err)
	}
	if storedPayload != created.PayloadJSON {
		t.Fatalf("stored payload = %s, want %s", storedPayload, created.PayloadJSON)
	}
}

type cancelAfterEventCommitDriver struct {
	delegate driver.Driver
	cancel   context.CancelFunc
	once     sync.Once
}

func (d *cancelAfterEventCommitDriver) Open(name string) (driver.Conn, error) {
	connection, err := d.delegate.Open(name)
	if err != nil {
		return nil, err
	}
	return &cancelAfterEventCommitConn{Conn: connection, owner: d}, nil
}

type cancelAfterEventCommitConn struct {
	driver.Conn
	owner *cancelAfterEventCommitDriver
}

func (c *cancelAfterEventCommitConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, driver.ErrSkip
	}
	transaction, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &cancelAfterEventCommitTx{Tx: transaction, owner: c.owner}, nil
}

type cancelAfterEventCommitTx struct {
	driver.Tx
	owner *cancelAfterEventCommitDriver
}

func (t *cancelAfterEventCommitTx) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	t.owner.once.Do(t.owner.cancel)
	return nil
}

func TestCancelEventDispatchOnlyWithdrawsWaitingEvents(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	statuses := map[string]string{
		"waiting-pending":   domain.TopicEventDispatchPending,
		"waiting-retrying":  domain.TopicEventDispatchRetrying,
		"expired-claim":     domain.TopicEventDispatchPublishing,
		"active-claim":      domain.TopicEventDispatchPublishing,
		"already-published": domain.TopicEventDispatchPublishedToBus,
		"dead":              domain.TopicEventDispatchDeadLetter,
	}
	ids := make([]string, 0, len(statuses))
	activeClaimUntil := time.Now().UTC().Add(time.Hour).UnixMilli()
	for id, status := range statuses {
		ids = append(ids, id)
		claimUntil := int64(99)
		if id == "active-claim" {
			claimUntil = activeClaimUntil
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO event(
			id, topic, source, correlation_id, payload_hash, payload_json,
			dispatch_status, parent_event_id, created_at, claim_id, claim_until, next_attempt_at
		) VALUES(?, 'webhook.test', 'webhook', '', 'hash', '{}', ?, '', 1, 'claim-1', ?, 99)`, id, status, claimUntil); err != nil {
			t.Fatalf("insert %s event: %v", id, err)
		}
	}

	cancellation, err := store.CancelEventDispatch(ctx, append(ids, "", "waiting-pending"), "business canceled")
	if err != nil {
		t.Fatalf("CancelEventDispatch returned error: %v", err)
	}
	if cancellation.Canceled != 2 {
		t.Fatalf("canceled=%d, want 2", cancellation.Canceled)
	}
	// The two publishing events must not be lumped together: only the one whose
	// claim still holds produces its run in a moment. The expired one waits for
	// another dispatch pass, so reporting it as in flight would tell the caller
	// to retry immediately and find nothing.
	if cancellation.InFlight != 1 {
		t.Fatalf("in flight=%d, want 1", cancellation.InFlight)
	}
	if cancellation.Stale != 1 {
		t.Fatalf("stale=%d, want 1", cancellation.Stale)
	}

	for id, original := range statuses {
		event, err := store.GetEvent(ctx, id)
		if err != nil {
			t.Fatalf("get %s event: %v", id, err)
		}
		want := original
		if original == domain.TopicEventDispatchPending || original == domain.TopicEventDispatchRetrying {
			want = domain.TopicEventDispatchCanceled
		}
		if event.DispatchStatus != want {
			t.Fatalf("event %s dispatch_status=%q, want %q", id, event.DispatchStatus, want)
		}
	}

	// Waiting events must leave the dispatch queue, and their claims must be
	// released. Publishing events remain in flight so an active worker can
	// finish delivery and release its claim safely.
	dispatchable, err := store.ListDispatchableEvents(ctx, time.UnixMilli(1000).UTC(), 100)
	if err != nil {
		t.Fatalf("ListDispatchableEvents returned error: %v", err)
	}
	for _, item := range dispatchable {
		if item.ID == "waiting-pending" || item.ID == "waiting-retrying" {
			t.Fatalf("withdrawn event %s is still dispatchable", item.ID)
		}
	}
	claimed, err := store.ClaimEvent(ctx, "waiting-pending", "claim-2", time.UnixMilli(1000).UTC(), time.UnixMilli(31000).UTC())
	if err != nil {
		t.Fatalf("ClaimEvent returned error: %v", err)
	}
	if claimed {
		t.Fatal("withdrawn event was claimed for dispatch")
	}

	// A withdrawn event must survive the canonical dispatch-status validator.
	// When "canceled" is missing from it the filter is silently dropped and the
	// query answers with every event instead of the withdrawn ones, so this
	// asserts the count rather than just the absence of an error.
	withdrawn, total, err := store.ListEvents(ctx, domain.TopicEventFilter{
		Topic: "webhook.test", DispatchStatus: domain.TopicEventDispatchCanceled, Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if total != 2 || len(withdrawn) != 2 {
		t.Fatalf("canceled events=%d total=%d, want 2 and 2", len(withdrawn), total)
	}
	for _, item := range withdrawn {
		if item.DispatchStatus != domain.TopicEventDispatchCanceled {
			t.Fatalf("event %s dispatch_status=%q, want canceled", item.ID, item.DispatchStatus)
		}
	}

	// The stale event is deliberately still dispatchable, which is exactly why
	// it is reported apart from the in-flight one: its run appears only after
	// another loop reclaims it.
	reclaimed, err := store.ClaimEvent(ctx, "expired-claim", "claim-3", time.UnixMilli(1000).UTC(), time.UnixMilli(31000).UTC())
	if err != nil {
		t.Fatalf("ClaimEvent returned error: %v", err)
	}
	if !reclaimed {
		t.Fatal("stale event was not reclaimable, so stale_events would be misreported")
	}
}
