package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

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
	cmd      *exec.Cmd
	done     chan error
	logs     synchronizedBuffer
	stopOnce sync.Once
}

func startE2EDaemon(t *testing.T, binary, repoRoot, testRoot, listenAddress, image string) *e2eDaemonProcess {
	return startE2EDaemonWithOverrides(t, binary, repoRoot, testRoot, listenAddress, image, nil)
}

func startE2EDaemonWithOverrides(t *testing.T, binary, repoRoot, testRoot, listenAddress, image string, overrides map[string]string) *e2eDaemonProcess {
	t.Helper()
	process := &e2eDaemonProcess{done: make(chan error, 1)}
	process.cmd = exec.Command(binary, "daemon")
	process.cmd.Dir = repoRoot
	env := map[string]string{
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
	}
	for name, value := range overrides {
		env[name] = value
	}
	process.cmd.Env = overrideE2EEnv(os.Environ(), env)
	process.cmd.Stdout = &process.logs
	process.cmd.Stderr = &process.logs
	if err := process.cmd.Start(); err != nil {
		t.Fatalf("start agent-compose daemon: %v", err)
	}
	go func() { process.done <- process.cmd.Wait() }()
	t.Cleanup(func() { process.stop(t) })
	return process
}

func (p *e2eDaemonProcess) stop(t *testing.T) {
	t.Helper()
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	p.stopOnce.Do(func() {
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
	})
}

func waitForE2EDaemon(t *testing.T, ctx context.Context, daemon *e2eDaemonProcess, baseURL string) {
	t.Helper()
	client := newE2EHTTPClient()
	client.Timeout = time.Second
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

func newE2EHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}
}

func newE2EDockerClient(t *testing.T, ctx context.Context, images ...string) *client.Client {
	t.Helper()
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create Docker client: %v", err)
	}
	if _, err := dockerClient.Ping(ctx); err != nil {
		_ = dockerClient.Close()
		t.Fatalf("Docker daemon is required: %v", err)
	}
	for _, image := range images {
		if _, err := dockerClient.ImageInspect(ctx, image); err != nil {
			_ = dockerClient.Close()
			t.Fatalf("Docker image %q is required: %v", image, err)
		}
	}
	t.Cleanup(func() {
		if err := dockerClient.Close(); err != nil {
			t.Logf("close Docker client: %v", err)
		}
	})
	return dockerClient
}

func inspectE2EDockerSandboxContainer(t *testing.T, ctx context.Context, dockerClient *client.Client, sandboxID string) containerapi.InspectResponse {
	t.Helper()
	containerInfo, err := findE2EDockerSandboxContainer(ctx, dockerClient, sandboxID)
	if err != nil {
		t.Fatal(err)
	}
	return containerInfo
}

func findE2EDockerSandboxContainer(ctx context.Context, dockerClient *client.Client, sandboxID string) (containerapi.InspectResponse, error) {
	args := filters.NewArgs(
		filters.Arg("label", "agent-compose.sandbox_id="+sandboxID),
		filters.Arg("label", "agent-compose.driver=docker"),
	)
	containers, err := dockerClient.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: args})
	if err != nil {
		return containerapi.InspectResponse{}, fmt.Errorf("list Docker sandbox containers: %w", err)
	}
	if len(containers) != 1 {
		return containerapi.InspectResponse{}, fmt.Errorf("Docker sandbox container count = %d, want 1", len(containers))
	}
	containerInfo, err := dockerClient.ContainerInspect(ctx, containers[0].ID)
	if err != nil {
		return containerapi.InspectResponse{}, fmt.Errorf("inspect Docker sandbox container: %w", err)
	}
	return containerInfo, nil
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

func removeE2EDockerContainersUnderRoot(t *testing.T, dockerClient *client.Client, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := filters.NewArgs(filters.Arg("label", "agent-compose.driver=docker"))
	containers, err := dockerClient.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: args})
	if err != nil {
		t.Errorf("list Docker containers for root cleanup: %v", err)
		return
	}
	root = filepath.Clean(root) + string(filepath.Separator)
	for _, item := range containers {
		info, err := dockerClient.ContainerInspect(ctx, item.ID)
		if err != nil {
			t.Errorf("inspect Docker container %s for root cleanup: %v", item.ID, err)
			continue
		}
		owned := false
		for _, mount := range info.Mounts {
			if strings.HasPrefix(filepath.Clean(mount.Source)+string(filepath.Separator), root) {
				owned = true
				break
			}
		}
		if owned {
			if err := dockerClient.ContainerRemove(ctx, item.ID, containerapi.RemoveOptions{Force: true}); err != nil {
				t.Errorf("remove Docker container %s for root cleanup: %v", item.ID, err)
			}
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
