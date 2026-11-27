package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestFollowRunLogsReportsAnArchivedLogAsAFailedPrecondition(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runs", "run-1", runlog.FileName)
	w, err := runlog.Open(path, runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.AppendInitialPrompt("go")
	_, _ = w.EndTurn("stop", "done", nil)
	_ = w.Close()
	if _, err := runlog.ArchiveLog(path, filepath.Join(root, "archives", "runs", "run-1", runlog.ArchivedFileName), time.Now()); err != nil {
		t.Fatal(err)
	}
	store := &apiProjectRunStore{
		runs: map[string]domain.ProjectRunRecord{
			"run-1": {RunID: "run-1", ProjectID: "project-1", Status: domain.ProjectRunStatusSucceeded, LogsPath: path, LogFormat: runlog.FormatJSONL},
		},
		runEvents: []domain.ProjectRunEventRecord{
			{ID: "e1", RunID: "run-1", Kind: domain.ProjectRunEventKindUserMessage, Text: "go", LogSeq: 1},
		},
	}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()

	for name, req := range map[string]*agentcomposev2.FollowRunLogsRequest{
		"text":       {RunId: "run-1"},
		"text tail":  {RunId: "run-1", TailLines: 5},
		"structured": {RunId: "run-1", Structured: true},
		"followed":   {RunId: "run-1", Structured: true, Follow: true},
	} {
		stream, err := client.FollowRunLogs(context.Background(), connect.NewRequest(req))
		if err == nil {
			for stream.Receive() {
			}
			err = stream.Err()
		}
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("%s: FollowRunLogs err = %v, want FailedPrecondition", name, err)
		}
		if strings.Contains(err.Error(), root) {
			t.Fatalf("%s: FollowRunLogs err names a host path: %v", name, err)
		}
	}
	// The conversation lives in the database and survives the archive.
	messages, err := client.ListRunMessages(context.Background(), connect.NewRequest(&agentcomposev2.ListRunMessagesRequest{RunId: "run-1"}))
	if err != nil || len(messages.Msg.GetMessages()) != 1 {
		t.Fatalf("ListRunMessages = %v, %v", messages, err)
	}
}
