package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

const examplesDockerImageEnv = "AGENT_COMPOSE_E2E_EXAMPLES_IMAGE"

type exampleRunOutput struct {
	ID         string `json:"id"`
	AgentName  string `json:"agent_name"`
	Status     string `json:"status"`
	SandboxID  string `json:"sandbox_id"`
	Output     string `json:"output"`
	ResultJSON string `json:"result_json"`
}

func TestE2EExamplesDocker(t *testing.T) {
	image := strings.TrimSpace(os.Getenv(examplesDockerImageEnv))
	if image == "" {
		t.Skipf("set %s to the locally available published guest image", examplesDockerImageEnv)
	}
	const manifestImage = "ghcr.io/chaitin/agent-compose-guest:latest"
	if image != manifestImage {
		t.Fatalf("%s = %q, examples use %q", examplesDockerImageEnv, image, manifestImage)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	root := e2eRepoRoot(t)
	testRoot, err := os.MkdirTemp(root, ".docker-examples-e2e-")
	if err != nil {
		t.Fatalf("create Docker-visible E2E root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	dockerClient := newE2EDockerClient(t, ctx, image)
	t.Cleanup(func() { removeE2EDockerContainersUnderRoot(t, dockerClient, testRoot) })
	binary := e2eDaemonBinary(t, ctx, root, testRoot)
	listen := unusedLoopbackAddress(t)
	baseURL := "http://" + listen
	daemon := startE2EDaemonWithOverrides(t, binary, root, testRoot, listen, image, map[string]string{
		"DOCKER_HOST_SANDBOX_ROOT": "",
	})
	waitForE2EDaemon(t, ctx, daemon, baseURL)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("examples daemon log:\n%s", daemon.logs.String())
		}
	})

	examplesRoot := filepath.Join(root, "examples", "agent-compose")
	t.Run("minimal", func(t *testing.T) {
		file := filepath.Join(examplesRoot, "docker-minimal", "agent-compose.yml")
		applyExampleProject(t, ctx, binary, root, baseURL, file)
		defer downExampleProject(t, ctx, binary, root, baseURL, file)
		run := runExampleCommand(t, ctx, binary, root, baseURL, file, "reviewer", "printf 'docker minimal ok\\n'", true)
		t.Logf("verified run: id=%s sandbox=%s status=%s output=%q", run.ID, run.SandboxID, run.Status, run.Output)
		if run.Status != "succeeded" || !strings.Contains(run.Output, "docker minimal ok") || run.SandboxID == "" {
			t.Fatalf("minimal run = %#v", run)
		}
		out := exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "exec", run.SandboxID, "--", "pwd")
		if strings.TrimSpace(out) == "" {
			t.Fatal("minimal exec returned no working directory")
		}
	})

	t.Run("workspace lifecycle", func(t *testing.T) {
		dir := filepath.Join(examplesRoot, "docker-workspace-lifecycle")
		file := filepath.Join(dir, "agent-compose.yml")
		applyExampleProject(t, ctx, binary, root, baseURL, file)
		defer downExampleProject(t, ctx, binary, root, baseURL, file)
		run := runExampleCommand(t, ctx, binary, root, baseURL, file, "worker", "test -f README.md && printf 'sandbox-only\\n' > generated.txt", true)
		t.Logf("verified run: id=%s sandbox=%s status=%s", run.ID, run.SandboxID, run.Status)
		exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "stop", run.SandboxID)
		exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "resume", run.SandboxID)
		out := exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "exec", run.SandboxID, "--", "cat", "generated.txt")
		if strings.TrimSpace(out) != "sandbox-only" {
			t.Fatalf("resumed workspace output = %q", out)
		}
		t.Logf("verified resumed output: %q", strings.TrimSpace(out))
		if _, err := os.Stat(filepath.Join(dir, "workspace", "generated.txt")); !os.IsNotExist(err) {
			t.Fatalf("sandbox change leaked to source: %v", err)
		}
		exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "stop", run.SandboxID)
		exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "rm", run.SandboxID)
	})

	t.Run("multi agent", func(t *testing.T) {
		file := filepath.Join(examplesRoot, "docker-multi-agent", "agent-compose.yml")
		applyExampleProject(t, ctx, binary, root, baseURL, file)
		defer downExampleProject(t, ctx, binary, root, baseURL, file)
		for _, agent := range []string{"reviewer", "tester"} {
			run := runExampleCommand(t, ctx, binary, root, baseURL, file, agent, "test -f project.txt && printf '"+agent+" ok\\n'", false)
			t.Logf("verified %s run: id=%s sandbox=%s status=%s output=%q", agent, run.ID, run.SandboxID, run.Status, run.Output)
			if run.AgentName != agent || !strings.Contains(run.Output, agent+" ok") {
				t.Fatalf("%s run = %#v", agent, run)
			}
		}
	})

	t.Run("environment and secrets", func(t *testing.T) {
		file := filepath.Join(examplesRoot, "docker-env-secrets", "agent-compose.yml")
		config := exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "config")
		if strings.Contains(config, "safe-example-secret") || !strings.Contains(config, "********") {
			t.Fatalf("redacted config = %q", config)
		}
		applyExampleProject(t, ctx, binary, root, baseURL, file)
		defer downExampleProject(t, ctx, binary, root, baseURL, file)
		command := `test "$PROJECT_VALUE" = project-level && test "$AGENT_VALUE" = agent-level && test "$PROJECT_SECRET" = safe-example-secret && test "$AGENT_SECRET" = safe-example-secret && printf 'environment ok\n'`
		run := runExampleCommand(t, ctx, binary, root, baseURL, file, "inspector", command, false)
		t.Logf("verified run: id=%s sandbox=%s status=%s output=%q", run.ID, run.SandboxID, run.Status, run.Output)
		if !strings.Contains(run.Output, "environment ok") {
			t.Fatalf("environment run = %#v", run)
		}
	})

	t.Run("volume persistence", func(t *testing.T) {
		file := filepath.Join(examplesRoot, "docker-volume-persistence", "agent-compose.yml")
		applyExampleProject(t, ctx, binary, root, baseURL, file)
		defer downExampleProject(t, ctx, binary, root, baseURL, file)
		command := `grep -q 'read-only bind fixture' /fixtures/readonly.txt && printf 'persistent\n' > /cache/value`
		run := runExampleCommand(t, ctx, binary, root, baseURL, file, "worker", command, true)
		exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "stop", run.SandboxID)
		exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "resume", run.SandboxID)
		out := exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "exec", run.SandboxID, "--", "cat", "/cache/value")
		if strings.TrimSpace(out) != "persistent" {
			t.Fatalf("persistent volume output = %q", out)
		}
		t.Logf("verified resumed volume output: %q", strings.TrimSpace(out))
		exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "exec", run.SandboxID, "--", "sh", "-c", "if touch /fixtures/unexpected 2>/dev/null; then exit 1; fi")
		exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "stop", run.SandboxID)
		exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "rm", run.SandboxID)
	})

	for _, tc := range []struct {
		name    string
		dir     string
		agent   string
		trigger string
		want    string
	}{
		{name: "script URL", dir: "docker-scheduler-script-url", agent: "reviewer", trigger: "source-loaded", want: "scheduler script URL ok"},
		{name: "script runtime", dir: "docker-scheduler-script-runtime", agent: "heartbeat", trigger: "follow-up", want: "heartbeat 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(examplesRoot, tc.dir, "agent-compose.yml")
			projectID := applyExampleProject(t, ctx, binary, root, baseURL, file)
			defer downExampleProject(t, ctx, binary, root, baseURL, file)
			exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "scheduler", "inspect", tc.agent, tc.trigger)
			waitForExampleSchedulerEvent(t, ctx, baseURL, projectID, tc.agent, tc.want)
		})
	}

	t.Run("cron control plane", func(t *testing.T) {
		file := filepath.Join(examplesRoot, "docker-scheduler-cron", "agent-compose.yml")
		applyExampleProject(t, ctx, binary, root, baseURL, file)
		defer downExampleProject(t, ctx, binary, root, baseURL, file)
		out := exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "scheduler", "ls", "reviewer")
		t.Logf("verified scheduler list:\n%s", strings.TrimSpace(out))
		if !strings.Contains(out, "hourly-review") || !strings.Contains(out, "cron") {
			t.Fatalf("cron scheduler list = %q", out)
		}
	})

	t.Run("build", func(t *testing.T) {
		file, imageRef := copyBuildExampleWithUniqueTag(t, examplesRoot, testRoot)
		defer func() {
			_ = exampleCLIErr(ctx, binary, root, baseURL, "--file", file, "rmi", imageRef, "--force")
		}()
		exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "build")
		applyExampleProject(t, ctx, binary, root, baseURL, file)
		defer downExampleProject(t, ctx, binary, root, baseURL, file)
		run := runExampleCommand(t, ctx, binary, root, baseURL, file, "worker", "cat /opt/agent-compose-example.txt", false)
		t.Logf("verified run: id=%s sandbox=%s status=%s output=%q", run.ID, run.SandboxID, run.Status, run.Output)
		if !strings.Contains(run.Output, "built-by-agent-compose") {
			t.Fatalf("build run = %#v", run)
		}
	})
}

