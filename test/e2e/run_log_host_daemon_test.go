package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/runlog"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// This matrix exercises real drivers and the checked-out guest runtime. The
// provider subprocess alone is a fixture; no credentials or model are needed.
func TestE2ERunLogHostDaemon(t *testing.T) {
	drivers := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_RUN_LOG_DRIVERS"))
	if drivers == "" {
		t.Skip("set AGENT_COMPOSE_E2E_RUN_LOG_DRIVERS to opt into real driver tests")
	}
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_RUN_LOG_IMAGE"))
	if image == "" {
		t.Fatal("AGENT_COMPOSE_E2E_RUN_LOG_IMAGE must name a guest built from this checkout")
	}
	for _, driver := range strings.Split(drivers, ",") {
		driver = strings.TrimSpace(driver)
		switch driver {
		case "docker", "boxlite", "microsandbox", "k8s":
		default:
			t.Fatalf("unknown driver %q", driver)
		}
		t.Run(driver, func(t *testing.T) {
			for _, scenario := range []string{"normal", "restart", "interactive-restart"} {
				t.Run(scenario, func(t *testing.T) {
					if driver == "k8s" && strings.HasPrefix(scenario, "interactive-") {
						t.Skip("k8s currently rejects interactive command transport (native_exec/wrapper stream unsupported)")
					}
					fixture := newRunLogE2E(t, driver, image)
					fixture.exerciseAgent(t, scenario)
				})
			}
			t.Run("scheduler", func(t *testing.T) {
				fixture := newRunLogE2E(t, driver, image)
				fixture.exerciseScheduler(t)
			})
		})
	}
}

type runLogE2E struct {
	ctx                                  context.Context
	repo, root, binary, address, baseURL string
	image, driver, projectID, sandboxID  string
	daemon                               *e2eDaemonProcess
	env                                  map[string]string
	runs                                 agentcomposev2connect.RunServiceClient
	projects                             agentcomposev2connect.ProjectServiceClient
	sandboxes                            agentcomposev2connect.SandboxServiceClient
	execs                                agentcomposev2connect.ExecServiceClient
}

func newRunLogE2E(t *testing.T, driver, image string) *runLogE2E {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)
	// Short, host-visible paths also work for VM runtime sockets and mounts.
	root, err := os.MkdirTemp("", "acrl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove run-log fixture: %v", err)
		}
	})
	f := &runLogE2E{ctx: ctx, repo: e2eRepoRoot(t), root: root, driver: driver, image: image}
	f.binary = e2eDaemonBinary(t, ctx, f.repo, root)
	f.address = unusedLoopbackAddress(t)
	f.baseURL = "http://" + f.address
	f.env = map[string]string{
		"RUNTIME_DRIVER": driver, "SANDBOX_START_TIMEOUT": "10m",
		"AGENT_COMPOSE_SANDBOX_HOST_MODE":   "managed",
		"AGENT_COMPOSE_SANDBOX_HOST_SOCKET": filepath.Join(root, "sandbox-host.sock"),
		"MICROSANDBOX_DEFAULT_IMAGE":        image, "K8S_DEFAULT_IMAGE": image,
	}
	if cache := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_RUN_LOG_CACHE")); cache != "" {
		f.env["IMAGE_CACHE_ROOT"] = cache
	}
	if kubeconfig := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_RUN_LOG_KUBECONFIG")); kubeconfig != "" {
		f.env["K8S_KUBECONFIG"] = kubeconfig
	}
	if namespace := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_RUN_LOG_NAMESPACE")); namespace != "" {
		f.env["K8S_NAMESPACE"] = namespace
	}
	if driver == "boxlite" || driver == "microsandbox" {
		f.startSandboxHost(t)
	}
	f.restart(t)
	client := newE2EHTTPClient()
	client.Timeout = 10 * time.Minute
	t.Cleanup(client.CloseIdleConnections)
	f.runs = agentcomposev2connect.NewRunServiceClient(client, f.baseURL)
	f.projects = agentcomposev2connect.NewProjectServiceClient(client, f.baseURL)
	f.sandboxes = agentcomposev2connect.NewSandboxServiceClient(client, f.baseURL)
	f.execs = agentcomposev2connect.NewExecServiceClient(client, f.baseURL)
	f.apply(t)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon log:\n%s", f.daemon.logs.String())
		}
	})
	return f
}

func (f *runLogE2E) restart(t *testing.T) {
	t.Helper()
	f.daemon = startE2EDaemonWithEnv(t, f.binary, f.repo, filepath.Join(f.root, "data"), f.address, f.image, f.env)
	waitForE2EDaemon(t, f.ctx, f.daemon, f.baseURL)
	// Register after this daemon's stop hook, including after every restart.
	daemon := f.daemon
	t.Cleanup(func() {
		if f.daemon != daemon {
			return
		}
		f.removeSandboxes(t)
	})
}

