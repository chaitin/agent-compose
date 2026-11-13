package runs

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// TestE2ERunInputAttachReconcilesStoredInput drives the public attach entry
// point against a run whose input evidence was left behind by a previous
// attempt. The reattach must replay the stored evidence and report it as a
// single InputReady or Error frame instead of opening a second execution.
func TestE2ERunInputAttachReconcilesStoredInput(t *testing.T) {
	cases := []struct {
		name       string
		stored     domain.RunInputState
		deadline   time.Time
		wantKind   RunAttachOutputKind
		wantState  domain.RunInputState
		wantCode   string
		wantFrames int
	}{
		{
			name:       "ready input replays as input_ready",
			stored:     domain.RunInputStateReady,
			deadline:   time.Now().UTC().Add(time.Hour),
			wantKind:   RunAttachOutputInputReady,
			wantState:  domain.RunInputStateReady,
			wantFrames: 1,
		},
		{
			name:       "unknown input requires reconciliation",
			stored:     domain.RunInputStateUnknown,
			deadline:   time.Now().UTC().Add(time.Hour),
			wantKind:   RunAttachOutputError,
			wantState:  domain.RunInputStateUnknown,
			wantCode:   "input_not_ready",
			wantFrames: 1,
		},
		{
			name:       "expired receiving input collapses to unknown",
			stored:     domain.RunInputStateReceiving,
			deadline:   time.Now().UTC().Add(-time.Minute),
			wantKind:   RunAttachOutputError,
			wantState:  domain.RunInputStateUnknown,
			wantCode:   "input_not_ready",
			wantFrames: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controller, configDB, _ := newTestRunAttachController(t, nil)
			runID, err := projects.StableProjectRunID("project-1", "worker", domain.ProjectRunSourceManual, "req-1")
			if err != nil {
				t.Fatalf("StableProjectRunID: %v", err)
			}
			configDB.runs[runID] = domain.ProjectRunRecord{
				RunID:     runID,
				ProjectID: "project-1",
				AgentName: "worker",
				Source:    domain.ProjectRunSourceManual,
				Status:    domain.ProjectRunStatusPending,
				InputPreparation: &domain.RunInputPreparation{
					RunID:    runID,
					State:    tc.stored,
					Deadline: tc.deadline,
					Files:    []domain.RunInputFile{{ID: "file", Name: "a.txt", Size: 4}},
				},
			}

			var sent []RunAttachOutput
			err = controller.RunProjectCommandAttachRegistered(
				context.Background(), context.Background(),
				func() (RunAttachInput, error) {
					return RunAttachInput{
						Kind:    RunAttachInputStart,
						Mode:    RunAttachModePrompt,
						Request: RunAgentRequest{ProjectID: "project-1", AgentName: "worker", Source: domain.ProjectRunSourceManual, ClientRequestID: "req-1", Prompt: "read"},
					}, nil
				},
				func(output RunAttachOutput) error { sent = append(sent, output); return nil },
				nil,
			)
			if err != nil {
				t.Fatalf("RunProjectCommandAttachRegistered() error = %v", err)
			}
			if len(sent) != tc.wantFrames {
				t.Fatalf("sent %d frames, want %d", len(sent), tc.wantFrames)
			}
			got := sent[0]
			if got.Kind != tc.wantKind {
				t.Fatalf("sent[0].Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Input == nil {
				t.Fatalf("sent[0].Input is nil, want %s evidence", tc.wantState)
			}
			if got.Input.State != tc.wantState {
				t.Fatalf("sent[0].Input.State = %q, want %q", got.Input.State, tc.wantState)
			}
			if tc.wantCode != "" && got.Code != tc.wantCode {
				t.Fatalf("sent[0].Code = %q, want %q", got.Code, tc.wantCode)
			}
		})
	}
}

// TestE2ERunInputAttachValidatesFirstFrame covers the public attach entry
// point's request validation before any run is created.
func TestE2ERunInputAttachValidatesFirstFrame(t *testing.T) {
	cases := []struct {
		name    string
		receive RunAttachReceiver
	}{
		{
			name:    "start frame is required",
			receive: func() (RunAttachInput, error) { return RunAttachInput{}, io.EOF },
		},
		{
			name: "first frame must be start",
			receive: func() (RunAttachInput, error) {
				return RunAttachInput{Kind: RunAttachInputFileChunk, FileID: "file"}, nil
			},
		},
		{
			name:    "command mode is required",
			receive: func() (RunAttachInput, error) { return RunAttachInput{Kind: RunAttachInputStart}, nil },
		},
		{
			name: "prompt is required",
			receive: func() (RunAttachInput, error) {
				return RunAttachInput{Kind: RunAttachInputStart, Mode: RunAttachModePrompt, Request: RunAgentRequest{ProjectID: "project-1", AgentName: "worker"}}, nil
			},
		},
		{
			name: "command is required",
			receive: func() (RunAttachInput, error) {
				return RunAttachInput{Kind: RunAttachInputStart, Mode: RunAttachModeCommand, Request: RunAgentRequest{ProjectID: "project-1", AgentName: "worker"}}, nil
			},
		},
		{
			name: "files cannot be appended on reattach",
			receive: func() (RunAttachInput, error) {
				return RunAttachInput{Kind: RunAttachInputStart, RunID: "existing-run", Mode: RunAttachModePrompt, Request: RunAgentRequest{InputFiles: []domain.RunInputFile{{ID: "file", Name: "a.txt", Size: 4}}}}, nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controller, _, _ := newTestRunAttachController(t, nil)
			err := controller.RunProjectCommandAttachRegistered(
				context.Background(), context.Background(),
				tc.receive,
				func(RunAttachOutput) error { return nil },
				nil,
			)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("RunProjectCommandAttachRegistered() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}
