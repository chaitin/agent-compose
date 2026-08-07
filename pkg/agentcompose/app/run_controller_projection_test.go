package app

import (
	"testing"
	"time"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestRunAttachInputFromProtoPreservesUnknownModeAsInvalid(t *testing.T) {
	input := runAttachInputFromProto(&agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
			Mode:    agentcomposev2.AttachRunMode(99),
			Request: &agentcomposev2.RunAgentRequest{Command: "echo hello"},
		}},
	})

	if input.Mode != runs.RunAttachModeInvalid {
		t.Fatalf("mode = %q, want invalid", input.Mode)
	}
}

func TestRunAttachInputFromProtoPreservesLegacyRequestVolumeBehavior(t *testing.T) {
	request := &agentcomposev2.RunAgentRequest{
		Command: "echo hello",
		Volumes: []*agentcomposev2.VolumeMountSpec{{
			Type:     agentcomposev2.VolumeMountType_VOLUME_MOUNT_TYPE_BIND,
			Source:   "/host/source",
			Target:   "/guest/target",
			ReadOnly: true,
		}},
	}
	if mapped := runAgentRequestFromProto(request); len(mapped.Volumes) != 1 {
		t.Fatalf("ordinary run volumes = %#v, want request volume preserved", mapped.Volumes)
	}

	input := runAttachInputFromProto(&agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
			Mode:    agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND,
			Request: request,
		}},
	})

	if len(input.Request.Volumes) != 0 {
		t.Fatalf("attach request volumes = %#v, want legacy ignored behavior", input.Request.Volumes)
	}
}

func TestRunAgentStreamStartedProjectionPreservesResponseFields(t *testing.T) {
	createdAt := time.Date(2026, 7, 10, 8, 9, 10, 123456789, time.FixedZone("CST", 8*60*60))
	run := domain.ProjectRunRecord{
		RunID:           "run-1",
		ProjectID:       "project-1",
		ProjectName:     "Project One",
		ProjectRevision: 3,
		AgentName:       "worker",
		AgentID:         "agent-1",
		Source:          domain.ProjectRunSourceAPI,
		Status:          domain.ProjectRunStatusRunning,
		SandboxID:       "session-1",
		Warnings:        []string{"warning-1"},
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}

	resp := runAgentStreamStartedProjection(run, createdAt)
	run.Warnings[0] = "mutated"

	if resp.GetEventType() != agentcomposev2.StreamAgentRunEventType_STREAM_AGENT_RUN_EVENT_TYPE_STARTED {
		t.Fatalf("event type = %s", resp.GetEventType())
	}
	if resp.GetRunId() != "run-1" || !resp.GetCreatedAt().AsTime().Equal(createdAt) {
		t.Fatalf("response identity/time = %#v", resp)
	}
	if resp.GetRun().GetRunId() != "run-1" || resp.GetRun().GetSandboxId() != "session-1" || resp.GetRun().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_RUNNING {
		t.Fatalf("run summary = %#v", resp.GetRun())
	}
	if got := resp.GetWarnings(); len(got) != 1 || got[0] != "warning-1" {
		t.Fatalf("warnings = %#v", got)
	}
	if got := resp.GetRun().GetWarnings(); len(got) != 1 || got[0] != "warning-1" {
		t.Fatalf("summary warnings = %#v", got)
	}
}

func TestRunAgentStreamChunkProjectionPreservesOutputFields(t *testing.T) {
	createdAt := time.Date(2026, 7, 10, 1, 2, 3, 4, time.UTC)

	resp := runAgentStreamChunkProjection("run-1", domain.ExecChunk{
		Text:   "stderr text\n",
		Stream: domain.StdioStderr,
	}, createdAt)

	if resp.GetEventType() != agentcomposev2.StreamAgentRunEventType_STREAM_AGENT_RUN_EVENT_TYPE_OUTPUT {
		t.Fatalf("event type = %s", resp.GetEventType())
	}
	if resp.GetRunId() != "run-1" || resp.GetChunk() != "stderr text\n" || !resp.GetCreatedAt().AsTime().Equal(createdAt) {
		t.Fatalf("response output fields = %#v", resp)
	}
	if resp.GetStream() != agentcomposev2.StdioStream_STDIO_STREAM_STDERR {
		t.Fatalf("stream = %s", resp.GetStream())
	}
	if transcript := resp.GetTranscript(); transcript.GetText() != "stderr text\n" || transcript.GetStream() != agentcomposev2.StdioStream_STDIO_STREAM_STDERR || !transcript.GetCreatedAt().AsTime().Equal(resp.GetCreatedAt().AsTime()) {
		t.Fatalf("transcript = %#v", transcript)
	}
}

func TestRunAgentStreamCompletedProjectionPreservesResponseFields(t *testing.T) {
	createdAt := time.Date(2026, 7, 10, 11, 12, 13, 0, time.UTC)
	run := domain.ProjectRunRecord{
		RunID:     "run-1",
		ProjectID: "project-1",
		Status:    domain.ProjectRunStatusSucceeded,
		ExitCode:  0,
		Warnings:  []string{"done-warning"},
	}

	resp := runAgentStreamCompletedProjection(run, createdAt)
	run.Warnings[0] = "mutated"

	if resp.GetEventType() != agentcomposev2.StreamAgentRunEventType_STREAM_AGENT_RUN_EVENT_TYPE_COMPLETED {
		t.Fatalf("event type = %s", resp.GetEventType())
	}
	if resp.GetRunId() != "run-1" || !resp.GetCreatedAt().AsTime().Equal(createdAt) {
		t.Fatalf("response identity/time = %#v", resp)
	}
	if resp.GetRun().GetRunId() != "run-1" || resp.GetRun().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf("run summary = %#v", resp.GetRun())
	}
	if got := resp.GetWarnings(); len(got) != 1 || got[0] != "done-warning" {
		t.Fatalf("warnings = %#v", got)
	}
	if got := resp.GetRun().GetWarnings(); len(got) != 1 || got[0] != "done-warning" {
		t.Fatalf("summary warnings = %#v", got)
	}
}
