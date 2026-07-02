package transport

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	loaderdomain "agent-compose/internal/agentcompose/loader"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type LoaderManager interface {
	Validate(context.Context, string, string) (loaderdomain.LoaderValidationResult, error)
	CreateLoader(context.Context, loaderdomain.Definition) (loaderdomain.Definition, error)
	UpdateLoader(context.Context, loaderdomain.Definition) (loaderdomain.Definition, error)
	DeleteLoader(context.Context, string) error
	SetLoaderEnabled(context.Context, string, bool) (loaderdomain.Definition, error)
	SetLoaderTriggerEnabled(context.Context, string, string, bool) (loaderdomain.Definition, error)
	RunNow(context.Context, string, string, string, time.Duration) (loaderdomain.RunSummary, error)
}

type LoaderStore interface {
	ListLoaderSummaries(context.Context) ([]loaderdomain.Summary, error)
	GetLoader(context.Context, string) (loaderdomain.Definition, error)
	ListLoaderRuns(context.Context, string, int) ([]loaderdomain.RunSummary, error)
	GetLoaderRun(context.Context, string, string) (loaderdomain.RunSummary, error)
	ListLoaderEvents(context.Context, string, int) ([]loaderdomain.Event, error)
}

type ResolveLoaderDefaultAgentFunc func(context.Context, string, string) (string, error)

type LoaderService struct {
	Manager                   LoaderManager
	Store                     LoaderStore
	ResolveLoaderDefaultAgent ResolveLoaderDefaultAgentFunc
}

func NewLoaderService(manager LoaderManager, store LoaderStore, resolveDefaultAgent ResolveLoaderDefaultAgentFunc) *LoaderService {
	return &LoaderService{Manager: manager, Store: store, ResolveLoaderDefaultAgent: resolveDefaultAgent}
}

func (s *LoaderService) ValidateLoader(ctx context.Context, req *connect.Request[agentcomposev1.ValidateLoaderRequest]) (*connect.Response[agentcomposev1.ValidateLoaderResponse], error) {
	result, err := s.Manager.Validate(ctx, req.Msg.GetRuntime(), req.Msg.GetScript())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	resp := &agentcomposev1.ValidateLoaderResponse{Warnings: append([]string(nil), result.Warnings...)}
	for _, trigger := range result.Triggers {
		resp.Triggers = append(resp.Triggers, ToProtoLoaderTrigger(trigger))
	}
	return connect.NewResponse(resp), nil
}

