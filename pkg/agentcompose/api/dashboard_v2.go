package api

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"agent-compose/pkg/dashboard"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type DashboardV2Handler struct{ hub *dashboard.Hub }

func NewDashboardV2Handler(hub *dashboard.Hub) *DashboardV2Handler {
	return &DashboardV2Handler{hub: hub}
}
func (h *DashboardV2Handler) GetDashboardOverview(ctx context.Context, _ *connect.Request[agentcomposev2.GetDashboardOverviewRequest]) (*connect.Response[agentcomposev2.GetDashboardOverviewResponse], error) {
	overview, err := h.hub.Current(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.GetDashboardOverviewResponse{Overview: dashboardOverviewV2(overview)}), nil
}
func (h *DashboardV2Handler) WatchDashboardOverview(ctx context.Context, _ *connect.Request[agentcomposev2.WatchDashboardOverviewRequest], stream *connect.ServerStream[agentcomposev2.WatchDashboardOverviewResponse]) error {
	PrepareStreamingHeaders(stream.ResponseHeader())
	overview, err := h.hub.Current(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if err := stream.Send(&agentcomposev2.WatchDashboardOverviewResponse{Overview: dashboardOverviewV2(overview), Reason: "initial"}); err != nil {
		return connect.NewError(connect.CodeUnknown, err)
	}
	events, cancel := h.hub.Watch(ctx)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return nil
			}
			v := event.Overview
			if err := stream.Send(&agentcomposev2.WatchDashboardOverviewResponse{Overview: dashboardOverviewV2(v), Reason: event.Reason}); err != nil {
				return connect.NewError(connect.CodeUnknown, err)
			}
		}
	}
}
func dashboardOverviewV2(overview *dashboard.Overview) *agentcomposev2.DashboardOverview {
	if overview == nil {
		return nil
	}
	return &agentcomposev2.DashboardOverview{
		Runs: &agentcomposev2.RunOverview{
			RunningCount: overview.Runs.RunningCount, RecentCount: overview.Runs.RecentCount, AttentionCount: overview.Runs.AttentionCount,
		},
		UpdatedAt: timestamppb.New(overview.UpdatedAt),
	}
}
