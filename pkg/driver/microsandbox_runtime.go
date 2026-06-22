//go:build cgo

package driver

import (
	appconfig "agent-compose/pkg/config"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

type microsandboxRuntime struct {
	config *appconfig.Config

	initMu sync.Mutex
	ready  bool

	lifecycleMu      sync.Mutex
	lifecycleHandles map[string]*microsandbox.Sandbox
}

type microsandboxExecCollector struct {
	stream ExecStreamWriter
	filter *execOutputFilter
	stdout bytes.Buffer
	stderr bytes.Buffer
	output bytes.Buffer
}

func newMicrosandboxRuntime(config *appconfig.Config) (BoxRuntime, error) {
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
	if chunk.IsStderr {
		c.stderr.WriteString(chunk.Text)
		return
	}
	c.stdout.WriteString(chunk.Text)
}

func (r *microsandboxRuntime) EnsureSession(ctx context.Context, session *Session, vmState VMState, proxyState ProxyState) (SessionVMInfo, error) {
	name := r.sandboxName(session, vmState)
	if err := r.ensureReady(ctx); err != nil {
		return SessionVMInfo{}, err
	}

	sandbox, created, restarted, err := r.getOrCreateSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		return SessionVMInfo{}, err
	}
	defer r.releaseSandboxHandle(name, sandbox)

	needLaunch := created || restarted
	if !needLaunch {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		probeErr := waitForJupyterProxy(probeCtx, proxyState)
		cancel()
		needLaunch = probeErr != nil
	}
	if needLaunch {
		if err := r.launchJupyter(ctx, sandbox, proxyState); err != nil {
			return SessionVMInfo{}, err
		}
	}
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	readyErr := waitForJupyterProxy(readyCtx, proxyState)
	cancel()
	if readyErr != nil {
		if logText := readSessionJupyterLog(session); jupyterLogIndicatesReady(logText) {
			slog.Warn("microsandbox jupyter probe timed out after guest reported ready", "session_id", session.Summary.ID, "error", readyErr)
		} else if logText != "" {
			return SessionVMInfo{}, fmt.Errorf("%w\nGuest log:\n%s", readyErr, logText)
		} else {
			return SessionVMInfo{}, readyErr
		}
	}
	return SessionVMInfo{
		BoxID:      name,
		JupyterURL: jupyterDirectURL(proxyState),
	}, nil
}

