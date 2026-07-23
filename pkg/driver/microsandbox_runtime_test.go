//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/identity"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

func TestMicrosandboxExecCollectorMapsStdioStreams(t *testing.T) {
	var streamed []ExecChunk
	collector := &microsandboxExecCollector{stream: func(chunk ExecChunk) {
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

func TestMicrosandboxExecStreamReturnsAfterExitWithoutDone(t *testing.T) {
	events := []*microsandbox.ExecEvent{
		{Kind: microsandbox.ExecEventStarted},
		{Kind: microsandbox.ExecEventStdout, Data: []byte("finished\n")},
		{Kind: microsandbox.ExecEventExited, ExitCode: 7},
	}
	var closeCalls int
	recv := func(ctx context.Context) (*microsandbox.ExecEvent, error) {
		if len(events) > 0 {
			event := events[0]
			events = events[1:]
			return event, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	closeHandle := func() error {
		closeCalls++
		return nil
	}
	collector := &microsandboxExecCollector{filter: newExecOutputFilter()}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	grace := 25 * time.Millisecond
	startedAt := time.Now()

	result, err := consumeMicrosandboxExecStream(ctx, recv, closeHandle, nil, collector, grace, nil, 0)
	if err != nil {
		t.Fatalf("consumeMicrosandboxExecStream returned error: %v", err)
	}
	if result.ExitCode != 7 || result.Success {
		t.Fatalf("result = %#v, want exit code 7 and failure", result)
	}
	if result.Output != "finished\n" {
		t.Fatalf("output = %q, want %q", result.Output, "finished\n")
	}
	if closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", closeCalls)
	}
	if elapsed := time.Since(startedAt); elapsed < grace {
		t.Fatalf("returned before drain grace period: %s", elapsed)
	}
}

func TestMicrosandboxExecStreamCancellationWaitsForTermination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	terminationStarted := make(chan struct{})
	allowTermination := make(chan struct{})
	result := make(chan error, 1)
	recv := func(ctx context.Context) (*microsandbox.ExecEvent, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	terminate := func(cleanupCtx context.Context) error {
		if cleanupCtx.Err() != nil {
			return cleanupCtx.Err()
		}
		close(terminationStarted)
		<-allowTermination
		return nil
	}
	go func() {
		_, err := consumeMicrosandboxExecStream(
			ctx,
			recv,
			func() error { return nil },
			terminate,
			&microsandboxExecCollector{},
			time.Second,
			nil,
			0,
		)
		result <- err
	}()

	cancel()
	select {
	case <-terminationStarted:
	case <-time.After(time.Second):
		t.Fatal("termination did not start after cancellation")
	}
	select {
	case err := <-result:
		t.Fatalf("ExecStream returned before termination completed: %v", err)
	default:
	}
	close(allowTermination)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExecStream error = %v, want context canceled", err)
		}
		if errors.Is(err, ErrExecTerminationUnconfirmed) {
			t.Fatalf("successful termination was reported as unconfirmed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ExecStream did not return after termination completed")
	}
}

type recordingMicrosandboxTerminationHandle struct {
	calls   []string
	killErr error
	waitErr error
}

func (h *recordingMicrosandboxTerminationHandle) Kill(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	h.calls = append(h.calls, "kill")
	return h.killErr
}

func (h *recordingMicrosandboxTerminationHandle) Wait(ctx context.Context) (int, error) {
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	h.calls = append(h.calls, "wait")
	return -1, h.waitErr
}

func TestTerminateMicrosandboxExecKillsThenWaitsWithFreshContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handle := &recordingMicrosandboxTerminationHandle{}
	if err := terminateMicrosandboxExec(ctx, handle); err != nil {
		t.Fatalf("terminateMicrosandboxExec returned error: %v", err)
	}
	if got := strings.Join(handle.calls, ","); got != "kill,wait" {
		t.Fatalf("termination calls = %q, want kill,wait", got)
	}
}

func TestMicrosandboxExecStreamCancellationReportsUnconfirmedTermination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wantErr := errors.New("kill failed")
	_, err := consumeMicrosandboxExecStream(
		ctx,
		func(ctx context.Context) (*microsandbox.ExecEvent, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		func() error { return nil },
		func(context.Context) error { return wantErr },
		&microsandboxExecCollector{},
		time.Second,
		nil,
		0,
	)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, wantErr) || !errors.Is(err, ErrExecTerminationUnconfirmed) {
		t.Fatalf("ExecStream error = %v, want cancellation and unconfirmed termination", err)
	}
}

// A command that leaves a background process holding the output pipes never
// produces an exit event. Waiting for one is a wait that never ends, so the
// silence is used to ask the guest whether the process is even still there.
func TestMicrosandboxExecStreamStopsWhenProcessIsGoneWithoutExit(t *testing.T) {
	events := []*microsandbox.ExecEvent{
		{Kind: microsandbox.ExecEventStarted, PID: 4242},
		{Kind: microsandbox.ExecEventStdout, Data: []byte("done\n")},
	}
	recv := func(ctx context.Context) (*microsandbox.ExecEvent, error) {
		if len(events) > 0 {
			event := events[0]
			events = events[1:]
			return event, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var closeCalls int
	closeHandle := func() error {
		closeCalls++
		return nil
	}
	var probedPID uint32
	probe := func(context.Context, uint32) (bool, error) {
		return false, nil
	}
	probeRecorder := func(ctx context.Context, pid uint32) (bool, error) {
		probedPID = pid
		return probe(ctx, pid)
	}
	collector := &microsandboxExecCollector{filter: newExecOutputFilter()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := consumeMicrosandboxExecStream(ctx, recv, closeHandle, nil, collector, 25*time.Millisecond, probeRecorder, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "without reporting its status") {
		t.Fatalf("error = %v, want a lost-exit failure", err)
	}
	if probedPID != 4242 {
		t.Fatalf("probed pid = %d, want the pid from the started event", probedPID)
	}
	if closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", closeCalls)
	}
	// The output collected before the stream went quiet is still reported, so
	// callers can see how far the command got.
	if result.Output != "done\n" || result.Success || result.ExitCode != -1 {
		t.Fatalf("result = %#v", result)
	}
}

// scriptedExecStream hands out queued events once each and then blocks, which
// is how a real stream behaves. A receiver that keeps re-delivering the same
// event instead would hold the drain window open forever.
type scriptedExecStream struct {
	mu       sync.Mutex
	pending  []*microsandbox.ExecEvent
	ready    chan struct{}
	active   int
	max      int
	canceled int
}

func (s *scriptedExecStream) push(event *microsandbox.ExecEvent) {
	s.mu.Lock()
	s.pending = append(s.pending, event)
	if s.ready == nil {
		s.ready = make(chan struct{}, 1)
	}
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

func (s *scriptedExecStream) recv(ctx context.Context) (*microsandbox.ExecEvent, error) {
	s.mu.Lock()
	if s.ready == nil {
		s.ready = make(chan struct{}, 1)
	}
	s.active++
	if s.active > s.max {
		s.max = s.active
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()

	for {
		s.mu.Lock()
		if len(s.pending) > 0 {
			event := s.pending[0]
			s.pending = s.pending[1:]
			s.mu.Unlock()
			return event, nil
		}
		ready := s.ready
		s.mu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			s.mu.Lock()
			s.canceled++
			s.mu.Unlock()
			return nil, ctx.Err()
		}
	}
}

func (s *scriptedExecStream) maxConcurrentReceivers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.max
}

func (s *scriptedExecStream) canceledReceivers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.canceled
}

// Silence on its own means nothing: a command can be quiet for a long time.
func TestMicrosandboxExecStreamKeepsWaitingWhileProcessLives(t *testing.T) {
	stream := &scriptedExecStream{pending: []*microsandbox.ExecEvent{
		// The pid has to be known before silence can be interpreted at all.
		{Kind: microsandbox.ExecEventStarted, PID: 11},
	}}
	var probeCalls int
	probe := func(context.Context, uint32) (bool, error) {
		probeCalls++
		if probeCalls == 3 {
			stream.push(&microsandbox.ExecEvent{Kind: microsandbox.ExecEventExited, ExitCode: 0})
		}
		return true, nil
	}
	collector := &microsandboxExecCollector{filter: newExecOutputFilter()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := consumeMicrosandboxExecStream(ctx, stream.recv, func() error { return nil }, nil, collector, 10*time.Millisecond, probe, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("consumeMicrosandboxExecStream returned error: %v", err)
	}
	if probeCalls < 3 {
		t.Fatalf("probe calls = %d, want the stream to keep waiting while the process lives", probeCalls)
	}
	if !result.Success || result.ExitCode != 0 {
		t.Fatalf("result = %#v, want the reported exit to win", result)
	}
	if got := stream.maxConcurrentReceivers(); got != 1 {
		t.Fatalf("concurrent receivers = %d, want exactly one", got)
	}
	if got := stream.canceledReceivers(); got != 1 {
		t.Fatalf("canceled receivers = %d, want only the terminal drain cancellation", got)
	}
}

// A probe that cannot answer must not be read as "the process is gone".
func TestMicrosandboxExecStreamKeepsWaitingWhenProbeFails(t *testing.T) {
	stream := &scriptedExecStream{pending: []*microsandbox.ExecEvent{
		{Kind: microsandbox.ExecEventStarted, PID: 9},
	}}
	var probeCalls int
	probe := func(context.Context, uint32) (bool, error) {
		probeCalls++
		if probeCalls == 2 {
			stream.push(&microsandbox.ExecEvent{Kind: microsandbox.ExecEventExited, ExitCode: 3})
		}
		return false, errors.New("guest agent did not answer")
	}
	collector := &microsandboxExecCollector{filter: newExecOutputFilter()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := consumeMicrosandboxExecStream(ctx, stream.recv, func() error { return nil }, nil, collector, 10*time.Millisecond, probe, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("consumeMicrosandboxExecStream returned error: %v", err)
	}
	if probeCalls < 2 {
		t.Fatalf("probe calls = %d, want the stream to keep waiting after a failed probe", probeCalls)
	}
	if result.ExitCode != 3 || result.Success {
		t.Fatalf("result = %#v, want the eventual exit status", result)
	}
}

// A stream that ends without an exit status used to be reported as exit code 0,
// which turns a lost status into a success whose output is quietly short.
func TestMicrosandboxExecStreamRejectsDoneWithoutExit(t *testing.T) {
	events := []*microsandbox.ExecEvent{
		{Kind: microsandbox.ExecEventStarted, PID: 5},
		{Kind: microsandbox.ExecEventStdout, Data: []byte("partial")},
		{Kind: microsandbox.ExecEventDone},
	}
	recv := func(context.Context) (*microsandbox.ExecEvent, error) {
		event := events[0]
		events = events[1:]
		return event, nil
	}
	collector := &microsandboxExecCollector{filter: newExecOutputFilter()}

	result, err := consumeMicrosandboxExecStream(context.Background(), recv, func() error { return nil }, nil, collector, 25*time.Millisecond, nil, 0)
	if err == nil || !strings.Contains(err.Error(), "without reporting a process exit status") {
		t.Fatalf("error = %v, want a missing-exit failure", err)
	}
	if result.Success || result.ExitCode != -1 {
		t.Fatalf("result = %#v, want a non-success result", result)
	}
	if result.Output != "partial" {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestMicrosandboxExecLivenessProbeTreatsZombieAsGone(t *testing.T) {
	if err := exec.Command("sh", "-c", microsandboxExecLivenessProbeCommand(uint32(os.Getpid()))).Run(); err != nil {
		t.Fatalf("probe reported current process gone: %v", err)
	}
	if err := exec.Command("sh", "-c", microsandboxExecLivenessProbeCommand(^uint32(0))).Run(); err == nil {
		t.Fatal("probe reported nonexistent process alive")
	}

	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() { _ = child.Wait() })
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := exec.Command("sh", "-c", microsandboxExecLivenessProbeCommand(uint32(child.Process.Pid))).Run()
		if err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe still reported exited child %d alive", child.Process.Pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMicrosandboxResolveLibkrunfwPrefersVersionedRealFile(t *testing.T) {
	libDir := t.TempDir()
	versioned := filepath.Join(libDir, "libkrunfw.so.5.5.0")
	if err := os.WriteFile(versioned, []byte("krun"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("libkrunfw.so.5.5.0", filepath.Join(libDir, "libkrunfw.so.5")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("libkrunfw.so.5", filepath.Join(libDir, "libkrunfw.so")); err != nil {
		t.Fatal(err)
	}

	runtime := &microsandboxRuntime{config: &appconfig.Config{
		MicrosandboxLibPath: filepath.Join(libDir, "libmicrosandbox_go_ffi.so"),
	}}
	got, err := runtime.resolveLibkrunfwPath()
	if err != nil {
		t.Fatalf("resolveLibkrunfwPath: %v", err)
	}
	if got != versioned {
		t.Fatalf("resolveLibkrunfwPath() = %q, want %q", got, versioned)
	}
}

func TestMicrosandboxResolveLibkrunfwUsesNumericVersionOrder(t *testing.T) {
	libDir := t.TempDir()
	for _, name := range []string{
		"libkrunfw.so.5.2.1",
		"libkrunfw.so.5.10.0",
		"libkrunfw.so.5.99.0.bak",
	} {
		if err := os.WriteFile(filepath.Join(libDir, name), []byte("krun"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runtime := &microsandboxRuntime{config: &appconfig.Config{
		MicrosandboxLibPath: filepath.Join(libDir, "libmicrosandbox_go_ffi.so"),
	}}
	want := filepath.Join(libDir, "libkrunfw.so.5.10.0")
	got, err := runtime.resolveLibkrunfwPath()
	if err != nil {
		t.Fatalf("resolveLibkrunfwPath: %v", err)
	}
	if got != want {
		t.Fatalf("resolveLibkrunfwPath() = %q, want %q", got, want)
	}
}

func TestMicrosandboxResolveLibkrunfwHandlesGlobMetaInDirectory(t *testing.T) {
	root := t.TempDir()
	libDir := filepath.Join(root, "lib[meta]")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	versioned := filepath.Join(libDir, "libkrunfw.so.5.6.0")
	if err := os.WriteFile(versioned, []byte("krun"), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime := &microsandboxRuntime{config: &appconfig.Config{
		MicrosandboxLibPath: filepath.Join(libDir, "libmicrosandbox_go_ffi.so"),
	}}
	got, err := runtime.resolveLibkrunfwPath()
	if err != nil {
		t.Fatalf("resolveLibkrunfwPath: %v", err)
	}
	if got != versioned {
		t.Fatalf("resolveLibkrunfwPath() = %q, want %q", got, versioned)
	}
}

func TestMicrosandboxPrepareEnvironmentPreservesDockerDisks(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtime := &microsandboxRuntime{config: config}
	disk := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", "old-session.raw")
	ignored := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", "note.txt")
	subdir := filepath.Join(config.MicrosandboxHome, "docker-disks", "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir docker-disks subdir: %v", err)
	}

	if err := runtime.prepareEnvironment(); err != nil {
		t.Fatalf("prepareEnvironment: %v", err)
	}
	for _, path := range []string{disk, ignored, subdir} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("path %s missing after prepareEnvironment: %v", path, err)
		}
	}
}

func TestMicrosandboxRemoveDockerDiskOnlyCurrentSession(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtime := &microsandboxRuntime{config: config}
	currentID := identity.Prefix + identity.NewRandomID(identity.ResourceSandbox)
	otherID := identity.NewRandomID(identity.ResourceSandbox)
	current := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", microsandboxDockerDiskName(currentID)+".raw")
	legacyCurrent := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", currentID+".raw")
	other := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", microsandboxDockerDiskName(otherID)+".raw")

	runtime.removeDockerDisk(currentID)

	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatalf("current disk exists after removeDockerDisk, err=%v", err)
	}
	if _, err := os.Stat(legacyCurrent); !os.IsNotExist(err) {
		t.Fatalf("legacy current disk exists after removeDockerDisk, err=%v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other disk missing after removeDockerDisk: %v", err)
	}
}

func TestMicrosandboxEnsureDockerDiskMigratesLegacyPath(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtime := &microsandboxRuntime{config: config}
	sandboxID := identity.Prefix + identity.NewRandomID(identity.ResourceSandbox)
	legacy := writeMicrosandboxFile(t, config.MicrosandboxHome, "docker-disks", sandboxID+".raw")

	got, err := runtime.ensureDockerDisk(sandboxID)
	if err != nil {
		t.Fatalf("ensureDockerDisk: %v", err)
	}
	want := runtime.dockerDiskPath(sandboxID)
	if got != want {
		t.Fatalf("ensureDockerDisk path = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("migrated docker disk missing: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy docker disk still exists after migration, err=%v", err)
	}
}

func TestMicrosandboxDockerDiskPathUsesIdentityHash(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtime := &microsandboxRuntime{config: config}
	sandboxID := identity.NewRandomID(identity.ResourceSandbox)
	hash, err := identity.Hash(sandboxID)
	if err != nil {
		t.Fatalf("hash sandbox id: %v", err)
	}

	got := runtime.dockerDiskPath(sandboxID)
	want := filepath.Join(config.MicrosandboxHome, "docker-disks", hash+".raw")
	if got != want {
		t.Fatalf("dockerDiskPath = %q, want %q", got, want)
	}
	if strings.ContainsAny(filepath.Base(got), ",:;") {
		t.Fatalf("docker disk basename = %q, want no runtime-forbidden characters", filepath.Base(got))
	}
}

func TestMicrosandboxBindMountSetsConfiguredQuota(t *testing.T) {
	runtime := &microsandboxRuntime{config: &appconfig.Config{SandboxDiskSizeGB: 60}}

	mount := runtime.microsandboxBindMount("/host/session", false)

	if mount.QuotaMiB != 60*1024 {
		t.Fatalf("QuotaMiB = %d, want %d", mount.QuotaMiB, 60*1024)
	}
	if mount.Readonly {
		t.Fatalf("Readonly = true, want false")
	}
}

func TestMicrosandboxBindMountPreservesReadonly(t *testing.T) {
	runtime := &microsandboxRuntime{config: &appconfig.Config{SandboxDiskSizeGB: 11}}

	mount := runtime.microsandboxBindMount("/host/session", true)

	if mount.QuotaMiB != 11*1024 {
		t.Fatalf("QuotaMiB = %d, want %d", mount.QuotaMiB, 11*1024)
	}
	if !mount.Readonly {
		t.Fatalf("Readonly = false, want true")
	}
}

func TestMicrosandboxBindMountFallsBackToBoxDiskSize(t *testing.T) {
	runtime := &microsandboxRuntime{config: &appconfig.Config{SandboxDiskSizeGB: 42}}

	mount := runtime.microsandboxBindMount("/host/session", false)

	if mount.QuotaMiB != 42*1024 {
		t.Fatalf("QuotaMiB = %d, want %d", mount.QuotaMiB, 42*1024)
	}
}

func testMicrosandboxConfig(t *testing.T) *appconfig.Config {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", filepath.Join(root, "user-home"))
	bin := writeMicrosandboxFile(t, root, "bin", "msb")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatalf("chmod msb: %v", err)
	}
	lib := writeMicrosandboxFile(t, root, "lib", "libmicrosandbox_go_ffi.so")
	_ = writeMicrosandboxFile(t, root, "lib", "libkrunfw.so.5.2.1")
	return &appconfig.Config{
		MicrosandboxHome:    home,
		MicrosandboxMSBPath: bin,
		MicrosandboxLibPath: lib,
		SandboxDiskSizeGB:   1,
	}
}

func writeMicrosandboxFile(t *testing.T, root string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
