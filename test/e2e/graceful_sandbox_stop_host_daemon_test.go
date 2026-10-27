package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

const (
	gracefulStopE2EDriversEnv     = "AGENT_COMPOSE_E2E_GRACEFUL_STOP_DRIVERS"
	gracefulStopE2EImageEnv       = "AGENT_COMPOSE_E2E_GRACEFUL_STOP_IMAGE"
	gracefulStopE2ELegacyImageEnv = "AGENT_COMPOSE_E2E_GRACEFUL_STOP_LEGACY_IMAGE"
)

func TestE2EGracefulSandboxStopHostDaemon(t *testing.T) {
	drivers := gracefulStopE2EDrivers(t)
	image := strings.TrimSpace(os.Getenv(gracefulStopE2EImageEnv))
	if image == "" {
		t.Fatalf("%s is required when %s is set", gracefulStopE2EImageEnv, gracefulStopE2EDriversEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	repoRoot := e2eRepoRoot(t)
	testRoot, err := os.MkdirTemp(repoRoot, ".graceful-stop-e2e-")
	if err != nil {
		t.Fatalf("create runtime-visible test root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })

	binary := e2eDaemonBinary(t, ctx, repoRoot, testRoot)
	listenAddress := unusedLoopbackAddress(t)
	baseURL := "http://" + listenAddress
	daemon := startE2EDaemonWithEnv(t, binary, repoRoot, testRoot, listenAddress, image, map[string]string{
		"SANDBOX_START_TIMEOUT": "10m",
		// The VM drivers live in the sandbox-host, which the native test daemon
		// must spawn itself (managed) on a socket it can create under the test
		// root rather than the compose-only /run/agent-compose mount point.
		"AGENT_COMPOSE_SANDBOX_HOST_MODE":   "managed",
		"AGENT_COMPOSE_SANDBOX_HOST_SOCKET": filepath.Join(testRoot, "sandbox-host.sock"),
	})
	waitForE2EDaemon(t, ctx, daemon, baseURL)

	httpClient := newE2EHTTPClient()
	httpClient.Timeout = 5 * time.Minute
	clients := gracefulStopE2EClients{
		projects:  agentcomposev2connect.NewProjectServiceClient(httpClient, baseURL),
		runs:      agentcomposev2connect.NewRunServiceClient(httpClient, baseURL),
		execs:     agentcomposev2connect.NewExecServiceClient(httpClient, baseURL),
		sandboxes: agentcomposev2connect.NewSandboxServiceClient(httpClient, baseURL),
	}

	for _, driver := range drivers {
		driver := driver
		t.Run(driver+"/success", func(t *testing.T) {
			fixture := newGracefulStopE2ESandbox(t, ctx, clients, testRoot, image, driver, "success", daemon)
			result := fixture.startExec(ctx, gracefulStopSuccessScript)
			fixture.waitForWorkspaceFile(t, "ready")

			response, err := clients.sandboxes.StopSandbox(ctx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{
				SandboxId:   fixture.sandboxID,
				Mode:        agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL,
				GracePeriod: durationpb.New(15 * time.Second),
			}))
			if err != nil {
				t.Fatalf("graceful StopSandbox: %v\ndaemon log:\n%s", err, daemon.logs.String())
			}
			if got := response.Msg.GetOutcome(); got != agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_GRACEFUL {
				t.Fatalf("graceful StopSandbox outcome = %s, want GRACEFUL", got)
			}
			assertGracefulStopE2EStopped(t, response.Msg.GetSandbox())
			fixture.assertWorkspaceFile(t, "cleanup-complete", "cleanup-complete")

			execResult := waitForGracefulStopE2EExec(t, result)
			if execResult.err != nil {
				t.Fatalf("Exec RPC returned error: %v", execResult.err)
			}
			if execResult.result.GetSuccess() || execResult.result.GetExitCode() != 0 || !strings.Contains(execResult.result.GetOutput(), "cleanup-output") {
				t.Fatalf("gracefully cancelled Exec result: %s", e2eExecResultSummary(execResult.result))
			}
			fixture.assertCommandArtifacts(t, "partial-output", "cleanup-output")
		})
	}

	if slices.Contains(drivers, "docker") {
		t.Run("docker/timeout_escalates", func(t *testing.T) {
			fixture := newGracefulStopE2ESandbox(t, ctx, clients, testRoot, image, "docker", "timeout", daemon)
			result := fixture.startExec(ctx, gracefulStopIgnoreTermScript)
			fixture.waitForWorkspaceFile(t, "ready")

			response, err := clients.sandboxes.StopSandbox(ctx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{
				SandboxId:   fixture.sandboxID,
				Mode:        agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL,
				GracePeriod: durationpb.New(time.Second),
			}))
			if err != nil {
				t.Fatalf("timeout StopSandbox: %v\ndaemon log:\n%s", err, daemon.logs.String())
			}
			if got := response.Msg.GetOutcome(); got != agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE_AFTER_GRACE_TIMEOUT {
				t.Fatalf("timeout StopSandbox outcome = %s, want FORCE_AFTER_GRACE_TIMEOUT", got)
			}
			assertGracefulStopE2EStopped(t, response.Msg.GetSandbox())
			_ = waitForGracefulStopE2EExec(t, result)
		})

		if legacyImage := strings.TrimSpace(os.Getenv(gracefulStopE2ELegacyImageEnv)); legacyImage != "" {
			t.Run("docker/legacy_guest_escalates", func(t *testing.T) {
				fixture := newGracefulStopE2ESandbox(t, ctx, clients, testRoot, legacyImage, "docker", "legacy", daemon)
				result := fixture.startExec(ctx, gracefulStopSuccessScript)
				fixture.waitForWorkspaceFile(t, "ready")

				response, err := clients.sandboxes.StopSandbox(ctx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{
					SandboxId:   fixture.sandboxID,
					Mode:        agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_GRACEFUL,
					GracePeriod: durationpb.New(time.Second),
				}))
				if err != nil {
					t.Fatalf("legacy guest StopSandbox: %v\ndaemon log:\n%s", err, daemon.logs.String())
				}
				if got := response.Msg.GetOutcome(); got != agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE_AFTER_GRACE_TIMEOUT {
					t.Fatalf("legacy guest StopSandbox outcome = %s, want FORCE_AFTER_GRACE_TIMEOUT", got)
				}
				assertGracefulStopE2EStopped(t, response.Msg.GetSandbox())
				_ = waitForGracefulStopE2EExec(t, result)
			})
		}

		t.Run("docker/explicit_force", func(t *testing.T) {
			fixture := newGracefulStopE2ESandbox(t, ctx, clients, testRoot, image, "docker", "force", daemon)
			result := fixture.startExec(ctx, gracefulStopIgnoreTermScript)
			fixture.waitForWorkspaceFile(t, "ready")

			response, err := clients.sandboxes.StopSandbox(ctx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{
				SandboxId: fixture.sandboxID,
				Mode:      agentcomposev2.SandboxStopMode_SANDBOX_STOP_MODE_FORCE,
			}))
			if err != nil {
				t.Fatalf("force StopSandbox: %v\ndaemon log:\n%s", err, daemon.logs.String())
			}
			if got := response.Msg.GetOutcome(); got != agentcomposev2.SandboxStopOutcome_SANDBOX_STOP_OUTCOME_FORCE {
				t.Fatalf("force StopSandbox outcome = %s, want FORCE", got)
			}
			assertGracefulStopE2EStopped(t, response.Msg.GetSandbox())
			_ = waitForGracefulStopE2EExec(t, result)
		})
	}
}

