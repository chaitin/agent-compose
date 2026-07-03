package agentcompose

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/dashboard"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func NewDashboardOverviewAggregator(di do.Injector) (*dashboard.Aggregator, error) {
	return newDashboardOverviewAggregator(do.MustInvoke[*Store](di), do.MustInvoke[*ConfigStore](di)), nil
}

func newDashboardOverviewAggregator(store *Store, configDB *ConfigStore) *dashboard.Aggregator {
	return dashboard.NewAggregator(store, configDB)
}

func NewDashboardOverviewHub(di do.Injector) (*dashboard.Hub, error) {
	ctx := do.MustInvoke[context.Context](di)
	aggregator := do.MustInvoke[*dashboard.Aggregator](di)
	return newDashboardOverviewHub(ctx, aggregator, 250*time.Millisecond), nil
}

func newDashboardOverviewHub(ctx context.Context, aggregator *dashboard.Aggregator, debounce time.Duration) *dashboard.Hub {
	return dashboard.NewHub(ctx, aggregator, debounce)
}

func (s *Service) GetDashboardOverview(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.DashboardOverviewResponse], error) {
	_ = req
	overview, err := s.dashboard.Current(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev1.DashboardOverviewResponse{Overview: overview}), nil
}

func (s *Service) WatchDashboardOverview(ctx context.Context, req *connect.Request[emptypb.Empty], stream *connect.ServerStream[agentcomposev1.DashboardOverviewEvent]) error {
	_ = req
	prepareStreamingHeaders(stream.ResponseHeader())
	overview, err := s.dashboard.Current(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if err := stream.Send(&agentcomposev1.DashboardOverviewEvent{Overview: overview, Reason: "initial"}); err != nil {
		return connect.NewError(connect.CodeUnknown, err)
	}
	events, cancel := s.dashboard.Watch(ctx)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := stream.Send(&agentcomposev1.DashboardOverviewEvent{Overview: event.Overview, Reason: event.Reason}); err != nil {
				return connect.NewError(connect.CodeUnknown, err)
			}
		}
	}
}
