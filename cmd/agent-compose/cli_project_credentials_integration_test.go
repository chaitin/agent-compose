package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestIntegrationCLIUpAppliesYAMLSourceCredentialsFromProjectEnv(t *testing.T) {
	for _, environment := range []string{"dotenv", "process"} {
		t.Run(environment, func(t *testing.T) {
			testCLIUpSourceCredentials(t, environment)
		})
	}
}

func testCLIUpSourceCredentials(t *testing.T, environment string) {
	t.Helper()
	const script = "function main() {}"
	wantToken := "dotenv-script-token"
	if environment == "process" {
		wantToken = "process-script-token"
		t.Setenv("YAML_SCRIPT_TOKEN", wantToken)
	}
	var sourceRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceRequests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			t.Error("CLI did not resolve the script credential from its environment")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if _, err := fmt.Fprint(w, script); err != nil {
			t.Errorf("write script: %v", err)
		}
	}))
	t.Cleanup(server.Close)
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

	projectDir := t.TempDir()
	composePath := writeComposeFile(t, projectDir, fmt.Sprintf(`
name: yaml-source-credentials
workspaces:
  shared:
    provider: git
    url: https://example.test/shared.git
    username: ${YAML_WORKSPACE_USER}
    password: ${YAML_WORKSPACE_PASSWORD}
agents:
  reviewer:
    provider: codex
    image: guest:v1
    driver:
      docker: {}
    scheduler:
      enabled: false
      script:
        provider: http
        url: %s
        token: ${YAML_SCRIPT_TOKEN}
    workspace:
      provider: git
      url: https://example.test/reviewer.git
      token: ${YAML_WORKSPACE_TOKEN}
    skills:
      - name: private-review
        provider: git
        url: https://example.test/private-review.git
        username: ${YAML_SKILL_USER}
        password: ${YAML_SKILL_PASSWORD}
        token: ${YAML_SKILL_TOKEN}
`, server.URL))
	writeTestFile(t, filepath.Join(projectDir, ".env"), strings.Join([]string{
		"YAML_SCRIPT_TOKEN=dotenv-script-token",
		"YAML_WORKSPACE_USER=workspace-user",
		"YAML_WORKSPACE_PASSWORD=workspace-password",
		"YAML_WORKSPACE_TOKEN=workspace-token",
		"YAML_SKILL_USER=skill-user",
		"YAML_SKILL_PASSWORD=skill-password",
		"YAML_SKILL_TOKEN=skill-token",
		"",
	}, "\n"))

	configOut, configErr, _, configCode := executeCLICommand("config", "--file", composePath, "--json")
	if configCode != 0 || configErr != "" {
		t.Fatalf("config code/stderr = %d / %q", configCode, configErr)
	}
	assertYAMLSourceCredentialsRedacted(t, configOut)

	upOut, upErr, _, upCode := executeCLICommand("up", "--file", composePath, "--json")
	if upCode != 0 || upErr != "" {
		t.Fatalf("up code/stderr = %d / %q", upCode, upErr)
	}
	up := decodeComposeUpOutput(t, upOut)
	if !up.Applied || up.Project.Name != "yaml-source-credentials" {
		t.Fatalf("up output = %#v", up)
	}

	clients, err := newCLIServiceClients(cliOptions{})
	if err != nil {
		t.Fatalf("create daemon clients: %v", err)
	}
	project, err := clients.project.GetProject(t.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project: &agentcomposev2.ProjectRef{
			Selector: &agentcomposev2.ProjectRef_Name{Name: "yaml-source-credentials"},
		},
		IncludeSpec: true,
	}))
	if err != nil {
		t.Fatalf("get applied project: %v", err)
	}
	assertAppliedYAMLSourceCredentialsRedacted(t, project.Msg.GetProject().GetSpec())
	if got := project.Msg.GetProject().GetSpec().GetAgents()[0].GetScheduler(); got.GetScript() != script || got.GetScriptSource() != nil {
		t.Fatalf("up did not submit the resolved script snapshot: %v", got)
	}
	if sourceRequests.Load() != 2 {
		t.Fatalf("script fetches = %d, want config and up CLI fetches only", sourceRequests.Load())
	}
}

func assertYAMLSourceCredentialsRedacted(t *testing.T, output string) {
	t.Helper()
	if got := strings.Count(output, `"********"`); got != 6 {
		t.Fatalf("config redacted credential count = %d, want 6:\n%s", got, output)
	}
	for _, forbidden := range []string{
		"workspace-user", "workspace-password", "workspace-token",
		"skill-user", "skill-password", "skill-token",
		"dotenv-script-token", "process-script-token",
		"${YAML_",
	} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("config output exposed source credential material %q:\n%s", forbidden, output)
		}
	}
}

func assertAppliedYAMLSourceCredentialsRedacted(t *testing.T, spec *agentcomposev2.ProjectSpec) {
	t.Helper()
	if spec == nil || len(spec.GetWorkspaces()) != 1 || len(spec.GetAgents()) != 1 {
		t.Fatalf("applied project spec = %#v", spec)
	}
	shared := spec.GetWorkspaces()[0].GetWorkspace()
	if shared.GetUsername() != "********" || shared.GetPassword() != "********" {
		t.Fatalf("applied shared workspace credentials = %#v", shared)
	}
	agent := spec.GetAgents()[0]
	if agent.GetWorkspace().GetToken() != "********" {
		t.Fatalf("applied agent workspace = %#v", agent.GetWorkspace())
	}
	if len(agent.GetSkills()) != 1 {
		t.Fatalf("applied agent skills = %#v", agent.GetSkills())
	}
	skill := agent.GetSkills()[0]
	if skill.GetUsername() != "********" || skill.GetPassword() != "********" || skill.GetToken() != "********" {
		t.Fatalf("applied skill credentials = %#v", skill)
	}
}
