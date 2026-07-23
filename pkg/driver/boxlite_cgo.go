//go:build linux && cgo && boxlitecgo

package driver

/*
#cgo CFLAGS: -I${SRCDIR}/../../build/boxlite/include
#cgo LDFLAGS: ${SRCDIR}/../../build/boxlite/lib/libboxlite.a -ldl -lpthread -lm
#include <stdint.h>
#include <stdlib.h>
#include "boxlite.h"

extern void agentcomposeBoxliteHandleCallback(CBoxHandle *box, CBoxliteError *err, uintptr_t handle);
extern void agentcomposeBoxliteVoidCallback(CBoxliteError *err, uintptr_t handle);
extern void agentcomposeBoxliteExecStdoutCallback(uint8_t *data, size_t len, uintptr_t handle);
extern void agentcomposeBoxliteExecStderrCallback(uint8_t *data, size_t len, uintptr_t handle);
extern void agentcomposeBoxliteExecWaitCallback(int exit_code, CBoxliteError *err, uintptr_t handle);
extern void agentcomposeBoxliteExecExitCallback(int exit_code, uintptr_t handle);

static void agentcomposeBoxliteHandleCallbackBridge(CBoxHandle *box, CBoxliteError *err, void *user_data) {
	agentcomposeBoxliteHandleCallback(box, err, (uintptr_t)user_data);
}

static void agentcomposeBoxliteVoidCallbackBridge(CBoxliteError *err, void *user_data) {
	agentcomposeBoxliteVoidCallback(err, (uintptr_t)user_data);
}

static void agentcomposeBoxliteExecStdoutCallbackBridge(const uint8_t *data, size_t len, void *user_data) {
	agentcomposeBoxliteExecStdoutCallback((uint8_t *)data, len, (uintptr_t)user_data);
}

static void agentcomposeBoxliteExecStderrCallbackBridge(const uint8_t *data, size_t len, void *user_data) {
	agentcomposeBoxliteExecStderrCallback((uint8_t *)data, len, (uintptr_t)user_data);
}

static void agentcomposeBoxliteExecWaitCallbackBridge(int exit_code, CBoxliteError *err, void *user_data) {
	agentcomposeBoxliteExecWaitCallback(exit_code, err, (uintptr_t)user_data);
}

static void agentcomposeBoxliteExecExitCallbackBridge(int exit_code, void *user_data) {
	agentcomposeBoxliteExecExitCallback(exit_code, (uintptr_t)user_data);
}

static enum BoxliteErrorCode agentcompose_boxlite_create_box(
	CBoxliteRuntime *runtime,
	CBoxliteOptions *opts,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	void *user_data = (void *)user_handle;
	return boxlite_create_box(runtime, opts, agentcomposeBoxliteHandleCallbackBridge, user_data, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_get(
	CBoxliteRuntime *runtime,
	const char *id_or_name,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	void *user_data = (void *)user_handle;
	return boxlite_get(runtime, id_or_name, agentcomposeBoxliteHandleCallbackBridge, user_data, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_start_box(
	CBoxHandle *handle,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	void *user_data = (void *)user_handle;
	return boxlite_start_box(handle, agentcomposeBoxliteVoidCallbackBridge, user_data, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_remove(
	CBoxliteRuntime *runtime,
	const char *id_or_name,
	int force,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	void *user_data = (void *)user_handle;
	return boxlite_remove(runtime, id_or_name, force, agentcomposeBoxliteVoidCallbackBridge, user_data, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_stop_box(
	CBoxHandle *handle,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	void *user_data = (void *)user_handle;
	return boxlite_stop_box(handle, agentcomposeBoxliteVoidCallbackBridge, user_data, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_execution_on_stdout(
	CExecutionHandle *execution,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	void *user_data = (void *)user_handle;
	return boxlite_execution_on_stdout(execution, agentcomposeBoxliteExecStdoutCallbackBridge, user_data, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_execution_on_stderr(
	CExecutionHandle *execution,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	void *user_data = (void *)user_handle;
	return boxlite_execution_on_stderr(execution, agentcomposeBoxliteExecStderrCallbackBridge, user_data, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_execution_wait(
	CExecutionHandle *execution,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	void *user_data = (void *)user_handle;
	return boxlite_execution_wait(execution, agentcomposeBoxliteExecWaitCallbackBridge, user_data, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_execution_on_exit(
	CExecutionHandle *execution,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	void *user_data = (void *)user_handle;
	return boxlite_execution_on_exit(execution, agentcomposeBoxliteExecExitCallbackBridge, user_data, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_execution_kill(
	CExecutionHandle *execution,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	void *user_data = (void *)user_handle;
	return boxlite_execution_kill(execution, agentcomposeBoxliteVoidCallbackBridge, user_data, out_error);
}
*/
import "C"

import (
	appconfig "agent-compose/pkg/config"
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"
)

type cgoSandboxRuntime struct {
	config   *appconfig.Config
	mu       sync.Mutex
	ensureMu sync.Mutex
	rt       *C.CBoxliteRuntime
}

type cgoBoxHandle struct {
	ptr *C.CBoxHandle
}

type cgoBoxInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	State struct {
		Status  string `json:"status"`
		Running bool   `json:"running"`
	} `json:"state"`
}

type cgoExecCollector struct {
	stream ExecStreamWriter
	filter *execOutputFilter
	stdout bytes.Buffer
	stderr bytes.Buffer
	output bytes.Buffer
}

type boxliteHandleResult struct {
	ptr *C.CBoxHandle
	err error
}

type boxliteExecWaitResult struct {
	exitCode int
	err      error
}

type boxliteHandleAwaiter struct {
	ch chan boxliteHandleResult
}

type boxliteVoidAwaiter struct {
	ch chan error
}

type boxliteExecAwaiter struct {
	collector *cgoExecCollector
	waitCh    chan boxliteExecWaitResult
	exitCh    chan int
	outputCh  chan struct{}
}

type boxliteAwaiterRegistry struct {
	mu    sync.Mutex
	next  uintptr
	items map[uintptr]any
}

