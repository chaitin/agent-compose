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

func TestIntegrationCLIWorkspaceRegistryConfigAndApply(t *testing.T) {
	testCLIWorkspaceRegistryConfigAndApply(t)
}

func TestIntegrationCLIDownFirstRepeatedPartialAndJSON(t *testing.T) {
	testCLIDownFirstRepeatedPartialAndJSON(t)
}

func TestIntegrationCLIPSTableAndJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-ps-demo
agents:
  reviewer:
    provider: codex
  worker:
    provider: codex
`)
	project := testCLIProject("project-cli-ps", "cli-ps-demo", composePath)
	sessions := []*agentcomposev2.Sandbox{
		testCLISessionSummary("session-running", "RUNNING", "project-cli-ps", "reviewer", "run-running"),
		testCLISessionSummary("session-stopped", "STOPPED", "project-cli-ps", "worker", "run-stopped"),
		testCLISessionSummary("session-error", "ERROR", "foreign-project", "", ""),
		testCLISessionSummary("session-foreign", "RUNNING", "foreign-project", "reviewer", "run-foreign"),
	}
	runs := []*agentcomposev2.RunSummary{
		{
			RunId:     "run-running",
			ProjectId: project.GetSummary().GetProjectId(),
			AgentName: "reviewer",
			Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
			SandboxId: "session-running",
			CreatedAt: "2026-06-11T00:00:00Z",
			UpdatedAt: "2026-06-11T00:00:01Z",
		},
		{
			RunId:     "run-error",
			ProjectId: project.GetSummary().GetProjectId(),
			AgentName: "worker",
			Status:    agentcomposev2.RunStatus_RUN_STATUS_FAILED,
			SandboxId: "session-error",
			CreatedAt: "2026-06-11T00:00:02Z",
			UpdatedAt: "2026-06-11T00:00:03Z",
		},
	}
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
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("ps", "--host", server.URL, "--file", composePath, "--json")
	if exitCode != 0 {
		t.Fatalf("ps --json exit code = %d, stderr=%q", exitCode, stderr)
	}
	var decoded composePSOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("ps JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.Project.Name != "cli-ps-demo" || len(decoded.Sandboxes) != 1 {
		t.Fatalf("ps JSON project/sandboxes = %#v", decoded)
	}
	if decoded.Sandboxes[0].SandboxID != "session-running" || decoded.Sandboxes[0].Agent != "reviewer" || decoded.Sandboxes[0].Status != "running" || decoded.Sandboxes[0].RunID != "run-running" {
		t.Fatalf("ps sandbox JSON = %#v", decoded.Sandboxes[0])
	}
	if stdout == "" || !strings.Contains(stdout, `"sandbox_id"`) || !strings.Contains(stdout, `"sandbox_short_id"`) || strings.Contains(stdout, `"session_id"`) {
		t.Fatalf("ps JSON sandbox field shape = %q", stdout)
	}

	sandboxOut, sandboxErr, _, sandboxCode := executeCLICommand("sandbox", "ls", "--host", server.URL, "--file", composePath, "--json")
	if sandboxCode != 0 || sandboxErr != "" {
		t.Fatalf("sandbox ls --json code/stderr = %d / %q", sandboxCode, sandboxErr)
	}
	var sandboxDecoded composePSOutput
	if err := json.Unmarshal([]byte(sandboxOut), &sandboxDecoded); err != nil {
		t.Fatalf("sandbox ls JSON decode failed: %v\n%s", err, sandboxOut)
	}
	if !reflect.DeepEqual(sandboxDecoded, decoded) {
		t.Fatalf("sandbox ls JSON = %#v, want %#v", sandboxDecoded, decoded)
	}

	textOut, textErr, _, textCode := executeCLICommand("ps", "--host", server.URL, "--file", composePath)
	if textCode != 0 || textErr != "" {
		t.Fatalf("ps text code/stderr = %d / %q", textCode, textErr)
	}
	for _, want := range []string{"SANDBOX ID", "AGENT", "STATUS", "RUN ID", "CREATED", "UPDATED", "session-runn", "reviewer", "running", "running"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("ps text output %q does not contain %q", textOut, want)
		}
	}
	for _, notWant := range []string{"session-stopped", "session-error", "session-foreign"} {
		if strings.Contains(textOut, notWant) {
			t.Fatalf("ps default text output %q contains %q", textOut, notWant)
		}
	}

	allOut, allErr, _, allCode := executeCLICommand("ps", "--host", server.URL, "--file", composePath, "--all")
	if allCode != 0 || allErr != "" {
		t.Fatalf("ps --all code/stderr = %d / %q", allCode, allErr)
	}
	for _, want := range []string{"session-runn", "session-stop", "session-erro"} {
		if !strings.Contains(allOut, want) {
			t.Fatalf("ps --all output %q does not contain %q", allOut, want)
		}
	}
	if strings.Contains(allOut, "session-foreign") {
		t.Fatalf("ps --all output %q contains foreign sandbox", allOut)
	}

	statusOut, statusErr, _, statusCode := executeCLICommand("ps", "--host", server.URL, "--file", composePath, "--status", "error")
	if statusCode != 0 || statusErr != "" {
		t.Fatalf("ps --status code/stderr = %d / %q", statusCode, statusErr)
	}
	if !strings.Contains(statusOut, "session-erro") || strings.Contains(statusOut, "session-runn") || strings.Contains(statusOut, "session-stop") {
		t.Fatalf("ps --status output = %q", statusOut)
	}

	verboseOut, verboseErr, _, verboseCode := executeCLICommand("ps", "--host", server.URL, "--file", composePath, "--verbose")
	if verboseCode != 0 || verboseErr != "" {
		t.Fatalf("ps --verbose code/stderr = %d / %q", verboseCode, verboseErr)
	}
	for _, want := range []string{"DRIVER", "IMAGE", "WORKSPACE", "boxlite", "guest:latest", "/workspace/session-running"} {
		if !strings.Contains(verboseOut, want) {
			t.Fatalf("ps --verbose output %q does not contain %q", verboseOut, want)
		}
	}
}
