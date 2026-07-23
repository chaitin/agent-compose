//go:build linux && cgo && microsandboxcgo

package driver

import (
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/identity"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
	"golang.org/x/sync/singleflight"
)

type microsandboxRuntime struct {
	config *appconfig.Config

	initMu sync.Mutex
	ready  bool

	lifecycleMu      sync.Mutex
	lifecycleHandles map[string]*microsandbox.Sandbox
	createLocks      microsandboxKeyedLocks
	baseBuilds       singleflight.Group
}

type microsandboxExecCollector struct {
	stream ExecStreamWriter
	filter *execOutputFilter
	stdout bytes.Buffer
	stderr bytes.Buffer
	output bytes.Buffer
}

const microsandboxExecExitIdleGracePeriod = 2 * time.Second

// microsandboxExecSilenceProbeInterval is how long a stream may produce nothing
// at all before the guest is asked whether the process is still there.
//
// The guest agent reports a process exit only after both of its output pipes
// reach EOF, and a pipe reaches EOF only once every process that inherited it
// has exited. A command that leaves a background helper running therefore ends
// without the stream ever saying so, and waiting on the next event is a wait
// that never returns. Silence alone proves nothing -- a build can be quiet for
// a long time -- so it only triggers the question, and the answer decides.
const microsandboxExecSilenceProbeInterval = 2 * time.Minute

// microsandboxExecLivenessProbeTimeout bounds the liveness question itself, so
// a guest agent that has stopped answering cannot turn the probe into a second
// place to hang.
const microsandboxExecLivenessProbeTimeout = 30 * time.Second

const (
	microsandboxManagedLabel   = "agent-compose.managed"
	microsandboxSandboxIDLabel = "agent-compose.sandbox_id"
)

type microsandboxExecEventReceiver func(context.Context) (*microsandbox.ExecEvent, error)
type microsandboxExecHandleCloser func() error
type microsandboxExecTerminator func(context.Context) error

type microsandboxExecTerminationHandle interface {
	Kill(context.Context) error
	Wait(context.Context) (int, error)
}

type microsandboxExecReceiveResult struct {
	event *microsandbox.ExecEvent
	err   error
}

func terminateMicrosandboxExec(ctx context.Context, handle microsandboxExecTerminationHandle) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), execTerminationTimeout)
	defer cancel()
	killErr := handle.Kill(cleanupCtx)
	_, waitErr := handle.Wait(cleanupCtx)
	if waitErr == nil {
		return nil
	}
	return errors.Join(killErr, waitErr)
}

// microsandboxExecLivenessProbe reports whether the guest process is still
// present. A nil probe, or a probe that cannot answer, leaves the stream
// waiting: only a definite "the process is gone" is allowed to end it.
type microsandboxExecLivenessProbe func(ctx context.Context, pid uint32) (bool, error)

