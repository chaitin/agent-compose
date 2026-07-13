package e2e

import (
	"bytes"
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
	"google.golang.org/protobuf/types/known/emptypb"

	domain "agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
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
	testRoot, err := os.MkdirTemp("", "agent-compose-docker-workspace-e2e-")
	if err != nil {
		t.Fatalf("create E2E temp root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(testRoot); err != nil {
			t.Logf("remove E2E temp root: %v", err)
		}
	})
	dockerClient := newE2EDockerClient(t, ctx, image)
	binary := e2eDaemonBinary(t, ctx, repoRoot, testRoot)
	socketPath := filepath.Join(testRoot, "agent-compose.sock")

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
	configClient := agentcomposev1connect.NewConfigServiceClient(httpClient1, baseURL1)
	sessionClient := agentcomposev1connect.NewSessionServiceClient(httpClient1, baseURL1)
	execClient := newE2EExecClient(httpClient1, baseURL1)
	sandboxClient := agentcomposev2connect.NewSandboxServiceClient(httpClient1, baseURL1)

	workspaceResp, err := configClient.CreateWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.CreateWorkspaceConfigRequest{
		Name:    "docker-workspace-resume-e2e",
		Type:    "file",
		Comment: "one-time source seed for Docker restart/resume E2E",
	}))
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	workspace := workspaceResp.Msg.GetWorkspace()
	workspaceID := workspace.GetId()
	workspaceRemoved := false
	t.Cleanup(func() {
		if workspaceRemoved {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := configClient.DeleteWorkspaceConfig(cleanupCtx, connect.NewRequest(&agentcomposev1.WorkspaceConfigIDRequest{WorkspaceId: workspaceID})); err != nil {
			t.Logf("public workspace config cleanup failed for %s: %v", workspaceID, err)
		}
	})
	if workspaceID == "" || workspace.GetType() != "file" || workspace.GetConfigJson() == "" {
		t.Fatalf("CreateWorkspaceConfig returned invalid file workspace: %#v", workspace)
	}

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
	sourceV1Files := []e2eWorkspaceUploadFile{
		{Path: modifiedPath, Content: []byte(sourceV1), Mode: 0o644},
		{Path: deletedPath, Content: []byte(deletedValue), Mode: 0o640},
	}
	sourceV1Entries := []e2eWorkspaceFileExpectation{
		{Path: modifiedPath, Size: int64(len(sourceV1))},
		{Path: deletedPath, Size: int64(len(deletedValue))},
	}
	assertE2EWorkspaceFiles(t, uploadE2EWorkspaceFiles(t, ctx, httpClient1, baseURL1, workspaceID, sourceV1Files), workspaceID, sourceV1Entries)

	createAResp, err := sessionClient.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:       "docker workspace resume A",
		WorkspaceId: workspaceID,
		GuestImage:  image,
		Driver:      "docker",
	}))
	if err != nil {
		t.Fatalf("CreateSession A returned error: %v", err)
	}
	sessionA := createAResp.Msg.GetSession()
	sandboxAID := sessionA.GetSummary().GetSessionId()
	sandboxARemoved := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if !sandboxARemoved {
			if _, err := sandboxClient.RemoveSandbox(cleanupCtx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxAID, Force: true})); err != nil {
				t.Logf("public sandbox A cleanup failed for %s: %v", sandboxAID, err)
			}
		}
		fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer fallbackCancel()
		removeE2EDockerSandboxFallback(t, fallbackCtx, dockerClient, sandboxAID)
	})
	assertE2ESessionWorkspaceState(t, sessionA, sandboxAID, workspaceID, domain.VMStatusRunning, "")
	workspaceAPath := sessionA.GetSummary().GetWorkspacePath()
	workspaceASnapshot := cloneE2EWorkspaceSnapshot(sessionA.GetWorkspace())
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

	// Sandbox mutations must not flow back into the workspace source.
	assertE2EWorkspaceFiles(t, listE2EWorkspaceFiles(t, ctx, httpClient1, baseURL1, workspaceID), workspaceID, sourceV1Entries)
	assertE2EWorkspaceDownload(t, downloadE2EWorkspaceFile(t, ctx, httpClient1, baseURL1, workspaceID, modifiedPath), modifiedPath, sourceV1)
	assertE2EWorkspaceDownload(t, downloadE2EWorkspaceFile(t, ctx, httpClient1, baseURL1, workspaceID, deletedPath), deletedPath, deletedValue)

	stopResp, err := sessionClient.StopSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sandboxAID}))
	if err != nil {
		t.Fatalf("StopSession A returned error: %v", err)
	}
	assertE2ESessionWorkspaceState(t, stopResp.Msg.GetSession(), sandboxAID, workspaceID, domain.VMStatusStopped, workspaceAPath)
	stoppedHandleA := inspectE2EDockerSandbox(t, ctx, dockerClient, sandboxAID)
	if stoppedHandleA.ContainerID != handleA.ContainerID || stoppedHandleA.Running {
		t.Fatalf("stopped Docker sandbox A handle = %+v, want stopped container %s", stoppedHandleA, handleA.ContainerID)
	}

	sourceV2Entries := []e2eWorkspaceFileExpectation{
		{Path: modifiedPath, Size: int64(len(sourceV2))},
		{Path: deletedPath, Size: int64(len(deletedValue))},
	}
	assertE2EWorkspaceFiles(t, uploadE2EWorkspaceFiles(t, ctx, httpClient1, baseURL1, workspaceID, []e2eWorkspaceUploadFile{{
		Path: modifiedPath, Content: []byte(sourceV2), Mode: 0o644,
	}}), workspaceID, sourceV2Entries)
	assertE2EWorkspaceFiles(t, listE2EWorkspaceFiles(t, ctx, httpClient1, baseURL1, workspaceID), workspaceID, sourceV2Entries)
	assertE2EWorkspaceDownload(t, downloadE2EWorkspaceFile(t, ctx, httpClient1, baseURL1, workspaceID, modifiedPath), modifiedPath, sourceV2)
	assertE2EWorkspaceDownload(t, downloadE2EWorkspaceFile(t, ctx, httpClient1, baseURL1, workspaceID, deletedPath), deletedPath, deletedValue)

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

	// Connect clients capture their base URL, so every client is rebuilt after restart.
	httpClient2 := newE2EHTTPClient()
	configClient = agentcomposev1connect.NewConfigServiceClient(httpClient2, baseURL2)
	sessionClient = agentcomposev1connect.NewSessionServiceClient(httpClient2, baseURL2)
	execClient = newE2EExecClient(httpClient2, baseURL2)
	sandboxClient = agentcomposev2connect.NewSandboxServiceClient(httpClient2, baseURL2)

	getAResp, err := sessionClient.GetSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sandboxAID}))
	if err != nil {
		t.Fatalf("GetSession A after daemon restart returned error: %v", err)
	}
	assertE2ESessionWorkspaceState(t, getAResp.Msg.GetSession(), sandboxAID, workspaceID, domain.VMStatusStopped, workspaceAPath)
	assertE2EWorkspaceSnapshot(t, getAResp.Msg.GetSession().GetWorkspace(), workspaceASnapshot)
	assertE2EWorkspaceConfigPersisted(t, ctx, configClient, workspace)

	resumeAResp, err := sessionClient.ResumeSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sandboxAID}))
	if err != nil {
		t.Fatalf("ResumeSession A after daemon restart returned error: %v", err)
	}
	assertE2ESessionWorkspaceState(t, resumeAResp.Msg.GetSession(), sandboxAID, workspaceID, domain.VMStatusRunning, workspaceAPath)
	assertE2EWorkspaceSnapshot(t, resumeAResp.Msg.GetSession().GetWorkspace(), workspaceASnapshot)
	resumedHandleA := inspectE2EDockerSandbox(t, ctx, dockerClient, sandboxAID)
	if resumedHandleA.ContainerID != handleA.ContainerID || !resumedHandleA.Running || filepath.Clean(resumedHandleA.WorkspaceSource) != filepath.Clean(workspaceAPath) {
		t.Fatalf("resumed Docker sandbox A handle = %+v, want original running handle %+v", resumedHandleA, handleA)
	}
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxAID, modifiedPath, agentValue)
	assertE2EWorkspaceFileAbsent(t, ctx, execClient, sandboxAID, deletedPath)
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxAID, generatedPath, generatedValue)

	createBResp, err := sessionClient.CreateSession(ctx, connect.NewRequest(&agentcomposev1.CreateSessionRequest{
		Title:       "docker workspace resume B",
		WorkspaceId: workspaceID,
		GuestImage:  image,
		Driver:      "docker",
	}))
	if err != nil {
		t.Fatalf("CreateSession B returned error: %v", err)
	}
	sessionB := createBResp.Msg.GetSession()
	sandboxBID := sessionB.GetSummary().GetSessionId()
	sandboxBRemoved := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if !sandboxBRemoved {
			if _, err := sandboxClient.RemoveSandbox(cleanupCtx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxBID, Force: true})); err != nil {
				t.Logf("public sandbox B cleanup failed for %s: %v", sandboxBID, err)
			}
		}
		fallbackCtx, fallbackCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer fallbackCancel()
		removeE2EDockerSandboxFallback(t, fallbackCtx, dockerClient, sandboxBID)
	})
	assertE2ESessionWorkspaceState(t, sessionB, sandboxBID, workspaceID, domain.VMStatusRunning, "")
	workspaceBPath := sessionB.GetSummary().GetWorkspacePath()
	if sandboxBID == sandboxAID || workspaceBPath == workspaceAPath {
		t.Fatalf("sandbox B identity/path = %q/%q, must differ from A %q/%q", sandboxBID, workspaceBPath, sandboxAID, workspaceAPath)
	}
	handleB := inspectE2EDockerSandbox(t, ctx, dockerClient, sandboxBID)
	if !handleB.Running || handleB.ContainerID == handleA.ContainerID || filepath.Clean(handleB.WorkspaceSource) != filepath.Clean(workspaceBPath) {
		t.Fatalf("Docker sandbox B handle = %+v, must be a distinct running handle for workspace %q", handleB, workspaceBPath)
	}
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxBID, modifiedPath, sourceV2)
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxBID, deletedPath, deletedValue)
	assertE2EWorkspaceFileAbsent(t, ctx, execClient, sandboxBID, generatedPath)
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxAID, modifiedPath, agentValue)
	assertE2EWorkspaceFileAbsent(t, ctx, execClient, sandboxAID, deletedPath)
	assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxAID, generatedPath, generatedValue)

	// A second sandbox and another daemon process still must not mutate the source.
	assertE2EWorkspaceFiles(t, listE2EWorkspaceFiles(t, ctx, httpClient2, baseURL2, workspaceID), workspaceID, sourceV2Entries)
	assertE2EWorkspaceDownload(t, downloadE2EWorkspaceFile(t, ctx, httpClient2, baseURL2, workspaceID, modifiedPath), modifiedPath, sourceV2)
	assertE2EWorkspaceDownload(t, downloadE2EWorkspaceFile(t, ctx, httpClient2, baseURL2, workspaceID, deletedPath), deletedPath, deletedValue)

	removeE2ESandboxPublic(t, ctx, sandboxClient, sandboxBID)
	sandboxBRemoved = true
	removeE2EDockerSandboxFallback(t, ctx, dockerClient, sandboxBID)
	assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, sandboxBID, 0)
	removeE2ESandboxPublic(t, ctx, sandboxClient, sandboxAID)
	sandboxARemoved = true
	removeE2EDockerSandboxFallback(t, ctx, dockerClient, sandboxAID)
	assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, sandboxAID, 0)

	if _, err := configClient.DeleteWorkspaceConfig(ctx, connect.NewRequest(&agentcomposev1.WorkspaceConfigIDRequest{WorkspaceId: workspaceID})); err != nil {
		t.Fatalf("DeleteWorkspaceConfig returned error: %v", err)
	}
	workspaceRemoved = true

	httpClient2.CloseIdleConnections()
	daemon2.stop(t)
	assertE2EDaemonReleased(t, daemon2, socketPath, listenAddress2)
	assertE2ETCPAddressReleased(t, listenAddress1)
	assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, sandboxAID, 0)
	assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, sandboxBID, 0)
	if err := os.RemoveAll(testRoot); err != nil {
		t.Fatalf("remove E2E temp root: %v", err)
	}
	if _, err := os.Stat(testRoot); !os.IsNotExist(err) {
		t.Fatalf("E2E temp root %q still exists after cleanup: %v", testRoot, err)
	}
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
		t.Fatalf("inspect Docker sandbox %s container: %v", sandboxID, err)
	}
	if containerInfo.ID == "" || containerInfo.Config == nil || containerInfo.State == nil {
		t.Fatalf("Docker sandbox %s returned incomplete container inspect", sandboxID)
	}
	if containerInfo.Config.Labels["agent-compose.sandbox_id"] != sandboxID || containerInfo.Config.Labels["agent-compose.driver"] != "docker" {
		t.Fatalf("Docker sandbox %s labels do not match exact sandbox/driver identity", sandboxID)
	}
	workspaceSource := ""
	for _, mount := range containerInfo.Mounts {
		if filepath.Clean(mount.Destination) == e2eGuestWorkspacePath {
			workspaceSource = mount.Source
			break
		}
	}
	if workspaceSource == "" {
		t.Fatalf("Docker sandbox %s has no %s mount", sandboxID, e2eGuestWorkspacePath)
	}
	return e2eDockerSandboxHandle{
		ContainerID:     containerInfo.ID,
		Running:         containerInfo.State.Running,
		WorkspaceSource: workspaceSource,
	}
}