var globalBoxliteAwaiters boxliteAwaiterRegistry

func (r *boxliteAwaiterRegistry) register(awaiter any) uintptr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = make(map[uintptr]any)
		r.next = 1
	}
	handle := r.next
	r.next++
	if handle == 0 {
		handle = r.next
		r.next++
	}
	r.items[handle] = awaiter
	return handle
}

func (r *boxliteAwaiterRegistry) lookup(handle uintptr) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	awaiter, ok := r.items[handle]
	return awaiter, ok
}

func (r *boxliteAwaiterRegistry) delete(handle uintptr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, handle)
}

func lookupHandleAwaiter(handle uintptr) (*boxliteHandleAwaiter, bool) {
	awaiter, ok := globalBoxliteAwaiters.lookup(handle)
	if !ok {
		return nil, false
	}
	typed, ok := awaiter.(*boxliteHandleAwaiter)
	return typed, ok
}

func lookupVoidAwaiter(handle uintptr) (*boxliteVoidAwaiter, bool) {
	awaiter, ok := globalBoxliteAwaiters.lookup(handle)
	if !ok {
		return nil, false
	}
	typed, ok := awaiter.(*boxliteVoidAwaiter)
	return typed, ok
}

func lookupExecAwaiter(handle uintptr) (*boxliteExecAwaiter, bool) {
	awaiter, ok := globalBoxliteAwaiters.lookup(handle)
	if !ok {
		return nil, false
	}
	typed, ok := awaiter.(*boxliteExecAwaiter)
	return typed, ok
}

func (c *cgoExecCollector) writeChunk(chunk ExecChunk) {
	if c.filter == nil {
		c.appendChunk(chunk)
		return
	}
	c.filter.Write(chunk, c.appendChunk)
}

func (c *cgoExecCollector) finish() {
	if c.filter == nil {
		return
	}
	c.filter.Finish(c.appendChunk)
}

func (c *cgoExecCollector) appendChunk(chunk ExecChunk) {
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

func newSandboxRuntime(config *appconfig.Config) (SandboxRuntime, error) {
	prependEnvPath("BOXLITE_RUNTIME_DIR", config.BoxliteRuntimeDir)
	prependEnvPath("LD_LIBRARY_PATH", filepath.Join(".", "build", "boxlite", "lib"))
	return &cgoSandboxRuntime{config: config}, nil
}

//export agentcomposeBoxliteHandleCallback
func agentcomposeBoxliteHandleCallback(box *C.CBoxHandle, ffiErr *C.CBoxliteError, handle C.uintptr_t) {
	awaiter, ok := lookupHandleAwaiter(uintptr(handle))
	if !ok {
		return
	}
	awaiter.ch <- boxliteHandleResult{ptr: box, err: boxliteAsyncError(ffiErr, "boxlite async operation")}
}

//export agentcomposeBoxliteVoidCallback
func agentcomposeBoxliteVoidCallback(ffiErr *C.CBoxliteError, handle C.uintptr_t) {
	awaiter, ok := lookupVoidAwaiter(uintptr(handle))
	if !ok {
		return
	}
	awaiter.ch <- boxliteAsyncError(ffiErr, "boxlite async operation")
}

//export agentcomposeBoxliteExecStdoutCallback
func agentcomposeBoxliteExecStdoutCallback(data *C.uint8_t, length C.size_t, handle C.uintptr_t) {
	awaiter, ok := lookupExecAwaiter(uintptr(handle))
	if !ok || awaiter.collector == nil || data == nil || length == 0 {
		return
	}
	awaiter.collector.writeChunk(ExecChunk{Text: string(C.GoBytes(unsafe.Pointer(data), C.int(length)))})
	notifyBoxliteExecOutput(awaiter)
}

//export agentcomposeBoxliteExecStderrCallback
func agentcomposeBoxliteExecStderrCallback(data *C.uint8_t, length C.size_t, handle C.uintptr_t) {
	awaiter, ok := lookupExecAwaiter(uintptr(handle))
	if !ok || awaiter.collector == nil || data == nil || length == 0 {
		return
	}
	awaiter.collector.writeChunk(ExecChunk{Text: string(C.GoBytes(unsafe.Pointer(data), C.int(length))), Stream: StdioStderr})
	notifyBoxliteExecOutput(awaiter)
}

func notifyBoxliteExecOutput(awaiter *boxliteExecAwaiter) {
	if awaiter.outputCh == nil {
		return
	}
	select {
	case awaiter.outputCh <- struct{}{}:
	default:
	}
}

//export agentcomposeBoxliteExecWaitCallback
func agentcomposeBoxliteExecWaitCallback(exitCode C.int, ffiErr *C.CBoxliteError, handle C.uintptr_t) {
	awaiter, ok := lookupExecAwaiter(uintptr(handle))
	if !ok {
		return
	}
	awaiter.waitCh <- boxliteExecWaitResult{exitCode: int(exitCode), err: boxliteAsyncError(ffiErr, "wait for box command")}
}

// agentcomposeBoxliteExecExitCallback fires when the guest process exits, carrying
// its exit code. This is independent of the stdout/stderr streams reaching EOF:
// boxlite_execution_wait only completes once the streams drain, so a lingering
// background process that inherited the exec's stdout/stderr would otherwise
// keep the wait pending until the command timeout. on_exit lets us detect
// completion at process exit, mirroring docker's "close on main-process exit".
//
//export agentcomposeBoxliteExecExitCallback
func agentcomposeBoxliteExecExitCallback(exitCode C.int, handle C.uintptr_t) {
	notifyBoxliteExecExit(int(exitCode), uintptr(handle))
}

func notifyBoxliteExecExit(exitCode int, handle uintptr) {
	awaiter, ok := lookupExecAwaiter(handle)
	if !ok || awaiter.exitCh == nil {
		return
	}
	select {
	case awaiter.exitCh <- exitCode:
	default:
	}
}

func (r *cgoSandboxRuntime) EnsureSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState, proxyState ProxyState) (SandboxVMInfo, error) {
	r.ensureMu.Lock()
	defer r.ensureMu.Unlock()

	startedAt := time.Now()
	slog.Info("agent-compose boxlite ensure sandbox begin", "sandbox_id", sandbox.Summary.ID, "host_port", proxyState.HostPort)
	box, created, err := r.getOrCreateBox(ctx, sandbox, vmState, proxyState)
	if err != nil {
		return SandboxVMInfo{}, err
	}
	defer box.free()
	slog.Info("agent-compose boxlite ensure sandbox box ready", "sandbox_id", sandbox.Summary.ID, "created", created, "elapsed_ms", time.Since(startedAt).Milliseconds())

	if created {
		slog.Info("agent-compose boxlite starting box", "sandbox_id", sandbox.Summary.ID)
		if err := r.startBox(ctx, box); err != nil {
			return SandboxVMInfo{}, err
		}
		slog.Info("agent-compose boxlite box started", "sandbox_id", sandbox.Summary.ID, "elapsed_ms", time.Since(startedAt).Milliseconds())
	}

	if err := r.ensureDirectoryOnlyGuestSandboxBootstrap(ctx, box, sandbox); err != nil {
		return SandboxVMInfo{}, err
	}
	slog.Info("agent-compose boxlite guest bootstrap ready", "sandbox_id", sandbox.Summary.ID, "created", created, "elapsed_ms", time.Since(startedAt).Milliseconds())

	if jupyterEnabled(proxyState) {
		slog.Info("agent-compose boxlite checking jupyter", "sandbox_id", sandbox.Summary.ID, "host_port", proxyState.HostPort)
		if err := waitForJupyterProxy(ctx, proxyState); err != nil {
			if err := waitForJupyterProxy(ctx, proxyState); err != nil {
				logText, _ := r.readJupyterLog(ctx, box)
				if strings.TrimSpace(logText) != "" {
					return SandboxVMInfo{}, fmt.Errorf("%w\nGuest log:\n%s", err, logText)
				}
				return SandboxVMInfo{}, err
			}
		}
		slog.Info("agent-compose boxlite jupyter ready", "sandbox_id", sandbox.Summary.ID, "elapsed_ms", time.Since(startedAt).Milliseconds())
	}

	boxID, err := r.boxID(box)
	if err != nil {
		return SandboxVMInfo{}, err
	}
	slog.Info("agent-compose boxlite ensure sandbox complete", "sandbox_id", sandbox.Summary.ID, "box_id", boxID, "elapsed_ms", time.Since(startedAt).Milliseconds())
	return SandboxVMInfo{
		BoxID:      boxID,
		JupyterURL: jupyterDirectURL(proxyState),
	}, nil
}

