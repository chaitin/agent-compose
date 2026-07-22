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

func (h *ProjectHandler) PruneSchedulerRuns(ctx context.Context, req *connect.Request[agentcomposev2.PruneSchedulerRunsRequest]) (*connect.Response[agentcomposev2.PruneSchedulerRunsResponse], error) {
	if h.schedulerPrune == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("scheduler run prune controller is unavailable"))
	}
	_, schedulers, err := h.resolveProjectSchedulerRunTargets(ctx, req.Msg.GetProject(), req.Msg.GetAgentName())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	statuses, err := schedulerRunPruneStatuses(req.Msg.GetStatus())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	olderThan, err := RuntimeCacheOlderThanFromProto(req.Msg.GetOlderThanSeconds())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	loaderIDs := make([]string, 0, len(schedulers))
	for _, scheduler := range schedulers {
		if loaderID := strings.TrimSpace(scheduler.ManagedLoaderID); loaderID != "" {
			loaderIDs = append(loaderIDs, loaderID)
		}
	}
	result, err := h.schedulerPrune.PruneSchedulerRuns(ctx, loaders.SchedulerRunPruneRequest{
		LoaderIDs: loaderIDs,
		TriggerID: strings.TrimSpace(req.Msg.GetTriggerId()),
		Statuses:  statuses,
		OlderThan: olderThan,
		Force:     req.Msg.GetForce(),
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(schedulerRunPruneResultToProto(result)), nil
}

func schedulerRunPruneStatuses(values []agentcomposev2.SchedulerRunStatus) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		switch value {
		case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED:
			result = append(result, domain.LoaderRunStatusSucceeded)
		case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_FAILED:
			result = append(result, domain.LoaderRunStatusFailed)
		case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_CANCELED:
			result = append(result, domain.LoaderRunStatusCanceled)
		case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SKIPPED:
			result = append(result, domain.LoaderRunStatusSkipped)
		default:
			return nil, fmt.Errorf("scheduler run prune only accepts terminal statuses")
		}
	}
	return result, nil
}

func schedulerRunPruneResultToProto(result loaders.SchedulerRunPruneResult) *agentcomposev2.PruneSchedulerRunsResponse {
	response := &agentcomposev2.PruneSchedulerRunsResponse{
		DryRun:      result.DryRun,
		Matched:     schedulerRunPruneStatsToProto(result.Matched),
		Removed:     schedulerRunPruneStatsToProto(result.Removed),
		SkippedRuns: result.SkippedRuns,
		Warnings:    append([]string(nil), result.Warnings...),
		Residues:    make([]*agentcomposev2.SchedulerRunPruneResidue, 0, len(result.Residues)),
	}
	for _, residue := range result.Residues {
		response.Residues = append(response.Residues, &agentcomposev2.SchedulerRunPruneResidue{
			LoaderId: residue.LoaderID,
			RunId:    residue.RunID,
			Path:     residue.Path,
			Error:    residue.Error,
		})
	}
	return response
}

func schedulerRunPruneStatsToProto(stats loaders.SchedulerRunPruneStats) *agentcomposev2.SchedulerRunPruneStats {
	return &agentcomposev2.SchedulerRunPruneStats{
		Runs:              stats.Runs,
		LoaderEvents:      stats.LoaderEvents,
		EventDeliveries:   stats.EventDeliveries,
		EventSandboxLinks: stats.EventSandboxLinks,
		ArtifactDirs:      stats.ArtifactDirs,
		ArtifactBytes:     stats.ArtifactBytes,
	}
}
