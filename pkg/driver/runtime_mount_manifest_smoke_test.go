//go:build boxlitecgo || cgo

package driver

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func runtimeSmokeEnabled(t *testing.T, driver string) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("SMOKE_RUNTIME_DRIVERS"))
	if raw == "" {
		t.Skipf("set SMOKE_RUNTIME_DRIVERS=%s to run real %s runtime smoke test", driver, driver)
	}
	for _, item := range strings.Split(raw, ",") {
		if resolveRuntimeDriver(item) == driver || strings.EqualFold(strings.TrimSpace(item), "all") {
			return
		}
	}
	t.Skipf("SMOKE_RUNTIME_DRIVERS=%q does not include %s", raw, driver)
}

func runtimeSmokeKeepTmp() bool {
	return strings.TrimSpace(os.Getenv("SMOKE_KEEP_TMP")) != ""
}

func newRuntimeSmokeConfig(t *testing.T, driver string) *appconfig.Config {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "agent-compose-smoke-")
	if err != nil {
		t.Fatalf("create smoke root: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() && runtimeSmokeKeepTmp() {
			t.Logf("keeping smoke root after failure: %s", root)
			return
		}
		_ = os.RemoveAll(root)
	})
	boxDiskSizeGB := 0
	if raw := strings.TrimSpace(os.Getenv("SMOKE_BOX_DISK_SIZE_GB")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("parse SMOKE_BOX_DISK_SIZE_GB=%q: %v", raw, err)
		}
		boxDiskSizeGB = parsed
	}
	repoRoot := runtimeSmokeRepoRoot(t)
	config := &appconfig.Config{
		DataRoot:                 root,
		SandboxRoot:              filepath.Join(root, "sandboxes"),
		RuntimeDriver:            driver,
		BoxliteHome:              filepath.Join(root, "boxlite"),
		DockerHome:               filepath.Join(root, "docker"),
		MicrosandboxHome:         filepath.Join(root, "microsandbox"),
		DefaultImage:             firstNonEmpty(os.Getenv("SMOKE_DEFAULT_IMAGE"), "debian:bookworm-slim"),
		DockerDefaultImage:       firstNonEmpty(os.Getenv("SMOKE_DOCKER_DEFAULT_IMAGE"), os.Getenv("SMOKE_DEFAULT_IMAGE"), "debian:bookworm-slim"),
		MicrosandboxDefaultImage: firstNonEmpty(os.Getenv("SMOKE_MICROSANDBOX_DEFAULT_IMAGE"), os.Getenv("SMOKE_DEFAULT_IMAGE"), "debian:bookworm-slim"),
		ImageRegistry:            firstNonEmpty(os.Getenv("IMAGE_REGISTRY"), "docker.io"),
		BoxRootfsPath:            strings.TrimSpace(os.Getenv("SMOKE_BOX_ROOTFS_PATH")),
		BoxDiskSizeGB:            boxDiskSizeGB,
		BoxCacheTTL:              time.Hour,
		BoxliteRuntimeDir:        firstNonEmpty(os.Getenv("BOXLITE_RUNTIME_DIR"), filepath.Join(repoRoot, "build", "boxlite", "runtime")),
		MicrosandboxMSBPath:      firstNonEmpty(os.Getenv("MICROSANDBOX_MSB_PATH"), filepath.Join(repoRoot, "build", "microsandbox", "bin", "msb")),
		MicrosandboxLibPath:      firstNonEmpty(os.Getenv("MICROSANDBOX_LIB_PATH"), filepath.Join(repoRoot, "build", "microsandbox", "lib", "libmicrosandbox_go_ffi.so")),
		GuestWorkspacePath:       "/workspace",
		GuestHomePath:            "/root",
		GuestStateRoot:           "/data/state",
		GuestRuntimeRoot:         "/data/runtime",
		GuestLogRoot:             "/data/logs",
		JupyterGuestPort:         8888,
		JupyterProxyBasePath:     "/agent-compose/session",
		SandboxStartTimeout:      3 * time.Minute,
		SandboxStopTimeout:       30 * time.Second,
	}
	if err := os.MkdirAll(config.SandboxRoot, 0o755); err != nil {
		t.Fatalf("create sandbox root: %v", err)
	}
	return config
}

func runtimeSmokeRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("find repo root from %s: go.mod not found", dir)
		}
		dir = parent
	}
}

func newRuntimeSmokeSandbox(t *testing.T, _ context.Context, config *appconfig.Config, driver string) (*Sandbox, VMState, ProxyState) {
	t.Helper()
	sandboxID := "runtime-mount-smoke-" + driver + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	sandboxRoot := filepath.Join(config.SandboxRoot, sandboxID)
	session := &Sandbox{
		Summary: SandboxSummary{
			ID:            sandboxID,
			Driver:        driver,
			GuestImage:    defaultGuestImageForDriver(config, driver),
			RuntimeRef:    sandboxID,
			WorkspacePath: filepath.Join(sandboxRoot, "workspace"),
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		},
		EnvItems: []SandboxEnvVar{{Name: "SMOKE_MARKER", Value: "/data/state/runtime-mount-smoke.txt"}},
	}
	if _, err := prepareRuntimeMountManifest(config, session, driver); err != nil {
		t.Fatalf("prepareRuntimeMountManifest returned error: %v", err)
	}
	vmState := VMState{
		Driver:      driver,
		Mode:        driver,
		BoxName:     sandboxID,
		Image:       session.Summary.GuestImage,
		RuntimeHome: runtimeHomeForDriver(config, driver),
	}
	proxyState := ProxyState{
		ProxyPath: "/jupyter/" + sandboxID + "/lab",
		GuestHost: "127.0.0.1",
		GuestPort: config.JupyterGuestPort,
	}
	return session, vmState, proxyState
}

func assertDirectoryOnlyRuntimeSmokeManifest(t *testing.T, session *Sandbox, driver string) {
	t.Helper()
	manifest, err := loadDirectoryRuntimeMountManifest(session, driver)
	if err != nil {
		t.Fatalf("loadDirectoryRuntimeMountManifest returned error: %v", err)
	}
	mounts := map[string]string{}
	for _, mount := range manifest.Mounts {
		mounts[mount.GuestPath] = mount.HostPath
	}
	if mounts[directoryOnlyGuestSandboxPath] != hostSandboxDir(session) {
		t.Fatalf("sandbox mount = %q, want %q", mounts[directoryOnlyGuestSandboxPath], hostSandboxDir(session))
	}
	for _, guestPath := range []string{"/root/.claude.json", "/root/.gitconfig"} {
		if mounts[guestPath] != "" {
			t.Fatalf("%s unexpectedly mounted as file source %s", guestPath, mounts[guestPath])
		}
	}
}

