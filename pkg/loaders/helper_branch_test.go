package loaders

import (
	"encoding/json"
	"testing"

	"agent-compose/pkg/model"
)

func TestLoaderHelperBranches(t *testing.T) {
	if got := int64FromMap(map[string]any{"n": json.Number("42")}, "n"); got != 42 {
		t.Fatalf("int64FromMap(json.Number) = %d", got)
	}
	if got := int64FromMap(map[string]any{"n": "bad"}, "n"); got != 0 {
		t.Fatalf("int64FromMap(bad) = %d", got)
	}
	if err := validateLoaderPublishTopic("bad.topic"); err == nil {
		t.Fatalf("validateLoaderPublishTopic bad prefix returned nil")
	}
	if err := validateLoaderPublishTopic("runtime.good"); err != nil {
		t.Fatalf("validateLoaderPublishTopic runtime.good = %v", err)
	}
	if jsonObjectDocument(`[]`) || !jsonObjectDocument(`{"ok":true}`) {
		t.Fatalf("jsonObjectDocument returned unexpected values")
	}

	responseJSON := `{"session":{"summary":{"sessionId":"resp-session"}}}`
	if got := loaderSessionRPCLinkedSessionID("CreateSession", `{"sessionId":"req-session"}`, responseJSON); got != "resp-session" {
		t.Fatalf("loaderSessionRPCLinkedSessionID CreateSession = %q", got)
	}
	if got := loaderSessionRPCLinkedSessionID("GetSession", `{"sessionId":"req-session"}`, `{}`); got != "req-session" {
		t.Fatalf("loaderSessionRPCLinkedSessionID GetSession = %q", got)
	}
	if got := loaderSessionRPCLinkedSessionID("ListSessions", `{"sessionId":"req-session"}`, `{}`); got != "" {
		t.Fatalf("loaderSessionRPCLinkedSessionID ListSessions = %q", got)
	}
	if loaderSessionIDFromJSON(`{bad`) != "" || loaderSessionIDFromJSON(`{"session":{"summary":{}}}`) != "" {
		t.Fatalf("loaderSessionIDFromJSON returned value for invalid payload")
	}

	if sessionTopicPayload(nil, "test") != nil {
		t.Fatalf("sessionTopicPayload nil did not return nil")
	}
	session := &Session{Summary: model.SessionSummary{ID: "session-branch", Title: "Branch", VMStatus: model.VMStatusRunning}}
	sessionPayload := sessionTopicPayload(session, "test")
	if sessionPayload["sessionId"] != "session-branch" || sessionPayload["source"] != "test" {
		t.Fatalf("sessionTopicPayload = %#v", sessionPayload)
	}
	cellPayload := cellTopicPayload("session-branch", NotebookCell{ID: "cell-1", Type: model.CellTypeShell, Agent: "codex", Success: true}, "test")
	if cellPayload["cellId"] != "cell-1" || cellPayload["agent"] != "codex" {
		t.Fatalf("cellTopicPayload = %#v", cellPayload)
	}
	commandPayload := loaderCommandEventPayload(
		LoaderCommandRequest{Mode: "shell", Command: "ignored", Args: []string{"-c"}, Cwd: "/tmp"},
		LoaderCommandResult{ExitCode: 2, Success: false, SessionID: "session-branch", CellID: "cell-1"},
	)
	if commandPayload["exitCode"] != 2 || commandPayload["success"] != false {
		t.Fatalf("loaderCommandEventPayload = %#v", commandPayload)
	}
}