func (r *cgoSandboxRuntime) StopSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState) (bool, error) {
	if strings.TrimSpace(vmState.BoxID) == "" {
		if sandbox != nil {
			if err := CleanupBoxliteVolumeBridgeMounts(hostSandboxDir(sandbox)); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	box, err := r.getBox(ctx, vmState.BoxID)
	if err != nil {
		if isBoxNotFound(err) {
			return true, nil
		}
		return false, err
	}
	defer box.free()
	missing, err := classifyBoxliteStopError(r.stopBox(ctx, box))
	if err != nil {
		return false, err
	}
	return missing, nil
}

func (r *cgoSandboxRuntime) RemoveSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState) error {
	lookup := firstNonEmpty(strings.TrimSpace(vmState.BoxID), strings.TrimSpace(vmState.BoxName))
	if sandbox != nil {
		lookup = firstNonEmpty(lookup, strings.TrimSpace(sandbox.Summary.RuntimeRef))
	}
	if lookup != "" {
		if err := r.removeBox(ctx, lookup, true); err != nil && !isBoxNotFound(err) {
			return err
		}
	}
	if sandbox != nil {
		if err := CleanupBoxliteVolumeBridgeMounts(hostSandboxDir(sandbox)); err != nil {
			return err
		}
	}
	return nil
}

func (r *cgoSandboxRuntime) Exec(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec) (ExecResult, error) {
	return r.execWithStream(ctx, sandbox, vmState, spec, nil)
}

func (r *cgoSandboxRuntime) ExecStream(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	return r.execWithStream(ctx, sandbox, vmState, spec, stream)
}

func (r *cgoSandboxRuntime) InteractionCapabilities() RuntimeInteractionCapabilities {
	return RuntimeInteractionCapabilities{}
}

func (r *cgoSandboxRuntime) OpenInteraction(ctx context.Context, sandbox *Sandbox, vmState VMState, spec RuntimeStartSpec) (RuntimeInteraction, error) {
	return UnsupportedRuntimeInteraction(RuntimeDriverBoxlite, r.InteractionCapabilities(), spec)
}

func (r *cgoSandboxRuntime) Stats(_ context.Context, sandbox *Sandbox, vmState VMState) (SandboxStats, error) {
	sandboxID := ""
	driverName := RuntimeDriverBoxlite
	if sandbox != nil {
		sandboxID = sandbox.Summary.ID
		driverName = firstNonEmpty(sandbox.Summary.Driver, driverName)
	}
	return unknownSandboxStats(
		sandboxID,
		firstNonEmpty(driverName, vmState.Driver, RuntimeDriverBoxlite),
		"boxlite metrics are not exposed by the current runtime wrapper",
	), nil
}

func (r *cgoSandboxRuntime) execWithStream(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	if strings.TrimSpace(vmState.BoxID) == "" {
		return ExecResult{}, fmt.Errorf("sandbox box is not initialized")
	}
	box, err := r.getBox(ctx, vmState.BoxID)
	if err != nil {
		return ExecResult{}, err
	}
	defer box.free()

	info, err := r.boxInfo(box)
	if err != nil {
		return ExecResult{}, err
	}
	if !info.State.Running {
		if err := r.startBox(ctx, box); err != nil {
			return ExecResult{}, err
		}
	}
	return executeUserCommandAfterBootstrap(
		func() error {
			return r.ensureDirectoryOnlyGuestSandboxBootstrap(ctx, box, sandbox)
		},
		func() (ExecResult, error) {
			return r.executeBox(ctx, box, spec, stream)
		},
	)
}

func (r *cgoSandboxRuntime) ensureDirectoryOnlyGuestSandboxBootstrap(ctx context.Context, box *cgoBoxHandle, sandbox *Sandbox) error {
	boxID := ""
	if id, err := r.boxID(box); err == nil {
		boxID = id
	}
	sandboxID := ""
	if sandbox != nil {
		sandboxID = sandbox.Summary.ID
	}
	result, err := r.executeBox(ctx, box, directoryOnlyGuestSandboxBootstrapExecSpecForSandbox(r.config, sandbox), nil)
	if err != nil {
		return formatDirectoryOnlyGuestSandboxBootstrapError(RuntimeDriverBoxlite, sandboxID, boxID, result, err)
	}
	if !result.Success {
		return formatDirectoryOnlyGuestSandboxBootstrapError(RuntimeDriverBoxlite, sandboxID, boxID, result, nil)
	}
	return nil
}

func (r *cgoSandboxRuntime) runtimeHandle() (*C.CBoxliteRuntime, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rt != nil {
		return r.rt, nil
	}

	homeDir := C.CString(r.config.BoxliteHome)
	defer C.free(unsafe.Pointer(homeDir))
	var registry C.struct_BoxliteImageRegistry
	var registryHost *C.char
	registryCount := C.int(0)
	if host, transport := parseBoxliteRegistry(r.config.ImageRegistry); host != "" {
		registryHost = C.CString(host)
		defer C.free(unsafe.Pointer(registryHost))
		registry = C.struct_BoxliteImageRegistry{
			host:      registryHost,
			transport: transport,
			search:    1,
		}
		registryCount = 1
	}

	var runtimeHandle *C.CBoxliteRuntime
	var ffiErr C.CBoxliteError
	code := C.boxlite_runtime_new(homeDir, &registry, registryCount, &runtimeHandle, &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "create runtime"); err != nil {
		return nil, err
	}
	r.rt = runtimeHandle
	return r.rt, nil
}

func parseBoxliteRegistry(value string) (string, C.enum_BoxliteRegistryTransport) {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return "", C.BoxliteRegistryTransportHttps
	}
	transport := C.enum_BoxliteRegistryTransport(C.BoxliteRegistryTransportHttps)
	switch {
	case strings.HasPrefix(cleaned, "http://"):
		transport = C.enum_BoxliteRegistryTransport(C.BoxliteRegistryTransportHttp)
		cleaned = strings.TrimPrefix(cleaned, "http://")
	case strings.HasPrefix(cleaned, "https://"):
		cleaned = strings.TrimPrefix(cleaned, "https://")
	}
	cleaned = strings.TrimPrefix(cleaned, "//")
	cleaned = strings.TrimRight(cleaned, "/")
	return cleaned, transport
}

