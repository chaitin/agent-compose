package chat

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// TestHistoryPageWalksBackwardAcrossRuns pages one message at a time across
// two runs, oldest last, and checks the cursor round-trips correctly.
func TestHistoryPageWalksBackwardAcrossRuns(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listRuns = listRunsReturning(
		&agentcomposev2.RunSummary{RunId: "run-1", CreatedAt: timestamppb.New(time.Unix(100, 0))},
		&agentcomposev2.RunSummary{RunId: "run-2", CreatedAt: timestamppb.New(time.Unix(200, 0))},
	)

	events := map[string][]*agentcomposev2.RunEvent{
		"run-1": {
			runEvent("e0", agentcomposev2.RunEventKind_RUN_EVENT_KIND_USER_MESSAGE, "q0"),
			runEvent("e1", agentcomposev2.RunEventKind_RUN_EVENT_KIND_AGENT_MESSAGE, "a0"),
		},
		"run-2": {
			runEvent("e2", agentcomposev2.RunEventKind_RUN_EVENT_KIND_USER_MESSAGE, "q1"),
			runEvent("e3", agentcomposev2.RunEventKind_RUN_EVENT_KIND_AGENT_MESSAGE, "a1"),
		},
	}
	daemon.listRunEvents = listEventsPage(events)

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start(WithID("conv-abc"))
	ctx := context.Background()

	want := []string{"a1", "q1", "a0", "q0"}
	var cursor string
	for i, expect := range want {
		page, err := conversation.HistoryPage(ctx, HistoryOptions{Limit: 1, Before: cursor})
		if err != nil {
			t.Fatalf("HistoryPage[%d]: %v", i, err)
		}
		if len(page.Messages) != 1 || page.Messages[0].Text != expect {
			t.Fatalf("HistoryPage[%d] = %#v, want a single message %q", i, page.Messages, expect)
		}
		last := i == len(want)-1
		if last && page.Cursor != "" {
			t.Errorf("HistoryPage[%d] cursor = %q, want empty at the start of history", i, page.Cursor)
		}
		if !last && page.Cursor == "" {
			t.Fatalf("HistoryPage[%d] cursor is empty, want more history to walk", i)
		}
		cursor = page.Cursor
	}
}

// TestHistoryPageGrowsItsWindowPastNonMessageEvents forces the run's tail
// probe to come up short and verifies HistoryPage still finds a message
// buried near the start of a long run instead of reporting too few.
func TestHistoryPageGrowsItsWindowPastNonMessageEvents(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listRuns = listRunsReturning(&agentcomposev2.RunSummary{RunId: "run-1"})

	total := historyPageSize + 50
	all := make([]*agentcomposev2.RunEvent, total)
	all[0] = runEvent("first", agentcomposev2.RunEventKind_RUN_EVENT_KIND_USER_MESSAGE, "buried")
	for i := 1; i < total-1; i++ {
		all[i] = runEvent("filler", agentcomposev2.RunEventKind_RUN_EVENT_KIND_STATUS, "running")
	}
	all[total-1] = runEvent("last", agentcomposev2.RunEventKind_RUN_EVENT_KIND_AGENT_MESSAGE, "recent")

	daemon.listRunEvents = listEventsPage(map[string][]*agentcomposev2.RunEvent{"run-1": all})

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start(WithID("conv-abc"))
	page, err := conversation.HistoryPage(context.Background(), HistoryOptions{Limit: 2})
	if err != nil {
		t.Fatalf("HistoryPage: %v", err)
	}
	if len(page.Messages) != 2 {
		t.Fatalf("history = %d messages, want 2 (the initial window only covers the run's tail)", len(page.Messages))
	}
	if page.Messages[0].Text != "buried" || page.Messages[1].Text != "recent" {
		t.Errorf("messages = %#v, want [buried, recent]", page.Messages)
	}
	if page.Cursor != "" {
		t.Errorf("cursor = %q, want empty: the whole run was scanned", page.Cursor)
	}
}

// runEvent builds one durable event with the fields these tests assert on.
func runEvent(id string, kind agentcomposev2.RunEventKind, text string) *agentcomposev2.RunEvent {
	return &agentcomposev2.RunEvent{Id: id, Kind: kind, Text: text}
}

// listRunsReturning answers every ListRuns with the same runs.
func listRunsReturning(runs ...*agentcomposev2.RunSummary) func(*agentcomposev2.ListRunsRequest) (*agentcomposev2.ListRunsResponse, error) {
	return func(*agentcomposev2.ListRunsRequest) (*agentcomposev2.ListRunsResponse, error) {
		return &agentcomposev2.ListRunsResponse{Runs: runs, Total: uint32(len(runs))}, nil
	}
}

// listEventsPage emulates the daemon's ListRunEvents pagination over a fixed
// event log, honoring the request's run, offset and limit.
func listEventsPage(events map[string][]*agentcomposev2.RunEvent) func(*agentcomposev2.ListRunEventsRequest) (*agentcomposev2.ListRunEventsResponse, error) {
	return func(request *agentcomposev2.ListRunEventsRequest) (*agentcomposev2.ListRunEventsResponse, error) {
		all := events[request.GetRunId()]
		total := uint32(len(all))
		start := min(request.GetOffset(), total)
		limit := request.GetLimit()
		if limit == 0 {
			limit = total
		}
		end := min(start+limit, total)
		return &agentcomposev2.ListRunEventsResponse{Events: all[start:end], Total: total}, nil
	}
}

// listEventsReturning answers every ListRunEvents with the same events.
func listEventsReturning(events ...*agentcomposev2.RunEvent) func(*agentcomposev2.ListRunEventsRequest) (*agentcomposev2.ListRunEventsResponse, error) {
	return func(*agentcomposev2.ListRunEventsRequest) (*agentcomposev2.ListRunEventsResponse, error) {
		return &agentcomposev2.ListRunEventsResponse{Events: events, Total: uint32(len(events))}, nil
	}
}
