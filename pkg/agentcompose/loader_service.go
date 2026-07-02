package agentcompose

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	transport "agent-compose/internal/agentcompose/transport"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func (s *Service) ValidateLoader(ctx context.Context, req *connect.Request[agentcomposev1.ValidateLoaderRequest]) (*connect.Response[agentcomposev1.ValidateLoaderResponse], error) {
	return s.loaderTransport().ValidateLoader(ctx, req)
}

func (s *Service) ListLoaders(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListLoadersResponse], error) {
	return s.loaderTransport().ListLoaders(ctx, req)
}

func (s *Service) GetLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return s.loaderTransport().GetLoader(ctx, req)
}

func (s *Service) CreateLoader(ctx context.Context, req *connect.Request[agentcomposev1.CreateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return s.loaderTransport().CreateLoader(ctx, req)
}

func (s *Service) UpdateLoader(ctx context.Context, req *connect.Request[agentcomposev1.UpdateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return s.loaderTransport().UpdateLoader(ctx, req)
}

func (s *Service) DeleteLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[emptypb.Empty], error) {
	return s.loaderTransport().DeleteLoader(ctx, req)
}

func (s *Service) SetLoaderEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return s.loaderTransport().SetLoaderEnabled(ctx, req)
}

func (s *Service) SetLoaderTriggerEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderTriggerEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	return s.loaderTransport().SetLoaderTriggerEnabled(ctx, req)
}

func (s *Service) RunLoaderNow(ctx context.Context, req *connect.Request[agentcomposev1.RunLoaderNowRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	return s.loaderTransport().RunLoaderNow(ctx, req)
}

func (s *Service) ListLoaderRuns(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderRunsRequest]) (*connect.Response[agentcomposev1.ListLoaderRunsResponse], error) {
	return s.loaderTransport().ListLoaderRuns(ctx, req)
}

func (s *Service) GetLoaderRun(ctx context.Context, req *connect.Request[agentcomposev1.LoaderRunIDRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	return s.loaderTransport().GetLoaderRun(ctx, req)
}

func (s *Service) ListLoaderEvents(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderEventsRequest]) (*connect.Response[agentcomposev1.ListLoaderEventsResponse], error) {
	return s.loaderTransport().ListLoaderEvents(ctx, req)
}

func (s *Service) loaderTransport() *transport.LoaderService {
	return transport.NewLoaderService(s.loaders, s.configDB, s.resolveLoaderDefaultAgent)
}

func (s *Service) resolveLoaderDefaultAgent(ctx context.Context, agentID, provider string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return provider, nil
	}
	agent, err := s.configDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return "", err
	}
	if !agent.Enabled {
		return "", fmt.Errorf("agent definition %s is disabled", agentID)
	}
	if strings.TrimSpace(provider) != "" && normalizeAgentKind(provider) == "" {
		return "", fmt.Errorf("loader default agent provider %q is not supported", provider)
	}
	return agent.Provider, nil
}

func parseLoaderRunTimeout(raw string) (time.Duration, error) {
	return transport.ParseLoaderRunTimeout(raw)
}

func protoEnvItemsToModel(items []*agentcomposev1.SessionEnvVar) []SessionEnvVar {
	return transport.ProtoEnvItemsToLoaderModel(items)
}

func toProtoLoaderSummary(item LoaderSummary) *agentcomposev1.LoaderSummary {
	return transport.ToProtoLoaderSummary(item)
}

func toProtoLoaderDetail(item Loader) *agentcomposev1.LoaderDetail {
	return transport.ToProtoLoaderDetail(item)
}

func toProtoLoaderTrigger(item LoaderTrigger) *agentcomposev1.LoaderTrigger {
	return transport.ToProtoLoaderTrigger(item)
}

func toProtoLoaderRunSummary(item LoaderRunSummary) *agentcomposev1.LoaderRunSummary {
	return transport.ToProtoLoaderRunSummary(item)
}

func toProtoLoaderRunDetail(item LoaderRunSummary) *agentcomposev1.LoaderRunDetail {
	return transport.ToProtoLoaderRunDetail(item)
}

func toProtoLoaderEvent(item LoaderEvent) *agentcomposev1.LoaderEvent {
	return transport.ToProtoLoaderEvent(item)
}

func toProtoLoaderTriggerKind(kind string) agentcomposev1.LoaderTriggerKind {
	return transport.ToProtoLoaderTriggerKind(kind)
}

func formatMaybeTime(value time.Time) string {
	return transport.FormatMaybeTime(value)
}
