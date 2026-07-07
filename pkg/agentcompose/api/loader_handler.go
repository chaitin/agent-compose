package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type LoaderController interface {
	Validate(ctx context.Context, runtime, script string) (loaders.LoaderValidationResult, error)
	CreateSchedulerExecution(ctx context.Context, loader domain.Loader) (domain.Loader, error)
	UpdateSchedulerExecution(ctx context.Context, loader domain.Loader) (domain.Loader, error)
	DeleteSchedulerExecution(ctx context.Context, loaderID string) error
	SetSchedulerExecutionEnabled(ctx context.Context, loaderID string, enabled bool) (domain.Loader, error)
	SetSchedulerExecutionTriggerEnabled(ctx context.Context, loaderID, triggerID string, enabled bool) (domain.Loader, error)
	RunSchedulerExecutionNow(ctx context.Context, loaderID, triggerID, payloadJSON string, timeout time.Duration) (domain.LoaderRunSummary, error)
}

type LoaderStore interface {
	ListLoaderSummaries(ctx context.Context) ([]domain.LoaderSummary, error)
	GetLoader(ctx context.Context, loaderID string) (domain.Loader, error)
	ListLoaderRuns(ctx context.Context, loaderID string, limit int) ([]domain.LoaderRunSummary, error)
	GetLoaderRun(ctx context.Context, loaderID, runID string) (domain.LoaderRunSummary, error)
	ListLoaderEvents(ctx context.Context, loaderID string, limit int) ([]domain.LoaderEvent, error)
	GetAgentDefinition(ctx context.Context, id string) (domain.AgentDefinition, error)
}

type LoaderHandler struct {
	controller LoaderController
	store      LoaderStore
}

func NewLoaderHandler(controller LoaderController, store LoaderStore) *LoaderHandler {
	return &LoaderHandler{controller: controller, store: store}
}

func (s *LoaderHandler) ValidateLoader(ctx context.Context, req *connect.Request[agentcomposev1.ValidateLoaderRequest]) (*connect.Response[agentcomposev1.ValidateLoaderResponse], error) {
	result, err := s.controller.Validate(ctx, req.Msg.GetRuntime(), req.Msg.GetScript())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	resp := &agentcomposev1.ValidateLoaderResponse{Warnings: append([]string(nil), result.Warnings...)}
	for _, trigger := range result.Triggers {
		resp.Triggers = append(resp.Triggers, LoaderTriggerToProto(trigger))
	}
	return connect.NewResponse(resp), nil
}

