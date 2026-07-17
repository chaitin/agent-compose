package e2e

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

const dockerWorkspaceE2EImageEnv = "AGENT_COMPOSE_E2E_DOCKER_WORKSPACE_IMAGE"

func TestE2EDockerFileWorkspaceResumePreservesState(t *testing.T) {
	image := strings.TrimSpace(os.Getenv(dockerWorkspaceE2EImageEnv))
	if image == "" {
		t.Skipf("set %s to a local Docker guest image", dockerWorkspaceE2EImageEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	repoRoot := e2eRepoRoot(t)
	testRoot, err := os.MkdirTemp(repoRoot, ".docker-workspace-e2e-")
	if err != nil {
		t.Fatalf("create Docker-visible E2E root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	dockerClient := newE2EDockerClient(t, ctx, image)
	binary := e2eDaemonBinary(t, ctx, repoRoot, testRoot)
	socketPath := filepath.Join(testRoot, "agent-compose.sock")

	const (
		modifiedPath   = "modified.txt"
		deletedPath    = "deleted.txt"
		generatedPath  = "generated.txt"
		sourceV1       = "source-version-one"
		sourceV2       = "source-version-two"
		deletedValue   = "source-template-delete-me"
		agentValue     = "sandbox-agent-version"
		generatedValue = "sandbox-generated-artifact"
	)
	sourceRoot := filepath.Join(testRoot, "workspace-source")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("create workspace source: %v", err)
	}
	writeE2EHostFile(t, filepath.Join(sourceRoot, modifiedPath), sourceV1)
	writeE2EHostFile(t, filepath.Join(sourceRoot, deletedPath), deletedValue)

	listenAddress1 := unusedLoopbackAddress(t)
	baseURL1 := "http://" + listenAddress1
	daemon1 := startE2EDaemon(t, binary, repoRoot, testRoot, listenAddress1, image)
	waitForE2EDaemon(t, ctx, daemon1, baseURL1)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("first agent-compose daemon log:\n%s", daemon1.logs.String())
		}
	})

	httpClient1 := newE2EHTTPClient()
	projectClient := agentcomposev2connect.NewProjectServiceClient(httpClient1, baseURL1)
	runClient := agentcomposev2connect.NewRunServiceClient(httpClient1, baseURL1)
	execClient := newE2EExecClient(httpClient1, baseURL1)
	sandboxClient := agentcomposev2connect.NewSandboxServiceClient(httpClient1, baseURL1)

	projectResp, err := projectClient.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: &agentcomposev2.ProjectSpec{
			Name: "docker-workspace-resume-e2e",
			Workspaces: []*agentcomposev2.NamedWorkspaceSpec{{
				Name: "source",
				Workspace: &agentcomposev2.WorkspaceSpec{
					Provider: "file",
					Path:     ".",
				},
			}},
			Agents: []*agentcomposev2.AgentSpec{{
				Name:      "worker",
				Provider:  "codex",
				Image:     image,
				Driver:    &agentcomposev2.DriverSpec{Name: "docker", Docker: &agentcomposev2.DockerDriverSpec{}},
				Workspace: &agentcomposev2.WorkspaceSpec{Name: "source"},
			}},
		},
		Source: &agentcomposev2.ProjectSource{
			ComposePath: filepath.Join(sourceRoot, "agent-compose.yml"),
			ProjectDir:  sourceRoot,
		},
	}))
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	if !projectResp.Msg.GetApplied() || projectResp.Msg.GetProject().GetSummary().GetProjectId() == "" {
		t.Fatalf("ApplyProject did not apply workspace project: %#v", projectResp.Msg)
	}
	projectID := projectResp.Msg.GetProject().GetSummary().GetProjectId()

	sandboxA := runE2EWorkspaceSandbox(t, ctx, runClient, sandboxClient, projectID, "workspace-resume-a")
	sandboxAID := sandboxA.GetSandboxId()
	sandboxARemoved := false
	t.Cleanup(func() {
		cleanupE2EWorkspaceSandbox(t, dockerClient, sandboxClient, sandboxAID, sandboxARemoved)
	})
	assertE2ESandboxWorkspaceState(t, sandboxA, sandboxAID, projectID, domain.VMStatusRunning, "")
	workspaceAPath := sandboxA.GetWorkspacePath()
	handleA := inspectE2EDockerSandbox(t, ctx, dockerClient, sandboxAID)
	if !handleA.Running || filepath.Clean(handleA.WorkspaceSource) != filepath.Clean(workspaceAPath) {
		t.Fatalf("Docker sandbox A handle = %+v, want running with workspace source %q", handleA, workspaceAPath)
	}

	writeE2EWorkspaceFile(t, ctx, execClient, sandboxAID, modifiedPath, agentValue)
	removeE2EWorkspaceFile(t, ctx, execClient, sandboxAID, deletedPath)
	writeE2EWorkspaceFile(t, ctx, execClient, sandboxAID, generatedPath, generatedValue)
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxAID, modifiedPath, agentValue)
	assertE2EWorkspaceFileAbsent(t, ctx, execClient, sandboxAID, deletedPath)
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxAID, generatedPath, generatedValue)
	assertE2EHostFile(t, filepath.Join(sourceRoot, modifiedPath), sourceV1)
	assertE2EHostFile(t, filepath.Join(sourceRoot, deletedPath), deletedValue)

	stopResp, err := sandboxClient.StopSandbox(ctx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{SandboxId: sandboxAID}))
	if err != nil {
		t.Fatalf("StopSandbox A returned error: %v", err)
	}
	assertE2ESandboxWorkspaceState(t, stopResp.Msg.GetSandbox(), sandboxAID, projectID, domain.VMStatusStopped, workspaceAPath)
	stoppedHandleA := inspectE2EDockerSandbox(t, ctx, dockerClient, sandboxAID)
	if stoppedHandleA.ContainerID != handleA.ContainerID || stoppedHandleA.Running {
		t.Fatalf("stopped Docker sandbox A handle = %+v, want stopped container %s", stoppedHandleA, handleA.ContainerID)
	}

	writeE2EHostFile(t, filepath.Join(sourceRoot, modifiedPath), sourceV2)
	listenAddress2 := unusedLoopbackAddress(t)
	baseURL2 := "http://" + listenAddress2
	httpClient1.CloseIdleConnections()
	daemon1.stop(t)
	assertE2EDaemonReleased(t, daemon1, socketPath, listenAddress1)
	if persistedHandleA := inspectE2EDockerSandbox(t, ctx, dockerClient, sandboxAID); persistedHandleA.ContainerID != handleA.ContainerID || persistedHandleA.Running {
		t.Fatalf("Docker sandbox A after daemon restart boundary = %+v, want stopped container %s", persistedHandleA, handleA.ContainerID)
	}

	daemon2 := startE2EDaemon(t, binary, repoRoot, testRoot, listenAddress2, image)
	waitForE2EDaemon(t, ctx, daemon2, baseURL2)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("second agent-compose daemon log:\n%s", daemon2.logs.String())
		}
	})

	httpClient2 := newE2EHTTPClient()
	projectClient = agentcomposev2connect.NewProjectServiceClient(httpClient2, baseURL2)
	runClient = agentcomposev2connect.NewRunServiceClient(httpClient2, baseURL2)
	execClient = newE2EExecClient(httpClient2, baseURL2)
	sandboxClient = agentcomposev2connect.NewSandboxServiceClient(httpClient2, baseURL2)

	getProjectResp, err := projectClient.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project:     &agentcomposev2.ProjectRef{ProjectId: projectID},
		IncludeSpec: true,
	}))
	if err != nil || getProjectResp.Msg.GetProject().GetSummary().GetProjectId() != projectID {
		t.Fatalf("GetProject after daemon restart = %#v, error %v", getProjectResp, err)
	}
	getAResp, err := sandboxClient.GetSandbox(ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxAID}))
	if err != nil {
		t.Fatalf("GetSandbox A after daemon restart returned error: %v", err)
	}
	assertE2ESandboxWorkspaceState(t, getAResp.Msg.GetSandbox(), sandboxAID, projectID, domain.VMStatusStopped, workspaceAPath)

	resumeAResp, err := sandboxClient.ResumeSandbox(ctx, connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: sandboxAID}))
	if err != nil {
		t.Fatalf("ResumeSandbox A after daemon restart returned error: %v", err)
	}
	assertE2ESandboxWorkspaceState(t, resumeAResp.Msg.GetSandbox(), sandboxAID, projectID, domain.VMStatusRunning, workspaceAPath)
	resumedHandleA := inspectE2EDockerSandbox(t, ctx, dockerClient, sandboxAID)
	if resumedHandleA.ContainerID != handleA.ContainerID || !resumedHandleA.Running || filepath.Clean(resumedHandleA.WorkspaceSource) != filepath.Clean(workspaceAPath) {
		t.Fatalf("resumed Docker sandbox A handle = %+v, want original running handle %+v", resumedHandleA, handleA)
	}
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxAID, modifiedPath, agentValue)
	assertE2EWorkspaceFileAbsent(t, ctx, execClient, sandboxAID, deletedPath)
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxAID, generatedPath, generatedValue)

	sandboxB := runE2EWorkspaceSandbox(t, ctx, runClient, sandboxClient, projectID, "workspace-resume-b")
	sandboxBID := sandboxB.GetSandboxId()
	sandboxBRemoved := false
	t.Cleanup(func() {
		cleanupE2EWorkspaceSandbox(t, dockerClient, sandboxClient, sandboxBID, sandboxBRemoved)
	})
	assertE2ESandboxWorkspaceState(t, sandboxB, sandboxBID, projectID, domain.VMStatusRunning, "")
	if sandboxBID == sandboxAID || sandboxB.GetWorkspacePath() == workspaceAPath {
		t.Fatalf("sandbox B identity/path = %q/%q, must differ from A %q/%q", sandboxBID, sandboxB.GetWorkspacePath(), sandboxAID, workspaceAPath)
	}
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxBID, modifiedPath, sourceV2)
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxBID, deletedPath, deletedValue)
	assertE2EWorkspaceFileAbsent(t, ctx, execClient, sandboxBID, generatedPath)
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxAID, modifiedPath, agentValue)
	assertE2EHostFile(t, filepath.Join(sourceRoot, modifiedPath), sourceV2)

	removeE2ESandboxPublic(t, ctx, sandboxClient, sandboxBID)
	sandboxBRemoved = true
	removeE2EDockerSandboxFallback(t, ctx, dockerClient, sandboxBID)
	assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, sandboxBID, 0)
	removeE2ESandboxPublic(t, ctx, sandboxClient, sandboxAID)
	sandboxARemoved = true
	removeE2EDockerSandboxFallback(t, ctx, dockerClient, sandboxAID)
	assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, sandboxAID, 0)

	if _, err := projectClient.RemoveProject(ctx, connect.NewRequest(&agentcomposev2.RemoveProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID},
	})); err != nil {
		t.Fatalf("RemoveProject returned error: %v", err)
	}
	httpClient2.CloseIdleConnections()
	daemon2.stop(t)
	assertE2EDaemonReleased(t, daemon2, socketPath, listenAddress2)
	assertE2ETCPAddressReleased(t, listenAddress1)
}

