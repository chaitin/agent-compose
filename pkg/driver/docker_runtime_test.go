package driver

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

func TestDockerRuntimeBindRuntimeMountSourceUsesHostSandboxRoot(t *testing.T) {
	config := &appconfig.Config{
		SandboxRoot:           "/data/sandboxes",
		DockerHostSandboxRoot: "/srv/agent-compose/sandboxes",
	}
	runtime := &dockerRuntime{config: config}

	got, err := runtime.bindRuntimeMountSource(context.Background(), nil, "/data/sandboxes/session-1/workspace")
	if err != nil {
		t.Fatalf("bindRuntimeMountSource returned error: %v", err)
	}
	want := filepath.Join("/srv/agent-compose/sandboxes", "session-1", "workspace")
	if got != want {
		t.Fatalf("bindRuntimeMountSource returned %q, want %q", got, want)
	}
}

func TestDockerSandboxLookupUsesSandboxLabel(t *testing.T) {
	if dockerSandboxLabelID != "agent-compose.sandbox_id" {
		t.Fatalf("docker sandbox label = %q", dockerSandboxLabelID)
	}
}

func TestDockerExecCollectorMapsStdioStreams(t *testing.T) {
	var streamed []ExecChunk
	collector := &dockerExecCollector{stream: func(chunk ExecChunk) {
		streamed = append(streamed, chunk)
	}}
	collector.writeChunk(ExecChunk{Text: "out"})
	collector.writeChunk(ExecChunk{Text: "err", Stream: StdioStderr})

	if collector.stdout.String() != "out" {
		t.Fatalf("stdout = %q", collector.stdout.String())
	}
	if collector.stderr.String() != "err" {
		t.Fatalf("stderr = %q", collector.stderr.String())
	}
	if collector.output.String() != "outerr" {
		t.Fatalf("output = %q", collector.output.String())
	}
	want := []ExecChunk{{Text: "out"}, {Text: "err", Stream: StdioStderr}}
	if len(streamed) != len(want) {
		t.Fatalf("streamed chunks = %#v", streamed)
	}
	for i := range want {
		if streamed[i] != want[i] {
			t.Fatalf("streamed[%d] = %#v, want %#v", i, streamed[i], want[i])
		}
	}
}

func TestDockerRuntimeInteractionCapabilities(t *testing.T) {
	runtime := &dockerRuntime{}
	caps := runtime.InteractionCapabilities()
	if !caps.NativeExec || !caps.Stdin || !caps.StdinEOF || !caps.TTY || !caps.Resize {
		t.Fatalf("docker capabilities missing command attach support: %#v", caps)
	}
	if caps.Signal {
		t.Fatalf("docker Signal capability = true, want false until native process signal support exists")
	}
	if caps.Artifacts {
		t.Fatalf("docker Artifacts capability = true, want false for native attach")
	}
	if err := caps.ValidateStartSpec(RuntimeDriverDocker, RuntimeStartSpec{
		Kind:        RuntimeOperationCommand,
		AttachStdin: true,
		TTY:         true,
		Rows:        24,
		Cols:        80,
	}); err != nil {
		t.Fatalf("ValidateStartSpec() error = %v", err)
	}
	err := caps.ValidateStartSpec(RuntimeDriverDocker, RuntimeStartSpec{
		Kind:        RuntimeOperationCommand,
		ArtifactDir: "/tmp/artifacts",
	})
	if !errors.Is(err, ErrRuntimeInteractionUnsupported) {
		t.Fatalf("ValidateStartSpec() artifact error = %v, want unsupported", err)
	}
}

