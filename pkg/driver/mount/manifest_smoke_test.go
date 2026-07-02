//go:build boxlitecgo || cgo

package drivermount

import (
	appconfig "agent-compose/pkg/config"
	. "agent-compose/pkg/driver/types"
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
		if ResolveRuntimeDriver(item) == driver || strings.EqualFold(strings.TrimSpace(item), "all") {
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
		SessionRoot:              filepath.Join(root, "sessions"),
		RuntimeDriver:            driver,
		BoxliteHome:              filepath.Join(root, "boxlite"),
		DockerHome:               filepath.Join(root, "docker"),
		MicrosandboxHome:         filepath.Join(root, "microsandbox"),
		DefaultImage:             FirstNonEmpty(os.Getenv("SMOKE_DEFAULT_IMAGE"), "debian:bookworm-slim"),
		DockerDefaultImage:       FirstNonEmpty(os.Getenv("SMOKE_DOCKER_DEFAULT_IMAGE"), os.Getenv("SMOKE_DEFAULT_IMAGE"), "debian:bookworm-slim"),
		MicrosandboxDefaultImage: FirstNonEmpty(os.Getenv("SMOKE_MICROSANDBOX_DEFAULT_IMAGE"), os.Getenv("SMOKE_DEFAULT_IMAGE"), "debian:bookworm-slim"),
		ImageRegistry:            FirstNonEmpty(os.Getenv("IMAGE_REGISTRY"), "docker.io"),
		BoxRootfsPath:            strings.TrimSpace(os.Getenv("SMOKE_BOX_ROOTFS_PATH")),
		BoxDiskSizeGB:            boxDiskSizeGB,
		BoxCacheTTL:              time.Hour,
		BoxliteRuntimeDir:        FirstNonEmpty(os.Getenv("BOXLITE_RUNTIME_DIR"), filepath.Join(repoRoot, "build", "boxlite", "runtime")),
		MicrosandboxMSBPath:      FirstNonEmpty(os.Getenv("MICROSANDBOX_MSB_PATH"), filepath.Join(repoRoot, "build", "microsandbox", "bin", "msb")),
		MicrosandboxLibPath:      FirstNonEmpty(os.Getenv("MICROSANDBOX_LIB_PATH"), filepath.Join(repoRoot, "build", "microsandbox", "lib", "libmicrosandbox_go_ffi.so")),
		GuestWorkspacePath:       "/workspace",
		GuestHomePath:            "/root",
		GuestStateRoot:           "/data/state",
		GuestRuntimeRoot:         "/data/runtime",
		GuestLogRoot:             "/data/logs",
		JupyterGuestPort:         8888,
		JupyterProxyBasePath:     "/agent-compose/session",
		SessionStartTimeout:      3 * time.Minute,
		SessionStopTimeout:       30 * time.Second,
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("create session root: %v", err)
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

func newRuntimeSmokeSession(t *testing.T, _ context.Context, config *appconfig.Config, driver string) (*Session, VMState, ProxyState) {
	t.Helper()
	sessionID := "runtime-mount-smoke-" + driver + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	sessionRoot := filepath.Join(config.SessionRoot, sessionID)
	session := &Session{
		Summary: SessionSummary{
			ID:            sessionID,
			Driver:        driver,
			GuestImage:    DefaultGuestImageForDriver(config, driver),
			RuntimeRef:    sessionID,
			WorkspacePath: filepath.Join(sessionRoot, "workspace"),
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		},
		EnvItems: []SessionEnvVar{{Name: "SMOKE_MARKER", Value: "/data/state/runtime-mount-smoke.txt"}},
	}
	if _, err := prepareRuntimeMountManifest(config, session, driver); err != nil {
		t.Fatalf("prepareRuntimeMountManifest returned error: %v", err)
	}
	vmState := VMState{
		Driver:      driver,
		Mode:        driver,
		BoxName:     sessionID,
		Image:       session.Summary.GuestImage,
		RuntimeHome: RuntimeHomeForDriver(config, driver),
	}
	proxyState := ProxyState{
		ProxyPath: "/jupyter/" + sessionID + "/lab",
		GuestHost: "127.0.0.1",
		GuestPort: config.JupyterGuestPort,
	}
	return session, vmState, proxyState
}

func assertDirectoryOnlyRuntimeSmokeManifest(t *testing.T, session *Session, driver string) {
	t.Helper()
	manifest, err := loadDirectoryRuntimeMountManifest(session, driver)
	if err != nil {
		t.Fatalf("loadDirectoryRuntimeMountManifest returned error: %v", err)
	}
	mounts := map[string]string{}
	for _, mount := range manifest.Mounts {
		mounts[mount.GuestPath] = mount.HostPath
	}
	if mounts[DirectoryOnlyGuestSessionPath] != HostSessionDir(session) {
		t.Fatalf("session mount = %q, want %q", mounts[DirectoryOnlyGuestSessionPath], HostSessionDir(session))
	}
	for _, guestPath := range []string{"/root/.claude.json", "/root/.gitconfig"} {
		if mounts[guestPath] != "" {
			t.Fatalf("%s unexpectedly mounted as file source %s", guestPath, mounts[guestPath])
		}
	}
}

func assertRuntimeSmokeHomeFiles(t *testing.T, ctx context.Context, runtime BoxRuntime, session *Session, vmState VMState) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	markerPath := filepath.Join(HostSessionDir(session), "state", "runtime-mount-smoke.txt")
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
	hostGitconfig := filepath.Join(HostSessionHome(session), ".gitconfig")
	if _, err := os.Stat(hostGitconfig); err != nil {
		t.Fatalf("host gitconfig missing after guest startup: %v", err)
	}
	homeMarkerPath := filepath.Join(HostSessionHome(session), ".agent-compose-smoke-home")
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
