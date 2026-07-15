package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	networkapi "github.com/docker/docker/api/types/network"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestE2EExamplesDockerProvider(t *testing.T) {
	if strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_EXAMPLES_PROVIDER")) != "1" {
		t.Skip("set AGENT_COMPOSE_E2E_EXAMPLES_PROVIDER=1 to run provider-backed examples")
	}
	image := strings.TrimSpace(os.Getenv(examplesDockerImageEnv))
	if image == "" {
		t.Fatalf("%s is required", examplesDockerImageEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	root := e2eRepoRoot(t)
	testRoot, err := os.MkdirTemp(root, ".docker-examples-provider-e2e-")
	if err != nil {
		t.Fatalf("create provider E2E root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	dockerClient := newE2EDockerClient(t, ctx, image)
	t.Cleanup(func() { removeE2EDockerContainersUnderRoot(t, dockerClient, testRoot) })
	binary := e2eDaemonBinary(t, ctx, root, testRoot)
	listen := unusedLoopbackAddress(t)
	baseURL := "http://" + listen
	provider := newExampleOpenAIProvider(t)
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		t.Fatalf("split daemon listen address: %v", err)
	}
	gateway := dockerBridgeGateway(t, ctx, dockerClient)
	overrides := providerE2EEnv(provider.URL, "http://"+net.JoinHostPort(gateway, port), port)
	daemon := startE2EDaemonWithOverrides(t, binary, root, testRoot, listen, image, overrides)
	waitForE2EDaemon(t, ctx, daemon, baseURL)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("provider examples daemon log:\n%s", daemon.logs.String())
		}
	})

	examplesRoot := filepath.Join(root, "examples", "agent-compose")
	t.Run("manual cron trigger", func(t *testing.T) {
		file := filepath.Join(examplesRoot, "docker-scheduler-cron", "agent-compose.yml")
		applyExampleProject(t, ctx, binary, root, baseURL, file)
		defer downExampleProject(t, ctx, binary, root, baseURL, file)
		out := exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "--json", "scheduler", "trigger", "reviewer", "hourly-review", "--prompt", "Reply with exactly: cron scheduler ok")
		t.Logf("verified manual cron output: %s", strings.TrimSpace(out))
		if !strings.Contains(out, `"status": "succeeded"`) || !strings.Contains(out, "cron scheduler ok") {
			t.Fatalf("cron trigger output = %q", out)
		}
	})

	t.Run("automatic timeout trigger", func(t *testing.T) {
		file := filepath.Join(examplesRoot, "docker-scheduler-timeout", "agent-compose.yml")
		up := exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "--json", "up")
		defer downExampleProject(t, ctx, binary, root, baseURL, file)
		var applied struct {
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
		}
		if err := json.Unmarshal([]byte(up), &applied); err != nil || applied.Project.ID == "" {
			t.Fatalf("decode timeout up output %q: %v", up, err)
		}
		client := agentcomposev2connect.NewRunServiceClient(newE2EHTTPClient(), baseURL)
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			resp, err := client.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
				ProjectId: applied.Project.ID,
				AgentName: "reviewer",
				Source:    agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER,
				Limit:     20,
			}))
			if err == nil {
				for _, run := range resp.Msg.GetRuns() {
					if run.GetStatus() == agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
						t.Logf("verified timeout run: id=%s sandbox=%s status=%s", run.GetRunId(), run.GetSandboxId(), run.GetStatus())
						return
					}
					if run.GetStatus() == agentcomposev2.RunStatus_RUN_STATUS_FAILED {
						t.Fatalf("timeout run failed: %s", run.GetError())
					}
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatal("timeout scheduler did not produce a successful run")
	})
}

func providerE2EEnv(endpoint, runtimeBaseURL, daemonPort string) map[string]string {
	return map[string]string{
		"AGENT_COMPOSE_RUNTIME_BASE_URL": runtimeBaseURL,
		"AGENT_TIMEOUT":                  "1m",
		"DOCKER_HOST_SANDBOX_ROOT":       "",
		"HTTP_LISTEN":                    net.JoinHostPort("0.0.0.0", daemonPort),
		"LLM_API_ENDPOINT":               endpoint,
		"LLM_API_KEY":                    "example-provider-key",
		"LLM_API_PROTOCOL":               "chat_completions",
		"LLM_MODEL":                      "gpt-5.4",
		"OPENAI_API_KEY":                 "",
		"OPENAI_BASE_URL":                "",
	}
}

func dockerBridgeGateway(t *testing.T, ctx context.Context, dockerClient interface {
	NetworkInspect(context.Context, string, networkapi.InspectOptions) (networkapi.Inspect, error)
}) string {
	t.Helper()
	network, err := dockerClient.NetworkInspect(ctx, "bridge", networkapi.InspectOptions{})
	if err != nil {
		t.Fatalf("inspect Docker bridge network: %v", err)
	}
	for _, config := range network.IPAM.Config {
		if gateway := strings.TrimSpace(config.Gateway); gateway != "" {
			return gateway
		}
	}
	t.Fatal("Docker bridge network has no gateway")
	return ""
}

func newExampleOpenAIProvider(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		t.Logf("controlled provider request %s %s", request.Method, request.URL.Path)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
			http.Error(w, "read request", http.StatusBadRequest)
			return
		}
		if !strings.HasSuffix(request.URL.Path, "/chat/completions") {
			t.Errorf("provider path = %q", request.URL.Path)
			http.NotFound(w, request)
			return
		}
		text := "cron scheduler ok"
		if strings.Contains(string(body), "timeout scheduler ok") {
			text = "timeout scheduler ok"
		}
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-example\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", text)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-example\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, "{\"id\":\"chatcmpl-example\",\"object\":\"chat.completion\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":\"stop\"}]}", text)
	}))
	t.Cleanup(server.Close)
	return server
}