func TestDockerCommandExecOptionsFromRuntimeStartSpec(t *testing.T) {
	options, err := dockerCommandExecOptions(RuntimeStartSpec{
		Kind:        RuntimeOperationCommand,
		AttachStdin: true,
		TTY:         true,
		Rows:        40,
		Cols:        120,
		Cwd:         "/workspace",
		Env:         map[string]string{"BASE": "1", "OVERRIDE": "outer"},
		Command: &RuntimeCommandSpec{
			Command: "bash",
			Args:    []string{"-lc", "printf ok"},
			Env:     map[string]string{"EXTRA": "2", "OVERRIDE": "inner"},
			Cwd:     "/workspace/project",
		},
	}, "/default")
	if err != nil {
		t.Fatalf("dockerCommandExecOptions() error = %v", err)
	}
	if !options.AttachStdin || !options.AttachStdout || options.AttachStderr || !options.Tty {
		t.Fatalf("exec attach flags = %+v", options)
	}
	if options.ConsoleSize == nil || *options.ConsoleSize != [2]uint{40, 120} {
		t.Fatalf("ConsoleSize = %#v, want [40 120]", options.ConsoleSize)
	}
	if !reflect.DeepEqual(options.Cmd, []string{"bash", "-lc", "printf ok"}) {
		t.Fatalf("Cmd = %#v", options.Cmd)
	}
	if !reflect.DeepEqual(options.Env, []string{"BASE=1", "EXTRA=2", "OVERRIDE=inner"}) {
		t.Fatalf("Env = %#v", options.Env)
	}
	if options.WorkingDir != "/workspace/project" {
		t.Fatalf("WorkingDir = %q", options.WorkingDir)
	}
}

func TestDockerCommandExecOptionsNonTTYAttachesStderr(t *testing.T) {
	options, err := dockerCommandExecOptions(RuntimeStartSpec{
		Command: &RuntimeCommandSpec{Command: "cat"},
	}, "/default")
	if err != nil {
		t.Fatalf("dockerCommandExecOptions() error = %v", err)
	}
	if options.AttachStdin || !options.AttachStdout || !options.AttachStderr || options.Tty {
		t.Fatalf("non-TTY exec attach flags = %+v", options)
	}
	if options.WorkingDir != "/default" {
		t.Fatalf("WorkingDir = %q, want /default", options.WorkingDir)
	}
	if options.ConsoleSize != nil {
		t.Fatalf("ConsoleSize = %#v, want nil", options.ConsoleSize)
	}
}

func TestDockerInteractionWriterProjectsFramesAndFiltersStderr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interaction := &dockerCommandInteraction{
		ctx:    ctx,
		output: make(chan RuntimeOutputFrame, 4),
	}

	stdoutWriter := &dockerInteractionWriter{interaction: interaction, stream: StdioStdout}
	if _, err := stdoutWriter.Write([]byte("out\n")); err != nil {
		t.Fatalf("stdout Write() error = %v", err)
	}
	frame := mustRecvDockerInteractionFrame(t, interaction)
	if frame.Type != RuntimeOutputStdout || string(frame.Data) != "out\n" {
		t.Fatalf("stdout frame = %#v", frame)
	}

	stderrWriter := &dockerInteractionWriter{
		interaction: interaction,
		stream:      StdioStderr,
		filter:      newExecOutputFilter(),
	}
	if _, err := stderrWriter.Write([]byte("libcontainer::process::init::process seccomp not available, unable to set seccomp privileges!\n")); err != nil {
		t.Fatalf("ignored stderr Write() error = %v", err)
	}
	if _, err := stderrWriter.Write([]byte("err")); err != nil {
		t.Fatalf("stderr Write() error = %v", err)
	}
	stderrWriter.finish()
	frame = mustRecvDockerInteractionFrame(t, interaction)
	if frame.Type != RuntimeOutputStderr || string(frame.Data) != "err" {
		t.Fatalf("stderr frame = %#v", frame)
	}
	select {
	case frame := <-interaction.output:
		t.Fatalf("unexpected extra frame = %#v", frame)
	default:
	}
}

