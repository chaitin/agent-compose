package driver

import (
	appconfig "agent-compose/pkg/config"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agent-compose/pkg/networks"

	cerrdefs "github.com/containerd/errdefs"
	dockertypes "github.com/docker/docker/api/types"
	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	mountapi "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
)

const (
	dockerSandboxLabelPrefix  = "agent-compose"
	dockerSandboxLabelID      = dockerSandboxLabelPrefix + ".sandbox_id"
	dockerSandboxLabelDriver  = dockerSandboxLabelPrefix + ".driver"
	dockerSandboxLabelProject = dockerSandboxLabelPrefix + ".project_id"

	dockerStopAPIMargin             = 5 * time.Second
	dockerStopFallbackActionTimeout = 5 * time.Second
)

type dockerRuntime struct {
	config *appconfig.Config
}

type dockerDaemonTopology struct {
	networkMode   containerapi.NetworkMode
	containerized bool
	kind          dockerDaemonTopologyKind
	detail        string
}

type dockerDaemonTopologyKind string

const (
	dockerDaemonNative          dockerDaemonTopologyKind = "native-host"
	dockerDaemonContainerHost   dockerDaemonTopologyKind = "container-host-network"
	dockerDaemonContainerBridge dockerDaemonTopologyKind = "container-bridge-network"
	dockerDaemonUnknown         dockerDaemonTopologyKind = "remote-or-unknown"
)

type dockerExecCollector struct {
	stream ExecStreamWriter
	filter *execOutputFilter
	stdout bytes.Buffer
	stderr bytes.Buffer
	output bytes.Buffer
}

type dockerExecWriter struct {
	collector *dockerExecCollector
	stream    StdioStream
}

type dockerCommandInteraction struct {
	ctx          context.Context
	cancel       context.CancelFunc
	docker       *dockerRuntime
	dockerClient *client.Client
	attachResp   dockertypes.HijackedResponse
	execID       string
	operationID  string
	tty          bool
	attachStdin  bool
	startedAt    time.Time

	output chan RuntimeOutputFrame
	done   chan struct{}

	writeMu       sync.Mutex
	closeSendOnce sync.Once
	result        RuntimeResult
	err           error
}

type dockerInteractionWriter struct {
	interaction *dockerCommandInteraction
	stream      StdioStream
	filter      *execOutputFilter
}

func newDockerRuntime(config *appconfig.Config) (SandboxRuntime, error) {
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
	if NormalizeStdioStream(chunk.Stream) == StdioStderr {
		c.stderr.WriteString(chunk.Text)
		return
	}
	c.stdout.WriteString(chunk.Text)
}

func (w *dockerExecWriter) Write(p []byte) (int, error) {
	if w == nil || w.collector == nil {
		return len(p), nil
	}
	w.collector.writeChunk(ExecChunk{Text: string(p), Stream: w.stream})
	return len(p), nil
}

