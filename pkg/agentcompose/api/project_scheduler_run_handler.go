package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ProjectSchedulerRunRuntime interface {
	RunScheduler(context.Context, loaders.SchedulerRunRequest) (domain.LoaderRunSummary, error)
	StartSchedulerRun(context.Context, loaders.SchedulerRunRequest) (domain.LoaderRunSummary, error)
	GetSchedulerRun(context.Context, string, string) (domain.LoaderRunSummary, error)
	StopSchedulerRun(context.Context, string, string, string) (domain.LoaderRunSummary, bool, error)
}

type ProjectSchedulerRunStore interface {
	GetLoaderRunForLoaders(context.Context, []string, string) (domain.LoaderRunSummary, error)
	ListLoaderRunsPage(context.Context, loaders.LoaderRunPageFilter) ([]domain.LoaderRunSummary, error)
}

type ProjectSchedulerRunSandboxStore interface {
	ListLoaderRunSandboxIDs(context.Context, []loaders.LoaderRunKey) (map[loaders.LoaderRunKey][]string, error)
}

func (h *ProjectHandler) InvokeScheduler(ctx context.Context, req *connect.Request[agentcomposev2.InvokeSchedulerRequest]) (*connect.Response[agentcomposev2.InvokeSchedulerResponse], error) {
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	var spec agentcomposev2.SchedulerSpec
	if err := json.Unmarshal([]byte(scheduler.SpecJSON), &spec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode scheduler spec: %w", err))
	}
	if strings.TrimSpace(spec.GetScript()) == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("scheduler %q does not define a script; use scheduler trigger to execute one of its triggers", scheduler.AgentName))
	}
	payloadJSON, err := normalizeSchedulerRunPayload(req.Msg.GetPayloadJson())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	if h.invocations == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler invocation controller is required"))
	}
	result, err := h.invocations.InvokeScheduler(ctx, scheduler.ManagedLoaderID, payloadJSON)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.InvokeSchedulerResponse{ResultJson: result.ResultJSON, DurationMs: result.DurationMs, Warnings: result.Warnings}), nil
}

