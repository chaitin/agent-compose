package main

import (
	"bytes"
	"connectrpc.com/connect"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"agent-compose/pkg/config"
	"agent-compose/pkg/identity"
	"agent-compose/pkg/imagecache"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"agent-compose/proto/health/v1/healthv1connect"
	"github.com/joho/godotenv"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestMain(m *testing.M) {
	clearDaemonTestEnv()
	os.Exit(m.Run())
}

func clearDaemonTestEnv() {
	envFile, err := findDaemonTestEnvFile()
	if err != nil {
		return
	}
	values, err := godotenv.Read(envFile)
	if err != nil {
		return
	}
	for key := range values {
		_ = os.Unsetenv(key)
	}
}

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

func findDaemonTestEnvFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, ".env")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func executeCommand(args ...string) (string, string, int, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runCount := 0
	cmd := newRootCommand(&stdout, &stderr, func(context.Context) error {
		runCount++
		return nil
	})
	if args == nil {
		args = []string{}
	}
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), runCount, err
}

func executeCLICommand(args ...string) (string, string, int, int) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runCount := 0
	exitCode := executeCLI(context.Background(), &stdout, &stderr, args, func(context.Context) error {
		runCount++
		return nil
	})
	return stdout.String(), stderr.String(), runCount, exitCode
}

func executeCLICommandWithInput(input string, args ...string) (string, string, int, int) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runCount := 0
	cmd := newRootCommand(&stdout, &stderr, func(context.Context) error {
		runCount++
		return nil
	})
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	exitCode := 0
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		_, _ = fmt.Fprintln(&stderr, err)
		exitCode = commandExitCode(err)
	}
	return stdout.String(), stderr.String(), runCount, exitCode
}

func TestVersionCommandPrintsBuildVersionWithoutStartingDaemon(t *testing.T) {
	oldVersion := config.BuildVersion
	config.BuildVersion = "test-version"
	t.Cleanup(func() { config.BuildVersion = oldVersion })

	stdout, stderr, runCount, err := executeCommand("version")
	if err != nil {
		t.Fatalf("version command returned error: %v", err)
	}
	if stdout != "test-version\n" {
		t.Fatalf("version stdout = %q, want %q", stdout, "test-version\n")
	}
	if stderr != "" {
		t.Fatalf("version stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestDaemonHelpDoesNotStartDaemon(t *testing.T) {
	stdout, stderr, runCount, err := executeCommand("daemon", "--help")
	if err != nil {
		t.Fatalf("daemon --help returned error: %v", err)
	}
	if !strings.Contains(stdout, "Start the agent-compose daemon") || !strings.Contains(stdout, "Usage:") {
		t.Fatalf("daemon --help stdout missing expected help text: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("daemon --help stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestRunHelpHidesOptionalModeFlagSentinel(t *testing.T) {
	stdout, stderr, runCount, exitCode := executeCLICommand("run", "--help")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run --help code/stderr = %d / %q", exitCode, stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	for _, unexpected := range []string{
		"agent-compose-run-mode",
		"\x00",
		`[="`,
	} {
		if strings.Contains(stdout, unexpected) {
			t.Fatalf("run --help contains %q:\n%s", unexpected, stdout)
		}
	}
	for _, want := range []string{
		"--prompt string",
		"--command string",
		"Prompt to send to the agent",
		"Bash command to execute in the agent sandbox",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("run --help does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestCLIHelpUsesSandboxTerminology(t *testing.T) {
	commands := [][]string{
		{"--help"},
		{"run", "--help"},
		{"logs", "--help"},
		{"exec", "--help"},
		{"inspect", "--help"},
		{"cache", "ls", "--help"},
		{"cache", "prune", "--help"},
		{"volume", "ls", "--help"},
		{"volume", "prune", "--help"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != 0 || stderr != "" {
				t.Fatalf("%v code/stderr = %d / %q", args, exitCode, stderr)
			}
			if strings.Contains(strings.ToLower(stdout), "session") {
				t.Fatalf("%v help contains session terminology:\n%s", args, stdout)
			}
		})
	}
}

func TestUnknownCommandFailsWithoutStartingDaemon(t *testing.T) {
	_, _, runCount, err := executeCommand("does-not-exist")
	if err == nil {
		t.Fatal("unknown command returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unknown command error = %q, want unknown command", err.Error())
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestRootCommandPrintsHelpWithoutStartingDaemon(t *testing.T) {
	stdout, stderr, runCount, err := executeCommand()
	if err != nil {
		t.Fatalf("root command returned error: %v", err)
	}
	if !strings.Contains(stdout, "agent-compose daemon and CLI") || !strings.Contains(stdout, "Usage:") {
		t.Fatalf("root command stdout missing expected help text: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("root command stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestDaemonCommandStartsDaemon(t *testing.T) {
	_, _, runCount, err := executeCommand("daemon")
	if err != nil {
		t.Fatalf("daemon command returned error: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("daemon command called daemon runner %d times, want 1", runCount)
	}
}

func TestCLIClientConfigPriority(t *testing.T) {
	testCLIClientConfigPriority(t)
}

func TestResolveAgentComposeSocketForCLIFallsBackToVarRun(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	socketPath, err := resolveAgentComposeSocketForCLI("")
	if err != nil {
		t.Fatalf("resolveAgentComposeSocketForCLI returned error: %v", err)
	}
	if socketPath != config.DefaultAgentComposeSocketPath {
		t.Fatalf("socketPath = %q, want %q", socketPath, config.DefaultAgentComposeSocketPath)
	}
}

func testCLIClientConfigPriority(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	socketPath := filepath.Join(root, "agent-compose.sock")
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "https://env.example")

	clientConfig, err := resolveCLIClientConfig("https://flag.example/")
	if err != nil {
		t.Fatalf("resolveCLIClientConfig returned error: %v", err)
	}
	if clientConfig.Source != "--host" || clientConfig.BaseURL != "https://flag.example" || clientConfig.UseUnixSocket {
		t.Fatalf("flag client config = %#v", clientConfig)
	}

	clientConfig, err = resolveCLIClientConfig("")
	if err != nil {
		t.Fatalf("resolveCLIClientConfig returned error: %v", err)
	}
	if clientConfig.Source != "AGENT_COMPOSE_HOST" || clientConfig.BaseURL != "https://env.example" || clientConfig.UseUnixSocket {
		t.Fatalf("env client config = %#v", clientConfig)
	}

	t.Setenv("AGENT_COMPOSE_HOST", "")
	clientConfig, err = resolveCLIClientConfig("")
	if err != nil {
		t.Fatalf("resolveCLIClientConfig returned error: %v", err)
	}
	if clientConfig.Source != "AGENT_COMPOSE_SOCKET" || clientConfig.SocketPath != socketPath || !clientConfig.UseUnixSocket {
		t.Fatalf("socket client config = %#v", clientConfig)
	}
}

func TestCLIClientConfigRemoteAuthFromEnvironment(t *testing.T) {
	t.Setenv("AUTH_USERNAME", "reviewer")
	t.Setenv("AUTH_PASSWORD", "secret")
	clientConfig, err := resolveCLIClientConfig("https://flag.example")
	if err != nil {
		t.Fatalf("resolveCLIClientConfig returned error: %v", err)
	}
	if clientConfig.AuthUsername != "reviewer" || clientConfig.AuthPassword != "secret" {
		t.Fatalf("remote auth config = %#v", clientConfig)
	}

	t.Setenv("AGENT_COMPOSE_HOST", "")
	socketPath := filepath.Join(t.TempDir(), "agent-compose.sock")
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	clientConfig, err = resolveCLIClientConfig("")
	if err != nil {
		t.Fatalf("resolveCLIClientConfig returned error: %v", err)
	}
	if clientConfig.AuthUsername != "" || clientConfig.AuthPassword != "" {
		t.Fatalf("unix socket auth config = %#v, want empty auth", clientConfig)
	}
}

func TestCLIClientConfigRejectsInvalidHost(t *testing.T) {
	testCLIClientConfigRejectsInvalidHost(t)
}

func testCLIClientConfigRejectsInvalidHost(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name     string
		hostFlag string
		envHost  string
		want     string
	}{
		{name: "flag missing scheme", hostFlag: "127.0.0.1:7410", want: "--host"},
		{name: "env missing host", envHost: "https://", want: "AGENT_COMPOSE_HOST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENT_COMPOSE_HOST", tc.envHost)
			_, err := resolveCLIClientConfig(tc.hostFlag)
			if err == nil {
				t.Fatal("resolveCLIClientConfig returned nil error, want invalid host error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestStatusCommandUsesHostFlagBeforeEnvironment(t *testing.T) {
	testStatusCommandUsesHostFlagBeforeEnvironment(t)
}

func testStatusCommandUsesHostFlagBeforeEnvironment(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("request path = %q, want /api/version", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"err":null,"msg":"OK","data":{"timestamp":1783501631.2438176,"timezone":"CST","timezone_offset":28800,"version":"flag"}}`)
	}))
	defer server.Close()
	t.Setenv("AGENT_COMPOSE_HOST", "http://127.0.0.1:1")

	stdout, stderr, runCount, err := executeCommand("status", "--host", server.URL)
	if err != nil {
		t.Fatalf("status command returned error: %v", err)
	}
	for _, want := range []string{"STATUS", "UPTIME", "VERSION", "OK", "2026-07-08 17:07:11 CST +0800", "flag"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status stdout = %q, want %q", stdout, want)
		}
	}
	if strings.Contains(stdout, `"version"`) {
		t.Fatalf("status stdout = %q, want text output", stdout)
	}
	if stderr != "" {
		t.Fatalf("status stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestStatusCommandJSONPrintsRawDaemonResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("request path = %q, want /api/version", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"err":null,"msg":"OK","data":{"timestamp":1783501631.2438176,"version":"json"}}`)
	}))
	defer server.Close()

	stdout, stderr, runCount, err := executeCommand("status", "--json", "--host", server.URL)
	if err != nil {
		t.Fatalf("status --json command returned error: %v", err)
	}
	if strings.TrimSpace(stdout) != `{"err":null,"msg":"OK","data":{"timestamp":1783501631.2438176,"version":"json"}}` {
		t.Fatalf("status --json stdout = %q, want raw daemon response", stdout)
	}
	if stderr != "" {
		t.Fatalf("status --json stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestStatusCommandUsesRemoteAuthFromEnvironment(t *testing.T) {
	t.Setenv("AUTH_USERNAME", "reviewer")
	t.Setenv("AUTH_PASSWORD", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "reviewer" || password != "secret" {
			t.Fatalf("BasicAuth = %q/%q/%v", username, password, ok)
		}
		_, _ = w.Write([]byte(`{"err":null,"msg":"OK","data":{"timestamp":1783501631.2438176,"timezone":"CST","timezone_offset":28800,"version":"test"}}`))
	}))
	defer server.Close()

	stdout, stderr, runCount, exitCode := executeCLICommand("status", "--host", server.URL)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("status auth code/stderr = %d / %q", exitCode, stderr)
	}
	for _, want := range []string{"STATUS", "UPTIME", "VERSION", "OK", "2026-07-08 17:07:11 CST +0800", "test"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status auth stdout = %q, want %q", stdout, want)
		}
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestStatusCommandUsesEnvironmentHost(t *testing.T) {
	testStatusCommandUsesEnvironmentHost(t)
}

func testStatusCommandUsesEnvironmentHost(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("request path = %q, want /api/version", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"err":null,"msg":"OK","data":{"timestamp":1783501631.2438176,"timezone":"CST","timezone_offset":28800,"version":"env"}}`)
	}))
	defer server.Close()
	t.Setenv("AGENT_COMPOSE_HOST", server.URL)

	stdout, _, runCount, err := executeCommand("status")
	if err != nil {
		t.Fatalf("status command returned error: %v", err)
	}
	for _, want := range []string{"STATUS", "UPTIME", "VERSION", "OK", "2026-07-08 17:07:11 CST +0800", "env"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status stdout = %q, want %q", stdout, want)
		}
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestStatusCommandUsesDefaultUnixSocket(t *testing.T) {
	testStatusCommandUsesDefaultUnixSocket(t)
}

func testStatusCommandUsesDefaultUnixSocket(t *testing.T) {
	t.Helper()
	runtimeDir := shortUnixSocketDir(t)
	socketPath := filepath.Join(runtimeDir, "agent-compose.sock")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("AGENT_COMPOSE_SOCKET", "")
	t.Setenv("AGENT_COMPOSE_HOST", "")

	app, cancel := newTestDaemonApp(t, "", nil)
	defer cancel()
	runCtx, stop := context.WithCancel(context.Background())
	errCh := runDaemonAppAsync(app, runCtx)
	waitForHTTPStatus(t, newUnixHTTPClient(socketPath), "http://agent-compose/api/version", http.StatusOK)

	stdout, stderr, runCount, err := executeCommand("status")
	stop()
	waitForDaemonExit(t, errCh)
	if err != nil {
		t.Fatalf("status command returned error: %v", err)
	}
	for _, want := range []string{"STATUS", "UPTIME", "VERSION", "OK", config.BuildVersion} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status stdout = %q, want %q", stdout, want)
		}
	}
	if stderr != "" {
		t.Fatalf("status stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestFormatDaemonStatusTime(t *testing.T) {
	offset := 8 * 60 * 60
	if got := formatDaemonStatusTime(1783501631.2438176, "CST", &offset); got != "2026-07-08 17:07:11 CST +0800" {
		t.Fatalf("formatDaemonStatusTime() = %q, want server timezone time", got)
	}
	if got := formatDaemonStatusTime(1783501631.2438176, "", nil); got != "2026-07-08 09:07:11 UTC +0000" {
		t.Fatalf("formatDaemonStatusTime() = %q, want legacy UTC fallback time", got)
	}
}

func TestStatusCommandReportsUnreadableDaemon(t *testing.T) {
	testStatusCommandReportsUnreadableDaemon(t)
}

func testStatusCommandReportsUnreadableDaemon(t *testing.T) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "missing.sock")
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "")

	_, _, runCount, err := executeCommand("status")
	if err == nil {
		t.Fatal("status command returned nil error, want daemon connection error")
	}
	for _, part := range []string{"connect daemon via AGENT_COMPOSE_SOCKET", socketPath} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q does not contain %q", err.Error(), part)
		}
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestConfigCommandPrintsNormalizedYAMLWithoutStartingDaemon(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "review-project")
	writeComposeFile(t, dir, `
workspaces:
  default:
    provider: local
    path: .
variables:
  API_KEY:
    value: ${API_KEY}
    secret: true
  PUBLIC: visible
agents:
  reviewer:
    provider: codex
    env:
      MODE: strict
`)
	withWorkingDir(t, dir)
	t.Setenv("API_KEY", "sk-secret")
	t.Setenv("AGENT_COMPOSE_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("AGENT_COMPOSE_HOST", "")

	stdout, stderr, runCount, err := executeCommand("config")
	if err != nil {
		t.Fatalf("config command returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("config stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	for _, want := range []string{"name: review-project", "variables:", "agents:", "network:", "mode: default", "secret: true", "********", "visible", "strict"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("config YAML missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "sk-secret") {
		t.Fatalf("config YAML leaked secret value:\n%s", stdout)
	}
}

func TestConfigCommandPrintsNormalizedJSONWithoutStartingDaemon(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "json-project")
	writeComposeFile(t, dir, `
name: json-project
variables:
  TOKEN:
    value: ${TOKEN}
    secret: true
workspaces:
  default:
    provider: local
    path: .
agents:
  reviewer:
    provider: codex
`)
	withWorkingDir(t, dir)
	t.Setenv("TOKEN", "secret-token")
	t.Setenv("AGENT_COMPOSE_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv("AGENT_COMPOSE_HOST", "")

	stdout, stderr, runCount, err := executeCommand("config", "--json")
	if err != nil {
		t.Fatalf("config --json returned error: %v", err)
	}
	if stderr != "" {
		t.Fatalf("config --json stderr = %q, want empty", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	if strings.Contains(stdout, "secret-token") {
		t.Fatalf("config JSON leaked secret value: %s", stdout)
	}
	var decoded struct {
		Name      string `json:"name"`
		Variables []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Secret bool   `json:"secret"`
		} `json:"variables"`
		Agents []struct {
			Name   string `json:"name"`
			Driver struct {
				Name string `json:"name"`
			} `json:"driver"`
		} `json:"agents"`
		Network struct {
			Mode string `json:"mode"`
		} `json:"network"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("config --json output is not JSON: %v\n%s", err, stdout)
	}
	if decoded.Name != "json-project" || decoded.Network.Mode != "default" {
		t.Fatalf("decoded config = %#v", decoded)
	}
	if len(decoded.Variables) != 1 || decoded.Variables[0].Name != "TOKEN" || decoded.Variables[0].Value != "********" || !decoded.Variables[0].Secret {
		t.Fatalf("decoded variables = %#v", decoded.Variables)
	}
	if len(decoded.Agents) != 1 || decoded.Agents[0].Name != "reviewer" || decoded.Agents[0].Driver.Name != "docker" {
		t.Fatalf("decoded agents = %#v", decoded.Agents)
	}
}

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

func TestStatusCommandUnavailableWritesStderrAndExitCode(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "missing.sock")
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "")

	stdout, stderr, runCount, exitCode := executeCLICommand("status")
	if exitCode != exitCodeUnavailable {
		t.Fatalf("status unavailable exit code = %d, want %d", exitCode, exitCodeUnavailable)
	}
	if stdout != "" {
		t.Fatalf("status unavailable stdout = %q, want empty", stdout)
	}
	for _, want := range []string{"connect daemon via AGENT_COMPOSE_SOCKET", socketPath} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("status unavailable stderr %q does not contain %q", stderr, want)
		}
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestCommandExitErrorForConnectClassifiesRPCFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
	}{
		{name: "invalid argument", err: connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("bad request")), code: exitCodeUsage},
		{name: "not found", err: connect.NewError(connect.CodeNotFound, fmt.Errorf("missing")), code: exitCodeUsage},
		{name: "failed precondition", err: connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("stopped")), code: exitCodeUsage},
		{name: "unavailable", err: connect.NewError(connect.CodeUnavailable, fmt.Errorf("daemon down")), code: exitCodeUnavailable},
		{name: "unsupported", err: connect.NewError(connect.CodeUnimplemented, fmt.Errorf("stats unsupported")), code: exitCodeUnsupported},
		{name: "ordinary failure", err: connect.NewError(connect.CodeInternal, fmt.Errorf("boom")), code: exitCodeGeneral},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := commandExitErrorForConnect(fmt.Errorf("operation: %w", tc.err))
			if got := commandExitCode(err); got != tc.code {
				t.Fatalf("exit code = %d, want %d; err=%v", got, tc.code, err)
			}
		})
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

			stdout, stderr, _, exitCode := executeCLICommand("ls", "--host", server.URL)
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

func TestInvalidHostWritesStderrAndUsageExitCode(t *testing.T) {
	stdout, stderr, runCount, exitCode := executeCLICommand("status", "--host", "127.0.0.1:7410")
	if exitCode != exitCodeUsage {
		t.Fatalf("invalid host exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("invalid host stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--host") || !strings.Contains(stderr, "127.0.0.1:7410") {
		t.Fatalf("invalid host stderr = %q", stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
}

func TestIntegrationCLIUpAppliesProjectFirstRepeatedModifiedAndJSON(t *testing.T) {
	testCLIUpAppliesProjectFirstRepeatedModifiedAndJSON(t)
}

func TestIntegrationCLIWorkspaceRegistryConfigAndApply(t *testing.T) {
	testCLIWorkspaceRegistryConfigAndApply(t)
}

func TestE2ECLIWorkspaceRegistryConfigAndApply(t *testing.T) {
	testCLIWorkspaceRegistryConfigAndApply(t)
}

func TestIntegrationCLIUpAppliesInlineSchedulerScriptAndPSJSON(t *testing.T) {
	composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "cli-inline-project"), inlineSchedulerComposeYAML("cli-inline-demo", 60000))
	configOut, configErr, configRunCount, err := executeCommand("config", "--file", composePath)
	if err != nil {
		t.Fatalf("config inline returned error: %v", err)
	}
	if configErr != "" {
		t.Fatalf("config inline stderr = %q, want empty", configErr)
	}
	if configRunCount != 0 {
		t.Fatalf("config inline daemon runner called %d times, want 0", configRunCount)
	}
	if !strings.Contains(configOut, "script:") || !strings.Contains(configOut, `scheduler.interval("interval-review"`) {
		t.Fatalf("config inline output missing scheduler script:\n%s", configOut)
	}

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

	firstOut, firstErr, _, firstCode := executeCLICommand("up", "--file", composePath)
	if firstCode != 0 {
		t.Fatalf("up inline first exit code = %d, stderr=%q", firstCode, firstErr)
	}
	if firstErr != "" {
		t.Fatalf("up inline first stderr = %q, want empty", firstErr)
	}
	for _, want := range []string{"ID", "NAME", "TYPE", "ACTION", "trigger"} {
		if !strings.Contains(firstOut, want) {
			t.Fatalf("up inline first stdout %q does not contain %q", firstOut, want)
		}
	}

	repeatedOut, repeatedErr, _, repeatedCode := executeCLICommand("up", "--file", composePath, "--json")
	if repeatedCode != 0 {
		t.Fatalf("up inline repeated exit code = %d, stderr=%q", repeatedCode, repeatedErr)
	}
	if repeatedErr != "" {
		t.Fatalf("up inline repeated stderr = %q, want empty", repeatedErr)
	}
	repeated := decodeComposeUpOutput(t, repeatedOut)
	if repeated.Project.Name != "cli-inline-demo" || repeated.Project.CurrentRevision != 1 || repeated.Project.SchedulerCount != 1 {
		t.Fatalf("up inline repeated project = %#v", repeated.Project)
	}
	if !repeated.Applied || !repeated.Unchanged || repeated.Revision.Revision != 1 {
		t.Fatalf("up inline repeated state = applied %v unchanged %v revision %#v", repeated.Applied, repeated.Unchanged, repeated.Revision)
	}
	assertComposeUpChange(t, repeated.Changes, "unchanged", "project_scheduler", "reviewer")

	psOut, psErr, _, psCode := executeCLICommand("ps", "--file", composePath, "--json")
	if psCode != 0 {
		t.Fatalf("ps inline exit code = %d, stderr=%q", psCode, psErr)
	}
	if psErr != "" {
		t.Fatalf("ps inline stderr = %q, want empty", psErr)
	}
	var psDecoded composePSOutput
	if err := json.Unmarshal([]byte(psOut), &psDecoded); err != nil {
		t.Fatalf("ps inline JSON decode failed: %v\n%s", err, psOut)
	}
	if psDecoded.Project.Name != "cli-inline-demo" || len(psDecoded.Sandboxes) != 0 {
		t.Fatalf("ps inline project/sandboxes = %#v", psDecoded)
	}
}

func testCLIUpAppliesProjectFirstRepeatedModifiedAndJSON(t *testing.T) {
	t.Helper()
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
    image: guest:v1
    driver:
      boxlite: {}
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
    image: guest:v1
    driver:
      boxlite: {}
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

func testCLIWorkspaceRegistryConfigAndApply(t *testing.T) {
	t.Helper()
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

	t.Run("single global workspace becomes default", func(t *testing.T) {
		composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "workspace-default"), `
name: workspace-default
workspaces:
  repo-root:
    provider: local
    path: .
agents:
  reviewer:
    provider: codex
    image: guest:v1
    driver:
      boxlite: {}
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
				Workspace struct {
					Provider string `json:"provider"`
					Path     string `json:"path"`
				} `json:"workspace"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatalf("config json decode failed: %v\n%s", err, stdout)
		}
		if len(decoded.Workspaces) != 1 || decoded.Workspaces[0].Key != "repo-root" || decoded.Workspaces[0].Provider != "local" {
			t.Fatalf("decoded workspaces = %#v", decoded.Workspaces)
		}
		if len(decoded.Agents) != 1 || decoded.Agents[0].Workspace.Provider != "local" || decoded.Agents[0].Workspace.Path != "." {
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
    provider: local
    path: .
  docs-repo:
    provider: git
    url: https://example.test/docs.git
    path: docs
agents:
  reviewer:
    provider: codex
    image: guest:v1
    driver:
      boxlite: {}
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
		if len(decoded.Agents) != 1 || decoded.Agents[0].Workspace.Provider != "local" || decoded.Agents[0].Workspace.Path != "." || decoded.Agents[0].Workspace.Name != "" {
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

	t.Run("multiple globals without agent workspace fails", func(t *testing.T) {
		composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "workspace-ambiguous"), `
name: workspace-ambiguous
workspaces:
  repo-root:
    provider: local
    path: .
  docs-repo:
    provider: git
    url: https://example.test/docs.git
agents:
  reviewer:
    provider: codex
`)

		_, stderr, _, exitCode := executeCLICommand("config", "--file", composePath, "--json")
		if exitCode == 0 {
			t.Fatalf("expected config to fail")
		}
		if !strings.Contains(stderr, "project workspaces has multiple entries") {
			t.Fatalf("stderr = %q", stderr)
		}
	})

	t.Run("missing named workspace fails", func(t *testing.T) {
		composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "workspace-missing-ref"), `
name: workspace-missing-ref
workspaces:
  repo-root:
    provider: local
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

func TestIntegrationCLIDownFirstRepeatedPartialAndJSON(t *testing.T) {
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
								{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED, ResourceType: "session", ResourceId: "session-1", Name: "reviewer run", Message: "stopped by project down"},
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
							{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED, ResourceType: "session", ResourceId: "session-1", Name: "reviewer run", Message: "stopped by project down"},
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
			"  \"failed_session_stops\": 0,\n" +
			"  \"changes\": [\n" +
			"    {\n" +
			"      \"action\": \"updated\",\n" +
			"      \"resource_type\": \"session\",\n" +
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
							{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED, ResourceType: "session", ResourceId: "session-ok", Name: "reviewer ok", Message: "stopped by project down"},
							{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED, ResourceType: "session", ResourceId: "session-failed", Name: "reviewer failed", Message: "failed to stop by project down: forced stop failure"},
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

func TestIntegrationCLIRunStreamsOutputAndSupportsSandboxReuse(t *testing.T) {
	dir := t.TempDir()
	composePath := writeComposeFile(t, dir, `
name: cli-run-demo
agents:
  reviewer:
    provider: codex
`)
	var sawRequest bool
	server := newRunServiceStubServer(t, runServiceStub{
		runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
			sawRequest = true
			if req.Msg.GetAgentName() != "reviewer" || req.Msg.GetPrompt() != "check this" || req.Msg.GetSessionId() != "session-reuse" || req.Msg.GetTriggerId() != "" {
				t.Fatalf("RunAgentStream request = %#v", req.Msg)
			}
			if req.Msg.GetSource() != agentcomposev2.RunSource_RUN_SOURCE_MANUAL || req.Msg.GetCleanupPolicy() != agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING {
				t.Fatalf("RunAgentStream source/cleanup = %#v", req.Msg)
			}
			if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_STARTED,
				RunId:     "run-success",
			}); err != nil {
				return err
			}
			if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType:  agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
				RunId:      "run-success",
				Transcript: &agentcomposev2.TranscriptEvent{Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDOUT, Text: "live output\n"},
			}); err != nil {
				return err
			}
			return stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
				RunId:     "run-success",
				Run: &agentcomposev2.RunSummary{
					RunId:     "run-success",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
					SessionId: "session-reuse",
				},
			})
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-success", "reviewer", "session-reuse", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "live output\n")}), nil
		},
	})
	defer server.Close()

	stdout, stderr, runCount, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--sandbox-id", "session-reuse", "--keep-running", "reviewer", "--prompt", "check this")
	if exitCode != 0 {
		t.Fatalf("run success exit code = %d, stderr=%q", exitCode, stderr)
	}
	if stdout != "live output\n" || stderr != "" {
		t.Fatalf("run success stdout/stderr = %q / %q", stdout, stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	if !sawRequest {
		t.Fatal("RunAgentStream was not called")
	}

	for _, tc := range []struct {
		name string
		flag string
	}{
		{name: "legacy sandbox flag", flag: "--sandbox"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacyOut, legacyErr, _, legacyCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, tc.flag, "session-reuse", "--keep-running", "reviewer", "--prompt", "check this")
			if legacyCode != exitCodeUsage {
				t.Fatalf("run %s exit code = %d, want %d; stderr=%q", tc.flag, legacyCode, exitCodeUsage, legacyErr)
			}
			if legacyOut != "" || !strings.Contains(legacyErr, "unknown flag: "+tc.flag) {
				t.Fatalf("run %s stdout/stderr = %q / %q", tc.flag, legacyOut, legacyErr)
			}
		})
	}
}

func TestIntegrationCLIRunDetachStartsBackgroundRun(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-detach
agents:
  reviewer:
    provider: codex
`)
	var sawStart bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			startRun: func(ctx context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
				sawStart = true
				runReq := req.Msg.GetRun()
				if runReq.GetAgentName() != "reviewer" || runReq.GetCommand() != "echo detached" || runReq.GetSessionId() != "" || runReq.GetDriver() != "microsandbox" {
					t.Fatalf("StartRun request = %#v", runReq)
				}
				if runReq.GetSource() != agentcomposev2.RunSource_RUN_SOURCE_MANUAL {
					t.Fatalf("StartRun source = %#v", runReq.GetSource())
				}
				return connect.NewResponse(&agentcomposev2.StartRunResponse{
					Run: &agentcomposev2.RunSummary{
						RunId:       "run-detached",
						ProjectId:   runReq.GetProjectId(),
						ProjectName: "cli-run-detach",
						AgentName:   "reviewer",
						Status:      agentcomposev2.RunStatus_RUN_STATUS_PENDING,
						SessionId:   "sandbox-detached",
						Source:      agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
					},
					Warnings: []string{"detached warning"},
					Started:  true,
				}), nil
			},
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				t.Fatalf("RunAgentStream should not be called for detached run")
				return nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "-d", "--host", server.URL, "--file", composePath, "--driver", "msb", "reviewer", "--command", "echo detached")
	if exitCode != 0 {
		t.Fatalf("run -d exit code = %d, stderr=%q", exitCode, stderr)
	}
	for _, want := range []string{
		"Run: run-detached",
		"Sandbox: sandbox-detached",
		"Status: pending",
		"Logs: agent-compose --host " + server.URL + " --file " + composePath + " logs --run-id run-detached --follow",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("run -d stdout %q does not contain %q", stdout, want)
		}
	}
	if !strings.Contains(stderr, "warning: detached warning") {
		t.Fatalf("run -d stderr = %q", stderr)
	}
	if !sawStart {
		t.Fatal("StartRun was not called")
	}
}

func TestIntegrationCLIRunDetachJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-detach-json
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			startRun: func(ctx context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
				runReq := req.Msg.GetRun()
				return connect.NewResponse(&agentcomposev2.StartRunResponse{
					Run: &agentcomposev2.RunSummary{
						RunId:       "run-detached-json",
						ProjectId:   runReq.GetProjectId(),
						ProjectName: "cli-run-detach-json",
						AgentName:   "reviewer",
						Status:      agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
						SessionId:   "sandbox-json",
						Source:      agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
						Warnings:    []string{"summary warning"},
					},
					Warnings: []string{"response warning"},
					Started:  true,
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "-d", "--json", "--host", server.URL, "--file", composePath, "reviewer", "--prompt", "inspect")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -d --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeRunOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode run -d JSON: %v\n%s", err, stdout)
	}
	if decoded.ID != "run-detached-json" || decoded.SandboxID != "sandbox-json" || decoded.Status != "running" {
		t.Fatalf("run -d JSON decoded = %#v", decoded)
	}
	if !strings.Contains(decoded.LogsCommand, "logs --run-id run-detached-json --follow") || len(decoded.Warnings) != 2 {
		t.Fatalf("run -d JSON logs/warnings = %#v", decoded)
	}
}

func TestIntegrationCLIRunDetachCommandCanBeFollowedByLogs(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-detach-logs
agents:
  reviewer:
    provider: codex
`)
	var sawCommand bool
	var sawFollow bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			startRun: func(ctx context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
				if req.Msg.GetRun().GetCommand() != "printf detached" {
					t.Fatalf("StartRun command = %#v", req.Msg.GetRun())
				}
				sawCommand = true
				return connect.NewResponse(&agentcomposev2.StartRunResponse{Run: &agentcomposev2.RunSummary{
					RunId:     "run-detached-logs",
					ProjectId: req.Msg.GetRun().GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
					SessionId: "sandbox-detached-logs",
					Source:    agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
				}, Started: true}), nil
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-detached-logs", "reviewer", "sandbox-detached-logs", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "")}), nil
			},
			followRunLogs: func(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
				sawFollow = true
				if req.Msg.GetRunId() != "run-detached-logs" {
					t.Fatalf("FollowRunLogs request = %#v", req.Msg)
				}
				return stream.Send(&agentcomposev2.RunLogChunk{
					Data: "detached output\n",
				})
			},
		},
	})
	defer server.Close()

	_, stderr, _, exitCode := executeCLICommand("run", "-d", "--host", server.URL, "--file", composePath, "reviewer", "--command", "printf detached")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -d --command code/stderr = %d / %q", exitCode, stderr)
	}
	logOut, logErr, _, logCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--run-id", "run-detached-logs", "--follow")
	runPrefix := "reviewer-run-detached-log | "
	wantLogOut := expectedLogSeparator(runPrefix, ">") +
		"reviewer-run-detached-log | test prompt\n" +
		expectedLogSeparator(runPrefix, "<") +
		"reviewer-run-detached-log | detached output\n"
	if logCode != 0 || logErr != "" || logOut != wantLogOut {
		t.Fatalf("logs --follow code/stdout/stderr = %d / %q / %q", logCode, logOut, logErr)
	}
	if !sawCommand || !sawFollow {
		t.Fatalf("sawCommand=%v sawFollow=%v", sawCommand, sawFollow)
	}
}

func TestIntegrationCLIRunSendsJupyterExpose(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-jupyter
agents:
  reviewer:
    provider: codex
`)
	var sawRequest bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				sawRequest = true
				if req.Msg.GetJupyter() == nil || !req.Msg.GetJupyter().GetEnabled() || !req.Msg.GetJupyter().GetExpose() {
					t.Fatalf("RunAgentStream jupyter request = %#v", req.Msg)
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-jupyter",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-jupyter",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-jupyter",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-jupyter", "reviewer", "sandbox-jupyter", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "--jupyter-expose", "--prompt", "inspect")
	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("run --jupyter-expose code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if !sawRequest {
		t.Fatal("RunAgentStream was not called")
	}
}