func consumeMicrosandboxExecStream(
	ctx context.Context,
	recv microsandboxExecEventReceiver,
	closeHandle microsandboxExecHandleCloser,
	terminate microsandboxExecTerminator,
	collector *microsandboxExecCollector,
	idleGrace time.Duration,
	probeAlive microsandboxExecLivenessProbe,
	silenceInterval time.Duration,
) (ExecResult, error) {
	exitCode := 0
	sawExit := false
	var pid uint32
	receiveCtx, cancelReceive := context.WithCancel(ctx)
	received := receiveMicrosandboxExecEvents(receiveCtx, recv)
	stopReceiving := func() {
		cancelReceive()
		for range received {
		}
	}
	defer stopReceiving()

	var waitTimer *time.Timer
	var wait <-chan time.Time
	stopWaitTimer := func() {
		if waitTimer == nil {
			return
		}
		if !waitTimer.Stop() {
			select {
			case <-waitTimer.C:
			default:
			}
		}
		wait = nil
	}
	defer stopWaitTimer()
	resetWaitTimer := func(duration time.Duration) {
		stopWaitTimer()
		if waitTimer == nil {
			waitTimer = time.NewTimer(duration)
		} else {
			waitTimer.Reset(duration)
		}
		wait = waitTimer.C
	}

	closeAfterDrainTimeout := func() {
		slog.Warn(
			"microsandbox exec stream timed out waiting for done after process exit; closing handle",
			"exit_code", exitCode,
			"drain_window", idleGrace,
		)
		if err := closeHandle(); err != nil {
			slog.Warn("failed to close microsandbox exec handle after drain timeout", "error", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			stopReceiving()
			var terminationErr error
			if terminate != nil {
				terminationErr = terminate(context.WithoutCancel(ctx))
			}
			collector.finish()
			return ExecResult{}, execTerminationResultError(RuntimeDriverMicrosandbox, strconv.FormatUint(uint64(pid), 10), ctx.Err(), terminationErr)
		case <-wait:
			if sawExit {
				stopReceiving()
				closeAfterDrainTimeout()
				return microsandboxExecResult(collector, exitCode, exitCode == 0), nil
			}
			gone, probeErr := microsandboxExecProcessGone(ctx, probeAlive, pid)
			if probeErr != nil {
				slog.Warn("failed to probe microsandbox exec process liveness; continuing to wait", "pid", pid, "error", probeErr)
				resetWaitTimer(silenceInterval)
				continue
			}
			if !gone {
				resetWaitTimer(silenceInterval)
				continue
			}
			slog.Warn(
				"microsandbox exec process is gone but the stream never reported its exit; closing handle",
				"pid", pid,
				"silence_window", silenceInterval,
			)
			stopReceiving()
			if closeErr := closeHandle(); closeErr != nil {
				slog.Warn("failed to close microsandbox exec handle after a lost exit", "error", closeErr)
			}
			collector.finish()
			return microsandboxExecResult(collector, -1, false), fmt.Errorf("microsandbox exec process %d exited without reporting its status; a process it started is still holding the output pipes open", pid)
		case result, ok := <-received:
			if !ok {
				collector.finish()
				if !sawExit {
					return microsandboxExecResult(collector, -1, false), fmt.Errorf("microsandbox exec stream ended without reporting a process exit status")
				}
				return microsandboxExecResult(collector, exitCode, exitCode == 0), nil
			}
			if result.err != nil {
				collector.finish()
				return ExecResult{}, result.err
			}
			event := result.event
			if event == nil || event.Kind == microsandbox.ExecEventDone {
				collector.finish()
				if !sawExit {
					return microsandboxExecResult(collector, -1, false), fmt.Errorf("microsandbox exec stream ended without reporting a process exit status")
				}
				return microsandboxExecResult(collector, exitCode, exitCode == 0), nil
			}

			switch event.Kind {
			case microsandbox.ExecEventStarted:
				pid = event.PID
				if probeAlive != nil && silenceInterval > 0 && pid != 0 {
					resetWaitTimer(silenceInterval)
				}
			case microsandbox.ExecEventStdout:
				collector.writeChunk(ExecChunk{Text: string(event.Data)})
				if sawExit {
					resetWaitTimer(idleGrace)
				} else if probeAlive != nil && silenceInterval > 0 && pid != 0 {
					resetWaitTimer(silenceInterval)
				}
			case microsandbox.ExecEventStderr:
				collector.writeChunk(ExecChunk{Text: string(event.Data), Stream: StdioStderr})
				if sawExit {
					resetWaitTimer(idleGrace)
				} else if probeAlive != nil && silenceInterval > 0 && pid != 0 {
					resetWaitTimer(silenceInterval)
				}
			case microsandbox.ExecEventExited:
				exitCode = event.ExitCode
				sawExit = true
				resetWaitTimer(idleGrace)
			case microsandbox.ExecEventFailed:
				collector.finish()
				return ExecResult{}, formatMicrosandboxExecFailure(event.Failure)
			case microsandbox.ExecEventStdinError:
				collector.writeChunk(ExecChunk{Text: formatMicrosandboxExecFailure(event.Failure).Error() + "\n", Stream: StdioStderr})
			}
		}
	}
}

func receiveMicrosandboxExecEvents(ctx context.Context, recv microsandboxExecEventReceiver) <-chan microsandboxExecReceiveResult {
	received := make(chan microsandboxExecReceiveResult, 1)
	go func() {
		defer close(received)
		for {
			// ExecHandle is not safe for concurrent use. More importantly, the
			// SDK documents that canceling Recv only releases the Go caller while
			// the Rust receive continues in the background. Keep exactly one Recv
			// loop for the lifetime of the stream; silence timers must never use
			// Recv cancellation as their clock.
			event, err := recv(ctx)
			select {
			case received <- microsandboxExecReceiveResult{event: event, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil || event == nil || event.Kind == microsandbox.ExecEventDone {
				return
			}
		}
	}()
	return received
}

func microsandboxExecResult(collector *microsandboxExecCollector, exitCode int, success bool) ExecResult {
	return ExecResult{
		ExitCode: exitCode,
		Success:  success,
		Stdout:   collector.stdout.String(),
		Stderr:   collector.stderr.String(),
		Output:   collector.output.String(),
	}
}

// microsandboxExecLivenessProbeFor asks the guest about a pid over a separate
// exec session, which is unaffected by the pipes the original session is still
// waiting on.
func microsandboxExecLivenessProbeFor(sandbox *microsandbox.Sandbox) microsandboxExecLivenessProbe {
	if sandbox == nil {
		return nil
	}
	return func(ctx context.Context, pid uint32) (bool, error) {
		output, err := sandbox.Exec(ctx, "sh", []string{"-c", microsandboxExecLivenessProbeCommand(pid)})
		if err != nil {
			return false, err
		}
		return output.ExitCode() == 0, nil
	}
}

func microsandboxExecLivenessProbeCommand(pid uint32) string {
	return fmt.Sprintf(
		"kill -0 %[1]d 2>/dev/null || exit 1; "+
			"stat=$(cat /proc/%[1]d/stat 2>/dev/null) || exit 0; "+
			"rest=${stat##*) }; state=${rest%%%% *}; [ \"$state\" != Z ]",
		pid,
	)
}

// microsandboxExecProcessGone answers only when it is sure. A probe that fails
// leaves the caller waiting, because "the guest could not be asked" and "the
// process is gone" must not lead to the same decision.
func microsandboxExecProcessGone(ctx context.Context, probeAlive microsandboxExecLivenessProbe, pid uint32) (bool, error) {
	if probeAlive == nil || pid == 0 {
		return false, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, microsandboxExecLivenessProbeTimeout)
	defer cancel()
	alive, err := probeAlive(probeCtx, pid)
	if err != nil {
		return false, err
	}
	return !alive, nil
}

func newMicrosandboxRuntime(config *appconfig.Config) (SandboxRuntime, error) {
	return &microsandboxRuntime{config: config, lifecycleHandles: map[string]*microsandbox.Sandbox{}}, nil
}

func (c *microsandboxExecCollector) writeChunk(chunk ExecChunk) {
	if c.filter == nil {
		c.appendChunk(chunk)
		return
	}
	c.filter.Write(chunk, c.appendChunk)
}

func (c *microsandboxExecCollector) finish() {
	if c.filter == nil {
		return
	}
	c.filter.Finish(c.appendChunk)
}

func (c *microsandboxExecCollector) appendChunk(chunk ExecChunk) {
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

func (r *microsandboxRuntime) EnsureSandbox(ctx context.Context, session *Sandbox, vmState VMState, proxyState ProxyState) (SandboxVMInfo, error) {
	name := r.sandboxName(session, vmState)
	if err := r.ensureReady(ctx); err != nil {
		return SandboxVMInfo{}, err
	}

	sandbox, created, restarted, err := r.getOrCreateSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		return SandboxVMInfo{}, err
	}
	defer r.releaseSandboxHandle(name, sandbox)

	if err := r.ensureDirectoryOnlyGuestSandboxBootstrap(ctx, sandbox, session, name); err != nil {
		return SandboxVMInfo{}, err
	}

	needLaunch := created || restarted
	if jupyterEnabled(proxyState) && !needLaunch {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		probeErr := waitForJupyterProxy(probeCtx, proxyState)
		cancel()
		needLaunch = probeErr != nil
	}
	if jupyterEnabled(proxyState) && needLaunch {
		if err := r.launchJupyter(ctx, sandbox, proxyState); err != nil {
			return SandboxVMInfo{}, err
		}
	}
	if jupyterEnabled(proxyState) {
		readyCtx, cancel := context.WithTimeout(ctx, r.config.JupyterReadyTimeout)
		readyErr := waitForJupyterProxy(readyCtx, proxyState)
		cancel()
		if readyErr != nil {
			if logText := readSandboxJupyterLog(session); jupyterLogIndicatesReady(logText) {
				slog.Warn("microsandbox jupyter probe timed out after guest reported ready", "sandbox_id", session.Summary.ID, "error", readyErr)
			} else if logText != "" {
				return SandboxVMInfo{}, fmt.Errorf("%w\nGuest log:\n%s", readyErr, logText)
			} else {
				return SandboxVMInfo{}, readyErr
			}
		}
	}
	return SandboxVMInfo{
		BoxID:      name,
		JupyterURL: jupyterDirectURL(proxyState),
	}, nil
}

func (r *microsandboxRuntime) StopSandbox(ctx context.Context, session *Sandbox, vmState VMState) (bool, error) {
	name := r.sandboxName(session, vmState)
	if err := r.ensureReady(ctx); err != nil {
		return false, err
	}
	handle, err := microsandbox.GetSandbox(ctx, name)
	if err != nil {
		if microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
			return true, nil
		}
		return false, err
	}
	if handle.Status() != microsandbox.SandboxStatusRunning && handle.Status() != microsandbox.SandboxStatusDraining {
		r.discardLifecycleHandle(name)
		return false, nil
	}

	if sandbox := r.takeLifecycleHandle(name); sandbox != nil {
		defer func() {
			if sandbox != nil {
				r.closeSandboxHandle(sandbox)
			}
		}()
		if err := sandbox.Stop(ctx); err != nil {
			if microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
				return true, nil
			}
			r.trackLifecycleHandle(name, sandbox)
			sandbox = nil
			return false, err
		}
		return false, nil
	}

	sandbox, stale, err := r.connectLiveSandbox(ctx, handle, name)
	if err != nil {
		return false, err
	}
	if stale || sandbox == nil {
		r.discardLifecycleHandle(name)
		return true, nil
	}
	defer r.releaseSandboxHandle(name, sandbox)
	if err := sandbox.Stop(ctx); err != nil {
		if microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func (r *microsandboxRuntime) RemoveSandbox(ctx context.Context, session *Sandbox, vmState VMState) error {
	unlock := r.createLocks.lock(session.Summary.ID)
	defer unlock()
	if _, err := r.StopSandbox(ctx, session, vmState); err != nil {
		return err
	}
	name := r.sandboxName(session, vmState)
	r.discardLifecycleHandle(name)
	if err := microsandbox.RemoveSandbox(ctx, name); err != nil &&
		!microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
		return fmt.Errorf("remove microsandbox %s: %w", name, err)
	}
	if err := r.removeDockerDiskFiles(session.Summary.ID); err != nil {
		return err
	}
	if err := r.removeRootfsDiskFiles(session.Summary.ID); err != nil {
		return err
	}
	return nil
}

func (r *microsandboxRuntime) Exec(ctx context.Context, session *Sandbox, vmState VMState, spec ExecSpec) (ExecResult, error) {
	name := r.sandboxName(session, vmState)
	if err := r.ensureReady(ctx); err != nil {
		return ExecResult{}, err
	}

	sandbox, err := r.connectSandbox(ctx, session, vmState, true)
	if err != nil {
		return ExecResult{}, err
	}
	defer r.releaseSandboxHandle(name, sandbox)
	return executeUserCommandAfterBootstrap(
		func() error {
			return r.ensureDirectoryOnlyGuestSandboxBootstrap(ctx, sandbox, session, name)
		},
		func() (ExecResult, error) {
			output, err := sandbox.Exec(ctx, spec.Command, spec.Args, r.execOptions(ctx, spec)...)
			if err != nil {
				return ExecResult{}, err
			}
			return ExecResult{
				ExitCode: output.ExitCode(),
				Stdout:   output.Stdout(),
				Stderr:   output.Stderr(),
				Output:   output.Stdout() + output.Stderr(),
				Success:  output.Success(),
			}, nil
		},
	)
}

func (r *microsandboxRuntime) ExecStream(ctx context.Context, session *Sandbox, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	name := r.sandboxName(session, vmState)
	if err := r.ensureReady(ctx); err != nil {
		return ExecResult{}, err
	}

	sandbox, err := r.connectSandbox(ctx, session, vmState, true)
	if err != nil {
		return ExecResult{}, err
	}
	defer r.releaseSandboxHandle(name, sandbox)
	return executeUserCommandAfterBootstrap(
		func() error {
			return r.ensureDirectoryOnlyGuestSandboxBootstrap(ctx, sandbox, session, name)
		},
		func() (ExecResult, error) {
			handle, err := sandbox.ExecStream(ctx, spec.Command, spec.Args, r.execOptions(ctx, spec)...)
			if err != nil {
				return ExecResult{}, err
			}
			handleClosed := false
			closeHandle := func() error {
				if handleClosed {
					return nil
				}
				handleClosed = true
				return handle.Close()
			}
			defer func() {
				if err := closeHandle(); err != nil {
					slog.Warn("failed to close microsandbox exec handle", "error", err)
				}
			}()

			collector := &microsandboxExecCollector{stream: stream, filter: newExecOutputFilter()}
			return consumeMicrosandboxExecStream(
				ctx,
				handle.Recv,
				closeHandle,
				func(cleanupCtx context.Context) error { return terminateMicrosandboxExec(cleanupCtx, handle) },
				collector,
				microsandboxExecExitIdleGracePeriod,
				microsandboxExecLivenessProbeFor(sandbox),
				microsandboxExecSilenceProbeInterval,
			)
		},
	)
}

func (r *microsandboxRuntime) ensureDirectoryOnlyGuestSandboxBootstrap(ctx context.Context, sandbox *microsandbox.Sandbox, session *Sandbox, sandboxName string) error {
	spec := directoryOnlyGuestSandboxBootstrapExecSpec(r.config)
	output, err := sandbox.Exec(ctx, spec.Command, spec.Args, r.execOptions(ctx, spec)...)
	result := ExecResult{}
	if output != nil {
		result = ExecResult{
			ExitCode: output.ExitCode(),
			Stdout:   output.Stdout(),
			Stderr:   output.Stderr(),
			Output:   output.Stdout() + output.Stderr(),
			Success:  output.Success(),
		}
	}
	sandboxID := ""
	if session != nil {
		sandboxID = session.Summary.ID
	}
	if err != nil {
		return formatDirectoryOnlyGuestSandboxBootstrapError(RuntimeDriverMicrosandbox, sandboxID, sandboxName, result, err)
	}
	if !result.Success {
		return formatDirectoryOnlyGuestSandboxBootstrapError(RuntimeDriverMicrosandbox, sandboxID, sandboxName, result, nil)
	}
	return nil
}

func (r *microsandboxRuntime) Stats(ctx context.Context, session *Sandbox, vmState VMState) (SandboxStats, error) {
	if err := r.ensureReady(ctx); err != nil {
		return SandboxStats{}, err
	}
	name := r.sandboxName(session, vmState)
	handle, err := microsandbox.GetSandbox(ctx, name)
	if err != nil {
		if microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
			return SandboxStats{}, fmt.Errorf("sandbox box is not initialized")
		}
		return SandboxStats{}, err
	}
	if handle.Status() != microsandbox.SandboxStatusRunning && handle.Status() != microsandbox.SandboxStatusDraining {
		return SandboxStats{}, fmt.Errorf("sandbox box is not running")
	}
	metrics, err := handle.Metrics(ctx)
	if err != nil {
		return SandboxStats{}, err
	}
	return microsandboxStatsFromMetrics(session, vmState, metrics), nil
}

func (r *microsandboxRuntime) ensureReady(ctx context.Context) error {
	r.initMu.Lock()
	defer r.initMu.Unlock()
	if r.ready {
		return nil
	}
	if err := r.prepareEnvironment(); err != nil {
		return err
	}
	if err := microsandbox.EnsureInstalled(ctx, microsandbox.WithSkipDownload()); err != nil {
		return err
	}
	r.ready = true
	return nil
}

func (r *microsandboxRuntime) prepareEnvironment() error {
	if err := os.MkdirAll(r.config.MicrosandboxHome, 0o755); err != nil {
		return fmt.Errorf("create microsandbox home: %w", err)
	}
	if _, err := os.Stat(r.config.MicrosandboxMSBPath); err != nil {
		return fmt.Errorf("microsandbox msb binary missing at %s: %w", r.config.MicrosandboxMSBPath, err)
	}
	if _, err := os.Stat(r.config.MicrosandboxLibPath); err != nil {
		return fmt.Errorf("microsandbox Go FFI library missing at %s: %w", r.config.MicrosandboxLibPath, err)
	}
	if err := validateMicrosandboxDiskTools(); err != nil {
		return err
	}
	libkrunfwPath, err := r.resolveLibkrunfwPath()
	if err != nil {
		return err
	}
	if libkrunfwPath == "" {
		return fmt.Errorf("microsandbox libkrunfw not found next to %s", r.config.MicrosandboxLibPath)
	}
	if err := r.installMicrosandboxRuntime(r.config.MicrosandboxMSBPath, libkrunfwPath); err != nil {
		return err
	}

	prependEnvPath("PATH", filepath.Dir(r.config.MicrosandboxMSBPath))
	prependEnvPath("LD_LIBRARY_PATH", filepath.Dir(r.config.MicrosandboxLibPath))
	_ = os.Setenv("MSB_HOME", r.config.MicrosandboxHome)
	_ = os.Setenv("MSB_PATH", r.config.MicrosandboxMSBPath)
	if err := r.writeMicrosandboxConfig(libkrunfwPath); err != nil {
		return err
	}
	return nil
}

func (r *microsandboxRuntime) resolveLibkrunfwPath() (string, error) {
	libDir := filepath.Dir(r.config.MicrosandboxLibPath)
	var selected string
	var selectedVersion []int
	entries, err := os.ReadDir(libDir)
	if err != nil {
		return "", fmt.Errorf("read microsandbox lib directory %s: %w", libDir, err)
	}
	for _, entry := range entries {
		match := filepath.Join(libDir, entry.Name())
		info, err := os.Lstat(match)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		version, ok := parseLibkrunfwVersion(filepath.Base(match))
		if !ok {
			continue
		}
		if selected == "" || compareIntVersions(version, selectedVersion) > 0 {
			selected = match
			selectedVersion = version
		}
	}
	if selected != "" {
		return selected, nil
	}
	for _, name := range []string{"libkrunfw.so.5", "libkrunfw.so"} {
		path := filepath.Join(libDir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", nil
}

func parseLibkrunfwVersion(name string) ([]int, bool) {
	const prefix = "libkrunfw.so."
	versionText, ok := strings.CutPrefix(name, prefix)
	if !ok || versionText == "" {
		return nil, false
	}
	parts := strings.Split(versionText, ".")
	version := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, false
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		version = append(version, value)
	}
	return version, true
}

func compareIntVersions(left []int, right []int) int {
	maxLen := len(left)
	if len(right) > maxLen {
		maxLen = len(right)
	}
	for i := 0; i < maxLen; i++ {
		var leftValue int
		if i < len(left) {
			leftValue = left[i]
		}
		var rightValue int
		if i < len(right) {
			rightValue = right[i]
		}
		if leftValue > rightValue {
			return 1
		}
		if leftValue < rightValue {
			return -1
		}
	}
	return 0
}

func (r *microsandboxRuntime) installMicrosandboxRuntime(msbPath string, libkrunfwPath string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory for microsandbox install: %w", err)
	}

	installRoot := filepath.Join(homeDir, ".microsandbox")
	binDir := filepath.Join(installRoot, "bin")
	libDir := filepath.Join(installRoot, "lib")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create microsandbox install bin dir: %w", err)
	}
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return fmt.Errorf("create microsandbox install lib dir: %w", err)
	}
	if err := copyFileWithMode(msbPath, filepath.Join(binDir, "msb"), 0o755); err != nil {
		return fmt.Errorf("install microsandbox msb: %w", err)
	}

	libFilename := filepath.Base(libkrunfwPath)
	installedLibPath := filepath.Join(libDir, libFilename)
	if err := copyFileWithMode(libkrunfwPath, installedLibPath, 0o644); err != nil {
		return fmt.Errorf("install microsandbox libkrunfw: %w", err)
	}
	if err := symlinkForce(libFilename, filepath.Join(libDir, "libkrunfw.so.5")); err != nil {
		return fmt.Errorf("link microsandbox libkrunfw.so.5: %w", err)
	}
	if err := symlinkForce("libkrunfw.so.5", filepath.Join(libDir, "libkrunfw.so")); err != nil {
		return fmt.Errorf("link microsandbox libkrunfw.so: %w", err)
	}
	return nil
}

