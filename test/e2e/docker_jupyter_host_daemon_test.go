package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

const dockerJupyterE2EImageEnv = "AGENT_COMPOSE_E2E_DOCKER_JUPYTER_IMAGE"

func TestE2EDockerJupyterHostDaemonStopResume(t *testing.T) {
	image := strings.TrimSpace(os.Getenv(dockerJupyterE2EImageEnv))
	if image == "" {
		t.Skipf("set %s to a Docker image containing JupyterLab", dockerJupyterE2EImageEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	repoRoot := e2eRepoRoot(t)
	testRoot, err := os.MkdirTemp(repoRoot, ".docker-jupyter-e2e-")
	if err != nil {
		t.Fatalf("create Docker-visible test root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	dockerClient := newE2EDockerClient(t, ctx, image)
	t.Cleanup(func() { _ = dockerClient.Close() })
	if hostname, hostErr := os.Hostname(); hostErr == nil {
		if _, inspectErr := dockerClient.ContainerInspect(ctx, hostname); inspectErr == nil {
			t.Skip("host-daemon Jupyter E2E requires the test runner to run outside Docker")
		}
	}

	binary := e2eDaemonBinary(t, ctx, repoRoot, testRoot)
	listenAddress := unusedLoopbackAddress(t)
	baseURL := "http://" + listenAddress
	daemon := startE2EDaemon(t, binary, repoRoot, testRoot, listenAddress, image)
	t.Cleanup(func() { daemon.stop(t) })
	waitForE2EDaemon(t, ctx, daemon, baseURL)

	httpClient := &http.Client{
		Timeout: 3 * time.Minute,
		Transport: func() *http.Transport {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			return transport
		}(),
	}
	projectClient := agentcomposev2connect.NewProjectServiceClient(httpClient, baseURL)
	runClient := agentcomposev2connect.NewRunServiceClient(httpClient, baseURL)
	sandboxClient := agentcomposev2connect.NewSandboxServiceClient(httpClient, baseURL)

	projectResp, err := projectClient.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: &agentcomposev2.ProjectSpec{
			Name: "docker-jupyter-e2e",
			Agents: []*agentcomposev2.AgentSpec{{
				Name:     "reviewer",
				Provider: "codex",
				Image:    image,
				Driver: &agentcomposev2.DriverSpec{
					Name:   "docker",
					Docker: &agentcomposev2.DockerDriverSpec{},
				},
			}},
		},
		Source: &agentcomposev2.ProjectSource{
			ComposePath: filepath.Join(testRoot, "agent-compose.yml"),
			ProjectDir:  testRoot,
		},
	}))
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v\ndaemon log:\n%s", err, daemon.logs.String())
	}
	if !projectResp.Msg.GetApplied() || projectResp.Msg.GetProject() == nil || projectResp.Msg.GetProject().GetSummary() == nil {
		t.Fatalf("ApplyProject did not apply project; issues: %s", formatE2EProjectIssues(projectResp.Msg.GetIssues()))
	}
	projectID := projectResp.Msg.GetProject().GetSummary().GetProjectId()
	if projectID == "" {
		t.Fatalf("ApplyProject returned empty project ID: %#v", projectResp.Msg)
	}

	runResp, err := runClient.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Command:         "printf docker-jupyter-e2e-ok",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: "docker-jupyter-e2e-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Jupyter: &agentcomposev2.RunJupyterSpec{
			Enabled: true,
			Expose:  true,
		},
	}))
	if err != nil {
		t.Fatalf("RunAgent returned error: %v\ndaemon log:\n%s", err, daemon.logs.String())
	}
	run := runResp.Msg.GetRun()
	sandboxID := run.GetSummary().GetSandboxId()
	if sandboxID == "" || run.GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || run.GetOutput() != "docker-jupyter-e2e-ok" {
		t.Fatalf("RunAgent sandbox_id=%q status=%s error=%q output_matches=%t", sandboxID, run.GetSummary().GetStatus(), run.GetSummary().GetError(), run.GetOutput() == "docker-jupyter-e2e-ok")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = sandboxClient.RemoveSandbox(cleanupCtx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxID, Force: true}))
		removeE2EDockerSandboxFallback(t, cleanupCtx, dockerClient, sandboxID)
	})

	proxyPath := filepath.Join(testRoot, "sandboxes", sandboxID, "proxy", "jupyter.json")
	initialState := readE2EProxyState(t, proxyPath)
	assertE2EHostProxyState(t, initialState)
	containerID, initialDockerPort := inspectE2EDockerJupyterPort(t, ctx, dockerClient, sandboxID, initialState.GuestPort)
	if initialState.HostPort != initialDockerPort {
		t.Fatalf("initial proxy host port = %d, Docker binding = %d", initialState.HostPort, initialDockerPort)
	}
	assertE2EJupyterReady(t, ctx, httpClient, baseURL, sandboxID, initialState)
	assertE2EJupyterReady(t, ctx, httpClient, "http://127.0.0.1:"+strconv.Itoa(initialState.HostPort), sandboxID, initialState)

	if _, err := sandboxClient.StopSandbox(ctx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{SandboxId: sandboxID})); err != nil {
		t.Fatalf("StopSandbox returned error: %v", err)
	}
	staleState := readE2EProxyState(t, proxyPath)
	staleState.HostPort = 1
	writeE2EProxyState(t, proxyPath, staleState)

	if _, err := sandboxClient.ResumeSandbox(ctx, connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: sandboxID})); err != nil {
		t.Fatalf("ResumeSandbox returned error: %v\ndaemon log:\n%s", err, daemon.logs.String())
	}
	resumedState := readE2EProxyState(t, proxyPath)
	assertE2EHostProxyState(t, resumedState)
	if resumedState.HostPort == staleState.HostPort {
		t.Fatalf("resumed proxy state retained stale host port %d", staleState.HostPort)
	}
	resumedContainerID, resumedDockerPort := inspectE2EDockerJupyterPort(t, ctx, dockerClient, sandboxID, resumedState.GuestPort)
	if resumedContainerID != containerID {
		t.Fatalf("resume used container %s, want existing container %s", resumedContainerID, containerID)
	}
	if resumedState.HostPort != resumedDockerPort {
		t.Fatalf("resumed proxy host port = %d, Docker binding = %d", resumedState.HostPort, resumedDockerPort)
	}
	assertE2EJupyterReady(t, ctx, httpClient, baseURL, sandboxID, resumedState)
	t.Logf("Docker Jupyter binding refreshed from persisted stale port to 127.0.0.1:%d", resumedState.HostPort)
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type e2eDaemonProcess struct {
	cmd  *exec.Cmd
	done chan error
	logs synchronizedBuffer
}

