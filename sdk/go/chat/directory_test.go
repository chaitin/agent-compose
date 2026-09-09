package chat

import (
	"context"
	"errors"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strconv"
	"testing"
	"time"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// Lookup is the question authorization asks, and it must not need a label
// read: the run list's own filter already answers "is there a conversation
// with this ID carrying these labels?".
func TestLookupAnswersFromTheFilterAloneWithoutReadingRunDetail(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listRuns = listRunsReturning(&agentcomposev2.RunSummary{
		RunId: "run_1", ProjectId: "p", AgentName: "a",
		Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
		CreatedAt: timestamppb.New(time.Now().UTC()),
	})
	found, ok, err := daemon.client(t).Lookup(context.Background(), "conv_1",
		map[string]string{"chat.user": "alice"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !ok {
		t.Fatal("a conversation with a matching run was reported absent")
	}
	if found.ID != "conv_1" || found.ProjectID != "p" || found.AgentName != "a" {
		t.Errorf("Lookup returned %+v", found)
	}
	if !found.Live {
		t.Error("a conversation whose run is still running was not reported live")
	}
	if calls := daemon.count("GetRun"); calls != 0 {
		t.Errorf("Lookup read run detail %d times; the filter is enough", calls)
	}
}

// No run matching both labels means the conversation is not this user's, or
// does not exist. The caller cannot tell which, which is the point.
func TestLookupReportsNothingWhenNoRunMatches(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listRuns = listRunsReturning()
	_, ok, err := daemon.client(t).Lookup(context.Background(), "conv_1",
		map[string]string{"chat.user": "mallory"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if ok {
		t.Fatal("a conversation was reported for a user with no matching run")
	}
}

func TestLookupRequiresAnID(t *testing.T) {
	daemon := newFakeDaemon(t)
	if _, _, err := daemon.client(t).Lookup(context.Background(), "  ", nil); err == nil {
		t.Fatal("an empty conversation ID was accepted")
	}
}

// Enumeration is the expensive one: a run summary carries no labels, so the
// conversation each run belongs to costs one detail read per run. A rebuilt
// conversation occupies several runs and must still appear once.
func TestConversationsReadsRunDetailAndFoldsARebuiltConversation(t *testing.T) {
	daemon := newFakeDaemon(t)
	early := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	late := early.Add(2 * time.Hour)
	daemon.listRuns = listRunsReturning(
		&agentcomposev2.RunSummary{
			RunId: "run_old", ProjectId: "p", AgentName: "a",
			Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
			CreatedAt: timestamppb.New(early),
		},
		&agentcomposev2.RunSummary{
			RunId: "run_new", ProjectId: "p", AgentName: "a",
			Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
			CreatedAt: timestamppb.New(late),
		},
	)
	daemon.getRun = getRunLabelled(map[string]string{conversationLabel: "conv_1", "chat.user": "alice"})

	found, err := daemon.client(t).Conversations(context.Background(), Search{
		Labels: map[string]string{"chat.user": "alice"},
	})
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(found) != 1 || found[0].ID != "conv_1" {
		t.Fatalf("got %+v, want one conv_1", found)
	}
	if !found[0].LastActive.Equal(late) {
		t.Errorf("the conversation was described by the wrong run: %v", found[0].LastActive)
	}
	if !found[0].Live {
		t.Error("a conversation whose latest run is running was not reported live")
	}
	if _, present := found[0].Labels[conversationLabel]; present {
		t.Errorf("the identity label leaked into the caller's labels: %v", found[0].Labels)
	}
	if found[0].Labels["chat.user"] != "alice" {
		t.Errorf("the caller's own labels were lost: %v", found[0].Labels)
	}
	if calls := daemon.count("GetRun"); calls != 2 {
		t.Errorf("read run detail %d times for 2 runs", calls)
	}
}

// The daemon's run list is shared with everything else that starts runs. A run
// with no conversation identity has no ID that Open could resume, so it is not
// a conversation and must not be reported as one.
func TestConversationsIgnoresRunsThisPackageDidNotStart(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listRuns = listRunsReturning(&agentcomposev2.RunSummary{
		RunId: "run_cli", ProjectId: "p", AgentName: "a",
		Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
	})
	daemon.getRun = getRunLabelled(map[string]string{"scheduler": "nightly"})
	found, err := daemon.client(t).Conversations(context.Background(), Search{})
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("runs without a conversation identity were listed: %+v", found)
	}
}

// getRunLabelled answers every GetRun with the same labels, which is the only
// field the conversation directory reads from a run's detail.
func getRunLabelled(labels map[string]string) func(*agentcomposev2.GetRunRequest) (*agentcomposev2.GetRunResponse, error) {
	return func(*agentcomposev2.GetRunRequest) (*agentcomposev2.GetRunResponse, error) {
		return &agentcomposev2.GetRunResponse{Run: &agentcomposev2.RunDetail{Labels: labels}}, nil
	}
}

// listRunsPaged emulates the daemon's run pagination over a fixed list,
// honoring the request's offset and limit. listRunsReturning hands back
// everything at once, which cannot show whether a caller walks past the first
// page.
func listRunsPaged(runs ...*agentcomposev2.RunSummary) func(*agentcomposev2.ListRunsRequest) (*agentcomposev2.ListRunsResponse, error) {
	return func(request *agentcomposev2.ListRunsRequest) (*agentcomposev2.ListRunsResponse, error) {
		total := uint32(len(runs))
		start := min(request.GetOffset(), total)
		end := total
		if limit := request.GetLimit(); limit > 0 {
			end = min(start+limit, total)
		}
		return &agentcomposev2.ListRunsResponse{Runs: runs[start:end], Total: total}, nil
	}
}

// A conversation is found through the runs it occupied, and a busy daemon has
// far more runs than one page holds. Stopping at the first page would hide
// whole conversations from an enumeration that claims to return every one.
func TestConversationsWalksPastTheFirstPageOfRuns(t *testing.T) {
	daemon := newFakeDaemon(t)
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	labels := map[string]map[string]string{}
	var runs []*agentcomposev2.RunSummary
	// One conversation hogs the first page and then some; the other is reachable
	// only by asking for a second one.
	for index := range listPageSize + 5 {
		runID := "run_" + strconv.Itoa(index)
		conversation := "conv_loud"
		if index >= listPageSize+3 {
			conversation = "conv_quiet"
		}
		runs = append(runs, &agentcomposev2.RunSummary{
			RunId: runID, ProjectId: "p", AgentName: "a",
			Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
			CreatedAt: timestamppb.New(created.Add(-time.Duration(index) * time.Minute)),
		})
		labels[runID] = map[string]string{conversationLabel: conversation}
	}
	daemon.listRuns = listRunsPaged(runs...)
	daemon.getRun = func(request *agentcomposev2.GetRunRequest) (*agentcomposev2.GetRunResponse, error) {
		return &agentcomposev2.GetRunResponse{Run: &agentcomposev2.RunDetail{Labels: labels[request.GetRunId()]}}, nil
	}

	found, err := daemon.client(t).Conversations(context.Background(), Search{})
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("got %d conversations, want both", len(found))
	}
	if found[0].ID != "conv_loud" || found[1].ID != "conv_quiet" {
		t.Errorf("got %q and %q, want conv_loud then conv_quiet by last activity", found[0].ID, found[1].ID)
	}
	if calls := daemon.count("ListRuns"); calls != 2 {
		t.Errorf("listed runs %d times, want 2 pages for %d runs", calls, len(runs))
	}
}

// An unbounded walk that reaches the end is complete, and must not carry the
// incomplete signal.
func TestConversationsWithNoLimitReportsComplete(t *testing.T) {
	daemon := newFakeDaemon(t)
	var runs []*agentcomposev2.RunSummary
	for index := range listPageSize + 5 {
		runs = append(runs, &agentcomposev2.RunSummary{
			RunId: "run_" + strconv.Itoa(index), ProjectId: "p", AgentName: "a",
			CreatedAt: timestamppb.New(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)),
		})
	}
	daemon.listRuns = listRunsPaged(runs...)
	daemon.getRun = getRunLabelled(map[string]string{conversationLabel: "conv_1"})

	if _, err := daemon.client(t).Conversations(context.Background(), Search{}); err != nil {
		t.Fatalf("Conversations: %v", err)
	}
}

// A caller's Limit is a budget over the whole walk. It bounds the cost of an
// enumeration — and running out of it is reported, because a subset that
// cannot be told apart from the whole is the bug this budget would otherwise
// reintroduce.
func TestConversationsHonorsLimitAsABudgetAndReportsWhatItLeftOut(t *testing.T) {
	daemon := newFakeDaemon(t)
	var runs []*agentcomposev2.RunSummary
	for index := range listPageSize * 2 {
		runs = append(runs, &agentcomposev2.RunSummary{
			RunId: "run_" + strconv.Itoa(index), ProjectId: "p", AgentName: "a",
			CreatedAt: timestamppb.New(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)),
		})
	}
	daemon.listRuns = listRunsPaged(runs...)
	daemon.getRun = getRunLabelled(map[string]string{conversationLabel: "conv_1"})

	found, err := daemon.client(t).Conversations(context.Background(), Search{Limit: listPageSize + 1})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Conversations err = %v, want ErrIncomplete: the budget ran out with runs unread", err)
	}
	// The partial answer still comes back: it is usable, just not the whole.
	if len(found) != 1 || found[0].ID != "conv_1" {
		t.Errorf("got %+v, want the conversations found before the budget ran out", found)
	}
	// A budget of one page plus one run costs two pages, not the whole list.
	if calls := daemon.count("ListRuns"); calls != 2 {
		t.Errorf("listed runs %d times, want 2", calls)
	}
	if calls := daemon.count("GetRun"); calls != listPageSize+1 {
		t.Errorf("examined %d runs, want the budget of %d", calls, listPageSize+1)
	}
}

