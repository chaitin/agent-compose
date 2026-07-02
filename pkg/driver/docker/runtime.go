package dockerdriver

import (
	appconfig "agent-compose/pkg/config"
	boxlitedriver "agent-compose/pkg/driver/boxlite"
	driverimage "agent-compose/pkg/driver/image"
	execfilter "agent-compose/pkg/driver/internal/execfilter"
	drivermount "agent-compose/pkg/driver/mount"
	. "agent-compose/pkg/driver/types"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	mountapi "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
)

const (
	dockerSessionLabelPrefix = "agent-compose"
	dockerSessionLabelID     = dockerSessionLabelPrefix + ".session_id"
	dockerSessionLabelDriver = dockerSessionLabelPrefix + ".driver"
)

type dockerRuntime struct {
	config *appconfig.Config
}

type dockerExecCollector struct {
	stream ExecStreamWriter
	filter *execfilter.ExecOutputFilter
	stdout bytes.Buffer
	stderr bytes.Buffer
	output bytes.Buffer
}

type dockerExecWriter struct {
	collector *dockerExecCollector
	isStderr  bool
}

func NewRuntime(config *appconfig.Config) (BoxRuntime, error) {
	return &dockerRuntime{config: config}, nil
}

func (c *dockerExecCollector) writeChunk(chunk ExecChunk) {
	if c.filter == nil {
		c.appendChunk(chunk)
		return
	}
	c.filter.Write(chunk, c.appendChunk)
}

func (c *dockerExecCollector) finish() {
	if c.filter == nil {
		return
	}
	c.filter.Finish(c.appendChunk)
}

func (c *dockerExecCollector) appendChunk(chunk ExecChunk) {
	if chunk.Text == "" {
		return
	}
	c.output.WriteString(chunk.Text)
	if c.stream != nil {
		c.stream(chunk)
	}
	if chunk.IsStderr {
		c.stderr.WriteString(chunk.Text)
		return
	}
	c.stdout.WriteString(chunk.Text)
}

func (w *dockerExecWriter) Write(p []byte) (int, error) {
	if w == nil || w.collector == nil {
		return len(p), nil
	}
	w.collector.writeChunk(ExecChunk{Text: string(p), IsStderr: w.isStderr})
	return len(p), nil
}