func startE2EDaemon(t *testing.T, binary, repoRoot, testRoot, listenAddress, image string) *e2eDaemonProcess {
	t.Helper()
	process := &e2eDaemonProcess{done: make(chan error, 1)}
	process.cmd = exec.Command(binary, "daemon")
	process.cmd.Dir = repoRoot
	process.cmd.Env = overrideE2EEnv(os.Environ(), map[string]string{
		"AGENT_COMPOSE_SOCKET":     filepath.Join(testRoot, "agent-compose.sock"),
		"AUTH_PASSWORD":            "",
		"AUTH_USERNAME":            "",
		"DATA_ROOT":                testRoot,
		"DEFAULT_IMAGE":            image,
		"DOCKER_DEFAULT_IMAGE":     image,
		"DOCKER_HOST_SANDBOX_ROOT": filepath.Join(testRoot, "sandboxes"),
		"DOCKER_HOST_SESSION_ROOT": "",
		"HTTP_BASIC_AUTH":          "",
		"HTTP_LISTEN":              listenAddress,
		"JUPYTER_PROXY_BASE":       "/jupyter",
		"JUPYTER_READY_TIMEOUT":    "2m",
		"LLM_API_ENDPOINT":         "",
		"LLM_API_KEY":              "",
		"OPENAI_API_KEY":           "",
		"RUNTIME_DRIVER":           "docker",
		"SANDBOX_ROOT":             filepath.Join(testRoot, "sandboxes"),
		"SANDBOX_START_TIMEOUT":    "3m",
	})
	process.cmd.Stdout = &process.logs
	process.cmd.Stderr = &process.logs
	if err := process.cmd.Start(); err != nil {
		t.Fatalf("start agent-compose daemon: %v", err)
	}
	go func() { process.done <- process.cmd.Wait() }()
	return process
}

func (p *e2eDaemonProcess) stop(t *testing.T) {
	t.Helper()
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case err := <-p.done:
		if err != nil {
			t.Logf("agent-compose daemon exit: %v", err)
		}
	case <-time.After(15 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Log("agent-compose daemon required forced termination")
	}
}

func waitForE2EDaemon(t *testing.T, ctx context.Context, daemon *e2eDaemonProcess, baseURL string) {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Timeout: time.Second, Transport: transport}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/version", nil)
		if err == nil {
			resp, requestErr := client.Do(req)
			if requestErr == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("agent-compose daemon did not become ready at %s\ndaemon log:\n%s", baseURL, daemon.logs.String())
}

func e2eDaemonBinary(t *testing.T, ctx context.Context, repoRoot, testRoot string) string {
	t.Helper()
	if configured := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_BINARY")); configured != "" {
		binary, err := filepath.Abs(configured)
		if err != nil {
			t.Fatalf("resolve AGENT_COMPOSE_E2E_BINARY: %v", err)
		}
		return binary
	}
	binary := filepath.Join(testRoot, "agent-compose")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/agent-compose")
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build agent-compose daemon: %v\n%s", err, output)
	}
	return binary
}