func mustRecvDockerInteractionFrame(t *testing.T, interaction *dockerCommandInteraction) RuntimeOutputFrame {
	t.Helper()
	select {
	case frame := <-interaction.output:
		return frame
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for docker interaction frame")
		return RuntimeOutputFrame{}
	}
}

func TestDockerRuntimeContainerEnvUsesRuntimeWorkspaceVariable(t *testing.T) {
	runtime := &dockerRuntime{config: &appconfig.Config{
		GuestWorkspacePath: "/workspace",
		GuestHomePath:      "/root",
		GuestStateRoot:     "/data/state",
		GuestRuntimeRoot:   "/data/runtime",
	}}
	session := &Sandbox{Summary: SandboxSummary{ID: "session-1"}}
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
		"SANDBOX_ID":    "session-1",
		"JUPYTER_TOKEN": "token-1",
	} {
		if got := envMap[key]; got != want {
			t.Fatalf("container env %s = %q, want %q", key, got, want)
		}
	}
}

func TestDockerSandboxHostConfigEnablesInit(t *testing.T) {
	hostConfig := dockerSandboxHostConfig(
		[]mountapi.Mount{{Type: mountapi.TypeBind, Source: "/host/workspace", Target: "/workspace"}},
		nil,
		containerapi.NetworkMode("bridge"),
	)
	if hostConfig.Init == nil || !*hostConfig.Init {
		t.Fatalf("docker sandbox host config Init = %v, want true", hostConfig.Init)
	}
	if hostConfig.AutoRemove {
		t.Fatalf("docker sandbox host config AutoRemove = true, want false")
	}
	if hostConfig.NetworkMode != containerapi.NetworkMode("bridge") || len(hostConfig.Mounts) != 1 {
		t.Fatalf("docker sandbox host config = %+v", hostConfig)
	}
}

func TestSandboxStopContextTimeoutAddsDockerAPIMargin(t *testing.T) {
	stopTimeout := 30 * time.Second
	if got := SandboxStopContextTimeout(RuntimeDriverDocker, stopTimeout); got != 35*time.Second {
		t.Fatalf("docker stop context timeout = %s, want 35s", got)
	}
	if got := SandboxStopContextTimeout(RuntimeDriverBoxlite, stopTimeout); got != stopTimeout {
		t.Fatalf("boxlite stop context timeout = %s, want %s", got, stopTimeout)
	}
	if got := SandboxStopContextTimeout(RuntimeDriverDocker, 0); got != 0 {
		t.Fatalf("zero docker stop context timeout = %s, want 0", got)
	}
}

func TestDockerStatsFromResponseMapsStableMetrics(t *testing.T) {
	startedAt := time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC)
	sampledAt := startedAt.Add(90 * time.Second)
	stats := dockerStatsFromResponse(
		&Sandbox{Summary: SandboxSummary{ID: "session-1", Driver: RuntimeDriverDocker}},
		VMState{},
		containerapi.InspectResponse{ContainerJSONBase: &containerapi.ContainerJSONBase{State: &containerapi.State{StartedAt: startedAt.Format(time.RFC3339Nano)}}},
		containerapi.StatsResponse{
			Read: sampledAt,
			CPUStats: containerapi.CPUStats{
				CPUUsage:    containerapi.CPUUsage{TotalUsage: 300, PercpuUsage: []uint64{1, 2}},
				SystemUsage: 1000,
				OnlineCPUs:  2,
			},
			PreCPUStats: containerapi.CPUStats{
				CPUUsage:    containerapi.CPUUsage{TotalUsage: 100},
				SystemUsage: 500,
			},
			MemoryStats: containerapi.MemoryStats{Usage: 256, Limit: 1024},
			Networks: map[string]containerapi.NetworkStats{
				"eth0": {RxBytes: 10, TxBytes: 20},
				"eth1": {RxBytes: 30, TxBytes: 40},
			},
			BlkioStats: containerapi.BlkioStats{IoServiceBytesRecursive: []containerapi.BlkioStatEntry{
				{Op: "Read", Value: 100},
				{Op: "Write", Value: 200},
				{Op: "read", Value: 50},
			}},
		},
	)
	if stats.SandboxID != "session-1" || stats.Driver != RuntimeDriverDocker {
		t.Fatalf("stats identity = %#v", stats)
	}
	assertMetricValue(t, stats.CPUPercent, MetricStatusOK, MetricUnitPercent, 80)
	assertMetricValue(t, stats.MemoryUsageBytes, MetricStatusOK, MetricUnitBytes, 256)
	assertMetricValue(t, stats.MemoryLimitBytes, MetricStatusOK, MetricUnitBytes, 1024)
	assertMetricValue(t, stats.MemoryPercent, MetricStatusOK, MetricUnitPercent, 25)
	assertMetricValue(t, stats.NetworkRxBytes, MetricStatusOK, MetricUnitBytes, 40)
	assertMetricValue(t, stats.NetworkTxBytes, MetricStatusOK, MetricUnitBytes, 60)
	assertMetricValue(t, stats.BlockReadBytes, MetricStatusOK, MetricUnitBytes, 150)
	assertMetricValue(t, stats.BlockWriteBytes, MetricStatusOK, MetricUnitBytes, 200)
	assertMetricValue(t, stats.UptimeSeconds, MetricStatusOK, MetricUnitSeconds, 90)
}