func runE2EWorkspaceSandbox(
	t *testing.T,
	ctx context.Context,
	runClient agentcomposev2connect.RunServiceClient,
	sandboxClient agentcomposev2connect.SandboxServiceClient,
	projectID string,
	requestID string,
) *agentcomposev2.Sandbox {
	t.Helper()
	runResp, err := runClient.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "worker",
		Command:         "true",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: requestID,
	}))
	if err != nil {
		t.Fatalf("RunAgent %s returned error: %v", requestID, err)
	}
	run := runResp.Msg.GetRun()
	if run.GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || run.GetSummary().GetSandboxId() == "" {
		t.Fatalf("RunAgent %s result = %#v, want succeeded run with sandbox", requestID, run)
	}
	sandboxResp, err := sandboxClient.GetSandbox(ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: run.GetSummary().GetSandboxId()}))
	if err != nil {
		t.Fatalf("GetSandbox for %s returned error: %v", requestID, err)
	}
	return sandboxResp.Msg.GetSandbox()
}

func writeE2EHostFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write host workspace file %s: %v", path, err)
	}
}

func assertE2EHostFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("host workspace file %s = %q, error %v; want %q", path, data, err, want)
	}
}

func cleanupE2EWorkspaceSandbox(t *testing.T, dockerClient *client.Client, sandboxClient agentcomposev2connect.SandboxServiceClient, sandboxID string, removed bool) {
	t.Helper()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !removed {
		_, _ = sandboxClient.RemoveSandbox(cleanupCtx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxID, Force: true}))
	}
	removeE2EDockerSandboxFallback(t, cleanupCtx, dockerClient, sandboxID)
}