func newE2EDockerClient(t *testing.T, ctx context.Context, image string) *client.Client {
	t.Helper()
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create Docker client: %v", err)
	}
	if _, err := dockerClient.Ping(ctx); err != nil {
		_ = dockerClient.Close()
		t.Fatalf("Docker daemon is required: %v", err)
	}
	if _, err := dockerClient.ImageInspect(ctx, image); err != nil {
		_ = dockerClient.Close()
		t.Fatalf("Docker Jupyter image %q is required: %v", image, err)
	}
	return dockerClient
}

func inspectE2EDockerJupyterPort(t *testing.T, ctx context.Context, dockerClient *client.Client, sandboxID string, guestPort int) (string, int) {
	t.Helper()
	args := filters.NewArgs(
		filters.Arg("label", "agent-compose.sandbox_id="+sandboxID),
		filters.Arg("label", "agent-compose.driver=docker"),
	)
	containers, err := dockerClient.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: args})
	if err != nil {
		t.Fatalf("list Docker sandbox containers: %v", err)
	}
	if len(containers) != 1 {
		t.Fatalf("Docker sandbox container count = %d, want 1", len(containers))
	}
	containerInfo, err := dockerClient.ContainerInspect(ctx, containers[0].ID)
	if err != nil {
		t.Fatalf("inspect Docker sandbox container: %v", err)
	}
	if containerInfo.NetworkSettings == nil {
		t.Fatal("Docker sandbox has no network settings")
	}
	port := nat.Port(strconv.Itoa(guestPort) + "/tcp")
	bindings := containerInfo.NetworkSettings.Ports[port]
	for _, binding := range bindings {
		if binding.HostIP != "127.0.0.1" {
			continue
		}
		hostPort, err := strconv.Atoi(binding.HostPort)
		if err == nil && hostPort > 0 {
			return containerInfo.ID, hostPort
		}
	}
	t.Fatalf("Docker sandbox bindings for %s = %#v, want non-zero 127.0.0.1 binding", port, bindings)
	return "", 0
}

func assertE2EJupyterReady(t *testing.T, ctx context.Context, client *http.Client, baseURL, sandboxID string, state domain.ProxyState) {
	t.Helper()
	endpoint := strings.TrimRight(baseURL, "/") + "/jupyter/" + sandboxID + "/api/kernelspecs?" + url.Values{"token": []string{state.Token}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("create Jupyter request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET Jupyter kernelspecs: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET Jupyter kernelspecs status = %d, want 200", resp.StatusCode)
	}
	var payload struct {
		Kernelspecs map[string]json.RawMessage `json:"kernelspecs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode Jupyter kernelspecs: %v", err)
	}
	if len(payload.Kernelspecs) == 0 {
		t.Fatal("Jupyter kernelspecs response is empty")
	}
}

func assertE2EHostProxyState(t *testing.T, state domain.ProxyState) {
	t.Helper()
	if !state.Enabled || !state.Exposed || state.GuestHost != "127.0.0.1" || state.HostPort <= 0 || state.GuestPort <= 0 || state.Token == "" {
		t.Fatalf("host daemon proxy state enabled=%t exposed=%t guest_host_present=%t host_port=%d guest_port=%d token_present=%t",
			state.Enabled, state.Exposed, strings.TrimSpace(state.GuestHost) != "", state.HostPort, state.GuestPort, state.Token != "")
	}
}

func readE2EProxyState(t *testing.T, path string) domain.ProxyState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read proxy state: %v", err)
	}
	var state domain.ProxyState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode proxy state: %v", err)
	}
	return state
}

func writeE2EProxyState(t *testing.T, path string, state domain.ProxyState) {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("encode proxy state: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write proxy state: %v", err)
	}
}

func removeE2EDockerSandboxFallback(t *testing.T, ctx context.Context, dockerClient *client.Client, sandboxID string) {
	t.Helper()
	args := filters.NewArgs(filters.Arg("label", "agent-compose.sandbox_id="+sandboxID))
	containers, err := dockerClient.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: args})
	if err != nil {
		t.Logf("fallback Docker sandbox lookup failed: %v", err)
		return
	}
	for _, item := range containers {
		if err := dockerClient.ContainerRemove(ctx, item.ID, containerapi.RemoveOptions{Force: true}); err != nil {
			t.Logf("fallback Docker sandbox removal failed for %s: %v", item.ID, err)
		}
	}
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate daemon listen address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release daemon listen address: %v", err)
	}
	return address
}

func overrideE2EEnv(environ []string, overrides map[string]string) []string {
	values := make(map[string]string, len(environ)+len(overrides))
	for _, item := range environ {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			values[name] = value
		}
	}
	for name, value := range overrides {
		values[name] = value
	}
	result := make([]string, 0, len(values))
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	return result
}

func e2eRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("find repository root from %s", dir)
		}
		dir = parent
	}
}

func formatE2EProjectIssues(issues []*agentcomposev2.ProjectValidationIssue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, fmt.Sprintf("%s: %s", issue.GetPath(), issue.GetMessage()))
	}
	return strings.Join(parts, "; ")
}