func (r *dockerRuntime) EnsureSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState, proxyState ProxyState) (SandboxVMInfo, error) {
	dockerClient, err := r.newClient()
	if err != nil {
		return SandboxVMInfo{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	topology := r.dockerDaemonTopology(ctx, dockerClient)
	networkPlan, err := r.networkPlanForSandbox(sandbox, topology, dockerClient)
	if err != nil {
		return SandboxVMInfo{}, err
	}
	if err := ensureDockerManagedNetworks(ctx, dockerClient, networkPlan); err != nil {
		return SandboxVMInfo{}, err
	}
	networkMode := topology.networkMode
	if networkPlan.Mode == networks.ModeMultiNetwork {
		networkMode = containerapi.NetworkMode(networkPlan.Attachments[0].RuntimeName)
	}
	containerInfo, _, err := r.getOrCreateContainer(ctx, dockerClient, sandbox, vmState, proxyState, networkMode, networkPlan)
	if err != nil {
		return SandboxVMInfo{}, err
	}
	if err := ensureDockerContainerAttachments(ctx, dockerClient, containerInfo, networkPlan); err != nil {
		return SandboxVMInfo{}, err
	}
	if containerInfo.State == nil || !containerInfo.State.Running {
		if err := dockerClient.ContainerStart(ctx, containerInfo.ID, containerapi.StartOptions{}); err != nil {
			return SandboxVMInfo{}, fmt.Errorf("start docker container %s: %w", containerInfo.ID, err)
		}
	}
	if topology.containerized {
		if err := ensureDockerContainerNetwork(ctx, dockerClient, containerInfo, string(topology.networkMode)); err != nil {
			return SandboxVMInfo{}, err
		}
	}
	containerInfo, err = dockerClient.ContainerInspect(ctx, containerInfo.ID)
	if err != nil {
		return SandboxVMInfo{}, fmt.Errorf("inspect started docker container %s: %w", containerInfo.ID, err)
	}
	proxyState, err = r.dockerSandboxProxyState(sandbox, vmState, proxyState, containerInfo, topology.containerized)
	if err != nil {
		return SandboxVMInfo{}, err
	}
	networkState := dockerSandboxNetworkState(containerInfo, networkPlan, sandbox.NetworkIntent != nil)
	if !jupyterEnabled(proxyState) {
		return SandboxVMInfo{BoxID: containerInfo.ID, ProxyState: &proxyState, NetworkState: networkState}, nil
	}

	readyCtx, cancel := context.WithTimeout(ctx, r.config.JupyterReadyTimeout)
	readyErr := waitForJupyterProxy(readyCtx, proxyState)
	cancel()
	if readyErr != nil {
		if logText := readSandboxJupyterLog(sandbox); jupyterLogIndicatesReady(logText) {
			return SandboxVMInfo{BoxID: containerInfo.ID, JupyterURL: jupyterDirectURL(proxyState), ProxyState: &proxyState, NetworkState: networkState}, nil
		}
		if logText := readSandboxJupyterLog(sandbox); logText != "" {
			return SandboxVMInfo{}, fmt.Errorf("%w\nGuest log:\n%s", readyErr, logText)
		}
		if logText, err := r.readContainerLogs(ctx, dockerClient, containerInfo.ID); err == nil && strings.TrimSpace(logText) != "" {
			return SandboxVMInfo{}, fmt.Errorf("%w\nContainer log:\n%s", readyErr, strings.TrimSpace(logText))
		}
		return SandboxVMInfo{}, readyErr
	}

	return SandboxVMInfo{BoxID: containerInfo.ID, JupyterURL: jupyterDirectURL(proxyState), ProxyState: &proxyState, NetworkState: networkState}, nil
}

func (r *dockerRuntime) IsSandboxAlive(ctx context.Context, sandbox *Sandbox, vmState VMState) (bool, error) {
	dockerClient, err := r.newClient()
	if err != nil {
		return false, err
	}
	defer func() { _ = dockerClient.Close() }()

	containerInfo, ok, err := r.findContainer(ctx, dockerClient, sandbox, vmState)
	if err != nil || !ok {
		return false, err
	}
	return containerInfo.State != nil && containerInfo.State.Running, nil
}

func (r *dockerRuntime) StopSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState) (bool, error) {
	dockerClient, err := r.newClient()
	if err != nil {
		return false, err
	}
	defer func() { _ = dockerClient.Close() }()

	containerInfo, ok, err := r.findContainer(ctx, dockerClient, sandbox, vmState)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	if containerInfo.State != nil && containerInfo.State.Running {
		timeoutSeconds := int(math.Ceil(r.config.SandboxStopTimeout.Seconds()))
		if timeoutSeconds < 0 {
			timeoutSeconds = 0
		}
		if err := dockerClient.ContainerStop(ctx, containerInfo.ID, containerapi.StopOptions{Timeout: &timeoutSeconds}); err != nil {
			if isDockerNotFound(err) {
				return true, nil
			}
			if stopped, inspectErr := r.containerStoppedAfterStopError(containerInfo.ID); inspectErr != nil {
				return false, fmt.Errorf("stop docker container %s: %w; inspect after stop failure: %v", containerInfo.ID, err, inspectErr)
			} else if !stopped {
				return false, fmt.Errorf("stop docker container %s: %w", containerInfo.ID, err)
			}
		}
	}
	return false, nil
}

func (r *dockerRuntime) RemoveSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState) error {
	dockerClient, err := r.newClient()
	if err != nil {
		return err
	}
	defer func() { _ = dockerClient.Close() }()

	containerInfo, ok, err := r.findContainer(ctx, dockerClient, sandbox, vmState)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	removeCtx, cancel := dockerFallbackContextIfDone(ctx)
	defer cancel()
	if err := dockerClient.ContainerRemove(removeCtx, containerInfo.ID, containerapi.RemoveOptions{Force: true}); err != nil && !isDockerNotFound(err) {
		return fmt.Errorf("remove docker container %s: %w", containerInfo.ID, err)
	}
	return nil
}

func (r *dockerRuntime) Exec(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec) (ExecResult, error) {
	return r.execWithStream(ctx, sandbox, vmState, spec, nil)
}

func (r *dockerRuntime) ExecStream(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	return r.execWithStream(ctx, sandbox, vmState, spec, stream)
}

func (r *dockerRuntime) InteractionCapabilities() RuntimeInteractionCapabilities {
	return RuntimeInteractionCapabilities{
		NativeExec: true,
		Stdin:      true,
		StdinEOF:   true,
		TTY:        true,
		Resize:     true,
		Signal:     false,
		Artifacts:  false,
	}
}

