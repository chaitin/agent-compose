package dockerdriver

import (
	appconfig "agent-compose/pkg/config"
	drivermount "agent-compose/pkg/driver/mount"
	. "agent-compose/pkg/driver/types"
	"context"
	"path/filepath"
	"strings"
	"testing"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
	networkapi "github.com/docker/docker/api/types/network"
)

func TestDockerRuntimeBindRuntimeMountSourceUsesHostSessionRoot(t *testing.T) {
	config := &appconfig.Config{
		SessionRoot:           "/data/sessions",
		DockerHostSessionRoot: "/srv/agent-compose/sessions",
	}
	runtime := &dockerRuntime{config: config}

	got, err := runtime.bindRuntimeMountSource(context.Background(), nil, "/data/sessions/session-1/workspace")
	if err != nil {
		t.Fatalf("bindRuntimeMountSource returned error: %v", err)
	}
	want := filepath.Join("/srv/agent-compose/sessions", "session-1", "workspace")
	if got != want {
		t.Fatalf("bindRuntimeMountSource returned %q, want %q", got, want)
	}
}

func TestDockerRuntimeContainerEnvUsesRuntimeWorkspaceVariable(t *testing.T) {
	runtime := &dockerRuntime{config: &appconfig.Config{
		GuestWorkspacePath: "/workspace",
		GuestHomePath:      "/root",
		GuestStateRoot:     "/data/state",
		GuestRuntimeRoot:   "/data/runtime",
	}}
	session := &Session{Summary: SessionSummary{ID: "session-1"}}
	env := runtime.containerEnv(session, ProxyState{Token: "token-1"})
	envMap := map[string]string{}
	for _, item := range env {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			t.Fatalf("env item missing '=': %q", item)
		}
		envMap[name] = value
	}
	for _, key := range []string{"HOME", "SESSION_WORKSPACE"} {
		if _, ok := envMap[key]; ok {
			t.Fatalf("container env still contains %s: %#v", key, envMap)
		}
	}
	for key, want := range map[string]string{
		"WORKSPACE":     "/workspace",
		"STATE_ROOT":    "/data/state",
		"RUNTIME_ROOT":  "/data/runtime",
		"SESSION_ID":    "session-1",
		"JUPYTER_TOKEN": "token-1",
	} {
		if got := envMap[key]; got != want {
			t.Fatalf("container env %s = %q, want %q", key, got, want)
		}
	}
}

func TestDockerRuntimeBindRuntimeMountSourceKeepsSourceWithoutHostRoot(t *testing.T) {
	config := &appconfig.Config{SessionRoot: "/data/sessions"}
	runtime := &dockerRuntime{config: config}

	got, err := runtime.bindRuntimeMountSource(context.Background(), nil, "/data/sessions/session-1/workspace")
	if err != nil {
		t.Fatalf("bindRuntimeMountSource returned error: %v", err)
	}
	if got != "/data/sessions/session-1/workspace" {
		t.Fatalf("bindRuntimeMountSource returned %q, want original source", got)
	}
}

func TestDockerRuntimeBindRuntimeMountSourceRejectsOutsideSessionRoot(t *testing.T) {
	config := &appconfig.Config{
		SessionRoot:           "/data/sessions",
		DockerHostSessionRoot: "/srv/agent-compose/sessions",
	}
	runtime := &dockerRuntime{config: config}

	if _, err := runtime.bindRuntimeMountSource(context.Background(), nil, "/data/other/session-1/workspace"); err == nil {
		t.Fatalf("bindRuntimeMountSource returned nil error for path outside session root")
	}
}

func TestRebasePathUnderRoot(t *testing.T) {
	got, err := rebasePathUnderRoot("/data/sessions/session-1", "/data", "/host/data")
	if err != nil {
		t.Fatalf("rebasePathUnderRoot returned error: %v", err)
	}
	want := filepath.Join("/host/data", "sessions", "session-1")
	if got != want {
		t.Fatalf("rebasePathUnderRoot returned %q, want %q", got, want)
	}
}