// A budget large enough to reach the end is not "incomplete": the signal has
// to mean something was left out, or callers will learn to ignore it.
func TestConversationsReportsCompleteWhenTheBudgetIsEnough(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listRuns = listRunsPaged(&agentcomposev2.RunSummary{
		RunId: "run_1", ProjectId: "p", AgentName: "a",
		CreatedAt: timestamppb.New(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)),
	})
	daemon.getRun = getRunLabelled(map[string]string{conversationLabel: "conv_1"})

	found, err := daemon.client(t).Conversations(context.Background(), Search{Limit: 50})
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("got %d conversations, want 1", len(found))
	}
}

// Lookup and EndSession want the conversation's newest run and nothing else.
// The daemon returns a run list newest first, so one row answers both however
// many runs the conversation has piled up — and asking for one row is what
// keeps a long-lived conversation from making the cheap question expensive.
func TestLookupAsksTheDaemonForOnlyTheNewestRun(t *testing.T) {
	daemon := newFakeDaemon(t)
	var asked *agentcomposev2.ListRunsRequest
	daemon.listRuns = func(request *agentcomposev2.ListRunsRequest) (*agentcomposev2.ListRunsResponse, error) {
		asked = request
		return &agentcomposev2.ListRunsResponse{
			Runs: []*agentcomposev2.RunSummary{{
				RunId: "run_newest", ProjectId: "p", AgentName: "a",
				Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
				CreatedAt: timestamppb.New(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)),
			}},
			Total: 1200,
		}, nil
	}

	found, ok, err := daemon.client(t).Lookup(context.Background(), "conv_1", nil)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !ok || !found.Live {
		t.Fatalf("Lookup returned %+v (ok=%v), want the live newest run", found, ok)
	}
	if asked.GetLimit() != 1 || asked.GetOffset() != 0 {
		t.Errorf("asked for offset %d limit %d, want the single newest run", asked.GetOffset(), asked.GetLimit())
	}
	// 1200 matching runs, one request: the answer does not get more expensive
	// as a conversation ages.
	if calls := daemon.count("ListRuns"); calls != 1 {
		t.Errorf("listed runs %d times, want 1", calls)
	}
}
