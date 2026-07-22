package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ProjectDelegate interface {
	ValidateProject(context.Context, *connect.Request[agentcomposev2.ValidateProjectRequest]) (*connect.Response[agentcomposev2.ValidateProjectResponse], error)
	ApplyProject(context.Context, *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error)
	RemoveProject(context.Context, *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error)
	WatchProject(context.Context, *connect.Request[agentcomposev2.WatchProjectRequest], *connect.ServerStream[agentcomposev2.WatchProjectResponse]) error
}

type ProjectStore interface {
	GetProject(context.Context, string) (domain.ProjectRecord, error)
	ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error)
	ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error)
	ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error)
	GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error)
}

type ProjectLoaderStore interface {
	GetLoader(context.Context, string) (domain.Loader, error)
	ListLoaderEvents(context.Context, string, int) ([]domain.LoaderEvent, error)
}

type ProjectLoaderRuntime interface {
	SetLoaderEnabled(context.Context, string, bool) (domain.Loader, error)
	SetLoaderTriggerEnabled(context.Context, string, string, bool) (domain.Loader, error)
}

type ProjectSchedulerInvocationRuntime interface {
	InvokeScheduler(context.Context, string, string) (loaders.InvocationResult, error)
}

type ProjectSchedulerPruneRuntime interface {
	PruneSchedulerRuns(context.Context, loaders.SchedulerRunPruneRequest) (loaders.SchedulerRunPruneResult, error)
}

type ProjectLoaderEventCursorStore interface {
	ListLoaderEventsBefore(context.Context, string, time.Time, string, int) ([]domain.LoaderEvent, error)
}
type ProjectAgentRunStateStore interface {
	ListProjectAgentRunStates(context.Context, string) ([]domain.ProjectAgentRunState, error)
}

type ProjectSchedulerPageStore interface {
	ListProjectSchedulersPage(context.Context, string, string, int) ([]domain.ProjectSchedulerRecord, error)
}

type ProjectHandler struct {
	agentcomposev2connect.UnimplementedProjectServiceHandler
	delegate       ProjectDelegate
	store          ProjectStore
	loaderRuntime  ProjectLoaderRuntime
	schedulerRuns  ProjectSchedulerRunRuntime
	invocations    ProjectSchedulerInvocationRuntime
	schedulerPrune ProjectSchedulerPruneRuntime
}

func NewProjectHandler(delegate ProjectDelegate, store ProjectStore, loaderRuntime ProjectLoaderRuntime) *ProjectHandler {
	schedulerRuns, _ := loaderRuntime.(ProjectSchedulerRunRuntime)
	invocations, _ := loaderRuntime.(ProjectSchedulerInvocationRuntime)
	schedulerPrune, _ := loaderRuntime.(ProjectSchedulerPruneRuntime)
	return &ProjectHandler{delegate: delegate, store: store, loaderRuntime: loaderRuntime, schedulerRuns: schedulerRuns, invocations: invocations, schedulerPrune: schedulerPrune}
}

func (h *ProjectHandler) ValidateProject(ctx context.Context, req *connect.Request[agentcomposev2.ValidateProjectRequest]) (*connect.Response[agentcomposev2.ValidateProjectResponse], error) {
	return h.delegate.ValidateProject(ctx, req)
}

func (h *ProjectHandler) ApplyProject(ctx context.Context, req *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
	return h.delegate.ApplyProject(ctx, req)
}

func (h *ProjectHandler) RemoveProject(ctx context.Context, req *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
	return h.delegate.RemoveProject(ctx, req)
}

func (h *ProjectHandler) WatchProject(ctx context.Context, req *connect.Request[agentcomposev2.WatchProjectRequest], stream *connect.ServerStream[agentcomposev2.WatchProjectResponse]) error {
	return h.delegate.WatchProject(ctx, req, stream)
}

