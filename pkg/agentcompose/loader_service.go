package agentcompose

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

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
	envItems, err := s.preserveUnchangedLoaderEnvSecrets(ctx, req.Msg.GetLoaderId(), protoEnvItemsToModel(req.Msg.GetEnvItems()))
	if err != nil {
		return nil, err
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
		EnvItems: envItems,
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
		LatestRunAt:       formatMaybeTime(item.LatestRunAt),
	}
}

func toProtoLoaderDetail(item Loader) *agentcomposev1.LoaderDetail {
	resp := &agentcomposev1.LoaderDetail{
		Summary:   toProtoLoaderSummary(item.Summary),
		Script:    item.Script,
		CapsetIds: item.Summary.CapsetIDs,
	}
	for _, trigger := range item.Triggers {
		resp.Triggers = append(resp.Triggers, toProtoLoaderTrigger(trigger))
	}
	for _, envItem := range item.EnvItems {
		value := envItem.Value
		if envItem.Secret && value != "" {
			value = maskedSecretValue
		}
		resp.EnvItems = append(resp.EnvItems, &agentcomposev1.SessionEnvVar{Name: envItem.Name, Value: value, Secret: envItem.Secret})
	}
	return resp
}

func (s *Service) preserveUnchangedLoaderEnvSecrets(ctx context.Context, loaderID string, items []SessionEnvVar) ([]SessionEnvVar, error) {
	existing, err := s.configDB.GetLoader(ctx, loaderID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	existingByName := make(map[string]SessionEnvVar, len(existing.EnvItems))
	for _, item := range existing.EnvItems {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		existingByName[name] = item
	}
	for index, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" || !item.Secret || item.Value != maskedSecretValue {
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

func toProtoLoaderTrigger(item LoaderTrigger) *agentcomposev1.LoaderTrigger {
	return &agentcomposev1.LoaderTrigger{
		LoaderId:    item.LoaderID,
		TriggerId:   item.ID,
		Kind:        toProtoLoaderTriggerKind(item.Kind),
		Topic:       item.Topic,
		IntervalMs:  item.IntervalMs,
		Enabled:     item.Enabled,
		AutoId:      item.AutoID,
		SpecJson:    item.SpecJSON,
		NextFireAt:  formatMaybeTime(item.NextFireAt),
		LastFiredAt: formatMaybeTime(item.LastFiredAt),
	}
}

func toProtoLoaderRunSummary(item LoaderRunSummary) *agentcomposev1.LoaderRunSummary {
	return &agentcomposev1.LoaderRunSummary{
		RunId:              item.ID,
		LoaderId:           item.LoaderID,
		TriggerId:          item.TriggerID,
		TriggerKind:        toProtoLoaderTriggerKind(item.TriggerKind),
		TriggerSource:      item.TriggerSource,
		Status:             item.Status,
		StartedAt:          item.StartedAt.Format(time.RFC3339Nano),
		CompletedAt:        formatMaybeTime(item.CompletedAt),
		DurationMs:         item.DurationMs,
		Error:              item.Error,
		ResultJson:         item.ResultJSON,
		PayloadJson:        item.PayloadJSON,
		SourceScriptSha256: item.SourceScriptHash,
		ArtifactsDir:       item.ArtifactsDir,
	}
}

func toProtoLoaderRunDetail(item LoaderRunSummary) *agentcomposev1.LoaderRunDetail {
	return &agentcomposev1.LoaderRunDetail{Summary: toProtoLoaderRunSummary(item)}
}

func toProtoLoaderEvent(item LoaderEvent) *agentcomposev1.LoaderEvent {
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

func toProtoLoaderTriggerKind(kind string) agentcomposev1.LoaderTriggerKind {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case LoaderTriggerKindInterval:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_INTERVAL
	case LoaderTriggerKindEvent:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_EVENT
	case LoaderTriggerKindTimeout:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_TIMEOUT
	case LoaderTriggerKindCron:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_CRON
	default:
		return agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_UNSPECIFIED
	}
}

func formatMaybeTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