func assertRuntimeSmokeHomeFiles(t *testing.T, ctx context.Context, runtime SandboxRuntime, session *Sandbox, vmState VMState) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	markerPath := filepath.Join(hostSandboxDir(session), "state", "runtime-mount-smoke.txt")
	for {
		data, err := os.ReadFile(markerPath)
		if err == nil && strings.TrimSpace(string(data)) == "ok" {
			break
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("runtime smoke marker missing at %s: %v", markerPath, err)
			}
			t.Fatalf("runtime smoke marker at %s did not contain ok", markerPath)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for runtime smoke marker: %v", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	hostGitconfig := filepath.Join(hostSandboxHome(session), ".gitconfig")
	if _, err := os.Stat(hostGitconfig); err != nil {
		t.Fatalf("host gitconfig missing after guest startup: %v", err)
	}
	homeMarkerPath := filepath.Join(hostSandboxHome(session), ".codex", "runtime-mount-smoke-home.txt")
	for {
		data, err := os.ReadFile(homeMarkerPath)
		if err == nil && strings.TrimSpace(string(data)) == "ok" {
			return
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("runtime smoke home marker missing at %s: %v", homeMarkerPath, err)
			}
			t.Fatalf("runtime smoke home marker at %s did not contain ok", homeMarkerPath)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for runtime smoke home marker: %v", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func assertRuntimeStopResumePreservesWritableLayer(t *testing.T, ctx context.Context, config *appconfig.Config, runtime SandboxRuntime, session *Sandbox, vmState VMState, proxyState ProxyState) {
	t.Helper()
	proxyState.Enabled = false
	info, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		t.Fatalf("EnsureSandbox() error = %v", err)
	}
	vmState.BoxID = info.BoxID
	originalBoxID := info.BoxID
	statePath := "/tmp/regression-sandbox-state.txt"
	if session.Summary.Driver != RuntimeDriverDocker {
		// BoxLite mounts /tmp as tmpfs, so use a path on the guest root disk
		// when checking writable-layer persistence across a VM power cycle.
		statePath = "/var/tmp/regression-sandbox-state.txt"
	}
	t.Cleanup(func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), SandboxStopContextTimeout(session.Summary.Driver, config.SandboxStopTimeout))
		defer removeCancel()
		_ = runtime.RemoveSandbox(removeCtx, session, vmState)
	})

	writeResult, err := runtime.Exec(ctx, session, vmState, ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", "printf %s sandbox-exec-before-stop-ok > " + statePath},
		Cwd:     "/",
	})
	if err != nil || !writeResult.Success {
		t.Fatalf("write state result = %#v, err=%v", writeResult, err)
	}

	for round := 1; round <= 2; round++ {
		missing, err := runtime.StopSandbox(ctx, session, vmState)
		if err != nil || missing {
			t.Fatalf("round %d StopSandbox() missing=%v error=%v", round, missing, err)
		}
		vmState.StoppedAt = time.Now().UTC()
		info, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
		if err != nil {
			t.Fatalf("round %d EnsureSandbox() error = %v", round, err)
		}
		if info.BoxID != originalBoxID {
			t.Fatalf("round %d resumed runtime = %q, want original %q", round, info.BoxID, originalBoxID)
		}
		vmState.StoppedAt = time.Time{}
		readResult, err := runtime.Exec(ctx, session, vmState, ExecSpec{
			Command: "cat",
			Args:    []string{statePath},
			Cwd:     "/",
		})
		if err != nil || !readResult.Success || strings.TrimSpace(readResult.Stdout) != "sandbox-exec-before-stop-ok" {
			t.Fatalf("round %d read state result = %#v, err=%v", round, readResult, err)
		}
	}

	if err := runtime.RemoveSandbox(ctx, session, vmState); err != nil {
		t.Fatalf("RemoveSandbox() error = %v", err)
	}
	vmState.StoppedAt = time.Now().UTC()
	if _, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState); err == nil || !strings.Contains(err.Error(), "refusing to recreate") {
		t.Fatalf("resume after runtime removal error = %v, want refusal to recreate", err)
	}
}

func runtimeSmokeGuestPathAssertionScript() string {
	return `
set -eu
test -d /root
test ! -L /root
test "$(readlink /workspace)" = "/data/workspace"
test "$(readlink /root/.codex)" = "/data/home/.codex"
test "$(readlink /root/.agents)" = "/data/home/.agents"
test "$(readlink /root/.claude)" = "/data/home/.claude"
test "$(readlink /root/.opencode)" = "/data/home/.opencode"
test "$(readlink /root/.gitconfig)" = "/data/home/.gitconfig"
test "$(readlink /root/.claude.json)" = "/data/home/.claude.json"
test "$(readlink /root/.gemini)" = "/data/home/.gemini"
test "$(readlink /root/.config/claude)" = "/data/home/.config/claude"
test "$(readlink /root/.config/Claude)" = "/data/home/.config/Claude"
test "$(readlink /root/.config/gemini)" = "/data/home/.config/gemini"
test "$(readlink /root/.config/opencode)" = "/data/home/.config/opencode"
test "$(readlink /root/.local/share/gemini)" = "/data/home/.local/share/gemini"
test -f /root/.codex/config.toml
test -f /root/.gitconfig
test -f /root/.claude.json
cd /workspace
printf ok > /root/.codex/runtime-mount-smoke-home.txt
printf ok > /data/state/runtime-mount-smoke.txt
`
}
