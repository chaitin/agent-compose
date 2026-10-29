package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

const (
	// dockerSandboxInitLabel marks a container whose PID 1 is the
	// agent-compose init process. Its value is the protocol handshake: only a
	// container created with this label is controlled over the init socket.
	dockerSandboxInitLabel = "agent-compose.sandbox_init"
	// sandboxInitGuestControlDir is the bind target inside the sandbox.
	sandboxInitGuestControlDir = "/run/agent-compose"
	// sandboxInitGuestSocket is the control socket path inside the sandbox.
	sandboxInitGuestSocket = "/run/agent-compose/init.sock"
)

// sandboxInitEnabled reports whether new Docker sandboxes should use init.
func (r *dockerRuntime) sandboxInitEnabled() bool {
	return r != nil && r.config != nil && r.config.SandboxInitEnabled
}

func (r *dockerRuntime) sandboxInitBinary() string {
	if r != nil && r.config != nil && strings.TrimSpace(r.config.SandboxInitBinary) != "" {
		return strings.TrimSpace(r.config.SandboxInitBinary)
	}
	return appconfig.DefaultSandboxInitBinary
}

// dockerSandboxUsesInit reports whether this existing container was created
// with the init process. The label, not the current daemon config, decides:
// disabling the feature must not break sandboxes that already use init.
func dockerSandboxUsesInit(info containerapi.InspectResponse) bool {
	return info.Config != nil && info.Config.Labels != nil && info.Config.Labels[dockerSandboxInitLabel] == "true"
}

func sandboxInitControlHostDir(sandbox *Sandbox) string {
	return filepath.Join(hostSandboxDir(sandbox), "init-control")
}

func sandboxInitSocketHostPath(sandbox *Sandbox) string {
	return filepath.Join(sandboxInitControlHostDir(sandbox), "init.sock")
}

