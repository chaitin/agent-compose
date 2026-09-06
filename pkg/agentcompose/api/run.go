package api

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
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
		Warnings:     append([]string(nil), run.Warnings...),
		ErrorStack:   run.ErrorStack,
		Labels:       run.Labels,
	}
}

func ProjectRunSummaryToProto(run domain.ProjectRunRecord) *agentcomposev2.RunSummary {
	sandboxID := run.SandboxID
	return &agentcomposev2.RunSummary{
		RunId:           run.RunID,
		ProjectId:       run.ProjectID,
		ProjectName:     run.ProjectName,
		ProjectRevision: uint64(run.ProjectRevision),
		AgentId:         run.AgentID,
		AgentName:       run.AgentName,
		Source:          ProjectRunSourceToProto(run.Source),
		SchedulerId:     run.SchedulerID,
		SchedulerRunId:  run.SchedulerRunID,
		TriggerId:       run.TriggerID,
		Status:          ProjectRunStatusToProto(run.Status),
		SandboxId:       sandboxID,
		RunShortId:      identity.ShortID(run.RunID),
		SandboxShortId:  identity.ShortID(sandboxID),
		ExitCode:        int32(run.ExitCode),
		Error:           run.Error,
		StartedAt:       FormatProjectTime(run.StartedAt),
		CompletedAt:     FormatProjectTime(run.CompletedAt),
		DurationMs:      run.DurationMs,
		CreatedAt:       FormatProjectTime(run.CreatedAt),
		UpdatedAt:       FormatProjectTime(run.UpdatedAt),
		Warnings:        append([]string(nil), run.Warnings...),
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

func StdioStreamToProto(stream domain.StdioStream) agentcomposev2.StdioStream {
	switch domain.NormalizeStdioStream(stream) {
	case domain.StdioStderr:
		return agentcomposev2.StdioStream_STDIO_STREAM_STDERR
	default:
		return agentcomposev2.StdioStream_STDIO_STREAM_STDOUT
	}
}

func TranscriptEventFromExecChunk(chunk domain.ExecChunk, createdAt time.Time) *agentcomposev2.TranscriptEvent {
	return &agentcomposev2.TranscriptEvent{
		Stream:    StdioStreamToProto(chunk.Stream),
		Text:      chunk.Text,
		CreatedAt: FormatProjectTime(createdAt),
	}
}

func FormatProjectTime(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}
