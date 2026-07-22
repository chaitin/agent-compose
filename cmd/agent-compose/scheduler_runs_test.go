package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestIntegrationCLISchedulerInvokeHasNoLegacyRunOrDetachCompatibility(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-main
agents:
  reviewer:
    provider: codex
    scheduler:
      enabled: true
      script: |
        function main(payload) { return payload; }
`)
	var invokeRequests []*agentcomposev2.InvokeSchedulerRequest
	server := newComposeServiceStubServer(t, composeServiceStubs{project: projectServiceStub{
		invokeScheduler: func(_ context.Context, req *connect.Request[agentcomposev2.InvokeSchedulerRequest]) (*connect.Response[agentcomposev2.InvokeSchedulerResponse], error) {
			invokeRequests = append(invokeRequests, req.Msg)
			return connect.NewResponse(&agentcomposev2.InvokeSchedulerResponse{ResultJson: `{"main":true}`, DurationMs: 25}), nil
		},
	}})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "invoke", "--host", server.URL, "--file", composePath, "--payload", ` { "main" : true } `, "reviewer")
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "Scheduler: reviewer") || !strings.Contains(stdout, `Result: {"main":true}`) {
		t.Fatalf("scheduler invoke code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if len(invokeRequests) != 1 || invokeRequests[0].GetAgentName() != "reviewer" || invokeRequests[0].GetPayloadJson() != `{"main":true}` {
		t.Fatalf("InvokeScheduler requests = %#v", invokeRequests)
	}

	legacyOut, legacyErr, _, legacyCode := executeCLICommand("scheduler", "run", "--host", server.URL, "--file", composePath, "reviewer")
	if legacyCode != exitCodeGeneral || legacyOut != "" || !strings.Contains(legacyErr, "unknown command") || len(invokeRequests) != 1 {
		t.Fatalf("removed scheduler run code/stdout/stderr/requests = %d / %q / %q / %d", legacyCode, legacyOut, legacyErr, len(invokeRequests))
	}
	detachOut, detachErr, _, detachCode := executeCLICommand("scheduler", "invoke", "--host", server.URL, "--file", composePath, "--detach", "reviewer")
	if detachCode != exitCodeUsage || detachOut != "" || !strings.Contains(detachErr, "unknown flag: --detach") || len(invokeRequests) != 1 {
		t.Fatalf("scheduler invoke removed --detach code/stdout/stderr/requests = %d / %q / %q / %d", detachCode, detachOut, detachErr, len(invokeRequests))
	}
}

func TestIntegrationCLISchedulerLogsDefaultsToAllTriggerRunsAndAppliesGlobalTail(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-logs
agents:
  reviewer:
    scheduler:
      script: function main() {}
`)
	var requests []*agentcomposev2.ListProjectSchedulerEventsRequest
	events := []*agentcomposev2.SchedulerEvent{
		{Id: "event-new", RunId: "run-new", AgentName: "reviewer", TriggerId: "trigger-new", Type: "loader.log", Level: "info", Message: "newest", CreatedAt: timestamppb.New(time.Unix(200, 0).UTC())},
		{Id: "event-old", RunId: "run-old", AgentName: "reviewer", TriggerId: "trigger-old", Type: "loader.log", Level: "info", Message: "oldest", CreatedAt: timestamppb.New(time.Unix(100, 0).UTC())},
	}
	server := newComposeServiceStubServer(t, composeServiceStubs{project: projectServiceStub{
		listProjectSchedulerEvents: func(_ context.Context, req *connect.Request[agentcomposev2.ListProjectSchedulerEventsRequest]) (*connect.Response[agentcomposev2.ListProjectSchedulerEventsResponse], error) {
			requests = append(requests, req.Msg)
			limit := min(int(req.Msg.GetLimit()), len(events))
			return connect.NewResponse(&agentcomposev2.ListProjectSchedulerEventsResponse{Events: events[:limit]}), nil
		},
	}})
	defer server.Close()
	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "logs", "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "oldest") || !strings.Contains(stdout, "newest") || strings.Index(stdout, "oldest") > strings.Index(stdout, "newest") {
		t.Fatalf("scheduler logs code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if len(requests) != 1 || requests[0].GetAgentName() != "" || requests[0].GetRunId() != "" || requests[0].GetLimit() != 500 {
		t.Fatalf("default logs requests = %#v", requests)
	}
	requests = nil
	tailOut, tailErr, _, tailCode := executeCLICommand("scheduler", "logs", "--host", server.URL, "--file", composePath, "--tail", "1")
	if tailCode != 0 || tailErr != "" || !strings.Contains(tailOut, "newest") || strings.Contains(tailOut, "oldest") || len(requests) != 1 || requests[0].GetLimit() != 1 {
		t.Fatalf("scheduler logs --tail 1 code/stdout/stderr/requests = %d / %q / %q / %#v", tailCode, tailOut, tailErr, requests)
	}
}

