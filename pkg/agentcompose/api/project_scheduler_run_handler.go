package api

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type ProjectSchedulerRunRuntime interface {
	RunScheduler(context.Context, schedulers.SchedulerRunRequest) (domain.SchedulerRunSummary, error)
	StartSchedulerRun(context.Context, schedulers.SchedulerRunRequest) (domain.SchedulerRunSummary, error)
	GetSchedulerRun(context.Context, string, string) (domain.SchedulerRunSummary, error)
	StopSchedulerRun(context.Context, string, string, string) (domain.SchedulerRunSummary, bool, error)
}

type ProjectSchedulerRunStore interface {
	GetSchedulerRunForSchedulers(context.Context, []string, string) (domain.SchedulerRunSummary, error)
	ListSchedulerRunsPage(context.Context, schedulers.SchedulerRunPageFilter) ([]domain.SchedulerRunSummary, error)
	CountSchedulerRunsPage(context.Context, schedulers.SchedulerRunPageFilter) (int, error)
}

type ProjectSchedulerRunSandboxStore interface {
	ListSchedulerRunSandboxIDs(context.Context, []schedulers.SchedulerRunKey) (map[schedulers.SchedulerRunKey][]string, error)
}

type ProjectSchedulerRunSandboxLookupStore interface {
	BatchGetLatestSchedulerRunsBySandboxIDs(context.Context, []string, []string) (map[string]domain.SchedulerRunSummary, error)
}

const maxSchedulerRunBatchSandboxIDs = 500