type e2eDockerSandboxHandle struct {
	ContainerID     string
	Running         bool
	WorkspaceSource string
}

func inspectE2EDockerSandbox(t *testing.T, ctx context.Context, dockerClient *client.Client, sandboxID string) e2eDockerSandboxHandle {
	t.Helper()
	args := filters.NewArgs(
		filters.Arg("label", "agent-compose.sandbox_id="+sandboxID),
		filters.Arg("label", "agent-compose.driver=docker"),
	)
	containers, err := dockerClient.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: args})
	if err != nil {
		t.Fatalf("list Docker sandbox %s containers: %v", sandboxID, err)
	}
	if len(containers) != 1 {
		t.Fatalf("Docker sandbox %s container count = %d, want 1", sandboxID, len(containers))
	}
	containerInfo, err := dockerClient.ContainerInspect(ctx, containers[0].ID)
	if err != nil {
		t.Fatalf("inspect Docker sandbox %s: %v", sandboxID, err)
	}
	workspaceSource := ""
	for _, mount := range containerInfo.Mounts {
		if filepath.Clean(mount.Destination) == e2eGuestWorkspacePath {
			workspaceSource = mount.Source
			break
		}
	}
	if containerInfo.Config == nil || containerInfo.State == nil || workspaceSource == "" {
		t.Fatalf("Docker sandbox %s returned incomplete inspect data", sandboxID)
	}
	return e2eDockerSandboxHandle{ContainerID: containerInfo.ID, Running: containerInfo.State.Running, WorkspaceSource: workspaceSource}
}

