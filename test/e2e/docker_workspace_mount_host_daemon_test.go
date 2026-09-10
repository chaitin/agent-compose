package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestE2EDockerWorkspaceMount(t *testing.T) {
	image := strings.TrimSpace(os.Getenv(dockerWorkspaceE2EImageEnv))
	if image == "" {
		t.Skipf("set %s to a local Docker guest image", dockerWorkspaceE2EImageEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	repoRoot := e2eRepoRoot(t)
	root, err := os.MkdirTemp(repoRoot, ".wm-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := e2eDaemonBinary(t, ctx, repoRoot, root)
	dockerClient := newE2EDockerClient(t, ctx, image)
	fileCount := workspaceMountE2EFileCount(t)
	address := unusedLoopbackAddress(t)
	baseURL := "http://" + address
	daemon := startE2EDaemon(t, binary, repoRoot, dataRoot, address, image)
	waitForE2EDaemon(t, ctx, daemon, baseURL)
	httpClient := newE2EHTTPClient()
	httpClient.Timeout = 3 * time.Minute
	defer httpClient.CloseIdleConnections()
	projectClient := agentcomposev2connect.NewProjectServiceClient(httpClient, baseURL)
	runClient := agentcomposev2connect.NewRunServiceClient(httpClient, baseURL)
	sandboxClient := agentcomposev2connect.NewSandboxServiceClient(httpClient, baseURL)
	execClient := newE2EExecClient(httpClient, baseURL)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("workspace mount daemon log:\n%s", daemon.logs.String())
		}
	})

	cases := []struct {
		name, mode, target, policy string
		readonly, restart          bool
	}{
		{name: "copy-root", target: ".", policy: "remove"},
		{name: "mount-rw-root", mode: "mount", target: ".", policy: "retain", restart: true},
		{name: "mount-rw-subdir", mode: "mount", target: "reference", policy: "remove"},
		{name: "mount-ro-root", mode: "mount", target: ".", policy: "remove", readonly: true},
		{name: "mount-ro-subdir", mode: "mount", target: "reference", policy: "remove", readonly: true},
	}
	suiteT := t
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := filepath.Join(root, tc.name)
			sourceRoot := filepath.Join(projectRoot, "source")
			fixture := writeWorkspaceMountE2EFixture(t, sourceRoot, tc.name, fileCount)
			projectID := applyWorkspaceMountE2EYAML(t, ctx, binary, baseURL, projectRoot, image, tc.name, tc.mode, tc.target, tc.policy, tc.readonly)
			started := time.Now()
			sandbox := runE2EWorkspaceSandbox(t, ctx, runClient, sandboxClient, projectID, tc.name)
			elapsed := time.Since(started)
			sandboxID := sandbox.GetSandboxId()
			assertWorkspaceMountE2EDelivery(t, sandbox, tc.mode, sourceRoot, tc.target, tc.readonly)
			removed := false
			t.Cleanup(func() { cleanupE2EWorkspaceSandbox(t, dockerClient, sandboxClient, sandboxID, removed) })
			copied, total := countWorkspaceMountE2ECopies(t, dataRoot, fixture)
			wantCopies := 0
			if tc.mode == "" {
				wantCopies = 2 * len(fixture)
			}
			if copied != wantCopies {
				t.Fatalf("source copies in daemon data root = %d, want %d", copied, wantCopies)
			}
			t.Logf("workspace_measurement case=%s source_files=%d source_bytes=%d daemon_files=%d source_copies=%d run_prepare_ms=%d", tc.name, len(fixture), workspaceMountE2EBytes(fixture), total, copied, elapsed.Milliseconds())
			guestRoot := path.Join(e2eGuestWorkspacePath, tc.target)
			assertWorkspaceMountE2EManifest(t, ctx, execClient, sandboxID, guestRoot, fixture)
			guestFixture := maps.Clone(fixture)
			containerBefore := inspectE2EDockerSandboxContainer(t, ctx, dockerClient, sandboxID)
			mountCount := 0
			for _, mount := range containerBefore.Mounts {
				if mount.Destination == guestRoot {
					mountCount++
					if mount.RW == tc.readonly {
						t.Fatalf("workspace mount RW=%t, readonly=%t", mount.RW, tc.readonly)
					}
				}
			}
			if mountCount != 1 {
				t.Fatalf("guest workspace target %s has %d mounts, want exactly one", guestRoot, mountCount)
			}

			writeE2EHostFile(t, filepath.Join(sourceRoot, "modified.txt"), tc.name+":host-update")
			fixture["modified.txt"] = tc.name + ":host-update"
			if tc.mode == "mount" {
				guestFixture["modified.txt"] = fixture["modified.txt"]
				assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxID, path.Join(tc.target, "modified.txt"), fixture["modified.txt"])
			} else {
				assertE2EWorkspaceFileContent(t, ctx, execClient, sandboxID, "modified.txt", tc.name+":modified")
			}
			if tc.readonly {
				assertWorkspaceMountE2EReadOnly(t, ctx, execClient, sandboxID, guestRoot)
			} else {
				mutateWorkspaceMountE2EFiles(t, ctx, execClient, sandboxID, guestRoot, tc.name)
				guestFixture["modified.txt"] = tc.name + ":guest-update"
				guestFixture["created.txt"] = tc.name + ":created"
				guestFixture["renamed.txt"] = guestFixture["rename.txt"]
				delete(guestFixture, "rename.txt")
				delete(guestFixture, "deleted.txt")
				if tc.mode == "mount" {
					fixture = maps.Clone(guestFixture)
				}
			}
			assertWorkspaceMountE2EHostManifest(t, sourceRoot, fixture)
			writeState := runE2EWorkspaceCommand(t, ctx, execClient, sandboxID, "sh", "-eu", "-c", `printf state > /data/state/mount-e2e; printf logs > /data/logs/mount-e2e`)
			requireE2EExecSuccess(t, "write sandbox-owned state and logs", writeState)
			if tc.target != "." {
				writeE2EWorkspaceFile(t, ctx, execClient, sandboxID, "owned.txt", "sandbox-owned")
			}

			if _, err := sandboxClient.StopSandbox(ctx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{SandboxId: sandboxID})); err != nil {
				t.Fatal(err)
			}
			if tc.policy == "remove" {
				assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, sandboxID, 0)
			}
			if tc.restart {
				httpClient.CloseIdleConnections()
				daemon.stop(t)
				address = unusedLoopbackAddress(t)
				baseURL = "http://" + address
				daemon = startE2EDaemon(suiteT, binary, repoRoot, dataRoot, address, image)
				waitForE2EDaemon(t, ctx, daemon, baseURL)
				projectClient = agentcomposev2connect.NewProjectServiceClient(httpClient, baseURL)
				runClient = agentcomposev2connect.NewRunServiceClient(httpClient, baseURL)
				sandboxClient = agentcomposev2connect.NewSandboxServiceClient(httpClient, baseURL)
				execClient = newE2EExecClient(httpClient, baseURL)
			}
			resumed, err := sandboxClient.ResumeSandbox(ctx, connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: sandboxID}))
			if err != nil {
				t.Fatal(err)
			}
			assertWorkspaceMountE2EDelivery(t, resumed.Msg.GetSandbox(), tc.mode, sourceRoot, tc.target, tc.readonly)
			stateCheck := runE2EWorkspaceCommand(t, ctx, execClient, sandboxID, "sh", "-eu", "-c", `test "$(cat /data/state/mount-e2e)" = state; test "$(cat /data/logs/mount-e2e)" = logs`)
			requireE2EExecSuccess(t, "preserve sandbox-owned state and logs", stateCheck)
			containerAfter := inspectE2EDockerSandboxContainer(t, ctx, dockerClient, sandboxID)
			if (containerAfter.ID == containerBefore.ID) != (tc.policy == "retain") {
				t.Fatalf("runtime identity after %s: before=%s after=%s", tc.policy, containerBefore.ID, containerAfter.ID)
			}
			assertWorkspaceMountE2EManifest(t, ctx, execClient, sandboxID, guestRoot, guestFixture)
			assertWorkspaceMountE2EHostManifest(t, sourceRoot, fixture)
			if tc.readonly {
				assertWorkspaceMountE2EReadOnly(t, ctx, execClient, sandboxID, guestRoot)
			}
			if tc.name == "mount-rw-root" {
				assertWorkspaceMountE2EMissingSourceLifecycle(t, ctx, sandboxClient, sandboxID, sourceRoot, tc.target)
			}
			removeE2ESandboxPublic(t, ctx, sandboxClient, sandboxID)
			removed = true
			if tc.name == "mount-rw-root" {
				if err := os.Rename(sourceRoot+".offline", sourceRoot); err != nil {
					t.Fatal(err)
				}
			}
			assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, sandboxID, 0)
			if _, err := projectClient.RemoveProject(ctx, connect.NewRequest(&agentcomposev2.RemoveProjectRequest{Project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: projectID}}})); err != nil {
				t.Fatal(err)
			}
			assertWorkspaceMountE2EHostManifest(t, sourceRoot, fixture)
		})
	}
}

