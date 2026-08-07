package api

import (
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"

	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func RunAttachOutputToProto(output runs.RunAttachOutput) *agentcomposev2.AttachAgentRunResponse {
	createdAt := output.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	response := &agentcomposev2.AttachAgentRunResponse{ServerFrameId: uuid.NewString(), CreatedAt: timestamppb.New(createdAt)}
	switch output.Kind {
	case runs.RunAttachOutputStarted:
		response.Frame = &agentcomposev2.AttachAgentRunResponse_Started{Started: &agentcomposev2.AttachStarted{OperationId: output.Run.RunID, RunId: output.Run.RunID, SandboxId: output.SandboxID, Run: ProjectRunSummaryToProto(output.Run), Warnings: append([]string(nil), output.Warnings...)}}
	case runs.RunAttachOutputData:
		stream := StdioStreamToProto(output.Stream)
		response.Frame = &agentcomposev2.AttachAgentRunResponse_Output{Output: &agentcomposev2.AttachOutput{Data: append([]byte(nil), output.Data...), Stream: stream, Tty: output.TTY, Transcript: &agentcomposev2.TranscriptEvent{Stream: stream, Text: string(output.Data), CreatedAt: response.CreatedAt}}}
	case runs.RunAttachOutputAgentEvent:
		response.Frame = &agentcomposev2.AttachAgentRunResponse_AgentEvent{AgentEvent: &agentcomposev2.AttachAgentEvent{Name: output.Name, Text: output.Text, PayloadJson: output.PayloadJSON, CreatedAt: response.CreatedAt}}
	case runs.RunAttachOutputAgentTurnCompleted:
		response.Frame = &agentcomposev2.AttachAgentRunResponse_AgentTurnCompleted{AgentTurnCompleted: &agentcomposev2.AttachAgentTurnCompleted{RunId: output.Run.RunID, ResultJson: output.ResultJSON, Warnings: append([]string(nil), output.Warnings...)}}
	case runs.RunAttachOutputResult:
		response.Frame = &agentcomposev2.AttachAgentRunResponse_Result{Result: &agentcomposev2.AttachResult{ExitCode: int32(output.ExitCode), Success: output.Success, Run: ProjectRunSummaryToProto(output.Run), Output: output.Output, ResultJson: output.ResultJSON, Error: output.Error}}
	case runs.RunAttachOutputError:
		response.Frame = &agentcomposev2.AttachAgentRunResponse_Error{Error: &agentcomposev2.AttachError{Code: output.Code, Message: output.Error, Terminal: output.Terminal}}
	}
	return response
}