func (h *ProjectHandler) GetScheduler(ctx context.Context, req *connect.Request[agentcomposev2.GetSchedulerRequest]) (*connect.Response[agentcomposev2.GetSchedulerResponse], error) {
	project, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, projectConnectError(err)
	}
	_ = project
	response, err := h.schedulerResponse(ctx, scheduler)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (h *ProjectHandler) ListSchedulers(ctx context.Context, req *connect.Request[agentcomposev2.ListSchedulersRequest]) (*connect.Response[agentcomposev2.ListSchedulersResponse], error) {
	limit := int(req.Msg.GetLimit())
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("limit must be between 1 and 500"))
	}
	query := strings.TrimSpace(req.Msg.GetQuery())
	cursor, err := decodeSchedulerCursor(req.Msg.GetCursor(), query)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	pageStore, ok := h.store.(ProjectSchedulerPageStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler page store is required"))
	}
	schedulers, err := pageStore.ListProjectSchedulersPage(ctx, query, cursor.LastKey, limit+1)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	summaries := make([]*agentcomposev2.SchedulerSummary, 0, min(len(schedulers), limit))
	for _, scheduler := range schedulers[:min(len(schedulers), limit)] {
		enabled, err := h.effectiveSchedulerEnabled(ctx, scheduler)
		if err != nil {
			return nil, err
		}
		displayName, description := projectSchedulerPresentation(scheduler.SpecJSON)
		summary := &agentcomposev2.SchedulerSummary{
			ProjectId:    scheduler.ProjectID,
			AgentName:    scheduler.AgentName,
			SchedulerId:  scheduler.SchedulerID,
			Enabled:      enabled,
			TriggerCount: uint32(scheduler.TriggerCount),
			DisplayName:  displayName,
			Description:  description,
		}
		summary.RunCount = uint32(scheduler.RunCount)
		summary.LatestRunAt = projectTimestamp(scheduler.LatestRunAt)
		summary.LastError = scheduler.LastError
		summaries = append(summaries, summary)
	}
	response := &agentcomposev2.ListSchedulersResponse{Schedulers: summaries}
	if len(schedulers) > limit {
		response.NextCursor = encodeSchedulerCursor(query, schedulerSummaryKey(summaries[len(summaries)-1]))
	}
	return connect.NewResponse(response), nil
}

func (h *ProjectHandler) effectiveSchedulerEnabled(ctx context.Context, scheduler domain.ProjectSchedulerRecord) (bool, error) {
	loaderStore, ok := h.store.(ProjectLoaderStore)
	if !ok || scheduler.ManagedLoaderID == "" {
		return scheduler.Enabled, nil
	}
	loader, err := loaderStore.GetLoader(ctx, scheduler.ManagedLoaderID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			return scheduler.Enabled, nil
		}
		return false, connect.NewError(connect.CodeInternal, err)
	}
	return loader.Summary.Enabled, nil
}

type schedulerPageCursor struct {
	Query   string `json:"query"`
	LastKey string `json:"last_key"`
}

func schedulerSummaryKey(item *agentcomposev2.SchedulerSummary) string {
	return item.GetProjectId() + "\x00" + item.GetAgentName() + "\x00" + item.GetSchedulerId()
}

