package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ProjectDelegate interface {
	ValidateProject(context.Context, *connect.Request[agentcomposev2.ValidateProjectRequest]) (*connect.Response[agentcomposev2.ValidateProjectResponse], error)
	ApplyProject(context.Context, *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error)
	// PatchProject and RemoveProject receive an already-validated domain
	// project reference: the API boundary (ProjectHandler, below) owns
	// interpreting the transport ProjectRef oneof, so delegates never parse
	// or import agentcomposev2.ProjectRef.
	PatchProject(context.Context, *connect.Request[agentcomposev2.PatchProjectRequest], projects.ProjectRef) (*connect.Response[agentcomposev2.ApplyProjectResponse], error)
	RemoveProject(context.Context, *connect.Request[agentcomposev2.RemoveProjectRequest], projects.ProjectRef) (*connect.Response[agentcomposev2.RemoveProjectResponse], error)
	WatchProject(context.Context, *connect.Request[agentcomposev2.WatchProjectRequest], *connect.ServerStream[agentcomposev2.WatchProjectResponse]) error
}

type ProjectStore interface {
	GetProject(context.Context, string) (domain.ProjectRecord, error)
	ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error)
	ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error)
	ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error)
	GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error)
}

type ProjectSchedulerStore interface {
	GetScheduler(context.Context, string) (domain.Scheduler, error)
	ListSchedulerEvents(context.Context, string, int) ([]domain.SchedulerEvent, error)
}

type ProjectSchedulerRuntime interface {
	SetSchedulerEnabled(context.Context, string, bool) (domain.Scheduler, error)
	SetSchedulerTriggerEnabled(context.Context, string, string, bool) (domain.Scheduler, error)
}

type ProjectSchedulerInvocationRuntime interface {
	InvokeScheduler(context.Context, string, string) (schedulers.InvocationResult, error)
}

type ProjectSchedulerPruneRuntime interface {
	PruneSchedulerRuns(context.Context, schedulers.SchedulerRunPruneRequest) (schedulers.SchedulerRunPruneResult, error)
}

type ProjectAgentRunStateStore interface {
	ListProjectAgentRunStates(context.Context, string) ([]domain.ProjectAgentRunState, error)
}

type ProjectSchedulerPageStore interface {
	ListProjectSchedulersPage(context.Context, string, int, int) ([]domain.ProjectSchedulerRecord, error)
	CountProjectSchedulers(context.Context, string) (int, error)
}

type ProjectHandler struct {
	agentcomposev2connect.UnimplementedProjectServiceHandler
	delegate         ProjectDelegate
	store            ProjectStore
	agentModels      ProjectAgentModelResolver
	schedulerRuntime ProjectSchedulerRuntime
	schedulerRuns    ProjectSchedulerRunRuntime
	invocations      ProjectSchedulerInvocationRuntime
	schedulerPrune   ProjectSchedulerPruneRuntime
	sandboxDirs      schedulers.SandboxDirResolver
}

func NewProjectHandler(delegate ProjectDelegate, store ProjectStore, schedulerRuntimes ...ProjectSchedulerRuntime) *ProjectHandler {
	var schedulerRuntime ProjectSchedulerRuntime
	if len(schedulerRuntimes) > 0 {
		schedulerRuntime = schedulerRuntimes[0]
	}
	return newProjectHandler(ProjectHandlerDeps{Delegate: delegate, Store: store, SchedulerRuntime: schedulerRuntime})
}

// WithSandboxDirs injects the sandbox directory resolver ResolveEventMessage
// needs to reconstruct command.completed scheduler_event messages from
// sandbox cell artifacts. NewProjectHandler has no dedicated parameter for
// it (schedulerRuntimes is already the trailing variadic slot), so any
// caller that needs it — beyond NewProjectHandlerWithAgentModels, which
// takes it directly — chains this instead of silently falling back to the
// "artifact not accessible" placeholder for every new command.completed row.
//
// Like the package's other With* injectors (WithLifecycleLocks,
// WithRemovalCoordinator, WithRunTargetResolver), this must be called once,
// immediately after construction, before the handler is registered with the
// RPC server. h.sandboxDirs is read without synchronization by every
// concurrent ListSchedulerEvents/ListProjectSchedulerEvents/
// StreamProjectSchedulerEvents request; calling this after the handler has
// started serving traffic is a data race.
func (h *ProjectHandler) WithSandboxDirs(sandboxDirs schedulers.SandboxDirResolver) *ProjectHandler {
	h.sandboxDirs = sandboxDirs
	return h
}