func (r *dockerRuntime) EnsureSession(ctx context.Context, session *Session, vmState VMState, proxyState ProxyState) (SessionVMInfo, error) {
	dockerClient, err := r.newClient()
	if err != nil {
		return SessionVMInfo{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	proxyState = r.dockerSessionProxyState(session, vmState, proxyState)
	containerInfo, _, err := r.getOrCreateContainer(ctx, dockerClient, session, vmState, proxyState)
	if err != nil {
		return SessionVMInfo{}, err
	}
	if containerInfo.State == nil || !containerInfo.State.Running {
		if err := dockerClient.ContainerStart(ctx, containerInfo.ID, containerapi.StartOptions{}); err != nil {
			return SessionVMInfo{}, fmt.Errorf("start docker container %s: %w", containerInfo.ID, err)
		}
	}

	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	readyErr := boxlitedriver.WaitForJupyterProxy(readyCtx, proxyState)
	cancel()
	if readyErr != nil {
		if logText := boxlitedriver.ReadSessionJupyterLog(session); boxlitedriver.JupyterLogIndicatesReady(logText) {
			return SessionVMInfo{BoxID: containerInfo.ID, JupyterURL: boxlitedriver.JupyterDirectURL(proxyState), ProxyState: &proxyState}, nil
		}
		if logText := boxlitedriver.ReadSessionJupyterLog(session); logText != "" {
			return SessionVMInfo{}, fmt.Errorf("%w\nGuest log:\n%s", readyErr, logText)
		}
		if logText, err := r.readContainerLogs(ctx, dockerClient, containerInfo.ID); err == nil && strings.TrimSpace(logText) != "" {
			return SessionVMInfo{}, fmt.Errorf("%w\nContainer log:\n%s", readyErr, strings.TrimSpace(logText))
		}
		return SessionVMInfo{}, readyErr
	}

	return SessionVMInfo{BoxID: containerInfo.ID, JupyterURL: boxlitedriver.JupyterDirectURL(proxyState), ProxyState: &proxyState}, nil
}

func (r *dockerRuntime) StopSession(ctx context.Context, session *Session, vmState VMState) (bool, error) {
	dockerClient, err := r.newClient()
	if err != nil {
		return false, err
	}
	defer func() { _ = dockerClient.Close() }()

	containerInfo, ok, err := r.findContainer(ctx, dockerClient, session, vmState)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	if containerInfo.State != nil && containerInfo.State.Running {
		timeoutSeconds := int(math.Ceil(r.config.SessionStopTimeout.Seconds()))
		if timeoutSeconds < 0 {
			timeoutSeconds = 0
		}
		if err := dockerClient.ContainerStop(ctx, containerInfo.ID, containerapi.StopOptions{Timeout: &timeoutSeconds}); err != nil && !isDockerNotFound(err) {
			return false, fmt.Errorf("stop docker container %s: %w", containerInfo.ID, err)
		}
	}
	if err := dockerClient.ContainerRemove(ctx, containerInfo.ID, containerapi.RemoveOptions{Force: true}); err != nil && !isDockerNotFound(err) {
		return false, fmt.Errorf("remove docker container %s: %w", containerInfo.ID, err)
	}
	return false, nil
}

func (r *dockerRuntime) Exec(ctx context.Context, session *Session, vmState VMState, spec ExecSpec) (ExecResult, error) {
	return r.execWithStream(ctx, session, vmState, spec, nil)
}

func (r *dockerRuntime) ExecStream(ctx context.Context, session *Session, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	return r.execWithStream(ctx, session, vmState, spec, stream)
}

func (r *dockerRuntime) execWithStream(ctx context.Context, session *Session, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	command := strings.TrimSpace(spec.Command)
	if command == "" {
		return ExecResult{}, fmt.Errorf("docker exec command is required")
	}

	dockerClient, err := r.newClient()
	if err != nil {
		return ExecResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	containerInfo, ok, err := r.findContainer(ctx, dockerClient, session, vmState)
	if err != nil {
		return ExecResult{}, err
	}
	if !ok || containerInfo.State == nil || !containerInfo.State.Running {
		return ExecResult{}, fmt.Errorf("docker container for session %s is not running", session.Summary.ID)
	}

	execResp, err := dockerClient.ContainerExecCreate(ctx, containerInfo.ID, containerapi.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          append([]string{command}, spec.Args...),
		Env:          dockerEnvList(spec.Env),
		WorkingDir:   FirstNonEmpty(spec.Cwd, r.config.GuestWorkspacePath),
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("create docker exec for session %s: %w", session.Summary.ID, err)
	}
	attachResp, err := dockerClient.ContainerExecAttach(ctx, execResp.ID, containerapi.ExecAttachOptions{})
	if err != nil {
		return ExecResult{}, fmt.Errorf("attach docker exec for session %s: %w", session.Summary.ID, err)
	}
	defer attachResp.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			attachResp.Close()
		case <-done:
		}
	}()

	collector := &dockerExecCollector{stream: stream, filter: execfilter.New()}
	_, copyErr := stdcopy.StdCopy(&dockerExecWriter{collector: collector}, &dockerExecWriter{collector: collector, isStderr: true}, attachResp.Reader)
	collector.finish()
	if copyErr != nil && ctx.Err() != nil {
		return ExecResult{}, ctx.Err()
	}
	if copyErr != nil && !isDockerStreamClosed(copyErr) {
		return ExecResult{}, copyErr
	}

	execInfo, err := r.waitForExecExit(ctx, dockerClient, execResp.ID)
	if err != nil {
		return ExecResult{}, err
	}
	result := ExecResult{
		ExitCode: execInfo.ExitCode,
		Stdout:   collector.stdout.String(),
		Stderr:   collector.stderr.String(),
		Output:   collector.output.String(),
	}
	result.Success = result.ExitCode == 0
	return result, nil
}

func (r *dockerRuntime) newClient() (*client.Client, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("connect docker daemon: verify docker.sock is accessible: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := dockerClient.Ping(ctx); err != nil {
		_ = dockerClient.Close()
		return nil, fmt.Errorf("docker daemon is unavailable: verify docker.sock is accessible: %w", err)
	}
	return dockerClient, nil
}

func (r *dockerRuntime) getOrCreateContainer(ctx context.Context, dockerClient *client.Client, session *Session, vmState VMState, proxyState ProxyState) (containerapi.InspectResponse, bool, error) {
	appconfig.ApplyDefaultGuestPaths(r.config)
	if containerInfo, ok, err := r.findContainer(ctx, dockerClient, session, vmState); err != nil {
		return containerapi.InspectResponse{}, false, err
	} else if ok {
		return containerInfo, false, nil
	}

	name := r.containerName(session, vmState)
	mounts, err := r.dockerRuntimeMounts(ctx, dockerClient, session)
	if err != nil {
		return containerapi.InspectResponse{}, false, err
	}
	networkMode := r.dockerGuestNetworkMode(ctx, dockerClient)
	port := nat.Port(strconv.Itoa(r.config.JupyterGuestPort) + "/tcp")
	containerConfig := &containerapi.Config{
		Image:        driverimage.ResolveSessionGuestImage(vmState.Image, session.Summary.GuestImage, DefaultGuestImageForDriver(r.config, RuntimeDriverDocker)),
		WorkingDir:   r.config.GuestWorkspacePath,
		Env:          r.containerEnv(session, proxyState),
		Entrypoint:   []string{"sh", "-lc"},
		Cmd:          []string{boxlitedriver.JupyterLaunchCommand(r.config, proxyState, false)},
		ExposedPorts: nat.PortSet{port: struct{}{}},
		Labels: map[string]string{
			dockerSessionLabelID:     session.Summary.ID,
			dockerSessionLabelDriver: RuntimeDriverDocker,
		},
	}
	hostConfig := &containerapi.HostConfig{
		Mounts:       mounts,
		PortBindings: nat.PortMap{port: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: strconv.Itoa(proxyState.HostPort)}}},
		AutoRemove:   false,
		NetworkMode:  networkMode,
	}
	createResp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, name)
	if err != nil {
		return containerapi.InspectResponse{}, false, fmt.Errorf("create docker container for session %s: %w", session.Summary.ID, err)
	}
	containerInfo, err := dockerClient.ContainerInspect(ctx, createResp.ID)
	if err != nil {
		return containerapi.InspectResponse{}, false, fmt.Errorf("inspect docker container %s: %w", createResp.ID, err)
	}
	return containerInfo, true, nil
}