func copyFileWithMode(src string, dst string, mode os.FileMode) (err error) {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".agent-compose-microsandbox-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := io.Copy(tmp, source); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	return nil
}

func symlinkForce(target string, path string) error {
	_ = os.Remove(path)
	return os.Symlink(target, path)
}

func (r *microsandboxRuntime) writeMicrosandboxConfig(libkrunfwPath string) error {
	configPath := filepath.Join(r.config.MicrosandboxHome, "config.json")
	payload := map[string]any{}
	if data, err := os.ReadFile(configPath); err == nil {
		if strings.TrimSpace(string(data)) != "" {
			if err := json.Unmarshal(data, &payload); err != nil {
				return fmt.Errorf("decode microsandbox config %s: %w", configPath, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read microsandbox config %s: %w", configPath, err)
	}

	payload["home"] = r.config.MicrosandboxHome
	paths := ensureJSONObject(payload, "paths")
	paths["msb"] = r.config.MicrosandboxMSBPath
	paths["libkrunfw"] = libkrunfwPath

	registries := ensureJSONObject(payload, "registries")
	r.removeGeneratedMicrosandboxRegistryCA(registries)

	configuredInsecureHosts := map[string]struct{}{}
	for _, host := range r.config.MicrosandboxInsecure {
		trimmed := strings.TrimSpace(host)
		if trimmed == "" {
			continue
		}
		configuredInsecureHosts[trimmed] = struct{}{}
	}
	if existingHosts, ok := registries["hosts"].(map[string]any); ok {
		for host := range existingHosts {
			trimmed := strings.TrimSpace(host)
			if !strings.HasPrefix(trimmed, "127.0.0.1:") && !strings.HasPrefix(trimmed, "localhost:") {
				continue
			}
			if _, keep := configuredInsecureHosts[trimmed]; keep {
				continue
			}
			delete(existingHosts, host)
		}
	}
	if len(configuredInsecureHosts) > 0 {
		hosts := ensureJSONObject(registries, "hosts")
		for host := range configuredInsecureHosts {
			ensureJSONObject(hosts, host)["insecure"] = true
		}
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode microsandbox config %s: %w", configPath, err)
	}
	if err := os.WriteFile(configPath, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("write microsandbox config %s: %w", configPath, err)
	}
	return nil
}

func (r *microsandboxRuntime) removeGeneratedMicrosandboxRegistryCA(registries map[string]any) {
	localCAPath := filepath.Join(r.config.MicrosandboxHome, "registry-local-ca.pem")
	bundlePath := filepath.Join(r.config.MicrosandboxHome, "registry-ca-bundle.pem")
	existingPath := strings.TrimSpace(jsonStringValue(registries["ca_certs"]))
	if existingPath == localCAPath || existingPath == bundlePath {
		delete(registries, "ca_certs")
	}
	_ = os.Remove(localCAPath)
	_ = os.Remove(bundlePath)
}

func (r *microsandboxRuntime) sandboxStateDir(name string) string {
	return filepath.Join(r.config.MicrosandboxHome, "sandboxes", name)
}

func (r *microsandboxRuntime) dockerDiskPath(sandboxID string) string {
	return filepath.Join(r.config.MicrosandboxHome, "docker-disks", microsandboxDockerDiskName(sandboxID)+".raw")
}

func (r *microsandboxRuntime) legacyDockerDiskPath(sandboxID string) string {
	return filepath.Join(r.config.MicrosandboxHome, "docker-disks", sandboxID+".raw")
}

func microsandboxDockerDiskName(sandboxID string) string {
	if hash, err := identity.Hash(sandboxID); err == nil {
		return hash
	}
	return sandboxID
}

func (r *microsandboxRuntime) ensureDockerDisk(sandboxID string) (string, error) {
	path := r.dockerDiskPath(sandboxID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create docker-disks directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		// Disk already exists; reuse it (idempotent for sandbox reconnects).
		if err := writeMicrosandboxDiskOwnership(path, sandboxID); err != nil {
			return "", err
		}
		return path, nil
	}
	legacyPath := r.legacyDockerDiskPath(sandboxID)
	if legacyPath != path {
		if _, err := os.Stat(legacyPath); err == nil {
			if err := os.Rename(legacyPath, path); err != nil {
				return "", fmt.Errorf("migrate legacy docker disk image %s to %s: %w", legacyPath, path, err)
			}
			if err := writeMicrosandboxDiskOwnership(path, sandboxID); err != nil {
				return "", err
			}
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat legacy docker disk image %s: %w", legacyPath, err)
		}
	}
	// Create a sparse raw file then format it as ext4. Sized by
	// SANDBOX_DISK_SIZE_GB (shared with the BoxLite driver; default 6 GiB).
	// Existing .raw files are reused as-is (see os.Stat check above).
	sizeGB := configuredSandboxResources(r.config).DiskSizeGB
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create docker disk image %s: %w", path, err)
	}
	if err := f.Truncate(int64(sizeGB) << 30); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("size docker disk image %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close docker disk image %s: %w", path, err)
	}
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", path).CombinedOutput(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("mkfs.ext4 docker disk image %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	if err := writeMicrosandboxDiskOwnership(path, sandboxID); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (r *microsandboxRuntime) removeDockerDisk(sandboxID string) {
	if err := r.removeDockerDiskFiles(sandboxID); err != nil {
		slog.Warn("agent-compose microsandbox: failed to remove docker disk image", "sandbox_id", sandboxID, "error", err)
	}
}

func (r *microsandboxRuntime) removeDockerDiskFiles(sandboxID string) error {
	paths := []string{r.dockerDiskPath(sandboxID)}
	if legacyPath := r.legacyDockerDiskPath(sandboxID); legacyPath != paths[0] {
		paths = append(paths, legacyPath)
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove microsandbox docker disk image %s: %w", path, err)
		}
		if err := os.Remove(path + ".owner.json"); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove microsandbox docker disk ownership %s: %w", path, err)
		}
	}
	return nil
}