func assertE2EDockerSandboxContainerCount(t *testing.T, ctx context.Context, dockerClient *client.Client, sandboxID string, want int) {
	t.Helper()
	args := filters.NewArgs(filters.Arg("label", "agent-compose.sandbox_id="+sandboxID), filters.Arg("label", "agent-compose.driver=docker"))
	containers, err := dockerClient.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: args})
	if err != nil {
		t.Fatalf("list Docker sandbox %s containers: %v", sandboxID, err)
	}
	if len(containers) != want {
		t.Fatalf("Docker sandbox %s container count = %d, want %d", sandboxID, len(containers), want)
	}
}

func assertE2ESandboxWorkspaceState(t *testing.T, sandbox *agentcomposev2.Sandbox, sandboxID, projectID, status, wantWorkspacePath string) {
	t.Helper()
	if sandbox == nil || sandboxID == "" || sandbox.GetSandboxId() != sandboxID || sandbox.GetProjectId() != projectID || sandbox.GetDriver() != "docker" || sandbox.GetStatus() != status || sandbox.GetImage() == "" {
		t.Fatalf("sandbox %s = %#v, want docker/%s with stable project identity", sandboxID, sandbox, status)
	}
	if sandbox.GetWorkspacePath() == "" || (wantWorkspacePath != "" && sandbox.GetWorkspacePath() != wantWorkspacePath) {
		t.Fatalf("sandbox %s workspace path = %q, want %q", sandboxID, sandbox.GetWorkspacePath(), wantWorkspacePath)
	}
}

func removeE2ESandboxPublic(t *testing.T, ctx context.Context, sandboxClient agentcomposev2connect.SandboxServiceClient, sandboxID string) {
	t.Helper()
	response, err := sandboxClient.RemoveSandbox(ctx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxID, Force: true}))
	if err != nil {
		t.Fatalf("RemoveSandbox %s returned error: %v", sandboxID, err)
	}
	if response.Msg.GetSandboxId() != sandboxID || !response.Msg.GetRemoved() || !response.Msg.GetStopped() {
		t.Fatalf("RemoveSandbox %s response = %#v, want removed and stopped", sandboxID, response.Msg)
	}
}

func assertE2EDaemonReleased(t *testing.T, daemon *e2eDaemonProcess, socketPath, listenAddress string) {
	t.Helper()
	if daemon == nil || daemon.cmd == nil || daemon.cmd.ProcessState == nil || !daemon.cmd.ProcessState.Exited() || daemon.cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("agent-compose daemon did not exit cleanly: process_state=%v", daemon.cmd.ProcessState)
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("agent-compose Unix socket %q remains after shutdown: %v", socketPath, err)
	}
	assertE2ETCPAddressReleased(t, listenAddress)
}

func assertE2ETCPAddressReleased(t *testing.T, listenAddress string) {
	t.Helper()
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		t.Fatalf("daemon TCP address %s remains in use: %v", listenAddress, err)
	}
	_ = listener.Close()
}
