package agentcompose

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/loaders"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func (s *Service) loaderService() *LoaderService {
	if s.loaderHandlers != nil {
		return s.loaderHandlers
	}
	s.loaderHandlers = loaders.NewService(s.configDB, s.loaders, s.bus)
	return s.loaderHandlers
}

func (s *Service) publishLoaderTopic(topic string, payload map[string]any) {
	if s == nil || s.bus == nil {
		return
	}
	s.bus.Publish(LoaderTopicEvent{
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
}

func (s *Service) ValidateLoader(ctx context.Context, req *connect.Request[agentcomposev1.ValidateLoaderRequest]) (*connect.Response[agentcomposev1.ValidateLoaderResponse], error) {
	return s.loaderService().ValidateLoader(ctx, req)
}

func (s *Service) ListLoaders(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListLoadersResponse], error) {
	return s.loaderService().ListLoaders(ctx, req)
}

func (s *Service) GetLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return s.loaderService().GetLoader(ctx, req)
}

func (s *Service) CreateLoader(ctx context.Context, req *connect.Request[agentcomposev1.CreateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return s.loaderService().CreateLoader(ctx, req)
}

func (s *Service) UpdateLoader(ctx context.Context, req *connect.Request[agentcomposev1.UpdateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return s.loaderService().UpdateLoader(ctx, req)
}

func (s *Service) DeleteLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[emptypb.Empty], error) {
	return s.loaderService().DeleteLoader(ctx, req)
}

func (s *Service) SetLoaderEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return s.loaderService().SetLoaderEnabled(ctx, req)
}

func (s *Service) SetLoaderTriggerEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderTriggerEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return s.loaderService().SetLoaderTriggerEnabled(ctx, req)
}

func (s *Service) RunLoaderNow(ctx context.Context, req *connect.Request[agentcomposev1.RunLoaderNowRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	return s.loaderService().RunLoaderNow(ctx, req)
}

func (s *Service) ListLoaderRuns(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderRunsRequest]) (*connect.Response[agentcomposev1.ListLoaderRunsResponse], error) {
	return s.loaderService().ListLoaderRuns(ctx, req)
}

func (s *Service) GetLoaderRun(ctx context.Context, req *connect.Request[agentcomposev1.LoaderRunIDRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	return s.loaderService().GetLoaderRun(ctx, req)
}

func (s *Service) ListLoaderEvents(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderEventsRequest]) (*connect.Response[agentcomposev1.ListLoaderEventsResponse], error) {
	return s.loaderService().ListLoaderEvents(ctx, req)
}