func (r *microsandboxRuntime) sandboxAgentSockPath(name string) string {
	return filepath.Join(r.sandboxStateDir(name), "runtime", "agent.sock")
}

func (r *microsandboxRuntime) cleanupStaleSandboxState(name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	stateDir := r.sandboxStateDir(name)
	if err := os.RemoveAll(stateDir); err != nil {
		return fmt.Errorf("remove stale microsandbox state %s: %w", stateDir, err)
	}
	return nil
}

func (r *microsandboxRuntime) isStaleMicrosandboxConnectionError(name string, err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(errText, "failed to connect to agent relay") &&
		(strings.Contains(errText, "connection refused") ||
			strings.Contains(errText, "no such file") ||
			strings.Contains(errText, "os error 111") ||
			strings.Contains(errText, "os error 2")) {
		return true
	}
	sockPath := r.sandboxAgentSockPath(name)
	conn, dialErr := net.DialTimeout("unix", sockPath, 200*time.Millisecond)
	if dialErr != nil {
		dialText := strings.ToLower(strings.TrimSpace(dialErr.Error()))
		return strings.Contains(dialText, "connection refused") || strings.Contains(dialText, "no such file")
	}
	_ = conn.Close()
	return false
}

func (r *microsandboxRuntime) connectLiveSandbox(ctx context.Context, handle *microsandbox.SandboxHandle, name string) (*microsandbox.Sandbox, bool, error) {
	sandbox, err := handle.Connect(ctx)
	if err == nil {
		return sandbox, false, nil
	}
	if microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) || r.isStaleMicrosandboxConnectionError(name, err) {
		if cleanupErr := r.cleanupStaleSandboxState(name); cleanupErr != nil {
			return nil, false, cleanupErr
		}
		slog.Warn("agent-compose microsandbox cleaned stale sandbox state", "sandbox", name, "error", err)
		return nil, true, nil
	}
	return nil, false, err
}

