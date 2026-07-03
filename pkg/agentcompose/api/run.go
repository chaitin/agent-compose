package api

import (
	"time"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func ProjectRunDetailToProto(run domain.ProjectRunRecord) *agentcomposev2.RunDetail {
	return &agentcomposev2.RunDetail{
		Summary:      ProjectRunSummaryToProto(run),
		Prompt:       run.Prompt,
		Output:       run.Output,
		ResultJson:   run.ResultJSON,
		LogsPath:     run.LogsPath,
		ArtifactsDir: run.ArtifactsDir,
		CleanupError: run.CleanupError,
		Driver:       run.Driver,
		ImageRef:     run.ImageRef,
	}
}

func ProjectRunSummaryToProto(run domain.ProjectRunRecord) *agentcomposev2.RunSummary {
	return &agentcomposev2.RunSummary{
		RunId:           run.RunID,
		ProjectId:       run.ProjectID,
		ProjectName:     run.ProjectName,
		ProjectRevision: uint64(run.ProjectRevision),
		AgentId:         run.ManagedAgentID,
		AgentName:       run.AgentName,
		Source:          ProjectRunSourceToProto(run.Source),
		SchedulerId:     run.SchedulerID,
		TriggerId:       run.TriggerID,
		Status:          ProjectRunStatusToProto(run.Status),
		SessionId:       run.SessionID,
		ExitCode:        int32(run.ExitCode),
		Error:           run.Error,
		StartedAt:       FormatProjectTime(run.StartedAt),
		CompletedAt:     FormatProjectTime(run.CompletedAt),
		DurationMs:      run.DurationMs,
		CreatedAt:       FormatProjectTime(run.CreatedAt),
		UpdatedAt:       FormatProjectTime(run.UpdatedAt),
	}
}

func ProjectRunStatusToProto(status string) agentcomposev2.RunStatus {
	switch runs.NormalizeStatus(status) {
	case domain.ProjectRunStatusPending:
		return agentcomposev2.RunStatus_RUN_STATUS_PENDING
	case domain.ProjectRunStatusRunning:
		return agentcomposev2.RunStatus_RUN_STATUS_RUNNING
	case domain.ProjectRunStatusSucceeded:
		return agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED
	case domain.ProjectRunStatusFailed:
		return agentcomposev2.RunStatus_RUN_STATUS_FAILED
	case domain.ProjectRunStatusCanceled:
		return agentcomposev2.RunStatus_RUN_STATUS_CANCELED
	default:
		return agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED
	}
}

func ProjectRunStatusFromProto(status agentcomposev2.RunStatus) string {
	switch status {
	case agentcomposev2.RunStatus_RUN_STATUS_PENDING:
		return domain.ProjectRunStatusPending
	case agentcomposev2.RunStatus_RUN_STATUS_RUNNING:
		return domain.ProjectRunStatusRunning
	case agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED:
		return domain.ProjectRunStatusSucceeded
	case agentcomposev2.RunStatus_RUN_STATUS_FAILED:
		return domain.ProjectRunStatusFailed
	case agentcomposev2.RunStatus_RUN_STATUS_CANCELED:
		return domain.ProjectRunStatusCanceled
	default:
		return ""
	}
}

func ProjectRunSourceToProto(source string) agentcomposev2.RunSource {
	switch runs.NormalizeSource(source) {
	case domain.ProjectRunSourceScheduler:
		return agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER
	case domain.ProjectRunSourceAPI:
		return agentcomposev2.RunSource_RUN_SOURCE_API
	case domain.ProjectRunSourceManual:
		return agentcomposev2.RunSource_RUN_SOURCE_MANUAL
	default:
		return agentcomposev2.RunSource_RUN_SOURCE_UNSPECIFIED
	}
}

func ProjectRunSourceFromProto(source agentcomposev2.RunSource) string {
	switch source {
	case agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER:
		return domain.ProjectRunSourceScheduler
	case agentcomposev2.RunSource_RUN_SOURCE_API:
		return domain.ProjectRunSourceAPI
	case agentcomposev2.RunSource_RUN_SOURCE_MANUAL:
		return domain.ProjectRunSourceManual
	default:
		return domain.ProjectRunSourceManual
	}
}

func ProjectRunSourceFilterFromProto(source agentcomposev2.RunSource) string {
	switch source {
	case agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER:
		return domain.ProjectRunSourceScheduler
	case agentcomposev2.RunSource_RUN_SOURCE_API:
		return domain.ProjectRunSourceAPI
	case agentcomposev2.RunSource_RUN_SOURCE_MANUAL:
		return domain.ProjectRunSourceManual
	default:
		return ""
	}
}

func FormatProjectTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
