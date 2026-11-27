package eventstream

import (
	"errors"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Enabled:          true,
		MaxSubscribers:   4,
		SubscriberBuffer: 4,
		ReplayEvents:     4,
		IdleTimeout:      time.Hour,
		Heartbeat:        time.Second,
		MaxBatchEvents:   8,
	}
}

func testEvent(eventType, projectID string, severity Severity) *Event {
	return &Event{Type: eventType, ProjectID: projectID, Severity: severity}
}

func mustFilter(t *testing.T, projectIDs, eventTypes []string, min Severity, includeLogs bool) Filter {
	t.Helper()
	filter, err := NewFilter(projectIDs, eventTypes, min, includeLogs)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	return filter
}

func receive(t *testing.T, ch <-chan *Event) *Event {
	t.Helper()
	select {
	case event, ok := <-ch:
		if !ok {
			t.Fatal("subscription channel closed")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

func expectNoEvent(t *testing.T, ch <-chan *Event) {
	t.Helper()
	select {
	case event := <-ch:
		t.Fatalf("unexpected event %q", event.Type)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestBrokerRequiresEnabled(t *testing.T) {
	broker := NewBroker(Config{})
	if broker.Enabled() {
		t.Fatal("zero config enabled the broker")
	}
	if _, _, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), ""); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Subscribe error = %v, want ErrDisabled", err)
	}
	if broker.Publish(testEvent(TypeRunStatus, "p1", SeverityInfo)) {
		t.Fatal("disabled broker published an event")
	}
}

func TestBrokerFiltersProjectTypeAndSeverity(t *testing.T) {
	broker := NewBroker(testConfig())
	defer broker.Close()
	filter := mustFilter(t, []string{"p1"}, []string{TypeRunStatus}, SeverityWarning, false)
	sub, resume, err := broker.Subscribe(filter, "")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	if resume.SyncRequired {
		t.Fatal("fresh subscription unexpectedly required sync")
	}
	if !broker.Active() || broker.SubscriberCount() != 1 {
		t.Fatalf("active=%v subscribers=%d", broker.Active(), broker.SubscriberCount())
	}

	broker.Publish(testEvent(TypeRunStatus, "p1", SeverityInfo))
	if !broker.Publish(testEvent(TypeRunStatus, "p1", SeverityWarning)) {
		t.Fatal("matching event was not delivered")
	}
	broker.Publish(testEvent(TypeRunStatus, "p2", SeverityError))
	broker.Publish(testEvent(TypeSchedulerEvent, "p1", SeverityError))
	event := receive(t, sub.Events())
	if event.Type != TypeRunStatus || event.ProjectID != "p1" || event.Severity != SeverityWarning {
		t.Fatalf("event = %+v", event)
	}
	if event.ID == "" || event.Cursor == "" || event.Sequence == 0 {
		t.Fatalf("event identity missing: %+v", event)
	}
	expectNoEvent(t, sub.Events())
}

func TestBrokerLogFiltering(t *testing.T) {
	if _, err := NewFilter(nil, []string{TypeRunLog}, SeverityInfo, false); !errors.Is(err, ErrLogEventsDisabled) {
		t.Fatalf("exact log type error = %v, want ErrLogEventsDisabled", err)
	}
	broker := NewBroker(testConfig())
	defer broker.Close()
	sub, _, err := broker.Subscribe(mustFilter(t, nil, []string{"run.*"}, SeverityInfo, false), "")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	if broker.Publish(testEvent(TypeRunLog, "p1", SeverityInfo)) {
		t.Fatal("log event delivered without include_logs")
	}
	if !broker.Publish(testEvent(TypeRunStatus, "p1", SeverityInfo)) {
		t.Fatal("run status event was not delivered")
	}
	if event := receive(t, sub.Events()); event.Type != TypeRunStatus {
		t.Fatalf("event type = %q", event.Type)
	}
}

func TestBrokerReplaysFromRingAndRejectsExpiredCursors(t *testing.T) {
	cfg := testConfig()
	cfg.ReplayEvents = 3
	broker := NewBroker(cfg)
	defer broker.Close()
	first, _, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), "")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	for i := 0; i < 4; i++ {
		broker.Publish(testEvent(TypeRunStatus, "p1", SeverityInfo))
	}
	cursor := EncodeCursorMust(t, broker, 2)
	first.Close()

	replayed, resume, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), cursor)
	if err != nil {
		t.Fatalf("Subscribe replay: %v", err)
	}
	defer replayed.Close()
	if resume.SyncRequired {
		t.Fatalf("replay unexpectedly required sync: %s", resume.Reason)
	}
	if len(resume.Replay) != 2 {
		t.Fatalf("replay length = %d, want 2", len(resume.Replay))
	}
	if resume.Replay[0].Sequence != 3 || resume.Replay[1].Sequence != 4 {
		t.Fatalf("replay sequences = %d, %d", resume.Replay[0].Sequence, resume.Replay[1].Sequence)
	}
	replayed.Close()

	expired, resume, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), EncodeCursorMust(t, broker, 0))
	if err != nil {
		t.Fatalf("Subscribe expired: %v", err)
	}
	defer expired.Close()
	if !resume.SyncRequired || resume.Reason != "cursor_expired" {
		t.Fatalf("expired resume = %+v", resume)
	}
}