func TestDockerRuntimeBindRuntimeMountSourceKeepsSourceWithoutHostRoot(t *testing.T) {
	config := &appconfig.Config{SandboxRoot: "/data/sandboxes"}
	runtime := &dockerRuntime{config: config}

	got, err := runtime.bindRuntimeMountSource(context.Background(), nil, "/data/sandboxes/session-1/workspace")
	if err != nil {
		t.Fatalf("bindRuntimeMountSource returned error: %v", err)
	}
	if got != "/data/sandboxes/session-1/workspace" {
		t.Fatalf("bindRuntimeMountSource returned %q, want original source", got)
	}
}

func TestDockerRuntimeBindRuntimeMountSourceRejectsOutsideSandboxRoot(t *testing.T) {
	config := &appconfig.Config{
		SandboxRoot:           "/data/sandboxes",
		DockerHostSandboxRoot: "/srv/agent-compose/sandboxes",
	}
	runtime := &dockerRuntime{config: config}

	if _, err := runtime.bindRuntimeMountSource(context.Background(), nil, "/data/other/session-1/workspace"); err == nil {
		t.Fatalf("bindRuntimeMountSource returned nil error for path outside sandbox root")
	}
}

func TestRebasePathUnderRoot(t *testing.T) {
	got, err := rebasePathUnderRoot("/data/sandboxes/session-1", "/data", "/host/data")
	if err != nil {
		t.Fatalf("rebasePathUnderRoot returned error: %v", err)
	}
	want := filepath.Join("/host/data", "sandboxes", "session-1")
	if got != want {
		t.Fatalf("rebasePathUnderRoot returned %q, want %q", got, want)
	}
}

func TestRebasePathUnderRootPreservesWindowsHostRoot(t *testing.T) {
	got, err := rebasePathUnderRoot("/data/sandboxes/session-1/workspace", "/data/sandboxes", `E:\program\agent-compose-main\data\agent-compose\sandboxes`)
	if err != nil {
		t.Fatalf("rebasePathUnderRoot returned error: %v", err)
	}
	want := `E:\program\agent-compose-main\data\agent-compose\sandboxes\session-1\workspace`
	if got != want {
		t.Fatalf("rebasePathUnderRoot returned %q, want %q", got, want)
	}
}