func (s *LoaderHandler) ListLoaders(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListLoadersResponse], error) {
	_ = req
	items, err := s.store.ListLoaderSummaries(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListLoadersResponse{}
	for _, item := range items {
		resp.Loaders = append(resp.Loaders, LoaderSummaryToProto(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *LoaderHandler) GetLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	item, err := s.store.GetLoader(ctx, req.Msg.GetLoaderId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: LoaderDetailToProto(item)}), nil
}

func (s *LoaderHandler) CreateLoader(ctx context.Context, req *connect.Request[agentcomposev1.CreateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	defaultAgent, err := s.resolveDefaultAgent(ctx, req.Msg.GetAgentId(), req.Msg.GetDefaultAgent())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	item, err := s.controller.CreateSchedulerExecution(ctx, domain.Loader{
		Summary: domain.LoaderSummary{
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
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: LoaderDetailToProto(item)}), nil
}

func (s *LoaderHandler) UpdateLoader(ctx context.Context, req *connect.Request[agentcomposev1.UpdateLoaderRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	defaultAgent, err := s.resolveDefaultAgent(ctx, req.Msg.GetAgentId(), req.Msg.GetDefaultAgent())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	envItems := protoEnvItemsToModel(req.Msg.GetEnvItems())
	envItems, err = s.preserveUnchangedLoaderEnvSecrets(ctx, req.Msg.GetLoaderId(), envItems)
	if err != nil {
		return nil, err
	}
	item, err := s.controller.UpdateSchedulerExecution(ctx, domain.Loader{
		Summary: domain.LoaderSummary{
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
		EnvItems: envItems,
	})
	if err != nil {
		return nil, loaderServiceConnectError(err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: LoaderDetailToProto(item)}), nil
}

func (s *LoaderHandler) preserveUnchangedLoaderEnvSecrets(ctx context.Context, loaderID string, items []domain.SessionEnvVar) ([]domain.SessionEnvVar, error) {
	existing, err := s.store.GetLoader(ctx, loaderID)
	if err != nil {
		return nil, loaderServiceConnectError(err)
	}
	existingByName := make(map[string]domain.SessionEnvVar, len(existing.EnvItems))
	for _, item := range existing.EnvItems {
		name := strings.TrimSpace(item.Name)
		if name != "" {
			existingByName[name] = item
		}
	}
	for index, item := range items {
		name := strings.TrimSpace(item.Name)
		value := strings.TrimSpace(item.Value)
		if name == "" || !item.Secret || (value != "" && value != secretRedactedValue) {
			continue
		}
		existingItem, ok := existingByName[name]
		if !ok || !existingItem.Secret || existingItem.Value == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("secret env %s requires a value", name))
		}
		items[index].Value = existingItem.Value
	}
	return items, nil
}

func (s *LoaderHandler) DeleteLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := s.controller.DeleteSchedulerExecution(ctx, req.Msg.GetLoaderId()); err != nil {
		return nil, loaderServiceConnectError(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *LoaderHandler) SetLoaderEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	item, err := s.controller.SetSchedulerExecutionEnabled(ctx, req.Msg.GetLoaderId(), req.Msg.GetEnabled())
	if err != nil {
		return nil, loaderServiceConnectError(err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: LoaderDetailToProto(item)}), nil
}

func (s *LoaderHandler) SetLoaderTriggerEnabled(ctx context.Context, req *connect.Request[agentcomposev1.SetLoaderTriggerEnabledRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	item, err := s.controller.SetSchedulerExecutionTriggerEnabled(ctx, req.Msg.GetLoaderId(), req.Msg.GetTriggerId(), req.Msg.GetEnabled())
	if err != nil {
		return nil, loaderServiceConnectError(err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderResponse{Loader: LoaderDetailToProto(item)}), nil
}

func (s *LoaderHandler) RunLoaderNow(ctx context.Context, req *connect.Request[agentcomposev1.RunLoaderNowRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	timeout, err := loaders.ParseRunTimeout(req.Msg.GetTimeout())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	run, err := s.controller.RunSchedulerExecutionNow(ctx, req.Msg.GetLoaderId(), req.Msg.GetTriggerId(), req.Msg.GetPayloadJson(), timeout)
	if err != nil {
		return nil, loaderServiceConnectError(err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderRunResponse{Run: LoaderRunDetailToProto(run)}), nil
}

func (s *LoaderHandler) ListLoaderRuns(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderRunsRequest]) (*connect.Response[agentcomposev1.ListLoaderRunsResponse], error) {
	runs, err := s.store.ListLoaderRuns(ctx, req.Msg.GetLoaderId(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListLoaderRunsResponse{}
	for _, item := range runs {
		resp.Runs = append(resp.Runs, LoaderRunSummaryToProto(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *LoaderHandler) GetLoaderRun(ctx context.Context, req *connect.Request[agentcomposev1.LoaderRunIDRequest]) (*connect.Response[agentcomposev1.LoaderRunResponse], error) {
	run, err := s.store.GetLoaderRun(ctx, req.Msg.GetLoaderId(), req.Msg.GetRunId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&agentcomposev1.LoaderRunResponse{Run: LoaderRunDetailToProto(run)}), nil
}

func (s *LoaderHandler) ListLoaderEvents(ctx context.Context, req *connect.Request[agentcomposev1.ListLoaderEventsRequest]) (*connect.Response[agentcomposev1.ListLoaderEventsResponse], error) {
	events, err := s.store.ListLoaderEvents(ctx, req.Msg.GetLoaderId(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListLoaderEventsResponse{}
	for _, item := range events {
		resp.Events = append(resp.Events, LoaderEventToProto(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *LoaderHandler) resolveDefaultAgent(ctx context.Context, agentID, provider string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return provider, nil
	}
	agent, err := s.store.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return "", err
	}
	if !agent.Enabled {
		return "", fmt.Errorf("agent definition %s is disabled", agentID)
	}
	if strings.TrimSpace(provider) != "" && domain.NormalizeAgentKind(provider) == "" {
		return "", fmt.Errorf("loader default agent provider %q is not supported", provider)
	}
	return agent.Provider, nil
}

func loaderServiceConnectError(err error) error {
	if isLoaderInternalError(err) {
		return connect.NewError(connect.CodeInternal, err)
	}
	if errors.Is(err, domain.ErrNotFound) ||
		errors.Is(err, domain.ErrFailedPrecondition) ||
		errors.Is(err, domain.ErrConflict) ||
		errors.Is(err, domain.ErrReferenced) ||
		errors.Is(err, domain.ErrAlreadyExists) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, domain.ErrUnsupported) {
		return ConnectErrorForDomain(err)
	}
	return connect.NewError(connect.CodeInvalidArgument, err)
}

func isLoaderInternalError(err error) bool {
	return errors.Is(err, sql.ErrConnDone) ||
		errors.Is(err, os.ErrPermission) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, os.ErrNotExist)
}

func protoEnvItemsToModel(items []*agentcomposev1.SessionEnvVar) []domain.SessionEnvVar {
	if len(items) == 0 {
		return nil
	}
	result := make([]domain.SessionEnvVar, 0, len(items))
	for _, item := range items {
		result = append(result, domain.SessionEnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	return domain.NormalizeEnvItems(result)
}