func TestRebasePathUnderRootPreservesWindowsHostRoot(t *testing.T) {
	got, err := rebasePathUnderRoot("/data/sessions/session-1/workspace", "/data/sessions", `E:\program\agent-compose-main\data\agent-compose\sessions`)
	if err != nil {
		t.Fatalf("rebasePathUnderRoot returned error: %v", err)
	}
	want := `E:\program\agent-compose-main\data\agent-compose\sessions\session-1\workspace`
	if got != want {
		t.Fatalf("rebasePathUnderRoot returned %q, want %q", got, want)
	}
}

func TestRebasePathUnderRootPreservesWindowsHostRootWithSlashes(t *testing.T) {
	got, err := rebasePathUnderRoot("/data/sessions/session-1/workspace", "/data/sessions", "E:/program/agent-compose-main/data/agent-compose/sessions")
	if err != nil {
		t.Fatalf("rebasePathUnderRoot returned error: %v", err)
	}
	want := "E:/program/agent-compose-main/data/agent-compose/sessions/session-1/workspace"
	if got != want {
		t.Fatalf("rebasePathUnderRoot returned %q, want %q", got, want)
	}
}

func TestRebasePathUnderRootPreservesWindowsUNCHostRoot(t *testing.T) {
	got, err := rebasePathUnderRoot("/data/sessions/session-1/workspace", "/data/sessions", `\\server\share\agent-compose\sessions`)
	if err != nil {
		t.Fatalf("rebasePathUnderRoot returned error: %v", err)
	}
	want := `\\server\share\agent-compose\sessions\session-1\workspace`
	if got != want {
		t.Fatalf("rebasePathUnderRoot returned %q, want %q", got, want)
	}
}

func TestRebasePathUnderRootDoesNotTreatLinuxBackslashAsWindowsRoot(t *testing.T) {
	got, err := rebasePathUnderRoot("/data/sessions/session-1/workspace", "/data/sessions", `/srv/agent-compose\weird/sessions`)
	if err != nil {
		t.Fatalf("rebasePathUnderRoot returned error: %v", err)
	}
	want := filepath.Join(`/srv/agent-compose\weird/sessions`, "session-1", "workspace")
	if got != want {
		t.Fatalf("rebasePathUnderRoot returned %q, want %q", got, want)
	}
}

func TestDockerRuntimeMountsConsumeManifestAndRebaseEachSource(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{
		SessionRoot:           filepath.Join(root, "sessions"),
		DockerHostSessionRoot: filepath.Join(root, "docker-host-sessions"),
		GuestWorkspacePath:    "/workspace",
		GuestHomePath:         "/root",
		GuestStateRoot:        "/data/state",
		GuestRuntimeRoot:      "/data/runtime",
		GuestLogRoot:          "/data/logs",
	}
	sessionRoot := filepath.Join(config.SessionRoot, "session-1")
	session := testRuntimeMountSession(sessionRoot)
	if _, err := drivermount.PrepareRuntimeMountManifest(config, session, RuntimeDriverDocker); err != nil {
		t.Fatalf("prepareRuntimeMountManifest returned error: %v", err)
	}
	runtime := &dockerRuntime{config: config}

	mounts, err := runtime.dockerRuntimeMounts(context.Background(), nil, session)
	if err != nil {
		t.Fatalf("dockerRuntimeMounts returned error: %v", err)
	}
	got := map[string]string{}
	for _, mount := range mounts {
		if mount.Type != mountapi.TypeBind || mount.ReadOnly {
			t.Fatalf("docker mount = %+v, want writable bind", mount)
		}
		got[mount.Target] = mount.Source
	}
	wantWorkspace := filepath.Join(config.DockerHostSessionRoot, "session-1", "workspace")
	if got["/workspace"] != wantWorkspace {
		t.Fatalf("/workspace source = %q, want %q", got["/workspace"], wantWorkspace)
	}
	wantCodex := filepath.Join(config.DockerHostSessionRoot, "session-1", "home", ".codex")
	if got["/root/.codex"] != wantCodex {
		t.Fatalf("/root/.codex source = %q, want %q", got["/root/.codex"], wantCodex)
	}
	for guestPath, relHostPath := range map[string]string{
		"/root/.claude.json": filepath.Join("home", ".claude.json"),
		"/root/.gitconfig":   filepath.Join("home", ".gitconfig"),
	} {
		wantSource := filepath.Join(config.DockerHostSessionRoot, "session-1", relHostPath)
		if got[guestPath] != wantSource {
			t.Fatalf("%s source = %q, want rebased file source %q", guestPath, got[guestPath], wantSource)
		}
	}
	if _, ok := got["/data"]; ok {
		t.Fatalf("docker mounts still include whole session root target /data: %+v", got)
	}
}