func TestBrokerEpochChangeRequiresSync(t *testing.T) {
	broker := NewBroker(testConfig())
	now := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	broker.now = func() time.Time { return now }
	epochs := []string{"epoch-1", "epoch-2"}
	broker.newEpoch = func() string {
		epoch := epochs[0]
		epochs = epochs[1:]
		return epoch
	}
	sub, _, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), "")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	broker.Publish(testEvent(TypeRunStatus, "p1", SeverityInfo))
	cursor := EncodeCursorMust(t, broker, 1)
	sub.Close()
	broker.deactivateIfIdleAt(now.Add(2 * time.Hour))

	restarted, resume, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), cursor)
	if err != nil {
		t.Fatalf("Subscribe after idle: %v", err)
	}
	defer restarted.Close()
	if !resume.SyncRequired || resume.Reason != "epoch_changed" {
		t.Fatalf("resume = %+v", resume)
	}
}

func TestBrokerSlowSubscriberDropsAndReportsGap(t *testing.T) {
	cfg := testConfig()
	cfg.SubscriberBuffer = 1
	broker := NewBroker(cfg)
	defer broker.Close()
	sub, _, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), "")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	if !broker.Publish(testEvent(TypeRunStatus, "p1", SeverityInfo)) {
		t.Fatal("first event was not delivered")
	}
	broker.Publish(testEvent(TypeRunStatus, "p1", SeverityInfo))
	count, first, last := sub.TakeDropped()
	if count != 1 || first == "" || last == "" || first != last {
		t.Fatalf("dropped count=%d first=%q last=%q", count, first, last)
	}
	if event := receive(t, sub.Events()); event.Sequence != 1 {
		t.Fatalf("first delivered sequence = %d", event.Sequence)
	}
}

func TestBrokerMaxSubscribers(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSubscribers = 1
	broker := NewBroker(cfg)
	defer broker.Close()
	sub, _, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), "")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	if _, _, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), ""); !errors.Is(err, ErrTooManySubscribers) {
		t.Fatalf("second Subscribe error = %v, want ErrTooManySubscribers", err)
	}
}

func TestBrokerCloseUnsubscribesAndRejectsNewWork(t *testing.T) {
	broker := NewBroker(testConfig())
	sub, _, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), "")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	broker.Close()
	if _, ok := <-sub.Events(); ok {
		t.Fatal("subscription channel remained open after broker close")
	}
	if _, _, err := broker.Subscribe(mustFilter(t, nil, nil, SeverityInfo, false), ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("Subscribe after close error = %v, want ErrClosed", err)
	}
	if broker.Publish(testEvent(TypeRunStatus, "p1", SeverityInfo)) {
		t.Fatal("closed broker published")
	}
}

func TestCursorRoundTripAndRejectsMalformed(t *testing.T) {
	token := EncodeCursor("epoch-1", 42)
	cursor, err := DecodeCursor(token)
	if err != nil || cursor.Epoch != "epoch-1" || cursor.Sequence != 42 {
		t.Fatalf("DecodeCursor(%q) = %+v, %v", token, cursor, err)
	}
	for _, raw := range []string{"", "not-base64!", EncodeCursor("", 1)} {
		if _, err := DecodeCursor(raw); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("DecodeCursor(%q) error = %v", raw, err)
		}
	}
}

// EncodeCursorMust returns the cursor of an event with the given sequence. It
// is a test helper for replay tests that do not retain the original event.
func EncodeCursorMust(t *testing.T, broker *Broker, sequence uint64) string {
	t.Helper()
	epoch, _, _ := broker.Head()
	if epoch == "" {
		t.Fatal("broker is not active")
	}
	return EncodeCursor(epoch, sequence)
}

func TestEventCatalogAndSeverityParsing(t *testing.T) {
	types := EventTypes()
	if len(types) == 0 || types[0] != TypeRunStatus {
		t.Fatalf("EventTypes = %#v", types)
	}
	types[0] = "mutated"
	if EventTypes()[0] != TypeRunStatus {
		t.Fatal("EventTypes returned its backing slice")
	}
	if !IsKnownType(TypeRunEvent) || IsKnownType("run.unknown") {
		t.Fatal("IsKnownType mismatch")
	}
	for level, want := range map[string]Severity{"": SeverityInfo, "info": SeverityInfo, "warn": SeverityWarning, "warning": SeverityWarning, "error": SeverityError, "fatal": SeverityError} {
		if got := ParseSeverity(level); got != want {
			t.Fatalf("ParseSeverity(%q) = %v, want %v", level, got, want)
		}
	}
	filter := mustFilter(t, nil, []string{"run.*"}, SeverityInfo, true)
	if !filter.IncludeLogs() {
		t.Fatal("IncludeLogs false for includeLogs filter")
	}
}