func (r *microsandboxRuntime) IsSandboxAlive(ctx context.Context, session *Sandbox, vmState VMState) (bool, error) {
	if err := r.ensureReady(ctx); err != nil {
		return false, err
	}
	name := r.sandboxName(session, vmState)
	handle, err := microsandbox.GetSandbox(ctx, name)
	if err != nil {
		if microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
			if cleanupErr := r.cleanupStaleSandboxState(name); cleanupErr != nil {
				return false, cleanupErr
			}
			return false, nil
		}
		return false, err
	}
	if handle.Status() != microsandbox.SandboxStatusRunning && handle.Status() != microsandbox.SandboxStatusDraining {
		r.discardLifecycleHandle(name)
		return false, nil
	}
	sandbox, stale, err := r.connectLiveSandbox(ctx, handle, name)
	if err != nil {
		return false, err
	}
	if stale || sandbox == nil {
		r.discardLifecycleHandle(name)
		return false, nil
	}
	r.releaseSandboxHandle(name, sandbox)
	return true, nil
}

func (r *microsandboxRuntime) getOrCreateSandbox(ctx context.Context, session *Sandbox, vmState VMState, proxyState ProxyState) (*microsandbox.Sandbox, bool, bool, error) {
	unlock := r.createLocks.lock(session.Summary.ID)
	defer unlock()
	name := r.sandboxName(session, vmState)
	handle, err := microsandbox.GetSandbox(ctx, name)
	if err != nil {
		if !microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
			return nil, false, false, err
		}
		if !vmState.StoppedAt.IsZero() {
			return nil, false, false, fmt.Errorf("microsandbox runtime state for stopped sandbox %s is missing; refusing to recreate it during resume", session.Summary.ID)
		}
		sandbox, err := r.createSandbox(ctx, session, vmState, proxyState, name)
		if err == nil {
			r.trackLifecycleHandle(name, sandbox)
		}
		return sandbox, true, true, err
	}
	if handle.Status() == microsandbox.SandboxStatusRunning || handle.Status() == microsandbox.SandboxStatusDraining {
		sandbox, stale, err := r.connectLiveSandbox(ctx, handle, name)
		if err != nil {
			return nil, false, false, err
		}
		if stale {
			sandbox, err := r.createSandbox(ctx, session, vmState, proxyState, name)
			if err == nil {
				r.trackLifecycleHandle(name, sandbox)
			}
			return sandbox, true, true, err
		}
		return sandbox, false, false, nil
	}
	sandbox, err := handle.Start(ctx)
	if err == nil {
		r.trackLifecycleHandle(name, sandbox)
	}
	return sandbox, false, true, err
}

