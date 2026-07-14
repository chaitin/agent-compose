package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"

	agentcomposeapp "agent-compose/pkg/agentcompose/app"
	"agent-compose/pkg/config"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestE2EDockerSchedulerScriptHelloWorldFlow(t *testing.T) {
	const helloText = "hello world from e2e scheduler"
	guestImage := firstNonEmptyEnv("AGENT_COMPOSE_E2E_GUEST_IMAGE", "agent-compose-guest:latest")
	requireDockerGuestImage(t, guestImage)

	root := t.TempDir()
	socketPath := shortUnixSocketPath(t)
	t.Setenv("DATA_ROOT", root)
	t.Setenv("HTTP_LISTEN", "")
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "")
	t.Setenv("RUNTIME_DRIVER", config.RuntimeDriverDocker)
	t.Setenv("DEFAULT_IMAGE", guestImage)
	t.Setenv("DOCKER_DEFAULT_IMAGE", guestImage)
	t.Setenv("SANDBOX_START_TIMEOUT", "90s")
	t.Setenv("SANDBOX_STOP_TIMEOUT", "15s")
	t.Setenv("LOADER_RUN_TIMEOUT", "2m")
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("BOXLITE_HOME", filepath.Join(root, "boxlite"))
	t.Setenv("BOXLITE_RUNTIME_DIR", filepath.Join(root, "boxlite-runtime"))
	t.Setenv("DOCKER_HOME", filepath.Join(root, "docker"))
	t.Setenv("MICROSANDBOX_HOME", filepath.Join(root, "microsandbox"))
	t.Setenv("MICROSANDBOX_MSB_PATH", filepath.Join(root, "msb"))
	t.Setenv("MICROSANDBOX_LIB_PATH", filepath.Join(root, "libmicrosandbox_go_ffi.so"))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app, err := NewDaemonApp(ctx, DaemonOptions{
		StartBackground: func(di do.Injector) error {
			return agentcomposeapp.StartBackground(di)
		},
	})
	if err != nil {
		t.Fatalf("NewDaemonApp returned error: %v", err)
	}

	runCtx, stop := context.WithCancel(context.Background())
	errCh := runDaemonAppAsync(app, runCtx)
	t.Cleanup(func() {
		stop()
		waitForDaemonExit(t, errCh)
	})
	client := newUnixHTTPClient(socketPath)
	waitForHTTPStatus(t, client, "http://agent-compose/api/version", http.StatusOK)

	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	projectDir := filepath.Join(root, "project")
	scriptDir := filepath.Join(projectDir, "scripts")
	if err := os.MkdirAll(scriptDir, 0o700); err != nil {
		t.Fatalf("create scheduler script dir: %v", err)
	}
	scriptPath := filepath.Join(scriptDir, "hello-scheduler.js")
	if err := os.WriteFile(scriptPath, []byte(fmt.Sprintf(`
scheduler.cron("hello-every-minute", "*/1 * * * *", async function() {
  const result = await scheduler.shell("echo %s", {
    sessionPolicy: "new",
    title: "e2e docker hello"
  });
  return { ok: result.success, output: result.output };
});
`, helloText)), 0o600); err != nil {
		t.Fatalf("write scheduler script: %v", err)
	}
	composePath := writeComposeFile(t, projectDir, fmt.Sprintf(`
name: e2e-docker-script
workspaces:
  default:
    provider: local
    path: .
agents:
  hello:
    provider: codex
    image: %s
    driver:
      docker: {}
    scheduler:
      script:
        url: ./scripts/hello-scheduler.js
`, guestImage))

	stdout, stderr, _, exitCode := executeCLICommand("up", "--file", composePath)
	if exitCode != 0 {
		t.Fatalf("compose up exit code = %d, stderr=%q, stdout=%q", exitCode, stderr, stdout)
	}
	if stderr != "" {
		t.Fatalf("compose up stderr = %q, want empty", stderr)
	}

	projectClient := agentcomposev2connect.NewProjectServiceClient(client, "http://agent-compose")
	scheduler := waitForManagedHelloScheduler(t, projectClient)
	detail, err := projectClient.GetScheduler(context.Background(), connect.NewRequest(&agentcomposev2.GetSchedulerRequest{
		Project:   &agentcomposev2.ProjectRef{ProjectId: scheduler.GetProjectId()},
		AgentName: scheduler.GetAgentName(),
	}))
	if err != nil {
		t.Fatalf("GetScheduler returned error: %v", err)
	}
	for _, trigger := range detail.Msg.GetTriggers() {
		t.Logf("resolved trigger %s enabled=%t next_fire_at=%v", trigger.GetTriggerId(), trigger.GetEnabled(), trigger.GetNextFireAt().AsTime())
	}
	runID, event := waitForScheduledHelloRun(t, projectClient, scheduler, helloText)
	if !strings.Contains(event.GetPayloadJson(), "sandboxId") || !strings.Contains(event.GetPayloadJson(), "cellId") {
		t.Fatalf("scheduler command event payload does not contain sandbox and cell links: %s", event.GetPayloadJson())
	}

	downOut, downErr, _, downCode := executeCLICommand("down", "--file", composePath)
	if downCode != 0 {
		t.Fatalf("compose down exit code = %d, stderr=%q, stdout=%q", downCode, downErr, downOut)
	}
	if downErr != "" {
		t.Fatalf("compose down stderr = %q, want empty", downErr)
	}
	t.Logf("scheduled run %s completed through scheduler %s with docker command output", runID, scheduler.GetSchedulerId())
}

