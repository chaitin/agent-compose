package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// A project that declares a first-party LLM credential is exactly the case the
// credential warnings exist for: the operator should be told what will happen to
// the value, but the project is still deployable. Treating the warning as a
// usage error would make the safe declaration undeployable.
func TestIntegrationCLIUpWarnsAboutADeclaredLLMCredentialWithoutFailing(t *testing.T) {
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
	composePath := writeComposeFile(t, projectDir, `
name: declared-llm-credential
agents:
  reviewer:
    provider: codex
    image: guest:v1
    model: deepseek-flash
    driver:
      docker: {}
    env:
      LLM_API_KEY: declared-in-the-project
      LLM_API_ENDPOINT: https://gateway.example.test
      LLM_API_PROTOCOL: responses
`)

	upOut, upErr, _, upCode := executeCLICommand("up", "--file", composePath, "--json")
	if upCode != 0 {
		t.Fatalf("up exit code = %d, stderr = %q", upCode, upErr)
	}
	if !strings.Contains(upErr, "warning:") || !strings.Contains(upErr, "LLM_API_KEY") {
		t.Fatalf("up stderr = %q, want a warning naming LLM_API_KEY", upErr)
	}
	if !strings.Contains(upErr, "does not reach the sandbox") {
		t.Fatalf("up stderr = %q, want the warning to state the key stays on the daemon", upErr)
	}
	up := decodeComposeUpOutput(t, upOut)
	if !up.Applied || up.Project.Name != "declared-llm-credential" {
		t.Fatalf("up output = %#v", up)
	}
	t.Log("evidence: project up accepted a declared LLM credential, reported the warning on stderr, and kept stdout machine-readable")
}

// An unrecognized credential cannot be proxied, so the warning has to say the
// value reaches the sandbox instead. It still must not block the apply.
func TestIntegrationCLIUpWarnsAboutAnUnrecognizedCredentialWithoutFailing(t *testing.T) {
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
	composePath := writeComposeFile(t, projectDir, `
name: unknown-llm-credential
agents:
  reviewer:
    provider: pi
    image: guest:v1
    model: deepseek-flash
    driver:
      docker: {}
    env:
      MYCORP_API_KEY: local-endpoint-only
`)

	upOut, upErr, _, upCode := executeCLICommand("up", "--file", composePath, "--json")
	if upCode != 0 {
		t.Fatalf("up exit code = %d, stderr = %q", upCode, upErr)
	}
	if !strings.Contains(upErr, "MYCORP_API_KEY") || !strings.Contains(upErr, "can be read there") {
		t.Fatalf("up stderr = %q, want a warning that MYCORP_API_KEY reaches the agent runtime", upErr)
	}
	if up := decodeComposeUpOutput(t, upOut); !up.Applied {
		t.Fatalf("up output = %#v", up)
	}
}

func TestSplitProjectValidationIssuesSeparatesWarningsFromBlockingIssues(t *testing.T) {
	warning := &agentcomposev2.ProjectValidationIssue{
		Severity: agentcomposev2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_WARNING,
		Path:     "agents.reviewer.env.LLM_API_KEY",
		Message:  "declares a first-party credential",
	}
	errorIssue := &agentcomposev2.ProjectValidationIssue{
		Severity: agentcomposev2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_ERROR,
		Path:     "agents.reviewer",
		Message:  "provider is required",
	}
	// An issue that never set a severity is the shape every validation error has,
	// so it must stay blocking rather than be silently downgraded.
	unspecified := &agentcomposev2.ProjectValidationIssue{Path: "spec", Message: "project spec is required"}

	blocking, warnings := splitProjectValidationIssues([]*agentcomposev2.ProjectValidationIssue{warning, errorIssue, unspecified})
	if len(blocking) != 2 || blocking[0] != errorIssue || blocking[1] != unspecified {
		t.Fatalf("blocking = %#v, want the error and the unspecified issue", blocking)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "agents.reviewer.env.LLM_API_KEY") || !strings.Contains(warnings[0], "declares a first-party credential") {
		t.Fatalf("warnings = %#v, want the rendered warning", warnings)
	}
}