func (r *microsandboxRuntime) connectSandbox(ctx context.Context, session *Sandbox, vmState VMState, startIfStopped bool) (*microsandbox.Sandbox, error) {
	name := r.sandboxName(session, vmState)
	handle, err := microsandbox.GetSandbox(ctx, name)
	if err != nil {
		if microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
			return nil, fmt.Errorf("sandbox box is not initialized")
		}
		return nil, err
	}
	if handle.Status() == microsandbox.SandboxStatusRunning || handle.Status() == microsandbox.SandboxStatusDraining {
		sandbox, stale, err := r.connectLiveSandbox(ctx, handle, name)
		if err != nil {
			return nil, err
		}
		if stale {
			return nil, fmt.Errorf("sandbox box is not initialized")
		}
		return sandbox, nil
	}
	if !startIfStopped {
		return nil, fmt.Errorf("sandbox box is not running")
	}
	defer func() {
		if r := recover(); r != nil {
			panic(r)
		}
	}()
	sandbox, startErr := handle.Start(ctx)
	if startErr == nil {
		r.trackLifecycleHandle(name, sandbox)
	}
	return sandbox, startErr
}

func (r *microsandboxRuntime) createSandbox(ctx context.Context, session *Sandbox, vmState VMState, proxyState ProxyState, name string) (*microsandbox.Sandbox, error) {
	appconfig.ApplyDefaultGuestPaths(r.config)
	manifest, err := loadDirectoryRuntimeMountManifest(session, RuntimeDriverMicrosandbox)
	if err != nil {
		return nil, err
	}
	mounts := make(map[string]microsandbox.MountConfig, len(manifest.Mounts)+1)
	for _, mount := range manifest.Mounts {
		bindMount := r.microsandboxBindMount(mount.HostPath, mount.ReadOnly)
		mounts[mount.GuestPath] = bindMount
		slog.Info(
			"agent-compose microsandbox configured bind mount",
			"sandbox", name,
			"guest_path", mount.GuestPath,
			"readonly", mount.ReadOnly,
			"quota_mib", bindMount.QuotaMiB,
			"configured_bind_quota_gb", r.config.SandboxDiskSizeGB,
			"sandbox_disk_size_gb", r.config.SandboxDiskSizeGB,
		)
	}
	imageRef := resolveSandboxGuestImage(vmState.Image, session.Summary.GuestImage, defaultGuestImageForDriver(r.config, RuntimeDriverMicrosandbox))
	imagePullTimeout := r.config.ImagePullTimeout
	if imagePullTimeout <= 0 {
		imagePullTimeout = defaultImagePullTimeout
	}
	baseDisk, err := r.resolveMicrosandboxBaseDisk(ctx, imageRef, session.Summary.PullPolicy, imagePullTimeout)
	if err != nil {
		return nil, err
	}
	rootfsDisk, err := r.ensureRootfsDiskWithCacheLock(ctx, session.Summary.ID, baseDisk)
	if err != nil {
		return nil, fmt.Errorf("provision microsandbox rootfs disk: %w", err)
	}
	rootfsCreated := rootfsDisk.Created
	defer func() {
		if rootfsCreated {
			if cleanupErr := removeMicrosandboxRootfsDiskPair(r.config.MicrosandboxHome, rootfsDisk.Path, true); cleanupErr != nil {
				slog.Warn("agent-compose microsandbox failed to clean newly-created rootfs disk", "sandbox_id", session.Summary.ID, "path", rootfsDisk.Path, "error", cleanupErr)
			}
		}
	}()

	// Keep guest Docker data on a separate disk so its unbounded image/layer
	// growth has an independent capacity and cleanup boundary.
	dockerDiskExisted := false
	for _, candidate := range []string{r.dockerDiskPath(session.Summary.ID), r.legacyDockerDiskPath(session.Summary.ID)} {
		if _, statErr := os.Lstat(candidate); statErr == nil {
			dockerDiskExisted = true
			break
		} else if !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("inspect docker disk before provisioning %s: %w", candidate, statErr)
		}
	}
	rawPath, err := r.ensureDockerDisk(session.Summary.ID)
	if err != nil {
		return nil, fmt.Errorf("provision docker disk: %w", err)
	}
	// If the sandbox never comes up, remove the disk we just provisioned:
	// StopSandbox is not guaranteed to run for a sandbox that never started,
	// so without this an unowned .raw would remain after failed creation. On
	// success the flag below disarms the cleanup.
	sandboxCreated := false
	defer func() {
		if !sandboxCreated && !dockerDiskExisted {
			r.removeDockerDisk(session.Summary.ID)
		}
	}()
	mounts["/var/lib/docker"] = microsandbox.Mount.Disk(rawPath, microsandbox.DiskOptions{Format: "raw", Fstype: "ext4"})
	// /run must be a per-VM tmpfs because msb guest init does not mount it.
	// The private root disk persists across stop/start, so runtime state written
	// under /run would otherwise outlive the VM. A stale
	// /run/docker/containerd/containerd.pid then makes dockerd kill its own
	// containerd and refuse to start. agentd recreates /run/microsandbox after
	// user tmpfs mounts are applied, so shadowing /run here is safe.
	mounts["/run"] = microsandbox.Mount.Tmpfs(microsandbox.TmpfsOptions{SizeMiB: 256})
	imageEnv := baseDisk.Env
	env := sandboxEnvMap(session.EnvItems, session.RuntimeEnvItems)
	if env == nil {
		env = map[string]string{}
	}
	// Merge image ENV as baseline — user/session env vars and agent-compose
	// defaults override image-defined values (matching Docker daemon behavior).
	for _, e := range imageEnv {
		if key, value, ok := parseEnvEntry(e); ok {
			if _, exists := env[key]; !exists {
				env[key] = value
			}
		}
	}
	env["GOPATH"] = "/usr/local/go"
	env["PATH"] = "/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	env["SANDBOX_ID"] = session.Summary.ID
	env["WORKSPACE"] = r.config.GuestWorkspacePath
	env["STATE_ROOT"] = r.config.GuestStateRoot
	env["RUNTIME_ROOT"] = r.config.GuestRuntimeRoot
	env["JUPYTER_TOKEN"] = proxyState.Token
	// Disable DNS rebind protection so guests can resolve names that point at
	// private/internal IPs (e.g. an internal container registry).
	rebindDisabled := false
	network := microsandbox.NetworkPolicy.AllowAll()
	network.DNS = &microsandbox.DNSConfig{RebindProtection: &rebindDisabled}
	resources := configuredSandboxResources(r.config)
	options := []microsandbox.SandboxOption{
		microsandbox.WithImageDisk(rootfsDisk.Path, "ext4"),
		microsandbox.WithWorkdir("/"),
		microsandbox.WithShell("/bin/bash"),
		microsandbox.WithEnv(env),
		microsandbox.WithNetwork(network),
		microsandbox.WithPullPolicy(microsandbox.PullPolicyNever),
		microsandbox.WithMounts(mounts),
		microsandbox.WithLabels(map[string]string{microsandboxManagedLabel: "true", microsandboxSandboxIDLabel: session.Summary.ID}),
		microsandbox.WithMemory(resources.MemoryMiB),
		microsandbox.WithCPUs(resources.CPUs),
	}
	if jupyterEnabled(proxyState) && proxyState.HostPort > 0 {
		options = append(options, microsandbox.WithPorts(map[uint16]uint16{uint16(proxyState.HostPort): uint16(proxyState.GuestPort)}))
	}
	sandbox, err := microsandbox.CreateSandbox(ctx, name, options...)
	if err != nil {
		return nil, err
	}
	sandboxCreated = true
	rootfsCreated = false
	return sandbox, nil
}