// Leave the source offline after the final successful resume so the caller also
// exercises RemoveSandbox against a running sandbox whose source disappeared.
func assertWorkspaceMountE2EMissingSourceLifecycle(t *testing.T, ctx context.Context, client agentcomposev2connect.SandboxServiceClient, sandboxID, source, target string) {
	t.Helper()
	offline := source + ".offline"
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, offline); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(source); errors.Is(err, fs.ErrNotExist) {
			_ = os.Rename(offline, source)
		}
	})
	response, err := client.GetSandbox(ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatalf("inspect running sandbox with missing source: %v", err)
	}
	delivery := response.Msg.GetSandbox().GetWorkspaceDelivery()
	if delivery.GetMode() != agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT || delivery.GetSourcePath() != resolved || delivery.GetTarget() != target {
		t.Fatalf("inspect lost missing source metadata: %v", delivery)
	}
	if _, err := client.StopSandbox(ctx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{SandboxId: sandboxID})); err != nil {
		t.Fatalf("stop running sandbox with missing source: %v", err)
	}
	if _, err := client.ResumeSandbox(ctx, connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: sandboxID})); err == nil || !strings.Contains(err.Error(), resolved) {
		t.Fatalf("resume must reject the missing source explicitly: %v", err)
	}
	if err := os.Rename(offline, source); err != nil {
		t.Fatal(err)
	}
	resumed, err := client.ResumeSandbox(ctx, connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatalf("resume after restoring source: %v", err)
	}
	assertWorkspaceMountE2EDelivery(t, resumed.Msg.GetSandbox(), "mount", source, target, false)
	if err := os.Rename(source, offline); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceMountE2EDelivery(t *testing.T, sandbox *agentcomposev2.Sandbox, mode, source, target string, readonly bool) {
	t.Helper()
	delivery := sandbox.GetWorkspaceDelivery()
	if delivery == nil {
		t.Fatal("GetSandbox/ResumeSandbox omitted workspace delivery snapshot")
	}
	if mode == "" {
		if delivery.GetMode() != agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY || delivery.GetSourcePath() != "" || delivery.GetTarget() != "" || delivery.GetReadOnly() {
			t.Fatalf("copy delivery exposes incorrect metadata: %v", delivery)
		}
		return
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.GetMode() != agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT || delivery.GetSourcePath() != resolved || delivery.GetTarget() != target || delivery.GetReadOnly() != readonly {
		t.Fatalf("mount delivery snapshot = %v, want source=%s target=%s readonly=%t", delivery, resolved, target, readonly)
	}
	if sandbox.GetWorkspacePath() == "" || sandbox.GetWorkspacePath() == resolved {
		t.Fatalf("workspace_path must remain sandbox-owned: %q", sandbox.GetWorkspacePath())
	}
}

func applyWorkspaceMountE2EYAML(t *testing.T, ctx context.Context, binary, baseURL, root, image, name, mode, target, policy string, readonly bool) string {
	t.Helper()
	modeLine := ""
	if mode != "" {
		modeLine = "    mode: " + mode + "\n"
	}
	yaml := fmt.Sprintf("name: %s\nworkspaces:\n  source:\n    provider: file\n    path: ./source\n    target: %s\n%s    read_only: %t\nagents:\n  worker:\n    provider: codex\n    image: %s\n    driver:\n      docker: {}\n    workspace:\n      name: source\n    sandbox:\n      stopped_runtime_policy: %s\n", name, target, modeLine, readonly, image, policy)
	composePath := filepath.Join(root, "agent-compose.yml")
	writeE2EHostFile(t, composePath, yaml)
	command := exec.CommandContext(ctx, binary, "up", "--host", baseURL, "--file", composePath)
	command.Env = overrideE2EEnv(os.Environ(), map[string]string{"AGENT_COMPOSE_AUTH_TOKEN": "", "AUTH_PASSWORD": "", "AUTH_USERNAME": ""})
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply YAML through CLI: %v\n%s", err, output)
	}
	httpClient := newE2EHTTPClient()
	defer httpClient.CloseIdleConnections()
	client := agentcomposev2connect.NewProjectServiceClient(httpClient, baseURL)
	response, err := client.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_Name{Name: name}}}))
	if err != nil || response.Msg.GetProject().GetSummary().GetProjectId() == "" {
		t.Fatalf("GetProject after YAML apply: %v", err)
	}
	return response.Msg.GetProject().GetSummary().GetProjectId()
}