func applyExampleProject(t *testing.T, ctx context.Context, binary, root, baseURL, file string) string {
	t.Helper()
	out := exampleCLI(t, ctx, binary, root, baseURL, "--file", file, "--json", "up")
	var applied struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal([]byte(out), &applied); err != nil || applied.Project.ID == "" {
		t.Fatalf("decode up output %q: %v", out, err)
	}
	return applied.Project.ID
}

func downExampleProject(t *testing.T, ctx context.Context, binary, root, baseURL, file string) {
	t.Helper()
	var ps struct {
		Sandboxes []struct {
			ID string `json:"sandbox_id"`
		} `json:"sandboxes"`
	}
	if out, err := exampleCLIOutput(ctx, binary, root, baseURL, "--file", file, "--json", "ps", "--all"); err == nil {
		if err := json.Unmarshal([]byte(out), &ps); err != nil {
			t.Errorf("decode ps output for %s: %v", file, err)
		} else {
			for _, sandbox := range ps.Sandboxes {
				if err := exampleCLIErr(ctx, binary, root, baseURL, "--file", file, "rm", sandbox.ID, "--force"); err != nil {
					t.Errorf("remove example sandbox %s: %v", sandbox.ID, err)
				}
			}
		}
	}
	if err := exampleCLIErr(ctx, binary, root, baseURL, "--file", file, "down"); err != nil {
		t.Errorf("down example %s: %v", file, err)
	}
}

