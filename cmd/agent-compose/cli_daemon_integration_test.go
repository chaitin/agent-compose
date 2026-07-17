package main

import (
	"agent-compose/pkg/config"
	"agent-compose/pkg/imagecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"
)

func TestIntegrationCLIResolvesShortResourceIDsBeforeDaemonRequests(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-short-id-demo
agents:
  reviewer:
    provider: codex
`)
	projectID := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sandboxID := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	runID := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	project := testCLIProject(projectID, "cli-short-id-demo", composePath)
	session := testCLISessionSummary(sandboxID, "RUNNING", projectID, "reviewer", runID)
	run := &agentcomposev2.RunSummary{
		RunId:     runID,
		ProjectId: projectID,
		AgentName: "reviewer",
		Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
		SandboxId: sandboxID,
		UpdatedAt: "2026-06-11T00:00:01Z",
	}
	var stopped []string
	var resumed []string
	var execSandbox string
	var runSandbox string
	var inspectedRun string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
		},
		run: runServiceStub{
			listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{run}}), nil
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				inspectedRun = req.Msg.GetRunId()
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(projectID, req.Msg.GetRunId(), "reviewer", sandboxID, agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "ok")}), nil
			},
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				runSandbox = req.Msg.GetSandboxId()
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     runID,
					Run:       run,
				})
			},
		},
		session: sessionServiceStub{
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: []*agentcomposev2.Sandbox{session}}), nil
			},
			stopSession: func(ctx context.Context, req *connect.Request[agentcomposev2.StopSandboxRequest]) (*connect.Response[agentcomposev2.StopSandboxResponse], error) {
				stopped = append(stopped, req.Msg.GetSandboxId())
				return connect.NewResponse(&agentcomposev2.StopSandboxResponse{}), nil
			},
			resumeSession: func(ctx context.Context, req *connect.Request[agentcomposev2.ResumeSandboxRequest]) (*connect.Response[agentcomposev2.ResumeSandboxResponse], error) {
				resumed = append(resumed, req.Msg.GetSandboxId())
				return connect.NewResponse(&agentcomposev2.ResumeSandboxResponse{}), nil
			},
		},
		exec: execServiceStub{
			execStream: func(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
				execSandbox = req.Msg.GetSandboxId()
				return stream.Send(&agentcomposev2.ExecStreamResponse{
					EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED,
					Result: &agentcomposev2.ExecResult{
						ExecId:    "exec-short",
						SandboxId: req.Msg.GetSandboxId(),
						Command:   req.Msg.GetCommand(),
						Success:   true,
					},
				})
			},
		},
	})
	defer server.Close()

	sandboxShort := shortOpaqueID(sandboxID)
	runShort := shortOpaqueID(runID)
	if _, stderr, _, exitCode := executeCLICommand("stop", "--host", server.URL, "--file", composePath, sandboxShort); exitCode != 0 || stderr != "" {
		t.Fatalf("stop short id code/stderr = %d / %q", exitCode, stderr)
	}
	if _, stderr, _, exitCode := executeCLICommand("resume", "--host", server.URL, "--file", composePath, sandboxShort); exitCode != 0 || stderr != "" {
		t.Fatalf("resume short id code/stderr = %d / %q", exitCode, stderr)
	}
	if _, stderr, _, exitCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, sandboxShort, "--command", "true"); exitCode != 0 || stderr != "" {
		t.Fatalf("exec short id code/stderr = %d / %q", exitCode, stderr)
	}
	if _, stderr, _, exitCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "run", runShort); exitCode != 0 || stderr != "" {
		t.Fatalf("inspect run short id code/stderr = %d / %q", exitCode, stderr)
	}
	if _, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--sandbox", sandboxShort, "reviewer", "--prompt", "hello"); exitCode != 0 || stderr != "" {
		t.Fatalf("run --sandbox short code/stderr = %d / %q", exitCode, stderr)
	}
	if !reflect.DeepEqual(stopped, []string{sandboxID}) || !reflect.DeepEqual(resumed, []string{sandboxID}) || execSandbox != sandboxID || inspectedRun != runID || runSandbox != sandboxID {
		t.Fatalf("resolved ids stopped=%#v resumed=%#v exec=%q inspect=%q run=%q", stopped, resumed, execSandbox, inspectedRun, runSandbox)
	}
}

func TestIntegrationCLICacheLifecycleWithInProcessDaemon(t *testing.T) {
	root := t.TempDir()
	imageCacheRoot := filepath.Join(root, "images")
	t.Setenv("DATA_ROOT", root)
	t.Setenv("SANDBOX_ROOT", filepath.Join(root, "sessions"))
	t.Setenv("IMAGE_CACHE_ROOT", imageCacheRoot)
	t.Setenv("HTTP_LISTEN", "")
	t.Setenv("AGENT_COMPOSE_SOCKET", "")
	t.Setenv("AGENT_COMPOSE_HOST", "")
	t.Setenv("RUNTIME_DRIVER", config.RuntimeDriverDocker)
	t.Setenv("DOCKER_IMAGE", "guest:latest")
	t.Setenv("SANDBOX_START_TIMEOUT", "1s")
	t.Setenv("SANDBOX_STOP_TIMEOUT", "1s")
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("BOXLITE_HOME", filepath.Join(root, "boxlite"))
	t.Setenv("BOXLITE_RUNTIME_DIR", filepath.Join(root, "boxlite-runtime"))
	t.Setenv("DOCKER_HOME", filepath.Join(root, "docker"))
	t.Setenv("MICROSANDBOX_HOME", filepath.Join(root, "microsandbox"))
	t.Setenv("MICROSANDBOX_MSB_PATH", filepath.Join(root, "msb"))
	t.Setenv("MICROSANDBOX_LIB_PATH", filepath.Join(root, "libmicrosandbox_go_ffi.so"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, err := NewDaemonApp(ctx, DaemonOptions{StartBackground: func(do.Injector) error { return nil }})
	if err != nil {
		t.Fatalf("NewDaemonApp returned error: %v", err)
	}
	server := httptest.NewServer(app.Echo)
	defer server.Close()

	cache, err := imagecache.New(imagecache.Config{Root: imageCacheRoot})
	if err != nil {
		t.Fatalf("imagecache.New returned error: %v", err)
	}
	referencedImageID := "sha256:cli-ref"
	referencedRootFS := cache.MaterializedRootFSPath(referencedImageID)
	referencedReady := filepath.Join(cache.MaterializedImageDir(referencedImageID), ".rootfs.ready")
	orphanRootFS := filepath.Join(cache.MaterializationRoot(), "cli-orphan", "rootfs")
	missingRootFS := filepath.Join(cache.MaterializationRoot(), "cli-missing", "rootfs")
	for _, dir := range []string{
		filepath.Join(referencedRootFS, "bin"),
		orphanRootFS,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for path, data := range map[string]string{
		filepath.Join(referencedRootFS, "bin", "tool"): "referenced",
		referencedReady:                          "ready",
		filepath.Join(orphanRootFS, "layer.txt"): "orphan",
	} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := cache.SaveMetadata(imagecache.MetadataFile{Images: []imagecache.ImageMetadata{
		{
			CacheKey:        referencedImageID,
			RequestedRef:    "agent:referenced",
			NormalizedRef:   "registry.example/agent:referenced",
			RepoDigests:     []string{"registry.example/agent@sha256:cli-ref"},
			ManifestDigest:  "sha256:manifest-cli-ref",
			ConfigDigest:    referencedImageID,
			RootFSCachePath: referencedRootFS,
		},
		{
			CacheKey:        "sha256:cli-missing",
			RequestedRef:    "agent:missing",
			NormalizedRef:   "registry.example/agent:missing",
			ManifestDigest:  "sha256:manifest-cli-missing",
			ConfigDigest:    "sha256:cli-missing",
			RootFSCachePath: missingRootFS,
		},
	}}); err != nil {
		t.Fatalf("SaveMetadata returned error: %v", err)
	}

	listOut, listErr, listRuns, listCode := executeCLICommand("cache", "ls", "--host", server.URL, "--json", "--type", "materialized")
	if listCode != 0 || listErr != "" || listRuns != 0 {
		t.Fatalf("cache ls code/stderr/runs = %d / %q / %d", listCode, listErr, listRuns)
	}
	var listed composeCacheListOutput
	if err := json.Unmarshal([]byte(listOut), &listed); err != nil {
		t.Fatalf("cache ls JSON decode failed: %v\n%s", err, listOut)
	}
	referenced := requireCLICacheByPath(t, listed.Caches, referencedRootFS)
	orphan := requireCLICacheByPath(t, listed.Caches, orphanRootFS)
	if referenced.Status != "unused" || !referenced.Removable || len(referenced.References) != 1 || referenced.References[0].Policy != "advisory" {
		t.Fatalf("advisory metadata cache = %#v", referenced)
	}
	if orphan.Status != "orphaned" || !orphan.Removable {
		t.Fatalf("orphan cache = %#v", orphan)
	}
	if !stringSliceContainsSubstring(listed.Warnings, "cli-missing") {
		t.Fatalf("cache ls warnings = %#v, want missing metadata path warning", listed.Warnings)
	}

	inspectOut, inspectErr, _, inspectCode := executeCLICommand("cache", "inspect", "--host", server.URL, referenced.ID)
	if inspectCode != 0 || inspectErr != "" {
		t.Fatalf("cache inspect code/stderr = %d / %q", inspectCode, inspectErr)
	}
	if !strings.Contains(inspectOut, "References:") || !strings.Contains(inspectOut, "agent:referenced") {
		t.Fatalf("cache inspect stdout = %q", inspectOut)
	}

	dryRunOut, dryRunErr, _, dryRunCode := executeCLICommand("cache", "prune", "--host", server.URL, "--type", "materialized", "--orphaned")
	if dryRunCode != 0 || dryRunErr != "" {
		t.Fatalf("cache prune dry-run code/stderr = %d / %q", dryRunCode, dryRunErr)
	}
	if !strings.Contains(dryRunOut, "Dry-run") || !strings.Contains(dryRunOut, orphan.ID) {
		t.Fatalf("cache prune dry-run stdout = %q", dryRunOut)
	}
	assertLocalPathExists(t, orphanRootFS)

	forceOut, forceErr, _, forceCode := executeCLICommand("cache", "prune", "--host", server.URL, "--json", "--type", "materialized", "--orphaned", "--force")
	if forceCode != 0 || forceErr != "" {
		t.Fatalf("cache prune force code/stderr = %d / %q", forceCode, forceErr)
	}
	var forceResult composeCacheOperationOutput
	if err := json.Unmarshal([]byte(forceOut), &forceResult); err != nil {
		t.Fatalf("cache prune force JSON decode failed: %v\n%s", err, forceOut)
	}
	if forceResult.DryRun || !stringSliceContains(forceResult.Removed, orphan.ID) {
		t.Fatalf("cache prune force result = %#v", forceResult)
	}
	assertLocalPathMissing(t, orphanRootFS)
	assertLocalPathExists(t, referencedRootFS)

	removedOut, removedErr, _, removedCode := executeCLICommand("cache", "rm", "--host", server.URL, "--force", referenced.ID)
	if removedCode != 0 || removedErr != "" {
		t.Fatalf("cache rm advisory exit code = %d; stderr=%q", removedCode, removedErr)
	}
	if !strings.Contains(removedOut, "Removed") || !strings.Contains(removedOut, referenced.ID) {
		t.Fatalf("cache rm advisory stdout = %q", removedOut)
	}
	assertLocalPathMissing(t, referencedRootFS)
	assertLocalPathMissing(t, referencedReady)
}

func TestIntegrationCLIRemoveImageDoesNotDeleteRuntimeCachesWithInProcessDaemon(t *testing.T) {
	t.Setenv("IMAGE_STORE_MODE", config.ImageStoreModeOCI)
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", nil)
	defer cancel()
	server := httptest.NewServer(app.Echo)
	defer server.Close()

	cache, err := imagecache.New(imagecache.Config{Root: app.Config.ImageCacheRoot})
	if err != nil {
		t.Fatalf("imagecache.New returned error: %v", err)
	}
	imageID := "sha256:cli-rmi"
	layoutPath := cache.MaterializedOCILayoutPath(imageID)
	rootfsPath := cache.MaterializedRootFSPath(imageID)
	boxliteCachePath := filepath.Join(app.Config.BoxliteHome, "images", "local", "keep")
	microsandboxDiskPath := filepath.Join(app.Config.MicrosandboxHome, "docker-disks", "keep.raw")
	for _, dir := range []string{
		layoutPath,
		rootfsPath,
		boxliteCachePath,
		filepath.Dir(microsandboxDiskPath),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for path, data := range map[string]string{
		filepath.Join(layoutPath, "sentinel"):   "layout",
		filepath.Join(rootfsPath, "sentinel"):   "rootfs",
		filepath.Join(boxliteCachePath, "disk"): "boxlite",
		microsandboxDiskPath:                    "microsandbox",
	} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := cache.SaveMetadata(imagecache.MetadataFile{Images: []imagecache.ImageMetadata{{
		CacheKey:        imageID,
		RequestedRef:    "registry.example/rmi:1.0",
		NormalizedRef:   "registry.example/rmi:1.0",
		RepoTags:        []string{"registry.example/rmi:1.0"},
		RepoDigests:     []string{"registry.example/rmi@sha256:cli-rmi"},
		ManifestDigest:  "sha256:manifest-cli-rmi",
		ConfigDigest:    imageID,
		LayoutCachePath: layoutPath,
		RootFSCachePath: rootfsPath,
	}}}); err != nil {
		t.Fatalf("SaveMetadata returned error: %v", err)
	}

	stdout, stderr, runCount, exitCode := executeCLICommand("rmi", "--host", server.URL, "--json", "--force", "--prune-children", "registry.example/rmi:1.0")
	if exitCode != 0 || stderr != "" || runCount != 0 {
		t.Fatalf("rmi code/stderr/runs = %d / %q / %d", exitCode, stderr, runCount)
	}
	var removed composeImageRemoveOutput
	if err := json.Unmarshal([]byte(stdout), &removed); err != nil {
		t.Fatalf("rmi JSON decode failed: %v\n%s", err, stdout)
	}
	if len(removed.DeletedIDs) != 1 || removed.DeletedIDs[0] != displayOpaqueID(imageID) || len(removed.Warnings) == 0 {
		t.Fatalf("rmi output = %#v", removed)
	}
	for _, path := range []string{
		filepath.Join(layoutPath, "sentinel"),
		filepath.Join(rootfsPath, "sentinel"),
		filepath.Join(boxliteCachePath, "disk"),
		microsandboxDiskPath,
	} {
		assertLocalPathExists(t, path)
	}
}