const gracefulStopSuccessScript = `
set -u
trap 'sleep 1; printf %s cleanup-complete > cleanup-complete; printf %s\\n cleanup-output; exit 0' TERM
printf %s ready > ready
printf %s\\n partial-output
sleep 60
`

const gracefulStopIgnoreTermScript = `
set -u
trap '' TERM
printf %s ready > ready
printf %s\\n partial-output
while :; do sleep 1; done
`

type gracefulStopE2EClients struct {
	projects  agentcomposev2connect.ProjectServiceClient
	runs      agentcomposev2connect.RunServiceClient
	execs     agentcomposev2connect.ExecServiceClient
	sandboxes agentcomposev2connect.SandboxServiceClient
}

type gracefulStopE2EFixture struct {
	sandboxID string
	hostRoot  string
	clients   gracefulStopE2EClients
}

type gracefulStopE2EExecResult struct {
	result *agentcomposev2.ExecResult
	err    error
}

func gracefulStopE2EDrivers(t *testing.T) []string {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(gracefulStopE2EDriversEnv))
	if raw == "" {
		t.Skipf("set %s to docker, boxlite, microsandbox, or a comma-separated subset", gracefulStopE2EDriversEnv)
	}
	seen := map[string]bool{}
	var drivers []string
	for _, item := range strings.Split(raw, ",") {
		driver := strings.ToLower(strings.TrimSpace(item))
		switch driver {
		case "docker", "boxlite", "microsandbox":
		default:
			t.Fatalf("unsupported %s entry %q", gracefulStopE2EDriversEnv, item)
		}
		if !seen[driver] {
			seen[driver] = true
			drivers = append(drivers, driver)
		}
	}
	if len(drivers) == 0 {
		t.Fatalf("%s did not select a driver", gracefulStopE2EDriversEnv)
	}
	return drivers
}

