package e2e

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EExamplesDockerLiveProvider(t *testing.T) {
	if strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_EXAMPLES_LIVE_PROVIDER")) != "1" {
		t.Skip("set AGENT_COMPOSE_E2E_EXAMPLES_LIVE_PROVIDER=1 to use the configured live provider")
	}
	image := strings.TrimSpace(os.Getenv(examplesDockerImageEnv))
	if image == "" {
		t.Fatalf("%s is required", examplesDockerImageEnv)
	}

	providerEnv := liveProviderE2EEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	root := e2eRepoRoot(t)
	testRoot, err := os.MkdirTemp(root, ".docker-examples-live-provider-e2e-")
	if err != nil {
		t.Fatalf("create live-provider E2E root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	dockerClient := newE2EDockerClient(t, ctx, image)
	t.Cleanup(func() { removeE2EDockerContainersUnderRoot(t, dockerClient, testRoot) })
	binary := e2eDaemonBinary(t, ctx, root, testRoot)
	listen := unusedLoopbackAddress(t)
	baseURL := "http://" + listen
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		t.Fatalf("split daemon listen address: %v", err)
	}
	gateway := dockerBridgeGateway(t, ctx, dockerClient)
	providerEnv["AGENT_COMPOSE_RUNTIME_BASE_URL"] = "http://" + net.JoinHostPort(gateway, port)
	providerEnv["DOCKER_HOST_SANDBOX_ROOT"] = ""
	providerEnv["HTTP_LISTEN"] = net.JoinHostPort("0.0.0.0", port)

	daemon := startE2EDaemonWithOverrides(t, binary, root, testRoot, listen, image, providerEnv)
	waitForE2EDaemon(t, ctx, daemon, baseURL)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("live-provider examples daemon log:\n%s", daemon.logs.String())
		}
	})

	file := filepath.Join(root, "examples", "agent-compose", "docker-minimal", "agent-compose.yml")
	applyExampleProject(t, ctx, binary, root, baseURL, file)
	defer downExampleProject(t, ctx, binary, root, baseURL, file)
	const marker = "agent-compose live provider ok"
	out := exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "--json", "run", "reviewer", "--prompt", "Reply with exactly: "+marker)
	t.Logf("verified live-provider output: %s", strings.TrimSpace(out))
	var run exampleRunOutput
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatalf("decode live-provider run output %q: %v", out, err)
	}
	if run.Status != "succeeded" || run.SandboxID == "" || !strings.Contains(run.Output, marker) {
		t.Fatalf("live-provider run = %#v", run)
	}
}

func liveProviderE2EEnv(t *testing.T) map[string]string {
	t.Helper()
	required := []string{"LLM_API_ENDPOINT", "LLM_API_KEY", "LLM_MODEL"}
	env := make(map[string]string, len(required)+3)
	for _, name := range required {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			t.Fatalf("%s is required for the live-provider E2E", name)
		}
		env[name] = value
	}
	for _, name := range []string{"LLM_API_PROTOCOL", "LLM_TIMEOUT", "AGENT_TIMEOUT"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			env[name] = value
		}
	}
	return env
}
