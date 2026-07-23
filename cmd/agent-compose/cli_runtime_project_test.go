package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestNormalizedRuntimeAgentSpecPreservesSchedulerConcurrencyPolicy(t *testing.T) {
	enabled := true
	agent := normalizedRuntimeAgentSpec(&agentcomposev2.AgentSpec{
		Name:     "worker",
		Provider: "codex",
		Enabled:  &enabled,
		Scheduler: &agentcomposev2.SchedulerSpec{
			Enabled:           true,
			ConcurrencyPolicy: "parallel",
		},
	})

	if agent.Scheduler == nil || agent.Scheduler.ConcurrencyPolicy != "parallel" {
		t.Fatalf("scheduler = %#v, want concurrency policy parallel", agent.Scheduler)
	}
}

func TestIntegrationCLIRuntimeCommandsSelectStoredProjectByName(t *testing.T) {
	withWorkingDir(t, t.TempDir())
	enabled := true
	project := &agentcomposev2.Project{
		Summary: &agentcomposev2.ProjectSummary{ProjectId: "project-stored", Name: "stored-project"},
		Spec: &agentcomposev2.ProjectSpec{Name: "stored-project", Agents: []*agentcomposev2.AgentSpec{{
			Name: "worker", Provider: "codex", Enabled: &enabled,
			Scheduler: &agentcomposev2.SchedulerSpec{Enabled: true, ConcurrencyPolicy: "parallel", Triggers: []*agentcomposev2.TriggerSpec{{Name: "manual", Kind: "manual"}}},
		}}},
		Agents:     []*agentcomposev2.ProjectAgent{{ProjectId: "project-stored", AgentName: "worker", ManagedAgentId: "agent-stored", Provider: "codex", Enabled: true}},
		Schedulers: []*agentcomposev2.ProjectScheduler{{ProjectId: "project-stored", AgentName: "worker", SchedulerId: "scheduler-stored", Enabled: true, TriggerCount: 1}},
	}
	var getProjectCalls int
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(_ context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				getProjectCalls++
				if req.Msg.GetProject().GetName() != "stored-project" || req.Msg.GetProject().GetProjectId() != "" || !req.Msg.GetIncludeSpec() {
					t.Fatalf("GetProject request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
			removeProject: func(_ context.Context, req *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
				if req.Msg.GetProject().GetProjectId() != "project-stored" {
					t.Fatalf("RemoveProject ref = %#v", req.Msg.GetProject())
				}
				return connect.NewResponse(&agentcomposev2.RemoveProjectResponse{Project: project}), nil
			},
		},
		run: runServiceStub{
			startRun: func(_ context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
				if req.Msg.GetRun().GetProjectId() != "project-stored" || req.Msg.GetRun().GetAgentName() != "worker" {
					t.Fatalf("StartRun request = %#v", req.Msg.GetRun())
				}
				return connect.NewResponse(&agentcomposev2.StartRunResponse{Run: &agentcomposev2.RunSummary{
					RunId: "run-stored", ProjectId: "project-stored", ProjectName: "stored-project", AgentName: "worker", Status: agentcomposev2.RunStatus_RUN_STATUS_PENDING,
				}, Started: true}), nil
			},
			listRuns: func(_ context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				if req.Msg.GetProjectId() != "project-stored" {
					t.Fatalf("ListRuns project = %q", req.Msg.GetProjectId())
				}
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{}), nil
			},
		},
		exec: execServiceStub{execStream: func(_ context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
			if req.Msg.GetSandboxId() != "sandbox-stored" {
				t.Fatalf("ExecStream target = %#v", req.Msg.GetTarget())
			}
			return stream.Send(&agentcomposev2.ExecStreamResponse{EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED, Result: &agentcomposev2.ExecResult{
				ExecId: "exec-stored", SandboxId: "sandbox-stored", Success: true,
			}})
		}},
	})
	t.Cleanup(server.Close)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "down", args: []string{"down"}, want: "stored-project"},
		{name: "run", args: []string{"run", "-d", "worker", "--command", "true"}, want: "run-stored"},
		{name: "scheduler list", args: []string{"scheduler", "ls"}, want: "manual"},
		{name: "logs", args: []string{"logs"}},
		{name: "exec", args: []string{"exec", "sandbox-stored", "--command", "true"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{}, test.args...)
			args = append(args, "--project-name", "stored-project", "--host", server.URL)
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != 0 || stderr != "" {
				t.Fatalf("code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
			}
			if test.want != "" && !strings.Contains(stdout, test.want) {
				t.Fatalf("stdout = %q, want %q", stdout, test.want)
			}
		})
	}
	if getProjectCalls != len(tests) {
		t.Fatalf("GetProject calls = %d, want %d", getProjectCalls, len(tests))
	}
}

func TestCLIConfigKeepsLocalComposeRequirementWithProjectName(t *testing.T) {
	withWorkingDir(t, t.TempDir())
	_, stderr, _, exitCode := executeCLICommand("config", "--project-name", "stored-project")
	if exitCode != exitCodeUsage || !strings.Contains(stderr, "agent-compose.yml") {
		t.Fatalf("config code/stderr = %d / %q", exitCode, stderr)
	}
}