func encodeSchedulerCursor(query, lastKey string) string {
	data, _ := json.Marshal(schedulerPageCursor{Query: query, LastKey: lastKey})
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeSchedulerCursor(token, query string) (schedulerPageCursor, error) {
	if strings.TrimSpace(token) == "" {
		return schedulerPageCursor{Query: query}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return schedulerPageCursor{}, fmt.Errorf("invalid cursor")
	}
	var cursor schedulerPageCursor
	if json.Unmarshal(data, &cursor) != nil || cursor.Query != query || cursor.LastKey == "" {
		return schedulerPageCursor{}, fmt.Errorf("invalid cursor")
	}
	return cursor, nil
}

type schedulerEventPageCursor struct {
	LoaderID  string    `json:"loader_id"`
	CreatedAt time.Time `json:"created_at"`
	EventID   string    `json:"event_id"`
}

func encodeSchedulerEventCursor(loaderID string, createdAt time.Time, eventID string) string {
	data, _ := json.Marshal(schedulerEventPageCursor{LoaderID: loaderID, CreatedAt: createdAt.UTC(), EventID: eventID})
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeSchedulerEventCursor(token, loaderID string) (schedulerEventPageCursor, error) {
	if strings.TrimSpace(token) == "" {
		return schedulerEventPageCursor{LoaderID: loaderID}, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return schedulerEventPageCursor{}, fmt.Errorf("invalid cursor")
	}
	var cursor schedulerEventPageCursor
	if json.Unmarshal(data, &cursor) != nil || cursor.LoaderID != loaderID || cursor.CreatedAt.IsZero() || cursor.EventID == "" {
		return schedulerEventPageCursor{}, fmt.Errorf("invalid cursor")
	}
	return cursor, nil
}

func (h *ProjectHandler) ListSchedulerEvents(ctx context.Context, req *connect.Request[agentcomposev2.ListSchedulerEventsRequest]) (*connect.Response[agentcomposev2.ListSchedulerEventsResponse], error) {
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, projectConnectError(err)
	}
	limit := int(req.Msg.GetLimit())
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("limit must be between 1 and 500"))
	}
	cursor, err := decodeSchedulerEventCursor(req.Msg.GetCursor(), scheduler.ManagedLoaderID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	store, ok := h.store.(ProjectLoaderEventCursorStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler runtime store is required"))
	}
	events, err := store.ListLoaderEventsBefore(ctx, scheduler.ManagedLoaderID, cursor.CreatedAt, cursor.EventID, limit+1)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	end := limit
	if end > len(events) {
		end = len(events)
	}
	response := &agentcomposev2.ListSchedulerEventsResponse{}
	for _, event := range events[:end] {
		response.Events = append(response.Events, schedulerEventToProto(event, scheduler))
	}
	if end < len(events) {
		last := events[end-1]
		response.NextCursor = encodeSchedulerEventCursor(scheduler.ManagedLoaderID, last.CreatedAt, last.ID)
	}
	return connect.NewResponse(response), nil
}

func (h *ProjectHandler) SetSchedulerEnabled(ctx context.Context, req *connect.Request[agentcomposev2.SetSchedulerEnabledRequest]) (*connect.Response[agentcomposev2.SetSchedulerEnabledResponse], error) {
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, projectConnectError(err)
	}
	if h.loaderRuntime == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler runtime controller is required"))
	}
	loader, err := h.loaderRuntime.SetLoaderEnabled(ctx, scheduler.ManagedLoaderID, req.Msg.GetEnabled())
	if err != nil {
		return nil, projectConnectError(err)
	}
	scheduler.Enabled = loader.Summary.Enabled
	return connect.NewResponse(&agentcomposev2.SetSchedulerEnabledResponse{Scheduler: ProjectSchedulersToProto([]domain.ProjectSchedulerRecord{scheduler})[0], Overridden: true}), nil
}