// sandboxInitClientPath is the path the daemon dials. Unix socket path length
// is limited to about 108 bytes; a deeply nested DATA_ROOT can exceed it even
// though the guest-side path is short. In that case the daemon dials a short
// symlink in its own temporary directory, which the kernel resolves to the
// real socket. The symlink is created lazily because the socket itself only
// exists after the container starts.
func sandboxInitClientPath(sandbox *Sandbox) string {
	socket := sandboxInitSocketHostPath(sandbox)
	if len(socket) <= 100 {
		return socket
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("agent-compose-init-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return socket
	}
	digest := sha256.Sum256([]byte(filepath.Clean(socket)))
	link := filepath.Join(dir, hex.EncodeToString(digest[:8])+".sock")
	if current, err := os.Readlink(link); err == nil && current == socket {
		return link
	}
	_ = os.Remove(link)
	if err := os.Symlink(socket, link); err != nil {
		return socket
	}
	return link
}

// cleanupSandboxInitClientPath removes the short symlink, if one was created.
func cleanupSandboxInitClientPath(sandbox *Sandbox) {
	socket := sandboxInitSocketHostPath(sandbox)
	link := sandboxInitClientPath(sandbox)
	if link != socket {
		_ = os.Remove(link)
	}
}

// prepareSandboxInitMount creates the host-side control directory and returns
// the bind mount the container needs. The socket itself is created by init.
func (r *dockerRuntime) prepareSandboxInitMount(ctx context.Context, dockerClient *client.Client, sandbox *Sandbox) (mountapi.Mount, error) {
	dir := sandboxInitControlHostDir(sandbox)
	if strings.TrimSpace(dir) == "" {
		return mountapi.Mount{}, errors.New("sandbox init control directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return mountapi.Mount{}, fmt.Errorf("create sandbox init control directory %s: %w", dir, err)
	}
	source, err := r.bindRuntimeMountSource(ctx, dockerClient, dir)
	if err != nil {
		return mountapi.Mount{}, fmt.Errorf("resolve sandbox init control mount source %s: %w", dir, err)
	}
	return mountapi.Mount{
		Type:   mountapi.TypeBind,
		Source: source,
		Target: sandboxInitGuestControlDir,
	}, nil
}

func (r *dockerRuntime) sandboxInitClient(sandbox *Sandbox) *sandboxinit.Client {
	return sandboxinit.NewClient(sandboxInitClientPath(sandbox))
}

// waitForSandboxInit blocks until the init control socket answers Ping. It is
// called after ContainerStart and before any command is sent to the sandbox.
func (r *dockerRuntime) waitForSandboxInit(ctx context.Context, dockerClient *client.Client, containerID string, sandbox *Sandbox) error {
	timeout := 30 * time.Second
	if r != nil && r.config != nil && r.config.SandboxStartTimeout > 0 {
		timeout = r.config.SandboxStartTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := r.sandboxInitClient(sandbox)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := client.Ping(waitCtx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if dockerClient != nil && containerID != "" {
			info, err := dockerClient.ContainerInspect(waitCtx, containerID)
			if err == nil && (info.State == nil || !info.State.Running) {
				reason := "container is no longer running"
				if info.State != nil && strings.TrimSpace(info.State.Status) != "" {
					reason = "container status is " + info.State.Status
				}
				if logs, logErr := r.readContainerLogs(waitCtx, dockerClient, containerID); logErr == nil {
					if text := strings.TrimSpace(logs); text != "" {
						if len(text) > 4096 {
							text = text[len(text)-4096:]
						}
						return fmt.Errorf("sandbox init did not become ready: %s; container log:\n%s", reason, text)
					}
				}
				return fmt.Errorf("sandbox init did not become ready: %s", reason)
			}
		}
		select {
		case <-waitCtx.Done():
			if lastErr == nil {
				lastErr = waitCtx.Err()
			}
			return fmt.Errorf("sandbox init did not become ready within %s: %w", timeout, lastErr)
		case <-ticker.C:
		}
	}
}

// initExecWithStream is the init-backed Exec/ExecStream implementation.
func (r *dockerRuntime) initExecWithStream(ctx context.Context, sandbox *Sandbox, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	command := strings.TrimSpace(spec.Command)
	if command == "" {
		return ExecResult{}, fmt.Errorf("sandbox init exec command is required")
	}
	marker := strings.TrimSpace(spec.Marker)
	if marker == "" {
		marker = newDockerExecMarker()
	}
	client := r.sandboxInitClient(sandbox)
	session, err := client.Exec(ctx, sandboxinit.Request{
		Command: command,
		Args:    append([]string(nil), spec.Args...),
		Env:     spec.Env,
		Cwd:     firstNonEmpty(spec.Cwd, r.config.GuestWorkspacePath),
		Marker:  marker,
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("start sandbox init exec for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	defer func() { _ = session.Close() }()

	collector := &dockerExecCollector{stream: stream, filter: newExecOutputFilter()}
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			collector.finish()
			if ctx.Err() != nil {
				r.killInitSession(sandbox, session.ID, !spec.KillOnlyOnCancel)
				return ExecResult{}, ctx.Err()
			}
			return ExecResult{}, fmt.Errorf("read sandbox init exec %s: %w", session.ID, err)
		}
		switch frame.Type {
		case sandboxinit.FrameStdout:
			collector.writeBytes(frame.Data, StdioStdout)
		case sandboxinit.FrameStderr:
			collector.writeBytes(frame.Data, StdioStderr)
		case sandboxinit.FrameError:
			collector.finish()
			r.killInitSession(sandbox, session.ID, true)
			return ExecResult{}, fmt.Errorf("sandbox init exec %s: %s", session.ID, frame.Message)
		case sandboxinit.FrameExit:
			collector.finish()
			result := ExecResult{
				ExitCode:      frame.Code,
				Stdout:        collector.stdout.String(),
				Stderr:        collector.stderr.String(),
				Output:        collector.output.String(),
				SecurityFacts: collector.filter.SecurityFacts(),
			}
			result.Success = result.ExitCode == 0
			return result, nil
		}
	}
}

func (r *dockerRuntime) killInitSession(sandbox *Sandbox, sessionID string, tree bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.sandboxInitClient(sandbox).Kill(ctx, sessionID, tree)
}

// initOpenInteraction creates a live init-backed interactive session.
func (r *dockerRuntime) initOpenInteraction(ctx context.Context, sandbox *Sandbox, spec RuntimeStartSpec) (RuntimeInteraction, error) {
	command := RuntimeCommandSpec{}
	if spec.Command != nil {
		command = *spec.Command
	}
	if strings.TrimSpace(command.Command) == "" {
		return nil, fmt.Errorf("sandbox init interaction command is required")
	}
	env := command.Env
	if len(spec.Env) > 0 {
		env = mergeStringMaps(spec.Env, command.Env)
	}
	childCtx, cancel := context.WithCancel(ctx)
	if spec.TimeoutMs > 0 {
		childCtx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutMs)*time.Millisecond)
	}
	client := r.sandboxInitClient(sandbox)
	session, err := client.Exec(childCtx, sandboxinit.Request{
		Command: command.Command,
		Args:    append([]string(nil), command.Args...),
		Env:     env,
		Cwd:     firstNonEmpty(command.Cwd, spec.Cwd, r.config.GuestWorkspacePath),
		TTY:     spec.TTY,
		Rows:    uint16(spec.Rows),
		Cols:    uint16(spec.Cols),
		Marker:  newDockerExecMarker(),
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start sandbox init interaction for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	interaction := &initCommandInteraction{
		ctx:         childCtx,
		cancel:      cancel,
		client:      client,
		sandbox:     sandbox,
		session:     session,
		operationID: spec.OperationID,
		tty:         spec.TTY,
		startedAt:   time.Now(),
		output:      make(chan RuntimeOutputFrame, 64),
		done:        make(chan struct{}),
	}
	interaction.emit(RuntimeOutputFrame{Type: RuntimeOutputStarted, StartedAt: interaction.startedAt})
	go interaction.run()
	return interaction, nil
}

type initCommandInteraction struct {
	ctx    context.Context
	cancel context.CancelFunc

	client  *sandboxinit.Client
	sandbox *Sandbox
	session *sandboxinit.Session

	operationID string
	tty         bool
	startedAt   time.Time

	output chan RuntimeOutputFrame
	done   chan struct{}

	result RuntimeResult
	err    error
}

func (i *initCommandInteraction) Send(frame RuntimeInputFrame) error {
	switch frame.Type {
	case RuntimeInputStdin:
		if len(frame.Data) == 0 {
			return nil
		}
		return i.session.SendStdin(i.ctx, frame.Data)
	case RuntimeInputStdinEOF:
		return i.session.CloseStdin(i.ctx)
	case RuntimeInputResize:
		return i.session.Resize(i.ctx, uint16(frame.Rows), uint16(frame.Cols))
	case RuntimeInputSignal:
		return i.session.Signal(i.ctx, initSignalName(frame.Signal))
	case RuntimeInputCancel:
		i.cancel()
		_ = i.session.Signal(context.Background(), "TERM")
		return nil
	default:
		return ErrRuntimeInteractionUnsupported
	}
}

func (i *initCommandInteraction) CloseSend() error {
	return i.session.CloseStdin(context.Background())
}

func (i *initCommandInteraction) Recv() (RuntimeOutputFrame, error) {
	frame, ok := <-i.output
	if !ok {
		return RuntimeOutputFrame{}, io.EOF
	}
	return frame, nil
}

func (i *initCommandInteraction) Wait() (RuntimeResult, error) {
	<-i.done
	return i.result, i.err
}

func (i *initCommandInteraction) run() {
	defer close(i.done)
	defer close(i.output)
	defer i.cancel()
	defer func() { _ = i.session.Close() }()

	var stdoutDecoder, stderrDecoder utf8StreamDecoder
	for {
		frame, err := i.session.Next(i.ctx)
		if err != nil {
			if i.ctx.Err() != nil {
				_ = i.session.Signal(context.Background(), "TERM")
				i.fail(i.ctx.Err())
				return
			}
			i.fail(fmt.Errorf("read sandbox init interaction: %w", err))
			return
		}
		switch frame.Type {
		case sandboxinit.FrameStdout:
			if text := stdoutDecoder.Write(frame.Data); text != "" {
				i.emit(RuntimeOutputFrame{Type: RuntimeOutputStdout, Data: []byte(text)})
			}
		case sandboxinit.FrameStderr:
			if text := stderrDecoder.Write(frame.Data); text != "" {
				i.emit(RuntimeOutputFrame{Type: RuntimeOutputStderr, Data: []byte(text)})
			}
		case sandboxinit.FrameError:
			i.fail(errors.New(frame.Message))
			return
		case sandboxinit.FrameExit:
			if text := stdoutDecoder.Finish(); text != "" {
				i.emit(RuntimeOutputFrame{Type: RuntimeOutputStdout, Data: []byte(text)})
			}
			if text := stderrDecoder.Finish(); text != "" {
				i.emit(RuntimeOutputFrame{Type: RuntimeOutputStderr, Data: []byte(text)})
			}
			completedAt := time.Now()
			i.result = RuntimeResult{
				OperationID: i.operationID,
				ExitCode:    frame.Code,
				Success:     frame.Code == 0,
				StartedAt:   i.startedAt,
				CompletedAt: completedAt,
			}
			i.emit(RuntimeOutputFrame{Type: RuntimeOutputResult, Result: &i.result})
			return
		}
	}
}

func (i *initCommandInteraction) fail(err error) {
	completedAt := time.Now()
	i.err = err
	i.result = RuntimeResult{
		OperationID: i.operationID,
		ExitCode:    -1,
		Error:       err.Error(),
		StartedAt:   i.startedAt,
		CompletedAt: completedAt,
	}
	i.emit(RuntimeOutputFrame{Type: RuntimeOutputError, Error: &RuntimeError{Message: err.Error()}})
	i.emit(RuntimeOutputFrame{Type: RuntimeOutputResult, Result: &i.result})
}

func (i *initCommandInteraction) emit(frame RuntimeOutputFrame) {
	select {
	case i.output <- frame:
	case <-i.ctx.Done():
	}
}

func initSignalName(signal RuntimeSignal) string {
	switch signal {
	case RuntimeSignalInterrupt:
		return "INT"
	case RuntimeSignalKill:
		return "KILL"
	default:
		return "TERM"
	}
}