func (r *dockerRuntime) dockerGuestNetworkMode(ctx context.Context, dockerClient *client.Client) containerapi.NetworkMode {
	networkName, ok, err := r.dockerNetworkNameFromSelfContainer(ctx, dockerClient)
	if err != nil {
		slog.Warn("failed to inspect current docker container network; falling back to default docker network", "error", err)
		return containerapi.NetworkMode("default")
	}
	if !ok {
		slog.Warn("current process is not running in an inspectable docker container; falling back to default docker network")
		return containerapi.NetworkMode("default")
	}
	return containerapi.NetworkMode(networkName)
}

func (r *dockerRuntime) dockerNetworkNameFromSelfContainer(ctx context.Context, dockerClient *client.Client) (string, bool, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", false, nil
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return "", false, nil
	}

	containerInfo, err := dockerClient.ContainerInspect(ctx, hostname)
	if err != nil {
		if isDockerNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect current docker container %s: %w", hostname, err)
	}
	networkName, ok := selectDockerNetworkName(containerInfo)
	return networkName, ok, nil
}

func selectDockerNetworkName(containerInfo containerapi.InspectResponse) (string, bool) {
	if containerInfo.NetworkSettings == nil || len(containerInfo.NetworkSettings.Networks) == 0 {
		return "", false
	}
	networkNames := make([]string, 0, len(containerInfo.NetworkSettings.Networks))
	for name := range containerInfo.NetworkSettings.Networks {
		name = strings.TrimSpace(name)
		if name != "" {
			networkNames = append(networkNames, name)
		}
	}
	sort.Strings(networkNames)
	for _, name := range networkNames {
		switch name {
		case "bridge", "host", "none":
			continue
		default:
			return name, true
		}
	}
	if len(networkNames) == 0 {
		return "", false
	}
	return networkNames[0], true
}

func (r *dockerRuntime) dockerRuntimeMounts(ctx context.Context, dockerClient *client.Client, session *Session) ([]mountapi.Mount, error) {
	manifest, err := drivermount.LoadRuntimeMountManifest(session, RuntimeDriverDocker)
	if err != nil {
		return nil, err
	}
	mounts := make([]mountapi.Mount, 0, len(manifest.Mounts))
	for _, item := range manifest.Mounts {
		source, err := r.bindRuntimeMountSource(ctx, dockerClient, item.HostPath)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mountapi.Mount{
			Type:     mountapi.TypeBind,
			Source:   source,
			Target:   item.GuestPath,
			ReadOnly: item.ReadOnly,
		})
	}
	return mounts, nil
}