func TestCLISchedulerExecutionRejectsUnsupportedFlags(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-unsupported
agents:
  reviewer:
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
          prompt: review
`)
	tests := []struct {
		name string
		args []string
		flag string
	}{
		{name: "prompt", args: []string{"--prompt", "override"}, flag: "--prompt"},
		{name: "sandbox", args: []string{"--sandbox", "sandbox-1"}, flag: "--sandbox"},
		{name: "driver", args: []string{"--driver", "docker"}, flag: "--driver"},
		{name: "keep running", args: []string{"--keep-running"}, flag: "--keep-running"},
		{name: "remove false", args: []string{"--rm=false"}, flag: "--rm"},
		{name: "jupyter", args: []string{"--jupyter"}, flag: "--jupyter"},
		{name: "jupyter expose", args: []string{"--jupyter-expose"}, flag: "--jupyter-expose"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"scheduler", "trigger", "--file", composePath}
			args = append(args, test.args...)
			args = append(args, "reviewer", "nightly")
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "unknown flag: "+test.flag) {
				t.Fatalf("unsupported flag code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
			}
		})
	}
	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "invoke", "--file", composePath, "--prompt", "override", "reviewer")
	if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "unknown flag: --prompt") {
		t.Fatalf("scheduler invoke unsupported flag code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
}

func TestIntegrationCLISchedulerRunsPaginatesAndFiltersAgent(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-runs
agents:
  reviewer:
    scheduler:
      enabled: true
      script: function main() {}
`)
	var requests []*agentcomposev2.ListSchedulerRunsRequest
	newRunID := "11111111-1111-4111-8111-111111111111"
	oldRunID := "22222222-2222-4222-8222-222222222222"
	server := newComposeServiceStubServer(t, composeServiceStubs{project: projectServiceStub{
		listSchedulerRuns: func(_ context.Context, req *connect.Request[agentcomposev2.ListSchedulerRunsRequest]) (*connect.Response[agentcomposev2.ListSchedulerRunsResponse], error) {
			requests = append(requests, req.Msg)
			if req.Msg.GetCursor() == "" {
				return connect.NewResponse(&agentcomposev2.ListSchedulerRunsResponse{
					Runs:       []*agentcomposev2.SchedulerRun{{RunId: newRunID, AgentName: "reviewer", TriggerId: "trigger-1", Status: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED}},
					NextCursor: "page-2",
				}), nil
			}
			return connect.NewResponse(&agentcomposev2.ListSchedulerRunsResponse{
				Runs: []*agentcomposev2.SchedulerRun{{RunId: oldRunID, AgentName: "reviewer", TriggerId: "trigger-1", Status: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SKIPPED}},
			}), nil
		},
	}})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "runs", "--host", server.URL, "--file", composePath, "reviewer")
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, shortOpaqueID(newRunID)) || !strings.Contains(stdout, shortOpaqueID(oldRunID)) || !strings.Contains(stdout, "skipped") {
		t.Fatalf("scheduler runs code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if len(requests) != 2 || requests[0].GetAgentName() != "reviewer" || requests[0].GetCursor() != "" || requests[1].GetCursor() != "page-2" {
		t.Fatalf("ListSchedulerRuns requests = %#v", requests)
	}

	requests = nil
	filteredOut, filteredErr, _, filteredCode := executeCLICommand("scheduler", "runs", "--host", server.URL, "--file", composePath, "--status", "skipped", "reviewer")
	if filteredCode != 0 || filteredErr != "" || !strings.Contains(filteredOut, shortOpaqueID(oldRunID)) || strings.Contains(filteredOut, shortOpaqueID(newRunID)) {
		t.Fatalf("scheduler runs --status skipped code/stdout/stderr = %d / %q / %q", filteredCode, filteredOut, filteredErr)
	}
	if len(requests) != 2 || requests[0].GetCursor() != "" || requests[1].GetCursor() != "page-2" {
		t.Fatalf("filtered ListSchedulerRuns requests = %#v", requests)
	}
	_, pendingErr, _, pendingCode := executeCLICommand("scheduler", "runs", "--host", server.URL, "--file", composePath, "--status", "pending")
	if pendingCode != exitCodeUsage || !strings.Contains(pendingErr, "running, succeeded, failed, canceled, or skipped") {
		t.Fatalf("scheduler runs --status pending code/stderr = %d / %q", pendingCode, pendingErr)
	}
}