func (r *microsandboxRuntime) StopSession(ctx context.Context, session *Session, vmState VMState) (bool, error) {
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

func (r *microsandboxRuntime) Exec(ctx context.Context, session *Session, vmState VMState, spec ExecSpec) (ExecResult, error) {
	name := r.sandboxName(session, vmState)
	if err := r.ensureReady(ctx); err != nil {
		return ExecResult{}, err
	}

	sandbox, err := r.connectSandbox(ctx, session, vmState, true)
	if err != nil {
		return ExecResult{}, err
	}
	defer r.releaseSandboxHandle(name, sandbox)
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
}

func (r *microsandboxRuntime) ExecStream(ctx context.Context, session *Session, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	name := r.sandboxName(session, vmState)
	if err := r.ensureReady(ctx); err != nil {
		return ExecResult{}, err
	}

	sandbox, err := r.connectSandbox(ctx, session, vmState, true)
	if err != nil {
		return ExecResult{}, err
	}
	defer r.releaseSandboxHandle(name, sandbox)
	handle, err := sandbox.ExecStream(ctx, spec.Command, spec.Args, r.execOptions(ctx, spec)...)
	if err != nil {
		return ExecResult{}, err
	}
	defer func() { _ = handle.Close() }()

	collector := &microsandboxExecCollector{stream: stream, filter: newExecOutputFilter()}
	exitCode := 0
	sawExit := false
	for {
		event, err := handle.Recv(ctx)
		if err != nil {
			collector.finish()
			return ExecResult{}, err
		}
		if event == nil || event.Kind == microsandbox.ExecEventDone {
			break
		}
		switch event.Kind {
		case microsandbox.ExecEventStdout:
			collector.writeChunk(ExecChunk{Text: string(event.Data)})
		case microsandbox.ExecEventStderr:
			collector.writeChunk(ExecChunk{Text: string(event.Data), IsStderr: true})
		case microsandbox.ExecEventExited:
			exitCode = event.ExitCode
			sawExit = true
		case microsandbox.ExecEventFailed:
			collector.finish()
			return ExecResult{}, formatMicrosandboxExecFailure(event.Failure)
		case microsandbox.ExecEventStdinError:
			collector.writeChunk(ExecChunk{Text: formatMicrosandboxExecFailure(event.Failure).Error() + "\n", IsStderr: true})
		}
	}
	collector.finish()
	if !sawExit {
		exitCode = 0
	}

	result := ExecResult{
		ExitCode: exitCode,
		Stdout:   collector.stdout.String(),
		Stderr:   collector.stderr.String(),
		Output:   collector.output.String(),
	}
	result.Success = result.ExitCode == 0
	return result, nil
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
	libkrunfwPath := r.resolveLibkrunfwPath()
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

func (r *microsandboxRuntime) resolveLibkrunfwPath() string {
	libDir := filepath.Dir(r.config.MicrosandboxLibPath)
	for _, name := range []string{"libkrunfw.so.5.2.1", "libkrunfw.so.5", "libkrunfw.so"} {
		path := filepath.Join(libDir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
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

func (r *microsandboxRuntime) IsSessionAlive(ctx context.Context, session *Session, vmState VMState) (bool, error) {
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

func (r *microsandboxRuntime) getOrCreateSandbox(ctx context.Context, session *Session, vmState VMState, proxyState ProxyState) (*microsandbox.Sandbox, bool, bool, error) {
	name := r.sandboxName(session, vmState)
	handle, err := microsandbox.GetSandbox(ctx, name)
	if err != nil {
		if !microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
			return nil, false, false, err
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

func (r *microsandboxRuntime) connectSandbox(ctx context.Context, session *Session, vmState VMState, startIfStopped bool) (*microsandbox.Sandbox, error) {
	name := r.sandboxName(session, vmState)
	handle, err := microsandbox.GetSandbox(ctx, name)
	if err != nil {
		if microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
			return nil, fmt.Errorf("session box is not initialized")
		}
		return nil, err
	}
	if handle.Status() == microsandbox.SandboxStatusRunning || handle.Status() == microsandbox.SandboxStatusDraining {
		sandbox, stale, err := r.connectLiveSandbox(ctx, handle, name)
		if err != nil {
			return nil, err
		}
		if stale {
			return nil, fmt.Errorf("session box is not initialized")
		}
		return sandbox, nil
	}
	if !startIfStopped {
		return nil, fmt.Errorf("session box is not running")
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

func (r *microsandboxRuntime) createSandbox(ctx context.Context, session *Session, vmState VMState, proxyState ProxyState, name string) (*microsandbox.Sandbox, error) {
	appconfig.ApplyDefaultGuestPaths(r.config)
	manifest, err := loadDirectoryRuntimeMountManifest(session, RuntimeDriverMicrosandbox)
	if err != nil {
		return nil, err
	}
	mounts := make(map[string]microsandbox.MountConfig, len(manifest.Mounts))
	for _, mount := range manifest.Mounts {
		mounts[mount.GuestPath] = microsandbox.Mount.Bind(mount.HostPath, microsandbox.MountOptions{Readonly: mount.ReadOnly})
	}
	hostPort := uint16(proxyState.HostPort)
	guestPort := uint16(r.config.JupyterGuestPort)
	imageRef := resolveSessionGuestImage(vmState.Image, session.Summary.GuestImage, defaultGuestImageForDriver(r.config, RuntimeDriverMicrosandbox))
	if resolvedRef, ok, err := r.resolveMicrosandboxImageRef(ctx, imageRef); err != nil {
		return nil, err
	} else if ok {
		imageRef = resolvedRef
	}
	env := sessionEnvMap(session.EnvItems)
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
	pullPolicy := microsandboxPullPolicyForImageRef(imageRef)
	sandbox, err := microsandbox.CreateSandbox(ctx, name,
		microsandbox.WithImage(imageRef),
		microsandbox.WithWorkdir("/"),
		microsandbox.WithShell("/bin/bash"),
		microsandbox.WithEnv(env),
		microsandbox.WithNetwork(microsandbox.NetworkPolicy.AllowAll()),
		microsandbox.WithPullPolicy(pullPolicy),
		microsandbox.WithMounts(mounts),
		microsandbox.WithPorts(map[uint16]uint16{hostPort: guestPort}),
	)
	return sandbox, err
}

func (r *microsandboxRuntime) resolveMicrosandboxImageRef(ctx context.Context, imageRef string) (string, bool, error) {
	rootfs, ok, err := resolveMicrosandboxRootFS(ctx, imageRef, microsandboxImageResolverOps{
		dockerAvailable: dockerDaemonAvailable,
		dockerMaterialize: func(ctx context.Context, imageRef string) (microsandboxRootFSResult, bool, error) {
			rootfs, ok, err := materializeLocalDockerImageRootfs(ctx, r.config.DataRoot, imageRef)
			if err != nil || !ok {
				return microsandboxRootFSResult{}, ok, err
			}
			return microsandboxRootFSResult{ImageID: rootfs.ImageID, ResolvedRef: rootfs.ResolvedRef, RootFSPath: rootfs.RootfsPath}, true, nil
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (microsandboxRootFSResult, bool, error) {
			return materializeMicrosandboxOCIRootFS(ctx, r.config, imageRef)
		},
	})
	if err != nil {
		return "", false, err
	}
	if ok {
		slog.Info("agent-compose microsandbox using materialized image rootfs", "image", imageRef, "resolved_ref", rootfs.ResolvedRef, "rootfs_path", rootfs.RootFSPath)
		return rootfs.RootFSPath, true, nil
	}
	return "", false, nil
}

func microsandboxPullPolicyForImageRef(imageRef string) microsandbox.PullPolicy {
	if filepath.IsAbs(imageRef) {
		return microsandbox.PullPolicyNever
	}
	return microsandbox.PullPolicyIfMissing
}

func (r *microsandboxRuntime) launchJupyter(ctx context.Context, sandbox *microsandbox.Sandbox, proxyState ProxyState) error {
	command := jupyterLaunchCommand(r.config, proxyState, true)
	handle, err := sandbox.ExecStream(ctx, "/bin/bash", []string{"-lc", command}, r.execOptions(ctx, ExecSpec{Cwd: r.config.GuestWorkspacePath})...)
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

func (r *microsandboxRuntime) sandboxName(session *Session, vmState VMState) string {
	return firstNonEmpty(strings.TrimSpace(vmState.BoxName), strings.TrimSpace(vmState.BoxID), strings.TrimSpace(session.Summary.RuntimeRef), "agent-compose-"+session.Summary.ID)
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
