package api

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestProjectHandlerPruneSchedulerRunsMapsRequestAndResult(t *testing.T) {
	store, runtime, handler := newSchedulerRunHandlerFixture()
	runtime.pruneResult = loaders.SchedulerRunPruneResult{
		DryRun: true,
		Matched: loaders.SchedulerRunPruneStats{
			Runs: 3, LoaderEvents: 4, EventDeliveries: 5, EventSandboxLinks: 6, ArtifactDirs: 2, ArtifactBytes: 17,
		},
		SkippedRuns: 1,
		Residues:    []loaders.SchedulerRunPruneResidue{{LoaderID: "loader-1", RunID: "run-1", Path: "/runs/run-1", Error: "failed"}},
		Warnings:    []string{"warning"},
	}
	response, err := handler.PruneSchedulerRuns(context.Background(), connect.NewRequest(&agentcomposev2.PruneSchedulerRunsRequest{
		Project:   &agentcomposev2.ProjectRef{ProjectId: store.project.ID},
		AgentName: store.scheduler.AgentName,
		TriggerId: " trigger-history ",
		Status: []agentcomposev2.SchedulerRunStatus{
			agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED,
			agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_FAILED,
		},
		OlderThanSeconds: 7200,
	}))
	if err != nil {
		t.Fatalf("PruneSchedulerRuns: %v", err)
	}
	if !slices.Equal(runtime.pruneRequest.LoaderIDs, []string{store.scheduler.ManagedLoaderID}) ||
		runtime.pruneRequest.TriggerID != "trigger-history" ||
		!slices.Equal(runtime.pruneRequest.Statuses, []string{domain.LoaderRunStatusSucceeded, domain.LoaderRunStatusFailed}) ||
		runtime.pruneRequest.OlderThan != 2*time.Hour || runtime.pruneRequest.Force {
		t.Fatalf("runtime request = %#v", runtime.pruneRequest)
	}
	if !response.Msg.GetDryRun() || response.Msg.GetMatched().GetRuns() != 3 || response.Msg.GetMatched().GetArtifactBytes() != 17 ||
		response.Msg.GetSkippedRuns() != 1 || len(response.Msg.GetResidues()) != 1 || response.Msg.GetResidues()[0].GetRunId() != "run-1" ||
		len(response.Msg.GetWarnings()) != 1 {
		t.Fatalf("response = %#v", response.Msg)
	}
}

func TestProjectHandlerPruneSchedulerRunsUsesCurrentProjectSchedulers(t *testing.T) {
	store, runtime, handler := newSchedulerRunHandlerFixture()
	currentSecond := store.scheduler
	currentSecond.AgentName = "agent-2"
	currentSecond.ManagedLoaderID = "loader-2"
	stale := store.scheduler
	stale.AgentName = "old-agent"
	stale.ManagedLoaderID = "loader-old"
	stale.Revision = store.project.CurrentRevision - 1
	store.schedulers = []domain.ProjectSchedulerRecord{store.scheduler, stale, currentSecond}

	_, err := handler.PruneSchedulerRuns(context.Background(), connect.NewRequest(&agentcomposev2.PruneSchedulerRunsRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID}, Force: true,
	}))
	if err != nil {
		t.Fatalf("PruneSchedulerRuns: %v", err)
	}
	if !slices.Equal(runtime.pruneRequest.LoaderIDs, []string{"loader-1", "loader-2"}) || !runtime.pruneRequest.Force {
		t.Fatalf("runtime request = %#v", runtime.pruneRequest)
	}
}

func TestProjectHandlerPruneSchedulerRunsRejectsInvalidStatusAndDuration(t *testing.T) {
	store, runtime, handler := newSchedulerRunHandlerFixture()
	for _, status := range []agentcomposev2.SchedulerRunStatus{
		agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_UNSPECIFIED,
		agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_RUNNING,
	} {
		_, err := handler.PruneSchedulerRuns(context.Background(), connect.NewRequest(&agentcomposev2.PruneSchedulerRunsRequest{
			Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID}, Status: []agentcomposev2.SchedulerRunStatus{status},
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("status %v code=%v err=%v", status, connect.CodeOf(err), err)
		}
	}
	_, err := handler.PruneSchedulerRuns(context.Background(), connect.NewRequest(&agentcomposev2.PruneSchedulerRunsRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID}, OlderThanSeconds: math.MaxUint64,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("overflow duration code=%v err=%v", connect.CodeOf(err), err)
	}
	if runtime.pruneRequest.LoaderIDs != nil {
		t.Fatalf("runtime called for invalid request: %#v", runtime.pruneRequest)
	}
}

func TestProjectHandlerPruneSchedulerRunsRuntimeUnavailable(t *testing.T) {
	store, _, _ := newSchedulerRunHandlerFixture()
	handler := NewProjectHandler(nil, store, &schedulerRuntimeFake{})
	_, err := handler.PruneSchedulerRuns(context.Background(), connect.NewRequest(&agentcomposev2.PruneSchedulerRunsRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID},
	}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("runtime unavailable code=%v err=%v", connect.CodeOf(err), err)
	}
}