func requireDockerGuestImage(t *testing.T, image string) {
	t.Helper()
	if _, err := osexec.LookPath("docker"); err != nil {
		t.Skip("docker CLI is required for this E2E test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := osexec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skipf("docker daemon is required for this E2E test: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := osexec.CommandContext(ctx, "docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("docker image %q is required; build it with `task image:agent-compose-guest`: %v", image, err)
	}
}

func firstNonEmptyEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func waitForManagedHelloScheduler(t *testing.T, client agentcomposev2connect.ProjectServiceClient) *agentcomposev2.SchedulerSummary {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.ListSchedulers(context.Background(), connect.NewRequest(&agentcomposev2.ListSchedulersRequest{Limit: 100}))
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for _, scheduler := range resp.Msg.GetSchedulers() {
			if scheduler.GetAgentName() == "hello" && scheduler.GetTriggerCount() == 1 && scheduler.GetEnabled() {
				return scheduler
			}
		}
		lastErr = fmt.Errorf("managed hello scheduler not found in %d schedulers", len(resp.Msg.GetSchedulers()))
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("scheduler was not created: %v", lastErr)
	return nil
}

func waitForScheduledHelloRun(t *testing.T, projectClient agentcomposev2connect.ProjectServiceClient, scheduler *agentcomposev2.SchedulerSummary, helloText string) (string, *agentcomposev2.SchedulerEvent) {
	t.Helper()
	timeout := time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)) + 90*time.Second
	if timeout < 90*time.Second {
		timeout = 90 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		eventsResp, err := projectClient.ListSchedulerEvents(context.Background(), connect.NewRequest(&agentcomposev2.ListSchedulerEventsRequest{
			Project:   &agentcomposev2.ProjectRef{ProjectId: scheduler.GetProjectId()},
			AgentName: scheduler.GetAgentName(),
			Limit:     100,
		}))
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		for _, event := range eventsResp.Msg.GetEvents() {
			if event.GetType() == "loader.run.failed" {
				lastErr = fmt.Errorf("loader run %s failed: %s", event.GetRunId(), event.GetMessage())
				continue
			}
			if event.GetRunId() != "" && event.GetType() == "loader.command.completed" && event.GetLevel() == "info" && strings.Contains(event.GetMessage(), helloText) {
				return event.GetRunId(), event
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("scheduled hello run did not complete before %s: %v", deadline.Format(time.RFC3339), lastErr)
	return "", nil
}