func TestIntegrationCLIRunCommandSendsCommandAndStreamsOutput(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-command
agents:
  reviewer:
    provider: codex
`)
	var sawRequest bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				sawRequest = true
				if req.Msg.GetAgentName() != "reviewer" || req.Msg.GetCommand() != "echo command" || req.Msg.GetPrompt() != "" || req.Msg.GetTriggerId() != "" {
					t.Fatalf("RunAgentStream command request = %#v", req.Msg)
				}
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     "run-command",
					Chunk:     "command stdout",
				}); err != nil {
					return err
				}
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     "run-command",
					Chunk:     "command stderr",
					Stream:    agentcomposev2.StdioStream_STDIO_STREAM_STDERR,
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-command",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-command",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-command",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-command", "reviewer", "sandbox-command", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "command stdout\n")}), nil
			},
		},
	})
	defer server.Close()

	projectID, err := domain.StableProjectID("cli-run-command", composePath)
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	agentID, err := domain.StableManagedAgentID(projectID, "reviewer")
	if err != nil {
		t.Fatalf("StableManagedAgentID returned error: %v", err)
	}
	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, identity.ShortID(agentID), "--command", "echo command")
	if exitCode != 0 || stderr != "command stderr\n" || stdout != "command stdout\n" {
		t.Fatalf("run --command code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if !sawRequest {
		t.Fatal("RunAgentStream was not called")
	}
}

func TestIntegrationCLIRunInteractivePromptReusesSession(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-prompt
agents:
  reviewer:
    provider: codex
`)
	var prompts []string
	var sessions []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				prompts = append(prompts, req.Msg.GetPrompt())
				sessions = append(sessions, req.Msg.GetSessionId())
				if req.Msg.GetCommand() != "" || req.Msg.GetTriggerId() != "" {
					t.Fatalf("RunAgentStream interactive prompt request = %#v", req.Msg)
				}
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				runID := fmt.Sprintf("run-repl-%d", len(prompts))
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType:  agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:      runID,
					Transcript: &agentcomposev2.TranscriptEvent{Text: fmt.Sprintf("prompt %d output\n", len(prompts))},
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     runID,
					Run: &agentcomposev2.RunSummary{
						RunId:     runID,
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-repl",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-repl", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("second prompt\n\n/exit\n", "run", "--host", server.URL, "--file", composePath, "reviewer", "-i", "--prompt", "first prompt")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -i --prompt code/stderr = %d / %q", exitCode, stderr)
	}
	if stdout != "prompt 1 output\nprompt 2 output\n" {
		t.Fatalf("run -i --prompt stdout = %q", stdout)
	}
	if strings.Join(prompts, "|") != "first prompt|second prompt" {
		t.Fatalf("prompts = %#v", prompts)
	}
	if strings.Join(sessions, "|") != "|sandbox-repl" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestIntegrationCLIRunInteractiveDriverOnlySentForInitialSandbox(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-driver
agents:
  reviewer:
    provider: codex
`)
	var drivers []string
	var prompts []string
	var sessions []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				drivers = append(drivers, req.Msg.GetDriver())
				prompts = append(prompts, req.Msg.GetPrompt())
				sessions = append(sessions, req.Msg.GetSessionId())
				if req.Msg.GetCommand() != "" || req.Msg.GetTriggerId() != "" {
					t.Fatalf("RunAgentStream interactive prompt request = %#v", req.Msg)
				}
				runID := fmt.Sprintf("run-driver-repl-%d", len(prompts))
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType:  agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:      runID,
					Transcript: &agentcomposev2.TranscriptEvent{Text: fmt.Sprintf("driver %d output\n", len(prompts))},
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     runID,
					Run: &agentcomposev2.RunSummary{
						RunId:     runID,
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-driver-repl",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-driver-repl", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("second prompt\n/exit\n", "run", "--host", server.URL, "--file", composePath, "--driver", "msb", "reviewer", "-i", "--prompt", "first prompt")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -i --driver --prompt code/stderr = %d / %q", exitCode, stderr)
	}
	if stdout != "driver 1 output\ndriver 2 output\n" {
		t.Fatalf("run -i --driver --prompt stdout = %q", stdout)
	}
	if strings.Join(prompts, "|") != "first prompt|second prompt" {
		t.Fatalf("prompts = %#v", prompts)
	}
	if strings.Join(sessions, "|") != "|sandbox-driver-repl" {
		t.Fatalf("sessions = %#v", sessions)
	}
	if strings.Join(drivers, "|") != "microsandbox|" {
		t.Fatalf("drivers = %#v", drivers)
	}
}

func TestIntegrationCLIRunInteractiveCommandReusesSession(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-command
agents:
  reviewer:
    provider: codex
`)
	var commands []string
	var sessions []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				commands = append(commands, req.Msg.GetCommand())
				sessions = append(sessions, req.Msg.GetSessionId())
				if req.Msg.GetPrompt() != "" || req.Msg.GetTriggerId() != "" {
					t.Fatalf("RunAgentStream interactive command request = %#v", req.Msg)
				}
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				runID := fmt.Sprintf("run-command-repl-%d", len(commands))
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType:  agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:      runID,
					Transcript: &agentcomposev2.TranscriptEvent{Text: fmt.Sprintf("command %d output\n", len(commands))},
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     runID,
					Run: &agentcomposev2.RunSummary{
						RunId:     runID,
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-command-repl",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-command-repl", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("pwd\nwhoami\n/exit\n", "run", "--host", server.URL, "--file", composePath, "reviewer", "-i", "--command")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -i --command code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if stdout != "command 1 output\ncommand 2 output\n" {
		t.Fatalf("run -i --command stdout = %q", stdout)
	}
	if strings.Join(commands, "|") != "pwd|whoami" {
		t.Fatalf("commands = %#v", commands)
	}
	if strings.Join(sessions, "|") != "|sandbox-command-repl" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestIntegrationCLIRunInteractiveRemoveCreatedSandboxOnExit(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-rm
agents:
  reviewer:
    provider: codex
`)
	var removed []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-repl-rm",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-repl-rm",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-repl-rm",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-repl-rm", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				removed = append(removed, req.Msg.GetSandboxId())
				if !req.Msg.GetForce() {
					t.Fatalf("RemoveSandbox force = false")
				}
				return connect.NewResponse(&agentcomposev2.RemoveSandboxResponse{SandboxId: req.Msg.GetSandboxId(), Removed: true}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("", "run", "--host", server.URL, "--file", composePath, "--rm", "reviewer", "-i", "--prompt", "first")
	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("run -i --rm code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if strings.Join(removed, "|") != "sandbox-repl-rm" {
		t.Fatalf("removed = %#v", removed)
	}
}

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
				if req.Msg.GetSessionId() != "sandbox-existing" {
					t.Fatalf("RunAgentStream session = %q, want sandbox-existing", req.Msg.GetSessionId())
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-repl-existing",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-repl-existing",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-existing",
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

	stdout, stderr, _, exitCode := executeCLICommandWithInput("", "run", "--host", server.URL, "--file", composePath, "--rm", "--sandbox-id", "sandbox-existing", "reviewer", "-i", "--prompt", "first")
	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("run -i --rm --sandbox-id code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
}

func TestCLIRunInteractivePromptProviderUnsupported(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-gemini
agents:
  reviewer:
    provider: gemini
`)
	stdout, stderr, _, exitCode := executeCLICommandWithInput("hello\n", "run", "--file", composePath, "reviewer", "-i", "--prompt")
	if exitCode != exitCodeUnsupported {
		t.Fatalf("run -i --prompt gemini exit code = %d, want %d; stderr=%q", exitCode, exitCodeUnsupported, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "unsupported for provider gemini") || !strings.Contains(stderr, "codex, claude, opencode") {
		t.Fatalf("run -i --prompt gemini stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestIntegrationCLIRunInteractivePromptDefaultProviderAllowed(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-default-provider
agents:
  reviewer: {}
`)
	var sawRun bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				sawRun = true
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-default-provider",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-default-provider",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-default-provider",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-default-provider", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("", "run", "--host", server.URL, "--file", composePath, "reviewer", "-i", "--prompt", "hello")
	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("run -i --prompt default provider code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if !sawRun {
		t.Fatal("RunAgentStream was not called")
	}
}

func TestIntegrationCLIRunTriggerPositionalRejected(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-trigger
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly-review
          cron: "0 1 * * *"
          prompt: review nightly
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				t.Fatalf("RunAgentStream should not be called for positional trigger")
				return nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "nightly-review")
	if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "does not accept positional trigger arguments") {
		t.Fatalf("run positional trigger code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
}

func TestIntegrationCLIRunTriggerPositionalJSONRejected(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-trigger-warning
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly-warning
          cron: "0 2 * * *"
          prompt: review nightly
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				t.Fatalf("RunAgentStream should not be called for positional trigger")
				return nil
			},
		},
	})
	defer server.Close()

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--json", "reviewer", "nightly-warning")
	if jsonCode != exitCodeUsage || jsonOut != "" || !strings.Contains(jsonErr, "does not accept positional trigger arguments") {
		t.Fatalf("run positional trigger --json code/stdout/stderr = %d / %q / %q", jsonCode, jsonOut, jsonErr)
	}
}

func TestIntegrationCLISchedulerList(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-list
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
          prompt: review nightly
        - name: events
          event:
            topic: repo.updated
          prompt: review event
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: testCLIProject(req.Msg.GetProject().GetProjectId(), "cli-scheduler-list", composePath)}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "ls", "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("scheduler ls code/stderr = %d / %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "AGENT") || !strings.Contains(stdout, "nightly") || !strings.Contains(stdout, "events") || !strings.Contains(stdout, "declarative") {
		t.Fatalf("scheduler ls stdout = %q", stdout)
	}

	projectID, err := domain.StableProjectID("cli-scheduler-list", composePath)
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	agentID, err := domain.StableManagedAgentID(projectID, "reviewer")
	if err != nil {
		t.Fatalf("StableManagedAgentID returned error: %v", err)
	}
	jsonOut, jsonErr, _, jsonCode := executeCLICommand("scheduler", "ls", identity.ShortID(agentID), "--json", "--host", server.URL, "--file", composePath)
	if jsonCode != 0 || jsonErr != "" || !strings.Contains(jsonOut, `"agent_name": "reviewer"`) || !strings.Contains(jsonOut, `"source": "declarative"`) {
		t.Fatalf("scheduler ls --json code/stdout/stderr = %d / %q / %q", jsonCode, jsonOut, jsonErr)
	}
}

func TestIntegrationCLISchedulerTriggerUsesRunAgentTriggerID(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-trigger
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
          prompt: review nightly
`)
	var sawRequest bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: testCLIProject(req.Msg.GetProject().GetProjectId(), "cli-scheduler-trigger", composePath)}), nil
			},
		},
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				sawRequest = true
				if req.Msg.GetAgentName() != "reviewer" || !identity.IsID(req.Msg.GetTriggerId()) || req.Msg.GetPrompt() != "" || req.Msg.GetCommand() != "" {
					t.Fatalf("RunAgentStream scheduler trigger request = %#v", req.Msg)
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-scheduler-trigger",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-scheduler-trigger",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-scheduler-trigger",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-scheduler-trigger", "reviewer", "sandbox-scheduler-trigger", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "trigger", "--host", server.URL, "--file", composePath, "reviewer", "nightly")
	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("scheduler trigger code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if !sawRequest {
		t.Fatal("RunAgentStream was not called")
	}
}

func TestIntegrationCLISchedulerInspectDeclarativeTriggerYAML(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-inspect
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
          prompt: review nightly
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: testCLIProject(req.Msg.GetProject().GetProjectId(), "cli-scheduler-inspect", composePath)}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "inspect", "--host", server.URL, "--file", composePath, "reviewer", "nightly")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("scheduler inspect code/stderr = %d / %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "name: nightly") || !strings.Contains(stdout, "cron: 0 2 * * *") || !strings.Contains(stdout, "prompt: review nightly") {
		t.Fatalf("scheduler inspect stdout = %q", stdout)
	}
}

func TestIntegrationCLISchedulerInspectLoaderRegisteredTrigger(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-loader
agents:
  reviewer:
    provider: codex
    scheduler:
      script: |
        scheduler.interval("loader-every-minute", async function() {}, 60000);
`)
	var requestedLoader string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				project := testCLIProject(req.Msg.GetProject().GetProjectId(), "cli-scheduler-loader", composePath)
				project.Schedulers[0].ManagedLoaderId = "loader-reviewer"
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
		},
		loader: loaderServiceStub{
			getLoader: func(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
				requestedLoader = req.Msg.GetLoaderId()
				return connect.NewResponse(&agentcomposev1.LoaderResponse{
					Loader: &agentcomposev1.LoaderDetail{
						Summary: &agentcomposev1.LoaderSummary{LoaderId: req.Msg.GetLoaderId(), Name: "reviewer-scheduler", Enabled: true},
						Triggers: []*agentcomposev1.LoaderTrigger{
							{
								LoaderId:    req.Msg.GetLoaderId(),
								TriggerId:   "loader-every-minute",
								Kind:        agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_INTERVAL,
								IntervalMs:  60000,
								Enabled:     true,
								NextFireAt:  "2026-07-06T12:00:00Z",
								LastFiredAt: "2026-07-06T11:59:00Z",
							},
						},
					},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "inspect", "--host", server.URL, "--file", composePath, "reviewer", "loader-every-minute")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("scheduler inspect loader code/stderr = %d / %q", exitCode, stderr)
	}
	if requestedLoader == "" || !strings.Contains(stdout, "trigger_id: loader-every-minute") || !strings.Contains(stdout, "interval_ms: 60000") || !strings.Contains(stdout, "kind: interval") {
		t.Fatalf("requestedLoader=%q stdout=%q", requestedLoader, stdout)
	}
}

func TestCLIRunInputModeUsageErrors(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-input-errors
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				t.Fatalf("RunAgentStream should not be called for invalid input mode")
				return nil
			},
		},
	})
	defer server.Close()

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "trigger flag unsupported",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--trigger", "nightly"},
			want: "unknown flag: --trigger",
		},
		{
			name: "legacy sandbox flag unsupported",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--sandbox", "sandbox-1", "--prompt", "check"},
			want: "unknown flag: --sandbox",
		},
		{
			name: "legacy session flag unsupported",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--session-id", "sandbox-1", "--prompt", "check"},
			want: "unknown flag: --session-id",
		},
		{
			name: "bad driver",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--driver", "bad", "--prompt", "check"},
			want: "unsupported agent-compose runtime driver",
		},
		{
			name: "driver with sandbox id",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--sandbox-id", "sandbox-1", "--driver", "docker", "--prompt", "check"},
			want: "run --driver cannot be combined with --sandbox-id",
		},
		{
			name: "command and prompt flags",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--command", "echo hi", "--prompt", "check"},
			want: "only one of --prompt or --command",
		},
		{
			name: "prompt flag and positional trigger",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--prompt", "check", "legacy"},
			want: "run with --prompt does not accept additional positional arguments",
		},
		{
			name: "command flag and positional trigger",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--command", "echo hi", "legacy"},
			want: "run with --command does not accept additional positional arguments",
		},
		{
			name: "positional trigger rejected",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "nightly"},
			want: "does not accept positional trigger arguments",
		},
		{
			name: "multiple extra positional arguments rejected",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "nightly", "extra"},
			want: "does not accept positional trigger arguments",
		},
		{
			name: "empty command flag",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--command", " "},
			want: "requires a non-empty command",
		},
		{
			name: "detach and interactive flags",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "-d", "-i"},
			want: "cannot be combined",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("stdout/stderr = %q / %q, want stderr containing %q", stdout, stderr, tc.want)
			}
		})
	}
}

func TestCLIRunInteractiveUsageErrors(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive
agents:
  reviewer:
    provider: codex
`)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no mode",
			args: []string{"run", "--file", composePath, "reviewer", "-i"},
			want: "requires exactly one of --prompt or --command",
		},
		{
			name: "json",
			args: []string{"run", "--file", composePath, "--json", "reviewer", "-i", "--prompt"},
			want: "cannot be combined with --json",
		},
		{
			name: "prompt and command",
			args: []string{"run", "--file", composePath, "reviewer", "-i", "--prompt", "--command"},
			want: "requires exactly one of --prompt or --command",
		},
		{
			name: "additional positional argument",
			args: []string{"run", "--file", composePath, "reviewer", "-i", "--prompt", "first", "legacy"},
			want: "does not accept additional positional arguments",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("run -i exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("run -i stdout/stderr = %q / %q, want %q", stdout, stderr, tc.want)
			}
		})
	}
}

func TestIntegrationCLIRunRemoveSandboxOnSuccess(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-rm
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_REMOVE_ON_COMPLETION {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     "run-rm",
					Chunk:     "done\n",
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-rm",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-rm",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-rm",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-rm", "reviewer", "sandbox-rm", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "done\n")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--rm", "reviewer", "--prompt", "clean")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run --rm code/stderr = %d / %q", exitCode, stderr)
	}
	if stdout != "done\n" {
		t.Fatalf("run --rm stdout = %q", stdout)
	}
}

func TestIntegrationCLIRunRemoveSandboxJSONDoesNotPrintCleanupText(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-rm-detail
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_REMOVE_ON_COMPLETION {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-rm-detail",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-rm-detail",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-rm-detail", "reviewer", "sandbox-from-detail", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--rm", "--json", "reviewer", "--prompt", "clean")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run --rm --json code/stderr = %d / %q", exitCode, stderr)
	}
	if strings.Contains(stdout, "removed sandbox") {
		t.Fatalf("run --rm --json stdout contains text cleanup output: %q", stdout)
	}
}

func TestIntegrationCLIRunFailureReturnsStableExitCode(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-failure
agents:
  reviewer:
    provider: codex
`)
	server := newRunServiceStubServer(t, runServiceStub{
		runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
			if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
				RunId:     "run-failed",
				Chunk:     "agent failed\n",
				Stream:    agentcomposev2.StdioStream_STDIO_STREAM_STDERR,
			}); err != nil {
				return err
			}
			return stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
				RunId:     "run-failed",
				Run: &agentcomposev2.RunSummary{
					RunId:     "run-failed",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_FAILED,
					SessionId: "session-failed",
					ExitCode:  7,
					Error:     "agent execution failed",
				},
			})
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-failed", "reviewer", "session-failed", agentcomposev2.RunStatus_RUN_STATUS_FAILED, 7, "agent failed\n")}), nil
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "--prompt", "fail")
	if exitCode != 7 {
		t.Fatalf("run failure exit code = %d, want 7; stderr=%q", exitCode, stderr)
	}
	if stdout != "" {
		t.Fatalf("run failure stdout = %q, want empty", stdout)
	}
	for _, want := range []string{"agent failed", "run-failed", "agent execution failed"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("run failure stderr %q does not contain %q", stderr, want)
		}
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
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_REMOVE_ON_COMPLETION {
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
						SessionId: "sandbox-failed",
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

func TestIntegrationCLIRunRemoveSandboxError(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-rm-error
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_REMOVE_ON_COMPLETION {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-rm-error",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-rm-error",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SessionId: "sandbox-rm-error",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				run := testRunDetail(req.Msg.GetProjectId(), "run-rm-error", "reviewer", "sandbox-rm-error", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")
				run.CleanupError = "remove failed"
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--rm", "reviewer", "--prompt", "clean")
	if exitCode == 0 {
		t.Fatalf("run --rm cleanup error exit code = %d, want non-zero", exitCode)
	}
	if stdout != "" || !strings.Contains(stderr, "succeeded but sandbox cleanup failed") || !strings.Contains(stderr, "remove failed") {
		t.Fatalf("run --rm cleanup error stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestIntegrationCLILogsFiltersRunAgentSessionAndJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-demo
agents:
  reviewer:
    provider: codex
`)
	runID := identity.NewID(identity.ResourceRun, "logs", "run")
	sandboxID := identity.NewID(identity.ResourceSandbox, "logs", "sandbox")
	var sawFilteredList bool
	server := newRunServiceStubServer(t, runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			switch req.Msg.GetLimit() {
			case 200:
				if req.Msg.GetSessionId() != "" || req.Msg.GetSandboxId() != "" {
					t.Fatalf("ListRuns resolver request = %#v", req.Msg)
				}
			case 20:
				sawFilteredList = true
				if req.Msg.GetAgentName() != "reviewer" || req.Msg.GetSandboxId() != sandboxID || req.Msg.GetSessionId() != "" {
					t.Fatalf("ListRuns filtered request = %#v", req.Msg)
				}
			default:
				t.Fatalf("ListRuns request = %#v", req.Msg)
			}
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
				RunId:     runID,
				ProjectId: req.Msg.GetProjectId(),
				AgentName: "reviewer",
				Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
				SandboxId: sandboxID,
			}}}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			if req.Msg.GetRunId() != runID {
				t.Fatalf("GetRun request = %#v", req.Msg)
			}
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", sandboxID, agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "stored log output\n")}), nil
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "reviewer", "--sandbox", identity.ShortID(sandboxID), "--json")
	if exitCode != 0 {
		t.Fatalf("logs exit code = %d, stderr=%q", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("logs stderr = %q, want empty", stderr)
	}
	var decoded composeLogsOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("logs JSON decode failed: %v\n%s", err, stdout)
	}
	if len(decoded.Runs) != 1 || decoded.Runs[0].RunID != displayOpaqueID(runID) || decoded.Runs[0].Prompt != "test prompt" || decoded.Runs[0].Content != "stored log output\n" {
		t.Fatalf("logs JSON = %#v", decoded)
	}
	if !sawFilteredList {
		t.Fatal("ListRuns was not called")
	}

	legacyOut, legacyErr, _, legacyCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--agent", "reviewer", "--session-id", sandboxID, "--json")
	if legacyCode != exitCodeUsage || legacyOut != "" || !strings.Contains(legacyErr, "unknown flag: --session-id") {
		t.Fatalf("logs --session-id code/stdout/stderr = %d / %q / %q", legacyCode, legacyOut, legacyErr)
	}

	runOut, runErr, _, runCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--run-id", identity.ShortID(runID))
	runPrefix := "reviewer-run-" + identity.ShortID(runID) + " | "
	wantRunOut := expectedLogSeparator(runPrefix, ">") +
		"reviewer-run-" + identity.ShortID(runID) + " | test prompt\n" +
		expectedLogSeparator(runPrefix, "<") +
		"reviewer-run-" + identity.ShortID(runID) + " | stored log output\n"
	if runCode != 0 || runErr != "" || runOut != wantRunOut {
		t.Fatalf("logs --run-id code/stdout/stderr = %d / %q / %q", runCode, runOut, runErr)
	}
}

func TestIntegrationCLILogsTailTextJSONAndRunID(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-tail
agents:
  reviewer:
    provider: codex
`)
	output := "one\ntwo\nthree\n"
	server := newRunServiceStubServer(t, runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
				RunId:     "run-tail",
				ProjectId: req.Msg.GetProjectId(),
				AgentName: "reviewer",
				Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
				SessionId: "session-tail",
			}}}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-tail", "reviewer", "session-tail", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, output)}), nil
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--tail", "2")
	tailPrefix := "reviewer-run-tail | "
	wantTail := expectedLogSeparator(tailPrefix, ">") +
		"reviewer-run-tail | test prompt\n" +
		expectedLogSeparator(tailPrefix, "<") +
		"reviewer-run-tail | two\n" +
		"reviewer-run-tail | three\n"
	if exitCode != 0 || stderr != "" || stdout != wantTail {
		t.Fatalf("logs --tail text code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "-n", "2", "--json")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("logs --tail --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeLogsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("logs --tail JSON decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Runs) != 1 || decoded.Runs[0].Prompt != "test prompt" || decoded.Runs[0].Content != "two\nthree\n" {
		t.Fatalf("logs --tail JSON = %#v", decoded)
	}

	runOut, runErr, _, runCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--run-id", "run-tail", "-n", "1")
	wantRunTail := expectedLogSeparator(tailPrefix, ">") +
		"reviewer-run-tail | test prompt\n" +
		expectedLogSeparator(tailPrefix, "<") +
		"reviewer-run-tail | three\n"
	if runCode != 0 || runErr != "" || runOut != wantRunTail {
		t.Fatalf("logs --run-id --tail code/stdout/stderr = %d / %q / %q", runCode, runOut, runErr)
	}
}

func TestIntegrationCLILogsTimestampAndMultiRunPrefixes(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-prefix
agents:
  reviewer:
    provider: codex
  writer:
    provider: codex
`)
	server := newRunServiceStubServer(t, runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{
				{
					RunId:     "run-reviewer",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
					SessionId: "session-reviewer",
				},
				{
					RunId:     "run-writer",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "writer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
					SessionId: "session-writer",
				},
			}}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			switch req.Msg.GetRunId() {
			case "run-reviewer":
				run := testRunDetail(req.Msg.GetProjectId(), "run-reviewer", "reviewer", "session-reviewer", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "review one\n")
				run.Summary.StartedAt = "2026-06-11T00:00:02Z"
				run.Summary.CompletedAt = "2026-06-11T00:00:03Z"
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
			case "run-writer":
				run := testRunDetail(req.Msg.GetProjectId(), "run-writer", "writer", "session-writer", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "write one\nwrite two\n")
				run.Summary.StartedAt = "2026-06-11T00:00:01Z"
				run.Summary.CompletedAt = "2026-06-11T00:00:04Z"
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
			default:
				t.Fatalf("unexpected run id %q", req.Msg.GetRunId())
				return nil, nil
			}
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--timestamp")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("logs --timestamp code/stderr = %d / %q", exitCode, stderr)
	}
	writerPrefix := "writer-run-writer [2026-06-11T00:00:04.000Z]| "
	reviewerPrefix := "reviewer-run-reviewer [2026-06-11T00:00:03.000Z]| "
	want := expectedLogSeparator(writerPrefix, ">") +
		"writer-run-writer [2026-06-11T00:00:04.000Z]| test prompt\n" +
		expectedLogSeparator(writerPrefix, "<") +
		"writer-run-writer [2026-06-11T00:00:04.000Z]| write one\n" +
		"writer-run-writer [2026-06-11T00:00:04.000Z]| write two\n" +
		expectedLogSeparator(reviewerPrefix, ">") +
		"reviewer-run-reviewer [2026-06-11T00:00:03.000Z]| test prompt\n" +
		expectedLogSeparator(reviewerPrefix, "<") +
		"reviewer-run-reviewer [2026-06-11T00:00:03.000Z]| review one\n"
	if stdout != want {
		t.Fatalf("logs --timestamp stdout = %q, want %q", stdout, want)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--json")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("logs --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeLogsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("logs --json decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Runs) != 2 || decoded.Runs[0].RunID != displayOpaqueID("run-writer") || decoded.Runs[1].RunID != displayOpaqueID("run-reviewer") {
		t.Fatalf("logs --json order = %#v", decoded.Runs)
	}
}