func (r *microsandboxRuntime) microsandboxBindMount(hostPath string, readonly bool) microsandbox.MountConfig {
	mount := microsandbox.Mount.Bind(hostPath, microsandbox.MountOptions{Readonly: readonly})
	if quotaGB := r.microsandboxBindQuotaGB(); quotaGB > 0 {
		mount.QuotaMiB = uint32(quotaGB) * 1024
	}
	return mount
}

func (r *microsandboxRuntime) microsandboxBindQuotaGB() int32 {
	return configuredSandboxResources(r.config).DiskSizeGB
}

func (r *microsandboxRuntime) launchJupyter(ctx context.Context, sandbox *microsandbox.Sandbox, proxyState ProxyState) error {
	// Use the launch command WITHOUT the directory-only bootstrap: the guest
	// sandbox bootstrap already ran unconditionally in ensureDirectoryOnlyGuestSandboxBootstrap
	// before this call, so re-running it here is redundant. Worse, the embedded
	// bootstrap (~1.5s of symlink setup) can consume the entire observeCtx window
	// below before the script reaches "nohup jupyterlab", so closing the exec
	// handle tears the still-bootstrapping process down and jupyter never starts.
	command := jupyterLaunchCommand(r.config, proxyState, true)
	// Run from "/" (not GuestWorkspacePath): /workspace is a symlink created by the
	// earlier bootstrap, so cwd=/workspace could fail chdir if anything is off.
	// Matches the boxlite driver; jupyter still serves /workspace via --ServerApp.root_dir.
	handle, err := sandbox.ExecStream(ctx, "/bin/bash", []string{"-lc", command}, r.execOptions(ctx, ExecSpec{Cwd: "/"})...)
	if err != nil {
		return err
	}
	defer r.closeMicrosandboxExecHandle(handle)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	observeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	for {
		recvCtx, recvCancel := context.WithTimeout(observeCtx, 250*time.Millisecond)
		event, recvErr := handle.Recv(recvCtx)
		recvCancel()
		if recvErr != nil {
			if errors.Is(recvErr, context.Canceled) || errors.Is(recvErr, context.DeadlineExceeded) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if observeCtx.Err() != nil {
					return nil
				}
				continue
			}
			return recvErr
		}
		if event == nil || event.Kind == microsandbox.ExecEventDone {
			return nil
		}
		switch event.Kind {
		case microsandbox.ExecEventStdout:
			stdout.Write(event.Data)
		case microsandbox.ExecEventStderr:
			stderr.Write(event.Data)
		case microsandbox.ExecEventFailed:
			return formatMicrosandboxExecFailure(event.Failure)
		case microsandbox.ExecEventExited:
			if event.ExitCode != 0 {
				message := strings.TrimSpace(firstNonEmpty(stderr.String(), stdout.String(), fmt.Sprintf("exit code %d", event.ExitCode)))
				return fmt.Errorf("launch jupyter in microsandbox: %s", message)
			}
			return nil
		}
	}
}