func (h *ProjectHandler) InvokeScheduler(ctx context.Context, req *connect.Request[agentcomposev2.InvokeSchedulerRequest]) (*connect.Response[agentcomposev2.InvokeSchedulerResponse], error) {
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	spec, err := decodeProjectSchedulerSpec(scheduler.SpecJSON)
	if err != nil {
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
	result, err := h.invocations.InvokeScheduler(ctx, scheduler.ID, payloadJSON)
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
	run, err := runtime.RunScheduler(ctx, schedulers.SchedulerRunRequest{
		SchedulerID: scheduler.ID,
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
		return nil, WriteErrorNotAppliedForCode(connect.CodeInvalidArgument, ResourceScheduler, fmt.Errorf("scheduler trigger id is required"))
	}
	_, scheduler, err := h.resolveProjectScheduler(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, ConnectErrorForDomainWrite(apierror.NotApplied(err), ResourceScheduler)
	}
	payloadJSON, err := normalizeSchedulerRunPayload(req.Msg.GetPayloadJson())
	if err != nil {
		return nil, ConnectErrorForDomainWrite(apierror.NotApplied(err), ResourceScheduler)
	}
	runtime, err := h.schedulerRunRuntime()
	if err != nil {
		return nil, ConnectErrorForDomainWrite(err, ResourceScheduler)
	}
	run, err := runtime.StartSchedulerRun(ctx, schedulers.SchedulerRunRequest{
		SchedulerID: scheduler.ID,
		TriggerID:   req.Msg.GetTriggerId(),
		PayloadJSON: payloadJSON,
	})
	if err != nil {
		return nil, ConnectErrorForDomainWrite(err, ResourceScheduler)
	}
	return connect.NewResponse(&agentcomposev2.StartSchedulerRunResponse{Run: schedulerRunToProto(run, scheduler)}), nil
}

func (h *ProjectHandler) GetSchedulerRun(ctx context.Context, req *connect.Request[agentcomposev2.GetSchedulerRunRequest]) (*connect.Response[agentcomposev2.GetSchedulerRunResponse], error) {
	_, scheduler, run, err := h.resolveProjectSchedulerRun(ctx, req.Msg.GetProject(), req.Msg.GetRunId())
	if err != nil {
		return nil, ConnectErrorForDomainKind(err, ResourceSchedulerRun)
	}
	item := schedulerRunToProto(run, scheduler)
	if sandboxStore, ok := h.store.(ProjectSchedulerRunSandboxStore); ok {
		sandboxIDs, err := sandboxStore.ListSchedulerRunSandboxIDs(ctx, []schedulers.SchedulerRunKey{{SchedulerID: run.SchedulerID, RunID: run.ID}})
		if err != nil {
			return nil, ConnectErrorForDomainKind(err, ResourceSchedulerRun)
		}
		item.SandboxIds = sandboxIDs[schedulers.SchedulerRunKey{SchedulerID: run.SchedulerID, RunID: run.ID}]
	}
	return connect.NewResponse(&agentcomposev2.GetSchedulerRunResponse{Run: item}), nil
}

func (h *ProjectHandler) ListSchedulerRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListSchedulerRunsRequest]) (*connect.Response[agentcomposev2.ListSchedulerRunsResponse], error) {
	_, schedulerRecords, err := h.resolveProjectSchedulerRunTargets(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, ConnectErrorForDomainKind(err, ResourceSchedulerRun)
	}
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	status, err := schedulerRunStatusFilter(req.Msg.GetStatus())
	if err != nil {
		return nil, ConnectErrorForDomainKind(err, ResourceSchedulerRun)
	}
	triggerID := strings.TrimSpace(req.Msg.GetTriggerId())
	schedulerIDs := make([]string, 0, len(schedulerRecords))
	bySchedulerID := make(map[string]domain.ProjectSchedulerRecord, len(schedulerRecords))
	for _, scheduler := range schedulerRecords {
		schedulerID := strings.TrimSpace(scheduler.ID)
		if schedulerID == "" {
			continue
		}
		schedulerIDs = append(schedulerIDs, schedulerID)
		bySchedulerID[schedulerID] = scheduler
	}
	store, err := h.schedulerRunStore()
	if err != nil {
		return nil, ConnectErrorForDomainKind(err, ResourceSchedulerRun)
	}
	runs, err := store.ListSchedulerRunsPage(ctx, schedulers.SchedulerRunPageFilter{
		SchedulerIDs:   schedulerIDs,
		RequireTrigger: true,
		TriggerID:      triggerID,
		Status:         status,
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		return nil, ConnectErrorForDomainKind(err, ResourceSchedulerRun)
	}
	total, err := store.CountSchedulerRunsPage(ctx, schedulers.SchedulerRunPageFilter{SchedulerIDs: schedulerIDs, RequireTrigger: true, TriggerID: triggerID, Status: status})
	if err != nil {
		return nil, ConnectErrorForDomainKind(err, ResourceSchedulerRun)
	}
	response := &agentcomposev2.ListSchedulerRunsResponse{Runs: make([]*agentcomposev2.SchedulerRun, 0, len(runs)), Total: uint32(total)}
	keys := make([]schedulers.SchedulerRunKey, 0, len(runs))
	for _, run := range runs {
		keys = append(keys, schedulers.SchedulerRunKey{SchedulerID: run.SchedulerID, RunID: run.ID})
	}
	sandboxIDs := make(map[schedulers.SchedulerRunKey][]string)
	if sandboxStore, ok := h.store.(ProjectSchedulerRunSandboxStore); ok {
		sandboxIDs, err = sandboxStore.ListSchedulerRunSandboxIDs(ctx, keys)
		if err != nil {
			return nil, ConnectErrorForDomain(err)
		}
	}
	for _, run := range runs {
		scheduler, ok := bySchedulerID[run.SchedulerID]
		if !ok {
			continue
		}
		item := schedulerRunToProto(run, scheduler)
		item.SandboxIds = sandboxIDs[schedulers.SchedulerRunKey{SchedulerID: run.SchedulerID, RunID: run.ID}]
		response.Runs = append(response.Runs, item)
	}
	return connect.NewResponse(response), nil
}

func (h *ProjectHandler) BatchGetLatestSchedulerRuns(ctx context.Context, req *connect.Request[agentcomposev2.BatchGetLatestSchedulerRunsRequest]) (*connect.Response[agentcomposev2.BatchGetLatestSchedulerRunsResponse], error) {
	if len(req.Msg.GetSandboxIds()) > maxSchedulerRunBatchSandboxIDs {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most %d sandbox ids may be queried", maxSchedulerRunBatchSandboxIDs))
	}
	_, schedulerRecords, err := h.resolveProjectSchedulerRunTargets(ctx, req.Msg.GetProject(), "")
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	sandboxIDs := idset.Normalize(req.Msg.GetSandboxIds())
	response := &agentcomposev2.BatchGetLatestSchedulerRunsResponse{Results: make([]*agentcomposev2.SandboxSchedulerRun, 0, len(sandboxIDs))}
	if len(sandboxIDs) == 0 {
		return connect.NewResponse(response), nil
	}
	store, ok := h.store.(ProjectSchedulerRunSandboxLookupStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler run sandbox lookup store is required"))
	}
	schedulerIDs, schedulersBySchedulerID := schedulerRecordIndex(schedulerRecords)
	runsBySandboxID, err := store.BatchGetLatestSchedulerRunsBySandboxIDs(ctx, schedulerIDs, sandboxIDs)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	for _, sandboxID := range sandboxIDs {
		run, found := runsBySandboxID[sandboxID]
		if !found {
			response.Results = append(response.Results, &agentcomposev2.SandboxSchedulerRun{SandboxId: sandboxID})
			continue
		}
		scheduler, found := schedulersBySchedulerID[run.SchedulerID]
		if !found {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler run sandbox lookup references an unknown project scheduler"))
		}
		response.Results = append(response.Results, &agentcomposev2.SandboxSchedulerRun{SandboxId: sandboxID, Run: schedulerRunToProto(run, scheduler)})
	}
	return connect.NewResponse(response), nil
}

func (h *ProjectHandler) StopSchedulerRun(ctx context.Context, req *connect.Request[agentcomposev2.StopSchedulerRunRequest]) (*connect.Response[agentcomposev2.StopSchedulerRunResponse], error) {
	_, scheduler, resolved, err := h.resolveProjectSchedulerRun(ctx, req.Msg.GetProject(), req.Msg.GetRunId())
	if err != nil {
		return nil, ConnectErrorForDomainWrite(apierror.NotApplied(err), ResourceSchedulerRun)
	}
	runtime, err := h.schedulerRunRuntime()
	if err != nil {
		return nil, ConnectErrorForDomainWrite(err, ResourceSchedulerRun)
	}
	run, requested, err := runtime.StopSchedulerRun(ctx, scheduler.ID, resolved.ID, req.Msg.GetReason())
	if err != nil {
		return nil, ConnectErrorForDomainWrite(err, ResourceSchedulerRun)
	}
	return connect.NewResponse(&agentcomposev2.StopSchedulerRunResponse{Run: schedulerRunToProto(run, scheduler), StopRequested: requested}), nil
}

func schedulerRunStatusFilter(status agentcomposev2.SchedulerRunStatus) (string, error) {
	switch status {
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_UNSPECIFIED:
		return "", nil
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_RUNNING:
		return domain.SchedulerRunStatusRunning, nil
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED:
		return domain.SchedulerRunStatusSucceeded, nil
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_FAILED:
		return domain.SchedulerRunStatusFailed, nil
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_CANCELED:
		return domain.SchedulerRunStatusCanceled, nil
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SKIPPED:
		return domain.SchedulerRunStatusSkipped, nil
	default:
		return "", domain.ClassifyError(domain.ErrInvalidArgument, "invalid scheduler run status", nil)
	}
}

func (h *ProjectHandler) resolveProjectSchedulerRun(ctx context.Context, ref *agentcomposev2.ProjectRef, rawRunID string) (domain.ProjectRecord, domain.ProjectSchedulerRecord, domain.SchedulerRunSummary, error) {
	project, err := h.resolveProjectRef(ctx, ref)
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.SchedulerRunSummary{}, err
	}
	runID := strings.TrimSpace(rawRunID)
	if runID == "" {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.SchedulerRunSummary{}, domain.ClassifyError(domain.ErrRequired, "scheduler run id is required", nil)
	}
	schedulers, err := h.currentProjectSchedulers(ctx, project)
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.SchedulerRunSummary{}, err
	}
	schedulerIDs := make([]string, 0, len(schedulers))
	for _, scheduler := range schedulers {
		if strings.TrimSpace(scheduler.ID) != "" {
			schedulerIDs = append(schedulerIDs, scheduler.ID)
		}
	}
	store, err := h.schedulerRunStore()
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.SchedulerRunSummary{}, err
	}
	run, err := store.GetSchedulerRunForSchedulers(ctx, schedulerIDs, runID)
	if err != nil {
		return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.SchedulerRunSummary{}, err
	}
	for _, scheduler := range schedulers {
		if scheduler.ID == run.SchedulerID {
			return project, scheduler, run, nil
		}
	}
	return domain.ProjectRecord{}, domain.ProjectSchedulerRecord{}, domain.SchedulerRunSummary{}, domain.ResourceError(domain.ErrNotFound, "scheduler run", runID, fmt.Sprintf("scheduler run %s not found", runID), nil)
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
