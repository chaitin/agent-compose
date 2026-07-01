package agentcompose

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/agentcompose/api"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func (s *Service) ValidateLoader(ctx context.Context, req *connect.Request[agentcomposev1.ValidateLoaderRequest]) (*connect.Response[agentcomposev1.ValidateLoaderResponse], error) {
	result, err := s.loaders.Validate(ctx, req.Msg.GetRuntime(), req.Msg.GetScript())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	resp := &agentcomposev1.ValidateLoaderResponse{Warnings: append([]string(nil), result.Warnings...)}
	for _, trigger := range result.Triggers {
		resp.Triggers = append(resp.Triggers, toProtoLoaderTrigger(trigger))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) ListLoaders(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListLoadersResponse], error) {
	_ = req
	items, err := s.configDB.ListLoaderSummaries(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListLoadersResponse{}
	for _, item := range items {
		resp.Loaders = append(resp.Loaders, toProtoLoaderSummary(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) GetLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	item, err := s.configDB.GetLoader(ctx, req.Msg.GetLoaderId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: toProtoLoaderDetail(item)}), nil
}

func (s *Service) CreateLoader(ctx context.Context, req *connect.Request[agentcomposev1.CreateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	defaultAgent, err := s.resolveLoaderDefaultAgent(ctx, req.Msg.GetAgentId(), req.Msg.GetDefaultAgent())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	item, err := s.loaders.CreateLoader(ctx, Loader{
		Summary: LoaderSummary{
			Name:              req.Msg.GetName(),
			Description:       req.Msg.GetDescription(),
			Enabled:           req.Msg.GetEnabled(),
			Runtime:           req.Msg.GetRuntime(),
			WorkspaceID:       req.Msg.GetWorkspaceId(),
			AgentID:           req.Msg.GetAgentId(),
			Driver:            req.Msg.GetDriver(),
			GuestImage:        req.Msg.GetGuestImage(),
			DefaultAgent:      defaultAgent,
			SessionPolicy:     req.Msg.GetSessionPolicy(),
			ConcurrencyPolicy: req.Msg.GetConcurrencyPolicy(),
			CapsetIDs:         req.Msg.GetCapsetIds(),
		},
		Script:   req.Msg.GetScript(),
		EnvItems: protoEnvItemsToModel(req.Msg.GetEnvItems()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: toProtoLoaderDetail(item)}), nil
}

func (s *Service) UpdateLoader(ctx context.Context, req *connect.Request[agentcomposev1.UpdateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	defaultAgent, err := s.resolveLoaderDefaultAgent(ctx, req.Msg.GetAgentId(), req.Msg.GetDefaultAgent())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	item, err := s.loaders.UpdateLoader(ctx, Loader{
		Summary: LoaderSummary{
			ID:                req.Msg.GetLoaderId(),
			Name:              req.Msg.GetName(),
			Description:       req.Msg.GetDescription(),
			Enabled:           req.Msg.GetEnabled(),
			Runtime:           req.Msg.GetRuntime(),
			WorkspaceID:       req.Msg.GetWorkspaceId(),
			AgentID:           req.Msg.GetAgentId(),
			Driver:            req.Msg.GetDriver(),
			GuestImage:        req.Msg.GetGuestImage(),
			DefaultAgent:      defaultAgent,
			SessionPolicy:     req.Msg.GetSessionPolicy(),
			ConcurrencyPolicy: req.Msg.GetConcurrencyPolicy(),
			CapsetIDs:         req.Msg.GetCapsetIds(),
		},
		Script:   req.Msg.GetScript(),
		EnvItems: protoEnvItemsToModel(req.Msg.GetEnvItems()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: toProtoLoaderDetail(item)}), nil
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

func (s *Service) DeleteLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := s.loaders.DeleteLoader(ctx, req.Msg.GetLoaderId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *Service) SetLoaderEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	item, err := s.loaders.SetLoaderEnabled(ctx, req.Msg.GetLoaderId(), req.Msg.GetEnabled())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: toProtoLoaderDetail(item)}), nil
}

func (s *Service) SetLoaderTriggerEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderTriggerEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	item, err := s.loaders.SetLoaderTriggerEnabled(ctx, req.Msg.GetLoaderId(), req.Msg.GetTriggerId(), req.Msg.GetEnabled())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: toProtoLoaderDetail(item)}), nil
}

func (s *Service) RunLoaderNow(ctx context.Context, req *connect.Request[agentcomposev1.RunLoaderNowRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	timeout, err := parseLoaderRunTimeout(req.Msg.GetTimeout())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	run, err := s.loaders.RunNow(ctx, req.Msg.GetLoaderId(), req.Msg.GetTriggerId(), req.Msg.GetPayloadJson(), timeout)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderRunResponse{Run: toProtoLoaderRunDetail(run)}), nil
}

func parseLoaderRunTimeout(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("loader run timeout must be positive")
	}
	return timeout, nil
}

func (s *Service) ListLoaderRuns(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderRunsRequest]) (*connect.Response[agentcomposev1.ListLoaderRunsResponse], error) {
	runs, err := s.configDB.ListLoaderRuns(ctx, req.Msg.GetLoaderId(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListLoaderRunsResponse{}
	for _, item := range runs {
		resp.Runs = append(resp.Runs, toProtoLoaderRunSummary(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) GetLoaderRun(ctx context.Context, req *connect.Request[agentcomposev1.LoaderRunIDRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	run, err := s.configDB.GetLoaderRun(ctx, req.Msg.GetLoaderId(), req.Msg.GetRunId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderRunResponse{Run: toProtoLoaderRunDetail(run)}), nil
}

func (s *Service) ListLoaderEvents(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderEventsRequest]) (*connect.Response[agentcomposev1.ListLoaderEventsResponse], error) {
	events, err := s.configDB.ListLoaderEvents(ctx, req.Msg.GetLoaderId(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListLoaderEventsResponse{}
	for _, item := range events {
		resp.Events = append(resp.Events, toProtoLoaderEvent(item))
	}
	return connect.NewResponse(resp), nil
}

func protoEnvItemsToModel(items []*agentcomposev1.SessionEnvVar) []SessionEnvVar {
	if len(items) == 0 {
		return nil
	}
	result := make([]SessionEnvVar, 0, len(items))
	for _, item := range items {
		result = append(result, SessionEnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	return normalizeEnvItems(result)
}

func toProtoLoaderSummary(item LoaderSummary) *agentcomposev1.LoaderSummary {
	return api.LoaderSummaryToProto(item)
}

func toProtoLoaderDetail(item Loader) *agentcomposev1.LoaderDetail {
	return api.LoaderDetailToProto(item)
}

func toProtoLoaderTrigger(item LoaderTrigger) *agentcomposev1.LoaderTrigger {
	return api.LoaderTriggerToProto(item)
}

func toProtoLoaderRunSummary(item LoaderRunSummary) *agentcomposev1.LoaderRunSummary {
	return api.LoaderRunSummaryToProto(item)
}

func toProtoLoaderRunDetail(item LoaderRunSummary) *agentcomposev1.LoaderRunDetail {
	return api.LoaderRunDetailToProto(item)
}

func toProtoLoaderEvent(item LoaderEvent) *agentcomposev1.LoaderEvent {
	return api.LoaderEventToProto(item)
}

func toProtoLoaderTriggerKind(kind string) agentcomposev1.LoaderTriggerKind {
	return api.LoaderTriggerKindToProto(kind)
}