// ProjectHandlerDeps bundles NewProjectHandlerWithAgentModels' (and, via
// newProjectHandler, NewProjectHandler's) dependencies.
type ProjectHandlerDeps struct {
	Delegate         ProjectDelegate
	Store            ProjectStore
	SchedulerRuntime ProjectSchedulerRuntime
	SchedulerRuns    ProjectSchedulerRunRuntime
	AgentModels      ProjectAgentModelResolver
	SandboxDirs      schedulers.SandboxDirResolver
}

// NewProjectHandlerWithAgentModels constructs a project handler that enriches project responses with resolved agent models
// and, given sandboxDirs, reconstructs command.completed scheduler_event messages from sandbox cell artifacts.
func NewProjectHandlerWithAgentModels(deps ProjectHandlerDeps) *ProjectHandler {
	return newProjectHandler(deps)
}

func newProjectHandler(deps ProjectHandlerDeps) *ProjectHandler {
	if controller, ok := deps.SchedulerRuntime.(*schedulers.Controller); ok && controller == nil {
		deps.SchedulerRuntime = nil
	}
	if supervisor, ok := deps.SchedulerRuns.(*schedulers.SchedulerRunSupervisor); ok && supervisor == nil {
		deps.SchedulerRuns = nil
	}
	schedulerRuns := deps.SchedulerRuns
	if schedulerRuns == nil {
		// A *schedulers.Controller no longer implements ProjectSchedulerRunRuntime
		// directly (that capability lives on its SchedulerRunSupervisor), so callers
		// that only set SchedulerRuntime — or that set SchedulerRuns to a typed-nil
		// supervisor — still get the controller's real supervisor here instead of
		// silently losing scheduler-run RPCs. The ProjectSchedulerRunRuntime
		// assertion below remains for callers (tests, fakes) that implement the
		// run-runtime methods directly without going through *schedulers.Controller.
		if controller, ok := deps.SchedulerRuntime.(*schedulers.Controller); ok && controller != nil {
			// controller.SchedulerRuns() is itself nil for a *Controller built by
			// anything other than NewController (e.g. a zero-value struct literal
			// in a test), so only take the concrete pointer once it's confirmed
			// non-nil — assigning a nil *SchedulerRunSupervisor straight into the
			// schedulerRuns interface would reintroduce the exact typed-nil footgun
			// this fallback exists to close.
			if runs := controller.SchedulerRuns(); runs != nil {
				schedulerRuns = runs
			}
		} else {
			schedulerRuns, _ = deps.SchedulerRuntime.(ProjectSchedulerRunRuntime)
		}
	}
	invocations, _ := deps.SchedulerRuntime.(ProjectSchedulerInvocationRuntime)
	schedulerPrune, _ := deps.SchedulerRuntime.(ProjectSchedulerPruneRuntime)
	return &ProjectHandler{delegate: deps.Delegate, store: deps.Store, agentModels: deps.AgentModels, schedulerRuntime: deps.SchedulerRuntime, schedulerRuns: schedulerRuns, invocations: invocations, schedulerPrune: schedulerPrune, sandboxDirs: deps.SandboxDirs}
}

func (h *ProjectHandler) ValidateProject(ctx context.Context, req *connect.Request[agentcomposev2.ValidateProjectRequest]) (*connect.Response[agentcomposev2.ValidateProjectResponse], error) {
	return h.delegate.ValidateProject(ctx, req)
}

func (h *ProjectHandler) ApplyProject(ctx context.Context, req *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
	return h.delegate.ApplyProject(ctx, req)
}