func TestRebasePathUnderRootPreservesWindowsHostRootWithSlashes(t *testing.T) {
	got, err := rebasePathUnderRoot("/data/sandboxes/session-1/workspace", "/data/sandboxes", "E:/program/agent-compose-main/data/agent-compose/sandboxes")
	if err != nil {
		t.Fatalf("rebasePathUnderRoot returned error: %v", err)
	}
	want := "E:/program/agent-compose-main/data/agent-compose/sandboxes/session-1/workspace"
	if got != want {
		t.Fatalf("rebasePathUnderRoot returned %q, want %q", got, want)
	}
}

func TestRebasePathUnderRootPreservesWindowsUNCHostRoot(t *testing.T) {
	got, err := rebasePathUnderRoot("/data/sandboxes/session-1/workspace", "/data/sandboxes", `\\server\share\agent-compose\sandboxes`)
	if err != nil {
		t.Fatalf("rebasePathUnderRoot returned error: %v", err)
	}
	want := `\\server\share\agent-compose\sandboxes\session-1\workspace`
	if got != want {
		t.Fatalf("rebasePathUnderRoot returned %q, want %q", got, want)
	}
}

func TestRebasePathUnderRootDoesNotTreatLinuxBackslashAsWindowsRoot(t *testing.T) {
	got, err := rebasePathUnderRoot("/data/sandboxes/session-1/workspace", "/data/sandboxes", `/srv/agent-compose\weird/sandboxes`)
	if err != nil {
		t.Fatalf("rebasePathUnderRoot returned error: %v", err)
	}
	want := filepath.Join(`/srv/agent-compose\weird/sandboxes`, "session-1", "workspace")
	if got != want {
		t.Fatalf("rebasePathUnderRoot returned %q, want %q", got, want)
	}
}

