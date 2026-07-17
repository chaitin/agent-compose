package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestIntegrationCLIRunInteractiveRemoveSkipsExistingSandbox(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-rm-existing
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetSandboxId() != "sandbox-existing" {
					t.Fatalf("RunAgentStream sandbox = %q, want sandbox-existing", req.Msg.GetSandboxId())
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-repl-existing",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-repl-existing",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-existing",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-existing", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				t.Fatalf("RemoveSandbox should not be called for an explicit sandbox")
				return nil, nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("", "run", "--host", server.URL, "--file", composePath, "--rm", "--sandbox", "sandbox-existing", "reviewer", "-i", "--prompt", "first")
	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("run -i --rm --sandbox code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
}

func TestIntegrationCLIRunRemoveSandboxSkipsFailedRun(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-rm-failed
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-rm-failed",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-rm-failed",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_FAILED,
						SandboxId: "sandbox-failed",
						ExitCode:  9,
						Error:     "failed before cleanup",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-rm-failed", "reviewer", "sandbox-failed", agentcomposev2.RunStatus_RUN_STATUS_FAILED, 9, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--rm", "reviewer", "--prompt", "clean")
	if exitCode != 9 {
		t.Fatalf("run --rm failed exit code = %d, want 9; stderr=%q", exitCode, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "failed before cleanup") {
		t.Fatalf("run --rm failed stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestIntegrationCLIStopSandbox(t *testing.T) {
	var stopped []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		session: sessionServiceStub{
			stopSession: func(ctx context.Context, req *connect.Request[agentcomposev2.StopSandboxRequest]) (*connect.Response[agentcomposev2.StopSandboxResponse], error) {
				stopped = append(stopped, req.Msg.GetSandboxId())
				return connect.NewResponse(&agentcomposev2.StopSandboxResponse{}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("stop", "--host", server.URL, "sandbox-stop")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("stop code/stderr = %d / %q", exitCode, stderr)
	}
	if stdout != "stopped sandbox sandbox-stop\n" {
		t.Fatalf("stop stdout = %q", stdout)
	}
	if len(stopped) != 1 || stopped[0] != "sandbox-stop" {
		t.Fatalf("stopped sandboxes = %#v", stopped)
	}
}

func TestIntegrationCLIStatsWithoutSandboxUsesProjectRunningSandboxes(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-stats-demo
agents:
  reviewer:
    provider: codex
  worker:
    provider: codex
`)
	project := testCLIProject("project-cli-stats", "cli-stats-demo", composePath)
	sessions := []*agentcomposev2.Sandbox{
		testCLISessionSummary("session-one", "RUNNING", "project-cli-stats", "reviewer", "run-one"),
		testCLISessionSummary("session-two", "RUNNING", "project-cli-stats", "worker", "run-two"),
		testCLISessionSummary("session-stopped", "STOPPED", "project-cli-stats", "reviewer", "run-stopped"),
		testCLISessionSummary("session-foreign", "RUNNING", "foreign-project", "reviewer", "run-foreign"),
	}
	runs := []*agentcomposev2.RunSummary{
		{RunId: "run-one", ProjectId: project.GetSummary().GetProjectId(), AgentName: "reviewer", Status: agentcomposev2.RunStatus_RUN_STATUS_RUNNING, SandboxId: "session-one", UpdatedAt: "2026-06-11T00:00:01Z"},
		{RunId: "run-two", ProjectId: project.GetSummary().GetProjectId(), AgentName: "worker", Status: agentcomposev2.RunStatus_RUN_STATUS_RUNNING, SandboxId: "session-two", UpdatedAt: "2026-06-11T00:00:02Z"},
		{RunId: "run-stopped", ProjectId: project.GetSummary().GetProjectId(), AgentName: "reviewer", Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, SandboxId: "session-stopped", UpdatedAt: "2026-06-11T00:00:03Z"},
	}
	var statsCalls []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
		},
		run: runServiceStub{
			listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				if req.Msg.GetProjectId() != project.GetSummary().GetProjectId() || req.Msg.GetLimit() < 100 {
					t.Fatalf("ListRuns request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: runs}), nil
			},
		},
		session: sessionServiceStub{
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
				if req.Msg.GetLimit() < 100 {
					t.Fatalf("ListSessions request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: sessions}), nil
			},
		},
		sandbox: sandboxServiceStub{
			getStats: func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxStatsRequest]) (*connect.Response[agentcomposev2.GetSandboxStatsResponse], error) {
				statsCalls = append(statsCalls, req.Msg.GetSandboxId())
				value := float64(len(statsCalls) * 10)
				return connect.NewResponse(&agentcomposev2.GetSandboxStatsResponse{Stats: &agentcomposev2.SandboxStats{
					SandboxId:        req.Msg.GetSandboxId(),
					Driver:           "boxlite",
					SampledAt:        "2026-07-04T08:00:00Z",
					CpuPercent:       testStatsMetric(value, "percent"),
					MemoryUsageBytes: testStatsMetric(value*100, "bytes"),
					UptimeSeconds:    testStatsMetric(value, "seconds"),
				}}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("stats", "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("stats code/stderr = %d / %q", exitCode, stderr)
	}
	for _, want := range []string{"SANDBOX", "session-one", "session-two", "boxlite", "10.00", "20.00"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stats output %q does not contain %q", stdout, want)
		}
	}
	for _, notWant := range []string{"session-stopped", "session-foreign"} {
		if strings.Contains(stdout, notWant) {
			t.Fatalf("stats output %q contains %q", stdout, notWant)
		}
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("stats", "--host", server.URL, "--file", composePath, "--json")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("stats --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeProjectStatsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("stats JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.Project.Name != "cli-stats-demo" || len(decoded.Stats) != 2 {
		t.Fatalf("stats JSON project/stats = %#v", decoded)
	}
	if decoded.Stats[0].SandboxID != "session-one" || decoded.Stats[1].SandboxID != "session-two" {
		t.Fatalf("stats JSON order = %#v", decoded.Stats)
	}
	if strings.Contains(jsonOut, "session-stopped") || strings.Contains(jsonOut, "session-foreign") {
		t.Fatalf("stats JSON includes non-running or foreign sandbox: %s", jsonOut)
	}
	wantCalls := []string{"session-one", "session-two", "session-one", "session-two"}
	if !reflect.DeepEqual(statsCalls, wantCalls) {
		t.Fatalf("stats calls = %#v, want %#v", statsCalls, wantCalls)
	}
}

func TestIntegrationCLIStatsWithoutSandboxAllowsNoRunningSandboxes(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-stats-empty
agents:
  reviewer:
    provider: codex
`)
	project := testCLIProject("project-cli-stats-empty", "cli-stats-empty", composePath)
	sessions := []*agentcomposev2.Sandbox{
		testCLISessionSummary("session-stopped", "STOPPED", "project-cli-stats-empty", "reviewer", "run-stopped"),
		testCLISessionSummary("session-foreign", "RUNNING", "foreign-project", "reviewer", "run-foreign"),
	}
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
		},
		run: runServiceStub{
			listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{}}), nil
			},
		},
		session: sessionServiceStub{
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: sessions}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("stats", "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("stats empty code/stderr = %d / %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "SANDBOX") || strings.Contains(stdout, "session-stopped") || strings.Contains(stdout, "session-foreign") {
		t.Fatalf("stats empty output = %q", stdout)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("stats", "--host", server.URL, "--file", composePath, "--json")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("stats empty --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeProjectStatsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("stats empty JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.Project.Name != "cli-stats-empty" || len(decoded.Stats) != 0 {
		t.Fatalf("stats empty JSON = %#v", decoded)
	}
}
