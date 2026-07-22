package api

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ProjectSchedulerEventStore interface {
	ListLoaderEventsPage(context.Context, loaders.LoaderEventPageFilter) ([]domain.LoaderEvent, error)
}

func (h *ProjectHandler) ListProjectSchedulerEvents(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectSchedulerEventsRequest]) (*connect.Response[agentcomposev2.ListProjectSchedulerEventsResponse], error) {
	project, schedulers, err := h.resolveProjectSchedulerRunTargets(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	agentName := strings.TrimSpace(req.Msg.GetAgentName())
	triggerID := strings.TrimSpace(req.Msg.GetTriggerId())
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID != "" {
		_, runScheduler, run, resolveErr := h.resolveProjectSchedulerRun(ctx, req.Msg.GetProject(), runID)
		if resolveErr != nil {
			return nil, ConnectErrorForDomain(resolveErr)
		}
		if agentName != "" && agentName != runScheduler.AgentName {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scheduler run does not belong to scheduler %q", agentName))
		}
		if triggerID != "" && triggerID != run.TriggerID {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scheduler run does not belong to trigger %q", triggerID))
		}
		agentName = runScheduler.AgentName
		triggerID = run.TriggerID
		runID = run.ID
		schedulers = []domain.ProjectSchedulerRecord{runScheduler}
	}
	limit, err := schedulerRunPageLimit(req.Msg.GetLimit())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	cursor, err := decodeProjectSchedulerEventCursor(req.Msg.GetCursor(), project.ID, project.CurrentRevision, agentName, triggerID, runID)
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
	store, ok := h.store.(ProjectSchedulerEventStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler event store is required"))
	}
	events, err := store.ListLoaderEventsPage(ctx, loaders.LoaderEventPageFilter{
		LoaderIDs:       loaderIDs,
		RequireTrigger:  true,
		TriggerID:       triggerID,
		RunID:           runID,
		BeforeCreatedAt: cursor.CreatedAt,
		BeforeLoaderID:  cursor.LoaderID,
		BeforeEventID:   cursor.EventID,
		Limit:           limit + 1,
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	end := min(limit, len(events))
	response := &agentcomposev2.ListProjectSchedulerEventsResponse{Events: make([]*agentcomposev2.SchedulerEvent, 0, end)}
	for _, event := range events[:end] {
		scheduler, ok := byLoaderID[event.LoaderID]
		if !ok {
			continue
		}
		response.Events = append(response.Events, schedulerEventToProto(event, scheduler))
	}
	if len(events) > limit {
		response.NextCursor = encodeProjectSchedulerEventCursor(project.ID, project.CurrentRevision, agentName, triggerID, runID, events[limit-1])
	}
	return connect.NewResponse(response), nil
}

func schedulerEventToProto(event domain.LoaderEvent, scheduler domain.ProjectSchedulerRecord) *agentcomposev2.SchedulerEvent {
	return &agentcomposev2.SchedulerEvent{
		Id:                  event.ID,
		Type:                event.Type,
		Level:               event.Level,
		Message:             event.Message,
		PayloadJson:         event.PayloadJSON,
		RunId:               event.RunID,
		TriggerId:           event.TriggerID,
		CreatedAt:           projectTimestamp(event.CreatedAt),
		AgentName:           scheduler.AgentName,
		SchedulerId:         scheduler.SchedulerID,
		LinkedSandboxId:     event.LinkedSandboxID,
		LinkedCellId:        event.LinkedCellID,
		LinkedAgentThreadId: event.LinkedAgentThreadID,
	}
}
