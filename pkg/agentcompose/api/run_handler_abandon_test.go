package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// TestRunHandlerAbandonRunCompletionEndsAStuckCleanup pins the operator exit for
// a run whose sandbox cleanup cannot converge: the RPC reaches the app layer,
// the run id is trimmed the way the other run RPCs trim it, and the answer says
// which terminal state the run landed in.
func TestRunHandlerAbandonRunCompletionEndsAStuckCleanup(t *testing.T) {
	stopper := &sequenceRunStopper{
		abandoned: true,
		abandonRun: domain.ProjectRunRecord{
			RunID: "run-1", Status: domain.ProjectRunStatusFailed,
			CleanupError: "sandbox cleanup abandoned by operator; the sandbox runtime may still exist: cluster gone",
		},
	}
	handler := NewRunHandler(nil, &apiProjectRunStore{}, stopper)
	client, closeServer := newRunHandlerTestClient(t, handler)
	defer closeServer()

	response, err := client.AbandonRunCompletion(context.Background(), connect.NewRequest(&agentcomposev2.AbandonRunCompletionRequest{
		RunId: " run-1 ", Reason: "cluster gone",
	}))
	if err != nil {
		t.Fatalf("AbandonRunCompletion returned error: %v", err)
	}
	if !response.Msg.GetAbandoned() {
		t.Fatalf("AbandonRunCompletion response = %#v, want abandoned", response.Msg)
	}
	if run := response.Msg.GetRun(); run.GetSummary().GetRunId() != "run-1" || run.GetCleanupError() == "" {
		t.Fatalf("returned run = %#v, want the finalized run with its cleanup error", run)
	}
	if stopper.abandonCalls != 1 || stopper.abandonNote != "cluster gone" {
		t.Fatalf("abandon calls/note = %d/%q, want 1/\"cluster gone\"", stopper.abandonCalls, stopper.abandonNote)
	}
}

// TestRunHandlerAbandonRunCompletionClassifiesRefusals keeps the operator-facing
// error useful: a run that cannot be abandoned is a client-side precondition
// failure, not an internal daemon error, and a missing run id never reaches the
// app layer.
func TestRunHandlerAbandonRunCompletionClassifiesRefusals(t *testing.T) {
	tests := []struct {
		name    string
		stopper *sequenceRunStopper
		runID   string
		want    connect.Code
	}{
		{
			name:    "still executing",
			stopper: &sequenceRunStopper{abandonErr: domain.ClassifyError(domain.ErrFailedPrecondition, "run is still executing, so stop it instead", nil)},
			runID:   "run-1",
			want:    connect.CodeFailedPrecondition,
		},
		{name: "missing run id", stopper: &sequenceRunStopper{}, runID: "  ", want: connect.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewRunHandler(nil, &apiProjectRunStore{}, tt.stopper)
			client, closeServer := newRunHandlerTestClient(t, handler)
			defer closeServer()

			_, err := client.AbandonRunCompletion(context.Background(), connect.NewRequest(&agentcomposev2.AbandonRunCompletionRequest{RunId: tt.runID}))
			if got := connect.CodeOf(err); got != tt.want {
				t.Fatalf("AbandonRunCompletion code = %v (err=%v), want %v", got, err, tt.want)
			}
			if tt.want == connect.CodeInvalidArgument && tt.stopper.abandonCalls != 0 {
				t.Fatalf("abandon calls = %d, want the invalid request rejected before the app layer", tt.stopper.abandonCalls)
			}
		})
	}

	// A handler without the capability wired answers unimplemented rather than
	// pretending the run was abandoned.
	handler := NewRunHandler(nil, &apiProjectRunStore{})
	client, closeServer := newRunHandlerTestClient(t, handler)
	defer closeServer()
	if _, err := client.AbandonRunCompletion(context.Background(), connect.NewRequest(&agentcomposev2.AbandonRunCompletionRequest{RunId: "run-1"})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("AbandonRunCompletion without a stopper error = %v, want unimplemented", err)
	}
}
