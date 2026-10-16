package api

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// AbandonRunCompletion ends a run whose sandbox cleanup will not converge. It is
// the transport half of the operator exit issue #771 asks for: the app layer
// decides the terminal state and records that the runtime may still exist, and
// this handler only validates the request and maps domain errors to Connect
// codes so a refusal is a precondition failure rather than an internal error.
func (h *RunHandler) AbandonRunCompletion(ctx context.Context, req *connect.Request[agentcomposev2.AbandonRunCompletionRequest]) (*connect.Response[agentcomposev2.AbandonRunCompletionResponse], error) {
	if h.stopper == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("run completion abandonment is not supported by this daemon"))
	}
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	run, abandoned, err := h.stopper.AbandonRunCompletion(ctx, runID, req.Msg.GetReason())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.AbandonRunCompletionResponse{
		Run:       ProjectRunDetailToProto(run),
		Abandoned: abandoned,
	}), nil
}