func TestLogsAgentFlagAndPositionalIsUsageError(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("logs", "reviewer", "--agent", "writer")
	if exitCode != exitCodeUsage {
		t.Fatalf("logs positional and --agent exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("logs positional and --agent stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "positionally or with --agent") {
		t.Fatalf("logs positional and --agent stderr = %q", stderr)
	}
}

func expectedLogSeparator(prefix, marker string) string {
	width := 80 - len(prefix)
	if width < 8 {
		width = 8
	}
	return prefix + strings.Repeat(marker, width) + "\n"
}

func TestRunLogLinePrefixWidthUsesDisplayWidth(t *testing.T) {
	summary := &agentcomposev2.RunSummary{
		RunId:       "run-123456789abc",
		AgentName:   "审查",
		CompletedAt: "2026-06-11T00:00:03Z",
	}
	if got, want := runLogLinePrefixWidth(summary, "", false), 24; got != want {
		t.Fatalf("runLogLinePrefixWidth without timestamp = %d, want %d", got, want)
	}
	if got, want := runLogLinePrefixWidth(summary, summary.GetCompletedAt(), true), 50; got != want {
		t.Fatalf("runLogLinePrefixWidth with timestamp = %d, want %d", got, want)
	}
}

func TestWritePrefixedRunOutputHonorsTimestampFlag(t *testing.T) {
	summary := &agentcomposev2.RunSummary{
		RunId:       "run-123456789abc",
		AgentName:   "reviewer",
		CompletedAt: "2026-06-11T00:00:03Z",
	}
	var out strings.Builder
	if err := writePrefixedRunOutput(&out, summary, "line\n", false); err != nil {
		t.Fatalf("writePrefixedRunOutput returned error: %v", err)
	}
	if got, want := out.String(), "reviewer-run-123456789abc | line\n"; got != want {
		t.Fatalf("writePrefixedRunOutput without timestamp = %q, want %q", got, want)
	}
}

func TestWriteLogDetailsFollowPrintsPromptOnce(t *testing.T) {
	detail := testRunDetail("project-logs", "run-follow-repeat", "reviewer", "session-follow-repeat", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "first\n")
	printed := map[string]runLogPrintState{}
	options := composeLogsOptions{Follow: true, TailLines: -1}
	var out bytes.Buffer
	if err := writeLogDetails(&out, []*agentcomposev2.RunDetail{detail}, printed, options); err != nil {
		t.Fatalf("writeLogDetails first follow call returned error: %v", err)
	}
	detail.Output = "first\nsecond\n"
	if err := writeLogDetails(&out, []*agentcomposev2.RunDetail{detail}, printed, options); err != nil {
		t.Fatalf("writeLogDetails second follow call returned error: %v", err)
	}
	if err := writeLogDetails(&out, []*agentcomposev2.RunDetail{detail}, printed, options); err != nil {
		t.Fatalf("writeLogDetails third follow call returned error: %v", err)
	}
	got := out.String()
	promptPrefix := runLogPrefix(detail.GetSummary()) + " | "
	promptSeparator := expectedLogSeparator(promptPrefix, ">")
	if strings.Count(got, promptSeparator) != 1 {
		t.Fatalf("follow log prompt printed %d times; output = %q", strings.Count(got, promptSeparator), got)
	}
	if !strings.Contains(got, "| test prompt\n") ||
		!strings.Contains(got, "| first\n") ||
		!strings.Contains(got, "| second\n") {
		t.Fatalf("follow log output missing expected content: %q", got)
	}
}

func TestIntegrationCLILogsFollowUsesServerStream(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-follow
agents:
  reviewer:
    provider: codex
`)
	var listCalls int
	var getCalls int
	var followCalls int
	server := newRunServiceStubServer(t, runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			listCalls++
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
				RunId:     "run-follow",
				ProjectId: req.Msg.GetProjectId(),
				AgentName: "reviewer",
				Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
				SessionId: "session-follow",
			}}}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			getCalls++
			if req.Msg.GetRunId() != "run-follow" {
				t.Fatalf("GetRun request = %#v", req.Msg)
			}
			run := testRunDetail(req.Msg.GetProjectId(), "run-follow", "reviewer", "session-follow", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "")
			if getCalls == 1 {
				run.Prompt = ""
				run.ResultJson = "{}"
			} else {
				run.Prompt = ""
				run.ResultJson = `{"mode":"command","command":"echo delayed prompt"}`
			}
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
		},
		followRunLogs: func(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
			followCalls++
			if req.Msg.GetRunId() != "run-follow" || !req.Msg.GetFollow() || req.Msg.GetTailLines() != 2 {
				t.Fatalf("FollowRunLogs request = %#v", req.Msg)
			}
			if err := stream.Send(&agentcomposev2.RunLogChunk{Data: "first\n", Offset: 6, RunStatus: agentcomposev2.RunStatus_RUN_STATUS_RUNNING, CreatedAt: "2026-07-06T08:01:36.372Z"}); err != nil {
				return err
			}
			return stream.Send(&agentcomposev2.RunLogChunk{Data: "second\n", Offset: 13, RunStatus: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, CreatedAt: "2026-07-06T08:01:36.875Z", IsFinal: true})
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--follow", "--tail", "2", "--timestamp")
	if exitCode != 0 {
		t.Fatalf("logs follow exit code = %d, stderr=%q", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("logs follow stderr = %q, want empty", stderr)
	}
	followPrefix := "reviewer-run-follow [2026-06-11T00:00:01.000Z]| "
	want := expectedLogSeparator(followPrefix, ">") +
		"reviewer-run-follow [2026-06-11T00:00:01.000Z]| echo delayed prompt\n" +
		expectedLogSeparator(followPrefix, "<") +
		"reviewer-run-follow [2026-06-11T00:00:01.000Z]| first\n" +
		"reviewer-run-follow [2026-06-11T00:00:01.000Z]| second\n"
	if stdout != want {
		t.Fatalf("logs follow stdout = %q", stdout)
	}
	if listCalls != 1 || getCalls != 2 || followCalls != 1 {
		t.Fatalf("logs follow list/get/follow calls = %d/%d/%d, want 1/2/1", listCalls, getCalls, followCalls)
	}
}

func TestIntegrationCLILogsFollowPrintsDelayedPromptWithoutOutput(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-follow-empty
agents:
  reviewer:
    provider: codex
`)
	var getCalls int
	server := newRunServiceStubServer(t, runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
				RunId:     "run-follow-empty",
				ProjectId: req.Msg.GetProjectId(),
				AgentName: "reviewer",
				Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
				SessionId: "session-follow-empty",
			}}}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			getCalls++
			run := testRunDetail(req.Msg.GetProjectId(), "run-follow-empty", "reviewer", "session-follow-empty", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "")
			run.Prompt = ""
			if getCalls == 1 {
				run.ResultJson = "{}"
			} else {
				run.ResultJson = `{"mode":"command","command":"true"}`
			}
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
		},
		followRunLogs: func(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
			return stream.Send(&agentcomposev2.RunLogChunk{RunStatus: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, IsFinal: true})
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--follow", "--timestamp")
	followEmptyPrefix := "reviewer-run-follow-empty [2026-06-11T00:00:01.000Z]| "
	want := expectedLogSeparator(followEmptyPrefix, ">") +
		"reviewer-run-follow-empty [2026-06-11T00:00:01.000Z]| true\n"
	if exitCode != 0 || stderr != "" || stdout != want {
		t.Fatalf("logs follow no output code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if getCalls != 2 {
		t.Fatalf("GetRun calls = %d, want 2", getCalls)
	}
}

func TestRealCLILogsUsesDaemonRunDataWithTimestampsAndChronologicalOrder(t *testing.T) {
	if os.Getenv("AGENT_COMPOSE_REAL_CLI_TEST") != "1" {
		t.Skip("set AGENT_COMPOSE_REAL_CLI_TEST=1 to run the real CLI + daemon logs test")
	}
	root := t.TempDir()
	binPath := filepath.Join(root, "agent-compose")
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelBuild()
	build := osexec.CommandContext(buildCtx, "go", "build", "-buildvcs=false", "-o", binPath, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOCACHE="+filepath.Join(root, "gocache"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent-compose CLI failed: %v\n%s", err, output)
	}

	composePath := writeComposeFile(t, filepath.Join(root, "project"), `
name: real-cli-logs
agents:
  writer:
    provider: codex
  reviewer:
    provider: codex
`)
	_, normalized, projectID, err := resolveComposeProject(cliOptions{ComposeFile: composePath})
	if err != nil {
		t.Fatalf("resolve compose project: %v", err)
	}

	dataRoot := filepath.Join(root, "data")
	di := do.New()
	storeConfig := &config.Config{DataRoot: dataRoot, DbAddr: filepath.Join(dataRoot, "data.db")}
	do.ProvideValue(di, storeConfig)
	store, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("create config store: %v", err)
	}
	if _, err := store.UpsertProject(context.Background(), domain.ProjectRecord{ID: projectID, Name: normalized.Name, SourcePath: composePath, CurrentRevision: 1}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	earlyStarted := time.Date(2026, 7, 8, 1, 2, 3, 0, time.UTC)
	lateStarted := earlyStarted.Add(2 * time.Second)
	if _, err := store.CreateProjectRun(context.Background(), domain.ProjectRunRecord{
		RunID:           "run-real-late",
		ProjectID:       projectID,
		ProjectName:     normalized.Name,
		ProjectRevision: 1,
		AgentName:       "reviewer",
		Source:          domain.ProjectRunSourceAPI,
		Status:          domain.ProjectRunStatusSucceeded,
		Output:          "late log\n",
		ResultJSON:      "{}",
		StartedAt:       lateStarted,
		CompletedAt:     lateStarted.Add(time.Second),
	}); err != nil {
		t.Fatalf("create late run: %v", err)
	}
	if _, err := store.CreateProjectRun(context.Background(), domain.ProjectRunRecord{
		RunID:           "run-real-early",
		ProjectID:       projectID,
		ProjectName:     normalized.Name,
		ProjectRevision: 1,
		AgentName:       "writer",
		Source:          domain.ProjectRunSourceAPI,
		Status:          domain.ProjectRunStatusSucceeded,
		Output:          "early log\n",
		ResultJSON:      "{}",
		StartedAt:       earlyStarted,
		CompletedAt:     earlyStarted.Add(time.Second),
	}); err != nil {
		t.Fatalf("create early run: %v", err)
	}
	if err := store.DB().Close(); err != nil {
		t.Fatalf("close config store: %v", err)
	}

	socketPath := shortUnixSocketPath(t)
	t.Setenv("DATA_ROOT", dataRoot)
	t.Setenv("HTTP_LISTEN", "")
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "")
	t.Setenv("RUNTIME_DRIVER", config.RuntimeDriverDocker)
	t.Setenv("SESSION_START_TIMEOUT", "1s")
	t.Setenv("SESSION_STOP_TIMEOUT", "1s")
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("BOXLITE_HOME", filepath.Join(root, "boxlite"))
	t.Setenv("BOXLITE_RUNTIME_DIR", filepath.Join(root, "boxlite-runtime"))
	t.Setenv("DOCKER_HOME", filepath.Join(root, "docker"))
	t.Setenv("MICROSANDBOX_HOME", filepath.Join(root, "microsandbox"))
	t.Setenv("MICROSANDBOX_MSB_PATH", filepath.Join(root, "msb"))
	t.Setenv("MICROSANDBOX_LIB_PATH", filepath.Join(root, "libmicrosandbox_go_ffi.so"))

	daemonCtx, stopDaemon := context.WithCancel(context.Background())
	app, err := NewDaemonApp(daemonCtx, DaemonOptions{StartBackground: func(do.Injector) error { return nil }})
	if err != nil {
		stopDaemon()
		t.Fatalf("NewDaemonApp returned error: %v", err)
	}
	errCh := runDaemonAppAsync(app, daemonCtx)
	t.Cleanup(func() {
		stopDaemon()
		waitForDaemonExit(t, errCh)
	})
	waitForHTTPStatus(t, newUnixHTTPClient(socketPath), "http://agent-compose/api/version", http.StatusOK)

	runCtx, cancelRun := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRun()
	logsCmd := osexec.CommandContext(runCtx, binPath, "--file", composePath, "logs")
	logsCmd.Env = append(os.Environ(),
		"AGENT_COMPOSE_SOCKET="+socketPath,
		"AGENT_COMPOSE_HOST=",
		"DATA_ROOT="+dataRoot,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	logsCmd.Stdout = &stdout
	logsCmd.Stderr = &stderr
	if err := logsCmd.Run(); err != nil {
		t.Fatalf("real CLI logs failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	want := "writer-run-real-early [2026-07-08T01:02:04.000Z]| early log\n" +
		"reviewer-run-real-late [2026-07-08T01:02:06.000Z]| late log\n"
	if stdout.String() != want || stderr.String() != "" {
		t.Fatalf("real CLI logs stdout/stderr = %q / %q, want %q / empty", stdout.String(), stderr.String(), want)
	}
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
	sessions := []*agentcomposev1.SessionSummary{
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
			SessionId: "session-running",
			CreatedAt: "2026-06-11T00:00:00Z",
			UpdatedAt: "2026-06-11T00:00:01Z",
		},
		{
			RunId:     "run-error",
			ProjectId: project.GetSummary().GetProjectId(),
			AgentName: "worker",
			Status:    agentcomposev2.RunStatus_RUN_STATUS_FAILED,
			SessionId: "session-error",
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
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
				if req.Msg.GetLimit() < 100 {
					t.Fatalf("ListSessions request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev1.ListSessionsResponse{Sessions: sessions}), nil
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
	if decoded.Sandboxes[0].ID != "session-running" || decoded.Sandboxes[0].Agent != "reviewer" || decoded.Sandboxes[0].Status != "running" || decoded.Sandboxes[0].RunID != "run-running" {
		t.Fatalf("ps sandbox JSON = %#v", decoded.Sandboxes[0])
	}
	if stdout == "" || !strings.Contains(stdout, `"id"`) || strings.Contains(stdout, `"session_id"`) {
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

func TestIntegrationCLISandboxCommandGroupHelp(t *testing.T) {
	stdout, stderr, runCount, exitCode := executeCLICommand("sandbox", "--help")
	if exitCode != 0 || stderr != "" || runCount != 0 {
		t.Fatalf("sandbox --help code/stderr/runCount = %d / %q / %d", exitCode, stderr, runCount)
	}
	for _, want := range []string{"Manage project sandboxes", "ls", "stop", "resume", "rm", "prune"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("sandbox --help output %q does not contain %q", stdout, want)
		}
	}
}

func TestIntegrationCLISandboxPruneFlagsAndOlderThanUsage(t *testing.T) {
	helpOut, helpErr, runCount, helpCode := executeCLICommand("sandbox", "prune", "--help")
	if helpCode != 0 || helpErr != "" || runCount != 0 {
		t.Fatalf("sandbox prune --help code/stderr/runCount = %d / %q / %d", helpCode, helpErr, runCount)
	}
	for _, want := range []string{"--status", "--agent", "--driver", "--older-than", "--force"} {
		if !strings.Contains(helpOut, want) {
			t.Fatalf("sandbox prune --help output %q does not contain %q", helpOut, want)
		}
	}

	stdout, stderr, _, exitCode := executeCLICommand("sandbox", "prune", "--older-than", "0")
	if exitCode != exitCodeUsage {
		t.Fatalf("sandbox prune invalid older-than exit code = %d, want usage; stderr=%q", exitCode, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, `invalid --older-than "0": duration must be positive`) {
		t.Fatalf("sandbox prune invalid older-than stdout/stderr = %q / %q", stdout, stderr)
	}

}

func TestIntegrationCLISandboxPruneDryRunFiltersAndSafety(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-prune-demo
agents:
  reviewer:
    provider: codex
  worker:
    provider: codex
`)
	project := testCLIProject("project-cli-prune", "cli-prune-demo", composePath)
	oldTime := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	newTime := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339Nano)

	session := func(id, status, projectID, agent, driver, updatedAt string) *agentcomposev1.SessionSummary {
		item := testCLISessionSummary(id, status, projectID, agent, "")
		item.Driver = driver
		item.UpdatedAt = updatedAt
		return item
	}
	createdFallback := session("session-created-fallback", "STOPPED", "project-cli-prune", "reviewer", "docker", "")
	createdFallback.CreatedAt = oldTime
	sessions := []*agentcomposev1.SessionSummary{
		session("session-stopped", "STOPPED", "project-cli-prune", "reviewer", "docker", oldTime),
		session("session-failed", "FAILED", "project-cli-prune", "worker", "boxlite", oldTime),
		session("session-running", "RUNNING", "project-cli-prune", "reviewer", "docker", oldTime),
		session("session-pending", "PENDING", "project-cli-prune", "worker", "boxlite", oldTime),
		session("session-error", "ERROR", "project-cli-prune", "worker", "microsandbox", oldTime),
		session("session-micro", "STOPPED", "project-cli-prune", "reviewer", "microsandbox", oldTime),
		session("session-new", "STOPPED", "project-cli-prune", "reviewer", "docker", newTime),
		createdFallback,
		session("session-bad-time", "STOPPED", "project-cli-prune", "reviewer", "docker", "not-a-time"),
		session("session-foreign", "STOPPED", "foreign-project", "reviewer", "docker", oldTime),
	}
	removeCalls := 0
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
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
				return connect.NewResponse(&agentcomposev1.ListSessionsResponse{Sessions: sessions}), nil
			},
		},
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				removeCalls++
				t.Fatalf("dry-run prune called RemoveSandbox with %#v", req.Msg)
				return nil, nil
			},
		},
	})
	defer server.Close()

	runPrune := func(args ...string) composeSandboxPruneOutput {
		t.Helper()
		base := []string{"sandbox", "prune", "--host", server.URL, "--file", composePath, "--json"}
		stdout, stderr, _, exitCode := executeCLICommand(append(base, args...)...)
		if exitCode != 0 || stderr != "" {
			t.Fatalf("sandbox prune %v code/stderr = %d / %q", args, exitCode, stderr)
		}
		var decoded composeSandboxPruneOutput
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatalf("sandbox prune %v JSON decode failed: %v\n%s", args, err, stdout)
		}
		if !decoded.DryRun || len(decoded.Removed) != 0 || len(decoded.Skipped) != 0 {
			t.Fatalf("sandbox prune %v output = %#v", args, decoded)
		}
		return decoded
	}
	matched := func(output composeSandboxPruneOutput) map[string]bool {
		t.Helper()
		result := map[string]bool{}
		for _, sandbox := range output.Matched {
			result[sandbox.ID] = true
		}
		return result
	}

	defaultMatches := matched(runPrune())
	for _, want := range []string{"session-stopped", "session-failed"} {
		if !defaultMatches[want] {
			t.Fatalf("default prune matched %#v, want %s", defaultMatches, want)
		}
	}
	for _, notWant := range []string{"session-running", "session-pending", "session-error", "session-foreign"} {
		if defaultMatches[notWant] {
			t.Fatalf("default prune matched %#v, should not include %s", defaultMatches, notWant)
		}
	}

	statusMatches := matched(runPrune("--status", "error"))
	if !reflect.DeepEqual(statusMatches, map[string]bool{"session-error": true}) {
		t.Fatalf("status error matches = %#v", statusMatches)
	}

	agentMatches := matched(runPrune("--agent", "worker"))
	if !agentMatches["session-failed"] || agentMatches["session-error"] || agentMatches["session-pending"] {
		t.Fatalf("agent worker matches = %#v", agentMatches)
	}

	driverMatches := matched(runPrune("--driver", "microsandbox"))
	if !reflect.DeepEqual(driverMatches, map[string]bool{"session-micro": true}) {
		t.Fatalf("driver microsandbox matches = %#v", driverMatches)
	}

	olderOutput := runPrune("--older-than", "24h")
	olderMatches := matched(olderOutput)
	for _, want := range []string{"session-stopped", "session-failed", "session-micro", "session-created-fallback"} {
		if !olderMatches[want] {
			t.Fatalf("older-than matches %#v, want %s", olderMatches, want)
		}
	}
	for _, notWant := range []string{"session-new", "session-bad-time", "session-foreign"} {
		if olderMatches[notWant] {
			t.Fatalf("older-than matches %#v, should not include %s", olderMatches, notWant)
		}
	}
	if len(olderOutput.Warnings) != 1 || !strings.Contains(olderOutput.Warnings[0], "session-bad-time") || !strings.Contains(olderOutput.Warnings[0], "invalid updated_at") {
		t.Fatalf("older-than warnings = %#v", olderOutput.Warnings)
	}
	if removeCalls != 0 {
		t.Fatalf("RemoveSandbox calls = %d, want 0", removeCalls)
	}
}

func TestIntegrationCLISandboxPruneForceRemovesMatchedAndReportsSkipped(t *testing.T) {
	type removeRequest struct {
		sandbox string
		force   bool
	}
	tests := []struct {
		name          string
		failSandbox   string
		wantExitCode  int
		wantRemoved   []string
		wantSkipped   []string
		wantRemoveSeq []removeRequest
	}{
		{
			name:         "all success",
			wantExitCode: 0,
			wantRemoved:  []string{"session-remove-a", "session-remove-b", "session-remove-c"},
			wantRemoveSeq: []removeRequest{
				{sandbox: "session-remove-a"},
				{sandbox: "session-remove-b"},
				{sandbox: "session-remove-c"},
			},
		},
		{
			name:         "partial failure",
			failSandbox:  "session-remove-b",
			wantExitCode: exitCodeGeneral,
			wantRemoved:  []string{"session-remove-a", "session-remove-c"},
			wantSkipped:  []string{"session-remove-b"},
			wantRemoveSeq: []removeRequest{
				{sandbox: "session-remove-a"},
				{sandbox: "session-remove-b"},
				{sandbox: "session-remove-c"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			composePath := writeComposeFile(t, t.TempDir(), `
name: cli-prune-force
agents:
  reviewer:
    provider: codex
  worker:
    provider: codex
`)
			project := testCLIProject("project-cli-prune-force", "cli-prune-force", composePath)
			sessions := []*agentcomposev1.SessionSummary{
				testCLISessionSummary("session-remove-a", "STOPPED", "project-cli-prune-force", "reviewer", ""),
				testCLISessionSummary("session-remove-b", "FAILED", "project-cli-prune-force", "worker", ""),
				testCLISessionSummary("session-running", "RUNNING", "project-cli-prune-force", "reviewer", ""),
				testCLISessionSummary("session-foreign", "STOPPED", "foreign-project", "reviewer", ""),
				testCLISessionSummary("session-remove-c", "STOPPED", "project-cli-prune-force", "worker", ""),
			}
			var removed []removeRequest
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
					listSessions: func(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
						return connect.NewResponse(&agentcomposev1.ListSessionsResponse{Sessions: sessions}), nil
					},
				},
				sandbox: sandboxServiceStub{
					removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
						removed = append(removed, removeRequest{sandbox: req.Msg.GetSandboxId(), force: req.Msg.GetForce()})
						if req.Msg.GetForce() {
							t.Fatalf("sandbox prune RemoveSandbox force = true for %s", req.Msg.GetSandboxId())
						}
						if req.Msg.GetSandboxId() == tc.failSandbox {
							return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete denied"))
						}
						return connect.NewResponse(&agentcomposev2.RemoveSandboxResponse{SandboxId: req.Msg.GetSandboxId(), Removed: true}), nil
					},
				},
			})
			defer server.Close()

			stdout, stderr, _, exitCode := executeCLICommand("sandbox", "prune", "--host", server.URL, "--file", composePath, "--json", "--force")
			if exitCode != tc.wantExitCode {
				t.Fatalf("sandbox prune --force exit code = %d, want %d; stderr=%q", exitCode, tc.wantExitCode, stderr)
			}
			if tc.wantExitCode == 0 && stderr != "" {
				t.Fatalf("sandbox prune --force stderr = %q, want empty", stderr)
			}
			if tc.wantExitCode != 0 && !strings.Contains(stderr, "sandbox prune skipped") {
				t.Fatalf("sandbox prune --force stderr = %q, want skipped summary", stderr)
			}
			var decoded composeSandboxPruneOutput
			if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
				t.Fatalf("sandbox prune --force JSON decode failed: %v\n%s", err, stdout)
			}
			if decoded.DryRun {
				t.Fatalf("sandbox prune --force output is dry-run: %#v", decoded)
			}
			if !reflect.DeepEqual(decoded.Removed, tc.wantRemoved) {
				t.Fatalf("removed = %#v, want %#v", decoded.Removed, tc.wantRemoved)
			}
			var skipped []string
			for _, item := range decoded.Skipped {
				skipped = append(skipped, item.Sandbox)
				if !strings.Contains(item.Reason, "remove failed") {
					t.Fatalf("skipped reason = %q", item.Reason)
				}
				if item.Sandbox == "session-remove-b" && (item.Agent != "worker" || item.Status != "failed") {
					t.Fatalf("skipped metadata = %#v, want worker/failed context", item)
				}
			}
			if !reflect.DeepEqual(skipped, tc.wantSkipped) {
				t.Fatalf("skipped = %#v, want %#v", skipped, tc.wantSkipped)
			}
			if !reflect.DeepEqual(removed, tc.wantRemoveSeq) {
				t.Fatalf("RemoveSandbox calls = %#v, want %#v", removed, tc.wantRemoveSeq)
			}
			for _, item := range decoded.Matched {
				if item.ID == "session-running" || item.ID == "session-foreign" {
					t.Fatalf("matched unsafe/unowned sandbox in forced prune: %#v", decoded.Matched)
				}
			}
		})
	}
}

func TestIntegrationCLISandboxPruneTextOutput(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-prune-text
agents:
  reviewer:
    provider: codex
  worker:
    provider: codex
`)
	project := testCLIProject("project-cli-prune-text", "cli-prune-text", composePath)
	sessions := []*agentcomposev1.SessionSummary{
		testCLISessionSummary("session-text-a", "STOPPED", "project-cli-prune-text", "reviewer", ""),
		testCLISessionSummary("session-text-b", "FAILED", "project-cli-prune-text", "worker", ""),
	}
	var fail bool
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
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
				return connect.NewResponse(&agentcomposev1.ListSessionsResponse{Sessions: sessions}), nil
			},
		},
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				if fail && req.Msg.GetSandboxId() == "session-text-b" {
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete denied"))
				}
				return connect.NewResponse(&agentcomposev2.RemoveSandboxResponse{SandboxId: req.Msg.GetSandboxId(), Removed: true}), nil
			},
		},
	})
	defer server.Close()

	dryOut, dryErr, _, dryCode := executeCLICommand("sandbox", "prune", "--host", server.URL, "--file", composePath)
	if dryCode != 0 || dryErr != "" {
		t.Fatalf("sandbox prune text dry-run code/stderr = %d / %q", dryCode, dryErr)
	}
	for _, want := range []string{"Dry-run: 2 matched, 0 skipped, 2 would be removed.", "Use --force", "Matched:", "SANDBOX", "AGENT", "STATUS", "DRIVER", "UPDATED", "REASON", "session-text", "would remove"} {
		if !strings.Contains(dryOut, want) {
			t.Fatalf("sandbox prune dry-run output %q does not contain %q", dryOut, want)
		}
	}

	forceOut, forceErr, _, forceCode := executeCLICommand("sandbox", "prune", "--host", server.URL, "--file", composePath, "--force")
	if forceCode != 0 || forceErr != "" {
		t.Fatalf("sandbox prune text force code/stderr = %d / %q", forceCode, forceErr)
	}
	for _, want := range []string{"Removed 2 sandbox(es); 2 matched, 0 skipped.", "Removed:", "session-text-a", "session-text-b", "Matched:", "matched"} {
		if !strings.Contains(forceOut, want) {
			t.Fatalf("sandbox prune force output %q does not contain %q", forceOut, want)
		}
	}

	fail = true
	skippedOut, skippedErr, _, skippedCode := executeCLICommand("sandbox", "prune", "--host", server.URL, "--file", composePath, "--force")
	if skippedCode != exitCodeGeneral {
		t.Fatalf("sandbox prune text skipped code = %d, want general; stderr=%q", skippedCode, skippedErr)
	}
	if !strings.Contains(skippedErr, "sandbox prune skipped 1 sandbox") {
		t.Fatalf("sandbox prune text skipped stderr = %q", skippedErr)
	}
	for _, want := range []string{"Removed 1 sandbox(es); 2 matched, 1 skipped.", "Skipped:", "session-text-b", "worker", "failed", "remove failed"} {
		if !strings.Contains(skippedOut, want) {
			t.Fatalf("sandbox prune skipped output %q does not contain %q", skippedOut, want)
		}
	}
}

func TestIntegrationCLISandboxPruneRejectsUnsafeStatuses(t *testing.T) {
	for _, status := range []string{"running", "pending"} {
		t.Run(status, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand("sandbox", "prune", "--status", status)
			if exitCode != exitCodeUsage {
				t.Fatalf("sandbox prune --status %s exit code = %d, want usage; stderr=%q", status, exitCode, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, "sandbox prune cannot target "+status+" sandboxes") {
				t.Fatalf("sandbox prune --status %s stdout/stderr = %q / %q", status, stdout, stderr)
			}
		})
	}
}

func TestIntegrationCLISandboxPruneRejectsInvalidDriver(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("sandbox", "prune", "--driver", "micro-sandbox")
	if exitCode != exitCodeUsage {
		t.Fatalf("sandbox prune --driver invalid exit code = %d, want usage; stderr=%q", exitCode, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, `invalid --driver "micro-sandbox": expected docker, boxlite, or microsandbox`) {
		t.Fatalf("sandbox prune --driver invalid stdout/stderr = %q / %q", stdout, stderr)
	}
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
			for _, want := range []string{
				`project "cli-ps-missing" has not been started on this daemon or was removed by ` + "`agent-compose down`",
				"agent-compose up --file " + composePath,
				"before `agent-compose " + tc.wantCommand + "`",
			} {
				if !strings.Contains(stderr, want) {
					t.Fatalf("%s missing project stderr = %q, want %q", tc.name, stderr, want)
				}
			}
			for _, notWant := range []string{"not_found", "sql: no rows"} {
				if strings.Contains(stderr, notWant) {
					t.Fatalf("%s missing project stderr = %q, should not expose %q", tc.name, stderr, notWant)
				}
			}
		})
	}
}