func workspaceMountE2EFileCount(t *testing.T) int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_WORKSPACE_FILES"))
	if value == "" {
		return 128
	}
	count, err := strconv.Atoi(value)
	if err != nil || count < 1 || count > 100000 {
		t.Fatal("AGENT_COMPOSE_E2E_WORKSPACE_FILES must be between 1 and 100000")
	}
	return count
}

func writeWorkspaceMountE2EFixture(t *testing.T, root, prefix string, count int) map[string]string {
	t.Helper()
	files := map[string]string{"modified.txt": prefix + ":modified", "deleted.txt": prefix + ":deleted", "rename.txt": prefix + ":rename"}
	for index := 0; index < count; index++ {
		files[fmt.Sprintf("nested/file-%06d.txt", index)] = fmt.Sprintf("%s:%06d:%s", prefix, index, strings.Repeat("x", 1024))
	}
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		writeE2EHostFile(t, filepath.Join(root, name), content)
	}
	return files
}

func workspaceMountE2EBytes(files map[string]string) int {
	total := 0
	for _, content := range files {
		total += len(content)
	}
	return total
}

func countWorkspaceMountE2ECopies(t *testing.T, root string, files map[string]string) (int, int) {
	t.Helper()
	hashes := make(map[[32]byte]bool)
	for _, content := range files {
		hashes[sha256.Sum256([]byte(content))] = true
	}
	copies, total := 0, 0
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		total++
		data, err := os.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if hashes[sha256.Sum256(data)] {
			copies++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return copies, total
}

func assertWorkspaceMountE2EManifest(t *testing.T, ctx context.Context, client agentcomposev2connect.ExecServiceClient, sandboxID, root string, files map[string]string) {
	t.Helper()
	lines := make([]string, 0, len(files))
	for name, content := range files {
		hash := sha256.Sum256([]byte(content))
		lines = append(lines, hex.EncodeToString(hash[:])+"  ./"+name+"\n")
	}
	sort.Strings(lines)
	manifestHash := sha256.Sum256([]byte(strings.Join(lines, "")))
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "sh", "-eu", "-c", `cd "$1"; find . -type f -exec sha256sum {} + | LC_ALL=C sort | sha256sum`, "workspace-manifest", root)
	requireE2EExecSuccess(t, "hash complete guest source tree", result)
	fields := strings.Fields(result.GetStdout())
	if len(fields) != 2 || fields[0] != hex.EncodeToString(manifestHash[:]) {
		t.Fatalf("guest source manifest differs from the complete %d-file fixture", len(files))
	}
}

