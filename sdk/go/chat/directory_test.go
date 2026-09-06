package chat

import (
	"context"
	"testing"
	"time"
)

// A conversation is a set of runs sharing an identity label, so a rebuilt one
// must appear once — described by its most recent run — rather than once per
// environment it has occupied.
func TestConversationsFoldsARebuiltConversationIntoOneEntry(t *testing.T) {
	daemon := newFakeDaemon(t)
	early := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	late := early.Add(2 * time.Hour)
	daemon.unary["ListRuns"] = map[string]any{
		"runs": []map[string]any{
			{
				"runId": "run_old", "projectId": "p", "agentName": "a",
				"status": "RUN_STATUS_SUCCEEDED", "createdAt": early,
				"labels": map[string]string{conversationLabel: "conv_1", "chat.user": "alice"},
			},
			{
				"runId": "run_new", "projectId": "p", "agentName": "a",
				"status": "RUN_STATUS_RUNNING", "createdAt": late,
				"labels": map[string]string{conversationLabel: "conv_1", "chat.user": "alice"},
			},
			{
				"runId": "run_other", "projectId": "p", "agentName": "a",
				"status": "RUN_STATUS_SUCCEEDED", "createdAt": early.Add(time.Hour),
				"labels": map[string]string{conversationLabel: "conv_2", "chat.user": "alice"},
			},
		},
	}

	found, err := daemon.client(t).Conversations(context.Background(), Search{Labels: map[string]string{"chat.user": "alice"}})
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("got %d conversations, want 2: %+v", len(found), found)
	}
	// Most recently active first.
	if found[0].ID != "conv_1" || found[1].ID != "conv_2" {
		t.Fatalf("wrong order: %+v", found)
	}
	if !found[0].LastActive.Equal(late) {
		t.Errorf("conv_1 was described by the wrong run: %v", found[0].LastActive)
	}
	if !found[0].Live {
		t.Error("a conversation whose latest run is still running is not reported live")
	}
	if found[1].Live {
		t.Error("a conversation whose run has finished is reported live")
	}
	if found[0].ProjectID != "p" || found[0].AgentName != "a" {
		t.Errorf("a listed conversation cannot be reopened: %+v", found[0])
	}
}

// The identity label is this package's, not the caller's: it comes back as the
// ID and must not also appear among the labels the caller set.
func TestConversationsReportsTheCallersLabelsWithoutTheIdentityOne(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.unary["ListRuns"] = map[string]any{
		"runs": []map[string]any{{
			"runId": "run_1", "projectId": "p", "agentName": "a",
			"status": "RUN_STATUS_RUNNING", "createdAt": time.Now().UTC(),
			"labels": map[string]string{conversationLabel: "conv_1", "chat.user": "alice", "team": "platform"},
		}},
	}
	found, err := daemon.client(t).Conversations(context.Background(), Search{})
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("got %d conversations, want 1", len(found))
	}
	if _, present := found[0].Labels[conversationLabel]; present {
		t.Errorf("the identity label leaked into the caller's labels: %v", found[0].Labels)
	}
	if found[0].Labels["chat.user"] != "alice" || found[0].Labels["team"] != "platform" {
		t.Errorf("the caller's own labels were lost: %v", found[0].Labels)
	}
}

// The daemon's run list is shared with everything else that starts runs. A run
// with no conversation identity has no ID that Open could resume, so it is not
// a conversation and must not be reported as one.
func TestConversationsIgnoresRunsThisPackageDidNotStart(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.unary["ListRuns"] = map[string]any{
		"runs": []map[string]any{
			{"runId": "run_cli", "projectId": "p", "agentName": "a", "status": "RUN_STATUS_SUCCEEDED"},
			{"runId": "run_scheduled", "projectId": "p", "agentName": "a", "status": "RUN_STATUS_SUCCEEDED",
				"labels": map[string]string{"scheduler": "nightly"}},
		},
	}
	found, err := daemon.client(t).Conversations(context.Background(), Search{})
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("runs without a conversation identity were listed: %+v", found)
	}
}

// Asking for one conversation is asking the daemon, not this process's memory:
// it is how a caller checks that a conversation exists and belongs to the user
// in front of them, with no local record to consult.
func TestConversationsCanNarrowToASingleConversation(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.unary["ListRuns"] = map[string]any{
		"runs": []map[string]any{{
			"runId": "run_1", "projectId": "p", "agentName": "a",
			"status": "RUN_STATUS_RUNNING", "createdAt": time.Now().UTC(),
			"labels": map[string]string{conversationLabel: "conv_1", "chat.user": "alice"},
		}},
	}
	found, err := daemon.client(t).Conversations(context.Background(), Search{
		ID: "conv_1", Labels: map[string]string{"chat.user": "alice"},
	})
	if err != nil {
		t.Fatalf("Conversations: %v", err)
	}
	if len(found) != 1 || found[0].ID != "conv_1" {
		t.Fatalf("got %+v, want just conv_1", found)
	}
}