func TestIntegrationCLIStopSandbox(t *testing.T) {
	var stopped []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		session: sessionServiceStub{
			stopSession: func(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
				stopped = append(stopped, req.Msg.GetSessionId())
				return connect.NewResponse(&agentcomposev1.SessionResponse{}), nil
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

func TestIntegrationCLIResolvesShortResourceIDsBeforeDaemonRequests(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-short-id-demo
agents:
  reviewer:
    provider: codex
`)
	projectID := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sandboxID := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	runID := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	project := testCLIProject(projectID, "cli-short-id-demo", composePath)
	session := testCLISessionSummary(sandboxID, "RUNNING", projectID, "reviewer", runID)
	run := &agentcomposev2.RunSummary{
		RunId:     runID,
		ProjectId: projectID,
		AgentName: "reviewer",
		Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
		SandboxId: sandboxID,
		UpdatedAt: "2026-06-11T00:00:01Z",
	}
	var stopped []string
	var resumed []string
	var execSandbox string
	var runSandbox string
	var inspectedRun string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
		},
		run: runServiceStub{
			listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{run}}), nil
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				inspectedRun = req.Msg.GetRunId()
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(projectID, req.Msg.GetRunId(), "reviewer", sandboxID, agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "ok")}), nil
			},
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				runSandbox = req.Msg.GetSessionId()
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     runID,
					Run:       run,
				})
			},
		},
		session: sessionServiceStub{
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
				return connect.NewResponse(&agentcomposev1.ListSessionsResponse{Sessions: []*agentcomposev1.SessionSummary{session}}), nil
			},
			stopSession: func(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
				stopped = append(stopped, req.Msg.GetSessionId())
				return connect.NewResponse(&agentcomposev1.SessionResponse{}), nil
			},
			resumeSession: func(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
				resumed = append(resumed, req.Msg.GetSessionId())
				return connect.NewResponse(&agentcomposev1.SessionResponse{}), nil
			},
		},
		exec: execServiceStub{
			execStream: func(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
				execSandbox = req.Msg.GetSessionId()
				return stream.Send(&agentcomposev2.ExecStreamResponse{
					EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED,
					Result: &agentcomposev2.ExecResult{
						ExecId:    "exec-short",
						SessionId: req.Msg.GetSessionId(),
						Command:   req.Msg.GetCommand(),
						Success:   true,
					},
				})
			},
		},
	})
	defer server.Close()

	sandboxShort := shortOpaqueID(sandboxID)
	runShort := shortOpaqueID(runID)
	if _, stderr, _, exitCode := executeCLICommand("stop", "--host", server.URL, "--file", composePath, sandboxShort); exitCode != 0 || stderr != "" {
		t.Fatalf("stop short id code/stderr = %d / %q", exitCode, stderr)
	}
	if _, stderr, _, exitCode := executeCLICommand("resume", "--host", server.URL, "--file", composePath, sandboxShort); exitCode != 0 || stderr != "" {
		t.Fatalf("resume short id code/stderr = %d / %q", exitCode, stderr)
	}
	if _, stderr, _, exitCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, sandboxShort, "--command", "true"); exitCode != 0 || stderr != "" {
		t.Fatalf("exec short id code/stderr = %d / %q", exitCode, stderr)
	}
	if _, stderr, _, exitCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "run", runShort); exitCode != 0 || stderr != "" {
		t.Fatalf("inspect run short id code/stderr = %d / %q", exitCode, stderr)
	}
	if _, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--sandbox-id", sandboxShort, "reviewer", "--prompt", "hello"); exitCode != 0 || stderr != "" {
		t.Fatalf("run --sandbox-id short code/stderr = %d / %q", exitCode, stderr)
	}
	if !reflect.DeepEqual(stopped, []string{sandboxID}) || !reflect.DeepEqual(resumed, []string{sandboxID}) || execSandbox != sandboxID || inspectedRun != runID || runSandbox != sandboxID {
		t.Fatalf("resolved ids stopped=%#v resumed=%#v exec=%q inspect=%q run=%q", stopped, resumed, execSandbox, inspectedRun, runSandbox)
	}
}

func TestIntegrationCLIResumeSandboxesJSON(t *testing.T) {
	var resumed []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		session: sessionServiceStub{
			resumeSession: func(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
				resumed = append(resumed, req.Msg.GetSessionId())
				return connect.NewResponse(&agentcomposev1.SessionResponse{}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("resume", "--host", server.URL, "--json", "sandbox-a", "sandbox-b")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("resume --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeSandboxActionOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("resume JSON decode failed: %v\n%s", err, stdout)
	}
	if len(decoded.Results) != 2 ||
		decoded.Results[0].Sandbox != "sandbox-a" ||
		decoded.Results[0].Status != "resumed" ||
		decoded.Results[1].Sandbox != "sandbox-b" ||
		decoded.Results[1].Status != "resumed" {
		t.Fatalf("resume JSON = %#v", decoded)
	}
	if len(resumed) != 2 || resumed[0] != "sandbox-a" || resumed[1] != "sandbox-b" {
		t.Fatalf("resumed sandboxes = %#v", resumed)
	}
}

func TestIntegrationCLIStatsTableAndJSON(t *testing.T) {
	var calls int
	server := newComposeServiceStubServer(t, composeServiceStubs{
		sandbox: sandboxServiceStub{
			getStats: func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxStatsRequest]) (*connect.Response[agentcomposev2.GetSandboxStatsResponse], error) {
				calls++
				if req.Msg.GetSandboxId() != "sandbox-stats" {
					t.Fatalf("GetSandboxStats sandbox = %q", req.Msg.GetSandboxId())
				}
				return connect.NewResponse(&agentcomposev2.GetSandboxStatsResponse{Stats: &agentcomposev2.SandboxStats{
					SandboxId:        req.Msg.GetSandboxId(),
					Driver:           "docker",
					SampledAt:        "2026-07-04T08:00:00Z",
					CpuPercent:       testStatsMetric(12.5, "percent"),
					MemoryUsageBytes: testStatsMetric(512, "bytes"),
					MemoryLimitBytes: &agentcomposev2.MetricValue{Unit: "bytes", Status: agentcomposev2.MetricStatus_METRIC_STATUS_UNKNOWN, Message: "missing"},
					MemoryPercent:    testStatsMetric(25, "percent"),
					NetworkRxBytes:   testStatsMetric(100, "bytes"),
					NetworkTxBytes:   testStatsMetric(200, "bytes"),
					BlockReadBytes:   testStatsMetric(300, "bytes"),
					BlockWriteBytes:  testStatsMetric(400, "bytes"),
					UptimeSeconds:    testStatsMetric(90, "seconds"),
				}}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("stats", "--host", server.URL, "sandbox-stats")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("stats code/stderr = %d / %q", exitCode, stderr)
	}
	for _, want := range []string{"SANDBOX", "sandbox-stat", "docker", "12.50", "512", "-", "90s"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stats output %q does not contain %q", stdout, want)
		}
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("stats", "--host", server.URL, "--json", "sandbox-stats")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("stats --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeStatsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("stats JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.ID != "sandbox-stats" || decoded.Driver != "docker" || decoded.MemoryLimitBytes.Status != "unknown" || decoded.MemoryLimitBytes.Value != nil {
		t.Fatalf("stats JSON = %#v", decoded)
	}
	if decoded.CPUPercent.Value == nil || *decoded.CPUPercent.Value != 12.5 {
		t.Fatalf("stats JSON cpu = %#v", decoded.CPUPercent)
	}
	if calls != 2 {
		t.Fatalf("GetSandboxStats calls = %d, want 2", calls)
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
	sessions := []*agentcomposev1.SessionSummary{
		testCLISessionSummary("session-one", "RUNNING", "project-cli-stats", "reviewer", "run-one"),
		testCLISessionSummary("session-two", "RUNNING", "project-cli-stats", "worker", "run-two"),
		testCLISessionSummary("session-stopped", "STOPPED", "project-cli-stats", "reviewer", "run-stopped"),
		testCLISessionSummary("session-foreign", "RUNNING", "foreign-project", "reviewer", "run-foreign"),
	}
	runs := []*agentcomposev2.RunSummary{
		{RunId: "run-one", ProjectId: project.GetSummary().GetProjectId(), AgentName: "reviewer", Status: agentcomposev2.RunStatus_RUN_STATUS_RUNNING, SessionId: "session-one", UpdatedAt: "2026-06-11T00:00:01Z"},
		{RunId: "run-two", ProjectId: project.GetSummary().GetProjectId(), AgentName: "worker", Status: agentcomposev2.RunStatus_RUN_STATUS_RUNNING, SessionId: "session-two", UpdatedAt: "2026-06-11T00:00:02Z"},
		{RunId: "run-stopped", ProjectId: project.GetSummary().GetProjectId(), AgentName: "reviewer", Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, SessionId: "session-stopped", UpdatedAt: "2026-06-11T00:00:03Z"},
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
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
				if req.Msg.GetLimit() < 100 {
					t.Fatalf("ListSessions request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev1.ListSessionsResponse{Sessions: sessions}), nil
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
	if decoded.Stats[0].ID != "session-one" || decoded.Stats[1].ID != "session-two" {
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
	sessions := []*agentcomposev1.SessionSummary{
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
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
				return connect.NewResponse(&agentcomposev1.ListSessionsResponse{Sessions: sessions}), nil
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

func TestCLIStatsUnsupportedUsesUnsupportedExitCode(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		sandbox: sandboxServiceStub{
			getStats: func(context.Context, *connect.Request[agentcomposev2.GetSandboxStatsRequest]) (*connect.Response[agentcomposev2.GetSandboxStatsResponse], error) {
				return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("sandbox stats are unsupported"))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("stats", "--host", server.URL, "sandbox-stats")
	if exitCode != exitCodeUnsupported {
		t.Fatalf("stats unsupported exit code = %d, want %d; stderr=%q", exitCode, exitCodeUnsupported, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "unsupported") {
		t.Fatalf("stats unsupported stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestIntegrationCLIRemoveSandboxes(t *testing.T) {
	type removedRequest struct {
		sandbox string
		force   bool
	}
	var removed []removedRequest
	server := newComposeServiceStubServer(t, composeServiceStubs{
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				removed = append(removed, removedRequest{sandbox: req.Msg.GetSandboxId(), force: req.Msg.GetForce()})
				return connect.NewResponse(&agentcomposev2.RemoveSandboxResponse{
					SandboxId: req.Msg.GetSandboxId(),
					Removed:   true,
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("rm", "--host", server.URL, "--force", "sandbox-a")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("rm --force code/stderr = %d / %q", exitCode, stderr)
	}
	if stdout != "removed sandbox sandbox-a\n" {
		t.Fatalf("rm --force stdout = %q", stdout)
	}
	if len(removed) != 1 || removed[0].sandbox != "sandbox-a" || !removed[0].force {
		t.Fatalf("removed requests = %#v", removed)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("rm", "--host", server.URL, "--json", "sandbox-b", "sandbox-c")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("rm --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeSandboxActionOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("rm JSON decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Results) != 2 || decoded.Results[0].Sandbox != "sandbox-b" || decoded.Results[0].Status != "removed" || decoded.Results[1].Sandbox != "sandbox-c" {
		t.Fatalf("rm JSON = %#v", decoded)
	}
	if len(removed) != 3 || removed[1].force || removed[2].force {
		t.Fatalf("removed requests after json = %#v", removed)
	}

	sandboxOut, sandboxErr, _, sandboxCode := executeCLICommand("sandbox", "rm", "--host", server.URL, "--force", "sandbox-d")
	if sandboxCode != 0 || sandboxErr != "" {
		t.Fatalf("sandbox rm --force code/stderr = %d / %q", sandboxCode, sandboxErr)
	}
	if sandboxOut != "removed sandbox sandbox-d\n" {
		t.Fatalf("sandbox rm --force stdout = %q", sandboxOut)
	}
	if len(removed) != 4 || removed[3].sandbox != "sandbox-d" || !removed[3].force {
		t.Fatalf("removed requests after sandbox rm = %#v", removed)
	}
}

func TestCLIRemoveSandboxRunningRequiresForce(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s is running", req.Msg.GetSandboxId()))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("rm", "--host", server.URL, "sandbox-running")
	if exitCode == 0 {
		t.Fatalf("rm running exit code = %d, want non-zero", exitCode)
	}
	if stdout != "" || !strings.Contains(stderr, "is running") {
		t.Fatalf("rm running stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestCLIRemoveSandboxUsageErrors(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("rm")
	if exitCode != exitCodeUsage {
		t.Fatalf("rm without args exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" || !strings.Contains(stderr, "requires at least 1 sandbox") {
		t.Fatalf("rm without args stdout/stderr = %q / %q", stdout, stderr)
	}

	stdout, stderr, _, exitCode = executeCLICommand("rm", " ")
	if exitCode != exitCodeUsage {
		t.Fatalf("rm empty sandbox exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" || !strings.Contains(stderr, "requires non-empty sandbox") {
		t.Fatalf("rm empty stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestCLIStopRequiresSandboxUsageError(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("stop")
	if exitCode != exitCodeUsage {
		t.Fatalf("stop without args exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("stop without args stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "requires at least 1 sandbox") {
		t.Fatalf("stop without args stderr = %q", stderr)
	}
}

func TestCLIResumeRejectsEmptySandboxUsageError(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("resume", " ")
	if exitCode != exitCodeUsage {
		t.Fatalf("resume empty sandbox exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("resume empty sandbox stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "requires non-empty sandbox") {
		t.Fatalf("resume empty sandbox stderr = %q", stderr)
	}
}

func TestIntegrationCLIExecStreamsAndSupportsJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-exec-demo
agents:
  reviewer:
    provider: codex
`)
	var sawSelector bool
	var sawSandbox bool
	var sawCommand bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		exec: execServiceStub{
			execStream: func(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
				if req.Msg.GetSessionId() == "sandbox-exec" {
					sawSandbox = true
					if req.Msg.GetCommand().GetCommand() != "bash" || req.Msg.GetCommand().GetArgs()[0] != "-lc" {
						t.Fatalf("ExecStream sandbox request = %#v", req.Msg)
					}
				}
				if req.Msg.GetSessionId() == "sandbox-command" {
					sawCommand = true
					if req.Msg.GetCommand().GetCommand() != "bash" || len(req.Msg.GetCommand().GetArgs()) != 2 || req.Msg.GetCommand().GetArgs()[0] != "-lc" || req.Msg.GetCommand().GetArgs()[1] != "git status --short" {
						t.Fatalf("ExecStream --command request = %#v", req.Msg)
					}
				}
				if selector := req.Msg.GetSelector(); selector != nil {
					sawSelector = true
					if selector.GetAgentName() != "reviewer" || req.Msg.GetCommand().GetCommand() != "bash" || req.Msg.GetCommand().GetArgs()[0] != "-lc" {
						t.Fatalf("ExecStream selector request = %#v", req.Msg)
					}
				}
				if err := stream.Send(&agentcomposev2.ExecStreamResponse{
					EventType:  agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_OUTPUT,
					ExecId:     "exec-cli",
					SessionId:  "session-exec",
					RunId:      "run-exec",
					Transcript: &agentcomposev2.TranscriptEvent{Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDOUT, Text: "exec stdout"},
				}); err != nil {
					return err
				}
				if err := stream.Send(&agentcomposev2.ExecStreamResponse{
					EventType:  agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_OUTPUT,
					ExecId:     "exec-cli",
					SessionId:  "session-exec",
					RunId:      "run-exec",
					Transcript: &agentcomposev2.TranscriptEvent{Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDERR, Text: "exec stderr"},
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.ExecStreamResponse{
					EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED,
					ExecId:    "exec-cli",
					SessionId: "session-exec",
					RunId:     "run-exec",
					Result: &agentcomposev2.ExecResult{
						ExecId:    "exec-cli",
						SessionId: "session-exec",
						RunId:     "run-exec",
						Command:   req.Msg.GetCommand(),
						Cwd:       req.Msg.GetCwd(),
						ExitCode:  0,
						Success:   true,
						Stdout:    "exec stdout\n",
						Stderr:    "exec stderr\n",
						Output:    "exec stdout\nexec stderr\n",
					},
				})
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "--cwd", "/workspace", "--", "sandbox-exec", "bash", "-lc", "pwd")
	if exitCode != 0 {
		t.Fatalf("exec exit code = %d, stderr=%q", exitCode, stderr)
	}
	if stdout != "exec stdout\n" || stderr != "exec stderr\n" {
		t.Fatalf("exec stdout/stderr = %q / %q", stdout, stderr)
	}
	if !sawSandbox {
		t.Fatal("ExecStream sandbox target was not used")
	}

	commandOut, commandErr, _, commandCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "sandbox-command", "--command", "git status --short")
	if commandCode != 0 {
		t.Fatalf("exec --command exit code = %d, stderr=%q", commandCode, commandErr)
	}
	if commandOut != "exec stdout\n" || commandErr != "exec stderr\n" {
		t.Fatalf("exec --command stdout/stderr = %q / %q", commandOut, commandErr)
	}
	if !sawCommand {
		t.Fatal("ExecStream --command target was not used")
	}

	legacyOut, legacyErr, _, legacyCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "--agent", "reviewer", "--cwd", "/workspace", "--", "bash", "-lc", "pwd")
	if legacyCode != 0 {
		t.Fatalf("exec --agent exit code = %d, stderr=%q", legacyCode, legacyErr)
	}
	if legacyOut != "exec stdout\n" || !strings.Contains(legacyErr, "agent-compose exec --agent is deprecated") || !strings.Contains(legacyErr, "exec stderr\n") {
		t.Fatalf("exec --agent stdout/stderr = %q / %q", legacyOut, legacyErr)
	}
	if !sawSelector {
		t.Fatal("ExecStream selector was not used")
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "--json", "session-exec", "bash")
	if jsonCode != 0 {
		t.Fatalf("exec --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	if jsonErr != "" || strings.Contains(jsonOut, "deprecated") {
		t.Fatalf("exec positional json stdout/stderr = %q / %q", jsonOut, jsonErr)
	}
	if strings.Contains(jsonErr, "exec stderr") {
		t.Fatalf("exec --json leaked transcript stdout/stderr = %q / %q", jsonOut, jsonErr)
	}
	var decoded composeExecOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("exec JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.ExecID != "exec-cli" || decoded.SandboxID != "session-exec" || decoded.Stdout != "exec stdout\n" || !decoded.Success {
		t.Fatalf("exec JSON = %#v", decoded)
	}

	legacyExecOut, legacyExecErr, _, legacyExecCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "--json", "--session-id", "session-exec", "--command", "printf alias")
	if legacyExecCode != exitCodeUsage || legacyExecOut != "" || !strings.Contains(legacyExecErr, "unknown flag: --session-id") {
		t.Fatalf("exec --session-id code/stdout/stderr = %d / %q / %q", legacyExecCode, legacyExecOut, legacyExecErr)
	}

	ambiguousOut, ambiguousErr, _, ambiguousCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "sandbox-command", "--command", "pwd", "whoami")
	if ambiguousCode != exitCodeUsage {
		t.Fatalf("exec --command ambiguous exit code = %d, want %d", ambiguousCode, exitCodeUsage)
	}
	if ambiguousOut != "" || !strings.Contains(ambiguousErr, "either with --command or positional arguments") {
		t.Fatalf("exec --command ambiguous stdout/stderr = %q / %q", ambiguousOut, ambiguousErr)
	}
}

func TestCLIExecInteractiveReservedUnsupported(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("exec", "sandbox-1", "-i")
	if exitCode != exitCodeUnsupported {
		t.Fatalf("exec -i exit code = %d, want %d; stderr=%q", exitCode, exitCodeUnsupported, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "exec -i/--interactive is not supported") {
		t.Fatalf("exec -i stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestWriteTranscriptOrChunkRoutesTranscriptAndChunks(t *testing.T) {
	var stdout strings.Builder
	var stderr strings.Builder
	if err := writeTranscriptOrChunk(&stdout, &stderr, &agentcomposev2.TranscriptEvent{Text: "err\n", Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDERR}, "ignored\n", agentcomposev2.StdioStream_STDIO_STREAM_STDOUT); err != nil {
		t.Fatalf("write transcript returned error: %v", err)
	}
	if err := writeTranscriptOrChunk(&stdout, &stderr, nil, "out\n", agentcomposev2.StdioStream_STDIO_STREAM_UNSPECIFIED); err != nil {
		t.Fatalf("write unspecified chunk returned error: %v", err)
	}
	if stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
}

func TestCLIExecRejectsEmptySandboxUsageError(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-exec-empty
agents:
  reviewer:
    provider: codex
`)
	stdout, stderr, _, exitCode := executeCLICommand("exec", "--file", composePath, " ")
	if exitCode != exitCodeUsage {
		t.Fatalf("exec empty sandbox exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("exec empty sandbox stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "requires non-empty sandbox") {
		t.Fatalf("exec empty sandbox stderr = %q", stderr)
	}
}

func TestIntegrationCLIExecAmbiguousSessionIsUsageError(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-exec-ambiguous
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		exec: execServiceStub{
			execStream: func(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("multiple running sessions found for project cli-exec-ambiguous agent reviewer"))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "--agent", "reviewer", "bash")
	if exitCode != exitCodeUsage {
		t.Fatalf("exec ambiguous exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "multiple running sessions") {
		t.Fatalf("exec ambiguous stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestIntegrationCLIInspectProjectAgentRunSandboxSessionJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-inspect-demo
agents:
  reviewer:
    provider: codex
`)
	project := testCLIProject("project-inspect", "cli-inspect-demo", composePath)
	reviewerID, err := domain.StableManagedAgentID(project.GetSummary().GetProjectId(), "reviewer")
	if err != nil {
		t.Fatalf("StableManagedAgentID reviewer returned error: %v", err)
	}
	workerID, err := domain.StableManagedAgentID(project.GetSummary().GetProjectId(), "worker")
	if err != nil {
		t.Fatalf("StableManagedAgentID worker returned error: %v", err)
	}
	project.Agents[0].ManagedAgentId = reviewerID
	project.Agents[1].ManagedAgentId = workerID
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
		},
		run: runServiceStub{
			listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
					RunId:     "run-inspect",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
					SessionId: "session-inspect",
				}}}), nil
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "session-inspect", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "inspect output\n")}), nil
			},
		},
		session: sessionServiceStub{
			getSession: func(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
				return connect.NewResponse(&agentcomposev1.SessionResponse{Session: testCLISessionDetail(req.Msg.GetSessionId(), "RUNNING")}), nil
			},
		},
	})
	defer server.Close()

	projectOut, projectErr, _, projectCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "project")
	if projectCode != 0 || projectErr != "" {
		t.Fatalf("inspect project code/stderr = %d / %q", projectCode, projectErr)
	}
	var projectDecoded composeProjectOutput
	if err := json.Unmarshal([]byte(projectOut), &projectDecoded); err != nil {
		t.Fatalf("inspect project JSON decode failed: %v\n%s", err, projectOut)
	}
	if projectDecoded.Project.Name != "cli-inspect-demo" || len(projectDecoded.Agents) != 2 || len(projectDecoded.Schedulers) != 1 {
		t.Fatalf("inspect project JSON = %#v", projectDecoded)
	}

	agentOut, agentErr, _, agentCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "agent", identity.ShortID(reviewerID))
	if agentCode != 0 || agentErr != "" {
		t.Fatalf("inspect agent code/stderr = %d / %q", agentCode, agentErr)
	}
	var agentDecoded composeAgentInspectOutput
	if err := json.Unmarshal([]byte(agentOut), &agentDecoded); err != nil {
		t.Fatalf("inspect agent JSON decode failed: %v\n%s", err, agentOut)
	}
	if agentDecoded.Agent.Name != "reviewer" || agentDecoded.LatestRun.ID != "run-inspect" || len(agentDecoded.RunningSandboxes) != 1 {
		t.Fatalf("inspect agent JSON = %#v", agentDecoded)
	}

	runOut, runErr, _, runCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "run", "run-inspect")
	if runCode != 0 || runErr != "" {
		t.Fatalf("inspect run code/stderr = %d / %q", runCode, runErr)
	}
	var runDecoded composeRunOutput
	if err := json.Unmarshal([]byte(runOut), &runDecoded); err != nil {
		t.Fatalf("inspect run JSON decode failed: %v\n%s", err, runOut)
	}
	if runDecoded.ID != "run-inspect" || runDecoded.Status != "running" || runDecoded.SandboxID != "session-inspect" {
		t.Fatalf("inspect run JSON = %#v", runDecoded)
	}

	sandboxOut, sandboxErr, _, sandboxCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "sandbox", "session-inspect")
	if sandboxCode != 0 || sandboxErr != "" {
		t.Fatalf("inspect sandbox code/stderr = %d / %q", sandboxCode, sandboxErr)
	}
	var sandboxDecoded composeSessionOutput
	if err := json.Unmarshal([]byte(sandboxOut), &sandboxDecoded); err != nil {
		t.Fatalf("inspect sandbox JSON decode failed: %v\n%s", err, sandboxOut)
	}
	if sandboxDecoded.ID != "session-inspect" || sandboxDecoded.VMStatus != "running" || sandboxDecoded.Tags["project"] == "" {
		t.Fatalf("inspect sandbox JSON = %#v", sandboxDecoded)
	}

	sessionOut, sessionErr, _, sessionCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "session", "session-inspect")
	if sessionCode != 0 {
		t.Fatalf("inspect session code = %d; stderr = %q", sessionCode, sessionErr)
	}
	if !strings.Contains(sessionErr, "deprecated") || !strings.Contains(sessionErr, "will be removed") || !strings.Contains(sessionErr, "agent-compose inspect sandbox") {
		t.Fatalf("inspect session stderr missing deprecated warning: %q", sessionErr)
	}
	var sessionDecoded composeSessionOutput
	if err := json.Unmarshal([]byte(sessionOut), &sessionDecoded); err != nil {
		t.Fatalf("inspect session JSON decode failed: %v\n%s", err, sessionOut)
	}
	if sessionDecoded.ID != "session-inspect" || sessionDecoded.VMStatus != "running" || sessionDecoded.Tags["project"] == "" {
		t.Fatalf("inspect session JSON = %#v", sessionDecoded)
	}
}

func TestIntegrationCLIImagesAliasesAndJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			listImages: func(ctx context.Context, req *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error) {
				calls++
				if calls == 1 && (req.Msg.GetQuery() != "agent" || !req.Msg.GetAll()) {
					t.Fatalf("ListImages request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.ListImagesResponse{
					Images:     []*agentcomposev2.Image{testCLIImage("sha256:abc1234567890", "agent:latest")},
					TotalCount: 1,
					StoreStatus: &agentcomposev2.ImageStoreStatus{
						Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
						Available: true,
						Endpoint:  "unix:///var/run/docker.sock",
					},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("images", "--host", server.URL, "--json", "--query", "agent", "--all")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("images --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeImageListOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("images JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.TotalCount != 1 || decoded.Images[0].ImageRef != "agent:latest" || decoded.StoreStatus.Store != "docker" {
		t.Fatalf("images JSON = %#v", decoded)
	}

	textOut, textErr, _, textCode := executeCLICommand("image", "ls", "--host", server.URL)
	if textCode != 0 {
		t.Fatalf("image ls code/stderr = %d / %q", textCode, textErr)
	}
	assertDeprecatedWarning(t, textErr, "agent-compose images")
	for _, want := range []string{"IMAGE ID", "REF", "DISK USAGE", "abc123456789", "agent:latest", "1.0KB"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("image ls output %q does not contain %q", textOut, want)
		}
	}
	for _, notWant := range []string{"STORE", "STATUS", "CONTENT SIZE", "docker", "available"} {
		if strings.Contains(textOut, notWant) {
			t.Fatalf("image ls default output %q contains %q", textOut, notWant)
		}
	}

	verboseOut, verboseErr, _, verboseCode := executeCLICommand("images", "--host", server.URL, "--verbose")
	if verboseCode != 0 || verboseErr != "" {
		t.Fatalf("images --verbose code/stderr = %d / %q", verboseCode, verboseErr)
	}
	for _, want := range []string{"REF", "IMAGE ID", "STORE", "STATUS", "PLATFORM", "DISK USAGE", "CONTENT SIZE", "CREATED", "docker", "available", "linux/amd64"} {
		if !strings.Contains(verboseOut, want) {
			t.Fatalf("images --verbose output %q does not contain %q", verboseOut, want)
		}
	}
	if calls != 3 {
		t.Fatalf("ListImages calls = %d, want 3", calls)
	}
}

func TestIntegrationCLICacheListTextJSONAndFilters(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		cache: cacheServiceStub{
			listCaches: func(ctx context.Context, req *connect.Request[agentcomposev2.ListCachesRequest]) (*connect.Response[agentcomposev2.ListCachesResponse], error) {
				calls++
				filter := req.Msg.GetFilter()
				switch calls {
				case 1:
					if filter.GetDriver() != "boxlite" || filter.GetType() != "materialized" || filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED {
						t.Fatalf("ListCaches JSON filter = %#v", filter)
					}
				case 2:
					if filter.GetDriver() != "all" || filter.GetType() != "" || filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED {
						t.Fatalf("ListCaches text filter = %#v", filter)
					}
				default:
					t.Fatalf("unexpected ListCaches call %d", calls)
				}
				return connect.NewResponse(&agentcomposev2.ListCachesResponse{
					Caches:   []*agentcomposev2.CacheItem{testCLICache("cache-materialized-1")},
					Warnings: []string{"scan warning"},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("cache", "ls", "--host", server.URL, "--json", "--driver", "boxlite", "--type", "materialized", "--status", "orphaned")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("cache ls --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeCacheListOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("cache ls JSON decode failed: %v\n%s", err, stdout)
	}
	if len(decoded.Caches) != 1 || decoded.Caches[0].ID != "cache-materialized-1" || decoded.Caches[0].Type != "materialized" || decoded.Warnings[0] != "scan warning" {
		t.Fatalf("cache ls JSON = %#v", decoded)
	}

	textOut, textErr, _, textCode := executeCLICommand("cache", "ls", "--host", server.URL, "--driver", "all")
	if textCode != 0 || textErr != "" {
		t.Fatalf("cache ls text code/stderr = %d / %q", textCode, textErr)
	}
	for _, want := range []string{"CACHE ID", "cache-materi", "boxlite", "materialized", "orphaned", "/tmp/cache/rootfs"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("cache ls text %q does not contain %q", textOut, want)
		}
	}
	if calls != 2 {
		t.Fatalf("ListCaches calls = %d, want 2", calls)
	}
}

func TestIntegrationCLIVolumeCommands(t *testing.T) {
	var listCalls int
	var createdLabel string
	var removed []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		volume: volumeServiceStub{
			listVolumes: func(ctx context.Context, req *connect.Request[agentcomposev2.ListVolumesRequest]) (*connect.Response[agentcomposev2.ListVolumesResponse], error) {
				listCalls++
				if req.Msg.GetQuery() != "cac" || req.Msg.GetDriver() != "local" {
					t.Fatalf("ListVolumes request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.ListVolumesResponse{Volumes: []*agentcomposev2.Volume{testCLIVolume("cache")}}), nil
			},
			createVolume: func(ctx context.Context, req *connect.Request[agentcomposev2.CreateVolumeRequest]) (*connect.Response[agentcomposev2.CreateVolumeResponse], error) {
				if req.Msg.GetName() != "cache" || req.Msg.GetDriver() != "local" || req.Msg.GetLabels()["purpose"] != "cache" || req.Msg.GetOptions()["quota"] != "1g" {
					t.Fatalf("CreateVolume request = %#v", req.Msg)
				}
				createdLabel = req.Msg.GetLabels()["purpose"]
				return connect.NewResponse(&agentcomposev2.CreateVolumeResponse{Volume: testCLIVolume("cache"), Created: true}), nil
			},
			inspectVolume: func(ctx context.Context, req *connect.Request[agentcomposev2.InspectVolumeRequest]) (*connect.Response[agentcomposev2.InspectVolumeResponse], error) {
				if req.Msg.GetName() != "cache" {
					t.Fatalf("InspectVolume request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.InspectVolumeResponse{Volume: testCLIVolume("cache")}), nil
			},
			removeVolume: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveVolumeRequest]) (*connect.Response[agentcomposev2.RemoveVolumeResponse], error) {
				if !req.Msg.GetForce() {
					t.Fatalf("RemoveVolume force = false")
				}
				removed = append(removed, req.Msg.GetName())
				return connect.NewResponse(&agentcomposev2.RemoveVolumeResponse{Name: req.Msg.GetName(), Removed: true}), nil
			},
			pruneVolumes: func(ctx context.Context, req *connect.Request[agentcomposev2.PruneVolumesRequest]) (*connect.Response[agentcomposev2.PruneVolumesResponse], error) {
				if !req.Msg.GetForce() || req.Msg.GetDriver() != "local" {
					t.Fatalf("PruneVolumes request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.PruneVolumesResponse{
					Removed: []*agentcomposev2.Volume{testCLIVolume("cache")},
					Matched: []*agentcomposev2.Volume{testCLIVolume("cache")},
				}), nil
			},
		},
	})
	defer server.Close()

	listOut, listErr, _, listCode := executeCLICommand("volume", "ls", "--host", server.URL, "--json", "--query", "cac", "--driver", "local")
	if listCode != 0 || listErr != "" {
		t.Fatalf("volume ls code/stderr = %d / %q", listCode, listErr)
	}
	var listDecoded composeVolumeListOutput
	if err := json.Unmarshal([]byte(listOut), &listDecoded); err != nil {
		t.Fatalf("volume ls JSON decode failed: %v\n%s", err, listOut)
	}
	if len(listDecoded.Volumes) != 1 || listDecoded.Volumes[0].Name != "cache" {
		t.Fatalf("volume ls JSON = %#v", listDecoded)
	}

	createOut, createErr, _, createCode := executeCLICommand("volume", "create", "--host", server.URL, "--label", "purpose=cache", "--opt", "quota=1g", "cache")
	if createCode != 0 || createErr != "" || strings.TrimSpace(createOut) != "cache" || createdLabel != "cache" {
		t.Fatalf("volume create code/stdout/stderr = %d / %q / %q label=%q", createCode, createOut, createErr, createdLabel)
	}

	inspectOut, inspectErr, _, inspectCode := executeCLICommand("inspect", "volume", "--host", server.URL, "cache")
	if inspectCode != 0 || inspectErr != "" || !strings.Contains(inspectOut, "Name: cache") || strings.Contains(inspectOut, "Volume ID") || !strings.Contains(inspectOut, "Labels") {
		t.Fatalf("inspect volume code/stdout/stderr = %d / %q / %q", inspectCode, inspectOut, inspectErr)
	}

	removeOut, removeErr, _, removeCode := executeCLICommand("volume", "rm", "--host", server.URL, "--force", "cache", "state")
	if removeCode != 0 || removeErr != "" || !strings.Contains(removeOut, "cache") || !strings.Contains(removeOut, "state") || len(removed) != 2 {
		t.Fatalf("volume rm code/stdout/stderr removed = %d / %q / %q / %#v", removeCode, removeOut, removeErr, removed)
	}

	pruneOut, pruneErr, _, pruneCode := executeCLICommand("volume", "prune", "--host", server.URL, "--driver", "local", "--force")
	if pruneCode != 0 || pruneErr != "" || !strings.Contains(pruneOut, "Removed 1 volume") || listCalls != 1 {
		t.Fatalf("volume prune code/stdout/stderr/listCalls = %d / %q / %q / %d", pruneCode, pruneOut, pruneErr, listCalls)
	}
}

func TestIntegrationCLICacheListFilterValuesAndUsageErrors(t *testing.T) {
	tests := []struct {
		name   string
		flag   string
		values []string
		assert func(*testing.T, *agentcomposev2.CacheFilter, string)
	}{
		{
			name:   "driver",
			flag:   "--driver",
			values: []string{"docker", "boxlite", "microsandbox", "all"},
			assert: func(t *testing.T, filter *agentcomposev2.CacheFilter, value string) {
				t.Helper()
				if filter.GetDriver() != value {
					t.Fatalf("driver filter = %q, want %q", filter.GetDriver(), value)
				}
			},
		},
		{
			name:   "type",
			flag:   "--type",
			values: []string{"oci", "materialized", "runtime", "sandbox", "session"},
			assert: func(t *testing.T, filter *agentcomposev2.CacheFilter, value string) {
				t.Helper()
				want := value
				if value == "sandbox" {
					want = "session"
				}
				if filter.GetType() != want {
					t.Fatalf("type filter = %q, want %q", filter.GetType(), want)
				}
			},
		},
		{
			name:   "status",
			flag:   "--status",
			values: []string{"active", "referenced", "unused", "expired", "orphaned", "unknown"},
			assert: func(t *testing.T, filter *agentcomposev2.CacheFilter, value string) {
				t.Helper()
				if cacheStatusText(filter.GetStatus()) != value {
					t.Fatalf("status filter = %s, want %q", filter.GetStatus(), value)
				}
			},
		},
	}
	for _, tc := range tests {
		for _, value := range tc.values {
			t.Run(tc.name+"_"+value, func(t *testing.T) {
				calls := 0
				server := newComposeServiceStubServer(t, composeServiceStubs{
					cache: cacheServiceStub{
						listCaches: func(ctx context.Context, req *connect.Request[agentcomposev2.ListCachesRequest]) (*connect.Response[agentcomposev2.ListCachesResponse], error) {
							calls++
							tc.assert(t, req.Msg.GetFilter(), value)
							return connect.NewResponse(&agentcomposev2.ListCachesResponse{}), nil
						},
					},
				})
				defer server.Close()

				stdout, stderr, _, exitCode := executeCLICommand("cache", "ls", "--host", server.URL, tc.flag, value)
				if exitCode != 0 || stderr != "" {
					t.Fatalf("cache ls %s %s code/stderr = %d / %q", tc.flag, value, exitCode, stderr)
				}
				if !strings.Contains(stdout, "CACHE ID") {
					t.Fatalf("cache ls %s %s stdout = %q", tc.flag, value, stdout)
				}
				if calls != 1 {
					t.Fatalf("ListCaches calls = %d, want 1", calls)
				}
			})
		}
	}

	invalid := []struct {
		args []string
		want string
	}{
		{args: []string{"cache", "ls", "--driver", "podman"}, want: "invalid --driver"},
		{args: []string{"cache", "ls", "--type", "blob"}, want: "invalid --type"},
		{args: []string{"cache", "ls", "--status", "deleted"}, want: "invalid --status"},
	}
	for _, tc := range invalid {
		stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
		if exitCode != exitCodeUsage {
			t.Fatalf("%v exit code = %d, want usage; stderr=%q", tc.args, exitCode, stderr)
		}
		if stdout != "" {
			t.Fatalf("%v stdout = %q, want empty", tc.args, stdout)
		}
		if !strings.Contains(stderr, tc.want) {
			t.Fatalf("%v stderr = %q, want %q", tc.args, stderr, tc.want)
		}
	}
}

func TestIntegrationCLICacheInspectTextJSONAndNotFound(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		cache: cacheServiceStub{
			inspectCache: func(ctx context.Context, req *connect.Request[agentcomposev2.InspectCacheRequest]) (*connect.Response[agentcomposev2.InspectCacheResponse], error) {
				calls++
				switch req.Msg.GetCacheId() {
				case "cache-materialized-1":
					return connect.NewResponse(&agentcomposev2.InspectCacheResponse{
						Cache:    testCLICache(req.Msg.GetCacheId()),
						Warnings: []string{"top warning"},
					}), nil
				case "missing-cache":
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("cache not found"))
				default:
					t.Fatalf("unexpected InspectCache cache_id = %q", req.Msg.GetCacheId())
					return nil, nil
				}
			},
		},
	})
	defer server.Close()

	textOut, textErr, _, textCode := executeCLICommand("cache", "inspect", "--host", server.URL, "cache-materialized-1")
	if textCode != 0 || textErr != "" {
		t.Fatalf("cache inspect text code/stderr = %d / %q", textCode, textErr)
	}
	for _, want := range []string{"Cache ID: cache-materialized-1", "Domain: materialized-image-cache", "References:", "Blocked reasons:", "top warning"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("cache inspect text %q does not contain %q", textOut, want)
		}
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("cache", "inspect", "--host", server.URL, "--json", "cache-materialized-1")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("cache inspect JSON code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeCacheInspectOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("cache inspect JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.Cache.ID != "cache-materialized-1" || decoded.Cache.Status != "orphaned" || decoded.Warnings[0] != "top warning" {
		t.Fatalf("cache inspect JSON = %#v", decoded)
	}

	genericOut, genericErr, _, genericCode := executeCLICommand("inspect", "--host", server.URL, "--json", "cache", "cache-materialized-1")
	if genericCode != 0 || genericErr != "" {
		t.Fatalf("inspect cache JSON code/stderr = %d / %q", genericCode, genericErr)
	}
	var genericDecoded composeCacheInspectOutput
	if err := json.Unmarshal([]byte(genericOut), &genericDecoded); err != nil {
		t.Fatalf("inspect cache JSON decode failed: %v\n%s", err, genericOut)
	}
	if genericDecoded.Cache.ID != "cache-materialized-1" {
		t.Fatalf("inspect cache JSON = %#v", genericDecoded)
	}

	missingOut, missingErr, _, missingCode := executeCLICommand("cache", "inspect", "--host", server.URL, "missing-cache")
	if missingCode != exitCodeUsage {
		t.Fatalf("cache inspect missing exit code = %d, want usage; stderr=%q", missingCode, missingErr)
	}
	if missingOut != "" {
		t.Fatalf("cache inspect missing stdout = %q, want empty", missingOut)
	}
	if !strings.Contains(missingErr, "inspect cache missing-cache") || !strings.Contains(missingErr, "not_found") {
		t.Fatalf("cache inspect missing stderr = %q", missingErr)
	}
	if calls != 4 {
		t.Fatalf("InspectCache calls = %d, want 4", calls)
	}
}

func TestCLICacheInspectUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: []string{"cache", "inspect"}, want: "accepts 1 arg(s), received 0"},
		{name: "extra", args: []string{"cache", "inspect", "cache-id", "extra"}, want: "accepts 1 arg(s), received 2"},
		{name: "empty", args: []string{"cache", "inspect", ""}, want: "cache inspect requires a cache id"},
		{name: "generic missing", args: []string{"inspect", "cache"}, want: "inspect cache requires a cache id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("%v exit code = %d, want usage; stderr=%q", tc.args, exitCode, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want empty", tc.args, stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v stderr = %q, want %q", tc.args, stderr, tc.want)
			}
		})
	}
}

