package api

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestRunInputLookupAndStructuredEvidence(t *testing.T) {
	id, err := runs.ResolveRequestRunID(runs.RequestIdentity{ProjectID: "p", AgentName: "a", Source: domain.ProjectRunSourceManual, ClientRequestID: "request"})
	if err != nil {
		t.Fatal(err)
	}
	evidence := &domain.RunInputPreparation{RunID: id, State: "ready", SandboxID: "sandbox", TemporaryNames: []string{"private-temp"}, Deadline: time.Now().Add(time.Minute), Files: []domain.RunInputFile{{ID: "file", Name: "original", RelativePath: "original (1)"}}}
	handler := NewRunHandler(nil, &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{id: {RunID: id, ProjectID: "p", InputPreparation: evidence}}})
	response, err := handler.GetRun(context.Background(), connect.NewRequest(&agentcomposev2.GetRunRequest{ProjectId: "p", AgentName: "a", Source: agentcomposev2.RunSource_RUN_SOURCE_MANUAL, ClientRequestId: "request"}))
	if err != nil {
		t.Fatal(err)
	}
	input := response.Msg.GetRun().GetInputPreparation()
	if input.GetRunId() != id || input.GetState() != agentcomposev2.RunInputState_RUN_INPUT_STATE_READY || input.GetFiles()[0].GetRelativePath() != "original (1)" {
		t.Fatalf("input=%v", input)
	}
	for _, query := range []*agentcomposev2.GetRunRequest{
		{ClientRequestId: "request"},
		{RunId: id, ClientRequestId: "request", ProjectId: "p", AgentName: "a", Source: agentcomposev2.RunSource_RUN_SOURCE_MANUAL},
		{ProjectId: "p", AgentName: "a", ClientRequestId: "request", Source: agentcomposev2.RunSource(999)},
	} {
		if _, err := handler.GetRun(context.Background(), connect.NewRequest(query)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("invalid selector err=%v", err)
		}
	}
	if _, err := handler.GetRun(context.Background(), connect.NewRequest(&agentcomposev2.GetRunRequest{RunId: id, ProjectId: "other"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("wrong project=%v", err)
	}
	failure := RunAttachOutputToProto(runs.RunAttachOutput{Kind: runs.RunAttachOutputError, Input: evidence, Terminal: true})
	if failure.GetError().GetOperationError().GetEffect() != agentcomposev2.OperationEffect_OPERATION_EFFECT_UNKNOWN || failure.GetError().GetInputPreparation().GetRunId() != id {
		t.Fatalf("structured failure=%v", failure)
	}
	expired := *evidence
	expired.State = "publishing"
	expired.Deadline = time.Now().Add(-time.Second)
	if got := RunInputToProto(&expired); got.GetState() != agentcomposev2.RunInputState_RUN_INPUT_STATE_UNKNOWN || got.GetFiles()[0].GetRelativePath() != "" {
		t.Fatal("expired publication not unknown")
	}
}