func (h *ProjectHandler) PatchProject(ctx context.Context, req *connect.Request[agentcomposev2.PatchProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
	projectRef, err := projectReferenceFromProto(req.Msg.GetProject())
	if err != nil {
		return nil, projectConnectError(err)
	}
	return h.delegate.PatchProject(ctx, req, projectRef)
}

func (h *ProjectHandler) RemoveProject(ctx context.Context, req *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
	projectRef, err := projectReferenceFromProto(req.Msg.GetProject())
	if err != nil {
		return nil, projectConnectError(err)
	}
	return h.delegate.RemoveProject(ctx, req, projectRef)
}

func (h *ProjectHandler) WatchProject(ctx context.Context, req *connect.Request[agentcomposev2.WatchProjectRequest], stream *connect.ServerStream[agentcomposev2.WatchProjectResponse]) error {
	return h.delegate.WatchProject(ctx, req, stream)
}

func (h *ProjectHandler) GetScheduler(ctx context.Context, req *connect.Request[agentcomposev2.GetSchedulerRequest]) (*connect.Response[agentcomposev2.GetSchedulerResponse], error) {
	scheduler, err := h.resolveGetScheduler(ctx, req.Msg)
	if err != nil {
		return nil, projectConnectError(err)
	}
	response, err := h.schedulerResponse(ctx, scheduler)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (h *ProjectHandler) ListSchedulers(ctx context.Context, req *connect.Request[agentcomposev2.ListSchedulersRequest]) (*connect.Response[agentcomposev2.ListSchedulersResponse], error) {
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(req.Msg.GetQuery())
	pageStore, ok := h.store.(ProjectSchedulerPageStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler page store is required"))
	}
	schedulers, err := pageStore.ListProjectSchedulersPage(ctx, query, offset, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	total, err := pageStore.CountProjectSchedulers(ctx, query)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	summaries := make([]*agentcomposev2.SchedulerSummary, 0, len(schedulers))
	for _, scheduler := range schedulers {
		displayName, description := projectSchedulerPresentation(scheduler.SpecJSON)
		summary := &agentcomposev2.SchedulerSummary{
			ProjectId:    scheduler.ProjectID,
			AgentName:    scheduler.AgentName,
			SchedulerId:  scheduler.SchedulerID,
			Enabled:      scheduler.Enabled,
			TriggerCount: uint32(scheduler.TriggerCount),
			DisplayName:  displayName,
			Description:  description,
		}
		summary.RunCount = uint32(scheduler.RunCount)
		summary.LatestRunAt = projectTimestamp(scheduler.LatestRunAt)
		summary.LastError = scheduler.LastError
		summaries = append(summaries, summary)
	}
	return connect.NewResponse(&agentcomposev2.ListSchedulersResponse{Schedulers: summaries, Total: uint32(total)}), nil
}

func (h *ProjectHandler) ListSchedulerEvents(ctx context.Context, req *connect.Request[agentcomposev2.ListSchedulerEventsRequest]) (*connect.Response[agentcomposev2.ListSchedulerEventsResponse], error) {
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, projectConnectError(err)
	}
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	store, ok := h.store.(ProjectSchedulerEventStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler runtime store is required"))
	}
	filter := schedulers.SchedulerEventPageFilter{SchedulerIDs: []string{scheduler.ID}, Offset: offset, Limit: limit}
	events, err := store.ListSchedulerEventsPage(ctx, filter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	total, err := store.CountSchedulerEventsPage(ctx, filter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	response := &agentcomposev2.ListSchedulerEventsResponse{Total: uint32(total)}
	for _, event := range events {
		response.Events = append(response.Events, schedulerEventToProto(event, scheduler))
	}
	resolveSchedulerEventMessages(ctx, events, response.Events, h.sandboxDirs)
	return connect.NewResponse(response), nil
}

func (h *ProjectHandler) SetSchedulerEnabled(ctx context.Context, req *connect.Request[agentcomposev2.SetSchedulerEnabledRequest]) (*connect.Response[agentcomposev2.SetSchedulerEnabledResponse], error) {
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, projectConnectError(err)
	}
	if h.schedulerRuntime == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler runtime controller is required"))
	}
	definition, err := h.schedulerRuntime.SetSchedulerEnabled(ctx, scheduler.ID, req.Msg.GetEnabled())
	if err != nil {
		return nil, projectConnectError(err)
	}
	scheduler.Enabled = definition.Summary.Enabled
	return connect.NewResponse(&agentcomposev2.SetSchedulerEnabledResponse{Scheduler: ProjectSchedulersToProto([]domain.ProjectSchedulerRecord{scheduler})[0], Overridden: true}), nil
}

func (h *ProjectHandler) SetSchedulerTriggerEnabled(ctx context.Context, req *connect.Request[agentcomposev2.SetSchedulerTriggerEnabledRequest]) (*connect.Response[agentcomposev2.SetSchedulerTriggerEnabledResponse], error) {
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, projectConnectError(err)
	}
	if h.schedulerRuntime == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler runtime controller is required"))
	}
	triggerID := strings.TrimSpace(req.Msg.GetTriggerId())
	if triggerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trigger id is required"))
	}
	definition, err := h.schedulerRuntime.SetSchedulerTriggerEnabled(ctx, scheduler.ID, triggerID, req.Msg.GetEnabled())
	if err != nil {
		return nil, projectConnectError(err)
	}
	for _, trigger := range definition.Triggers {
		if trigger.ID == triggerID {
			resolved := resolvedTriggerToProto(trigger, declaredTriggerSpec(scheduler, trigger.ID))
			resolved.Overridden = true
			return connect.NewResponse(&agentcomposev2.SetSchedulerTriggerEnabledResponse{Trigger: resolved}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("trigger %s not found", triggerID))
}

func resolvedTriggerToProto(trigger domain.SchedulerTrigger, declared *agentcomposev2.TriggerSpec) *agentcomposev2.ResolvedTrigger {
	spec := runtimeTriggerSpec(trigger)
	if declared != nil {
		spec = proto.Clone(declared).(*agentcomposev2.TriggerSpec)
	}
	return &agentcomposev2.ResolvedTrigger{Spec: spec, TriggerId: trigger.ID, Enabled: trigger.Enabled, NextFireAt: projectTimestamp(trigger.NextFireAt), LastFiredAt: projectTimestamp(trigger.LastFiredAt)}
}

func runtimeTriggerSpec(trigger domain.SchedulerTrigger) *agentcomposev2.TriggerSpec {
	spec := &agentcomposev2.TriggerSpec{Name: trigger.ID, Kind: triggerKindToProto(trigger.Kind)}
	duration := time.Duration(trigger.IntervalMs * int64(time.Millisecond)).String()
	switch trigger.Kind {
	case domain.SchedulerTriggerKindCron:
		var value struct {
			Expr string `json:"expr"`
		}
		if json.Unmarshal([]byte(trigger.SpecJSON), &value) == nil {
			spec.Cron = value.Expr
		}
	case domain.SchedulerTriggerKindInterval:
		spec.Interval = duration
	case domain.SchedulerTriggerKindTimeout:
		spec.Timeout = duration
	case domain.SchedulerTriggerKindEvent:
		spec.Event = &agentcomposev2.EventTriggerSpec{Topic: trigger.Topic}
	}
	return spec
}

func declaredTriggerSpec(scheduler domain.ProjectSchedulerRecord, triggerID string) *agentcomposev2.TriggerSpec {
	spec, err := decodeProjectSchedulerSpec(scheduler.SpecJSON)
	if err != nil {
		return nil
	}
	for index, trigger := range spec.GetTriggers() {
		id, err := projects.StableSchedulerTriggerID(scheduler.ProjectID, scheduler.AgentName, "", trigger.GetName(), index)
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
	spec, err := decodeProjectSchedulerSpec(scheduler.SpecJSON)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode scheduler spec: %w", err))
	}
	response := &agentcomposev2.GetSchedulerResponse{Scheduler: ProjectSchedulersToProto([]domain.ProjectSchedulerRecord{scheduler})[0], Spec: spec}
	schedulerStore, ok := h.store.(ProjectSchedulerStore)
	if !ok || scheduler.ID == "" {
		return response, nil
	}
	definition, err := schedulerStore.GetScheduler(ctx, scheduler.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	response.Scheduler.Enabled = definition.Summary.Enabled
	response.Overridden = definition.Summary.Enabled != response.Spec.Enabled
	for _, trigger := range definition.Triggers {
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
		spec = RedactProjectSpecSecrets(spec)
	}
	projectProto := ProjectToProto(project, spec, agents, schedulers)
	if err := h.enrichProjectAgentModels(ctx, project, agents, projectProto); err != nil {
		return nil, err
	}
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
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	result, err := h.store.ListProjects(ctx, domain.ProjectListOptions{
		Query:          req.Msg.GetQuery(),
		IncludeRemoved: req.Msg.GetIncludeRemoved(),
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev2.ListProjectsResponse{
		Total: uint32(result.TotalCount),
	}
	for _, project := range result.Projects {
		counts := result.CountsByProjectID[project.ID]
		resp.Projects = append(resp.Projects, projectSummaryWithCountsToProto(project, counts.AgentCount, counts.SchedulerCount))
	}
	return connect.NewResponse(resp), nil
}

func (h *ProjectHandler) resolveProjectRef(ctx context.Context, ref *agentcomposev2.ProjectRef) (domain.ProjectRecord, error) {
	return resolveProjectReference(ctx, h.store, ref)
}