func (f *runLogE2E) apply(t *testing.T) {
	t.Helper()
	project := filepath.Join(f.root, "project")
	workspace := filepath.Join(project, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join(f.repo, "test/e2e/testdata/runlog-codex.cjs"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "runlog-codex.cjs"), fixture, 0o755); err != nil {
		t.Fatal(err)
	}
	writeE2EHostFile(t, filepath.Join(project, "workflow.js"), runLogE2ESchedulerScript)
	policy := "retain"
	if f.driver == "k8s" {
		policy = "remove"
	}
	yaml := fmt.Sprintf(`name: run-log-e2e
workspaces:
  default:
    provider: file
    path: ./workspace
agents:
  worker:
    provider: codex
    image: %s
    driver:
      %s: {}
    workspace:
      name: default
    env:
      CODEX_BIN: /workspace/runlog-codex.cjs
    sandbox:
      stopped_runtime_policy: %s
    scheduler:
      script:
        provider: file
        path: ./workflow.js
`, f.image, f.driver, policy)
	file := filepath.Join(project, "agent-compose.yml")
	writeE2EHostFile(t, file, yaml)
	command := exec.CommandContext(f.ctx, f.binary, "up", "--host", f.baseURL, "--file", file)
	command.Env = overrideE2EEnv(os.Environ(), map[string]string{"AGENT_COMPOSE_AUTH_TOKEN": "", "AUTH_PASSWORD": "", "AUTH_USERNAME": ""})
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply run-log project: %v\n%s", err, output)
	}
	response, err := f.projects.GetProject(f.ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_Name{Name: "run-log-e2e"}}}))
	if err != nil {
		t.Fatal(err)
	}
	f.projectID = response.Msg.GetProject().GetSummary().GetProjectId()
}

func (f *runLogE2E) exerciseAgent(t *testing.T, scenario string) {
	t.Helper()
	interactive := strings.HasPrefix(scenario, "interactive-")
	mode := "gated"
	if scenario == "normal" {
		mode = "immediate"
	}
	response, err := f.runs.StartAgentRun(f.ctx, connect.NewRequest(&agentcomposev2.StartAgentRunRequest{
		Interactive: interactive,
		Run: &agentcomposev2.RunAgentRequest{ProjectId: f.projectID, AgentName: "worker", Prompt: "runlog:" + scenario + ":" + mode,
			Source: agentcomposev2.RunSource_RUN_SOURCE_API, CleanupPolicy: agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING},
	}))
	if err != nil {
		t.Fatal(err)
	}
	runID := response.Msg.GetRun().GetRunId()
	if runID == "" {
		t.Fatal("StartAgentRun omitted run identity")
	}
	logPath := filepath.Join(f.root, "data", "runs", runID, "run.log.jsonl")
	if scenario != "normal" {
		f.wait(t, "committed tool call", func() bool {
			run := f.get(t, runID)
			f.sandboxID = run.GetSummary().GetSandboxId()
			if status := run.GetSummary().GetStatus(); status == agentcomposev2.RunStatus_RUN_STATUS_FAILED || status == agentcomposev2.RunStatus_RUN_STATUS_CANCELED {
				t.Fatalf("run failed before its first commit: %s\ndaemon log:\n%s", run, f.daemon.logs.String())
			}
			entries, err := runlog.ReadEntries(logPath, 0)
			return err == nil && runlog.Summarize(entries).ToolCalls == 1
		})
		writeE2EWorkspaceFile(t, f.ctx, f.execs, f.sandboxID, scenario+".phase1", "release")
		// The answer is mid-paragraph. Whether the daemon still holds it for
		// coalescing or already committed it after the pause, the hard restart
		// must end with it once.
		f.wait(t, "answer held mid-paragraph", func() bool {
			return e2eWorkspaceFileExists(t, f.ctx, f.execs, f.sandboxID, scenario+".middle-emitted")
		})
		oldDaemon := f.daemon
		oldDaemon.hardKill(t)
		f.restart(t)
		t.Logf("hard restart completed: driver=%s run=%s interactive=%t", f.driver, runID, interactive)
		writeE2EWorkspaceFile(t, f.ctx, f.execs, f.sandboxID, scenario+".finish", "release")
	}
	expected := []string{runLogE2EAnswer(scenario)}
	if interactive {
		f.finishInteractive(t, runID, &expected)
	}
	f.wait(t, "terminal run", func() bool {
		status := f.get(t, runID).GetSummary().GetStatus()
		return status == agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || status == agentcomposev2.RunStatus_RUN_STATUS_FAILED || status == agentcomposev2.RunStatus_RUN_STATUS_CANCELED
	})
	run := f.get(t, runID)
	f.sandboxID = run.GetSummary().GetSandboxId()
	if run.GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || run.GetSummary().GetLogIncomplete() {
		t.Fatalf("run outcome: %s", run)
	}
	f.assertMessages(t, runID, expected, false)
	f.assertLog(t, runID, expected)
	f.assertNoAgentCells(t)
	// History belongs to the run and must survive removal of its sandbox.
	if _, err := f.sandboxes.RemoveSandbox(f.ctx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: f.sandboxID, Force: true})); err != nil {
		t.Fatal(err)
	}
	f.sandboxID = ""
	f.assertMessages(t, runID, expected, false)
	f.assertLog(t, runID, expected)
	t.Logf("verified outcome, messages, log health, storage and removal: driver=%s scenario=%s", f.driver, scenario)
}

func runLogE2EAnswer(token string) string { return "runlog-answer-" + token + ":before:middle:after" }

func (f *runLogE2E) get(t *testing.T, runID string) *agentcomposev2.RunDetail {
	t.Helper()
	response, err := f.runs.GetRun(f.ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{RunId: runID}))
	if err != nil {
		t.Fatal(err)
	}
	return response.Msg.GetRun()
}

func (f *runLogE2E) wait(t *testing.T, description string, ready func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Minute)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s: %v\ndaemon log:\n%s", description, ctx.Err(), f.daemon.logs.String())
		case <-ticker.C:
		}
	}
}

func e2eWorkspaceFileExists(t *testing.T, ctx context.Context, client agentcomposev2connect.ExecServiceClient, sandboxID, name string) bool {
	t.Helper()
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "test", "-f", cleanE2EWorkspaceRelativePath(t, name))
	return result.GetSuccess() && result.GetExitCode() == 0
}