func (h *ProjectHandler) SetSchedulerTriggerEnabled(ctx context.Context, req *connect.Request[agentcomposev2.SetSchedulerTriggerEnabledRequest]) (*connect.Response[agentcomposev2.SetSchedulerTriggerEnabledResponse], error) {
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, projectConnectError(err)
	}
	if h.loaderRuntime == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler runtime controller is required"))
	}
	triggerID := strings.TrimSpace(req.Msg.GetTriggerId())
	if triggerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trigger id is required"))
	}
	loader, err := h.loaderRuntime.SetLoaderTriggerEnabled(ctx, scheduler.ManagedLoaderID, triggerID, req.Msg.GetEnabled())
	if err != nil {
		return nil, projectConnectError(err)
	}
	for _, trigger := range loader.Triggers {
		if trigger.ID == triggerID {
			resolved := resolvedTriggerToProto(trigger, declaredTriggerSpec(scheduler, trigger.ID))
			resolved.Overridden = true
			return connect.NewResponse(&agentcomposev2.SetSchedulerTriggerEnabledResponse{Trigger: resolved}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger %s not found", triggerID))
}

func resolvedTriggerToProto(trigger domain.LoaderTrigger, declared *agentcomposev2.TriggerSpec) *agentcomposev2.ResolvedTrigger {
	spec := runtimeTriggerSpec(trigger)
	if declared != nil {
		spec = proto.Clone(declared).(*agentcomposev2.TriggerSpec)
	}
	return &agentcomposev2.ResolvedTrigger{Spec: spec, TriggerId: trigger.ID, Enabled: trigger.Enabled, NextFireAt: projectTimestamp(trigger.NextFireAt), LastFiredAt: projectTimestamp(trigger.LastFiredAt)}
}

func runtimeTriggerSpec(trigger domain.LoaderTrigger) *agentcomposev2.TriggerSpec {
	spec := &agentcomposev2.TriggerSpec{Name: trigger.ID, Kind: trigger.Kind}
	duration := time.Duration(trigger.IntervalMs * int64(time.Millisecond)).String()
	switch trigger.Kind {
	case domain.LoaderTriggerKindCron:
		var value struct {
			Expr string `json:"expr"`
		}
		if json.Unmarshal([]byte(trigger.SpecJSON), &value) == nil {
			spec.Cron = value.Expr
		}
	case domain.LoaderTriggerKindInterval:
		spec.Interval = duration
	case domain.LoaderTriggerKindTimeout:
		spec.Timeout = duration
	case domain.LoaderTriggerKindEvent:
		spec.Event = &agentcomposev2.EventTriggerSpec{Topic: trigger.Topic}
	}
	return spec
}

func declaredTriggerSpec(scheduler domain.ProjectSchedulerRecord, triggerID string) *agentcomposev2.TriggerSpec {
	var spec agentcomposev2.SchedulerSpec
	if json.Unmarshal([]byte(scheduler.SpecJSON), &spec) != nil {
		return nil
	}
	for index, trigger := range spec.GetTriggers() {
		id, err := domain.StableManagedTriggerID(scheduler.ProjectID, scheduler.AgentName, "", trigger.GetName(), index)
		if err == nil && id == triggerID {
			return trigger
		}
	}
	return nil
}

func (h *ProjectHandler) resolveProjectScheduler(ctx context.Context, ref *agentcomposev2.ProjectRef, rawAgentName string) (domain.ProjectRecord, domain.ProjectSchedulerRecord, error) {
	project, err := h.resolveProjectRef(ctx, ref)
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, err
	}
	agentName := strings.TrimSpace(rawAgentName)
	if agentName == "" {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.ClassifyError(domain.ErrRequired, "agent name is required", nil)
	}
	schedulers, err := h.store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, err
	}
	for _, scheduler := range schedulers {
		if scheduler.AgentName == agentName {
			return project, scheduler, nil
		}
	}
	return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.ResourceError(domain.ErrNotFound, "scheduler", agentName, fmt.Sprintf("scheduler for agent %s not found", agentName), sql.ErrNoRows)
}

func (h *ProjectHandler) schedulerResponse(ctx context.Context, scheduler domain.ProjectSchedulerRecord) (*agentcomposev2.GetSchedulerResponse, error) {
	response := &agentcomposev2.GetSchedulerResponse{Scheduler: ProjectSchedulersToProto([]domain.ProjectSchedulerRecord{scheduler})[0], Spec: &agentcomposev2.SchedulerSpec{}}
	if err := json.Unmarshal([]byte(scheduler.SpecJSON), response.Spec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode scheduler spec: %w", err))
	}
	loaderStore, ok := h.store.(ProjectLoaderStore)
	if !ok || scheduler.ManagedLoaderID == "" {
		return response, nil
	}
	loader, err := loaderStore.GetLoader(ctx, scheduler.ManagedLoaderID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	response.Scheduler.Enabled = loader.Summary.Enabled
	response.Overridden = loader.Summary.Enabled != response.Spec.Enabled
	for _, trigger := range loader.Triggers {
		response.Triggers = append(response.Triggers, resolvedTriggerToProto(trigger, declaredTriggerSpec(scheduler, trigger.ID)))
	}
	return response, nil
}

func projectTimestamp(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}

func projectConnectError(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, domain.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrAmbiguous) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (h *ProjectHandler) GetProject(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
	if h.store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	project, err := h.resolveProjectRef(ctx, req.Msg.GetProject())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		if errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrAmbiguous) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	agents, err := h.store.ListProjectAgents(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	schedulers, err := h.store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var spec *agentcomposev2.ProjectSpec
	if req.Msg.GetIncludeSpec() && project.CurrentRevision > 0 {
		revision, err := h.store.GetProjectRevision(ctx, project.ID, project.CurrentRevision)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		spec, err = runs.DecodeRevisionSpec(revision.SpecJSON)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode project %s revision %d: %w", project.Name, project.CurrentRevision, err))
		}
	}
	projectProto := ProjectToProto(project, spec, agents, schedulers)
	if err := h.enrichProjectAgentRuns(ctx, projectProto); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: projectProto}), nil
}