func (r *cgoSandboxRuntime) resolveRootfsPath(ctx context.Context, imageRef string, pullPolicy string, pullTimeout time.Duration) (string, []string, error) {
	if strings.TrimSpace(r.config.BoxRootfsPath) != "" {
		return r.config.BoxRootfsPath, nil, nil
	}
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return "", nil, nil
	}
	layout, ok, err := r.materializeLocalImageRootfs(ctx, imageRef, pullPolicy, pullTimeout)
	if err != nil {
		return "", nil, err
	}
	if ok {
		slog.Info("agent-compose boxlite using materialized local image rootfs", "image", imageRef, "resolved_ref", layout.ResolvedRef, "rootfs_path", layout.RootfsPath)
		return layout.RootfsPath, layout.Env, nil
	}
	return "", nil, nil
}

func (r *cgoSandboxRuntime) materializeLocalImageRootfs(ctx context.Context, imageRef string, pullPolicy string, pullTimeout time.Duration) (localDockerImageLayout, bool, error) {
	layout, ok, err := resolveBoxliteImageLayout(ctx, imageRef, boxliteImageResolverOps{
		dockerAvailable: dockerDaemonAvailable,
		applyDockerPullPolicy: func(ctx context.Context, imageRef string) error {
			return applyDockerDaemonPullPolicy(ctx, imageRef, pullPolicy, pullTimeout)
		},
		dockerMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			layout, ok, err := materializeLocalDockerImageLayout(ctx, r.config.DataRoot, imageRef)
			if err != nil || !ok {
				return boxliteImageLayoutResult{}, ok, err
			}
			return boxliteImageLayoutResult{ImageID: layout.ImageID, ResolvedRef: layout.ResolvedRef, RootfsPath: layout.RootfsPath, Env: layout.Env}, true, nil
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			return materializeBoxliteOCIImageLayout(ctx, r.config, imageRef, pullPolicy)
		},
	})
	if err != nil || !ok {
		return localDockerImageLayout{}, ok, err
	}
	return localDockerImageLayout{ImageID: layout.ImageID, ResolvedRef: layout.ResolvedRef, RootfsPath: layout.RootfsPath, Env: layout.Env}, true, nil
}

func untarInto(dst string, src io.Reader) error {
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || name == "/" {
			continue
		}
		if strings.HasPrefix(name, "../") || name == ".." {
			return fmt.Errorf("tar entry escapes rootfs: %s", hdr.Name)
		}
		target := filepath.Join(dst, name)
		rel, err := filepath.Rel(dst, target)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rel, "..") {
			return fmt.Errorf("tar entry escapes rootfs: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, tr); err != nil {
				_ = file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		case tar.TypeLink:
			if err := os.Link(filepath.Join(dst, filepath.Clean(hdr.Linkname)), target); err != nil {
				return err
			}
		default:
			continue
		}
	}
}