func (r *dockerRuntime) bindRuntimeMountSource(ctx context.Context, dockerClient *client.Client, hostPath string) (string, error) {
	hostPath = filepath.Clean(strings.TrimSpace(hostPath))
	if hostPath == "." || hostPath == "" {
		return "", fmt.Errorf("docker runtime mount source is empty")
	}

	hostRoot := strings.TrimSpace(r.config.DockerHostSessionRoot)
	if hostRoot != "" {
		return rebasePathUnderRoot(hostPath, r.config.SessionRoot, hostRoot)
	}

	if dockerClient != nil {
		if bindPath, ok, err := r.bindRuntimeMountSourceFromSelfContainer(ctx, dockerClient, hostPath); err != nil {
			return "", err
		} else if ok {
			return bindPath, nil
		}
	}

	return hostPath, nil
}

func (r *dockerRuntime) bindRuntimeMountSourceFromSelfContainer(ctx context.Context, dockerClient *client.Client, hostPath string) (string, bool, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", false, nil
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return "", false, nil
	}

	containerInfo, err := dockerClient.ContainerInspect(ctx, hostname)
	if err != nil {
		if isDockerNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect current docker container %s: %w", hostname, err)
	}

	var bestSource string
	var bestDestination string
	for _, mount := range containerInfo.Mounts {
		source := filepath.Clean(strings.TrimSpace(mount.Source))
		destination := filepath.Clean(strings.TrimSpace(mount.Destination))
		if source == "." || source == "" || destination == "." || destination == "" {
			continue
		}
		if _, err := relativePathUnderRoot(hostPath, destination); err != nil {
			continue
		}
		if len(destination) > len(bestDestination) {
			bestSource = source
			bestDestination = destination
		}
	}
	if bestSource == "" {
		return "", false, nil
	}

	bindPath, err := rebasePathUnderRoot(hostPath, bestDestination, bestSource)
	if err != nil {
		return "", false, err
	}
	return bindPath, true, nil
}

func rebasePathUnderRoot(path, oldRoot, newRoot string) (string, error) {
	relativeDir, err := relativePathUnderRoot(path, oldRoot)
	if err != nil {
		return "", err
	}
	return joinDockerHostPath(newRoot, relativeDir), nil
}