func (r *microsandboxRuntime) closeMicrosandboxExecHandle(handle *microsandbox.ExecHandle) {
	if handle == nil {
		return
	}
	go func() {
		if err := handle.Close(); err != nil {
			slog.Warn("failed to close microsandbox exec handle", "error", err)
		}
	}()
}

func (r *microsandboxRuntime) execOptions(ctx context.Context, spec ExecSpec) []microsandbox.ExecOption {
	options := make([]microsandbox.ExecOption, 0, 3)
	if strings.TrimSpace(spec.Cwd) != "" {
		options = append(options, microsandbox.WithExecCwd(spec.Cwd))
	}
	if len(spec.Env) > 0 {
		options = append(options, microsandbox.WithExecEnv(spec.Env))
	}
	if deadline, ok := ctx.Deadline(); ok {
		if timeout := time.Until(deadline); timeout > 0 {
			options = append(options, microsandbox.WithExecTimeout(timeout))
		}
	}
	return options
}

func (r *microsandboxRuntime) sandboxName(session *Sandbox, vmState VMState) string {
	return firstNonEmpty(strings.TrimSpace(vmState.BoxName), strings.TrimSpace(vmState.BoxID), strings.TrimSpace(session.Summary.RuntimeRef), "agent-compose-"+session.Summary.ID)
}

func microsandboxStatsFromMetrics(session *Sandbox, vmState VMState, metrics *microsandbox.Metrics) SandboxStats {
	sandboxID := ""
	driverName := RuntimeDriverMicrosandbox
	if session != nil {
		sandboxID = session.Summary.ID
		driverName = firstNonEmpty(session.Summary.Driver, driverName)
	}
	if metrics == nil {
		return unknownSandboxStats(sandboxID, firstNonEmpty(driverName, vmState.Driver, RuntimeDriverMicrosandbox), "microsandbox metrics are unavailable")
	}
	stats := SandboxStats{
		SandboxID:        sandboxID,
		Driver:           firstNonEmpty(driverName, vmState.Driver, RuntimeDriverMicrosandbox),
		SampledAt:        time.Now().UTC(),
		CPUPercent:       metricOK(metrics.CPUPercent, MetricUnitPercent),
		MemoryUsageBytes: metricOK(float64(metrics.MemoryBytes), MetricUnitBytes),
		MemoryLimitBytes: metricOK(float64(metrics.MemoryLimitBytes), MetricUnitBytes),
		MemoryPercent:    metricUnknown(MetricUnitPercent, "memory limit is unknown"),
		NetworkRxBytes:   metricOK(float64(metrics.NetRxBytes), MetricUnitBytes),
		NetworkTxBytes:   metricOK(float64(metrics.NetTxBytes), MetricUnitBytes),
		BlockReadBytes:   metricOK(float64(metrics.DiskReadBytes), MetricUnitBytes),
		BlockWriteBytes:  metricOK(float64(metrics.DiskWriteBytes), MetricUnitBytes),
		UptimeSeconds:    metricOK(metrics.Uptime.Seconds(), MetricUnitSeconds),
	}
	if metrics.MemoryLimitBytes > 0 {
		stats.MemoryPercent = metricOK(float64(metrics.MemoryBytes)/float64(metrics.MemoryLimitBytes)*100, MetricUnitPercent)
	}
	return stats
}

func (r *microsandboxRuntime) releaseSandboxHandle(name string, sandbox *microsandbox.Sandbox) {
	if sandbox == nil {
		return
	}
	if sandbox.OwnsLifecycleOrFalse() && r.isTrackedLifecycleHandle(name, sandbox) {
		return
	}
	r.closeSandboxHandle(sandbox)
}

func (r *microsandboxRuntime) trackLifecycleHandle(name string, sandbox *microsandbox.Sandbox) {
	if sandbox == nil || !sandbox.OwnsLifecycleOrFalse() {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	r.lifecycleMu.Lock()
	previous := r.lifecycleHandles[name]
	r.lifecycleHandles[name] = sandbox
	r.lifecycleMu.Unlock()
	if previous != nil && previous != sandbox {
		r.closeSandboxHandle(previous)
	}
}

func (r *microsandboxRuntime) takeLifecycleHandle(name string) *microsandbox.Sandbox {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	handle := r.lifecycleHandles[name]
	delete(r.lifecycleHandles, name)
	return handle
}

func (r *microsandboxRuntime) discardLifecycleHandle(name string) {
	if handle := r.takeLifecycleHandle(name); handle != nil {
		r.closeSandboxHandle(handle)
	}
}

func (r *microsandboxRuntime) isTrackedLifecycleHandle(name string, sandbox *microsandbox.Sandbox) bool {
	name = strings.TrimSpace(name)
	if name == "" || sandbox == nil {
		return false
	}
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	return r.lifecycleHandles[name] == sandbox
}

func (r *microsandboxRuntime) closeSandboxHandle(sandbox *microsandbox.Sandbox) {
	if sandbox == nil {
		return
	}
	if err := sandbox.Close(); err != nil && !microsandbox.IsKind(err, microsandbox.ErrInvalidHandle) {
		slog.Warn("failed to close microsandbox handle", "error", err)
	}
}

func formatMicrosandboxExecFailure(failure *microsandbox.ExecFailure) error {
	if failure == nil {
		return fmt.Errorf("execute command failed")
	}
	message := strings.TrimSpace(failure.Message)
	if message == "" {
		message = strings.TrimSpace(firstNonEmpty(failure.Kind, failure.Path))
	}
	if message == "" {
		message = "execute command failed"
	}
	return fmt.Errorf("%s", message)
}

func ensureJSONObject(root map[string]any, key string) map[string]any {
	if existing, ok := root[key]; ok {
		if typed, ok := existing.(map[string]any); ok {
			return typed
		}
	}
	created := map[string]any{}
	root[key] = created
	return created
}

func jsonStringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