func runExampleCommand(t *testing.T, ctx context.Context, binary, root, baseURL, file, agent, command string, keep bool) exampleRunOutput {
	t.Helper()
	args := []string{"--file", file, "--json", "run", agent, "--command", command}
	if keep {
		args = append(args, "--keep-running")
	}
	out := exampleCLI(t, ctx, binary, root, baseURL, args...)
	var run exampleRunOutput
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatalf("decode run output %q: %v", out, err)
	}
	if run.ID == "" || run.Status != "succeeded" {
		t.Fatalf("run did not succeed: %#v", run)
	}
	return run
}

func exampleCLI(t *testing.T, ctx context.Context, binary, root, baseURL string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"--host", baseURL}, args...)
	cmd := exec.CommandContext(ctx, binary, cmdArgs...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent-compose %s: %v\n%s", strings.Join(cmdArgs, " "), err, out)
	}
	return string(out)
}

func exampleCLIErr(ctx context.Context, binary, root, baseURL string, args ...string) error {
	_, err := exampleCLIOutput(ctx, binary, root, baseURL, args...)
	return err
}

func exampleCLIOutput(ctx context.Context, binary, root, baseURL string, args ...string) (string, error) {
	cmdArgs := append([]string{"--host", baseURL}, args...)
	cmd := exec.CommandContext(ctx, binary, cmdArgs...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("agent-compose %s: %w: %s", strings.Join(cmdArgs, " "), err, out)
	}
	return string(out), nil
}

func waitForExampleSchedulerEvent(t *testing.T, ctx context.Context, baseURL, projectID, agentName, want string) {
	t.Helper()
	client := agentcomposev2connect.NewProjectServiceClient(newE2EHTTPClient(), baseURL)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		resp, err := client.ListSchedulerEvents(ctx, connect.NewRequest(&agentcomposev2.ListSchedulerEventsRequest{
			Project:   &agentcomposev2.ProjectRef{ProjectId: projectID},
			AgentName: agentName,
			Limit:     100,
		}))
		if err == nil {
			for _, event := range resp.Msg.GetEvents() {
				if event.GetType() == "loader.run.failed" {
					t.Fatalf("scheduler run failed: %s", event.GetMessage())
				}
				if strings.Contains(event.GetMessage(), want) || strings.Contains(event.GetPayloadJson(), want) {
					t.Logf("verified scheduler event: type=%s message=%q payload=%s", event.GetType(), event.GetMessage(), event.GetPayloadJson())
					return
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("scheduler event did not contain %q", want)
}

func copyBuildExampleWithUniqueTag(t *testing.T, examplesRoot, testRoot string) (string, string) {
	t.Helper()
	source := filepath.Join(examplesRoot, "docker-build")
	destination := filepath.Join(testRoot, "docker-build")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatalf("create build example copy: %v", err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(source, "Dockerfile"))
	if err != nil {
		t.Fatalf("read build Dockerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, "Dockerfile"), dockerfile, 0o600); err != nil {
		t.Fatalf("write build Dockerfile: %v", err)
	}
	composeData, err := os.ReadFile(filepath.Join(source, "agent-compose.yml"))
	if err != nil {
		t.Fatalf("read build compose: %v", err)
	}
	imageRef := "agent-compose-example-e2e-" + fmt.Sprint(time.Now().UnixNano()) + ":local"
	content := strings.ReplaceAll(string(composeData), "agent-compose-example-build:local", imageRef)
	content = strings.ReplaceAll(content, "agent-compose-example-build:latest", imageRef)
	file := filepath.Join(destination, "agent-compose.yml")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("write build compose: %v", err)
	}
	return file, imageRef
}