func assertWorkspaceMountE2EHostManifest(t *testing.T, root string, files map[string]string) {
	t.Helper()
	seen := 0
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		want, exists := files[filepath.ToSlash(relative)]
		if !exists || !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected source entry %s", relative)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if string(data) != want {
			return fmt.Errorf("source file %s changed unexpectedly", relative)
		}
		seen++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != len(files) {
		t.Fatalf("source contains %d files, want %d", seen, len(files))
	}
}

func mutateWorkspaceMountE2EFiles(t *testing.T, ctx context.Context, client agentcomposev2connect.ExecServiceClient, sandboxID, root, prefix string) {
	t.Helper()
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "sh", "-eu", "-c", `cd "$1"; printf '%s' "$2:guest-update" > modified.txt; printf '%s' "$2:created" > created.txt; rm deleted.txt; mv rename.txt renamed.txt`, "workspace-mutate", root, prefix)
	requireE2EExecSuccess(t, "mutate workspace source files", result)
}

func assertWorkspaceMountE2EReadOnly(t *testing.T, ctx context.Context, client agentcomposev2connect.ExecServiceClient, sandboxID, root string) {
	t.Helper()
	for _, operation := range []string{`printf fail > created.txt`, `printf fail > modified.txt`, `rm deleted.txt`, `mv rename.txt renamed.txt`} {
		result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "sh", "-eu", "-c", `cd "$1"; `+operation, "workspace-readonly", root)
		if result.GetSuccess() || result.GetExitCode() == 0 {
			t.Fatalf("read-only workspace allowed operation %q", operation)
		}
	}
}