func (h *ProjectHandler) enrichProjectAgentRuns(ctx context.Context, project *agentcomposev2.Project) error {
	store, ok := h.store.(ProjectAgentRunStateStore)
	if !ok {
		return nil
	}
	states, err := store.ListProjectAgentRunStates(ctx, project.GetSummary().GetProjectId())
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	statesByAgent := make(map[string]domain.ProjectAgentRunState, len(states))
	for _, state := range states {
		statesByAgent[state.AgentName] = state
	}
	for _, agent := range project.GetAgents() {
		state, ok := statesByAgent[agent.GetAgentName()]
		if !ok {
			continue
		}
		current := &agentcomposev2.ProjectAgentCurrentRun{RunningRunCount: state.RunningRunCount, RunningSchedulerRunCount: state.RunningSchedulerRunCount}
		if current.RunningRunCount+current.RunningSchedulerRunCount > 0 {
			current.Text = fmt.Sprintf("%d running", current.RunningRunCount+current.RunningSchedulerRunCount)
			agent.CurrentRun = current
		}
		agent.LatestRun = &agentcomposev2.ProjectAgentLatestRun{RunId: state.LatestRunID, Status: ProjectRunStatusToProto(state.LatestStatus), Source: ProjectRunSourceToProto(state.LatestSource), At: projectTimestamp(state.LatestAt)}
		if state.LatestStatus == domain.ProjectRunStatusFailed {
			agent.Health = agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_AT_RISK
		}
	}
	return nil
}

func (h *ProjectHandler) ListProjects(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error) {
	if h.store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	result, err := h.store.ListProjects(ctx, domain.ProjectListOptions{
		Query:          req.Msg.GetQuery(),
		IncludeRemoved: req.Msg.GetIncludeRemoved(),
		Offset:         int(req.Msg.GetOffset()),
		Limit:          int(req.Msg.GetLimit()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev2.ListProjectsResponse{
		TotalCount: uint32(result.TotalCount),
		HasMore:    result.HasMore,
		NextOffset: uint32(result.NextOffset),
	}
	for _, project := range result.Projects {
		agents, err := h.store.ListProjectAgents(ctx, project.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		schedulers, err := h.store.ListProjectSchedulers(ctx, project.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		resp.Projects = append(resp.Projects, ProjectSummaryToProto(project, agents, schedulers))
	}
	return connect.NewResponse(resp), nil
}

func (h *ProjectHandler) resolveProjectRef(ctx context.Context, ref *agentcomposev2.ProjectRef) (domain.ProjectRecord, error) {
	if ref == nil {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrRequired, "project ref is required", nil)
	}
	if projectID := strings.TrimSpace(ref.GetProjectId()); projectID != "" {
		return h.store.GetProject(ctx, projectID)
	}
	name := strings.TrimSpace(ref.GetName())
	sourcePath := strings.TrimSpace(ref.GetSourcePath())
	if name != "" && sourcePath != "" {
		projectID, err := domain.StableProjectID(name, sourcePath)
		if err != nil {
			return domain.ProjectRecord{}, err
		}
		return h.store.GetProject(ctx, projectID)
	}
	if name == "" {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrRequired, "project id or name is required", nil)
	}
	result, err := h.store.ListProjects(ctx, domain.ProjectListOptions{Query: name, Limit: 200})
	if err != nil {
		return domain.ProjectRecord{}, err
	}
	var matches []domain.ProjectRecord
	for _, project := range result.Projects {
		if project.Name == name {
			matches = append(matches, project)
		}
	}
	if len(matches) == 0 {
		return domain.ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", name, fmt.Sprintf("project %s not found", name), sql.ErrNoRows)
	}
	if len(matches) > 1 {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrAmbiguous, fmt.Sprintf("project name %s is ambiguous; use project_id or source_path", name), nil)
	}
	return matches[0], nil
}