func (r *cgoSandboxRuntime) getOrCreateBox(ctx context.Context, sandbox *Sandbox, vmState VMState, proxyState ProxyState) (*cgoBoxHandle, bool, error) {
	if existingID := strings.TrimSpace(vmState.BoxID); existingID != "" {
		box, err := r.getBox(ctx, existingID)
		if err == nil {
			info, infoErr := r.boxInfo(box)
			if infoErr == nil {
				status := normalizeBoxliteStatus(info.State.Status)
				if shouldRecreateBoxForStatus(status) {
					box.free()
					if !vmState.StoppedAt.IsZero() {
						return nil, false, fmt.Errorf("boxlite runtime state for stopped sandbox %s is not resumable from status %s", sandbox.Summary.ID, status)
					}
					if err := r.removeBox(ctx, existingID, true); err != nil && !isBoxNotFound(err) {
						return nil, false, err
					}
				} else {
					if !info.State.Running {
						if err := r.startBox(ctx, box); err != nil {
							box.free()
							return nil, false, err
						}
					}
					return box, false, nil
				}
			} else {
				return box, false, nil
			}
		}
		if !isBoxNotFound(err) {
			return nil, false, err
		}
	}
	if !vmState.StoppedAt.IsZero() {
		return nil, false, fmt.Errorf("boxlite runtime state for stopped sandbox %s is missing; refusing to recreate it during resume", sandbox.Summary.ID)
	}

	box, err := r.createBox(ctx, sandbox, vmState, proxyState)
	if err != nil {
		return nil, false, err
	}
	select {
	case <-ctx.Done():
		box.free()
		return nil, false, ctx.Err()
	default:
	}
	return box, true, nil
}

func normalizeBoxliteStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func shouldRecreateBoxForStatus(status string) bool {
	switch status {
	case "failed", "dead", "removed", "exited":
		return true
	default:
		return false
	}
}

func cStringArray(values []string) (**C.char, int, func()) {
	if len(values) == 0 {
		return nil, 0, func() {}
	}
	out := make([]*C.char, 0, len(values))
	for _, value := range values {
		out = append(out, C.CString(value))
	}
	raw := C.malloc(C.size_t(len(out)) * C.size_t(unsafe.Sizeof(uintptr(0))))
	array := unsafe.Slice((**C.char)(raw), len(out))
	copy(array, out)
	return (**C.char)(raw), len(out), func() {
		for _, value := range out {
			C.free(unsafe.Pointer(value))
		}
		C.free(raw)
	}
}

func cStringOrEmpty(value *C.char) string {
	if value == nil {
		return ""
	}
	return C.GoString(value)
}

func boxliteAsyncError(ffiErr *C.CBoxliteError, action string) error {
	if ffiErr == nil {
		return nil
	}
	if ffiErr.code == C.Ok {
		C.boxlite_error_free(ffiErr)
		return nil
	}
	message := action
	if ffiErr.message != nil {
		message = fmt.Sprintf("%s: %s", action, C.GoString(ffiErr.message))
	}
	code := int(ffiErr.code)
	C.boxlite_error_free(ffiErr)
	return &boxliteCallError{code: code, message: message}
}

func boxliteDrainError(ffiErr *C.CBoxliteError, action string) error {
	if ffiErr == nil {
		return fmt.Errorf("%s: unknown boxlite runtime drain error", action)
	}
	if err := boxliteAsyncError(ffiErr, action); err != nil {
		return err
	}
	return fmt.Errorf("%s: unknown boxlite runtime drain error", action)
}

func (r *cgoSandboxRuntime) drainRuntimeCallbacks(ctx context.Context, runtimeHandle *C.CBoxliteRuntime, timeout time.Duration) (int, error) {
	if runtimeHandle == nil {
		return 0, fmt.Errorf("boxlite runtime is not initialized")
	}
	if timeout < 0 {
		timeout = 0
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, ctx.Err()
		}
		if timeout == 0 || remaining < timeout {
			timeout = remaining
		}
	}
	timeoutMs := timeout.Milliseconds()
	if timeout > 0 && timeoutMs == 0 {
		timeoutMs = 1
	}
	var ffiErr C.CBoxliteError
	events := C.boxlite_runtime_drain(runtimeHandle, C.int(timeoutMs), &ffiErr)
	if events < 0 {
		return 0, boxliteDrainError(&ffiErr, "drain boxlite callbacks")
	}
	if err := ctx.Err(); err != nil {
		return int(events), err
	}
	return int(events), nil
}

func (r *cgoSandboxRuntime) flushRuntimeCallbacks(runtimeHandle *C.CBoxliteRuntime) {
	if runtimeHandle == nil {
		return
	}
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		events, err := r.drainRuntimeCallbacks(ctx, runtimeHandle, 0)
		if err != nil {
			slog.Warn("failed to flush boxlite callbacks", "error", err)
			return
		}
		if events == 0 {
			return
		}
	}
}