func (r *dockerRuntime) OpenInteraction(ctx context.Context, sandbox *Sandbox, vmState VMState, spec RuntimeStartSpec) (RuntimeInteraction, error) {
	if err := r.InteractionCapabilities().ValidateStartSpec(RuntimeDriverDocker, spec); err != nil {
		return nil, err
	}
	if normalizeRuntimeOperationKind(spec.Kind) != RuntimeOperationCommand {
		return nil, NewRuntimeInteractionUnsupportedError(RuntimeDriverDocker, spec, RuntimeCapabilityNativeExec, "docker native attach only supports command interactions")
	}
	command := RuntimeCommandSpec{}
	if spec.Command != nil {
		command = *spec.Command
	}
	if strings.TrimSpace(command.Command) == "" {
		return nil, fmt.Errorf("docker interaction command is required")
	}

	childCtx, cancel := context.WithCancel(ctx)
	if spec.TimeoutMs > 0 {
		childCtx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutMs)*time.Millisecond)
	}
	dockerClient, err := r.newClient()
	if err != nil {
		cancel()
		return nil, err
	}
	closeClient := true
	defer func() {
		if closeClient {
			_ = dockerClient.Close()
		}
	}()

	containerInfo, ok, err := r.findContainer(childCtx, dockerClient, sandbox, vmState)
	if err != nil {
		cancel()
		return nil, err
	}
	if !ok || containerInfo.State == nil || !containerInfo.State.Running {
		cancel()
		return nil, fmt.Errorf("docker container for sandbox %s is not running", sandbox.Summary.ID)
	}

	execOptions, err := dockerCommandExecOptions(spec, r.config.GuestWorkspacePath)
	if err != nil {
		cancel()
		return nil, err
	}
	execResp, err := dockerClient.ContainerExecCreate(childCtx, containerInfo.ID, execOptions)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create docker exec for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	attachResp, err := dockerClient.ContainerExecAttach(childCtx, execResp.ID, containerapi.ExecAttachOptions{
		Tty:         spec.TTY,
		ConsoleSize: dockerConsoleSize(spec.Rows, spec.Cols),
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("attach docker exec for sandbox %s: %w", sandbox.Summary.ID, err)
	}

	closeClient = false
	interaction := &dockerCommandInteraction{
		ctx:          childCtx,
		cancel:       cancel,
		docker:       r,
		dockerClient: dockerClient,
		attachResp:   attachResp,
		execID:       execResp.ID,
		operationID:  spec.OperationID,
		tty:          spec.TTY,
		attachStdin:  spec.AttachStdin,
		startedAt:    time.Now(),
		output:       make(chan RuntimeOutputFrame, 64),
		done:         make(chan struct{}),
	}
	interaction.emit(RuntimeOutputFrame{Type: RuntimeOutputStarted, StartedAt: interaction.startedAt})
	go interaction.run()
	go interaction.closeOnContextDone()
	return interaction, nil
}

