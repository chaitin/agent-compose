package api

import (
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
)

func TestRunAttachOutputToProtoMapsFrames(t *testing.T) {
	run := domain.ProjectRunRecord{RunID: "run-1", ProjectID: "project-1", Status: domain.ProjectRunStatusSucceeded}
	cases := []struct {
		name   string
		output runs.RunAttachOutput
		check  func(*testing.T, any)
	}{
		{name: "started", output: runs.RunAttachOutput{Kind: runs.RunAttachOutputStarted, Run: run, SandboxID: "sandbox-1", Warnings: []string{"warning"}}},
		{name: "output", output: runs.RunAttachOutput{Kind: runs.RunAttachOutputData, Data: []byte("chunk"), Stream: domain.StdioStderr, TTY: true}},
		{name: "agent event", output: runs.RunAttachOutput{Kind: runs.RunAttachOutputAgentEvent, Name: "event", Text: "text", PayloadJSON: `{}`}},
		{name: "turn completed", output: runs.RunAttachOutput{Kind: runs.RunAttachOutputAgentTurnCompleted, Run: run, ResultJSON: `{}`}},
		{name: "result", output: runs.RunAttachOutput{Kind: runs.RunAttachOutputResult, Run: run, Success: true, ExitCode: 3, Output: "out", ResultJSON: `{}`}},
		{name: "error", output: runs.RunAttachOutput{Kind: runs.RunAttachOutputError, Code: "failed", Error: "message", Terminal: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			response := RunAttachOutputToProto(tc.output)
			if response.GetServerFrameId() == "" || response.GetCreatedAt() == nil || response.GetFrame() == nil {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestRunAttachOutputToProtoPreservesCreatedAt(t *testing.T) {
	createdAt := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	response := RunAttachOutputToProto(runs.RunAttachOutput{Kind: runs.RunAttachOutputError, CreatedAt: createdAt})
	if got := response.GetCreatedAt().AsTime(); !got.Equal(createdAt) {
		t.Fatalf("created at = %s, want %s", got, createdAt)
	}
}

// A declined human message names the client frame it answers in the error's
// details. Without them a client that has since sent another message could not
// tell which of its messages the answer is about.
func TestRunAttachOutputToProtoCarriesErrorDetails(t *testing.T) {
	details := map[string]string{"client_frame_id": "frame-1"}
	response := RunAttachOutputToProto(runs.RunAttachOutput{Kind: runs.RunAttachOutputError, Code: "duplicate_message", Details: details})
	if got := response.GetError().GetDetails()["client_frame_id"]; got != "frame-1" {
		t.Fatalf("details client_frame_id = %q, want frame-1", got)
	}
	// The frame holds its own copy, so a sender that reuses its map cannot
	// rewrite a frame already on its way out.
	details["client_frame_id"] = "changed"
	if got := response.GetError().GetDetails()["client_frame_id"]; got != "frame-1" {
		t.Fatalf("details followed the sender's map to %q", got)
	}
}
