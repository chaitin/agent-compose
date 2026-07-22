package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestSchedulerRunIsNewer(t *testing.T) {
	tests := []struct {
		name         string
		schedulerRun composeSchedulerRunItem
		projectRun   *agentcomposev2.RunSummary
		want         bool
	}{
		{name: "missing scheduler run", projectRun: &agentcomposev2.RunSummary{RunId: "project-run"}},
		{name: "no project run", schedulerRun: composeSchedulerRunItem{RunID: "scheduler-run"}, want: true},
		{
			name:         "scheduler run is newer",
			schedulerRun: composeSchedulerRunItem{RunID: "scheduler-run", CompletedAt: "2026-07-15T12:00:00.1Z"},
			projectRun:   &agentcomposev2.RunSummary{RunId: "project-run", CompletedAt: "2026-07-15T12:00:00Z"},
			want:         true,
		},
		{
			name:         "project run is newer",
			schedulerRun: composeSchedulerRunItem{RunID: "scheduler-run", CompletedAt: "2026-07-15T11:00:00Z"},
			projectRun:   &agentcomposev2.RunSummary{RunId: "project-run", CreatedAt: "2026-07-15T10:00:00Z", CompletedAt: "2026-07-15T12:00:00Z"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := schedulerRunIsNewer(tt.schedulerRun, tt.projectRun); got != tt.want {
				t.Fatalf("schedulerRunIsNewer() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestLegacySchedulerSandboxBelongsToProject(t *testing.T) {
	project := testCLIProject("project-1", "project-one", "/work/agent-compose.yml")
	loaderID, err := domain.StableManagedLoaderID("project-1", "reviewer", "")
	if err != nil {
		t.Fatalf("build managed loader id: %v", err)
	}
	legacyTags := map[string]string{
		"origin":    "loader",
		"loader_id": loaderID,
	}
	legacySandbox := &agentcomposev2.Sandbox{Tags: []*agentcomposev2.SandboxTag{
		{Name: "origin", Value: "loader"},
		{Name: "loader_id", Value: loaderID},
	}}
	if !composePSSessionBelongsToProject(legacySandbox, project, nil) {
		t.Fatal("ps should include a legacy managed scheduler sandbox in its project")
	}
	if !legacySchedulerSandboxBelongsToProject(legacyTags, project) {
		t.Fatal("legacy managed scheduler sandbox should belong to its project")
	}
	if agentName := legacySchedulerAgentForProject(legacyTags, project); agentName != "reviewer" {
		t.Fatalf("legacy scheduler agent = %q, want reviewer", agentName)
	}
	if legacySchedulerSandboxBelongsToProject(legacyTags, testCLIProject("project-2", "project-two", "/other/agent-compose.yml")) {
		t.Fatal("legacy managed scheduler sandbox should not belong to another project")
	}
	if legacySchedulerSandboxBelongsToProject(map[string]string{"origin": "loader", "loader_id": "standalone-loader"}, project) {
		t.Fatal("standalone loader sandbox should not belong to the project")
	}
	if legacySchedulerSandboxBelongsToProject(map[string]string{"origin": "loader", "loader_id": loaderID, "project_id": "project-2"}, project) {
		t.Fatal("explicit project ownership should not be overridden by legacy inference")
	}
}

func TestIntegrationCLIPSDisplaysLatestSchedulerRunForSandbox(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-ps-scheduler-run
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
`)
	const sandboxID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	projectRunID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	schedulerRunID := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	staleTagRunID := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	projectID := ""
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(_ context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				projectID = req.Msg.GetProject().GetProjectId()
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: testCLIProject(projectID, "cli-ps-scheduler-run", composePath)}), nil
			},
			listSchedulerRuns: func(_ context.Context, req *connect.Request[agentcomposev2.ListSchedulerRunsRequest]) (*connect.Response[agentcomposev2.ListSchedulerRunsResponse], error) {
				if req.Msg.GetAgentName() != "reviewer" {
					t.Fatalf("ListSchedulerRuns agent = %q, want reviewer", req.Msg.GetAgentName())
				}
				return connect.NewResponse(&agentcomposev2.ListSchedulerRunsResponse{Runs: []*agentcomposev2.SchedulerRun{{
					RunId: schedulerRunID, AgentName: "reviewer", TriggerId: "nightly", Status: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED,
					SandboxIds: []string{sandboxID}, CompletedAt: timestamppb.New(time.Date(2026, 7, 15, 12, 1, 0, 0, time.UTC)),
				}}}), nil
			},
		},
		run: runServiceStub{listRuns: func(context.Context, *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
				RunId: projectRunID, ProjectId: projectID, AgentName: "reviewer", SandboxId: sandboxID, CompletedAt: "2026-07-15T11:00:00Z",
			}}}), nil
		}},
		sandbox: sandboxServiceStub{listSandboxes: func(context.Context, *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: []*agentcomposev2.Sandbox{{
				SandboxId: sandboxID,
				Status:    "running",
				Tags: []*agentcomposev2.SandboxTag{
					{Name: "origin", Value: "scheduler"},
					{Name: "project_id", Value: projectID},
					{Name: "agent", Value: "reviewer"},
					{Name: "scheduler_run_id", Value: staleTagRunID},
				},
			}}}), nil
		}},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("ps", "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("ps code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if !strings.Contains(stdout, schedulerRunID[:12]) || strings.Contains(stdout, projectRunID[:12]) || strings.Contains(stdout, staleTagRunID[:12]) {
		t.Fatalf("ps output = %q, want latest scheduler run %s", stdout, schedulerRunID[:12])
	}
}
