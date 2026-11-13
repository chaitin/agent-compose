package api

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/runhistory"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type ProjectRunHistoryRemover interface {
	RemoveRunHistory(context.Context, runhistory.Identity) error
}

func (h *ProjectHandler) RemoveRunHistory(ctx context.Context, req *connect.Request[agentcomposev2.RemoveRunHistoryRequest]) (*connect.Response[agentcomposev2.RemoveRunHistoryResponse], error) {
	id := runhistory.Identity{ProjectID: req.Msg.GetProjectId(), RunID: req.Msg.GetRunId(), SchedulerID: req.Msg.GetSchedulerId()}
	kind := ResourceRun
	switch req.Msg.GetKind() {
	case agentcomposev2.RunHistoryKind_RUN_HISTORY_KIND_RUN:
		id.Kind = runhistory.Run
	case agentcomposev2.RunHistoryKind_RUN_HISTORY_KIND_SCHEDULER_RUN:
		id.Kind, kind = runhistory.SchedulerRun, ResourceSchedulerRun
	}
	if err := id.Validate(); err != nil {
		return nil, WriteErrorNotAppliedForCode(connect.CodeInvalidArgument, kind, err)
	}
	if h.runHistory == nil {
		return nil, WriteErrorNotAppliedForCode(connect.CodeFailedPrecondition, kind, fmt.Errorf("run history remover is unavailable"))
	}
	if err := h.runHistory.RemoveRunHistory(ctx, id); err != nil {
		return nil, ConnectErrorForDomainWrite(err, kind)
	}
	return connect.NewResponse(&agentcomposev2.RemoveRunHistoryResponse{}), nil
}
