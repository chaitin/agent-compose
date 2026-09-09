package chat

import (
	"context"
	"google.golang.org/protobuf/types/known/timestamppb"
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
