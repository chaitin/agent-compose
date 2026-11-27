package api

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type ProjectSchedulerEventStore interface {
	ListSchedulerEventsPage(context.Context, schedulers.SchedulerEventPageFilter) ([]domain.SchedulerEvent, error)
	CountSchedulerEventsPage(context.Context, schedulers.SchedulerEventPageFilter) (int, error)
}

func (h *ProjectHandler) ListProjectSchedulerEvents(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectSchedulerEventsRequest]) (*connect.Response[agentcomposev2.ListProjectSchedulerEventsResponse], error) {
	_, schedulerRecords, err := h.resolveProjectSchedulerRunTargets(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
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
		triggerID = run.TriggerID
		runID = run.ID
		schedulerRecords = []domain.ProjectSchedulerRecord{runScheduler}
	}
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
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
	store, ok := h.store.(ProjectSchedulerEventStore)
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("scheduler event store is required"))
	}
	sandboxID := strings.TrimSpace(req.Msg.GetSandboxId())
	events, err := store.ListSchedulerEventsPage(ctx, schedulers.SchedulerEventPageFilter{
		SchedulerIDs:    schedulerIDs,
		RequireTrigger:  true,
		TriggerID:       triggerID,
		RunID:           runID,
		LinkedSandboxID: sandboxID,
		Offset:          offset,
		Limit:           limit,
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	total, err := store.CountSchedulerEventsPage(ctx, schedulers.SchedulerEventPageFilter{
		SchedulerIDs: schedulerIDs, RequireTrigger: true, TriggerID: triggerID, RunID: runID, LinkedSandboxID: sandboxID,
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	response := &agentcomposev2.ListProjectSchedulerEventsResponse{Events: make([]*agentcomposev2.SchedulerEvent, 0, len(events)), Total: uint32(total)}
	matched := make([]domain.SchedulerEvent, 0, len(events))
	for _, event := range events {
		scheduler, ok := bySchedulerID[event.SchedulerID]
		if !ok {
			continue
		}
		matched = append(matched, event)
		response.Events = append(response.Events, schedulerEventToProto(event, scheduler))
	}
	resolveSchedulerEventMessages(ctx, matched, response.Events, h.sandboxDirs)
	return connect.NewResponse(response), nil
}

// maxConcurrentEventMessageResolves bounds how many ResolveEventMessage calls
// that actually read a sandbox cell artifact run in parallel for a single
// response page. New command.completed rows (empty DB message) are the only
// ones that pay this cost; a small bounded pool trades a handful of goroutines
// for lower
// wall-clock time without unbounded fan-out against the filesystem.
const maxConcurrentEventMessageResolves = 8

// resolveSchedulerEventMessages fills in item.Message for each item from its
// index-aligned source event. events and items must be the same length and
// in the same order. Events that ResolveEventMessage can answer without I/O
// (every non-command.completed event, and any command.completed event whose
// DB message is already non-empty — see EventMessageNeedsArtifactRead) are
// resolved inline; only events that need an artifact read are dispatched to
// the bounded worker pool, since that's the only case worth the goroutine
// overhead. Each dispatched goroutine only touches its own index, so no
// synchronization beyond the WaitGroup is needed.
func resolveSchedulerEventMessages(ctx context.Context, events []domain.SchedulerEvent, items []*agentcomposev2.SchedulerEvent, sandboxDirs schedulers.SandboxDirResolver) {
	sem := make(chan struct{}, maxConcurrentEventMessageResolves)
	var wg sync.WaitGroup
	for i := range items {
		if !schedulers.EventMessageNeedsArtifactRead(events[i]) {
			items[i].Message, _ = schedulers.ResolveEventMessage(ctx, events[i], sandboxDirs)
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			items[i].Message, _ = schedulers.ResolveEventMessage(ctx, events[i], sandboxDirs)
		}(i)
	}
	wg.Wait()
}

func schedulerEventToProto(event domain.SchedulerEvent, scheduler domain.ProjectSchedulerRecord) *agentcomposev2.SchedulerEvent {
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
