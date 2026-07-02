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
	"google.golang.org/protobuf/types/known/emptypb"

	appdomain "agent-compose/internal/app"
	"agent-compose/pkg/config"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
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
	t.Setenv("SESSION_START_TIMEOUT", "90s")
	t.Setenv("SESSION_STOP_TIMEOUT", "15s")
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
			return appdomain.StartBackground(di)
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
	composePath := writeComposeFile(t, filepath.Join(root, "project"), fmt.Sprintf(`
name: e2e-docker-script
agents:
  hello:
    provider: codex
    image: %s
    driver:
      docker: {}
    scheduler:
      script: |
        scheduler.cron("hello-every-minute", "*/1 * * * *", async function() {
          const result = await scheduler.shell("echo %s", {
            sessionPolicy: "new",
            title: "e2e docker hello"
          });
          return { ok: result.success, output: result.output };
        });
`, guestImage, helloText))

	stdout, stderr, _, exitCode := executeCLICommand("up", "--file", composePath)
	if exitCode != 0 {
		t.Fatalf("compose up exit code = %d, stderr=%q, stdout=%q", exitCode, stderr, stdout)
	}
	if stderr != "" {
		t.Fatalf("compose up stderr = %q, want empty", stderr)
	}

	loaderClient := agentcomposev1connect.NewLoaderServiceClient(client, "http://agent-compose")
	loaderID := waitForManagedHelloLoader(t, loaderClient)
	runID, event := waitForScheduledHelloRun(t, loaderClient, loaderID, helloText)
	if event.GetLinkedSessionId() == "" || event.GetLinkedCellId() == "" {
		t.Fatalf("loader command event links = session %q cell %q, want both set", event.GetLinkedSessionId(), event.GetLinkedCellId())
	}

	downOut, downErr, _, downCode := executeCLICommand("down", "--file", composePath)
	if downCode != 0 {
		t.Fatalf("compose down exit code = %d, stderr=%q, stdout=%q", downCode, downErr, downOut)
	}
	if downErr != "" {
		t.Fatalf("compose down stderr = %q, want empty", downErr)
	}
	t.Logf("scheduled run %s completed through loader %s with docker command output", runID, loaderID)
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

func waitForManagedHelloLoader(t *testing.T, client agentcomposev1connect.LoaderServiceClient) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.ListLoaders(context.Background(), connect.NewRequest(&emptypb.Empty{}))
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for _, loader := range resp.Msg.GetLoaders() {
			if strings.Contains(loader.GetName(), "e2e-docker-script/hello scheduler") &&
				loader.GetDriver() == config.RuntimeDriverDocker &&
				loader.GetTriggerCount() == 1 &&
				loader.GetEnabled() {
				return loader.GetLoaderId()
			}
		}
		lastErr = fmt.Errorf("managed hello loader not found in %d loaders", len(resp.Msg.GetLoaders()))
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("loader was not created: %v", lastErr)
	return ""
}

func waitForScheduledHelloRun(t *testing.T, client agentcomposev1connect.LoaderServiceClient, loaderID string, helloText string) (string, *agentcomposev1.LoaderEvent) {
	t.Helper()
	timeout := time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)) + 90*time.Second
	if timeout < 90*time.Second {
		timeout = 90 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		runsResp, err := client.ListLoaderRuns(context.Background(), connect.NewRequest(&agentcomposev1.ListLoaderRunsRequest{
			LoaderId: loaderID,
			Limit:    10,
		}))
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		for _, run := range runsResp.Msg.GetRuns() {
			if run.GetStatus() == "failed" {
				lastErr = fmt.Errorf("loader run %s failed: %s", run.GetRunId(), run.GetError())
				continue
			}
			if run.GetStatus() != "succeeded" ||
				run.GetTriggerKind() != agentcomposev1.LoaderTriggerKind_LOADER_TRIGGER_KIND_CRON ||
				!strings.Contains(run.GetTriggerSource(), "cron:") {
				continue
			}
			event, ok := findHelloCommandEvent(t, client, loaderID, run.GetRunId(), helloText)
			if ok {
				return run.GetRunId(), event
			}
			lastErr = fmt.Errorf("succeeded run %s has no hello command event yet", run.GetRunId())
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("scheduled hello run did not complete before %s: %v", deadline.Format(time.RFC3339), lastErr)
	return "", nil
}

func findHelloCommandEvent(t *testing.T, client agentcomposev1connect.LoaderServiceClient, loaderID, runID, helloText string) (*agentcomposev1.LoaderEvent, bool) {
	t.Helper()
	eventsResp, err := client.ListLoaderEvents(context.Background(), connect.NewRequest(&agentcomposev1.ListLoaderEventsRequest{
		LoaderId: loaderID,
		Limit:    50,
	}))
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	for _, event := range eventsResp.Msg.GetEvents() {
		if event.GetRunId() == runID &&
			event.GetType() == "loader.command.completed" &&
			event.GetLevel() == "info" &&
			strings.Contains(event.GetMessage(), helloText) {
			return event, true
		}
	}
	return nil, false
}