func (s *LoaderService) ListLoaders(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListLoadersResponse], error) {
	_ = req
	items, err := s.Store.ListLoaderSummaries(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListLoadersResponse{}
	for _, item := range items {
		resp.Loaders = append(resp.Loaders, ToProtoLoaderSummary(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *LoaderService) GetLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	item, err := s.Store.GetLoader(ctx, req.Msg.GetLoaderId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: ToProtoLoaderDetail(item)}), nil
}

func (s *LoaderService) CreateLoader(ctx context.Context, req *connect.Request[agentcomposev1.CreateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	defaultAgent, err := s.resolveDefaultAgent(ctx, req.Msg.GetAgentId(), req.Msg.GetDefaultAgent())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	item, err := s.Manager.CreateLoader(ctx, loaderdomain.Definition{
		Summary: loaderdomain.Summary{
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
		EnvItems: ProtoEnvItemsToLoaderModel(req.Msg.GetEnvItems()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: ToProtoLoaderDetail(item)}), nil
}

func (s *LoaderService) UpdateLoader(ctx context.Context, req *connect.Request[agentcomposev1.UpdateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	defaultAgent, err := s.resolveDefaultAgent(ctx, req.Msg.GetAgentId(), req.Msg.GetDefaultAgent())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	item, err := s.Manager.UpdateLoader(ctx, loaderdomain.Definition{
		Summary: loaderdomain.Summary{
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
		EnvItems: ProtoEnvItemsToLoaderModel(req.Msg.GetEnvItems()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: ToProtoLoaderDetail(item)}), nil
}

func (s *LoaderService) DeleteLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := s.Manager.DeleteLoader(ctx, req.Msg.GetLoaderId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *LoaderService) SetLoaderEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	item, err := s.Manager.SetLoaderEnabled(ctx, req.Msg.GetLoaderId(), req.Msg.GetEnabled())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: ToProtoLoaderDetail(item)}), nil
}

func (s *LoaderService) SetLoaderTriggerEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderTriggerEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	item, err := s.Manager.SetLoaderTriggerEnabled(ctx, req.Msg.GetLoaderId(), req.Msg.GetTriggerId(), req.Msg.GetEnabled())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: ToProtoLoaderDetail(item)}), nil
}

func (s *LoaderService) RunLoaderNow(ctx context.Context, req *connect.Request[agentcomposev1.RunLoaderNowRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	timeout, err := ParseLoaderRunTimeout(req.Msg.GetTimeout())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	run, err := s.Manager.RunNow(ctx, req.Msg.GetLoaderId(), req.Msg.GetTriggerId(), req.Msg.GetPayloadJson(), timeout)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderRunResponse{Run: ToProtoLoaderRunDetail(run)}), nil
}

func (s *LoaderService) ListLoaderRuns(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderRunsRequest]) (*connect.Response[agentcomposev1.ListLoaderRunsResponse], error) {
	runs, err := s.Store.ListLoaderRuns(ctx, req.Msg.GetLoaderId(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListLoaderRunsResponse{}
	for _, item := range runs {
		resp.Runs = append(resp.Runs, ToProtoLoaderRunSummary(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *LoaderService) GetLoaderRun(ctx context.Context, req *connect.Request[agentcomposev1.LoaderRunIDRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	run, err := s.Store.GetLoaderRun(ctx, req.Msg.GetLoaderId(), req.Msg.GetRunId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderRunResponse{Run: ToProtoLoaderRunDetail(run)}), nil
}

func (s *LoaderService) ListLoaderEvents(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderEventsRequest]) (*connect.Response[agentcomposev1.ListLoaderEventsResponse], error) {
	events, err := s.Store.ListLoaderEvents(ctx, req.Msg.GetLoaderId(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListLoaderEventsResponse{}
	for _, item := range events {
		resp.Events = append(resp.Events, ToProtoLoaderEvent(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *LoaderService) resolveDefaultAgent(ctx context.Context, agentID, provider string) (string, error) {
	if s.ResolveLoaderDefaultAgent == nil {
		return provider, nil
	}
	return s.ResolveLoaderDefaultAgent(ctx, agentID, provider)
}

func ParseLoaderRunTimeout(raw string) (time.Duration, error) {
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

func ProtoEnvItemsToLoaderModel(items []*agentcomposev1.SessionEnvVar) []loaderdomain.EnvVar {
	if len(items) == 0 {
		return nil
	}
	result := make([]loaderdomain.EnvVar, 0, len(items))
	for _, item := range items {
		result = append(result, loaderdomain.EnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	return loaderdomain.NormalizeEnvItems(result)
}

func ToProtoLoaderSummary(item loaderdomain.Summary) *agentcomposev1.LoaderSummary {
	return &agentcomposev1.LoaderSummary{
		LoaderId:          item.ID,
		Name:              item.Name,
		Description:       item.Description,
		Enabled:           item.Enabled,
		Runtime:           item.Runtime,
		WorkspaceId:       item.WorkspaceID,
		AgentId:           item.AgentID,
		Driver:            item.Driver,
		GuestImage:        item.GuestImage,
		DefaultAgent:      item.DefaultAgent,
		SessionPolicy:     item.SessionPolicy,
		ConcurrencyPolicy: item.ConcurrencyPolicy,
		CapsetIds:         item.CapsetIDs,
		CreatedAt:         item.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:         item.UpdatedAt.Format(time.RFC3339Nano),
		LastError:         item.LastError,
		TriggerCount:      uint32(item.TriggerCount),
		RunCount:          uint32(item.RunCount),
		EventCount:        uint32(item.EventCount),
		LatestRunAt:       FormatMaybeTime(item.LatestRunAt),
	}
}

func ToProtoLoaderDetail(item loaderdomain.Definition) *agentcomposev1.LoaderDetail {
	resp := &agentcomposev1.LoaderDetail{
		Summary:   ToProtoLoaderSummary(item.Summary),
		Script:    item.Script,
		CapsetIds: item.Summary.CapsetIDs,
	}
	for _, trigger := range item.Triggers {
		resp.Triggers = append(resp.Triggers, ToProtoLoaderTrigger(trigger))
	}
	for _, envItem := range item.EnvItems {
		value := envItem.Value
		if envItem.Secret && value != "" {
			value = "********"
		}
		resp.EnvItems = append(resp.EnvItems, &agentcomposev1.SessionEnvVar{Name: envItem.Name, Value: value, Secret: envItem.Secret})
	}
	return resp
}

func ToProtoLoaderTrigger(item loaderdomain.Trigger) *agentcomposev1.LoaderTrigger {
	return &agentcomposev1.LoaderTrigger{
		LoaderId:    item.LoaderID,
		TriggerId:   item.ID,
		Kind:        ToProtoLoaderTriggerKind(item.Kind),
		Topic:       item.Topic,
		IntervalMs:  item.IntervalMs,
		Enabled:     item.Enabled,
		AutoId:      item.AutoID,
		SpecJson:    item.SpecJSON,
		NextFireAt:  FormatMaybeTime(item.NextFireAt),
		LastFiredAt: FormatMaybeTime(item.LastFiredAt),
	}
}

func ToProtoLoaderRunSummary(item loaderdomain.RunSummary) *agentcomposev1.LoaderRunSummary {
	return &agentcomposev1.LoaderRunSummary{
		RunId:              item.ID,
		LoaderId:           item.LoaderID,
		TriggerId:          item.TriggerID,
		TriggerKind:        ToProtoLoaderTriggerKind(item.TriggerKind),
		TriggerSource:      item.TriggerSource,
		Status:             item.Status,
		StartedAt:          item.StartedAt.Format(time.RFC3339Nano),
		CompletedAt:        FormatMaybeTime(item.CompletedAt),
		DurationMs:         item.DurationMs,
		Error:              item.Error,
		ResultJson:         item.ResultJSON,
		PayloadJson:        item.PayloadJSON,
		SourceScriptSha256: item.SourceScriptHash,
		ArtifactsDir:       item.ArtifactsDir,
	}
}

func ToProtoLoaderRunDetail(item loaderdomain.RunSummary) *agentcomposev1.LoaderRunDetail {
	return &agentcomposev1.LoaderRunDetail{Summary: ToProtoLoaderRunSummary(item)}
}

func ToProtoLoaderEvent(item loaderdomain.Event) *agentcomposev1.LoaderEvent {
	return &agentcomposev1.LoaderEvent{
		Id:                   item.ID,
		LoaderId:             item.LoaderID,
		RunId:                item.RunID,
		TriggerId:            item.TriggerID,
		Type:                 item.Type,
		Level:                item.Level,
		Message:              item.Message,
		PayloadJson:          item.PayloadJSON,
		LinkedSessionId:      item.LinkedSessionID,
		LinkedCellId:         item.LinkedCellID,
		LinkedAgentSessionId: item.LinkedAgentSessionID,
		CreatedAt:            item.CreatedAt.Format(time.RFC3339Nano),
	}
}

func ToProtoLoaderTriggerKind(kind string) agentcomposev1.LoaderTriggerKind {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case loaderdomain.TriggerKindInterval:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_INTERVAL
	case loaderdomain.TriggerKindEvent:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_EVENT
	case loaderdomain.TriggerKindTimeout:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_TIMEOUT
	case loaderdomain.TriggerKindCron:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_CRON
	default:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_UNSPECIFIED
	}
}