func (r *dockerRuntime) Stats(ctx context.Context, sandbox *Sandbox, vmState VMState) (SandboxStats, error) {
	dockerClient, err := r.newClient()
	if err != nil {
		return SandboxStats{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	containerInfo, ok, err := r.findContainer(ctx, dockerClient, sandbox, vmState)
	if err != nil {
		return SandboxStats{}, err
	}
	if !ok || containerInfo.State == nil || !containerInfo.State.Running {
		return SandboxStats{}, fmt.Errorf("docker container for sandbox %s is not running", sandbox.Summary.ID)
	}
	reader, err := dockerClient.ContainerStatsOneShot(ctx, containerInfo.ID)
	if err != nil {
		return SandboxStats{}, fmt.Errorf("read docker stats for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	defer func() { _ = reader.Body.Close() }()
	var response containerapi.StatsResponse
	if err := json.NewDecoder(reader.Body).Decode(&response); err != nil {
		return SandboxStats{}, fmt.Errorf("decode docker stats for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	return dockerStatsFromResponse(sandbox, vmState, containerInfo, response), nil
}

func (r *dockerRuntime) execWithStream(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	command := strings.TrimSpace(spec.Command)
	if command == "" {
		return ExecResult{}, fmt.Errorf("docker exec command is required")
	}

	dockerClient, err := r.newClient()
	if err != nil {
		return ExecResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	containerInfo, ok, err := r.findContainer(ctx, dockerClient, sandbox, vmState)
	if err != nil {
		return ExecResult{}, err
	}
	if !ok || containerInfo.State == nil || !containerInfo.State.Running {
		return ExecResult{}, fmt.Errorf("docker container for sandbox %s is not running", sandbox.Summary.ID)
	}

	execResp, err := dockerClient.ContainerExecCreate(ctx, containerInfo.ID, containerapi.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          append([]string{command}, spec.Args...),
		Env:          dockerEnvList(spec.Env),
		WorkingDir:   firstNonEmpty(spec.Cwd, r.config.GuestWorkspacePath),
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("create docker exec for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	attachResp, err := dockerClient.ContainerExecAttach(ctx, execResp.ID, containerapi.ExecAttachOptions{})
	if err != nil {
		return ExecResult{}, fmt.Errorf("attach docker exec for sandbox %s: %w", sandbox.Summary.ID, err)
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

	collector := &dockerExecCollector{stream: stream, filter: newExecOutputFilter()}
	_, copyErr := stdcopy.StdCopy(&dockerExecWriter{collector: collector, stream: StdioStdout}, &dockerExecWriter{collector: collector, stream: StdioStderr}, attachResp.Reader)
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

func (i *dockerCommandInteraction) Send(frame RuntimeInputFrame) error {
	switch frame.Type {
	case RuntimeInputStdin:
		if !i.attachStdin {
			return ErrRuntimeInteractionUnsupported
		}
		if len(frame.Data) == 0 {
			return nil
		}
		i.writeMu.Lock()
		defer i.writeMu.Unlock()
		if i.ctx.Err() != nil {
			return i.ctx.Err()
		}
		_, err := i.attachResp.Conn.Write(frame.Data)
		return err
	case RuntimeInputStdinEOF:
		return i.CloseSend()
	case RuntimeInputResize:
		return i.resize(frame.Rows, frame.Cols)
	case RuntimeInputCancel:
		i.cancel()
		i.attachResp.Close()
		return nil
	case RuntimeInputSignal:
		return ErrRuntimeInteractionUnsupported
	default:
		return ErrRuntimeInteractionUnsupported
	}
}

func (i *dockerCommandInteraction) CloseSend() error {
	var err error
	i.closeSendOnce.Do(func() {
		i.writeMu.Lock()
		defer i.writeMu.Unlock()
		err = i.attachResp.CloseWrite()
	})
	return err
}

func (i *dockerCommandInteraction) Recv() (RuntimeOutputFrame, error) {
	frame, ok := <-i.output
	if !ok {
		return RuntimeOutputFrame{}, io.EOF
	}
	return frame, nil
}

func (i *dockerCommandInteraction) Wait() (RuntimeResult, error) {
	<-i.done
	return i.result, i.err
}

func (i *dockerCommandInteraction) resize(rows, cols uint32) error {
	if rows == 0 && cols == 0 {
		return nil
	}
	return i.dockerClient.ContainerExecResize(i.ctx, i.execID, containerapi.ResizeOptions{
		Height: uint(rows),
		Width:  uint(cols),
	})
}

func (i *dockerCommandInteraction) closeOnContextDone() {
	<-i.ctx.Done()
	i.attachResp.Close()
}

func (i *dockerCommandInteraction) run() {
	defer close(i.done)
	defer close(i.output)
	defer i.cancel()
	defer func() { _ = i.dockerClient.Close() }()
	defer i.attachResp.Close()

	copyErr := i.copyOutput()
	var runErr error
	if copyErr != nil && i.ctx.Err() != nil {
		runErr = i.ctx.Err()
	} else if copyErr != nil && !isDockerStreamClosed(copyErr) {
		runErr = copyErr
	}

	exitCode := -1
	if runErr == nil {
		execInfo, err := i.docker.waitForExecExit(i.ctx, i.dockerClient, i.execID)
		if err != nil {
			runErr = err
		} else {
			exitCode = execInfo.ExitCode
		}
	}

	completedAt := time.Now()
	i.result = RuntimeResult{
		OperationID: i.operationID,
		ExitCode:    exitCode,
		Success:     runErr == nil && exitCode == 0,
		StartedAt:   i.startedAt,
		CompletedAt: completedAt,
	}
	if runErr != nil {
		i.err = runErr
		i.result.Error = runErr.Error()
		i.emit(RuntimeOutputFrame{Type: RuntimeOutputError, Error: &RuntimeError{Message: runErr.Error()}})
	}
	i.emit(RuntimeOutputFrame{Type: RuntimeOutputResult, Result: &i.result})
}

func (i *dockerCommandInteraction) copyOutput() error {
	if i.tty {
		_, err := io.Copy(&dockerInteractionWriter{interaction: i, stream: StdioStdout}, i.attachResp.Reader)
		return err
	}
	stderrWriter := &dockerInteractionWriter{
		interaction: i,
		stream:      StdioStderr,
		filter:      newExecOutputFilter(),
	}
	_, err := stdcopy.StdCopy(
		&dockerInteractionWriter{interaction: i, stream: StdioStdout},
		stderrWriter,
		i.attachResp.Reader,
	)
	stderrWriter.finish()
	return err
}

func (i *dockerCommandInteraction) emit(frame RuntimeOutputFrame) {
	select {
	case i.output <- frame:
	case <-i.ctx.Done():
	}
}

func (w *dockerInteractionWriter) Write(p []byte) (int, error) {
	if len(p) == 0 || w == nil || w.interaction == nil {
		return len(p), nil
	}
	chunk := ExecChunk{Text: string(p), Stream: w.stream}
	if w.filter != nil {
		w.filter.Write(chunk, w.emitChunk)
		return len(p), nil
	}
	w.emitChunk(chunk)
	return len(p), nil
}

func (w *dockerInteractionWriter) finish() {
	if w != nil && w.filter != nil {
		w.filter.Finish(w.emitChunk)
	}
}

func (w *dockerInteractionWriter) emitChunk(chunk ExecChunk) {
	frameType := RuntimeOutputStdout
	if NormalizeStdioStream(chunk.Stream) == StdioStderr {
		frameType = RuntimeOutputStderr
	}
	w.interaction.emit(RuntimeOutputFrame{Type: frameType, Data: []byte(chunk.Text)})
}

func dockerCommandExecOptions(spec RuntimeStartSpec, defaultCwd string) (containerapi.ExecOptions, error) {
	command := RuntimeCommandSpec{}
	if spec.Command != nil {
		command = *spec.Command
	}
	commandName := strings.TrimSpace(command.Command)
	if commandName == "" {
		return containerapi.ExecOptions{}, fmt.Errorf("docker interaction command is required")
	}
	env := command.Env
	if len(spec.Env) > 0 {
		env = mergeStringMaps(spec.Env, command.Env)
	}
	return containerapi.ExecOptions{
		AttachStdin:  spec.AttachStdin,
		AttachStdout: true,
		AttachStderr: !spec.TTY,
		Tty:          spec.TTY,
		ConsoleSize:  dockerConsoleSize(spec.Rows, spec.Cols),
		Cmd:          append([]string{commandName}, command.Args...),
		Env:          dockerEnvList(env),
		WorkingDir:   firstNonEmpty(command.Cwd, spec.Cwd, defaultCwd),
	}, nil
}

func dockerConsoleSize(rows, cols uint32) *[2]uint {
	if rows == 0 && cols == 0 {
		return nil
	}
	return &[2]uint{uint(rows), uint(cols)}
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

func (r *dockerRuntime) getOrCreateContainer(ctx context.Context, dockerClient *client.Client, sandbox *Sandbox, vmState VMState, proxyState ProxyState, networkMode containerapi.NetworkMode, networkPlan networks.Plan) (containerapi.InspectResponse, bool, error) {
	appconfig.ApplyDefaultGuestPaths(r.config)
	if containerInfo, ok, err := r.findContainer(ctx, dockerClient, sandbox, vmState); err != nil {
		return containerapi.InspectResponse{}, false, err
	} else if ok {
		return containerInfo, false, nil
	}
	if !vmState.StoppedAt.IsZero() {
		if err := r.validateLegacyDockerRecreate(sandbox, vmState); err != nil {
			return containerapi.InspectResponse{}, false, fmt.Errorf("docker runtime state for stopped sandbox %s is missing; refusing to recreate it during resume: %w", sandbox.Summary.ID, err)
		}
		slog.Warn("recreating missing legacy docker runtime from persisted mounts; container writable layer is not recoverable", "sandbox_id", sandbox.Summary.ID, "image", firstNonEmpty(vmState.Image, sandbox.Summary.GuestImage))
	}

	name := r.containerName(sandbox, vmState)
	mounts, err := r.dockerRuntimeMounts(ctx, dockerClient, sandbox)
	if err != nil {
		return containerapi.InspectResponse{}, false, err
	}
	var exposedPorts nat.PortSet
	var portBindings nat.PortMap
	cmdText := "tail -f /dev/null"
	if jupyterEnabled(proxyState) {
		exposedPorts, portBindings = dockerJupyterPortConfig(proxyState.GuestPort)
		cmdText = jupyterLaunchCommand(r.config, proxyState, false)
	}
	exposedPorts, portBindings, err = mergeDockerNetworkPortConfig(exposedPorts, portBindings, sandbox.NetworkIntent)
	if err != nil {
		return containerapi.InspectResponse{}, false, fmt.Errorf("configure docker ports for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	containerConfig := &containerapi.Config{
		Image:        resolveSandboxGuestImage(vmState.Image, sandbox.Summary.GuestImage, defaultGuestImageForDriver(r.config, RuntimeDriverDocker)),
		WorkingDir:   r.config.GuestWorkspacePath,
		Env:          r.containerEnv(sandbox, proxyState),
		Entrypoint:   []string{"sh", "-lc"},
		Cmd:          []string{cmdText},
		ExposedPorts: exposedPorts,
		Labels: map[string]string{
			dockerSandboxLabelID:     sandbox.Summary.ID,
			dockerSandboxLabelDriver: RuntimeDriverDocker,
		},
	}
	if sandbox.NetworkIntent != nil {
		containerConfig.Labels[dockerSandboxLabelProject] = strings.TrimSpace(sandbox.NetworkIntent.ProjectID)
	}
	hostConfig := dockerSandboxHostConfig(mounts, portBindings, networkMode)
	createResp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, dockerContainerNetworkingConfig(networkPlan), nil, name)
	if err != nil {
		return containerapi.InspectResponse{}, false, fmt.Errorf("create docker container for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	containerInfo, err := dockerClient.ContainerInspect(ctx, createResp.ID)
	if err != nil {
		return containerapi.InspectResponse{}, false, fmt.Errorf("inspect docker container %s: %w", createResp.ID, err)
	}
	return containerInfo, true, nil
}

func (r *dockerRuntime) validateLegacyDockerRecreate(sandbox *Sandbox, vmState VMState) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is required")
	}
	appconfig.ApplyDefaultGuestPaths(r.config)
	id, err := uuid.Parse(strings.TrimSpace(sandbox.Summary.ID))
	if err != nil || id.String() != strings.ToLower(strings.TrimSpace(sandbox.Summary.ID)) {
		return fmt.Errorf("only canonical legacy UUID sandboxes may be reconstructed")
	}
	if resolveRuntimeDriver(firstNonEmpty(vmState.Driver, sandbox.Summary.Driver)) != RuntimeDriverDocker {
		return fmt.Errorf("legacy sandbox driver is not docker")
	}
	if strings.TrimSpace(firstNonEmpty(vmState.Image, sandbox.Summary.GuestImage)) == "" {
		return fmt.Errorf("legacy sandbox image is missing")
	}
	manifest, err := loadRuntimeMountManifest(sandbox, RuntimeDriverDocker)
	if err != nil {
		return err
	}
	required := map[string]bool{
		r.config.GuestWorkspacePath: false,
		r.config.GuestStateRoot:     false,
		r.config.GuestRuntimeRoot:   false,
		r.config.GuestLogRoot:       false,
	}
	for _, mount := range manifest.Mounts {
		if _, ok := required[mount.GuestPath]; ok {
			required[mount.GuestPath] = true
		}
	}
	for guestPath, present := range required {
		if !present {
			return fmt.Errorf("legacy runtime mount %s is missing", guestPath)
		}
	}
	return nil
}

func (r *dockerRuntime) containerStoppedAfterStopError(containerID string) (bool, error) {
	inspectCtx, cancel := context.WithTimeout(context.Background(), dockerStopFallbackActionTimeout)
	defer cancel()
	dockerClient, err := r.newClient()
	if err != nil {
		return false, err
	}
	defer func() { _ = dockerClient.Close() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		containerInfo, err := dockerClient.ContainerInspect(inspectCtx, containerID)
		if isDockerNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if containerInfo.State == nil || !containerInfo.State.Running {
			return true, nil
		}
		select {
		case <-inspectCtx.Done():
			return false, nil
		case <-ticker.C:
		}
	}
}

func dockerFallbackContextIfDone(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.Background(), dockerStopFallbackActionTimeout)
}

func dockerSandboxHostConfig(mounts []mountapi.Mount, portBindings nat.PortMap, networkMode containerapi.NetworkMode) *containerapi.HostConfig {
	useInit := true
	return &containerapi.HostConfig{
		Mounts:       mounts,
		PortBindings: portBindings,
		AutoRemove:   false,
		NetworkMode:  networkMode,
		Init:         &useInit,
	}
}

func dockerJupyterPortConfig(guestPort int) (nat.PortSet, nat.PortMap) {
	port := nat.Port(strconv.Itoa(guestPort) + "/tcp")
	return nat.PortSet{port: struct{}{}}, nat.PortMap{
		port: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: ""}},
	}
}

func SandboxStopContextTimeout(driver string, stopTimeout time.Duration) time.Duration {
	if driver != RuntimeDriverDocker || stopTimeout <= 0 {
		return stopTimeout
	}
	return stopTimeout + dockerStopAPIMargin
}

func (r *dockerRuntime) dockerDaemonTopology(ctx context.Context, dockerClient *client.Client) dockerDaemonTopology {
	containerInfo, ok, err := r.dockerSelfContainer(ctx, dockerClient)
	if err != nil {
		slog.Warn("failed to inspect current docker container network; falling back to default docker network", "error", err)
		return dockerDaemonTopology{networkMode: containerapi.NetworkMode("default"), kind: dockerDaemonUnknown, detail: err.Error()}
	}
	if !ok {
		slog.Warn("current process is not running in an inspectable docker container; falling back to default docker network")
		if _, markerErr := os.Stat("/.dockerenv"); markerErr == nil {
			return dockerDaemonTopology{networkMode: containerapi.NetworkMode("default"), kind: dockerDaemonUnknown, detail: "process is containerized but its Docker container cannot be inspected"}
		}
		return dockerDaemonTopology{networkMode: containerapi.NetworkMode("default"), kind: dockerDaemonNative}
	}
	if containerInfo.HostConfig != nil && containerInfo.HostConfig.NetworkMode.IsHost() {
		return dockerDaemonTopology{networkMode: containerapi.NetworkMode("default"), kind: dockerDaemonContainerHost}
	}
	networkName, ok := selectDockerNetworkName(containerInfo)
	if !ok {
		return dockerDaemonTopology{networkMode: containerapi.NetworkMode("default"), containerized: true, kind: dockerDaemonUnknown, detail: "daemon container has no usable bridge network"}
	}
	return dockerDaemonTopology{networkMode: containerapi.NetworkMode(networkName), containerized: true, kind: dockerDaemonContainerBridge}
}

func (r *dockerRuntime) dockerSelfContainer(ctx context.Context, dockerClient *client.Client) (containerapi.InspectResponse, bool, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return containerapi.InspectResponse{}, false, nil
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return containerapi.InspectResponse{}, false, nil
	}

	containerInfo, err := dockerClient.ContainerInspect(ctx, hostname)
	if err != nil {
		if !isDockerNotFound(err) {
			return containerapi.InspectResponse{}, false, fmt.Errorf("inspect current docker container %s: %w", hostname, err)
		}
		mountInfo, readErr := os.ReadFile("/proc/self/mountinfo")
		if readErr != nil {
			return containerapi.InspectResponse{}, false, nil
		}
		containerID := dockerContainerIDFromMountInfo(mountInfo)
		if containerID == "" || containerID == hostname {
			return containerapi.InspectResponse{}, false, nil
		}
		containerInfo, err = dockerClient.ContainerInspect(ctx, containerID)
		if err != nil {
			if isDockerNotFound(err) {
				return containerapi.InspectResponse{}, false, nil
			}
			return containerapi.InspectResponse{}, false, fmt.Errorf("inspect current docker container %s from mountinfo: %w", containerID, err)
		}
	}
	return containerInfo, true, nil
}

func dockerContainerIDFromMountInfo(data []byte) string {
	const marker = "/containers/"
	for _, field := range strings.Fields(string(data)) {
		markerIndex := strings.Index(field, marker)
		if markerIndex < 0 {
			continue
		}
		remainder := field[markerIndex+len(marker):]
		containerID, _, _ := strings.Cut(remainder, "/")
		if len(containerID) != 64 {
			continue
		}
		valid := true
		for _, char := range containerID {
			if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
				valid = false
				break
			}
		}
		if valid {
			return containerID
		}
	}
	return ""
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

func (r *dockerRuntime) dockerRuntimeMounts(ctx context.Context, dockerClient *client.Client, sandbox *Sandbox) ([]mountapi.Mount, error) {
	manifest, err := loadRuntimeMountManifest(sandbox, RuntimeDriverDocker)
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

	hostRoot := strings.TrimSpace(r.config.DockerHostSandboxRoot)
	if hostRoot != "" {
		return rebasePathUnderRoot(hostPath, r.config.SandboxRoot, hostRoot)
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
	containerInfo, ok, err := r.dockerSelfContainer(ctx, dockerClient)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
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

func (r *dockerRuntime) findContainer(ctx context.Context, dockerClient *client.Client, sandbox *Sandbox, vmState VMState) (containerapi.InspectResponse, bool, error) {
	for _, lookup := range []string{strings.TrimSpace(vmState.BoxID), r.containerName(sandbox, vmState)} {
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
	args.Add("label", dockerSandboxLabelID+"="+sandbox.Summary.ID)
	args.Add("label", dockerSandboxLabelDriver+"="+RuntimeDriverDocker)
	containers, err := dockerClient.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: args})
	if err != nil {
		return containerapi.InspectResponse{}, false, fmt.Errorf("list docker containers for sandbox %s: %w", sandbox.Summary.ID, err)
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

func (r *dockerRuntime) dockerSandboxProxyState(sandbox *Sandbox, vmState VMState, proxyState ProxyState, containerInfo containerapi.InspectResponse, containerizedDaemon bool) (ProxyState, error) {
	if !proxyState.Enabled {
		proxyState.GuestHost = ""
		proxyState.GuestPort = 0
		return proxyState, nil
	}
	hostPort, err := dockerJupyterHostPort(containerInfo, proxyState.GuestPort)
	if err != nil {
		return ProxyState{}, err
	}
	proxyState.HostPort = hostPort
	if !containerizedDaemon {
		proxyState.GuestHost = "127.0.0.1"
		return proxyState, nil
	}
	proxyState.GuestHost = strings.TrimPrefix(strings.TrimSpace(containerInfo.Name), "/")
	if proxyState.GuestHost == "" {
		proxyState.GuestHost = r.containerName(sandbox, vmState)
	}
	return proxyState, nil
}

func dockerJupyterHostPort(containerInfo containerapi.InspectResponse, guestPort int) (int, error) {
	if guestPort <= 0 {
		return 0, fmt.Errorf("docker jupyter guest port must be positive, got %d", guestPort)
	}
	if containerInfo.NetworkSettings == nil {
		return 0, fmt.Errorf("docker container %s has no network settings", containerInfo.ID)
	}
	port := nat.Port(strconv.Itoa(guestPort) + "/tcp")
	bindings, ok := containerInfo.NetworkSettings.Ports[port]
	if !ok || len(bindings) == 0 {
		return 0, fmt.Errorf("docker container %s has no binding for jupyter port %s", containerInfo.ID, port)
	}
	for _, binding := range bindings {
		hostIP := net.ParseIP(strings.TrimSpace(binding.HostIP))
		if hostIP == nil || !hostIP.IsLoopback() {
			continue
		}
		hostPort, err := strconv.Atoi(strings.TrimSpace(binding.HostPort))
		if err != nil || hostPort <= 0 || hostPort > 65535 {
			continue
		}
		return hostPort, nil
	}
	return 0, fmt.Errorf("docker container %s has no valid loopback binding for jupyter port %s", containerInfo.ID, port)
}

func ensureDockerContainerNetwork(ctx context.Context, dockerClient *client.Client, containerInfo containerapi.InspectResponse, networkName string) error {
	networkName = strings.TrimSpace(networkName)
	if networkName == "" || networkName == "default" {
		return nil
	}
	if containerInfo.NetworkSettings != nil {
		if _, ok := containerInfo.NetworkSettings.Networks[networkName]; ok {
			return nil
		}
	}
	if err := dockerClient.NetworkConnect(ctx, networkName, containerInfo.ID, nil); err != nil {
		return fmt.Errorf("connect docker container %s to daemon network %s: %w", containerInfo.ID, networkName, err)
	}
	return nil
}

func (r *dockerRuntime) containerName(sandbox *Sandbox, vmState VMState) string {
	return firstNonEmpty(strings.TrimSpace(vmState.BoxName), strings.TrimSpace(sandbox.Summary.RuntimeRef), "agent-compose-"+sanitizeDockerContainerName(sandbox.Summary.ID))
}

func (r *dockerRuntime) containerEnv(sandbox *Sandbox, proxyState ProxyState) []string {
	appconfig.ApplyDefaultGuestPaths(r.config)
	env := sandboxEnvMap(sandbox.EnvItems, sandbox.RuntimeEnvItems)
	if env == nil {
		env = map[string]string{}
	}
	env["GOPATH"] = "/usr/local/go"
	env["PATH"] = "/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	env["SANDBOX_ID"] = sandbox.Summary.ID
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

func dockerStatsFromResponse(sandbox *Sandbox, vmState VMState, containerInfo containerapi.InspectResponse, response containerapi.StatsResponse) SandboxStats {
	sandboxID := ""
	driverName := RuntimeDriverDocker
	if sandbox != nil {
		sandboxID = sandbox.Summary.ID
		driverName = firstNonEmpty(sandbox.Summary.Driver, driverName)
	}
	sampledAt := response.Read
	if sampledAt.IsZero() {
		sampledAt = time.Now().UTC()
	}
	stats := SandboxStats{
		SandboxID:        sandboxID,
		Driver:           firstNonEmpty(driverName, vmState.Driver, RuntimeDriverDocker),
		SampledAt:        sampledAt.UTC(),
		CPUPercent:       metricOK(dockerCPUPercent(response), MetricUnitPercent),
		MemoryUsageBytes: metricOK(float64(response.MemoryStats.Usage), MetricUnitBytes),
		MemoryLimitBytes: metricOK(float64(response.MemoryStats.Limit), MetricUnitBytes),
		MemoryPercent:    metricUnknown(MetricUnitPercent, "memory limit is unknown"),
		NetworkRxBytes:   metricOK(float64(dockerNetworkRxBytes(response)), MetricUnitBytes),
		NetworkTxBytes:   metricOK(float64(dockerNetworkTxBytes(response)), MetricUnitBytes),
		BlockReadBytes:   metricOK(float64(dockerBlockReadBytes(response)), MetricUnitBytes),
		BlockWriteBytes:  metricOK(float64(dockerBlockWriteBytes(response)), MetricUnitBytes),
		UptimeSeconds:    metricUnknown(MetricUnitSeconds, "container start time is unknown"),
	}
	if response.MemoryStats.Limit > 0 {
		stats.MemoryPercent = metricOK(float64(response.MemoryStats.Usage)/float64(response.MemoryStats.Limit)*100, MetricUnitPercent)
	}
	if startedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(containerInfo.State.StartedAt)); err == nil && !startedAt.IsZero() {
		uptime := sampledAt.Sub(startedAt)
		if uptime < 0 {
			uptime = 0
		}
		stats.UptimeSeconds = metricOK(uptime.Seconds(), MetricUnitSeconds)
	}
	return stats
}

func dockerCPUPercent(response containerapi.StatsResponse) float64 {
	cpuDelta := float64(response.CPUStats.CPUUsage.TotalUsage - response.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(response.CPUStats.SystemUsage - response.PreCPUStats.SystemUsage)
	onlineCPUs := float64(response.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(response.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpuDelta <= 0 || systemDelta <= 0 || onlineCPUs <= 0 {
		return 0
	}
	return cpuDelta / systemDelta * onlineCPUs * 100
}

func dockerNetworkRxBytes(response containerapi.StatsResponse) uint64 {
	var total uint64
	for _, network := range response.Networks {
		total += network.RxBytes
	}
	return total
}

func dockerNetworkTxBytes(response containerapi.StatsResponse) uint64 {
	var total uint64
	for _, network := range response.Networks {
		total += network.TxBytes
	}
	return total
}

func dockerBlockReadBytes(response containerapi.StatsResponse) uint64 {
	var total uint64
	for _, entry := range response.BlkioStats.IoServiceBytesRecursive {
		if strings.EqualFold(entry.Op, "read") {
			total += entry.Value
		}
	}
	return total
}

func dockerBlockWriteBytes(response containerapi.StatsResponse) uint64 {
	var total uint64
	for _, entry := range response.BlkioStats.IoServiceBytesRecursive {
		if strings.EqualFold(entry.Op, "write") {
			total += entry.Value
		}
	}
	return total
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
		return "sandbox"
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
		return "sandbox"
	}
	return result
}