func (h *ProjectHandler) RunScheduler(ctx context.Context, req *connect.Request[agentcomposev2.RunSchedulerRequest]) (*connect.Response[agentcomposev2.RunSchedulerResponse], error) {
	if strings.TrimSpace(req.Msg.GetTriggerId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scheduler trigger id is required"))
	}
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	payloadJSON, err := normalizeSchedulerRunPayload(req.Msg.GetPayloadJson())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	runtime, err := h.schedulerRunRuntime()
	if err != nil {
		return nil, err
	}
	run, err := runtime.RunScheduler(ctx, loaders.SchedulerRunRequest{
		LoaderID:    scheduler.ManagedLoaderID,
		TriggerID:   req.Msg.GetTriggerId(),
		PayloadJSON: payloadJSON,
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.RunSchedulerResponse{Run: schedulerRunToProto(run, scheduler)}), nil
}

func (h *ProjectHandler) StartSchedulerRun(ctx context.Context, req *connect.Request[agentcomposev2.StartSchedulerRunRequest]) (*connect.Response[agentcomposev2.StartSchedulerRunResponse], error) {
	if strings.TrimSpace(req.Msg.GetTriggerId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scheduler trigger id is required"))
	}
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	payloadJSON, err := normalizeSchedulerRunPayload(req.Msg.GetPayloadJson())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	runtime, err := h.schedulerRunRuntime()
	if err != nil {
		return nil, err
	}
	run, err := runtime.StartSchedulerRun(ctx, loaders.SchedulerRunRequest{
		LoaderID:    scheduler.ManagedLoaderID,
		TriggerID:   req.Msg.GetTriggerId(),
		PayloadJSON: payloadJSON,
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.StartSchedulerRunResponse{Run: schedulerRunToProto(run, scheduler)}), nil
}

func (h *ProjectHandler) GetSchedulerRun(ctx context.Context, req *connect.Request[agentcomposev2.GetSchedulerRunRequest]) (*connect.Response[agentcomposev2.GetSchedulerRunResponse], error) {
	_, scheduler, run, err := h.resolveProjectSchedulerRun(ctx, req.Msg.GetProject(), req.Msg.GetRunId())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.GetSchedulerRunResponse{Run: schedulerRunToProto(run, scheduler)}), nil
}

func (h *ProjectHandler) ListSchedulerRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListSchedulerRunsRequest]) (*connect.Response[agentcomposev2.ListSchedulerRunsResponse], error) {
	project, schedulers, err := h.resolveProjectSchedulerRunTargets(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	limit, err := schedulerRunPageLimit(req.Msg.GetLimit())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	status, err := schedulerRunStatusFilter(req.Msg.GetStatus())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	triggerID := strings.TrimSpace(req.Msg.GetTriggerId())
	cursor, err := decodeSchedulerRunCursor(req.Msg.GetCursor(), project.ID, project.CurrentRevision, req.Msg.GetAgentName(), triggerID, status)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	loaderIDs := make([]string, 0, len(schedulers))
	byLoaderID := make(map[string]domain.ProjectSchedulerRecord, len(schedulers))
	for _, scheduler := range schedulers {
		loaderID := strings.TrimSpace(scheduler.ManagedLoaderID)
		if loaderID == "" {
			continue
		}
		loaderIDs = append(loaderIDs, loaderID)
		byLoaderID[loaderID] = scheduler
	}
	store, err := h.schedulerRunStore()
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	runs, err := store.ListLoaderRunsPage(ctx, loaders.LoaderRunPageFilter{
		LoaderIDs:       loaderIDs,
		RequireTrigger:  true,
		TriggerID:       triggerID,
		Status:          status,
		BeforeStartedAt: cursor.StartedAt,
		BeforeLoaderID:  cursor.LoaderID,
		BeforeRunID:     cursor.RunID,
		Limit:           limit + 1,
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	end := min(limit, len(runs))
	response := &agentcomposev2.ListSchedulerRunsResponse{Runs: make([]*agentcomposev2.SchedulerRun, 0, end)}
	keys := make([]loaders.LoaderRunKey, 0, end)
	for _, run := range runs[:end] {
		keys = append(keys, loaders.LoaderRunKey{LoaderID: run.LoaderID, RunID: run.ID})
	}
	sandboxIDs := make(map[loaders.LoaderRunKey][]string)
	if sandboxStore, ok := h.store.(ProjectSchedulerRunSandboxStore); ok {
		sandboxIDs, err = sandboxStore.ListLoaderRunSandboxIDs(ctx, keys)
		if err != nil {
			return nil, ConnectErrorForDomain(err)
		}
	}
	for _, run := range runs[:end] {
		scheduler, ok := byLoaderID[run.LoaderID]
		if !ok {
			continue
		}
		item := schedulerRunToProto(run, scheduler)
		item.SandboxIds = sandboxIDs[loaders.LoaderRunKey{LoaderID: run.LoaderID, RunID: run.ID}]
		response.Runs = append(response.Runs, item)
	}
	if len(runs) > limit {
		response.NextCursor = encodeSchedulerRunCursor(project.ID, project.CurrentRevision, req.Msg.GetAgentName(), triggerID, status, runs[limit-1])
	}
	return connect.NewResponse(response), nil
}

func (h *ProjectHandler) StopSchedulerRun(ctx context.Context, req *connect.Request[agentcomposev2.StopSchedulerRunRequest]) (*connect.Response[agentcomposev2.StopSchedulerRunResponse], error) {
	_, scheduler, resolved, err := h.resolveProjectSchedulerRun(ctx, req.Msg.GetProject(), req.Msg.GetRunId())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	runtime, err := h.schedulerRunRuntime()
	if err != nil {
		return nil, err
	}
	run, requested, err := runtime.StopSchedulerRun(ctx, scheduler.ManagedLoaderID, resolved.ID, req.Msg.GetReason())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.StopSchedulerRunResponse{Run: schedulerRunToProto(run, scheduler), StopRequested: requested}), nil
}

func schedulerRunStatusFilter(status agentcomposev2.SchedulerRunStatus) (string, error) {
	switch status {
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_UNSPECIFIED:
		return "", nil
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_RUNNING:
		return domain.LoaderRunStatusRunning, nil
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED:
		return domain.LoaderRunStatusSucceeded, nil
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_FAILED:
		return domain.LoaderRunStatusFailed, nil
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_CANCELED:
		return domain.LoaderRunStatusCanceled, nil
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SKIPPED:
		return domain.LoaderRunStatusSkipped, nil
	default:
		return "", domain.ClassifyError(domain.ErrInvalidArgument, "invalid scheduler run status", nil)
	}
}

func (h *ProjectHandler) resolveProjectSchedulerRun(ctx context.Context, ref *agentcomposev2.ProjectRef, rawRunID string) (domain.ProjectRecord, domain.ProjectSchedulerRecord, domain.LoaderRunSummary, error) {
	project, err := h.resolveProjectRef(ctx, ref)
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.LoaderRunSummary{}, err
	}
	runID := strings.TrimSpace(rawRunID)
	if runID == "" {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.LoaderRunSummary{}, domain.ClassifyError(domain.ErrRequired, "scheduler run id is required", nil)
	}
	schedulers, err := h.currentProjectSchedulers(ctx, project)
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.LoaderRunSummary{}, err
	}
	loaderIDs := make([]string, 0, len(schedulers))
	for _, scheduler := range schedulers {
		if strings.TrimSpace(scheduler.ManagedLoaderID) != "" {
			loaderIDs = append(loaderIDs, scheduler.ManagedLoaderID)
		}
	}
	store, err := h.schedulerRunStore()
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.LoaderRunSummary{}, err
	}
	run, err := store.GetLoaderRunForLoaders(ctx, loaderIDs, runID)
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.LoaderRunSummary{}, err
	}
	for _, scheduler := range schedulers {
		if scheduler.ManagedLoaderID == run.LoaderID {
			return project, scheduler, run, nil
		}
	}
	return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.LoaderRunSummary{}, domain.ResourceError(domain.ErrNotFound, "scheduler run", runID, fmt.Sprintf("scheduler run %s not found", runID), nil)
}