func TestIntegrationCLISchedulerStop(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-stop
agents:
  reviewer:
    scheduler:
      enabled: true
      script: function main() {}
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{project: projectServiceStub{
		stopSchedulerRun: func(_ context.Context, req *connect.Request[agentcomposev2.StopSchedulerRunRequest]) (*connect.Response[agentcomposev2.StopSchedulerRunResponse], error) {
			if req.Msg.GetRunId() != "scheduler-run-active" || req.Msg.GetReason() != "manual stop" {
				t.Fatalf("StopSchedulerRun request = %#v", req.Msg)
			}
			return connect.NewResponse(&agentcomposev2.StopSchedulerRunResponse{
				Run:           &agentcomposev2.SchedulerRun{RunId: req.Msg.GetRunId(), AgentName: "reviewer", Status: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_CANCELED, Error: req.Msg.GetReason()},
				StopRequested: true,
			}), nil
		},
	}})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "stop", "--host", server.URL, "--file", composePath, "--reason", "manual stop", "scheduler-run-active")
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "Status: canceled") || !strings.Contains(stdout, "Stop requested: true") {
		t.Fatalf("scheduler stop code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
}

func TestIntegrationCLIInspectRunFallsBackToSchedulerRun(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-inspect-run
agents:
  reviewer:
    scheduler:
      enabled: true
      script: function main() {}
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getSchedulerRun: func(_ context.Context, req *connect.Request[agentcomposev2.GetSchedulerRunRequest]) (*connect.Response[agentcomposev2.GetSchedulerRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetSchedulerRunResponse{Run: &agentcomposev2.SchedulerRun{
					RunId: req.Msg.GetRunId(), ProjectId: req.Msg.GetProject().GetProjectId(), AgentName: "reviewer",
					Status: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED, ResultJson: `{"inspected":true}`,
				}}), nil
			},
		},
		run: runServiceStub{
			getRun: func(context.Context, *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return nil, connect.NewError(connect.CodeNotFound, context.Canceled)
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("inspect", "run", "scheduler-run-inspect", "--host", server.URL, "--file", composePath, "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("inspect scheduler run code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	var output composeSchedulerRunOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil || output.ID != "scheduler-run-inspect" || output.Status != "succeeded" || output.ResultJSON != `{"inspected":true}` {
		t.Fatalf("inspect scheduler run output=%#v err=%v", output, err)
	}
}