func TestParseOlderThanSeconds(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    uint64
		wantErr string
	}{
		{name: "empty", value: "", want: 0},
		{name: "days", value: "7d", want: 7 * 24 * 3600},
		{name: "hours", value: "168h", want: 7 * 24 * 3600},
		{name: "fractional seconds", value: "1500ms", want: 1},
		{name: "invalid", value: "later", wantErr: "expected a positive duration"},
		{name: "zero", value: "0", wantErr: "duration must be positive"},
		{name: "negative", value: "-1h", wantErr: "duration must be positive"},
		{name: "subsecond", value: "500ms", wantErr: "duration must be at least 1s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOlderThanSeconds(tc.value)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseOlderThanSeconds(%q) error = %v, want %q", tc.value, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOlderThanSeconds(%q) unexpected error: %v", tc.value, err)
			}
			if got != tc.want {
				t.Fatalf("parseOlderThanSeconds(%q) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestComposeSandboxPruneOutputJSONShape(t *testing.T) {
	output := composeSandboxPruneOutput{
		DryRun: true,
		Matched: []composePSSandboxOutput{{
			ID:        "sandbox-match",
			ShortID:   "sandbox-match",
			Agent:     "worker",
			Status:    "stopped",
			UpdatedAt: "2026-06-11T00:00:00Z",
			Driver:    "boxlite",
		}},
		Removed: []string{"sandbox-removed"},
		Skipped: []composeSandboxPruneSkipped{{
			Sandbox:   "sandbox-skipped",
			Agent:     "worker",
			Status:    "failed",
			UpdatedAt: "2026-06-11T00:00:00Z",
			Driver:    "boxlite",
			Reason:    "remove failed: denied",
		}},
		Warnings: []string{"scan warning"},
	}
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal composeSandboxPruneOutput: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode composeSandboxPruneOutput JSON: %v\n%s", err, data)
	}
	for _, key := range []string{"dry_run", "matched", "removed", "skipped", "warnings"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("composeSandboxPruneOutput JSON %s missing key %q", data, key)
		}
	}
	if strings.Contains(string(data), "DryRun") || strings.Contains(string(data), "Sandbox") || strings.Contains(string(data), "Reason") {
		t.Fatalf("composeSandboxPruneOutput JSON uses Go field names: %s", data)
	}
	if !strings.Contains(string(data), `"updated_at"`) {
		t.Fatalf("composeSandboxPruneOutput JSON missing skipped metadata: %s", data)
	}
}

func TestIntegrationCLICachePruneDryRunForceAndJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		cache: cacheServiceStub{
			pruneCaches: func(ctx context.Context, req *connect.Request[agentcomposev2.PruneCachesRequest]) (*connect.Response[agentcomposev2.PruneCachesResponse], error) {
				calls++
				switch calls {
				case 1:
					if req.Msg.GetForce() || req.Msg.GetIncludeReferenced() {
						t.Fatalf("PruneCaches dry-run flags = force:%t include:%t", req.Msg.GetForce(), req.Msg.GetIncludeReferenced())
					}
					filter := req.Msg.GetFilter()
					if filter.GetDriver() != "boxlite" || filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED {
						t.Fatalf("PruneCaches dry-run filter = %#v", filter)
					}
					return connect.NewResponse(&agentcomposev2.PruneCachesResponse{
						DryRun:   true,
						Matched:  []*agentcomposev2.CacheItem{testCLICache("cache-dry-run")},
						Skipped:  []*agentcomposev2.CacheItem{testCLICache("cache-protected")},
						Warnings: []string{"scan warning"},
					}), nil
				case 2:
					if !req.Msg.GetForce() || !req.Msg.GetIncludeReferenced() {
						t.Fatalf("PruneCaches force flags = force:%t include:%t", req.Msg.GetForce(), req.Msg.GetIncludeReferenced())
					}
					filter := req.Msg.GetFilter()
					if filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED || filter.GetOlderThanSeconds() != 7*24*3600 {
						t.Fatalf("PruneCaches force filter = %#v", filter)
					}
					return connect.NewResponse(&agentcomposev2.PruneCachesResponse{
						DryRun:  false,
						Matched: []*agentcomposev2.CacheItem{testCLICache("cache-removed")},
						Removed: []string{"cache-removed"},
					}), nil
				default:
					t.Fatalf("unexpected PruneCaches call %d", calls)
					return nil, nil
				}
			},
		},
	})
	defer server.Close()

	textOut, textErr, _, textCode := executeCLICommand("cache", "prune", "--host", server.URL, "--driver", "boxlite", "--unused")
	if textCode != 0 || textErr != "" {
		t.Fatalf("cache prune dry-run code/stderr = %d / %q", textCode, textErr)
	}
	for _, want := range []string{"Dry-run", "cache-dry-run", "cache-protected", "scan warning"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("cache prune dry-run stdout %q does not contain %q", textOut, want)
		}
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("cache", "prune", "--host", server.URL, "--json", "--force", "--include-referenced", "--orphaned", "--older-than", "7d")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("cache prune force JSON code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeCacheOperationOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("cache prune JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.DryRun || len(decoded.Removed) != 1 || decoded.Removed[0] != "cache-removed" {
		t.Fatalf("cache prune JSON = %#v", decoded)
	}
	if calls != 2 {
		t.Fatalf("PruneCaches calls = %d, want 2", calls)
	}
}

func TestIntegrationCLICachePruneFilterMappings(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		assert func(*testing.T, *agentcomposev2.PruneCachesRequest)
	}{
		{
			name: "unused",
			args: []string{"--unused"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				if req.GetFilter().GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED {
					t.Fatalf("status = %s, want unused", req.GetFilter().GetStatus())
				}
			},
		},
		{
			name: "orphaned",
			args: []string{"--orphaned"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				if req.GetFilter().GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED {
					t.Fatalf("status = %s, want orphaned", req.GetFilter().GetStatus())
				}
			},
		},
		{
			name: "expired",
			args: []string{"--expired"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				if req.GetFilter().GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED {
					t.Fatalf("status = %s, want expired", req.GetFilter().GetStatus())
				}
			},
		},
		{
			name: "older than days",
			args: []string{"--older-than", "7d"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				if req.GetFilter().GetOlderThanSeconds() != 7*24*3600 {
					t.Fatalf("older_than_seconds = %d, want 604800", req.GetFilter().GetOlderThanSeconds())
				}
			},
		},
		{
			name: "older than hours and include referenced",
			args: []string{"--older-than", "168h", "--include-referenced"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				if req.GetFilter().GetOlderThanSeconds() != 7*24*3600 || !req.GetIncludeReferenced() {
					t.Fatalf("request = %#v", req)
				}
			},
		},
		{
			name: "common filters",
			args: []string{"--driver", "microsandbox", "--type", "sandbox", "--status", "unknown"},
			assert: func(t *testing.T, req *agentcomposev2.PruneCachesRequest) {
				t.Helper()
				filter := req.GetFilter()
				if filter.GetDriver() != "microsandbox" || filter.GetType() != "session" || filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_UNKNOWN {
					t.Fatalf("filter = %#v", filter)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := newComposeServiceStubServer(t, composeServiceStubs{
				cache: cacheServiceStub{
					pruneCaches: func(ctx context.Context, req *connect.Request[agentcomposev2.PruneCachesRequest]) (*connect.Response[agentcomposev2.PruneCachesResponse], error) {
						calls++
						tc.assert(t, req.Msg)
						return connect.NewResponse(&agentcomposev2.PruneCachesResponse{DryRun: true}), nil
					},
				},
			})
			defer server.Close()
			args := append([]string{"cache", "prune", "--host", server.URL}, tc.args...)
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != 0 || stderr != "" {
				t.Fatalf("cache prune %v code/stderr = %d / %q", tc.args, exitCode, stderr)
			}
			if !strings.Contains(stdout, "Dry-run") {
				t.Fatalf("cache prune stdout = %q", stdout)
			}
			if calls != 1 {
				t.Fatalf("PruneCaches calls = %d, want 1", calls)
			}
		})
	}
}

func TestCLICachePruneUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "invalid duration", args: []string{"cache", "prune", "--older-than", "bogus"}, want: "invalid --older-than"},
		{name: "zero duration", args: []string{"cache", "prune", "--older-than", "0s"}, want: "duration must be positive"},
		{name: "negative duration", args: []string{"cache", "prune", "--older-than", "-1h"}, want: "duration must be positive"},
		{name: "subsecond duration", args: []string{"cache", "prune", "--older-than", "500ms"}, want: "at least 1s"},
		{name: "shortcut conflict", args: []string{"cache", "prune", "--unused", "--orphaned"}, want: "mutually exclusive"},
		{name: "shortcut status conflict", args: []string{"cache", "prune", "--unused", "--status", "orphaned"}, want: "cannot be combined with --status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("%v exit code = %d, want usage; stderr=%q", tc.args, exitCode, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want empty", tc.args, stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v stderr = %q, want %q", tc.args, stderr, tc.want)
			}
		})
	}
}

func TestIntegrationCLICacheRemoveDryRunForceProtectedAndJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		cache: cacheServiceStub{
			removeCache: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveCacheRequest]) (*connect.Response[agentcomposev2.RemoveCacheResponse], error) {
				calls++
				switch req.Msg.GetCacheId() {
				case "cache-dry-run":
					if req.Msg.GetForce() {
						t.Fatalf("RemoveCache dry-run force = true")
					}
					return connect.NewResponse(&agentcomposev2.RemoveCacheResponse{
						DryRun:  true,
						Matched: []*agentcomposev2.CacheItem{testCLICache("cache-dry-run")},
					}), nil
				case "cache-remove":
					if !req.Msg.GetForce() {
						t.Fatalf("RemoveCache force = false")
					}
					return connect.NewResponse(&agentcomposev2.RemoveCacheResponse{
						DryRun:  false,
						Matched: []*agentcomposev2.CacheItem{testCLICache("cache-remove")},
						Removed: []string{"cache-remove"},
					}), nil
				case "cache-protected":
					protected := testCLICache("cache-protected")
					protected.Status = agentcomposev2.CacheStatus_CACHE_STATUS_ACTIVE
					protected.Removable = false
					protected.BlockedReasons = []string{"cache is active"}
					return connect.NewResponse(&agentcomposev2.RemoveCacheResponse{
						DryRun:   false,
						Matched:  []*agentcomposev2.CacheItem{protected},
						Skipped:  []*agentcomposev2.CacheItem{protected},
						Warnings: []string{"cache is active"},
					}), nil
				case "cache-connect-protected":
					return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cache is protected"))
				case "cache-remove-failed":
					failed := testCLICache("cache-remove-failed")
					failed.Removable = false
					failed.BlockedReasons = []string{"remove failed"}
					return connect.NewResponse(&agentcomposev2.RemoveCacheResponse{
						DryRun:   false,
						Matched:  []*agentcomposev2.CacheItem{failed},
						Skipped:  []*agentcomposev2.CacheItem{failed},
						Warnings: []string{"remove cache-remove-failed: permission denied"},
					}), nil
				default:
					t.Fatalf("unexpected RemoveCache cache_id = %q", req.Msg.GetCacheId())
					return nil, nil
				}
			},
		},
	})
	defer server.Close()

	textOut, textErr, _, textCode := executeCLICommand("cache", "rm", "--host", server.URL, "cache-dry-run")
	if textCode != 0 || textErr != "" {
		t.Fatalf("cache rm dry-run code/stderr = %d / %q", textCode, textErr)
	}
	if !strings.Contains(textOut, "Dry-run") || !strings.Contains(textOut, "cache-dry-run") {
		t.Fatalf("cache rm dry-run stdout = %q", textOut)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("cache", "rm", "--host", server.URL, "--json", "--force", "cache-remove")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("cache rm force JSON code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeCacheOperationOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("cache rm JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.DryRun || len(decoded.Removed) != 1 || decoded.Removed[0] != "cache-remove" {
		t.Fatalf("cache rm JSON = %#v", decoded)
	}

	protectedOut, protectedErr, _, protectedCode := executeCLICommand("cache", "rm", "--host", server.URL, "--force", "cache-protected")
	if protectedCode != exitCodeUsage {
		t.Fatalf("cache rm protected exit code = %d, want usage; stderr=%q", protectedCode, protectedErr)
	}
	if !strings.Contains(protectedOut, "Skipped") || !strings.Contains(protectedOut, "cache-protected") {
		t.Fatalf("cache rm protected stdout = %q", protectedOut)
	}
	if !strings.Contains(protectedErr, "cache is active") {
		t.Fatalf("cache rm protected stderr = %q", protectedErr)
	}

	failedOut, failedErr, _, failedCode := executeCLICommand("cache", "rm", "--host", server.URL, "--force", "cache-remove-failed")
	if failedCode != exitCodeUsage {
		t.Fatalf("cache rm remove-failed exit code = %d, want usage; stderr=%q", failedCode, failedErr)
	}
	if !strings.Contains(failedOut, "Skipped") || !strings.Contains(failedOut, "cache-remove-failed") {
		t.Fatalf("cache rm remove-failed stdout = %q", failedOut)
	}
	if !strings.Contains(failedErr, "permission denied") {
		t.Fatalf("cache rm remove-failed stderr = %q", failedErr)
	}

	connectOut, connectErr, _, connectCode := executeCLICommand("cache", "rm", "--host", server.URL, "--force", "cache-connect-protected")
	if connectCode != exitCodeUsage {
		t.Fatalf("cache rm connect protected exit code = %d, want usage; stderr=%q", connectCode, connectErr)
	}
	if connectOut != "" {
		t.Fatalf("cache rm connect protected stdout = %q, want empty", connectOut)
	}
	if !strings.Contains(connectErr, "failed_precondition") {
		t.Fatalf("cache rm connect protected stderr = %q", connectErr)
	}
	if calls != 5 {
		t.Fatalf("RemoveCache calls = %d, want 5", calls)
	}
}

func TestCLICacheRemoveUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: []string{"cache", "rm"}, want: "cache rm accepts 1 arg(s), received 0"},
		{name: "extra", args: []string{"cache", "rm", "cache-id", "extra"}, want: "cache rm accepts 1 arg(s), received 2"},
		{name: "empty", args: []string{"cache", "rm", ""}, want: "cache rm requires a cache id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("%v exit code = %d, want usage; stderr=%q", tc.args, exitCode, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want empty", tc.args, stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v stderr = %q, want %q", tc.args, stderr, tc.want)
			}
		})
	}
}