func (h *ProjectHandler) resolveProjectSchedulerRunTargets(ctx context.Context, ref *agentcomposev2.ProjectRef, rawAgentName string) (domain.ProjectRecord, []domain.ProjectSchedulerRecord, error) {
	agentName := strings.TrimSpace(rawAgentName)
	if agentName != "" {
		project, scheduler, err := h.resolveProjectScheduler(ctx, ref, agentName)
		if err != nil {
			return domain.ProjectRecord{}, nil, err
		}
		return project, []domain.ProjectSchedulerRecord{scheduler}, nil
	}
	project, err := h.resolveProjectRef(ctx, ref)
	if err != nil {
		return domain.ProjectRecord{}, nil, err
	}
	schedulers, err := h.currentProjectSchedulers(ctx, project)
	return project, schedulers, err
}

func (h *ProjectHandler) currentProjectSchedulers(ctx context.Context, project domain.ProjectRecord) ([]domain.ProjectSchedulerRecord, error) {
	schedulers, err := h.store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	current := make([]domain.ProjectSchedulerRecord, 0, len(schedulers))
	for _, scheduler := range schedulers {
		if project.CurrentRevision > 0 && scheduler.Revision != project.CurrentRevision {
			continue
		}
		current = append(current, scheduler)
	}
	return current, nil
}

func (h *ProjectHandler) schedulerRunRuntime() (ProjectSchedulerRunRuntime, error) {
	if h.schedulerRuns == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler run controller is required"))
	}
	return h.schedulerRuns, nil
}

func (h *ProjectHandler) schedulerRunStore() (ProjectSchedulerRunStore, error) {
	store, ok := h.store.(ProjectSchedulerRunStore)
	if !ok {
		return nil, fmt.Errorf("scheduler run store is required")
	}
	return store, nil
}

func normalizeSchedulerRunPayload(raw string) (string, error) {
	payloadJSON, err := domain.NormalizeJSONDocument(raw)
	if err != nil {
		return "", domain.ClassifyError(domain.ErrInvalidArgument, "payload_json must contain valid JSON", err)
	}
	return payloadJSON, nil
}

func schedulerRunPageLimit(raw uint32) (int, error) {
	if raw == 0 {
		return 100, nil
	}
	if raw > 500 {
		return 0, domain.ClassifyError(domain.ErrInvalidArgument, "limit must be between 1 and 500", nil)
	}
	return int(raw), nil
}