func newGracefulStopE2ESandbox(
	t *testing.T,
	ctx context.Context,
	clients gracefulStopE2EClients,
	testRoot string,
	image string,
	driver string,
	scenario string,
	daemon *e2eDaemonProcess,
) gracefulStopE2EFixture {
	t.Helper()
	name := fmt.Sprintf("graceful-stop-%s-%s-%d", driver, scenario, time.Now().UnixNano())
	project, err := clients.projects.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: &agentcomposev2.ProjectSpec{
			Name: name,
			Agents: []*agentcomposev2.AgentSpec{{
				Name:     "worker",
				Provider: "codex",
				Image:    image,
				Driver:   gracefulStopE2EDriverSpec(driver),
			}},
		},
		Source: &agentcomposev2.ProjectSource{
			ComposePath: filepath.Join(testRoot, name+".yml"),
			ProjectDir:  testRoot,
		},
	}))
	if err != nil {
		t.Fatalf("ApplyProject for %s: %v", driver, err)
	}
	if !project.Msg.GetApplied() {
		t.Fatalf("ApplyProject for %s was rejected: %s", driver, formatE2EProjectIssues(project.Msg.GetIssues()))
	}
	projectID := project.Msg.GetProject().GetSummary().GetProjectId()
	run, err := clients.runs.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "worker",
		Command:         "printf sandbox-ready",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: name,
	}))
	if err != nil {
		t.Fatalf("RunAgent for %s: %v\ndaemon log:\n%s", driver, err, daemon.logs.String())
	}
	detail := run.Msg.GetRun()
	sandboxID := detail.GetSummary().GetSandboxId()
	if sandboxID == "" || detail.GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf(
			"RunAgent for %s returned sandbox_id=%q status=%s error=%q\ndaemon log:\n%s",
			driver,
			sandboxID,
			detail.GetSummary().GetStatus(),
			detail.GetSummary().GetError(),
			daemon.logs.String(),
		)
	}
	hostRoot := gracefulStopE2EHostRoot(t, testRoot, sandboxID)
	fixture := gracefulStopE2EFixture{sandboxID: sandboxID, hostRoot: hostRoot, clients: clients}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = clients.sandboxes.RemoveSandbox(cleanupCtx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxID, Force: true}))
	})
	return fixture
}