func TestDockerRuntimeMountsConsumeManifestAndRebaseEachSource(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{
		SandboxRoot:           filepath.Join(root, "sandboxes"),
		DockerHostSandboxRoot: filepath.Join(root, "docker-host-sandboxes"),
		GuestWorkspacePath:    "/workspace",
		GuestHomePath:         "/root",
		GuestStateRoot:        "/data/state",
		GuestRuntimeRoot:      "/data/runtime",
		GuestLogRoot:          "/data/logs",
	}
	sandboxRoot := filepath.Join(config.SandboxRoot, "session-1")
	session := testRuntimeMountSandbox(sandboxRoot)
	if _, err := prepareRuntimeMountManifest(config, session, RuntimeDriverDocker); err != nil {
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
	wantWorkspace := filepath.Join(config.DockerHostSandboxRoot, "session-1", "workspace")
	if got["/workspace"] != wantWorkspace {
		t.Fatalf("/workspace source = %q, want %q", got["/workspace"], wantWorkspace)
	}
	wantCodex := filepath.Join(config.DockerHostSandboxRoot, "session-1", "home", ".codex")
	if got["/root/.codex"] != wantCodex {
		t.Fatalf("/root/.codex source = %q, want %q", got["/root/.codex"], wantCodex)
	}
	for guestPath, relHostPath := range map[string]string{
		"/root/.claude.json": filepath.Join("home", ".claude.json"),
		"/root/.gitconfig":   filepath.Join("home", ".gitconfig"),
	} {
		wantSource := filepath.Join(config.DockerHostSandboxRoot, "session-1", relHostPath)
		if got[guestPath] != wantSource {
			t.Fatalf("%s source = %q, want rebased file source %q", guestPath, got[guestPath], wantSource)
		}
	}
	if _, ok := got["/data"]; ok {
		t.Fatalf("docker mounts still include whole sandbox root target /data: %+v", got)
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

func TestDockerJupyterPortConfigRequestsAutomaticLoopbackBinding(t *testing.T) {
	exposedPorts, portBindings := dockerJupyterPortConfig(9999)
	port := nat.Port("9999/tcp")
	if _, ok := exposedPorts[port]; !ok {
		t.Fatalf("exposed ports = %#v, want %s", exposedPorts, port)
	}
	bindings := portBindings[port]
	if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || bindings[0].HostPort != "" {
		t.Fatalf("port bindings = %#v, want automatic loopback binding", portBindings)
	}
}

func TestDockerSandboxPortConfigCombinesJupyterAndNetworkBindings(t *testing.T) {
	sandbox := &Sandbox{Network: &SandboxNetwork{Bindings: []SandboxPortBinding{
		{HostIP: "10.254.1.1", HostPort: 32000, GuestPort: 8080, Protocol: "tcp", Publisher: NetworkPublisherDocker},
		{HostIP: "10.254.2.1", HostPort: 32001, GuestPort: 8080, Protocol: "tcp", Publisher: NetworkPublisherDocker},
	}}}
	exposedPorts, portBindings, err := dockerSandboxPortConfig(sandbox, ProxyState{Enabled: true, GuestPort: 9999})
	if err != nil {
		t.Fatalf("dockerSandboxPortConfig() error = %v", err)
	}
	if len(exposedPorts) != 2 {
		t.Fatalf("exposed ports = %#v", exposedPorts)
	}
	bindings := portBindings[nat.Port("8080/tcp")]
	if len(bindings) != 2 || bindings[0].HostIP != "10.254.1.1" || bindings[1].HostPort != "32001" {
		t.Fatalf("network port bindings = %#v", bindings)
	}
	jupyter := portBindings[nat.Port("9999/tcp")]
	if len(jupyter) != 1 || jupyter[0].HostIP != "127.0.0.1" || jupyter[0].HostPort != "" {
		t.Fatalf("jupyter port binding = %#v", jupyter)
	}
}

func TestDockerSandboxPrimaryNetworkModeDoesNotInheritHost(t *testing.T) {
	if got := dockerSandboxPrimaryNetworkMode("host"); got != containerapi.NetworkMode("default") {
		t.Fatalf("dockerSandboxPrimaryNetworkMode(host) = %q", got)
	}
	if got := dockerSandboxPrimaryNetworkMode("project_default"); got != containerapi.NetworkMode("project_default") {
		t.Fatalf("dockerSandboxPrimaryNetworkMode(project_default) = %q", got)
	}
}

func TestDockerJupyterHostPortSelectsValidLoopbackBinding(t *testing.T) {
	containerInfo := dockerInspectWithJupyterBindings("container-1", "/runtime-ref", 9999, []nat.PortBinding{
		{HostIP: "0.0.0.0", HostPort: "41000"},
		{HostIP: "127.0.0.1", HostPort: "invalid"},
		{HostIP: "::1", HostPort: "42000"},
		{HostIP: "127.0.0.1", HostPort: "43000"},
	})
	got, err := dockerJupyterHostPort(containerInfo, 9999)
	if err != nil {
		t.Fatalf("dockerJupyterHostPort returned error: %v", err)
	}
	if got != 42000 {
		t.Fatalf("dockerJupyterHostPort = %d, want first valid loopback port 42000", got)
	}
}

func TestDockerJupyterHostPortRejectsInvalidBindings(t *testing.T) {
	tests := []struct {
		name          string
		containerInfo containerapi.InspectResponse
		guestPort     int
	}{
		{name: "zero guest port", containerInfo: dockerInspectWithJupyterBindings("container-1", "/runtime-ref", 9999, []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "42000"}})},
		{name: "missing network settings", containerInfo: containerapi.InspectResponse{ContainerJSONBase: &containerapi.ContainerJSONBase{ID: "container-1"}}, guestPort: 9999},
		{name: "missing binding", containerInfo: dockerInspectWithJupyterBindings("container-1", "/runtime-ref", 8888, []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "42000"}}), guestPort: 9999},
		{name: "zero port", containerInfo: dockerInspectWithJupyterBindings("container-1", "/runtime-ref", 9999, []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "0"}}), guestPort: 9999},
		{name: "invalid port", containerInfo: dockerInspectWithJupyterBindings("container-1", "/runtime-ref", 9999, []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "abc"}}), guestPort: 9999},
		{name: "non-loopback", containerInfo: dockerInspectWithJupyterBindings("container-1", "/runtime-ref", 9999, []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: "42000"}}), guestPort: 9999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := dockerJupyterHostPort(tt.containerInfo, tt.guestPort); err == nil {
				t.Fatal("dockerJupyterHostPort returned nil error")
			}
		})
	}
}

