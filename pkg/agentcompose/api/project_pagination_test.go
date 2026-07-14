package api

import (
	"testing"
	"time"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestSchedulerCursorBindsQueryAndStableKey(t *testing.T) {
	item := &agentcomposev2.SchedulerSummary{ProjectId: "project", AgentName: "agent", SchedulerId: "scheduler"}
	key := schedulerSummaryKey(item)
	token := encodeSchedulerCursor("needle", key)
	cursor, err := decodeSchedulerCursor(token, "needle")
	if err != nil || cursor.LastKey != key {
		t.Fatalf("decode cursor=%#v err=%v", cursor, err)
	}
	if _, err := decodeSchedulerCursor(token, "different"); err == nil {
		t.Fatal("query-mismatched token was accepted")
	}
}

func TestSchedulerEventCursorBindsLoaderAndTuple(t *testing.T) {
	createdAt := time.Date(2026, 7, 12, 1, 2, 3, 4_000_000, time.UTC)
	token := encodeSchedulerEventCursor("loader-1", createdAt, "event-1")
	cursor, err := decodeSchedulerEventCursor(token, "loader-1")
	if err != nil || !cursor.CreatedAt.Equal(createdAt) || cursor.EventID != "event-1" {
		t.Fatalf("decode cursor=%#v err=%v", cursor, err)
	}
	if _, err := decodeSchedulerEventCursor(token, "loader-2"); err == nil {
		t.Fatal("loader-mismatched token was accepted")
	}
}

func TestCursorDecodersUseCursorTerminology(t *testing.T) {
	assertInvalidCursor := func(err error) {
		t.Helper()
		if err == nil || err.Error() != "invalid cursor" {
			t.Fatalf("error = %v, want invalid cursor", err)
		}
	}

	_, err := decodeSchedulerCursor("not-base64!", "query")
	assertInvalidCursor(err)
	_, err = decodeSchedulerEventCursor("not-base64!", "loader")
	assertInvalidCursor(err)
	_, err = decodeSandboxCursor("not-base64!")
	assertInvalidCursor(err)
	_, err = decodeRunEventCursor("run", "not-base64!")
	assertInvalidCursor(err)
}