func gracefulStopE2EHostRoot(t *testing.T, testRoot string, sandboxID string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(testRoot, "sandboxes", "*", "*", "*", sandboxID))
	if err != nil || len(matches) != 1 {
		t.Fatalf("host sandbox roots for %s = %v, error = %v", sandboxID, matches, err)
	}
	return matches[0]
}

func gracefulStopE2EDriverSpec(driver string) *agentcomposev2.DriverSpec {
	spec := &agentcomposev2.DriverSpec{Name: driver}
	switch driver {
	case "boxlite":
		spec.Config = &agentcomposev2.DriverSpec_Boxlite{Boxlite: &agentcomposev2.BoxliteDriverSpec{}}
	case "microsandbox":
		spec.Config = &agentcomposev2.DriverSpec_Microsandbox{Microsandbox: &agentcomposev2.MicrosandboxDriverSpec{}}
	default:
		spec.Config = &agentcomposev2.DriverSpec_Docker{Docker: &agentcomposev2.DockerDriverSpec{}}
	}
	return spec
}

func (f gracefulStopE2EFixture) startExec(ctx context.Context, script string) <-chan gracefulStopE2EExecResult {
	result := make(chan gracefulStopE2EExecResult, 1)
	go func() {
		response, err := f.clients.execs.Exec(ctx, connect.NewRequest(&agentcomposev2.ExecRequest{
			Target: &agentcomposev2.ExecRequest_SandboxId{SandboxId: f.sandboxID},
			Command: &agentcomposev2.ExecCommand{
				Command: "sh",
				Args:    []string{"-c", script},
			},
			Cwd:            "/workspace",
			TimeoutMs:      uint32((5 * time.Minute).Milliseconds()),
			MaxOutputBytes: 64 << 10,
		}))
		if response == nil || response.Msg == nil {
			result <- gracefulStopE2EExecResult{err: err}
			return
		}
		result <- gracefulStopE2EExecResult{result: response.Msg.GetResult(), err: err}
	}()
	return result
}

func (f gracefulStopE2EFixture) waitForWorkspaceFile(t *testing.T, name string) {
	t.Helper()
	path := filepath.Join(f.hostRoot, "workspace", name)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("workspace file %s did not appear", name)
}

func (f gracefulStopE2EFixture) assertWorkspaceFile(t *testing.T, name string, want string) {
	t.Helper()
	if got := f.workspaceFile(t, name); got != want {
		t.Fatalf("workspace file %s = %q, want %q", name, got, want)
	}
}

func (f gracefulStopE2EFixture) workspaceFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.hostRoot, "workspace", name))
	if err != nil {
		t.Fatalf("read workspace file %s: %v", name, err)
	}
	return string(data)
}

func (f gracefulStopE2EFixture) assertCommandArtifacts(t *testing.T, fragments ...string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(f.hostRoot, "state", "exec", "*", "command-result.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("command result artifacts = %v, error = %v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("read command result artifact: %v", err)
	}
	var artifact struct {
		Output  string `json:"output"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatalf("decode command result artifact: %v", err)
	}
	if artifact.Success {
		t.Fatal("gracefully cancelled command artifact unexpectedly reports success")
	}
	for _, fragment := range fragments {
		if !strings.Contains(artifact.Output, fragment) {
			t.Fatalf("command result artifact output does not contain %q", fragment)
		}
	}
}

func waitForGracefulStopE2EExec(t *testing.T, result <-chan gracefulStopE2EExecResult) gracefulStopE2EExecResult {
	t.Helper()
	select {
	case completed := <-result:
		return completed
	case <-time.After(30 * time.Second):
		t.Fatal("Exec RPC did not finish after sandbox stop")
		return gracefulStopE2EExecResult{}
	}
}

func assertGracefulStopE2EStopped(t *testing.T, sandbox *agentcomposev2.Sandbox) {
	t.Helper()
	if sandbox == nil || sandbox.GetStatus() != agentcomposev2.SandboxStatus_SANDBOX_STATUS_STOPPED {
		t.Fatalf("StopSandbox status = %v, want STOPPED", sandbox.GetStatus())
	}
}