func (r *cgoSandboxRuntime) waitForHandleResult(ctx context.Context, runtimeHandle *C.CBoxliteRuntime, ch <-chan boxliteHandleResult, action string) (*cgoBoxHandle, error) {
	for {
		select {
		case result := <-ch:
			if result.err != nil {
				return nil, result.err
			}
			if result.ptr == nil {
				return nil, fmt.Errorf("%s: boxlite returned empty handle", action)
			}
			return &cgoBoxHandle{ptr: result.ptr}, nil
		default:
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := r.drainRuntimeCallbacks(ctx, runtimeHandle, 100*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

func (r *cgoSandboxRuntime) waitForVoidResult(ctx context.Context, runtimeHandle *C.CBoxliteRuntime, ch <-chan error) error {
	for {
		select {
		case err := <-ch:
			return err
		default:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := r.drainRuntimeCallbacks(ctx, runtimeHandle, 100*time.Millisecond); err != nil {
			return err
		}
	}
}

// boxliteExecExitIdleGracePeriod bounds how long executeBox waits after the
// guest process exits and output callbacks go idle, while boxlite_execution_wait
// still has not reported stream completion. If output keeps draining, the idle
// deadline is extended so high-output commands are not cut off. The fallback is
// only for lingering descendants that keep stdout/stderr open without producing
// more output.
const boxliteExecExitIdleGracePeriod = 2 * time.Second

func (r *cgoSandboxRuntime) waitForExecCompletion(ctx context.Context, runtimeHandle *C.CBoxliteRuntime, awaiter *boxliteExecAwaiter) (int, error) {
	return waitForExecCompletion(ctx, awaiter, boxliteExecExitIdleGracePeriod, func(timeout time.Duration) error {
		_, err := r.drainRuntimeCallbacks(ctx, runtimeHandle, timeout)
		return err
	})
}

func (r *cgoSandboxRuntime) killExecution(ctx context.Context, runtimeHandle *C.CBoxliteRuntime, execution *C.CExecutionHandle) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), execTerminationTimeout)
	defer cancel()

	awaiter := &boxliteVoidAwaiter{ch: make(chan error, 1)}
	awaiterHandle := globalBoxliteAwaiters.register(awaiter)
	defer globalBoxliteAwaiters.delete(awaiterHandle)
	var ffiErr C.CBoxliteError
	code := C.agentcompose_boxlite_execution_kill(execution, C.uintptr_t(awaiterHandle), &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "kill box command"); err != nil {
		return err
	}
	return r.waitForVoidResult(cleanupCtx, runtimeHandle, awaiter.ch)
}

func waitForExecCompletion(ctx context.Context, awaiter *boxliteExecAwaiter, exitIdleGrace time.Duration, drain func(time.Duration) error) (int, error) {
	exited := false
	exitCode := 0
	var idleDeadline time.Time
	for {
		// Authoritative completion: process exited and streams drained.
		select {
		case result := <-awaiter.waitCh:
			return result.exitCode, result.err
		default:
		}
		// Process exit, independent of stream EOF. Start a bounded grace
		// period so trailing output still has a chance to be collected.
		if !exited {
			select {
			case code := <-awaiter.exitCh:
				exited = true
				exitCode = code
				idleDeadline = time.Now().Add(exitIdleGrace)
			default:
			}
		}
		if exited {
			for {
				select {
				case <-awaiter.outputCh:
					idleDeadline = time.Now().Add(exitIdleGrace)
				default:
					goto outputDrained
				}
			}
		}
	outputDrained:
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if exited && !time.Now().Before(idleDeadline) {
			return exitCode, nil
		}
		if err := drain(100 * time.Millisecond); err != nil {
			return 0, err
		}
	}
}

func (r *cgoSandboxRuntime) buildBoxOptions(ctx context.Context, sandbox *Sandbox, vmState VMState, proxyState ProxyState) (*C.CBoxliteOptions, error) {
	appconfig.ApplyDefaultGuestPaths(r.config)
	manifest, err := loadDirectoryRuntimeMountManifest(sandbox, RuntimeDriverBoxlite)
	if err != nil {
		return nil, err
	}
	imageRef := resolveSandboxGuestImage(vmState.Image, sandbox.Summary.GuestImage, r.config.DefaultImage)
	imagePullTimeout := r.config.ImagePullTimeout
	if imagePullTimeout <= 0 {
		imagePullTimeout = defaultImagePullTimeout
	}
	rootfsPath, imageEnv, err := r.resolveRootfsPath(ctx, imageRef, sandbox.Summary.PullPolicy, imagePullTimeout)
	if err != nil {
		return nil, err
	}

	imageArgValue := strings.TrimSpace(imageRef)
	imageArg := C.CString(imageArgValue)
	defer C.free(unsafe.Pointer(imageArg))

	var ffiErr C.CBoxliteError
	var options *C.CBoxliteOptions
	code := C.boxlite_options_new(imageArg, &options, &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "create box options"); err != nil {
		return nil, err
	}

	if strings.TrimSpace(rootfsPath) != "" {
		rootfsCString := C.CString(rootfsPath)
		defer C.free(unsafe.Pointer(rootfsCString))
		C.boxlite_options_set_rootfs_path(options, rootfsCString)
	}
	workdirCString := C.CString("/")
	defer C.free(unsafe.Pointer(workdirCString))
	C.boxlite_options_set_workdir(options, workdirCString)
	C.boxlite_options_set_auto_remove(options, 0)
	C.boxlite_options_set_detach(options, 1)
	C.boxlite_options_set_network_enabled(options)
	resources := configuredSandboxResources(r.config)
	C.boxlite_options_set_cpus(options, C.int(resources.CPUs))
	C.boxlite_options_set_memory(options, C.int(resources.MemoryMiB))
	C.boxlite_options_set_disk_size_gb(options, C.int(resources.DiskSizeGB))

	for _, mount := range manifest.Mounts {
		hostPathCString := C.CString(mount.HostPath)
		guestPathCString := C.CString(mount.GuestPath)
		readOnly := C.int(0)
		if mount.ReadOnly {
			readOnly = 1
		}
		C.boxlite_options_add_volume(options, hostPathCString, guestPathCString, readOnly)
		C.free(unsafe.Pointer(hostPathCString))
		C.free(unsafe.Pointer(guestPathCString))
	}

	if jupyterEnabled(proxyState) && proxyState.HostPort > 0 {
		code := C.boxlite_options_add_port(
			options,
			C.uint16_t(proxyState.HostPort),
			C.uint16_t(proxyState.GuestPort),
			C.BoxlitePortProtocolTcp,
			nil,
		)
		if err := boxliteStatusError(code, nil, "add jupyter port forwarding"); err != nil {
			return nil, err
		}
	}

	entrypoint, entrypointLen, freeEntrypoint := cStringArray([]string{"sh", "-lc"})
	defer freeEntrypoint()
	C.boxlite_options_set_entrypoint(options, entrypoint, C.int(entrypointLen))

	commandText := "sleep infinity"
	if jupyterEnabled(proxyState) {
		commandText = directoryOnlyJupyterLaunchCommand(r.config, proxyState, false)
	}
	command, commandLen, freeCommand := cStringArray([]string{commandText})
	defer freeCommand()
	C.boxlite_options_set_cmd(options, command, C.int(commandLen))

	baseEnv := sandboxEnvMap(sandbox.EnvItems, sandbox.RuntimeEnvItems)
	if baseEnv == nil {
		baseEnv = map[string]string{}
	}
	// Merge image ENV as baseline — user/session env vars and agent-compose
	// defaults override image-defined values (matching Docker daemon behavior).
	for _, e := range imageEnv {
		if key, value, ok := parseEnvEntry(e); ok {
			if _, exists := baseEnv[key]; !exists {
				baseEnv[key] = value
			}
		}
	}
	baseEnv["GOPATH"] = "/usr/local/go"
	baseEnv["PATH"] = "/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	baseEnv["SANDBOX_ID"] = sandbox.Summary.ID
	baseEnv["WORKSPACE"] = r.config.GuestWorkspacePath
	baseEnv["STATE_ROOT"] = r.config.GuestStateRoot
	baseEnv["RUNTIME_ROOT"] = r.config.GuestRuntimeRoot
	baseEnv["JUPYTER_TOKEN"] = proxyState.Token
	if len(baseEnv) > 0 {
		keys := make([]string, 0, len(baseEnv))
		for key := range baseEnv {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			keyCString := C.CString(key)
			valueCString := C.CString(baseEnv[key])
			C.boxlite_options_add_env(options, keyCString, valueCString)
			C.free(unsafe.Pointer(keyCString))
			C.free(unsafe.Pointer(valueCString))
		}
	}

	return options, nil
}

func (r *cgoSandboxRuntime) createBox(ctx context.Context, sandbox *Sandbox, vmState VMState, proxyState ProxyState) (*cgoBoxHandle, error) {
	runtimeHandle, err := r.runtimeHandle()
	if err != nil {
		return nil, err
	}
	options, err := r.buildBoxOptions(ctx, sandbox, vmState, proxyState)
	if err != nil {
		return nil, err
	}

	awaiter := &boxliteHandleAwaiter{ch: make(chan boxliteHandleResult, 1)}
	awaiterHandle := globalBoxliteAwaiters.register(awaiter)
	defer globalBoxliteAwaiters.delete(awaiterHandle)
	var ffiErr C.CBoxliteError
	code := C.agentcompose_boxlite_create_box(runtimeHandle, options, C.uintptr_t(awaiterHandle), &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "create box"); err != nil {
		C.boxlite_options_free(options)
		return nil, err
	}
	return r.waitForHandleResult(ctx, runtimeHandle, awaiter.ch, "create box")
}

func (r *cgoSandboxRuntime) getBox(ctx context.Context, idOrName string) (*cgoBoxHandle, error) {
	runtimeHandle, err := r.runtimeHandle()
	if err != nil {
		return nil, err
	}
	lookup := C.CString(idOrName)
	defer C.free(unsafe.Pointer(lookup))
	awaiter := &boxliteHandleAwaiter{ch: make(chan boxliteHandleResult, 1)}
	awaiterHandle := globalBoxliteAwaiters.register(awaiter)
	defer globalBoxliteAwaiters.delete(awaiterHandle)
	var ffiErr C.CBoxliteError
	code := C.agentcompose_boxlite_get(runtimeHandle, lookup, C.uintptr_t(awaiterHandle), &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "attach box"); err != nil {
		return nil, err
	}
	return r.waitForHandleResult(ctx, runtimeHandle, awaiter.ch, "attach box")
}

func (r *cgoSandboxRuntime) startBox(ctx context.Context, box *cgoBoxHandle) error {
	runtimeHandle, err := r.runtimeHandle()
	if err != nil {
		return err
	}
	awaiter := &boxliteVoidAwaiter{ch: make(chan error, 1)}
	awaiterHandle := globalBoxliteAwaiters.register(awaiter)
	defer globalBoxliteAwaiters.delete(awaiterHandle)
	var ffiErr C.CBoxliteError
	code := C.agentcompose_boxlite_start_box(box.ptr, C.uintptr_t(awaiterHandle), &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "start box"); err != nil {
		return err
	}
	return r.waitForVoidResult(ctx, runtimeHandle, awaiter.ch)
}

func (r *cgoSandboxRuntime) removeBox(ctx context.Context, idOrName string, force bool) error {
	runtimeHandle, err := r.runtimeHandle()
	if err != nil {
		return err
	}
	lookup := C.CString(idOrName)
	defer C.free(unsafe.Pointer(lookup))
	awaiter := &boxliteVoidAwaiter{ch: make(chan error, 1)}
	awaiterHandle := globalBoxliteAwaiters.register(awaiter)
	defer globalBoxliteAwaiters.delete(awaiterHandle)
	var ffiErr C.CBoxliteError
	forceFlag := C.int(0)
	if force {
		forceFlag = 1
	}
	code := C.agentcompose_boxlite_remove(runtimeHandle, lookup, forceFlag, C.uintptr_t(awaiterHandle), &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "remove box"); err != nil {
		return err
	}
	return r.waitForVoidResult(ctx, runtimeHandle, awaiter.ch)
}

func (r *cgoSandboxRuntime) stopBox(ctx context.Context, box *cgoBoxHandle) error {
	runtimeHandle, err := r.runtimeHandle()
	if err != nil {
		return err
	}
	awaiter := &boxliteVoidAwaiter{ch: make(chan error, 1)}
	awaiterHandle := globalBoxliteAwaiters.register(awaiter)
	defer globalBoxliteAwaiters.delete(awaiterHandle)
	var ffiErr C.CBoxliteError
	code := C.agentcompose_boxlite_stop_box(box.ptr, C.uintptr_t(awaiterHandle), &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "stop box"); err != nil {
		return err
	}
	return r.waitForVoidResult(ctx, runtimeHandle, awaiter.ch)
}

func (r *cgoSandboxRuntime) boxID(box *cgoBoxHandle) (string, error) {
	raw := C.boxlite_box_id(box.ptr)
	if raw == nil {
		return "", fmt.Errorf("boxlite returned empty box id")
	}
	defer C.boxlite_free_string(raw)
	return C.GoString(raw), nil
}

func (r *cgoSandboxRuntime) boxInfo(box *cgoBoxHandle) (cgoBoxInfo, error) {
	var ffiErr C.CBoxliteError
	var raw *C.struct_CBoxInfo
	code := C.boxlite_box_info(box.ptr, &raw, &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "inspect box"); err != nil {
		return cgoBoxInfo{}, err
	}
	defer C.boxlite_free_box_info(raw)
	info := cgoBoxInfo{ID: cStringOrEmpty(raw.id), Name: cStringOrEmpty(raw.name)}
	info.State.Status = cStringOrEmpty(raw.status)
	info.State.Running = raw.running != 0
	return info, nil
}

func (r *cgoSandboxRuntime) executeBox(ctx context.Context, box *cgoBoxHandle, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return ExecResult{}, fmt.Errorf("command is required")
	}
	runtimeHandle, err := r.runtimeHandle()
	if err != nil {
		return ExecResult{}, err
	}
	args, argc, freeArgs := cStringArray(spec.Args)
	defer freeArgs()
	envPairs := make([]string, 0, len(spec.Env)*2)
	if len(spec.Env) > 0 {
		keys := make([]string, 0, len(spec.Env))
		for key := range spec.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			envPairs = append(envPairs, key, spec.Env[key])
		}
	}
	env, envCount, freeEnv := cStringArray(envPairs)
	defer freeEnv()
	timeoutSecs := 0.0
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline).Seconds()
		if remaining > 0 {
			timeoutSecs = remaining
		}
	}
	command := C.CString(spec.Command)
	defer C.free(unsafe.Pointer(command))

	var workdir *C.char
	if strings.TrimSpace(spec.Cwd) != "" {
		workdir = C.CString(spec.Cwd)
		defer C.free(unsafe.Pointer(workdir))
	}

	collector := &cgoExecCollector{stream: stream, filter: newExecOutputFilter()}
	awaiter := &boxliteExecAwaiter{collector: collector, waitCh: make(chan boxliteExecWaitResult, 1), exitCh: make(chan int, 1), outputCh: make(chan struct{}, 1)}
	awaiterHandle := globalBoxliteAwaiters.register(awaiter)
	defer globalBoxliteAwaiters.delete(awaiterHandle)
	cmd := C.struct_BoxliteCommand{
		command:      command,
		args:         args,
		argc:         C.int(argc),
		env_pairs:    env,
		env_count:    C.int(envCount),
		workdir:      workdir,
		user:         nil,
		timeout_secs: C.double(timeoutSecs),
		tty:          0,
	}
	var execution *C.CExecutionHandle
	var ffiErr C.CBoxliteError
	code := C.boxlite_box_exec(box.ptr, &cmd, &execution, &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "execute command"); err != nil {
		return ExecResult{}, err
	}
	defer C.boxlite_execution_free(execution)
	code = C.agentcompose_boxlite_execution_on_stdout(execution, C.uintptr_t(awaiterHandle), &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "register box stdout callback"); err != nil {
		return ExecResult{}, err
	}
	code = C.agentcompose_boxlite_execution_on_stderr(execution, C.uintptr_t(awaiterHandle), &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "register box stderr callback"); err != nil {
		return ExecResult{}, err
	}
	code = C.agentcompose_boxlite_execution_on_exit(execution, C.uintptr_t(awaiterHandle), &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "register box exit callback"); err != nil {
		return ExecResult{}, err
	}
	code = C.agentcompose_boxlite_execution_wait(execution, C.uintptr_t(awaiterHandle), &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "wait for box command"); err != nil {
		return ExecResult{}, err
	}
	exitCode, err := r.waitForExecCompletion(ctx, runtimeHandle, awaiter)
	if err != nil && ctx.Err() != nil {
		terminationErr := r.killExecution(ctx, runtimeHandle, execution)
		r.flushRuntimeCallbacks(runtimeHandle)
		collector.finish()
		return ExecResult{}, execTerminationResultError(RuntimeDriverBoxlite, fmt.Sprintf("%d", awaiterHandle), ctx.Err(), terminationErr)
	}
	r.flushRuntimeCallbacks(runtimeHandle)
	collector.finish()
	if err != nil {
		return ExecResult{}, err
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

func (r *cgoSandboxRuntime) readJupyterLog(ctx context.Context, box *cgoBoxHandle) (string, error) {
	logPath := jupyterLogPath(r.config)
	result, err := r.executeBox(ctx, box, ExecSpec{Command: "sh", Args: []string{"-lc", "cat " + shellQuote(logPath) + " 2>/dev/null || true"}, Cwd: r.config.GuestWorkspacePath}, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

func (h *cgoBoxHandle) free() {
	if h == nil || h.ptr == nil {
		return
	}
	C.boxlite_box_free(h.ptr)
	h.ptr = nil
}

func boxliteStatusError(code C.enum_BoxliteErrorCode, ffiErr *C.CBoxliteError, action string) error {
	if code == C.Ok {
		if ffiErr != nil {
			C.boxlite_error_free(ffiErr)
		}
		return nil
	}
	message := action
	if ffiErr != nil && ffiErr.message != nil {
		message = fmt.Sprintf("%s: %s", action, C.GoString(ffiErr.message))
	}
	wrapped := &boxliteCallError{code: int(code), message: message}
	if ffiErr != nil {
		C.boxlite_error_free(ffiErr)
	}
	return wrapped
}

type boxliteCallError struct {
	code    int
	message string
}

const (
	boxliteStatusNotFound = int(C.NotFound)
	boxliteStatusStopped  = int(C.Stopped)
)

func (e *boxliteCallError) Error() string {
	return e.message
}

func isBoxNotFound(err error) bool {
	var callErr *boxliteCallError
	return errors.As(err, &callErr) && callErr.code == boxliteStatusNotFound
}

func isStoppedBox(err error) bool {
	var callErr *boxliteCallError
	return errors.As(err, &callErr) && callErr.code == boxliteStatusStopped
}

func classifyBoxliteStopError(err error) (bool, error) {
	switch {
	case err == nil, isStoppedBox(err):
		return false, nil
	case isBoxNotFound(err):
		return true, nil
	default:
		return false, err
	}
}
