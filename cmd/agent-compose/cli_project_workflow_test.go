package main

import (
	"agent-compose/pkg/identity"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestResolveComposeAgentNameFromCandidates(t *testing.T) {
	firstID := identity.NewID(identity.ResourceAgent, "project", "reviewer")
	secondID := identity.NewID(identity.ResourceAgent, "project", "worker")
	candidates := []composeAgentRefCandidate{
		{Name: "reviewer", ID: firstID, ShortID: identity.ShortID(firstID)},
		{Name: "worker", ID: secondID, ShortID: identity.ShortID(secondID)},
	}

	for _, ref := range []string{"reviewer", firstID, identity.ShortID(firstID), strings.TrimPrefix(firstID, identity.Prefix)[:16]} {
		got, err := resolveComposeAgentNameFromCandidates(ref, candidates)
		if err != nil || got != "reviewer" {
			t.Fatalf("resolve agent ref %q = %q, %v; want reviewer", ref, got, err)
		}
	}

	if _, err := resolveComposeAgentNameFromCandidates("missing", candidates); err == nil {
		t.Fatalf("missing agent ref returned nil error")
	}
	if _, err := resolveComposeAgentNameFromCandidates("123456789abc", []composeAgentRefCandidate{
		{Name: "a", ID: "sha256:123456789abc" + strings.Repeat("0", 52), ShortID: "123456789abc"},
		{Name: "b", ID: "sha256:123456789abc" + strings.Repeat("1", 52), ShortID: "123456789abc"},
	}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous agent ref err = %v", err)
	}
}

func TestUpScriptURLFetchFailureDoesNotApply(t *testing.T) {
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer sourceServer.Close()
	var daemonRequests int
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		daemonRequests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer daemon.Close()
	composePath := writeComposeFile(t, t.TempDir(), fmt.Sprintf(`
name: failed-script-url
agents:
  reviewer:
    scheduler:
      script:
        provider: http
        url: %s/scheduler.js
`, sourceServer.URL))
	_, stderr, _, exitCode := executeCLICommand("up", "--file", composePath, "--host", daemon.URL)
	if exitCode == 0 || !strings.Contains(stderr, "status 503") {
		t.Fatalf("up stderr=%q exit=%d", stderr, exitCode)
	}
	if daemonRequests != 0 {
		t.Fatalf("daemon received %d request(s), want none", daemonRequests)
	}
}

func TestE2ECLIWorkspaceRegistryConfigAndApply(t *testing.T) {
	testCLIWorkspaceRegistryConfigAndApply(t)
}

func testCLIWorkspaceRegistryConfigAndApply(t *testing.T) {
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

	t.Run("single global workspace is not selected implicitly", func(t *testing.T) {
		composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "workspace-default"), `
name: workspace-default
workspaces:
  repo-root:
    provider: file
    path: .
agents:
  reviewer:
    provider: codex
    image: guest:v1
    driver:
      docker: {}
`)

		stdout, stderr, _, exitCode := executeCLICommand("config", "--file", composePath, "--json")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("config code/stderr = %d / %q", exitCode, stderr)
		}
		var decoded struct {
			Workspaces []struct {
				Key      string `json:"key"`
				Provider string `json:"provider"`
				Path     string `json:"path"`
			} `json:"workspaces"`
			Agents []struct {
				Name      string `json:"name"`
				Workspace *struct {
					Provider string `json:"provider"`
					Path     string `json:"path"`
				} `json:"workspace"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatalf("config json decode failed: %v\n%s", err, stdout)
		}
		if len(decoded.Workspaces) != 1 || decoded.Workspaces[0].Key != "repo-root" || decoded.Workspaces[0].Provider != "file" {
			t.Fatalf("decoded workspaces = %#v", decoded.Workspaces)
		}
		if len(decoded.Agents) != 1 || decoded.Agents[0].Workspace != nil {
			t.Fatalf("decoded agents = %#v", decoded.Agents)
		}

		upOut, upErr, _, upCode := executeCLICommand("up", "--file", composePath, "--json")
		if upCode != 0 || upErr != "" {
			t.Fatalf("up code/stderr = %d / %q", upCode, upErr)
		}
		up := decodeComposeUpOutput(t, upOut)
		if up.Project.Name != "workspace-default" || !up.Applied {
			t.Fatalf("up output = %#v", up)
		}
	})

	t.Run("agent workspace name resolves global reference", func(t *testing.T) {
		composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "workspace-reference"), `
name: workspace-reference
workspaces:
  repo-root:
    provider: file
    path: .
  docs-repo:
    provider: git
    url: https://example.test/docs.git
    target: docs
agents:
  reviewer:
    provider: codex
    image: guest:v1
    driver:
      docker: {}
    workspace:
      name: repo-root
`)

		stdout, stderr, _, exitCode := executeCLICommand("config", "--file", composePath, "--json")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("config code/stderr = %d / %q", exitCode, stderr)
		}
		var decoded struct {
			Workspaces []struct {
				Key string `json:"key"`
			} `json:"workspaces"`
			Agents []struct {
				Workspace struct {
					Provider string `json:"provider"`
					Path     string `json:"path"`
					Name     string `json:"name"`
				} `json:"workspace"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatalf("config json decode failed: %v\n%s", err, stdout)
		}
		if len(decoded.Workspaces) != 2 || decoded.Workspaces[0].Key != "docs-repo" || decoded.Workspaces[1].Key != "repo-root" {
			t.Fatalf("decoded workspaces = %#v", decoded.Workspaces)
		}
		if len(decoded.Agents) != 1 || decoded.Agents[0].Workspace.Provider != "file" || decoded.Agents[0].Workspace.Path != "." || decoded.Agents[0].Workspace.Name != "" {
			t.Fatalf("decoded agents = %#v", decoded.Agents)
		}

		upOut, upErr, _, upCode := executeCLICommand("up", "--file", composePath, "--json")
		if upCode != 0 || upErr != "" {
			t.Fatalf("up code/stderr = %d / %q", upCode, upErr)
		}
		up := decodeComposeUpOutput(t, upOut)
		if up.Project.Name != "workspace-reference" || !up.Applied {
			t.Fatalf("up output = %#v", up)
		}
	})

	t.Run("multiple globals are not selected implicitly", func(t *testing.T) {
		composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "workspace-ambiguous"), `
name: workspace-ambiguous
workspaces:
  repo-root:
    provider: file
    path: .
  docs-repo:
    provider: git
    url: https://example.test/docs.git
agents:
  reviewer:
    provider: codex
    image: guest:v1
    driver:
      docker: {}
`)

		stdout, stderr, _, exitCode := executeCLICommand("config", "--file", composePath, "--json")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("config code/stderr = %d / %q", exitCode, stderr)
		}
		var decoded struct {
			Workspaces []struct {
				Key string `json:"key"`
			} `json:"workspaces"`
			Agents []struct {
				Workspace *struct {
					Provider string `json:"provider"`
				} `json:"workspace"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatalf("config json decode failed: %v\n%s", err, stdout)
		}
		if len(decoded.Workspaces) != 2 || decoded.Workspaces[0].Key != "docs-repo" || decoded.Workspaces[1].Key != "repo-root" {
			t.Fatalf("decoded workspaces = %#v", decoded.Workspaces)
		}
		if len(decoded.Agents) != 1 || decoded.Agents[0].Workspace != nil {
			t.Fatalf("decoded agents = %#v", decoded.Agents)
		}

		upOut, upErr, _, upCode := executeCLICommand("up", "--file", composePath, "--json")
		if upCode != 0 || upErr != "" {
			t.Fatalf("up code/stderr = %d / %q", upCode, upErr)
		}
		up := decodeComposeUpOutput(t, upOut)
		if up.Project.Name != "workspace-ambiguous" || !up.Applied {
			t.Fatalf("up output = %#v", up)
		}
	})

	t.Run("missing named workspace fails", func(t *testing.T) {
		composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "workspace-missing-ref"), `
name: workspace-missing-ref
workspaces:
  repo-root:
    provider: file
    path: .
agents:
  reviewer:
    provider: codex
    workspace:
      name: missing
`)

		_, stderr, _, exitCode := executeCLICommand("config", "--file", composePath, "--json")
		if exitCode == 0 {
			t.Fatalf("expected config to fail")
		}
		if !strings.Contains(stderr, `workspace "missing" is not defined`) {
			t.Fatalf("stderr = %q", stderr)
		}
	})
}

func TestCLIDownFirstRepeatedPartialAndJSON(t *testing.T) {
	testCLIDownFirstRepeatedPartialAndJSON(t)
}

func TestE2ECLIDownFirstRepeatedPartialAndJSON(t *testing.T) {
	testCLIDownFirstRepeatedPartialAndJSON(t)
}

func testCLIDownFirstRepeatedPartialAndJSON(t *testing.T) {
	t.Helper()
	composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "cli-down-project"), `
name: cli-down-demo
agents:
  reviewer:
    provider: codex
    model: gpt-test
    image: guest:v1
    scheduler:
      triggers:
        - name: hourly
          cron: "0 * * * *"
          prompt: review hourly
`)
	t.Run("first and repeated text output", func(t *testing.T) {
		callCount := 0
		server := newComposeServiceStubServer(t, composeServiceStubs{
			project: projectServiceStub{
				removeProject: func(_ context.Context, req *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
					callCount++
					if strings.TrimSpace(req.Msg.GetProject().GetProjectId()) == "" {
						t.Fatalf("RemoveProject project id is empty: %#v", req.Msg.GetProject())
					}
					project := testCLIProject("project-down", "cli-down-demo", "compose.yml")
					if callCount == 1 {
						return connect.NewResponse(&agentcomposev2.RemoveProjectResponse{
							Project: project,
							Changes: []*agentcomposev2.ProjectChange{
								{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED, ResourceType: "project", ResourceId: "project-down", Name: "cli-down-demo", Message: "removed by project down"},
								{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED, ResourceType: "project_scheduler", ResourceId: "scheduler-reviewer", Name: "reviewer", Message: "disabled by project down"},
								{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED, ResourceType: "sandbox", ResourceId: "session-1", Name: "reviewer run", Message: "stopped by project down"},
							},
						}), nil
					}
					return connect.NewResponse(&agentcomposev2.RemoveProjectResponse{Project: project}), nil
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("down", "--host", server.URL, "--file", composePath)
		if exitCode != 0 || stderr != "" {
			t.Fatalf("down first code/stderr = %d / %q", exitCode, stderr)
		}
		for _, want := range []string{"ID", "NAME", "TYPE", "ACTION", "MESSAGE", "project-down", "cli-down-demo", "removed", "trigger", "hourly", "session-1", "stopped by project down"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("down first stdout %q does not contain %q", stdout, want)
			}
		}
		for _, unwanted := range []string{"Project:", "Status:", "Failed sandbox stops:", "project_scheduler", "loader"} {
			if strings.Contains(stdout, unwanted) {
				t.Fatalf("down first stdout %q unexpectedly contains %q", stdout, unwanted)
			}
		}

		repeatedOut, repeatedErr, _, repeatedCode := executeCLICommand("down", "--host", server.URL, "--file", composePath)
		if repeatedCode != 0 || repeatedErr != "" {
			t.Fatalf("down repeated code/stderr = %d / %q", repeatedCode, repeatedErr)
		}
		for _, want := range []string{"ID", "NAME", "TYPE", "ACTION", "project-down", "cli-down-demo", "project", "unchanged"} {
			if !strings.Contains(repeatedOut, want) {
				t.Fatalf("down repeated stdout %q does not contain %q", repeatedOut, want)
			}
		}
		if callCount != 2 {
			t.Fatalf("RemoveProject call count = %d, want 2", callCount)
		}
	})
	t.Run("json output golden", func(t *testing.T) {
		server := newComposeServiceStubServer(t, composeServiceStubs{
			project: projectServiceStub{
				removeProject: func(context.Context, *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
					return connect.NewResponse(&agentcomposev2.RemoveProjectResponse{
						Project: testCLIProject("project-down", "cli-down-demo", "compose.yml"),
						Changes: []*agentcomposev2.ProjectChange{
							{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED, ResourceType: "sandbox", ResourceId: "session-1", Name: "reviewer run", Message: "stopped by project down"},
						},
					}), nil
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("down", "--host", server.URL, "--file", composePath, "--json")
		if exitCode != 0 || stderr != "" {
			t.Fatalf("down --json code/stderr = %d / %q", exitCode, stderr)
		}
		want := "{\n" +
			"  \"project\": {\n" +
			"    \"id\": \"project-down\",\n" +
			"    \"name\": \"cli-down-demo\",\n" +
			"    \"short_id\": \"project-down\",\n" +
			"    \"source_path\": \"compose.yml\",\n" +
			"    \"current_revision\": 1,\n" +
			"    \"spec_hash\": \"sha256:test\",\n" +
			"    \"agent_count\": 2,\n" +
			"    \"scheduler_count\": 1\n" +
			"  },\n" +
			"  \"status\": \"down\",\n" +
			"  \"failed_sandbox_stops\": 0,\n" +
			"  \"changes\": [\n" +
			"    {\n" +
			"      \"action\": \"updated\",\n" +
			"      \"resource_type\": \"sandbox\",\n" +
			"      \"id\": \"session-1\",\n" +
			"      \"short_id\": \"session-1\",\n" +
			"      \"name\": \"reviewer run\",\n" +
			"      \"message\": \"stopped by project down\"\n" +
			"    }\n" +
			"  ]\n" +
			"}\n"
		if stdout != want {
			t.Fatalf("down --json stdout mismatch\nwant:\n%s\ngot:\n%s", want, stdout)
		}
	})
	t.Run("partial failure exit code and stderr", func(t *testing.T) {
		server := newComposeServiceStubServer(t, composeServiceStubs{
			project: projectServiceStub{
				removeProject: func(context.Context, *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
					return connect.NewResponse(&agentcomposev2.RemoveProjectResponse{
						Project: testCLIProject("project-down", "cli-down-demo", "compose.yml"),
						Changes: []*agentcomposev2.ProjectChange{
							{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED, ResourceType: "sandbox", ResourceId: "session-ok", Name: "reviewer ok", Message: "stopped by project down"},
							{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED, ResourceType: "sandbox", ResourceId: "session-failed", Name: "reviewer failed", Message: "failed to stop by project down: forced stop failure"},
						},
					}), nil
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("down", "--host", server.URL, "--file", composePath)
		if exitCode != exitCodeGeneral {
			t.Fatalf("down partial exit code = %d, want %d; stderr=%q", exitCode, exitCodeGeneral, stderr)
		}
		if !strings.Contains(stderr, "completed with 1 sandbox stop failure") {
			t.Fatalf("down partial stderr = %q", stderr)
		}
		for _, want := range []string{"ID", "NAME", "TYPE", "ACTION", "MESSAGE", "session-fail", "forced stop failure"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("down partial stdout %q does not contain %q", stdout, want)
			}
		}
	})
}

func TestJupyterBrowserLocation(t *testing.T) {
	tests := []struct {
		name        string
		notebookURL string
		proxyPath   string
		want        string
	}{
		{name: "exposed notebook URL", notebookURL: "/jupyter/sandbox/lab?token=secret", proxyPath: "/jupyter/sandbox/lab", want: "/jupyter/sandbox/lab?token=secret"},
		{name: "private proxy path", proxyPath: "/jupyter/sandbox/lab", want: "/jupyter/sandbox"},
		{name: "private proxy path trailing slash", proxyPath: "/jupyter/sandbox/lab/", want: "/jupyter/sandbox"},
		{name: "entry path already", proxyPath: "/jupyter/sandbox", want: "/jupyter/sandbox"},
		{name: "lab root has no parent entry", proxyPath: "/lab", want: "/lab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jupyterBrowserLocation(tt.notebookURL, tt.proxyPath); got != tt.want {
				t.Fatalf("jupyterBrowserLocation(%q, %q) = %q, want %q", tt.notebookURL, tt.proxyPath, got, tt.want)
			}
		})
	}
}

func writeComposeFile(t *testing.T, dir string, content string) string {
	return writeComposeFileNamed(t, dir, "agent-compose.yml", content)
}

func writeComposeFileNamed(t *testing.T, dir string, name string, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create compose dir: %v", err)
	}
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(name, "agent-compose.") && !strings.Contains(trimmed, "\nworkspaces:") && !strings.HasPrefix(trimmed, "workspaces:") {
		content = "workspaces:\n  default:\n    provider: file\n    path: .\n" + content
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}
	return path
}