func assertE2EDockerSandboxContainerCount(t *testing.T, ctx context.Context, dockerClient *client.Client, sandboxID string, want int) {
	t.Helper()
	args := filters.NewArgs(
		filters.Arg("label", "agent-compose.sandbox_id="+sandboxID),
		filters.Arg("label", "agent-compose.driver=docker"),
	)
	containers, err := dockerClient.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: args})
	if err != nil {
		t.Fatalf("list Docker sandbox %s containers: %v", sandboxID, err)
	}
	if len(containers) != want {
		t.Fatalf("Docker sandbox %s container count = %d, want %d", sandboxID, len(containers), want)
	}
}

func assertE2ESessionWorkspaceState(
	t *testing.T,
	session *agentcomposev1.SessionDetail,
	sandboxID string,
	workspaceID string,
	vmStatus string,
	wantWorkspacePath string,
) {
	t.Helper()
	if session == nil || session.GetSummary() == nil || session.GetWorkspace() == nil {
		t.Fatalf("session %s response is missing summary or workspace snapshot", sandboxID)
	}
	summary := session.GetSummary()
	if sandboxID == "" || summary.GetSessionId() != sandboxID || summary.GetDriver() != "docker" || summary.GetVmStatus() != vmStatus || summary.GetGuestImage() == "" {
		t.Fatalf("session %s summary = %#v, want docker/%s with stable identity", sandboxID, summary, vmStatus)
	}
	if summary.GetWorkspacePath() == "" || (wantWorkspacePath != "" && summary.GetWorkspacePath() != wantWorkspacePath) {
		t.Fatalf("session %s workspace path = %q, want %q", sandboxID, summary.GetWorkspacePath(), wantWorkspacePath)
	}
	workspace := session.GetWorkspace()
	if session.GetWorkspaceId() != workspaceID || workspace.GetId() != workspaceID || workspace.GetType() != "file" || workspace.GetConfigJson() == "" {
		t.Fatalf("session %s workspace identity/snapshot = %q/%#v, want file workspace %s", sandboxID, session.GetWorkspaceId(), workspace, workspaceID)
	}
}