func joinDockerHostPath(root, relativePath string) string {
	root = strings.TrimSpace(root)
	relativePath = filepath.Clean(strings.TrimSpace(relativePath))
	if relativePath == "." || relativePath == "" {
		return root
	}
	if isWindowsHostPath(root) && strings.Contains(root, "\\") {
		return strings.TrimRight(root, `\/`) + `\` + strings.ReplaceAll(relativePath, "/", `\`)
	}
	if isWindowsHostPath(root) || strings.Contains(root, "/") {
		return strings.TrimRight(root, "/") + "/" + filepath.ToSlash(relativePath)
	}
	return filepath.Join(root, relativePath)
}

func isWindowsHostPath(path string) bool {
	if strings.HasPrefix(path, `\\`) {
		return true
	}
	if len(path) < 3 {
		return false
	}
	drive := path[0]
	if (drive < 'A' || drive > 'Z') && (drive < 'a' || drive > 'z') {
		return false
	}
	return path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

func relativePathUnderRoot(path, root string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	root = filepath.Clean(strings.TrimSpace(root))
	if path == "." || path == "" || root == "." || root == "" {
		return "", fmt.Errorf("path and root are required")
	}
	relativeDir, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve %s under %s: %w", path, root, err)
	}
	if relativeDir == "." || strings.HasPrefix(relativeDir, ".."+string(filepath.Separator)) || relativeDir == ".." || filepath.IsAbs(relativeDir) {
		return "", fmt.Errorf("path %s is outside root %s", path, root)
	}
	return relativeDir, nil
}

func (r *dockerRuntime) findContainer(ctx context.Context, dockerClient *client.Client, session *Session, vmState VMState) (containerapi.InspectResponse, bool, error) {
	for _, lookup := range []string{strings.TrimSpace(vmState.BoxID), r.containerName(session, vmState)} {
		if lookup == "" {
			continue
		}
		containerInfo, err := dockerClient.ContainerInspect(ctx, lookup)
		if err == nil {
			return containerInfo, true, nil
		}
		if !isDockerNotFound(err) {
			return containerapi.InspectResponse{}, false, fmt.Errorf("inspect docker container %s: %w", lookup, err)
		}
	}

	args := filters.NewArgs()
	args.Add("label", dockerSessionLabelID+"="+session.Summary.ID)
	args.Add("label", dockerSessionLabelDriver+"="+RuntimeDriverDocker)
	containers, err := dockerClient.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: args})
	if err != nil {
		return containerapi.InspectResponse{}, false, fmt.Errorf("list docker containers for session %s: %w", session.Summary.ID, err)
	}
	if len(containers) == 0 {
		return containerapi.InspectResponse{}, false, nil
	}
	containerInfo, err := dockerClient.ContainerInspect(ctx, containers[0].ID)
	if err != nil {
		if isDockerNotFound(err) {
			return containerapi.InspectResponse{}, false, nil
		}
		return containerapi.InspectResponse{}, false, fmt.Errorf("inspect docker container %s: %w", containers[0].ID, err)
	}
	return containerInfo, true, nil
}

func (r *dockerRuntime) dockerSessionProxyState(session *Session, vmState VMState, proxyState ProxyState) ProxyState {
	proxyState.GuestHost = r.containerName(session, vmState)
	proxyState.GuestPort = r.config.JupyterGuestPort
	return proxyState
}

func (r *dockerRuntime) containerName(session *Session, vmState VMState) string {
	return FirstNonEmpty(strings.TrimSpace(vmState.BoxName), strings.TrimSpace(session.Summary.RuntimeRef), "agent-compose-"+sanitizeDockerContainerName(session.Summary.ID))
}

func (r *dockerRuntime) containerEnv(session *Session, proxyState ProxyState) []string {
	appconfig.ApplyDefaultGuestPaths(r.config)
	env := SessionEnvMap(session.EnvItems, session.RuntimeEnvItems)
	if env == nil {
		env = map[string]string{}
	}
	env["GOPATH"] = "/usr/local/go"
	env["PATH"] = "/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	env["SESSION_ID"] = session.Summary.ID
	env["WORKSPACE"] = r.config.GuestWorkspacePath
	env["STATE_ROOT"] = r.config.GuestStateRoot
	env["RUNTIME_ROOT"] = r.config.GuestRuntimeRoot
	env["JUPYTER_TOKEN"] = proxyState.Token
	return dockerEnvList(env)
}

func (r *dockerRuntime) waitForExecExit(ctx context.Context, dockerClient *client.Client, execID string) (containerapi.ExecInspect, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		execInfo, err := dockerClient.ContainerExecInspect(ctx, execID)
		if err != nil {
			return containerapi.ExecInspect{}, fmt.Errorf("inspect docker exec %s: %w", execID, err)
		}
		if !execInfo.Running {
			return execInfo, nil
		}
		select {
		case <-ctx.Done():
			return containerapi.ExecInspect{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *dockerRuntime) readContainerLogs(ctx context.Context, dockerClient *client.Client, containerID string) (string, error) {
	logs, err := dockerClient.ContainerLogs(ctx, containerID, containerapi.LogsOptions{ShowStdout: true, ShowStderr: true, Tail: "200"})
	if err != nil {
		return "", err
	}
	defer func() { _ = logs.Close() }()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logs); err != nil {
		return "", err
	}
	return strings.TrimSpace(stdout.String() + stderr.String()), nil
}

func dockerEnvList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]string, 0, len(keys))
	for _, key := range keys {
		items = append(items, key+"="+env[key])
	}
	return items
}

func isDockerNotFound(err error) bool {
	if err == nil {
		return false
	}
	if cerrdefs.IsNotFound(err) {
		return true
	}
	lowered := strings.ToLower(err.Error())
	return strings.Contains(lowered, "no such container") || strings.Contains(lowered, "404")
}

func isDockerStreamClosed(err error) bool {
	if err == nil {
		return false
	}
	return err == io.EOF || strings.Contains(strings.ToLower(err.Error()), "use of closed network connection")
}

func sanitizeDockerContainerName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "session"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "session"
	}
	return result
}
