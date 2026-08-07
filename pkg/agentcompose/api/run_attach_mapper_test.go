package api

import (
	"testing"
	"time"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
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