func cloneE2EWorkspaceSnapshot(snapshot *agentcomposev1.SessionWorkspaceSnapshot) *agentcomposev1.SessionWorkspaceSnapshot {
	if snapshot == nil {
		return nil
	}
	return &agentcomposev1.SessionWorkspaceSnapshot{
		Id:         snapshot.GetId(),
		Name:       snapshot.GetName(),
		Type:       snapshot.GetType(),
		ConfigJson: snapshot.GetConfigJson(),
	}
}

func assertE2EWorkspaceSnapshot(t *testing.T, got, want *agentcomposev1.SessionWorkspaceSnapshot) {
	t.Helper()
	if got == nil || want == nil || got.GetId() != want.GetId() || got.GetName() != want.GetName() || got.GetType() != want.GetType() || got.GetConfigJson() != want.GetConfigJson() {
		t.Fatalf("workspace snapshot = %#v, want persisted snapshot %#v", got, want)
	}
}

func assertE2EWorkspaceConfigPersisted(
	t *testing.T,
	ctx context.Context,
	configClient agentcomposev1connect.ConfigServiceClient,
	want *agentcomposev1.WorkspaceConfig,
) {
	t.Helper()
	response, err := configClient.ListWorkspaceConfigs(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("ListWorkspaceConfigs after daemon restart returned error: %v", err)
	}
	for _, workspace := range response.Msg.GetWorkspaces() {
		if workspace.GetId() != want.GetId() {
			continue
		}
		if workspace.GetName() != want.GetName() || workspace.GetType() != want.GetType() || workspace.GetConfigJson() != want.GetConfigJson() || workspace.GetComment() != want.GetComment() {
			t.Fatalf("workspace config after daemon restart = %#v, want %#v", workspace, want)
		}
		return
	}
	t.Fatalf("workspace config %s is missing after daemon restart", want.GetId())
}

func assertE2EWorkspaceDownload(t *testing.T, download e2eWorkspaceDownload, path, content string) {
	t.Helper()
	if !bytes.Equal(download.Bytes, []byte(content)) || download.ContentType != "application/octet-stream" || download.Filename != filepath.Base(path) || download.ContentDisposition == "" {
		t.Fatalf("workspace download %q metadata/content mismatch: bytes=%d content_type=%q disposition_set=%t filename=%q", path, len(download.Bytes), download.ContentType, download.ContentDisposition != "", download.Filename)
	}
}

func removeE2ESandboxPublic(
	t *testing.T,
	ctx context.Context,
	sandboxClient agentcomposev2connect.SandboxServiceClient,
	sandboxID string,
) {
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
		t.Fatalf("agent-compose TCP address %s remains in use: %v", listenAddress, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close TCP release probe for %s: %v", listenAddress, err)
	}
}