func TestIntegrationCLICacheLifecycleWithInProcessDaemon(t *testing.T) {
	root := t.TempDir()
	imageCacheRoot := filepath.Join(root, "images")
	t.Setenv("DATA_ROOT", root)
	t.Setenv("SESSION_ROOT", filepath.Join(root, "sessions"))
	t.Setenv("IMAGE_CACHE_ROOT", imageCacheRoot)
	t.Setenv("HTTP_LISTEN", "")
	t.Setenv("AGENT_COMPOSE_SOCKET", "")
	t.Setenv("AGENT_COMPOSE_HOST", "")
	t.Setenv("RUNTIME_DRIVER", config.RuntimeDriverDocker)
	t.Setenv("DOCKER_IMAGE", "guest:latest")
	t.Setenv("SESSION_START_TIMEOUT", "1s")
	t.Setenv("SESSION_STOP_TIMEOUT", "1s")
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("BOXLITE_HOME", filepath.Join(root, "boxlite"))
	t.Setenv("BOXLITE_RUNTIME_DIR", filepath.Join(root, "boxlite-runtime"))
	t.Setenv("DOCKER_HOME", filepath.Join(root, "docker"))
	t.Setenv("MICROSANDBOX_HOME", filepath.Join(root, "microsandbox"))
	t.Setenv("MICROSANDBOX_MSB_PATH", filepath.Join(root, "msb"))
	t.Setenv("MICROSANDBOX_LIB_PATH", filepath.Join(root, "libmicrosandbox_go_ffi.so"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, err := NewDaemonApp(ctx, DaemonOptions{StartBackground: func(do.Injector) error { return nil }})
	if err != nil {
		t.Fatalf("NewDaemonApp returned error: %v", err)
	}
	server := httptest.NewServer(app.Echo)
	defer server.Close()

	cache, err := imagecache.New(imagecache.Config{Root: imageCacheRoot})
	if err != nil {
		t.Fatalf("imagecache.New returned error: %v", err)
	}
	referencedImageID := "sha256:cli-ref"
	referencedRootFS := cache.MaterializedRootFSPath(referencedImageID)
	referencedReady := filepath.Join(cache.MaterializedImageDir(referencedImageID), ".rootfs.ready")
	orphanRootFS := filepath.Join(cache.MaterializationRoot(), "cli-orphan", "rootfs")
	missingRootFS := filepath.Join(cache.MaterializationRoot(), "cli-missing", "rootfs")
	for _, dir := range []string{
		filepath.Join(referencedRootFS, "bin"),
		orphanRootFS,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for path, data := range map[string]string{
		filepath.Join(referencedRootFS, "bin", "tool"): "referenced",
		referencedReady:                          "ready",
		filepath.Join(orphanRootFS, "layer.txt"): "orphan",
	} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := cache.SaveMetadata(imagecache.MetadataFile{Images: []imagecache.ImageMetadata{
		{
			CacheKey:        referencedImageID,
			RequestedRef:    "agent:referenced",
			NormalizedRef:   "registry.example/agent:referenced",
			RepoDigests:     []string{"registry.example/agent@sha256:cli-ref"},
			ManifestDigest:  "sha256:manifest-cli-ref",
			ConfigDigest:    referencedImageID,
			RootFSCachePath: referencedRootFS,
		},
		{
			CacheKey:        "sha256:cli-missing",
			RequestedRef:    "agent:missing",
			NormalizedRef:   "registry.example/agent:missing",
			ManifestDigest:  "sha256:manifest-cli-missing",
			ConfigDigest:    "sha256:cli-missing",
			RootFSCachePath: missingRootFS,
		},
	}}); err != nil {
		t.Fatalf("SaveMetadata returned error: %v", err)
	}

	listOut, listErr, listRuns, listCode := executeCLICommand("cache", "ls", "--host", server.URL, "--json", "--type", "materialized")
	if listCode != 0 || listErr != "" || listRuns != 0 {
		t.Fatalf("cache ls code/stderr/runs = %d / %q / %d", listCode, listErr, listRuns)
	}
	var listed composeCacheListOutput
	if err := json.Unmarshal([]byte(listOut), &listed); err != nil {
		t.Fatalf("cache ls JSON decode failed: %v\n%s", err, listOut)
	}
	referenced := requireCLICacheByPath(t, listed.Caches, referencedRootFS)
	orphan := requireCLICacheByPath(t, listed.Caches, orphanRootFS)
	if referenced.Status != "referenced" || referenced.Removable {
		t.Fatalf("referenced cache = %#v", referenced)
	}
	if orphan.Status != "orphaned" || !orphan.Removable {
		t.Fatalf("orphan cache = %#v", orphan)
	}
	if !stringSliceContainsSubstring(listed.Warnings, "cli-missing") {
		t.Fatalf("cache ls warnings = %#v, want missing metadata path warning", listed.Warnings)
	}

	inspectOut, inspectErr, _, inspectCode := executeCLICommand("cache", "inspect", "--host", server.URL, referenced.ID)
	if inspectCode != 0 || inspectErr != "" {
		t.Fatalf("cache inspect code/stderr = %d / %q", inspectCode, inspectErr)
	}
	if !strings.Contains(inspectOut, "References:") || !strings.Contains(inspectOut, "agent:referenced") {
		t.Fatalf("cache inspect stdout = %q", inspectOut)
	}

	dryRunOut, dryRunErr, _, dryRunCode := executeCLICommand("cache", "prune", "--host", server.URL, "--type", "materialized", "--orphaned")
	if dryRunCode != 0 || dryRunErr != "" {
		t.Fatalf("cache prune dry-run code/stderr = %d / %q", dryRunCode, dryRunErr)
	}
	if !strings.Contains(dryRunOut, "Dry-run") || !strings.Contains(dryRunOut, orphan.ID) {
		t.Fatalf("cache prune dry-run stdout = %q", dryRunOut)
	}
	assertLocalPathExists(t, orphanRootFS)

	forceOut, forceErr, _, forceCode := executeCLICommand("cache", "prune", "--host", server.URL, "--json", "--type", "materialized", "--orphaned", "--force")
	if forceCode != 0 || forceErr != "" {
		t.Fatalf("cache prune force code/stderr = %d / %q", forceCode, forceErr)
	}
	var forceResult composeCacheOperationOutput
	if err := json.Unmarshal([]byte(forceOut), &forceResult); err != nil {
		t.Fatalf("cache prune force JSON decode failed: %v\n%s", err, forceOut)
	}
	if forceResult.DryRun || !stringSliceContains(forceResult.Removed, orphan.ID) {
		t.Fatalf("cache prune force result = %#v", forceResult)
	}
	assertLocalPathMissing(t, orphanRootFS)
	assertLocalPathExists(t, referencedRootFS)

	protectedOut, protectedErr, _, protectedCode := executeCLICommand("cache", "rm", "--host", server.URL, "--force", referenced.ID)
	if protectedCode != exitCodeUsage {
		t.Fatalf("cache rm referenced exit code = %d, want usage; stderr=%q", protectedCode, protectedErr)
	}
	if !strings.Contains(protectedOut, "Skipped") || !strings.Contains(protectedOut, referenced.ID) {
		t.Fatalf("cache rm referenced stdout = %q", protectedOut)
	}
	assertLocalPathExists(t, referencedRootFS)

	includeOut, includeErr, _, includeCode := executeCLICommand("cache", "prune", "--host", server.URL, "--json", "--type", "materialized", "--status", "referenced", "--include-referenced", "--force")
	if includeCode != 0 || includeErr != "" {
		t.Fatalf("cache prune include referenced code/stderr = %d / %q", includeCode, includeErr)
	}
	var includeResult composeCacheOperationOutput
	if err := json.Unmarshal([]byte(includeOut), &includeResult); err != nil {
		t.Fatalf("cache prune include referenced JSON decode failed: %v\n%s", err, includeOut)
	}
	if includeResult.DryRun || len(includeResult.Removed) == 0 {
		t.Fatalf("cache prune include referenced result = %#v", includeResult)
	}
	assertLocalPathMissing(t, referencedRootFS)
	assertLocalPathMissing(t, referencedReady)
}

func TestIntegrationCLIRemoveImageDoesNotDeleteRuntimeCachesWithInProcessDaemon(t *testing.T) {
	t.Setenv("IMAGE_STORE_MODE", config.ImageStoreModeOCI)
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", nil)
	defer cancel()
	server := httptest.NewServer(app.Echo)
	defer server.Close()

	cache, err := imagecache.New(imagecache.Config{Root: app.Config.ImageCacheRoot})
	if err != nil {
		t.Fatalf("imagecache.New returned error: %v", err)
	}
	imageID := "sha256:cli-rmi"
	layoutPath := cache.MaterializedOCILayoutPath(imageID)
	rootfsPath := cache.MaterializedRootFSPath(imageID)
	boxliteCachePath := filepath.Join(app.Config.BoxliteHome, "images", "local", "keep")
	microsandboxDiskPath := filepath.Join(app.Config.MicrosandboxHome, "docker-disks", "keep.raw")
	for _, dir := range []string{
		layoutPath,
		rootfsPath,
		boxliteCachePath,
		filepath.Dir(microsandboxDiskPath),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for path, data := range map[string]string{
		filepath.Join(layoutPath, "sentinel"):   "layout",
		filepath.Join(rootfsPath, "sentinel"):   "rootfs",
		filepath.Join(boxliteCachePath, "disk"): "boxlite",
		microsandboxDiskPath:                    "microsandbox",
	} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := cache.SaveMetadata(imagecache.MetadataFile{Images: []imagecache.ImageMetadata{{
		CacheKey:        imageID,
		RequestedRef:    "registry.example/rmi:1.0",
		NormalizedRef:   "registry.example/rmi:1.0",
		RepoTags:        []string{"registry.example/rmi:1.0"},
		RepoDigests:     []string{"registry.example/rmi@sha256:cli-rmi"},
		ManifestDigest:  "sha256:manifest-cli-rmi",
		ConfigDigest:    imageID,
		LayoutCachePath: layoutPath,
		RootFSCachePath: rootfsPath,
	}}}); err != nil {
		t.Fatalf("SaveMetadata returned error: %v", err)
	}

	stdout, stderr, runCount, exitCode := executeCLICommand("rmi", "--host", server.URL, "--json", "--force", "--prune-children", "registry.example/rmi:1.0")
	if exitCode != 0 || stderr != "" || runCount != 0 {
		t.Fatalf("rmi code/stderr/runs = %d / %q / %d", exitCode, stderr, runCount)
	}
	var removed composeImageRemoveOutput
	if err := json.Unmarshal([]byte(stdout), &removed); err != nil {
		t.Fatalf("rmi JSON decode failed: %v\n%s", err, stdout)
	}
	if len(removed.DeletedIDs) != 1 || removed.DeletedIDs[0] != displayOpaqueID(imageID) || len(removed.Warnings) == 0 {
		t.Fatalf("rmi output = %#v", removed)
	}
	for _, path := range []string{
		filepath.Join(layoutPath, "sentinel"),
		filepath.Join(rootfsPath, "sentinel"),
		filepath.Join(boxliteCachePath, "disk"),
		microsandboxDiskPath,
	} {
		assertLocalPathExists(t, path)
	}
}

func TestIntegrationCLIImagePullAliasesAndJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			pullImage: func(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
				calls++
				if req.Msg.GetImageRef() != "agent:latest" {
					t.Fatalf("PullImage image_ref = %q", req.Msg.GetImageRef())
				}
				if calls == 1 && (req.Msg.GetPlatform().GetOs() != "linux" || req.Msg.GetPlatform().GetArchitecture() != "amd64") {
					t.Fatalf("PullImage platform = %#v", req.Msg.GetPlatform())
				}
				return connect.NewResponse(&agentcomposev2.PullImageResponse{
					Image:       testCLIImage("sha256:pull123456789", "agent:latest"),
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					ResolvedRef: "agent@sha256:def",
					Progress: []*agentcomposev2.ImagePullProgress{{
						Id:           "layer1",
						Status:       "Downloaded",
						CurrentBytes: 3,
						TotalBytes:   3,
					}},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("pull", "--host", server.URL, "--json", "--platform", "linux/amd64", "agent:latest")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("pull --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeImagePullOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("pull JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.ImageRef != "agent:latest" || decoded.ResolvedRef != "agent@sha256:def" || decoded.Status != "succeeded" || len(decoded.Progress) != 1 {
		t.Fatalf("pull JSON = %#v", decoded)
	}

	textOut, textErr, _, textCode := executeCLICommand("image", "pull", "--host", server.URL, "agent:latest")
	if textCode != 0 {
		t.Fatalf("image pull code/stderr = %d / %q", textCode, textErr)
	}
	assertDeprecatedWarning(t, textErr, "agent-compose pull")
	if !strings.Contains(textOut, "Pulled agent:latest") || !strings.Contains(textOut, "agent@sha256:def") {
		t.Fatalf("image pull output = %q", textOut)
	}
	if calls != 2 {
		t.Fatalf("PullImage calls = %d, want 2", calls)
	}
}

func TestIntegrationCLIImagePullSkippedWarnings(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			pullImage: func(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
				return connect.NewResponse(&agentcomposev2.PullImageResponse{
					Image:       testCLIImage("sha256:local123456789", req.Msg.GetImageRef()),
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					ResolvedRef: "agent@sha256:local",
					Warnings:    []string{"skipped pull: image agent:latest already exists locally"},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("pull", "--host", server.URL, "agent:latest")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("pull skipped code/stderr = %d / %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "Skipped agent:latest") || !strings.Contains(stdout, "already exists locally") || strings.Contains(stdout, "Pulled agent:latest") {
		t.Fatalf("pull skipped stdout = %q", stdout)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("pull", "--host", server.URL, "--json", "agent:latest")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("pull skipped --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeImagePullOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("pull skipped JSON decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Warnings) != 1 || !strings.Contains(decoded.Warnings[0], "already exists locally") {
		t.Fatalf("pull skipped JSON = %#v", decoded)
	}
}

func TestIntegrationCLIPullProjectImages(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-pull-project
agents:
  reviewer:
    provider: codex
    image: agent:v1
  tester:
    provider: codex
    image: agent:v1
  builder:
    provider: codex
    image: agent:v2
`)
	var pulled []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			pullImage: func(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
				if req.Msg.GetPlatform().GetOs() != "linux" || req.Msg.GetPlatform().GetArchitecture() != "amd64" {
					t.Fatalf("PullImage platform = %#v", req.Msg.GetPlatform())
				}
				imageRef := req.Msg.GetImageRef()
				pulled = append(pulled, imageRef)
				return connect.NewResponse(&agentcomposev2.PullImageResponse{
					Image:       testCLIImage("sha256:"+strings.TrimPrefix(imageRef, "agent:"), imageRef),
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					ResolvedRef: imageRef + "@sha256:def",
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("pull", "--host", server.URL, "--file", composePath, "--platform", "linux/amd64")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("pull project code/stderr = %d / %q", exitCode, stderr)
	}
	for _, want := range []string{"Pulled agent:v2", "agent:v2@sha256:def", "Pulled agent:v1", "agent:v1@sha256:def"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("pull project stdout %q does not contain %q", stdout, want)
		}
	}
	if len(pulled) != 2 || pulled[0] != "agent:v2" || pulled[1] != "agent:v1" {
		t.Fatalf("pulled images = %#v", pulled)
	}
}

func TestIntegrationCLIPullProjectImagesJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-pull-project-json
agents:
  reviewer:
    provider: codex
    image: agent:v1
  builder:
    provider: codex
    image: agent:v2
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			pullImage: func(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
				imageRef := req.Msg.GetImageRef()
				return connect.NewResponse(&agentcomposev2.PullImageResponse{
					Image:       testCLIImage("sha256:"+strings.TrimPrefix(imageRef, "agent:"), imageRef),
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					ResolvedRef: imageRef + "@sha256:def",
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("pull", "--host", server.URL, "--file", composePath, "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("pull project --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeProjectImagePullOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("pull project JSON decode failed: %v\n%s", err, stdout)
	}
	if len(decoded.Images) != 2 || decoded.Images[0].ImageRef != "agent:v2" || decoded.Images[1].ImageRef != "agent:v1" {
		t.Fatalf("pull project JSON = %#v", decoded)
	}
}

func TestIntegrationCLIImageBuildLegacyProject(t *testing.T) {
	dir := t.TempDir()
	contextDir := filepath.Join(dir, "agent")
	if err := os.Mkdir(contextDir, 0o700); err != nil {
		t.Fatalf("create context: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile.agent"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	composePath := writeComposeFile(t, dir, `
name: cli-legacy-build-project
agents:
  reviewer:
    provider: codex
    image: reviewer:dev
    build:
      context: agent
      dockerfile: Dockerfile.agent
      target: runtime
      args:
        NODE_ENV: production
      platforms:
        - linux/amd64
      tags:
        - reviewer:latest
      no_cache: true
      pull: true
`)
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			buildImage: func(ctx context.Context, req *connect.Request[agentcomposev2.BuildImageRequest], stream *connect.ServerStream[agentcomposev2.BuildImageEvent]) error {
				calls++
				if req.Msg.GetContextDir() != contextDir {
					t.Fatalf("BuildImage context_dir = %q, want %q", req.Msg.GetContextDir(), contextDir)
				}
				if req.Msg.GetDockerfile() != "Dockerfile.agent" {
					t.Fatalf("BuildImage dockerfile = %q", req.Msg.GetDockerfile())
				}
				if got := req.Msg.GetTags(); len(got) != 3 || got[0] != "reviewer:dev" || got[1] != "reviewer:latest" || got[2] != "reviewer:ci" {
					t.Fatalf("BuildImage tags = %#v", got)
				}
				if req.Msg.GetBuildArgs()["NODE_ENV"] != "development" {
					t.Fatalf("BuildImage build_args = %#v", req.Msg.GetBuildArgs())
				}
				if req.Msg.GetTarget() != "runtime" || !req.Msg.GetNoCache() || !req.Msg.GetPull() {
					t.Fatalf("BuildImage flags target=%q no_cache=%v pull=%v", req.Msg.GetTarget(), req.Msg.GetNoCache(), req.Msg.GetPull())
				}
				if req.Msg.GetPlatform().GetOs() != "linux" || req.Msg.GetPlatform().GetArchitecture() != "amd64" {
					t.Fatalf("BuildImage platform = %#v", req.Msg.GetPlatform())
				}
				if err := stream.Send(&agentcomposev2.BuildImageEvent{
					Status:   agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_RUNNING,
					Message:  "build step",
					ImageRef: "reviewer:dev",
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.BuildImageEvent{
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					Message:     "Built reviewer:dev",
					ImageRef:    "reviewer:dev",
					ResolvedRef: "reviewer:dev@sha256:built",
					Image:       testCLIImage("sha256:built", "reviewer:dev"),
				})
			},
		},
	})
	defer server.Close()

	textOut, textErr, _, textCode := executeCLICommand("image", "build", "--host", server.URL, "--file", composePath, "-t", "reviewer:ci", "--dockerfile", "Dockerfile.agent", "--target", "runtime", "--build-arg", "NODE_ENV=development", "--platform", "linux/amd64", "--no-cache", "--pull", "reviewer")
	if textCode != 0 {
		t.Fatalf("image build code/stderr = %d / %q", textCode, textErr)
	}
	assertDeprecatedWarning(t, textErr, "agent-compose build")
	if !strings.Contains(textOut, "build step") || !strings.Contains(textOut, "Built reviewer:dev") {
		t.Fatalf("image build output = %q", textOut)
	}
	if calls != 1 {
		t.Fatalf("BuildImage calls = %d, want 1", calls)
	}
}

func TestIntegrationCLIProjectBuildImages(t *testing.T) {
	dir := t.TempDir()
	contextDir := filepath.Join(dir, "agent")
	if err := os.Mkdir(contextDir, 0o700); err != nil {
		t.Fatalf("create context: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile.agent"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	composePath := writeComposeFile(t, dir, `
name: cli-build-project
agents:
  reviewer:
    provider: codex
    image: reviewer:dev
    build:
      context: agent
      dockerfile: Dockerfile.agent
      target: runtime
      args:
        NODE_ENV: production
      platforms:
        - linux/amd64
      tags:
        - reviewer:latest
      no_cache: true
      pull: true
  tester:
    provider: codex
    image: tester:dev
`)
	var built []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			buildImage: func(ctx context.Context, req *connect.Request[agentcomposev2.BuildImageRequest], stream *connect.ServerStream[agentcomposev2.BuildImageEvent]) error {
				built = append(built, firstNonEmptyString(req.Msg.GetTags()...))
				if req.Msg.GetContextDir() != contextDir {
					t.Fatalf("BuildImage context_dir = %q, want %q", req.Msg.GetContextDir(), contextDir)
				}
				if req.Msg.GetDockerfile() != "Dockerfile.agent" || req.Msg.GetTarget() != "runtime" {
					t.Fatalf("BuildImage dockerfile/target = %q/%q", req.Msg.GetDockerfile(), req.Msg.GetTarget())
				}
				if req.Msg.GetBuildArgs()["NODE_ENV"] != "development" {
					t.Fatalf("CLI build arg did not override compose args: %#v", req.Msg.GetBuildArgs())
				}
				if got := req.Msg.GetTags(); len(got) != 3 || got[0] != "reviewer:dev" || got[1] != "reviewer:latest" || got[2] != "reviewer:ci" {
					t.Fatalf("BuildImage tags = %#v", got)
				}
				if !req.Msg.GetNoCache() || !req.Msg.GetPull() {
					t.Fatalf("BuildImage no_cache/pull = %v/%v", req.Msg.GetNoCache(), req.Msg.GetPull())
				}
				return stream.Send(&agentcomposev2.BuildImageEvent{
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					Message:     "Built reviewer:dev",
					ImageRef:    "reviewer:dev",
					ResolvedRef: "reviewer:dev@sha256:built",
					Image:       testCLIImage("sha256:built", "reviewer:dev"),
				})
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("build", "--host", server.URL, "--file", composePath, "--json", "--build-arg", "NODE_ENV=development", "-t", "reviewer:ci")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("project build --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeProjectImageBuildOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("project build JSON decode failed: %v\n%s", err, stdout)
	}
	if len(decoded.Images) != 1 || decoded.Images[0].ImageRef != "reviewer:dev" {
		t.Fatalf("project build JSON = %#v", decoded)
	}
	if len(built) != 1 || built[0] != "reviewer:dev" {
		t.Fatalf("built images = %#v", built)
	}
}

func TestIntegrationCLIImageRemoveAliasesAndJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			removeImage: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
				calls++
				if req.Msg.GetImageRef() != "agent:old" {
					t.Fatalf("RemoveImage image_ref = %q", req.Msg.GetImageRef())
				}
				if calls == 1 && !req.Msg.GetForce() {
					t.Fatalf("RemoveImage force = false for rmi")
				}
				if calls == 2 && !req.Msg.GetPruneChildren() {
					t.Fatalf("RemoveImage prune_children = false for image rm")
				}
				return connect.NewResponse(&agentcomposev2.RemoveImageResponse{
					ImageRef:     req.Msg.GetImageRef(),
					UntaggedRefs: []string{"agent:old"},
					DeletedIds:   []string{"sha256:old"},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("rmi", "--host", server.URL, "--json", "--force", "agent:old")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("rmi --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeImageRemoveOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("rmi JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.ImageRef != "agent:old" || decoded.UntaggedRefs[0] != "agent:old" || decoded.DeletedIDs[0] != "old" {
		t.Fatalf("rmi JSON = %#v", decoded)
	}

	textOut, textErr, _, textCode := executeCLICommand("image", "rm", "--host", server.URL, "--prune-children", "agent:old")
	if textCode != 0 {
		t.Fatalf("image rm code/stderr = %d / %q", textCode, textErr)
	}
	assertDeprecatedWarning(t, textErr, "agent-compose rmi")
	if !strings.Contains(textOut, "Untagged: agent:old") || !strings.Contains(textOut, "Deleted: old") {
		t.Fatalf("image rm output = %q", textOut)
	}
	if calls != 2 {
		t.Fatalf("RemoveImage calls = %d, want 2", calls)
	}
}

func TestIntegrationCLIImageRemoveMissingImageMessage(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			removeImage: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
				if req.Msg.GetImageRef() != "missing:latest" {
					t.Fatalf("RemoveImage image_ref = %q", req.Msg.GetImageRef())
				}
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("remove image: image %s: endpoint unix:///var/run/docker.sock: Error response from daemon: No such image: %s", req.Msg.GetImageRef(), req.Msg.GetImageRef()))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("rmi", "--host", server.URL, "missing:latest")
	if exitCode != exitCodeUsage {
		t.Fatalf("rmi missing image exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
	}
	if stdout != "" {
		t.Fatalf("rmi missing image stdout = %q", stdout)
	}
	if want := "image missing:latest does not exist\n"; stderr != want {
		t.Fatalf("rmi missing image stderr = %q, want %q", stderr, want)
	}
}

func TestIntegrationCLIImageInspectJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			inspectImage: func(ctx context.Context, req *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
				calls++
				if req.Msg.GetImageRef() != "agent:latest" {
					t.Fatalf("InspectImage image_ref = %q", req.Msg.GetImageRef())
				}
				return connect.NewResponse(&agentcomposev2.InspectImageResponse{
					Image: testCLIImage("sha256:inspect123456789", "agent:latest"),
					StoreStatus: &agentcomposev2.ImageStoreStatus{
						Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
						Available: true,
						Endpoint:  "unix:///var/run/docker.sock",
					},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("inspect", "--host", server.URL, "image", "agent:latest")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("inspect image code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeImageInspectOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("inspect image JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.Image.ImageRef != "agent:latest" || decoded.Image.Platform != "linux/amd64" || decoded.StoreStatus.Endpoint == "" {
		t.Fatalf("inspect image JSON = %#v", decoded)
	}

	legacyOut, legacyErr, _, legacyCode := executeCLICommand("image", "inspect", "--host", server.URL, "agent:latest")
	if legacyCode != 0 {
		t.Fatalf("legacy image inspect code = %d; stderr=%q", legacyCode, legacyErr)
	}
	assertDeprecatedWarning(t, legacyErr, "agent-compose inspect image")
	var legacyDecoded composeImageInspectOutput
	if err := json.Unmarshal([]byte(legacyOut), &legacyDecoded); err != nil {
		t.Fatalf("legacy image inspect JSON decode failed: %v\n%s", err, legacyOut)
	}
	if legacyDecoded.Image.ImageRef != "agent:latest" || legacyDecoded.StoreStatus.Endpoint == "" {
		t.Fatalf("legacy image inspect JSON = %#v", legacyDecoded)
	}
	if calls != 2 {
		t.Fatalf("InspectImage calls = %d, want 2", calls)
	}
}

func TestIntegrationCLIImageInspectMissingImageMessage(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			inspectImage: func(ctx context.Context, req *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
				if req.Msg.GetImageRef() != "missing:latest" {
					t.Fatalf("InspectImage image_ref = %q", req.Msg.GetImageRef())
				}
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("inspect image: image %s: endpoint unix:///var/run/docker.sock: Error response from daemon: No such image: %s", req.Msg.GetImageRef(), req.Msg.GetImageRef()))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("inspect", "--host", server.URL, "image", "missing:latest")
	if exitCode != exitCodeUsage {
		t.Fatalf("inspect image missing exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
	}
	if stdout != "" {
		t.Fatalf("inspect image missing stdout = %q", stdout)
	}
	if want := "image missing:latest does not exist\n"; stderr != want {
		t.Fatalf("inspect image missing stderr = %q, want %q", stderr, want)
	}
}

func TestComposeImageOutputFromProtoAcceptsOCIStatus(t *testing.T) {
	image := testCLIImage("sha256:oci123456789", "agent:latest")
	image.Store = agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE
	image.Docker = nil
	image.Oci = &agentcomposev2.OCIImageStatus{
		LayoutCached:   true,
		RootfsCached:   true,
		CacheKey:       "sha256:oci123456789",
		ManifestDigest: "sha256:manifest",
		ConfigDigest:   "sha256:oci123456789",
		MediaType:      "application/vnd.oci.image.manifest.v1+json",
	}

	output := composeImageOutputFromProto(image)
	if output.Store != "oci-cache" || output.ImageID != "oci123456789" || output.ImageRef != "agent:latest" || output.Platform != "linux/amd64" {
		t.Fatalf("OCI image output = %#v", output)
	}
}

func TestIntegrationCLIImagesJSONAcceptsOCIStoreStatus(t *testing.T) {
	image := testCLIImage("sha256:oci123456789", "agent:latest")
	image.Store = agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE
	image.Docker = nil
	image.Oci = &agentcomposev2.OCIImageStatus{
		LayoutCached:   true,
		CacheKey:       "sha256:oci123456789",
		ManifestDigest: "sha256:manifest",
		ConfigDigest:   "sha256:oci123456789",
		MediaType:      "application/vnd.oci.image.manifest.v1+json",
	}
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			listImages: func(ctx context.Context, req *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListImagesResponse{
					Images:     []*agentcomposev2.Image{image},
					TotalCount: 1,
					StoreStatus: &agentcomposev2.ImageStoreStatus{
						Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
						Available: true,
						Endpoint:  "/tmp/images/oci",
					},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("images", "--host", server.URL, "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("images --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeImageListOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("images JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.TotalCount != 1 || decoded.Images[0].Store != "oci-cache" || decoded.StoreStatus.Store != "oci-cache" || decoded.StoreStatus.Endpoint != "/tmp/images/oci" {
		t.Fatalf("images JSON = %#v", decoded)
	}
}

func TestIntegrationCLIImageDockerErrorIsClear(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			pullImage: func(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("pull image image agent:missing: endpoint tcp://docker.example:2375: docker daemon unavailable"))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("pull", "--host", server.URL, "agent:missing")
	if exitCode != exitCodeUnavailable {
		t.Fatalf("pull Docker error exit code = %d, want %d; stderr=%q", exitCode, exitCodeUnavailable, stderr)
	}
	if stdout != "" {
		t.Fatalf("pull Docker error stdout = %q, want empty", stdout)
	}
	for _, want := range []string{"agent:missing", "tcp://docker.example:2375", "docker daemon unavailable"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("pull Docker error stderr %q does not contain %q", stderr, want)
		}
	}
}

func TestLogsJSONFollowIsUsageError(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("logs", "--json", "--follow")
	if exitCode != exitCodeUsage {
		t.Fatalf("logs --json --follow exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("logs --json --follow stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "cannot be combined") {
		t.Fatalf("logs --json --follow stderr = %q", stderr)
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

func TestCLIOutputHelpersCoverEdgeBranches(t *testing.T) {
	project := &agentcomposev2.Project{
		Summary: &agentcomposev2.ProjectSummary{
			ProjectId:       "project-1",
			Name:            "Project",
			SourcePath:      "/tmp/agent-compose.yml",
			CurrentRevision: 2,
			SpecHash:        "hash",
			AgentCount:      1,
			SchedulerCount:  1,
		},
	}
	applyResp := &agentcomposev2.ApplyProjectResponse{
		Project:   project,
		Revision:  &agentcomposev2.ProjectRevision{Revision: 2, SpecHash: "hash"},
		Unchanged: true,
		Changes: []*agentcomposev2.ProjectChange{{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED,
			ResourceType: "agent",
			ResourceId:   "agent-1",
			Name:         "reviewer",
		}},
	}
	if output := composeUpOutputFromResponse(applyResp); !output.Unchanged || output.Project.ID != "project-1" || len(output.Changes) != 1 {
		t.Fatalf("composeUpOutputFromResponse = %#v", output)
	}
	var text bytes.Buffer
	if err := writeComposeUpText(&text, composeDisplayChangesFromProjectChanges(applyResp.GetChanges(), nil)); err != nil {
		t.Fatalf("writeComposeUpText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "ACTION") || !strings.Contains(text.String(), "reviewer") {
		t.Fatalf("compose up text = %q", text.String())
	}

	removeResp := &agentcomposev2.RemoveProjectResponse{
		Project: project,
		Changes: []*agentcomposev2.ProjectChange{{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED,
			ResourceType: "session",
			ResourceId:   "sandbox-1",
			Name:         "sandbox-1",
			Message:      "stop failed",
		}},
	}
	down := composeDownOutputFromResponse(removeResp)
	if down.Status != "partial-failure" || down.FailedSessionStops != 1 {
		t.Fatalf("composeDownOutputFromResponse = %#v", down)
	}
	text.Reset()
	if err := writeComposeDownText(&text, composeDownDisplayChanges(removeResp, nil)); err != nil {
		t.Fatalf("writeComposeDownText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "MESSAGE") || !strings.Contains(text.String(), "stop failed") {
		t.Fatalf("compose down text = %q", text.String())
	}

	serviceCount := uint32(3)
	projects := []composeProjectListItem{{
		ID:             "project-1",
		Name:           "Project",
		ConfigFile:     "/tmp/agent-compose.yml",
		ProjectDir:     "/tmp",
		Revision:       2,
		SpecHash:       "hash",
		AgentCount:     1,
		SchedulerCount: 1,
		ServiceCount:   &serviceCount,
		UpdatedAt:      "2026-07-06T00:00:00Z",
	}, {
		ID:        "project-removed",
		Name:      "Removed",
		RemovedAt: "2026-07-06T00:00:00Z",
	}}
	text.Reset()
	if err := writeProjectListText(&text, projects, true); err != nil {
		t.Fatalf("writeProjectListText verbose returned error: %v", err)
	}
	if !strings.Contains(text.String(), "SERVICES") || !strings.Contains(text.String(), "removed") || projectServiceCountText(nil) != "-" {
		t.Fatalf("project list text = %q", text.String())
	}

	value := 12.5
	stats := composeStatsOutputFromProto(&agentcomposev2.SandboxStats{
		SandboxId:        "sandbox-1",
		Driver:           "boxlite",
		SampledAt:        "2026-07-06T00:00:00Z",
		CpuPercent:       &agentcomposev2.MetricValue{Value: &value, Unit: "percent", Status: agentcomposev2.MetricStatus_METRIC_STATUS_OK},
		MemoryUsageBytes: &agentcomposev2.MetricValue{Value: &value, Unit: "bytes", Status: agentcomposev2.MetricStatus_METRIC_STATUS_UNAVAILABLE, Message: "n/a"},
		UptimeSeconds:    &agentcomposev2.MetricValue{Value: &value, Unit: "seconds", Status: agentcomposev2.MetricStatus_METRIC_STATUS_OK},
	})
	if stats.CPUPercent.Status != "ok" || stats.MemoryUsageBytes.Status != "unavailable" || composeStatsOutputFromProto(nil).ID != "" {
		t.Fatalf("stats output = %#v", stats)
	}
	text.Reset()
	if err := writeStatsText(&text, []composeStatsOutput{stats}); err != nil {
		t.Fatalf("writeStatsText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "12.50") || !strings.Contains(text.String(), "12s") {
		t.Fatalf("stats text = %q", text.String())
	}

	run := testRunDetail("project-1", "run-123456789", "reviewer", "sandbox-1", agentcomposev2.RunStatus_RUN_STATUS_FAILED, 9, "one\ntwo\nthree\n")
	run.Prompt = "prompt"
	run.ResultJson = `{"ok":true}`
	run.CleanupError = "cleanup failed"
	run.Driver = "boxlite"
	run.ImageRef = "agent:latest"
	run.Summary.Source = agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER
	run.Summary.Warnings = []string{"warn-a"}
	run.Warnings = []string{"warn-a", "warn-b"}
	runOutput := composeRunOutputFromDetailWithOptions(run, composeLogsOptions{TailLines: 2})
	if runOutput.Output != "two\nthree\n" || runOutput.Source != "scheduler" || len(runOutput.Warnings) != 2 {
		t.Fatalf("run output = %#v", runOutput)
	}
	text.Reset()
	if err := writeLogsForRun(&text, run, false, composeLogsOptions{TailLines: 1, Timestamp: true}); err != nil {
		t.Fatalf("writeLogsForRun text returned error: %v", err)
	}
	if !strings.Contains(text.String(), "reviewer-run-123456789") || !strings.Contains(text.String(), "three") {
		t.Fatalf("run logs text = %q", text.String())
	}
	text.Reset()
	if err := writeLogsForRun(&text, run, true, composeLogsOptions{TailLines: 1}); err != nil {
		t.Fatalf("writeLogsForRun JSON returned error: %v", err)
	}
	if !strings.Contains(text.String(), `"runs"`) || !strings.Contains(text.String(), `"three\n"`) {
		t.Fatalf("run logs json = %q", text.String())
	}

	execOutput := composeExecOutputFromResult(&agentcomposev2.ExecResult{
		ExecId: "exec-1", SessionId: "sandbox-1", RunId: "run-1",
		Command: &agentcomposev2.ExecCommand{Command: "bash", Args: []string{"-lc", "false"}},
		Cwd:     "/workspace", ExitCode: 127, Success: false, Stdout: "out", Stderr: "err", Output: "outerr", Error: "failed",
	})
	if execOutput.Command != "bash" || execOutput.Args[0] != "-lc" || execResultExitCode(&agentcomposev2.ExecResult{ExitCode: 127}) != exitCodeGeneral {
		t.Fatalf("exec output = %#v", execOutput)
	}
}

func TestCLIImageCacheAndFilterHelpersCoverEdgeBranches(t *testing.T) {
	image := testCLIImage("sha256:1234567890abcdef", "agent:latest")
	image.AvailabilityStatus = agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_ERROR
	image.Platform = &agentcomposev2.ImagePlatform{Os: "linux", Architecture: "amd64", Variant: "v8"}
	image.Labels = map[string]string{"k": "v"}
	pull := composeImagePullOutputFromResponse(&agentcomposev2.PullImageResponse{
		Image:       image,
		ResolvedRef: "agent@sha256:1234",
		Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_FAILED,
		Progress: []*agentcomposev2.ImagePullProgress{{
			Id: "layer", Status: "done", Progress: "1/1", CurrentBytes: 1, TotalBytes: 1,
		}},
		Warnings: []string{"already exists; skipped"},
	})
	if pull.Status != "failed" || pull.Image.Platform != "linux/amd64/v8" || !imagePullSkipped(pull) {
		t.Fatalf("pull output = %#v", pull)
	}
	var text bytes.Buffer
	if err := writeImagePullText(&text, pull); err != nil {
		t.Fatalf("writeImagePullText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "Skipped") || !strings.Contains(text.String(), "Warning") {
		t.Fatalf("pull text = %q", text.String())
	}
	listOutput := composeImageListOutputFromResponse(&agentcomposev2.ListImagesResponse{
		Images:     []*agentcomposev2.Image{image},
		TotalCount: 1,
		HasMore:    true,
		NextOffset: 25,
		StoreStatus: &agentcomposev2.ImageStoreStatus{
			Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
			Available: true,
			Endpoint:  "/tmp/images",
		},
	})
	if len(listOutput.Images) != 1 || !listOutput.HasMore || listOutput.StoreStatus.Store != "oci-cache" {
		t.Fatalf("image list output = %#v", listOutput)
	}
	inspectOutput := composeImageInspectOutputFromResponse(&agentcomposev2.InspectImageResponse{
		Image:       image,
		StoreStatus: &agentcomposev2.ImageStoreStatus{Store: agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON, Error: "down"},
	})
	if inspectOutput.Image.ImageID == "" || inspectOutput.StoreStatus.Store != "docker" || inspectOutput.StoreStatus.Error != "down" {
		t.Fatalf("image inspect output = %#v", inspectOutput)
	}
	removeOutput := composeImageRemoveOutputFromResponse(&agentcomposev2.RemoveImageResponse{
		ImageRef: "agent:old", UntaggedRefs: []string{"agent:old"}, DeletedIds: []string{"sha256:old"}, Warnings: []string{"warn"},
	})
	if removeOutput.ImageRef != "agent:old" || len(removeOutput.UntaggedRefs) != 1 || len(removeOutput.DeletedIDs) != 1 {
		t.Fatalf("image remove output = %#v", removeOutput)
	}
	text.Reset()
	if err := writeImagesText(&text, listOutput.Images, false); err != nil {
		t.Fatalf("writeImagesText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "IMAGE ID") || !strings.Contains(text.String(), "REF") || !strings.Contains(text.String(), "DISK USAGE") || !strings.Contains(text.String(), "agent:latest") || !strings.Contains(text.String(), "1.0KB") || strings.Contains(text.String(), "CONTENT SIZE") {
		t.Fatalf("images text = %q", text.String())
	}
	text.Reset()
	untaggedImage := composeImageOutput{
		ImageID:          "sha256:7e31c0c15f55c1c4bc9ccbd8d435987df0893c71f1ecdb324e87df0bc77e1c2a",
		ShortID:          "7e31c0c15f55",
		ImageRef:         "sha256:7e31c0c15f55c1c4bc9ccbd8d435987df0893c71f1ecdb324e87df0bc77e1c2a",
		ResolvedRef:      "example.com/agent@sha256:7e31c0c15f55c1c4bc9ccbd8d435987df0893c71f1ecdb324e87df0bc77e1c2a",
		SizeBytes:        559279329,
		VirtualSizeBytes: 559279329,
	}
	if err := writeImagesText(&text, []composeImageOutput{untaggedImage}, false); err != nil {
		t.Fatalf("writeImagesText untagged returned error: %v", err)
	}
	if !strings.Contains(text.String(), "<none>") || strings.Contains(text.String(), "example.com/agent@sha256") || strings.Contains(text.String(), "sha256:7e31c0c15f55") {
		t.Fatalf("untagged images text = %q", text.String())
	}
	text.Reset()
	if err := writeImagesText(&text, listOutput.Images, true); err != nil {
		t.Fatalf("writeImagesText verbose returned error: %v", err)
	}
	if !strings.Contains(text.String(), "STORE") || !strings.Contains(text.String(), "STATUS") || !strings.Contains(text.String(), "CONTENT SIZE") || !strings.Contains(text.String(), "CREATED") {
		t.Fatalf("images verbose text = %q", text.String())
	}
	if shortImageID("sha256:1234567890abcdef") != "1234567890ab" || imagePlatformText(&agentcomposev2.ImagePlatform{Os: "linux"}) != "linux" {
		t.Fatalf("image helper output mismatch")
	}
	if formatImageSizeForText(0) != "0B" || formatImageSizeForText(559279329) != "559.3MB" || firstNonZeroUint64(0, 42) != 42 {
		t.Fatalf("image text helper output mismatch")
	}
	if formatImageCreatedForText("") != "-" || formatImageCreatedForText("created") != "created" || formatImageAgeForText(2*time.Hour) != "2 hours" {
		t.Fatalf("image time helper output mismatch")
	}
	if imageListRefForText(untaggedImage) != "<none>" || imageRefLooksUntagged("example.com/agent@sha256:def", "") != true || imageRefLooksUntagged("agent:latest", "") {
		t.Fatalf("image ref text helper output mismatch")
	}

	cache := composeCacheOutputFromProto(testCLICache("cache-full"))
	cache.SessionID = "session-1"
	if cache.ID != "cache-full" || cache.Domain == "" || cache.Type == "" || cacheRefSessionText(cache) == "-" {
		t.Fatalf("cache output = %#v", cache)
	}
	emptyCache := composeCacheOutputFromProto(nil)
	if emptyCache.ID != "" {
		t.Fatalf("nil cache output = %#v", emptyCache)
	}
	cacheListOutput := composeCacheListOutputFromResponse(&agentcomposev2.ListCachesResponse{
		Caches:   []*agentcomposev2.CacheItem{testCLICache("cache-list")},
		Warnings: []string{"cache warning"},
	})
	if len(cacheListOutput.Caches) != 1 || len(cacheListOutput.Warnings) != 1 {
		t.Fatalf("cache list output = %#v", cacheListOutput)
	}
	cacheInspectOutput := composeCacheInspectOutputFromResponse(&agentcomposev2.InspectCacheResponse{
		Cache:    testCLICache("cache-inspect"),
		Warnings: []string{"inspect warning"},
	})
	if cacheInspectOutput.Cache.ID != "cache-inspect" || len(cacheInspectOutput.Warnings) != 1 {
		t.Fatalf("cache inspect output = %#v", cacheInspectOutput)
	}
	pruneOutput := composeCacheOperationOutputFromPruneResponse(&agentcomposev2.PruneCachesResponse{
		DryRun: true,
		Matched: []*agentcomposev2.CacheItem{
			testCLICache("cache-match"),
		},
		Skipped:  []*agentcomposev2.CacheItem{testCLICache("cache-skip")},
		Removed:  []string{"cache-old"},
		Warnings: []string{"prune warning"},
	})
	removeCacheOutput := composeCacheOperationOutputFromRemoveResponse(&agentcomposev2.RemoveCacheResponse{
		Matched: []*agentcomposev2.CacheItem{testCLICache("cache-remove")},
		Skipped: []*agentcomposev2.CacheItem{testCLICache("cache-remove-skip")},
		Removed: []string{"cache-remove"},
	})
	if len(pruneOutput.Matched) != 1 || len(pruneOutput.Skipped) != 1 || len(removeCacheOutput.Removed) != 1 {
		t.Fatalf("cache operation outputs prune=%#v remove=%#v", pruneOutput, removeCacheOutput)
	}
	text.Reset()
	if err := writeCacheListText(&text, cacheListOutput); err != nil {
		t.Fatalf("writeCacheListText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "CACHE ID") || !strings.Contains(text.String(), "cache-list") || !strings.Contains(text.String(), "Warnings") {
		t.Fatalf("cache list text = %q", text.String())
	}
	text.Reset()
	if err := writeCacheInspectText(&text, composeCacheInspectOutput{Cache: cache, Warnings: []string{"top warning"}}); err != nil {
		t.Fatalf("writeCacheInspectText returned error: %v", err)
	}
	for _, want := range []string{"Cache ID", "Image:", "Sandbox:", "Last used:", "References:", "Warnings:"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("cache inspect text %q missing %q", text.String(), want)
		}
	}
	text.Reset()
	if err := writeCacheOperationOutput(&text, false, composeCacheOperationOutput{
		DryRun:   true,
		Matched:  []composeCacheOutput{cache},
		Skipped:  []composeCacheOutput{{ID: "cache-skip", ShortID: "cache-skip", Driver: "docker", Type: "oci", Status: "active", BlockedReasons: []string{"in use"}}},
		Warnings: []string{"warning"},
	}); err != nil {
		t.Fatalf("writeCacheOperationOutput text returned error: %v", err)
	}
	if !strings.Contains(text.String(), "Dry-run") || !strings.Contains(text.String(), "Skipped") || !strings.Contains(text.String(), "in use") {
		t.Fatalf("cache operation text = %q", text.String())
	}
	text.Reset()
	if err := writeCacheOperationOutput(&text, true, composeCacheOperationOutput{Removed: []string{"cache-full"}}); err != nil {
		t.Fatalf("writeCacheOperationOutput JSON returned error: %v", err)
	}
	if !strings.Contains(text.String(), `"removed"`) {
		t.Fatalf("cache operation json = %q", text.String())
	}
	text.Reset()
	if err := writeCacheOperationOutput(&text, false, composeCacheOperationOutput{
		Matched: []composeCacheOutput{cache},
		Removed: []string{
			"cache-full",
		},
	}); err != nil {
		t.Fatalf("writeCacheOperationOutput removed text returned error: %v", err)
	}
	if !strings.Contains(text.String(), "Removed 1 cache") || !strings.Contains(text.String(), "Matched") {
		t.Fatalf("cache operation removed text = %q", text.String())
	}

	text.Reset()
	if err := writeSandboxPruneOutput(&text, false, composeSandboxPruneOutput{
		DryRun:   true,
		Matched:  []composePSSandboxOutput{{ID: "sandbox-1", ShortID: "sandbox-1", Agent: "reviewer", Status: "stopped", Driver: "boxlite", CreatedAt: "created"}},
		Skipped:  []composeSandboxPruneSkipped{{Sandbox: "sandbox-2", Reason: "running"}},
		Warnings: []string{"warning"},
	}); err != nil {
		t.Fatalf("writeSandboxPruneOutput returned error: %v", err)
	}
	if !strings.Contains(text.String(), "Use --force") || !strings.Contains(text.String(), "would remove") {
		t.Fatalf("sandbox prune text = %q", text.String())
	}
	text.Reset()
	if err := writeSandboxPruneOutput(&text, true, composeSandboxPruneOutput{Removed: []string{"sandbox-removed"}}); err != nil {
		t.Fatalf("writeSandboxPruneOutput JSON returned error: %v", err)
	}
	if !strings.Contains(text.String(), "sandbox-removed") {
		t.Fatalf("sandbox prune json = %q", text.String())
	}
	text.Reset()
	if err := writeSandboxPruneOutput(&text, false, composeSandboxPruneOutput{
		Matched: []composePSSandboxOutput{{ID: "sandbox-3", ShortID: "sandbox-3", Agent: "worker", Status: "stopped", Driver: "docker", UpdatedAt: "updated"}},
		Removed: []string{"sandbox-3"},
	}); err != nil {
		t.Fatalf("writeSandboxPruneOutput removed returned error: %v", err)
	}
	if !strings.Contains(text.String(), "Removed 1 sandbox") || !strings.Contains(text.String(), "sandbox-3") {
		t.Fatalf("sandbox prune removed text = %q", text.String())
	}

	for _, value := range []string{"linux/amd64", "linux/amd64/v8"} {
		platform, err := parseImagePlatform(value)
		if err != nil || platform.GetOs() != "linux" || platform.GetArchitecture() != "amd64" {
			t.Fatalf("parseImagePlatform(%q) = %#v err=%v", value, platform, err)
		}
	}
	if _, err := parseImagePlatform("linux"); err == nil || !strings.Contains(err.Error(), "expected os/arch") {
		t.Fatalf("parseImagePlatform invalid error = %v", err)
	}
	if platform, err := parseImagePlatform(""); err != nil || platform != nil {
		t.Fatalf("parseImagePlatform empty = %#v err=%v", platform, err)
	}
	if filter, err := cacheFilterFromOptions(composeCacheFilterOptions{}); err != nil || filter != nil {
		t.Fatalf("empty cache filter = %#v err=%v", filter, err)
	}
	if filter, err := cacheFilterFromOptions(composeCacheFilterOptions{Driver: "all", Type: "sandbox", Status: "referenced"}); err != nil || filter.GetDriver() != "all" || filter.GetType() != "session" || filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_REFERENCED {
		t.Fatalf("cache filter = %#v err=%v", filter, err)
	}
	if filter, err := cacheFilterFromPruneOptions(composeCachePruneOptions{OlderThan: "2h"}); err != nil || filter.GetOlderThanSeconds() != 7200 {
		t.Fatalf("prune filter = %#v err=%v", filter, err)
	}
	for _, tc := range []struct {
		options composeCachePruneOptions
		want    agentcomposev2.CacheStatus
	}{
		{options: composeCachePruneOptions{Unused: true}, want: agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED},
		{options: composeCachePruneOptions{Orphaned: true}, want: agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED},
		{options: composeCachePruneOptions{Expired: true}, want: agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED},
	} {
		filter, err := cacheFilterFromPruneOptions(tc.options)
		if err != nil || filter.GetStatus() != tc.want {
			t.Fatalf("shortcut prune filter = %#v err=%v want %v", filter, err, tc.want)
		}
	}
	for _, tc := range []struct {
		options composeCachePruneOptions
		want    string
	}{
		{options: composeCachePruneOptions{Unused: true, Orphaned: true}, want: "mutually exclusive"},
		{options: composeCachePruneOptions{Unused: true, composeCacheFilterOptions: composeCacheFilterOptions{Status: "active"}}, want: "cannot be combined"},
		{options: composeCachePruneOptions{OlderThan: "bad"}, want: "invalid --older-than"},
		{options: composeCachePruneOptions{OlderThan: "0s"}, want: "positive"},
		{options: composeCachePruneOptions{OlderThan: "500ms"}, want: "at least 1s"},
	} {
		if _, err := cacheFilterFromPruneOptions(tc.options); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("cacheFilterFromPruneOptions(%#v) error = %v, want %q", tc.options, err, tc.want)
		}
	}
	for _, tc := range []struct {
		options composeCacheFilterOptions
		want    string
	}{
		{options: composeCacheFilterOptions{Driver: "bad"}, want: "invalid --driver"},
		{options: composeCacheFilterOptions{Type: "bad"}, want: "invalid --type"},
		{options: composeCacheFilterOptions{Status: "bad"}, want: "invalid --status"},
	} {
		if _, err := cacheFilterFromOptions(tc.options); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("cacheFilterFromOptions(%#v) error = %v, want %q", tc.options, err, tc.want)
		}
	}
	if cacheDomainText(agentcomposev2.CacheDomain_CACHE_DOMAIN_UNSPECIFIED) != "unspecified" ||
		cacheDomainText(agentcomposev2.CacheDomain_CACHE_DOMAIN_OCI_IMAGE_STORE) != "oci-image-store" ||
		cacheDomainText(agentcomposev2.CacheDomain_CACHE_DOMAIN_RUNTIME_DERIVED_CACHE) != "runtime-derived-cache" ||
		cacheTypeText(agentcomposev2.CacheDomain_CACHE_DOMAIN_SESSION_EPHEMERAL_STATE) != "sandbox" ||
		cacheTypeText(agentcomposev2.CacheDomain_CACHE_DOMAIN_OCI_IMAGE_STORE) != "oci" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_ACTIVE) != "active" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_REFERENCED) != "referenced" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED) != "unused" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED) != "expired" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_UNKNOWN) != "unknown" ||
		imageStoreText(agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_UNSPECIFIED) != "unspecified" ||
		imageAvailabilityStatusText(agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_MISSING) != "missing" ||
		imageAvailabilityStatusText(agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE) != "available" ||
		imageOperationStatusText(agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED) != "succeeded" {
		t.Fatalf("status text helper mismatch")
	}
	for _, tc := range []struct {
		status agentcomposev2.RunStatus
		text   string
	}{
		{agentcomposev2.RunStatus_RUN_STATUS_PENDING, "pending"},
		{agentcomposev2.RunStatus_RUN_STATUS_RUNNING, "running"},
		{agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, "succeeded"},
		{agentcomposev2.RunStatus_RUN_STATUS_FAILED, "failed"},
		{agentcomposev2.RunStatus_RUN_STATUS_CANCELED, "canceled"},
		{agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED, "unspecified"},
	} {
		if got := runStatusText(tc.status); got != tc.text {
			t.Fatalf("runStatusText(%v) = %q, want %q", tc.status, got, tc.text)
		}
	}
	for _, tc := range []struct {
		source agentcomposev2.RunSource
		text   string
	}{
		{agentcomposev2.RunSource_RUN_SOURCE_MANUAL, "manual"},
		{agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER, "scheduler"},
		{agentcomposev2.RunSource_RUN_SOURCE_API, "api"},
		{agentcomposev2.RunSource_RUN_SOURCE_UNSPECIFIED, "unspecified"},
	} {
		if got := runSourceText(tc.source); got != tc.text {
			t.Fatalf("runSourceText(%v) = %q, want %q", tc.source, got, tc.text)
		}
	}
	if firstNonEmptyString("", " value ") != " value " || firstNonEmptyString(" ", "") != "" {
		t.Fatalf("firstNonEmptyString returned unexpected value")
	}
	quoted := shellQuoteCLIArg("it's complicated")
	if quoted != "'it'\"'\"'s complicated'" || shellQuoteCLIArg("") != "''" || shellQuoteCLIArg("plain") != "plain" {
		t.Fatalf("shellQuoteCLIArg returned %q", quoted)
	}
	unique := appendUniqueStrings([]string{"a", " "}, "b", "a", " c ")
	if strings.Join(unique, ",") != "a,b,c" {
		t.Fatalf("appendUniqueStrings = %#v", unique)
	}
}

func TestCLIRunStreamAndDetailEdgeBranches(t *testing.T) {
	t.Run("stream completes without terminal run", func(t *testing.T) {
		server := newRunServiceStubServer(t, runServiceStub{
			runAgentStream: func(context.Context, *connect.Request[agentcomposev2.RunAgentRequest], *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return nil
			},
		})
		defer server.Close()
		client := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
		_, _, _, err := runComposeRunStreamAndDetail(context.Background(), io.Discard, io.Discard, client, "project-1", "Project", &agentcomposev2.RunAgentRequest{AgentName: "reviewer"}, false)
		if err == nil || !strings.Contains(err.Error(), "stream completed without terminal run") {
			t.Fatalf("terminal missing error = %v", err)
		}
	})

	t.Run("stream rpc error", func(t *testing.T) {
		server := newRunServiceStubServer(t, runServiceStub{
			runAgentStream: func(context.Context, *connect.Request[agentcomposev2.RunAgentRequest], *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return connect.NewError(connect.CodeUnavailable, fmt.Errorf("runner unavailable"))
			},
		})
		defer server.Close()
		client := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
		_, _, _, err := runComposeRunStreamAndDetail(context.Background(), io.Discard, io.Discard, client, "project-1", "Project", &agentcomposev2.RunAgentRequest{AgentName: "reviewer"}, false)
		if err == nil || !strings.Contains(err.Error(), "runner unavailable") || commandExitCode(err) != exitCodeUnavailable {
			t.Fatalf("stream rpc error = %v code=%d", err, commandExitCode(err))
		}
	})

	t.Run("warnings aggregate and output can be suppressed", func(t *testing.T) {
		server := newRunServiceStubServer(t, runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     "run-warn",
					Warnings:  []string{"event warning"},
					Chunk:     "hidden\n",
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-warn",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-warn",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: req.Msg.GetAgentName(),
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						Warnings:  []string{"summary warning", "event warning"},
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				run := testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-warn", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "stored\n")
				run.Warnings = []string{"detail warning"}
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
			},
		})
		defer server.Close()
		client := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
		var stdout bytes.Buffer
		detail, completed, warnings, err := runComposeRunStreamAndDetail(context.Background(), &stdout, io.Discard, client, "project-1", "Project", &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "reviewer"}, true)
		if err != nil {
			t.Fatalf("run stream returned error: %v", err)
		}
		if stdout.String() != "" || detail.GetSummary().GetRunId() != "run-warn" || completed.GetRunId() != "run-warn" {
			t.Fatalf("stdout/detail/completed = %q/%#v/%#v", stdout.String(), detail, completed)
		}
		if strings.Join(warnings, "|") != "event warning|summary warning" {
			t.Fatalf("warnings = %#v", warnings)
		}
	})

	t.Run("output writer error stops stream", func(t *testing.T) {
		server := newRunServiceStubServer(t, runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     "run-write-error",
					Chunk:     "cannot write\n",
				})
			},
		})
		defer server.Close()
		client := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
		_, _, _, err := runComposeRunStreamAndDetail(context.Background(), failingWriter{}, io.Discard, client, "project-1", "Project", &agentcomposev2.RunAgentRequest{AgentName: "reviewer"}, false)
		if err == nil || !strings.Contains(err.Error(), "write failed") {
			t.Fatalf("writer error = %v", err)
		}
	})

	t.Run("get detail error is wrapped", func(t *testing.T) {
		server := newRunServiceStubServer(t, runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-detail-missing",
					Run:       &agentcomposev2.RunSummary{RunId: "run-detail-missing", ProjectId: req.Msg.GetProjectId(), AgentName: req.Msg.GetAgentName(), Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("missing run"))
			},
		})
		defer server.Close()
		client := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
		_, _, _, err := runComposeRunStreamAndDetail(context.Background(), io.Discard, io.Discard, client, "project-1", "Project", &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "reviewer"}, false)
		if err == nil || !strings.Contains(err.Error(), "get run run-detail-missing") || commandExitCode(err) != exitCodeUsage {
			t.Fatalf("get detail error = %v code=%d", err, commandExitCode(err))
		}
	})
}

func TestCLIRunCompletionErrorBranches(t *testing.T) {
	failed := &agentcomposev2.RunSummary{RunId: "run-fail-cleanup", Status: agentcomposev2.RunStatus_RUN_STATUS_FAILED, ExitCode: 8, Error: "agent failed"}
	detail := &agentcomposev2.RunDetail{CleanupError: "remove failed"}
	err := composeRunCompletionError("Project", "reviewer", failed, detail)
	if err == nil || commandExitCode(err) != 8 || !strings.Contains(err.Error(), "cleanup warning: remove failed") {
		t.Fatalf("failed cleanup completion error = %v code=%d", err, commandExitCode(err))
	}
	if got := runDetailCleanupError(nil); got != "" {
		t.Fatalf("nil cleanup error = %q", got)
	}
}

func TestCLIRunCommandAdditionalEdgeWorkflows(t *testing.T) {
	t.Run("optional prompt flag consumes positional value", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-optional-prompt
agents:
  reviewer:
    provider: codex
`)
		var sawRequest bool
		server := newComposeServiceStubServer(t, composeServiceStubs{
			run: runServiceStub{
				runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
					sawRequest = true
					if req.Msg.GetPrompt() != "positional prompt" || req.Msg.GetCommand() != "" || req.Msg.GetTriggerId() != "" {
						t.Fatalf("RunAgentStream request = %#v", req.Msg)
					}
					return stream.Send(&agentcomposev2.RunAgentStreamResponse{
						EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
						RunId:     "run-optional-prompt",
						Run:       &agentcomposev2.RunSummary{RunId: "run-optional-prompt", ProjectId: req.Msg.GetProjectId(), AgentName: "reviewer", Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED},
					})
				},
				getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
					return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-optional", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "--prompt", "positional prompt")
		if exitCode != 0 || stdout != "" || stderr != "" || !sawRequest {
			t.Fatalf("run optional prompt code/stdout/stderr/saw = %d/%q/%q/%v", exitCode, stdout, stderr, sawRequest)
		}
	})

	t.Run("optional command flag consumes positional value", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-optional-command
agents:
  reviewer:
    provider: codex
`)
		var sawRequest bool
		server := newComposeServiceStubServer(t, composeServiceStubs{
			run: runServiceStub{
				runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
					sawRequest = true
					if req.Msg.GetCommand() != "echo positional" || req.Msg.GetPrompt() != "" || req.Msg.GetTriggerId() != "" {
						t.Fatalf("RunAgentStream request = %#v", req.Msg)
					}
					return stream.Send(&agentcomposev2.RunAgentStreamResponse{
						EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
						RunId:     "run-optional-command",
						Run:       &agentcomposev2.RunSummary{RunId: "run-optional-command", ProjectId: req.Msg.GetProjectId(), AgentName: "reviewer", Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED},
					})
				},
				getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
					return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-optional", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "--command", "echo positional")
		if exitCode != 0 || stdout != "" || stderr != "" || !sawRequest {
			t.Fatalf("run optional command code/stdout/stderr/saw = %d/%q/%q/%v", exitCode, stdout, stderr, sawRequest)
		}
	})

	t.Run("detached response without run", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-detached-empty
agents:
  reviewer:
    provider: codex
`)
		server := newComposeServiceStubServer(t, composeServiceStubs{
			run: runServiceStub{
				startRun: func(context.Context, *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
					return connect.NewResponse(&agentcomposev2.StartRunResponse{Started: true}), nil
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "-d", "reviewer", "--prompt", "detached")
		if exitCode != exitCodeGeneral || stdout != "" || !strings.Contains(stderr, "response did not include run summary") {
			t.Fatalf("run detached empty code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
	})

	t.Run("interactive remove cleanup failure becomes command error", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-cleanup
agents:
  reviewer:
    provider: codex
`)
		server := newComposeServiceStubServer(t, composeServiceStubs{
			run: runServiceStub{
				runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
					return stream.Send(&agentcomposev2.RunAgentStreamResponse{
						EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
						RunId:     "run-interactive-cleanup",
						Run:       &agentcomposev2.RunSummary{RunId: "run-interactive-cleanup", ProjectId: req.Msg.GetProjectId(), AgentName: "reviewer", Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, SessionId: "sandbox-cleanup"},
					})
				},
				getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
					return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-cleanup", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
				},
			},
			sandbox: sandboxServiceStub{
				removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
					if req.Msg.GetSandboxId() != "sandbox-cleanup" || !req.Msg.GetForce() {
						t.Fatalf("RemoveSandbox request = %#v", req.Msg)
					}
					return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("cleanup unavailable"))
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommandWithInput("/exit\n", "run", "--host", server.URL, "--file", composePath, "--rm", "reviewer", "-i", "--prompt", "first prompt")
		if exitCode != exitCodeUnavailable || stdout != "" || !strings.Contains(stderr, "remove interactive sandbox sandbox-cleanup") {
			t.Fatalf("run interactive cleanup code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
	})
}

func TestCLICommandBranchSweepWorkflows(t *testing.T) {
	t.Run("project pull and build empty or invalid", func(t *testing.T) {
		noImageCompose := writeComposeFile(t, t.TempDir(), `
name: cli-empty-images
agents:
  reviewer:
    provider: codex
`)
		stdout, stderr, _, exitCode := executeCLICommand("pull", "--file", noImageCompose)
		if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "No project images configured") {
			t.Fatalf("pull no images code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		jsonOut, jsonErr, _, jsonCode := executeCLICommand("pull", "--file", noImageCompose, "--json")
		if jsonCode != 0 || jsonErr != "" || !strings.Contains(jsonOut, `"images": []`) {
			t.Fatalf("pull no images json code/stdout/stderr = %d/%q/%q", jsonCode, jsonOut, jsonErr)
		}
		buildOut, buildErr, _, buildCode := executeCLICommand("build", "--file", noImageCompose)
		if buildCode != 0 || buildErr != "" || !strings.Contains(buildOut, "No project images configured for build") {
			t.Fatalf("build no images code/stdout/stderr = %d/%q/%q", buildCode, buildOut, buildErr)
		}

		buildCompose := writeComposeFile(t, t.TempDir(), `
name: cli-build-invalid
agents:
  reviewer:
    provider: codex
    build:
      context: .
  tagged:
    provider: codex
    image: tagged:latest
    build:
      context: .
`)
		for _, tc := range []struct {
			name string
			args []string
			want string
		}{
			{name: "unknown agent", args: []string{"build", "--file", buildCompose, "missing"}, want: "unknown build agent"},
			{name: "missing tag", args: []string{"build", "--file", buildCompose, "reviewer"}, want: "requires image or build.tags"},
			{name: "bad build arg", args: []string{"build", "--file", buildCompose, "--build-arg", "BROKEN", "tagged"}, want: "invalid --build-arg"},
			{name: "bad platform", args: []string{"build", "--file", buildCompose, "--platform", "linux", "tagged"}, want: "expected os/arch"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
				if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, tc.want) {
					t.Fatalf("%v code/stdout/stderr = %d/%q/%q, want %q", tc.args, exitCode, stdout, stderr, tc.want)
				}
			})
		}
	})

	t.Run("exec stream error result branches", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-exec-branches
agents:
  reviewer:
    provider: codex
`)
		server := newComposeServiceStubServer(t, composeServiceStubs{
			exec: execServiceStub{
				execStream: func(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
					switch target := req.Msg.GetTarget().(type) {
					case *agentcomposev2.ExecRequest_SessionId:
						switch target.SessionId {
						case "no-result":
							return nil
						case "failed":
							return stream.Send(&agentcomposev2.ExecStreamResponse{
								EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED,
								Result: &agentcomposev2.ExecResult{
									ExecId: "exec-failed", SessionId: target.SessionId,
									Command: req.Msg.GetCommand(), ExitCode: 42, Success: false, Stderr: "boom\n",
								},
							})
						case "default-shell":
							if req.Msg.GetCommand().GetCommand() != "sh" || len(req.Msg.GetCommand().GetArgs()) != 0 {
								t.Fatalf("default shell request = %#v", req.Msg.GetCommand())
							}
							return stream.Send(&agentcomposev2.ExecStreamResponse{
								EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED,
								Result:    &agentcomposev2.ExecResult{ExecId: "exec-shell", SessionId: target.SessionId, Command: req.Msg.GetCommand(), Success: true},
							})
						}
					case *agentcomposev2.ExecRequest_RunId:
						if target.RunId == "run-stream-error" {
							return connect.NewError(connect.CodeUnavailable, fmt.Errorf("exec stream down"))
						}
					}
					return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unexpected exec request"))
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "no-result", "true")
		if exitCode != exitCodeGeneral || stdout != "" || !strings.Contains(stderr, "stream completed without result") {
			t.Fatalf("exec no result code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("exec", "--host", server.URL, "--file", composePath, "failed", "false")
		if exitCode != 42 || stdout != "" || !strings.Contains(stderr, "exec-failed") || !strings.Contains(stderr, "boom") {
			t.Fatalf("exec failed code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("exec", "--host", server.URL, "--file", composePath, "default-shell")
		if exitCode != 0 || stdout != "" || stderr != "" {
			t.Fatalf("exec default shell code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("exec", "--host", server.URL, "--file", composePath, "--run-id", "run-stream-error", "true")
		if exitCode != exitCodeUnavailable || stdout != "" || !strings.Contains(stderr, "exec stream down") {
			t.Fatalf("exec stream error code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		for _, tc := range []struct {
			args []string
			want string
		}{
			{args: []string{"exec", "--file", composePath, "--run-id", "run-1", "--agent", "reviewer", "true"}, want: "target can only be specified once"},
			{args: []string{"exec", "--file", composePath, "--run-id", "", "true"}, want: "requires a value"},
			{args: []string{"exec", "--file", composePath, "--agent", "", "true"}, want: "requires a value"},
		} {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v code/stdout/stderr = %d/%q/%q, want %q", tc.args, exitCode, stdout, stderr, tc.want)
			}
		}
	})

	t.Run("logs empty and error branches", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-empty-branches
agents:
  reviewer:
    provider: codex
`)
		listCalls := 0
		server := newComposeServiceStubServer(t, composeServiceStubs{
			run: runServiceStub{
				listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
					listCalls++
					if req.Msg.GetAgentName() == "reviewer" {
						return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("list unavailable"))
					}
					return connect.NewResponse(&agentcomposev2.ListRunsResponse{}), nil
				},
				getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("run missing"))
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath)
		if exitCode != 0 || stdout != "" || stderr != "" {
			t.Fatalf("logs empty text code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--json")
		if exitCode != 0 || stderr != "" || !strings.Contains(stdout, `"runs": null`) {
			t.Fatalf("logs empty json code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--agent", "reviewer")
		if exitCode != exitCodeUnavailable || stdout != "" || !strings.Contains(stderr, "list unavailable") {
			t.Fatalf("logs list error code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--run-id", "missing")
		if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "run missing") {
			t.Fatalf("logs get error code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		if listCalls < 3 {
			t.Fatalf("listRuns calls = %d", listCalls)
		}
	})

	t.Run("inspect usage and service errors", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-inspect-branches
agents:
  reviewer:
    provider: codex
`)
		server := newComposeServiceStubServer(t, composeServiceStubs{
			project: projectServiceStub{
				getProject: func(context.Context, *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project missing"))
				},
			},
			run: runServiceStub{
				getRun: func(context.Context, *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("run missing"))
				},
			},
			session: sessionServiceStub{
				getSession: func(context.Context, *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
					return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("sandbox unavailable"))
				},
			},
		})
		defer server.Close()
		for _, tc := range []struct {
			args []string
			code int
			want string
		}{
			{args: []string{"inspect", "image"}, code: exitCodeUsage, want: "requires an image reference"},
			{args: []string{"inspect", "--file", composePath, "agent"}, code: exitCodeUsage, want: "requires an agent name"},
			{args: []string{"inspect", "--file", composePath, "run"}, code: exitCodeUsage, want: "requires a run id"},
			{args: []string{"inspect", "--file", composePath, "sandbox"}, code: exitCodeUsage, want: "requires a sandbox"},
			{args: []string{"inspect", "--file", composePath, "session"}, code: exitCodeUsage, want: "requires a sandbox"},
			{args: []string{"inspect", "--file", composePath, "unknown"}, code: exitCodeUsage, want: "unsupported inspect target"},
			{args: []string{"inspect", "--host", server.URL, "--file", composePath, "project"}, code: exitCodeUsage, want: "has not been started"},
			{args: []string{"inspect", "--host", server.URL, "--file", composePath, "run", "missing"}, code: exitCodeUsage, want: "run missing"},
			{args: []string{"inspect", "--host", server.URL, "--file", composePath, "sandbox", "sandbox-1"}, code: exitCodeUnavailable, want: "sandbox unavailable"},
		} {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != tc.code || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v code/stdout/stderr = %d/%q/%q, want code %d contains %q", tc.args, exitCode, stdout, stderr, tc.code, tc.want)
			}
		}
	})

	t.Run("image and cache removal text branches", func(t *testing.T) {
		server := newComposeServiceStubServer(t, composeServiceStubs{
			image: imageServiceStub{
				removeImage: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
					return connect.NewResponse(&agentcomposev2.RemoveImageResponse{ImageRef: req.Msg.GetImageRef()}), nil
				},
			},
			cache: cacheServiceStub{
				removeCache: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveCacheRequest]) (*connect.Response[agentcomposev2.RemoveCacheResponse], error) {
					return connect.NewResponse(&agentcomposev2.RemoveCacheResponse{
						Skipped:  []*agentcomposev2.CacheItem{{CacheId: req.Msg.GetCacheId(), BlockedReasons: []string{"remove failed"}}},
						Warnings: []string{"cache-force remove failed: permission denied"},
					}), nil
				},
			},
		})
		defer server.Close()
		stdout, stderr, _, exitCode := executeCLICommand("rmi", "--host", server.URL, "agent:unused")
		if exitCode != 0 || stderr != "" || stdout != "Removed: agent:unused\n" {
			t.Fatalf("rmi removed text code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("cache", "rm", "--host", server.URL, "--force", "cache-force")
		if exitCode != exitCodeUsage || stdout == "" || !strings.Contains(stderr, "permission denied") {
			t.Fatalf("cache rm force skipped code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
	})
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("write failed")
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

	stdout, stderr, _, exitCode := executeCLICommand("ls", "--host", server.URL)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("ls code/stderr = %d / %q", exitCode, stderr)
	}
	for _, want := range []string{"ID", "NAME", "CONFIG FILE", "reviewer", "/path/to/reviewer/agent-compose.yml", "builder"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("ls output %q does not contain %q", stdout, want)
		}
	}

	verboseOut, verboseErr, _, verboseCode := executeCLICommand("ls", "--host", server.URL, "--verbose")
	if verboseCode != 0 || verboseErr != "" {
		t.Fatalf("ls --verbose code/stderr = %d / %q", verboseCode, verboseErr)
	}
	for _, want := range []string{"ID", "NAME", "PROJECT DIR", "SPEC HASH", "proj_1", "/path/to/reviewer", "sha256:builder", "active"} {
		if !strings.Contains(verboseOut, want) {
			t.Fatalf("ls --verbose output %q does not contain %q", verboseOut, want)
		}
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("ls", "--host", server.URL, "--json")
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

	stdout, stderr, _, exitCode := executeCLICommand("ls", "--host", server.URL, "--limit", "10", "--offset", "20", "--json")
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

func TestCLIImageRootCommandWarnsDeprecated(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("image")
	if exitCode != 0 {
		t.Fatalf("image root exit code = %d, stderr=%q", exitCode, stderr)
	}
	assertDeprecatedWarning(t, stderr, "agent-compose images")
	if !strings.Contains(stdout, "Deprecated") {
		t.Fatalf("image root help output = %q", stdout)
	}
}

func TestNewDaemonAppBuildsHandlerWithoutListening(t *testing.T) {
	testNewDaemonAppBuildsHandlerWithoutListening(t)
}

func testNewDaemonAppBuildsHandlerWithoutListening(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen test port: %v", err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Fatalf("close test listener: %v", err)
		}
	}()

	app, cancel := newTestDaemonApp(t, ln.Addr().String(), func(di do.Injector) error {
		t.Fatalf("background managers started during construction")
		return nil
	})
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	rec := httptest.NewRecorder()
	app.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/version status = %d, want %d", rec.Code, http.StatusOK)
	}
	var decoded struct {
		Data struct {
			Timezone       string `json:"timezone"`
			TimezoneOffset *int   `json:"timezone_offset"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("/api/version JSON decode failed: %v", err)
	}
	if decoded.Data.Timezone == "" || decoded.Data.TimezoneOffset == nil {
		t.Fatalf("/api/version timezone fields = %q/%v, want server timezone", decoded.Data.Timezone, decoded.Data.TimezoneOffset)
	}
}

func TestNewDaemonAppDefaultsToSocketOnlyConfig(t *testing.T) {
	testNewDaemonAppDefaultsToSocketOnlyConfig(t)
}

func testNewDaemonAppDefaultsToSocketOnlyConfig(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("HTTP_LISTEN", "")
	t.Setenv("AGENT_COMPOSE_SOCKET", "")
	t.Setenv("AGENT_COMPOSE_HOST", "")
	t.Setenv("RUNTIME_DRIVER", config.RuntimeDriverDocker)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app, err := NewDaemonApp(ctx, DaemonOptions{
		StartBackground: func(do.Injector) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewDaemonApp returned error: %v", err)
	}
	if app.Config.HttpListen != "" {
		t.Fatalf("HttpListen = %q, want empty by default", app.Config.HttpListen)
	}
	wantSocket := filepath.Join(runtimeDir, "agent-compose.sock")
	if app.Config.AgentComposeSocket != wantSocket {
		t.Fatalf("AgentComposeSocket = %q, want %q", app.Config.AgentComposeSocket, wantSocket)
	}
}

func TestDaemonAppRegistersCoreRoutes(t *testing.T) {
	testDaemonAppRegistersCoreRoutes(t)
}

func testDaemonAppRegistersCoreRoutes(t *testing.T) {
	t.Helper()
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", nil)
	defer cancel()

	server := httptest.NewServer(app.Echo)
	defer server.Close()

	healthClient := healthv1connect.NewHealthServiceClient(server.Client(), server.URL)
	healthResp, err := healthClient.Status(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("health status returned error: %v", err)
	}
	if healthResp.Msg.GetVersion() != config.BuildVersion {
		t.Fatalf("health version = %q, want %q", healthResp.Msg.GetVersion(), config.BuildVersion)
	}

	sessionClient := agentcomposev1connect.NewSessionServiceClient(server.Client(), server.URL)
	_, err = sessionClient.GetSession(context.Background(), connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: "missing"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetSession error code = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeNotFound, err)
	}

	projectClient := agentcomposev2connect.NewProjectServiceClient(server.Client(), server.URL)
	validateResp, err := projectClient.ValidateProject(context.Background(), connect.NewRequest(&agentcomposev2.ValidateProjectRequest{}))
	if err != nil {
		t.Fatalf("ValidateProject returned error: %v", err)
	}
	if validateResp.Msg.GetValid() || len(validateResp.Msg.GetIssues()) == 0 {
		t.Fatalf("ValidateProject empty response = %#v, want validation issue", validateResp.Msg)
	}
	runClient := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
	_, err = runClient.GetRun(context.Background(), connect.NewRequest(&agentcomposev2.GetRunRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || err == nil || !strings.Contains(err.Error(), "run id is required") {
		t.Fatalf("GetRun empty error = %v, want invalid argument with run id message", err)
	}
	execClient := agentcomposev2connect.NewExecServiceClient(server.Client(), server.URL)
	_, err = execClient.Exec(context.Background(), connect.NewRequest(&agentcomposev2.ExecRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || err == nil || !strings.Contains(err.Error(), "exec target is required") {
		t.Fatalf("Exec empty error = %v, want invalid argument with target message", err)
	}
	imageClient := agentcomposev2connect.NewImageServiceClient(server.Client(), server.URL)
	_, err = imageClient.InspectImage(context.Background(), connect.NewRequest(&agentcomposev2.InspectImageRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || err == nil || !strings.Contains(err.Error(), "image_ref is required") {
		t.Fatalf("InspectImage empty error = %v, want invalid argument with image_ref message", err)
	}

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/agentcompose.v2.ProjectService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.RunService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.ExecService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.ImageService/*"},
		{method: http.MethodGet, path: "/api/webhook-sources"},
		{method: http.MethodGet, path: "/api/agent-compose/workspaces/:workspaceID/files"},
		{method: http.MethodGet, path: "/jupyter/:sessionID"},
	} {
		if !hasRoute(app, route.method, route.path) {
			t.Fatalf("%s %s route was not registered", route.method, route.path)
		}
	}
}

func TestDaemonAppDoesNotRegisterStaticWebRoutes(t *testing.T) {
	testDaemonAppDoesNotRegisterStaticWebRoutes(t)
}

func testDaemonAppDoesNotRegisterStaticWebRoutes(t *testing.T) {
	t.Helper()
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", nil)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	rec := httptest.NewRecorder()
	app.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/version status = %d, want %d", rec.Code, http.StatusOK)
	}

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/ui"},
		{method: http.MethodGet, path: "/ui/*"},
		{method: http.MethodGet, path: "/*"},
	} {
		if hasRoute(app, route.method, route.path) {
			t.Fatalf("%s %s static route should not be registered", route.method, route.path)
		}
	}

	for _, path := range []string{"/ui", "/ui/runs", "/agent-compose.html"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		app.Echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func TestDaemonAppStartsBackgroundOnce(t *testing.T) {
	testDaemonAppStartsBackgroundOnce(t)
}

func testDaemonAppStartsBackgroundOnce(t *testing.T) {
	t.Helper()
	starts := 0
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", func(di do.Injector) error {
		starts++
		return nil
	})
	defer cancel()

	if err := app.StartBackground(); err != nil {
		t.Fatalf("first StartBackground returned error: %v", err)
	}
	if err := app.StartBackground(); err != nil {
		t.Fatalf("second StartBackground returned error: %v", err)
	}
	if starts != 1 {
		t.Fatalf("background start count = %d, want 1", starts)
	}
}

func TestDaemonAppServesUnixSocketAndOptionalTCP(t *testing.T) {
	testDaemonAppServesUnixSocketAndOptionalTCP(t)
}

func testDaemonAppServesUnixSocketAndOptionalTCP(t *testing.T) {
	t.Helper()
	socketPath := shortUnixSocketPath(t)
	tcpListen := freeTCPListenAddress(t)
	app, cancel := newTestDaemonAppWithSocketAndTCP(t, socketPath, tcpListen, nil)
	defer cancel()

	runCtx, stop := context.WithCancel(context.Background())
	errCh := runDaemonAppAsync(app, runCtx)

	unixClient := newUnixHTTPClient(socketPath)
	waitForHTTPStatus(t, unixClient, "http://agent-compose/api/version", http.StatusOK)
	waitForHTTPStatus(t, http.DefaultClient, "http://"+tcpListen+"/api/version", http.StatusOK)

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket path: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 0600", got)
	}

	stop()
	waitForDaemonExit(t, errCh)
	if _, err := os.Stat(socketPath); !errorsIsNotExist(err) {
		t.Fatalf("socket path still exists after shutdown, stat err=%v", err)
	}
	ln, err := net.Listen("tcp", tcpListen)
	if err != nil {
		t.Fatalf("tcp listener was not released after shutdown: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close tcp listener after shutdown: %v", err)
	}
}

func TestDaemonAppCleansStaleUnixSocket(t *testing.T) {
	testDaemonAppCleansStaleUnixSocket(t)
}

func testDaemonAppCleansStaleUnixSocket(t *testing.T) {
	t.Helper()
	socketPath := shortUnixSocketPath(t)
	createStaleUnixSocketFile(t, socketPath)
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("expected stale socket file to remain: %v", err)
	}

	app, cancel := newTestDaemonAppWithSocketAndTCP(t, socketPath, "", nil)
	defer cancel()
	runCtx, stop := context.WithCancel(context.Background())
	errCh := runDaemonAppAsync(app, runCtx)

	waitForHTTPStatus(t, newUnixHTTPClient(socketPath), "http://agent-compose/api/version", http.StatusOK)
	stop()
	waitForDaemonExit(t, errCh)
}

func TestDaemonAppReportsTCPPortConflict(t *testing.T) {
	testDaemonAppReportsTCPPortConflict(t)
}

func testDaemonAppReportsTCPPortConflict(t *testing.T) {
	t.Helper()
	socketPath := shortUnixSocketPath(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen occupied tcp port: %v", err)
	}
	defer func() {
		if err := occupied.Close(); err != nil {
			t.Fatalf("close occupied tcp port: %v", err)
		}
	}()
	httpListen := occupied.Addr().String()

	app, cancel := newTestDaemonAppWithSocketAndTCP(t, socketPath, httpListen, nil)
	defer cancel()
	err = app.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil error, want tcp port conflict")
	}
	for _, part := range []string{"HTTP_LISTEN", httpListen} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q does not contain %q", err.Error(), part)
		}
	}
	if _, statErr := os.Stat(socketPath); !errorsIsNotExist(statErr) {
		t.Fatalf("socket path was not cleaned after tcp listen failure, stat err=%v", statErr)
	}
}

func TestDaemonAppReportsUncreatableUnixSocketPath(t *testing.T) {
	testDaemonAppReportsUncreatableUnixSocketPath(t)
}

func testDaemonAppReportsUncreatableUnixSocketPath(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	parentFile := filepath.Join(root, "socket-parent-file")
	if err := os.WriteFile(parentFile, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write socket parent file: %v", err)
	}
	socketPath := filepath.Join(parentFile, "agent-compose.sock")

	app, cancel := newTestDaemonAppWithSocketAndTCP(t, socketPath, "", nil)
	defer cancel()
	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("Run returned nil error, want unix socket path error")
	}
	for _, part := range []string{"AGENT_COMPOSE_SOCKET", socketPath} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q does not contain %q", err.Error(), part)
		}
	}
}

func newTestDaemonApp(t *testing.T, httpListen string, startBackground func(do.Injector) error) (*DaemonApp, context.CancelFunc) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("HTTP_LISTEN", httpListen)
	t.Setenv("RUNTIME_DRIVER", config.RuntimeDriverDocker)
	t.Setenv("SESSION_START_TIMEOUT", "1s")
	t.Setenv("SESSION_STOP_TIMEOUT", "1s")
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("BOXLITE_HOME", filepath.Join(root, "boxlite"))
	t.Setenv("BOXLITE_RUNTIME_DIR", filepath.Join(root, "boxlite-runtime"))
	t.Setenv("DOCKER_HOME", filepath.Join(root, "docker"))
	t.Setenv("MICROSANDBOX_HOME", filepath.Join(root, "microsandbox"))
	t.Setenv("MICROSANDBOX_MSB_PATH", filepath.Join(root, "msb"))
	t.Setenv("MICROSANDBOX_LIB_PATH", filepath.Join(root, "libmicrosandbox_go_ffi.so"))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := DaemonOptions{}
	if startBackground != nil {
		opts.StartBackground = startBackground
	} else {
		opts.StartBackground = func(do.Injector) error { return nil }
	}
	app, err := NewDaemonApp(ctx, opts)
	if err != nil {
		cancel()
		t.Fatalf("NewDaemonApp returned error: %v", err)
	}
	return app, cancel
}

func newTestDaemonAppWithSocketAndTCP(t *testing.T, socketPath string, httpListen string, startBackground func(do.Injector) error) (*DaemonApp, context.CancelFunc) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("HTTP_LISTEN", httpListen)
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("RUNTIME_DRIVER", config.RuntimeDriverDocker)
	t.Setenv("SESSION_START_TIMEOUT", "1s")
	t.Setenv("SESSION_STOP_TIMEOUT", "1s")
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("BOXLITE_HOME", filepath.Join(root, "boxlite"))
	t.Setenv("BOXLITE_RUNTIME_DIR", filepath.Join(root, "boxlite-runtime"))
	t.Setenv("DOCKER_HOME", filepath.Join(root, "docker"))
	t.Setenv("MICROSANDBOX_HOME", filepath.Join(root, "microsandbox"))
	t.Setenv("MICROSANDBOX_MSB_PATH", filepath.Join(root, "msb"))
	t.Setenv("MICROSANDBOX_LIB_PATH", filepath.Join(root, "libmicrosandbox_go_ffi.so"))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := DaemonOptions{}
	if startBackground != nil {
		opts.StartBackground = startBackground
	} else {
		opts.StartBackground = func(do.Injector) error { return nil }
	}
	app, err := NewDaemonApp(ctx, opts)
	if err != nil {
		cancel()
		t.Fatalf("NewDaemonApp returned error: %v", err)
	}
	return app, cancel
}

func runDaemonAppAsync(app *DaemonApp, ctx context.Context) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()
	return errCh
}

func waitForDaemonExit(t *testing.T, errCh <-chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("daemon Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon Run did not return after shutdown")
	}
}

func waitForHTTPStatus(t *testing.T, client *http.Client, url string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Fatalf("close response body: %v", closeErr)
			}
			if resp.StatusCode == want {
				return
			}
			lastErr = fmt.Errorf("status = %d, want %d", resp.StatusCode, want)
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("GET %s did not return status %d: %v", url, want, lastErr)
}

func newUnixHTTPClient(socketPath string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: transport}
}

func freeTCPListenAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free tcp port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close free tcp port: %v", err)
	}
	return addr
}

func assertDeprecatedWarning(t *testing.T, stderr string, replacement string) {
	t.Helper()
	if !strings.Contains(stderr, "deprecated") || !strings.Contains(stderr, replacement) {
		t.Fatalf("deprecated warning stderr = %q, want replacement %q", stderr, replacement)
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
		content = "workspaces:\n  default:\n    provider: local\n    path: .\n" + content
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}
	return path
}

func inlineSchedulerComposeYAML(name string, intervalMs int) string {
	return fmt.Sprintf(`
name: %s
agents:
  reviewer:
    provider: codex
    image: guest:v1
    driver:
      boxlite: {}
    scheduler:
      script: |
        scheduler.interval("interval-review", function intervalReview() {}, %d);
`, name, intervalMs)
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

func testStatsMetric(value float64, unit string) *agentcomposev2.MetricValue {
	return &agentcomposev2.MetricValue{Value: &value, Unit: unit, Status: agentcomposev2.MetricStatus_METRIC_STATUS_OK}
}

type runServiceStub struct {
	startRun       func(context.Context, *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error)
	runAgentStream func(context.Context, *connect.Request[agentcomposev2.RunAgentRequest], *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error
	getRun         func(context.Context, *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error)
	listRuns       func(context.Context, *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error)
	followRunLogs  func(context.Context, *connect.Request[agentcomposev2.FollowRunLogsRequest], *connect.ServerStream[agentcomposev2.RunLogChunk]) error

	agentcomposev2connect.UnimplementedRunServiceHandler
}

func (s runServiceStub) StartRun(ctx context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
	if s.startRun == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("StartRun stub is not configured"))
	}
	return s.startRun(ctx, req)
}

func (s runServiceStub) RunAgentStream(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
	if s.runAgentStream == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RunAgentStream stub is not configured"))
	}
	return s.runAgentStream(ctx, req, stream)
}

func (s runServiceStub) GetRun(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	if s.getRun == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetRun stub is not configured"))
	}
	return s.getRun(ctx, req)
}

func (s runServiceStub) ListRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	if s.listRuns == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListRuns stub is not configured"))
	}
	return s.listRuns(ctx, req)
}

func (s runServiceStub) FollowRunLogs(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
	if s.followRunLogs == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("FollowRunLogs stub is not configured"))
	}
	return s.followRunLogs(ctx, req, stream)
}

func newRunServiceStubServer(t *testing.T, stub runServiceStub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewRunServiceHandler(stub)
	mux.Handle(path, handler)
	return httptest.NewServer(mux)
}

type composeServiceStubs struct {
	project projectServiceStub
	run     runServiceStub
	exec    execServiceStub
	image   imageServiceStub
	cache   cacheServiceStub
	volume  volumeServiceStub
	sandbox sandboxServiceStub
	session sessionServiceStub
	loader  loaderServiceStub
}

type projectServiceStub struct {
	getProject    func(context.Context, *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error)
	listProjects  func(context.Context, *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error)
	removeProject func(context.Context, *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error)

	agentcomposev2connect.UnimplementedProjectServiceHandler
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

type execServiceStub struct {
	exec       func(context.Context, *connect.Request[agentcomposev2.ExecRequest]) (*connect.Response[agentcomposev2.ExecResponse], error)
	execStream func(context.Context, *connect.Request[agentcomposev2.ExecRequest], *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error

	agentcomposev2connect.UnimplementedExecServiceHandler
}

func (s execServiceStub) Exec(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest]) (*connect.Response[agentcomposev2.ExecResponse], error) {
	if s.exec == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("Exec stub is not configured"))
	}
	return s.exec(ctx, req)
}

func (s execServiceStub) ExecStream(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
	if s.execStream == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ExecStream stub is not configured"))
	}
	return s.execStream(ctx, req, stream)
}

type imageServiceStub struct {
	listImages   func(context.Context, *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error)
	pullImage    func(context.Context, *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error)
	inspectImage func(context.Context, *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error)
	removeImage  func(context.Context, *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error)
	buildImage   func(context.Context, *connect.Request[agentcomposev2.BuildImageRequest], *connect.ServerStream[agentcomposev2.BuildImageEvent]) error

	agentcomposev2connect.UnimplementedImageServiceHandler
}

func (s imageServiceStub) ListImages(ctx context.Context, req *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error) {
	if s.listImages == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListImages stub is not configured"))
	}
	return s.listImages(ctx, req)
}

func (s imageServiceStub) PullImage(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
	if s.pullImage == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("PullImage stub is not configured"))
	}
	return s.pullImage(ctx, req)
}

func (s imageServiceStub) InspectImage(ctx context.Context, req *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
	if s.inspectImage == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("InspectImage stub is not configured"))
	}
	return s.inspectImage(ctx, req)
}

func (s imageServiceStub) RemoveImage(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
	if s.removeImage == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RemoveImage stub is not configured"))
	}
	return s.removeImage(ctx, req)
}

func (s imageServiceStub) BuildImage(ctx context.Context, req *connect.Request[agentcomposev2.BuildImageRequest], stream *connect.ServerStream[agentcomposev2.BuildImageEvent]) error {
	if s.buildImage == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("BuildImage stub is not configured"))
	}
	return s.buildImage(ctx, req, stream)
}

type cacheServiceStub struct {
	listCaches   func(context.Context, *connect.Request[agentcomposev2.ListCachesRequest]) (*connect.Response[agentcomposev2.ListCachesResponse], error)
	inspectCache func(context.Context, *connect.Request[agentcomposev2.InspectCacheRequest]) (*connect.Response[agentcomposev2.InspectCacheResponse], error)
	pruneCaches  func(context.Context, *connect.Request[agentcomposev2.PruneCachesRequest]) (*connect.Response[agentcomposev2.PruneCachesResponse], error)
	removeCache  func(context.Context, *connect.Request[agentcomposev2.RemoveCacheRequest]) (*connect.Response[agentcomposev2.RemoveCacheResponse], error)

	agentcomposev2connect.UnimplementedCacheServiceHandler
}

func (s cacheServiceStub) ListCaches(ctx context.Context, req *connect.Request[agentcomposev2.ListCachesRequest]) (*connect.Response[agentcomposev2.ListCachesResponse], error) {
	if s.listCaches == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListCaches stub is not configured"))
	}
	return s.listCaches(ctx, req)
}

func (s cacheServiceStub) InspectCache(ctx context.Context, req *connect.Request[agentcomposev2.InspectCacheRequest]) (*connect.Response[agentcomposev2.InspectCacheResponse], error) {
	if s.inspectCache == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("InspectCache stub is not configured"))
	}
	return s.inspectCache(ctx, req)
}

func (s cacheServiceStub) PruneCaches(ctx context.Context, req *connect.Request[agentcomposev2.PruneCachesRequest]) (*connect.Response[agentcomposev2.PruneCachesResponse], error) {
	if s.pruneCaches == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("PruneCaches stub is not configured"))
	}
	return s.pruneCaches(ctx, req)
}

func (s cacheServiceStub) RemoveCache(ctx context.Context, req *connect.Request[agentcomposev2.RemoveCacheRequest]) (*connect.Response[agentcomposev2.RemoveCacheResponse], error) {
	if s.removeCache == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RemoveCache stub is not configured"))
	}
	return s.removeCache(ctx, req)
}

type volumeServiceStub struct {
	listVolumes   func(context.Context, *connect.Request[agentcomposev2.ListVolumesRequest]) (*connect.Response[agentcomposev2.ListVolumesResponse], error)
	createVolume  func(context.Context, *connect.Request[agentcomposev2.CreateVolumeRequest]) (*connect.Response[agentcomposev2.CreateVolumeResponse], error)
	inspectVolume func(context.Context, *connect.Request[agentcomposev2.InspectVolumeRequest]) (*connect.Response[agentcomposev2.InspectVolumeResponse], error)
	removeVolume  func(context.Context, *connect.Request[agentcomposev2.RemoveVolumeRequest]) (*connect.Response[agentcomposev2.RemoveVolumeResponse], error)
	pruneVolumes  func(context.Context, *connect.Request[agentcomposev2.PruneVolumesRequest]) (*connect.Response[agentcomposev2.PruneVolumesResponse], error)

	agentcomposev2connect.UnimplementedVolumeServiceHandler
}

func (s volumeServiceStub) ListVolumes(ctx context.Context, req *connect.Request[agentcomposev2.ListVolumesRequest]) (*connect.Response[agentcomposev2.ListVolumesResponse], error) {
	if s.listVolumes == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListVolumes stub is not configured"))
	}
	return s.listVolumes(ctx, req)
}

func (s volumeServiceStub) CreateVolume(ctx context.Context, req *connect.Request[agentcomposev2.CreateVolumeRequest]) (*connect.Response[agentcomposev2.CreateVolumeResponse], error) {
	if s.createVolume == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("CreateVolume stub is not configured"))
	}
	return s.createVolume(ctx, req)
}

func (s volumeServiceStub) InspectVolume(ctx context.Context, req *connect.Request[agentcomposev2.InspectVolumeRequest]) (*connect.Response[agentcomposev2.InspectVolumeResponse], error) {
	if s.inspectVolume == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("InspectVolume stub is not configured"))
	}
	return s.inspectVolume(ctx, req)
}

func (s volumeServiceStub) RemoveVolume(ctx context.Context, req *connect.Request[agentcomposev2.RemoveVolumeRequest]) (*connect.Response[agentcomposev2.RemoveVolumeResponse], error) {
	if s.removeVolume == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RemoveVolume stub is not configured"))
	}
	return s.removeVolume(ctx, req)
}

func (s volumeServiceStub) PruneVolumes(ctx context.Context, req *connect.Request[agentcomposev2.PruneVolumesRequest]) (*connect.Response[agentcomposev2.PruneVolumesResponse], error) {
	if s.pruneVolumes == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("PruneVolumes stub is not configured"))
	}
	return s.pruneVolumes(ctx, req)
}

type sandboxServiceStub struct {
	removeSandbox func(context.Context, *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error)
	getStats      func(context.Context, *connect.Request[agentcomposev2.GetSandboxStatsRequest]) (*connect.Response[agentcomposev2.GetSandboxStatsResponse], error)

	agentcomposev2connect.UnimplementedSandboxServiceHandler
}

func (s sandboxServiceStub) RemoveSandbox(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
	if s.removeSandbox == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RemoveSandbox stub is not configured"))
	}
	return s.removeSandbox(ctx, req)
}

func (s sandboxServiceStub) GetSandboxStats(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxStatsRequest]) (*connect.Response[agentcomposev2.GetSandboxStatsResponse], error) {
	if s.getStats == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetSandboxStats stub is not configured"))
	}
	return s.getStats(ctx, req)
}

type sessionServiceStub struct {
	getSession    func(context.Context, *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error)
	listSessions  func(context.Context, *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error)
	resumeSession func(context.Context, *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error)
	stopSession   func(context.Context, *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error)

	agentcomposev1connect.UnimplementedSessionServiceHandler
}

func (s sessionServiceStub) GetSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	if s.getSession == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetSession stub is not configured"))
	}
	return s.getSession(ctx, req)
}

func (s sessionServiceStub) ListSessions(ctx context.Context, req *connect.Request[agentcomposev1.ListSessionsRequest]) (*connect.Response[agentcomposev1.ListSessionsResponse], error) {
	if s.listSessions == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListSessions stub is not configured"))
	}
	return s.listSessions(ctx, req)
}

func (s sessionServiceStub) ResumeSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	if s.resumeSession == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ResumeSession stub is not configured"))
	}
	return s.resumeSession(ctx, req)
}

func (s sessionServiceStub) StopSession(ctx context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	if s.stopSession == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("StopSession stub is not configured"))
	}
	return s.stopSession(ctx, req)
}

type loaderServiceStub struct {
	getLoader func(context.Context, *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error)

	agentcomposev1connect.UnimplementedLoaderServiceHandler
}

func (s loaderServiceStub) GetLoader(ctx context.Context, req *connect.Request[agentcomposev1.LoaderIDRequest]) (*connect.Response[agentcomposev1.LoaderResponse], error) {
	if s.getLoader == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetLoader stub is not configured"))
	}
	return s.getLoader(ctx, req)
}

func newComposeServiceStubServer(t *testing.T, stubs composeServiceStubs) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	if stubs.project.getProject != nil || stubs.project.listProjects != nil || stubs.project.removeProject != nil {
		path, handler := agentcomposev2connect.NewProjectServiceHandler(stubs.project)
		mux.Handle(path, handler)
	}
	if stubs.run.startRun != nil || stubs.run.runAgentStream != nil || stubs.run.getRun != nil || stubs.run.listRuns != nil || stubs.run.followRunLogs != nil {
		path, handler := agentcomposev2connect.NewRunServiceHandler(stubs.run)
		mux.Handle(path, handler)
	}
	if stubs.exec.exec != nil || stubs.exec.execStream != nil {
		path, handler := agentcomposev2connect.NewExecServiceHandler(stubs.exec)
		mux.Handle(path, handler)
	}
	if stubs.image.listImages != nil || stubs.image.pullImage != nil || stubs.image.inspectImage != nil || stubs.image.removeImage != nil || stubs.image.buildImage != nil {
		path, handler := agentcomposev2connect.NewImageServiceHandler(stubs.image)
		mux.Handle(path, handler)
	}
	if stubs.cache.listCaches != nil || stubs.cache.inspectCache != nil || stubs.cache.pruneCaches != nil || stubs.cache.removeCache != nil {
		path, handler := agentcomposev2connect.NewCacheServiceHandler(stubs.cache)
		mux.Handle(path, handler)
	}
	if stubs.volume.listVolumes != nil || stubs.volume.createVolume != nil || stubs.volume.inspectVolume != nil || stubs.volume.removeVolume != nil || stubs.volume.pruneVolumes != nil {
		path, handler := agentcomposev2connect.NewVolumeServiceHandler(stubs.volume)
		mux.Handle(path, handler)
	}
	if stubs.sandbox.removeSandbox != nil || stubs.sandbox.getStats != nil {
		path, handler := agentcomposev2connect.NewSandboxServiceHandler(stubs.sandbox)
		mux.Handle(path, handler)
	}
	if stubs.session.getSession != nil || stubs.session.listSessions != nil || stubs.session.resumeSession != nil || stubs.session.stopSession != nil {
		path, handler := agentcomposev1connect.NewSessionServiceHandler(stubs.session)
		mux.Handle(path, handler)
	}
	if stubs.loader.getLoader != nil {
		path, handler := agentcomposev1connect.NewLoaderServiceHandler(stubs.loader)
		mux.Handle(path, handler)
	}
	return httptest.NewServer(mux)
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
				ProjectId:       projectID,
				AgentName:       "reviewer",
				SchedulerId:     "scheduler-reviewer",
				ManagedLoaderId: "loader-reviewer",
				Enabled:         true,
				TriggerCount:    1,
			},
		},
	}
}

func testCLIImage(imageID, imageRef string) *agentcomposev2.Image {
	return &agentcomposev2.Image{
		ImageId:            imageID,
		ImageRef:           imageRef,
		ResolvedRef:        "agent@sha256:def",
		RepoTags:           []string{imageRef},
		RepoDigests:        []string{"agent@sha256:def"},
		Store:              agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
		AvailabilityStatus: agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE,
		Platform: &agentcomposev2.ImagePlatform{
			Os:           "linux",
			Architecture: "amd64",
		},
		SizeBytes:        1024,
		VirtualSizeBytes: 1024,
		CreatedAt:        "2026-06-11T00:00:00Z",
		InspectedAt:      "2026-06-11T01:00:00Z",
		Docker:           &agentcomposev2.DockerImageStatus{Local: true},
		Labels:           map[string]string{"role": "test"},
	}
}

func testCLICache(cacheID string) *agentcomposev2.CacheItem {
	return &agentcomposev2.CacheItem{
		CacheId:        cacheID,
		Domain:         agentcomposev2.CacheDomain_CACHE_DOMAIN_MATERIALIZED_IMAGE_CACHE,
		Driver:         "boxlite",
		Kind:           "materialized-rootfs",
		Path:           "/tmp/cache/rootfs",
		SizeBytes:      4096,
		ImageId:        "sha256:cache",
		ImageRef:       "agent:latest",
		ResolvedRef:    "agent@sha256:cache",
		Status:         agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED,
		Removable:      true,
		BlockedReasons: []string{"dry-run only"},
		LastUsedAt:     "2026-06-11T00:00:00Z",
		LastUsedSource: "mtime",
		References: []*agentcomposev2.CacheReference{{
			Type:        "image-metadata",
			Id:          "sha256:cache",
			Name:        "agent:latest",
			Path:        "/tmp/cache/rootfs",
			Status:      "stopped",
			Description: "agent@sha256:cache",
		}},
		Warnings: []string{"item warning"},
	}
}

func testCLIVolume(name string) *agentcomposev2.Volume {
	return &agentcomposev2.Volume{
		Name:      name,
		Driver:    "local",
		Path:      "/tmp/agent-compose/volumes/local/11111111-1111-4111-8111-111111111111/data",
		Labels:    map[string]string{"purpose": "cache"},
		ProjectId: "project-1",
		CreatedAt: "2026-07-07T12:00:00Z",
		UpdatedAt: "2026-07-07T12:00:00Z",
	}
}

func testCLISessionDetail(sessionID, vmStatus string) *agentcomposev1.SessionDetail {
	return &agentcomposev1.SessionDetail{
		Summary: testCLISessionSummary(sessionID, vmStatus, "project-cli", "reviewer", ""),
	}
}

func testCLISessionSummary(sessionID, vmStatus, projectID, agentName, runID string) *agentcomposev1.SessionSummary {
	tags := []*agentcomposev1.SessionTag{{Name: "project", Value: projectID}}
	if agentName != "" {
		tags = append(tags, &agentcomposev1.SessionTag{Name: "agent", Value: agentName})
	}
	if runID != "" {
		tags = append(tags, &agentcomposev1.SessionTag{Name: "run_id", Value: runID})
	}
	return &agentcomposev1.SessionSummary{
		SessionId:     sessionID,
		Title:         "CLI Session",
		Driver:        "boxlite",
		VmStatus:      vmStatus,
		WorkspacePath: "/workspace/" + sessionID,
		ProxyPath:     "/agent-compose/session/" + sessionID + "/lab",
		GuestImage:    "guest:latest",
		TriggerSource: "manual",
		CreatedAt:     "2026-06-11T00:00:00Z",
		UpdatedAt:     "2026-06-11T00:00:01Z",
		CellCount:     1,
		EventCount:    2,
		Tags:          tags,
	}
}

func testRunDetail(projectID, runID, agentName, sessionID string, status agentcomposev2.RunStatus, exitCode int32, output string) *agentcomposev2.RunDetail {
	return &agentcomposev2.RunDetail{
		Summary: &agentcomposev2.RunSummary{
			RunId:      runID,
			ProjectId:  projectID,
			AgentName:  agentName,
			Source:     agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
			Status:     status,
			SessionId:  sessionID,
			ExitCode:   exitCode,
			StartedAt:  "2026-06-11T00:00:00Z",
			UpdatedAt:  "2026-06-11T00:00:01Z",
			DurationMs: 1000,
		},
		Prompt:       "test prompt",
		Output:       output,
		ResultJson:   "{}",
		LogsPath:     "/tmp/output.txt",
		ArtifactsDir: "/tmp/artifacts",
	}
}

func requireCLICacheByPath(t *testing.T, caches []composeCacheOutput, path string) composeCacheOutput {
	t.Helper()
	for _, cache := range caches {
		if cache.Path == path {
			if strings.TrimSpace(cache.ID) == "" {
				t.Fatalf("cache for path %s has empty cache id: %#v", path, cache)
			}
			return cache
		}
	}
	t.Fatalf("missing cache for path %s in %#v", path, caches)
	return composeCacheOutput{}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func stringSliceContainsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

func assertLocalPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertLocalPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing, stat err=%v", path, err)
	}
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
}

func createStaleUnixSocketFile(t *testing.T, socketPath string) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("create unix socket fd: %v", err)
	}
	defer func() {
		if err := syscall.Close(fd); err != nil {
			t.Fatalf("close unix socket fd: %v", err)
		}
	}()
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: socketPath}); err != nil {
		t.Fatalf("bind stale unix socket file: %v", err)
	}
}

func shortUnixSocketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortUnixSocketDir(t), "ac.sock")
}

func shortUnixSocketDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ac-sock-")
	if err != nil {
		t.Fatalf("create short unix socket temp dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Fatalf("remove short unix socket temp dir: %v", err)
		}
	})
	return root
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}

func hasRoute(app *DaemonApp, method, path string) bool {
	for _, route := range app.Echo.Routes() {
		if route.Method != method {
			continue
		}
		if route.Path == path {
			return true
		}
	}
	return false
}
