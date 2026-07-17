package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestConfigCommandUsesGlobalFileProjectNameAndJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "file-project")
	composePath := writeComposeFile(t, dir, `
name: original-project
agents:
  reviewer:
    provider: codex
`)
	withWorkingDir(t, t.TempDir())
	t.Setenv("AGENT_COMPOSE_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("AGENT_COMPOSE_HOST", "")

	stdout, stderr, runCount, err := executeCommand("config", "--file", composePath, "--project-name", "override-project", "--json")
	if err != nil {
		t.Fatalf("config with global flags returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("config with global flags stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	var decoded struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("config global JSON output is not JSON: %v\n%s", err, stdout)
	}
	if decoded.Name != "override-project" {
		t.Fatalf("config project name = %q, want override-project", decoded.Name)
	}
}

func TestConfigCommandDiscoversDefaultYAMLComposeFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "yaml-project")
	writeComposeFileNamed(t, dir, "agent-compose.yaml", `
name: yaml-project
agents:
  reviewer:
    provider: codex
`)
	withWorkingDir(t, dir)
	t.Setenv("AGENT_COMPOSE_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("AGENT_COMPOSE_HOST", "")

	stdout, stderr, runCount, err := executeCommand("config", "--json")
	if err != nil {
		t.Fatalf("config with default yaml returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("config with default yaml stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	var decoded struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("config yaml JSON output is not JSON: %v\n%s", err, stdout)
	}
	if decoded.Name != "yaml-project" {
		t.Fatalf("config project name = %q, want yaml-project", decoded.Name)
	}
}

func TestConfigCommandAmbiguousDefaultComposeFilesIsUsageError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ambiguous-project")
	writeComposeFileNamed(t, dir, "agent-compose.yml", `
name: yml-project
agents:
  reviewer:
    provider: codex
`)
	writeComposeFileNamed(t, dir, "agent-compose.yaml", `
name: yaml-project
agents:
  reviewer:
    provider: codex
`)
	withWorkingDir(t, dir)

	stdout, stderr, runCount, exitCode := executeCLICommand("config")
	if exitCode != exitCodeUsage {
		t.Fatalf("config ambiguous files exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
	}
	if stdout != "" {
		t.Fatalf("config ambiguous files stdout = %q, want empty", stdout)
	}
	for _, want := range []string{"agent-compose.yml", "agent-compose.yaml", "--file"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("config ambiguous files stderr %q does not contain %q", stderr, want)
		}
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestConfigCommandExplicitYAMLFileUsesFileDirectoryAsProjectRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "explicit-yaml-project")
	composePath := writeComposeFileNamed(t, dir, "agent-compose.yaml", `
agents:
  reviewer:
    provider: codex
`)
	withWorkingDir(t, t.TempDir())
	t.Setenv("AGENT_COMPOSE_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("AGENT_COMPOSE_HOST", "")

	stdout, stderr, runCount, err := executeCommand("config", "--file", composePath, "--json")
	if err != nil {
		t.Fatalf("config with explicit yaml returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("config with explicit yaml stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	var decoded struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("config explicit yaml JSON output is not JSON: %v\n%s", err, stdout)
	}
	if decoded.Name != "explicit-yaml-project" {
		t.Fatalf("config project name = %q, want explicit-yaml-project", decoded.Name)
	}
}

func TestConfigCommandMissingComposeFileWritesStderrAndExitCode(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing-agent-compose.yml")
	stdout, stderr, runCount, exitCode := executeCLICommand("config", "--file", missingPath)
	if exitCode != exitCodeUsage {
		t.Fatalf("config missing file exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("config missing file stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, missingPath) || !strings.Contains(stderr, "no such file") {
		t.Fatalf("config missing file stderr = %q", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestIntegrationCLIListProjectsClassifiesNotFoundAndUnsupported(t *testing.T) {
	tests := []struct {
		name    string
		rpcCode connect.Code
		exit    int
		want    string
	}{
		{name: "not found", rpcCode: connect.CodeNotFound, exit: exitCodeUsage, want: "not_found"},
		{name: "unsupported", rpcCode: connect.CodeUnimplemented, exit: exitCodeUnsupported, want: "unimplemented"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newComposeServiceStubServer(t, composeServiceStubs{
				project: projectServiceStub{
					listProjects: func(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error) {
						return nil, connect.NewError(tc.rpcCode, fmt.Errorf("%s list projects", tc.name))
					},
				},
			})
			defer server.Close()

			stdout, stderr, _, exitCode := executeCLICommand("project", "ls", "--host", server.URL)
			if exitCode != tc.exit {
				t.Fatalf("ls %s exit code = %d, want %d; stderr=%q", tc.name, exitCode, tc.exit, stderr)
			}
			if stdout != "" {
				t.Fatalf("ls %s stdout = %q, want empty", tc.name, stdout)
			}
			if !strings.Contains(stderr, "list projects") || !strings.Contains(stderr, tc.want) {
				t.Fatalf("ls %s stderr = %q, want operation context and %q", tc.name, stderr, tc.want)
			}
		})
	}
}

func TestIntegrationCLIUpAppliesProjectFirstRepeatedModifiedAndJSON(t *testing.T) {
	testCLIUpAppliesProjectFirstRepeatedModifiedAndJSON(t)
}

func testCLIUpAppliesProjectFirstRepeatedModifiedAndJSON(t *testing.T) {
	t.Helper()
	useTestDockerImage(t, "guest:v1")
	socketPath := shortUnixSocketPath(t)
	app, cancel := newTestDaemonAppWithSocketAndTCP(t, socketPath, "", nil)
	defer cancel()
	runCtx, stop := context.WithCancel(context.Background())
	errCh := runDaemonAppAsync(app, runCtx)
	t.Cleanup(func() {
		stop()
		waitForDaemonExit(t, errCh)
	})
	waitForHTTPStatus(t, newUnixHTTPClient(socketPath), "http://agent-compose/api/version", http.StatusOK)
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "")

	composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "cli-up-project"), `
name: cli-up-demo
agents:
  reviewer:
    provider: codex
    model: gpt-initial
    system_prompt: |
      Review the proposed changes carefully.
    image: guest:v1
    driver:
      docker: {}
    scheduler:
      triggers:
        - name: hourly
          cron: "0 * * * *"
          prompt: review hourly
`)
	stdout, stderr, runCount, exitCode := executeCLICommand("up", "--file", composePath)
	if exitCode != 0 {
		t.Fatalf("up first exit code = %d, stderr=%q", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("up first stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	for _, want := range []string{"ID", "NAME", "TYPE", "ACTION", "created", "agent", "trigger", "hourly"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("up first stdout %q does not contain %q", stdout, want)
		}
	}
	for _, unwanted := range []string{"project_agent", "agent_definition", "project_scheduler", "loader"} {
		if strings.Contains(stdout, unwanted) {
			t.Fatalf("up first stdout %q unexpectedly contains %q", stdout, unwanted)
		}
	}

	repeatedOut, repeatedErr, _, repeatedCode := executeCLICommand("up", "--file", composePath, "--json")
	if repeatedCode != 0 {
		t.Fatalf("up repeated exit code = %d, stderr=%q", repeatedCode, repeatedErr)
	}
	if repeatedErr != "" {
		t.Fatalf("up repeated stderr = %q, want empty", repeatedErr)
	}
	repeated := decodeComposeUpOutput(t, repeatedOut)
	if repeated.Project.Name != "cli-up-demo" || repeated.Project.CurrentRevision != 1 || repeated.Project.AgentCount != 1 || repeated.Project.SchedulerCount != 1 {
		t.Fatalf("up repeated project output = %#v", repeated.Project)
	}
	if !repeated.Applied || !repeated.Unchanged || repeated.Revision.Revision != 1 {
		t.Fatalf("up repeated state = applied %v unchanged %v revision %#v", repeated.Applied, repeated.Unchanged, repeated.Revision)
	}
	assertComposeUpChange(t, repeated.Changes, "unchanged", "project", "cli-up-demo")
	assertComposeUpChange(t, repeated.Changes, "unchanged", "project_agent", "reviewer")
	assertComposeUpChange(t, repeated.Changes, "unchanged", "agent_definition", "reviewer")
	assertComposeUpChange(t, repeated.Changes, "unchanged", "project_scheduler", "reviewer")

	if err := os.WriteFile(composePath, []byte(`
name: cli-up-demo
workspaces:
  default:
    provider: local
    path: .
agents:
  reviewer:
    provider: codex
    model: gpt-updated
    system_prompt: |
      Review the proposed changes carefully.
    image: guest:v1
    driver:
      docker: {}
    scheduler:
      triggers:
        - name: hourly
          cron: "0 * * * *"
          prompt: review hourly
`), 0o600); err != nil {
		t.Fatalf("update compose file: %v", err)
	}
	changedOut, changedErr, _, changedCode := executeCLICommand("up", "--file", composePath, "--json")
	if changedCode != 0 {
		t.Fatalf("up changed exit code = %d, stderr=%q", changedCode, changedErr)
	}
	if changedErr != "" {
		t.Fatalf("up changed stderr = %q, want empty", changedErr)
	}
	changed := decodeComposeUpOutput(t, changedOut)
	if changed.Project.CurrentRevision != 2 || changed.Revision.Revision != 2 {
		t.Fatalf("up changed revisions = project %d response %d", changed.Project.CurrentRevision, changed.Revision.Revision)
	}
	if !changed.Applied || changed.Unchanged {
		t.Fatalf("up changed state = applied %v unchanged %v", changed.Applied, changed.Unchanged)
	}
	assertComposeUpChange(t, changed.Changes, "updated", "project_agent", "reviewer")
	assertComposeUpChange(t, changed.Changes, "updated", "agent_definition", "reviewer")
}

func TestIntegrationCLIProjectCommandsMissingProjectAreFriendly(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-ps-missing
agents:
  reviewer:
    provider: codex
`)
	tests := []struct {
		name        string
		args        []string
		wantCommand string
	}{
		{name: "ps", args: []string{"ps", "--host", "%s", "--file", composePath}, wantCommand: "ps"},
		{name: "stats", args: []string{"stats", "--host", "%s", "--file", composePath}, wantCommand: "stats"},
		{name: "inspect project", args: []string{"inspect", "--host", "%s", "--file", composePath, "project"}, wantCommand: "inspect project"},
		{name: "inspect agent", args: []string{"inspect", "--host", "%s", "--file", composePath, "agent", "reviewer"}, wantCommand: "inspect agent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newComposeServiceStubServer(t, composeServiceStubs{
				project: projectServiceStub{
					getProject: func(context.Context, *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
						return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project project-cli-ps-missing not found: sql: no rows in result set"))
					},
				},
			})
			defer server.Close()

			args := append([]string(nil), tc.args...)
			for i, arg := range args {
				if arg == "%s" {
					args[i] = server.URL
				}
			}
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("%s missing project exit code = %d, want %d; stderr=%q", tc.name, exitCode, exitCodeUsage, stderr)
			}
			if stdout != "" {
				t.Fatalf("%s missing project stdout = %q, want empty", tc.name, stdout)
			}
			want := `project "cli-ps-missing" is not running: it has not been started on this daemon or was removed by ` +
				"`agent-compose down`.\n" +
				"To start it, run `agent-compose up --file " + composePath + "` before `agent-compose " + tc.wantCommand + "`"
			if !strings.Contains(stderr, want) {
				t.Fatalf("%s missing project stderr = %q, want two-line message %q", tc.name, stderr, want)
			}
			for _, notWant := range []string{"not_found", "sql: no rows"} {
				if strings.Contains(stderr, notWant) {
					t.Fatalf("%s missing project stderr = %q, should not expose %q", tc.name, stderr, notWant)
				}
			}
		})
	}
}

func TestConfigCommandQuietOnlyValidates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "quiet-project")
	writeComposeFile(t, dir, `
name: quiet-project
agents:
  reviewer:
    provider: codex
`)
	withWorkingDir(t, dir)
	t.Setenv("AGENT_COMPOSE_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("AGENT_COMPOSE_HOST", "")

	stdout, stderr, runCount, err := executeCommand("config", "--quiet")
	if err != nil {
		t.Fatalf("config --quiet returned error: %v", err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("quiet output stdout=%q stderr=%q, want empty", stdout, stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestConfigCommandQuietFailureIncludesComposePathAndField(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "invalid-project")
	composePath := writeComposeFile(t, dir, `
name: invalid-project
network:
  mode: bridge
agents:
  reviewer:
    provider: codex
`)
	withWorkingDir(t, dir)

	stdout, stderr, runCount, err := executeCommand("config", "--quiet")
	if err == nil {
		t.Fatalf("config --quiet returned nil error, want validation error")
	}
	for _, want := range []string{composePath, "network.mode", "unsupported"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err.Error(), want)
		}
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("quiet failure output stdout=%q stderr=%q, want empty", stdout, stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestIntegrationCLIListProjectsTextVerboseAndJSON(t *testing.T) {
	requests := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			listProjects: func(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error) {
				requests++
				switch req.Msg.GetOffset() {
				case 0:
					return connect.NewResponse(&agentcomposev2.ListProjectsResponse{
						Projects: []*agentcomposev2.ProjectSummary{{
							ProjectId:       "proj_1",
							Name:            "reviewer",
							SourcePath:      "/path/to/reviewer/agent-compose.yml",
							CurrentRevision: 3,
							SpecHash:        "sha256:reviewer",
							AgentCount:      2,
							SchedulerCount:  1,
							UpdatedAt:       "2026-07-03T10:00:00Z",
						}},
						TotalCount: 2,
						HasMore:    true,
						NextOffset: 1,
					}), nil
				case 1:
					return connect.NewResponse(&agentcomposev2.ListProjectsResponse{
						Projects: []*agentcomposev2.ProjectSummary{{
							ProjectId:       "proj_2",
							Name:            "builder",
							SourcePath:      "/path/to/builder/agent-compose.yaml",
							CurrentRevision: 5,
							SpecHash:        "sha256:builder",
							AgentCount:      1,
							SchedulerCount:  0,
							UpdatedAt:       "2026-07-03T11:00:00Z",
						}},
						TotalCount: 2,
					}), nil
				default:
					t.Fatalf("ListProjects unexpected offset = %d", req.Msg.GetOffset())
					return nil, nil
				}
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("project", "ls", "--host", server.URL)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("ls code/stderr = %d / %q", exitCode, stderr)
	}
	for _, want := range []string{"ID", "NAME", "CONFIG FILE", "reviewer", "/path/to/reviewer/agent-compose.yml", "builder"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("ls output %q does not contain %q", stdout, want)
		}
	}

	verboseOut, verboseErr, _, verboseCode := executeCLICommand("project", "ls", "--host", server.URL, "--verbose")
	if verboseCode != 0 || verboseErr != "" {
		t.Fatalf("ls --verbose code/stderr = %d / %q", verboseCode, verboseErr)
	}
	for _, want := range []string{"ID", "NAME", "PROJECT DIR", "SPEC HASH", "proj_1", "/path/to/reviewer", "sha256:builder", "active"} {
		if !strings.Contains(verboseOut, want) {
			t.Fatalf("ls --verbose output %q does not contain %q", verboseOut, want)
		}
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("project", "ls", "--host", server.URL, "--json")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("ls --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeProjectListOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("ls JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.TotalCount != 2 || len(decoded.Projects) != 2 {
		t.Fatalf("ls JSON = %#v", decoded)
	}
	if decoded.Projects[0].Name != "reviewer" || decoded.Projects[0].AgentCount != 2 || decoded.Projects[0].SchedulerCount != 1 || decoded.Projects[0].ServiceCount != nil {
		t.Fatalf("ls first project JSON = %#v", decoded.Projects[0])
	}
	if requests != 6 {
		t.Fatalf("ListProjects requests = %d, want 6", requests)
	}
}

func TestIntegrationCLIListProjectsPaginationFlags(t *testing.T) {
	requests := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			listProjects: func(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error) {
				requests++
				if req.Msg.GetOffset() != 20 || req.Msg.GetLimit() != 10 {
					t.Fatalf("ListProjects pagination request = offset %d limit %d", req.Msg.GetOffset(), req.Msg.GetLimit())
				}
				return connect.NewResponse(&agentcomposev2.ListProjectsResponse{
					Projects: []*agentcomposev2.ProjectSummary{{
						ProjectId:       "proj_page",
						Name:            "page",
						SourcePath:      "/path/to/page/agent-compose.yml",
						CurrentRevision: 7,
					}},
					TotalCount: 31,
					HasMore:    true,
					NextOffset: 30,
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("project", "ls", "--host", server.URL, "--limit", "10", "--offset", "20", "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("ls pagination code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeProjectListOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("ls pagination JSON decode failed: %v\n%s", err, stdout)
	}
	if requests != 1 || decoded.TotalCount != 31 || !decoded.HasMore || decoded.NextOffset != 30 || len(decoded.Projects) != 1 {
		t.Fatalf("ls pagination requests/output = %d / %#v", requests, decoded)
	}
}

func decodeComposeUpOutput(t *testing.T, raw string) composeUpOutput {
	t.Helper()
	var decoded composeUpOutput
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode up JSON output: %v\n%s", err, raw)
	}
	return decoded
}

func assertComposeUpChange(t *testing.T, changes []composeUpChangeOutput, action, resourceType, name string) {
	t.Helper()
	for _, change := range changes {
		if change.Action == action && change.ResourceType == resourceType && change.Name == name {
			return
		}
	}
	t.Fatalf("change %s/%s/%s not found in %#v", action, resourceType, name, changes)
}

type projectServiceStub struct {
	applyProject        func(context.Context, *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error)
	getProject          func(context.Context, *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error)
	listProjects        func(context.Context, *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error)
	removeProject       func(context.Context, *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error)
	getScheduler        func(context.Context, *connect.Request[agentcomposev2.GetSchedulerRequest]) (*connect.Response[agentcomposev2.GetSchedulerResponse], error)
	listSchedulerEvents func(context.Context, *connect.Request[agentcomposev2.ListSchedulerEventsRequest]) (*connect.Response[agentcomposev2.ListSchedulerEventsResponse], error)
	runScheduler        func(context.Context, *connect.Request[agentcomposev2.RunSchedulerRequest]) (*connect.Response[agentcomposev2.RunSchedulerResponse], error)
	startSchedulerRun   func(context.Context, *connect.Request[agentcomposev2.StartSchedulerRunRequest]) (*connect.Response[agentcomposev2.StartSchedulerRunResponse], error)
	getSchedulerRun     func(context.Context, *connect.Request[agentcomposev2.GetSchedulerRunRequest]) (*connect.Response[agentcomposev2.GetSchedulerRunResponse], error)
	listSchedulerRuns   func(context.Context, *connect.Request[agentcomposev2.ListSchedulerRunsRequest]) (*connect.Response[agentcomposev2.ListSchedulerRunsResponse], error)
	stopSchedulerRun    func(context.Context, *connect.Request[agentcomposev2.StopSchedulerRunRequest]) (*connect.Response[agentcomposev2.StopSchedulerRunResponse], error)

	agentcomposev2connect.UnimplementedProjectServiceHandler
}

func (s projectServiceStub) ApplyProject(ctx context.Context, req *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
	if s.applyProject == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ApplyProject stub is not configured"))
	}
	return s.applyProject(ctx, req)
}

func (s projectServiceStub) GetProject(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
	if s.getProject == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetProject stub is not configured"))
	}
	return s.getProject(ctx, req)
}

func (s projectServiceStub) ListProjects(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error) {
	if s.listProjects == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListProjects stub is not configured"))
	}
	return s.listProjects(ctx, req)
}

func (s projectServiceStub) RemoveProject(ctx context.Context, req *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
	if s.removeProject == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RemoveProject stub is not configured"))
	}
	return s.removeProject(ctx, req)
}

func TestComposePSAllIncludesEveryStatusOnlyForCurrentProject(t *testing.T) {
	project := testCLIProject("project-1", "project-one", "/work/agent-compose.yml")
	stubs := composeServiceStubs{
		run: runServiceStub{listRuns: func(context.Context, *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{}), nil
		}},
		sandbox: sandboxServiceStub{listSandboxes: func(context.Context, *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: []*agentcomposev2.Sandbox{
				{SandboxId: "sandbox-project-running", Status: "running", Tags: []*agentcomposev2.SandboxTag{{Name: "project_id", Value: "project-1"}}},
				{SandboxId: "sandbox-project-stopped", Status: "stopped", Tags: []*agentcomposev2.SandboxTag{{Name: "project_id", Value: "project-1"}}},
				{SandboxId: "sandbox-other-running", Status: "running", Tags: []*agentcomposev2.SandboxTag{{Name: "project_id", Value: "project-2"}}},
			}}), nil
		}},
	}
	server := newComposeServiceStubServer(t, stubs)
	t.Cleanup(server.Close)
	clients := cliServiceClients{
		run:     agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL),
		sandbox: agentcomposev2connect.NewSandboxServiceClient(server.Client(), server.URL),
	}

	runningOutput, err := composePSOutputFromProject(t.Context(), clients, project, composePSOptions{})
	if err != nil {
		t.Fatalf("build default ps output: %v", err)
	}
	if len(runningOutput.Sandboxes) != 1 || runningOutput.Sandboxes[0].RawID != "sandbox-project-running" {
		t.Fatalf("default ps sandboxes = %#v, want only current project running sandbox", runningOutput.Sandboxes)
	}

	allOutput, err := composePSOutputFromProject(t.Context(), clients, project, composePSOptions{All: true})
	if err != nil {
		t.Fatalf("build ps --all output: %v", err)
	}
	if len(allOutput.Sandboxes) != 2 || allOutput.Sandboxes[0].RawID != "sandbox-project-running" || allOutput.Sandboxes[1].RawID != "sandbox-project-stopped" {
		t.Fatalf("ps --all sandboxes = %#v, want all statuses from current project only", allOutput.Sandboxes)
	}
}

func testCLIProject(projectID, name, sourcePath string) *agentcomposev2.Project {
	return &agentcomposev2.Project{
		Summary: &agentcomposev2.ProjectSummary{
			ProjectId:       projectID,
			Name:            name,
			SourcePath:      sourcePath,
			CurrentRevision: 1,
			SpecHash:        "sha256:test",
			AgentCount:      2,
			SchedulerCount:  1,
		},
		Spec: &agentcomposev2.ProjectSpec{Name: name},
		Agents: []*agentcomposev2.ProjectAgent{
			{
				ProjectId:        projectID,
				AgentName:        "reviewer",
				ManagedAgentId:   "agent-reviewer",
				Provider:         "codex",
				Model:            "gpt-test",
				Image:            "guest:v1",
				Driver:           "boxlite",
				SchedulerEnabled: true,
			},
			{
				ProjectId:      projectID,
				AgentName:      "worker",
				ManagedAgentId: "agent-worker",
				Provider:       "codex",
				Model:          "gpt-worker",
				Image:          "guest:v2",
				Driver:         "boxlite",
			},
		},
		Schedulers: []*agentcomposev2.ProjectScheduler{
			{
				ProjectId:    projectID,
				AgentName:    "reviewer",
				SchedulerId:  "scheduler-reviewer",
				Enabled:      true,
				TriggerCount: 1,
			},
		},
	}
}