func TestDockerRuntimeSandboxProxyStateUsesContainerNameAndGuestPort(t *testing.T) {
	runtime := &dockerRuntime{config: &appconfig.Config{JupyterGuestPort: 8888}}
	sandbox := &Sandbox{
		Summary: SandboxSummary{
			ID:         "session 1",
			RuntimeRef: "runtime-ref",
		},
	}
	state := ProxyState{
		GuestHost: "127.0.0.1",
		HostPort:  39000, // stale persisted value
		GuestPort: 9999,
		Token:     "secret",
		Enabled:   true,
	}
	containerInfo := dockerInspectWithJupyterBindings("container-1", "/actual-runtime-ref", 9999, []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "42000"}})

	hostState, err := runtime.dockerSandboxProxyState(sandbox, VMState{}, state, containerInfo, false)
	if err != nil {
		t.Fatalf("host dockerSandboxProxyState returned error: %v", err)
	}
	if hostState.GuestHost != "127.0.0.1" || hostState.HostPort != 42000 {
		t.Fatalf("host daemon proxy state = %+v, want 127.0.0.1:42000", hostState)
	}
	containerState, err := runtime.dockerSandboxProxyState(sandbox, VMState{}, state, containerInfo, true)
	if err != nil {
		t.Fatalf("container dockerSandboxProxyState returned error: %v", err)
	}
	if containerState.GuestHost != "actual-runtime-ref" || containerState.GuestPort != 9999 || containerState.HostPort != 42000 {
		t.Fatalf("container daemon proxy state = %+v, want actual-runtime-ref:9999 with host port 42000", containerState)
	}
	if containerState.Token != "secret" {
		t.Fatalf("dockerSandboxProxyState did not preserve token: %+v", containerState)
	}
}

func dockerInspectWithJupyterBindings(id, name string, guestPort int, bindings []nat.PortBinding) containerapi.InspectResponse {
	networkSettings := &containerapi.NetworkSettings{}
	networkSettings.Ports = nat.PortMap{nat.Port(strconv.Itoa(guestPort) + "/tcp"): bindings}
	return containerapi.InspectResponse{
		ContainerJSONBase: &containerapi.ContainerJSONBase{ID: id, Name: name},
		NetworkSettings:   networkSettings,
	}
}

func TestValidateLegacyDockerRecreateRequiresUUIDAndPersistedMounts(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{SandboxRoot: root}
	runtime := &dockerRuntime{config: config}
	sandbox := &Sandbox{Summary: SandboxSummary{
		ID:            "04c587f2-01c3-487b-b933-524ce4332235",
		Driver:        RuntimeDriverDocker,
		GuestImage:    "guest:latest",
		WorkspacePath: filepath.Join(root, "04c587f2-01c3-487b-b933-524ce4332235", "workspace"),
	}}
	if _, err := prepareRuntimeMountManifest(config, sandbox, RuntimeDriverDocker); err != nil {
		t.Fatalf("prepare manifest: %v", err)
	}
	state := VMState{Driver: RuntimeDriverDocker, Image: "guest:latest", StoppedAt: time.Now()}
	if err := runtime.validateLegacyDockerRecreate(sandbox, state); err != nil {
		t.Fatalf("legacy recreate validation: %v", err)
	}
	sandbox.Summary.ID = strings.Repeat("a", 64)
	if err := runtime.validateLegacyDockerRecreate(sandbox, state); err == nil || !strings.Contains(err.Error(), "legacy UUID") {
		t.Fatalf("new sandbox validation error = %v", err)
	}
}