func TestSelectDockerNetworkNamePrefersUserDefinedNetwork(t *testing.T) {
	got, ok := selectDockerNetworkName(containerapi.InspectResponse{
		NetworkSettings: &containerapi.NetworkSettings{
			Networks: map[string]*networkapi.EndpointSettings{
				"bridge":             {},
				"playground_default": {},
				"z_custom":           {},
			},
		},
	})
	if !ok {
		t.Fatalf("selectDockerNetworkName returned ok=false")
	}
	if got != "playground_default" {
		t.Fatalf("selectDockerNetworkName returned %q, want playground_default", got)
	}
}

func TestSelectDockerNetworkNameFallsBackToDefaultNetwork(t *testing.T) {
	got, ok := selectDockerNetworkName(containerapi.InspectResponse{
		NetworkSettings: &containerapi.NetworkSettings{
			Networks: map[string]*networkapi.EndpointSettings{
				"bridge": {},
			},
		},
	})
	if !ok {
		t.Fatalf("selectDockerNetworkName returned ok=false")
	}
	if got != "bridge" {
		t.Fatalf("selectDockerNetworkName returned %q, want bridge", got)
	}
}

func TestSelectDockerNetworkNameReturnsFalseWithoutNetworks(t *testing.T) {
	if got, ok := selectDockerNetworkName(containerapi.InspectResponse{}); ok || got != "" {
		t.Fatalf("selectDockerNetworkName returned %q, %v; want empty false", got, ok)
	}
	if got, ok := selectDockerNetworkName(containerapi.InspectResponse{
		NetworkSettings: &containerapi.NetworkSettings{},
	}); ok || got != "" {
		t.Fatalf("selectDockerNetworkName returned %q, %v; want empty false", got, ok)
	}
}

func TestDockerRuntimeSessionProxyStateUsesContainerNameAndGuestPort(t *testing.T) {
	runtime := &dockerRuntime{config: &appconfig.Config{JupyterGuestPort: 8888}}
	got := runtime.dockerSessionProxyState(&Session{
		Summary: SessionSummary{
			ID:         "session 1",
			RuntimeRef: "runtime-ref",
		},
	}, VMState{}, ProxyState{
		GuestHost: "127.0.0.1",
		HostPort:  39000,
		GuestPort: 9999,
		Token:     "secret",
	})

	if got.GuestHost != "runtime-ref" || got.GuestPort != 8888 {
		t.Fatalf("dockerSessionProxyState target = %s:%d, want runtime-ref:8888", got.GuestHost, got.GuestPort)
	}
	if got.HostPort != 39000 || got.Token != "secret" {
		t.Fatalf("dockerSessionProxyState did not preserve host port/token: %+v", got)
	}
}

func testRuntimeMountSession(root string) *Session {
	return &Session{Summary: SessionSummary{
		ID:            "session-1",
		WorkspacePath: filepath.Join(root, "workspace"),
	}}
}
