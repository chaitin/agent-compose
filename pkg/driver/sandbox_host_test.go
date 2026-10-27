package driver

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/driver/sandboxhost"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeSandboxHostRuntime implements the full SandboxRuntime surface plus the
// optional interfaces the sandbox-host dispatches to, recording calls so tests
// can assert the wire round-trip reached the right method with the right data.
type fakeSandboxHostRuntime struct {
	mu sync.Mutex

	needsCreate bool
	needsErr    error

	ensureCalls  int
	lastSandbox  *Sandbox
	lastVMState  VMState
	lastProxy    ProxyState
	ensureResult SandboxVMInfo
	ensureErr    error

	execResult ExecResult
	execErr    error

	stopMissing bool
	stopErr     error
	removeErr   error

	alive    bool
	aliveErr error

	stats    SandboxStats
	statsErr error

	signalErr    error
	terminateErr error

	caps        RuntimeInteractionCapabilities
	interaction RuntimeInteraction
	openErr     error
}

func (f *fakeSandboxHostRuntime) EnsureSandbox(_ context.Context, sandbox *Sandbox, vmState VMState, proxyState ProxyState) (SandboxVMInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls++
	f.lastSandbox = sandbox
	f.lastVMState = vmState
	f.lastProxy = proxyState
	return f.ensureResult, f.ensureErr
}

func (f *fakeSandboxHostRuntime) NeedsCreate(context.Context, *Sandbox, VMState) (bool, error) {
	return f.needsCreate, f.needsErr
}

func (f *fakeSandboxHostRuntime) StopSandbox(context.Context, *Sandbox, VMState) (bool, error) {
	return f.stopMissing, f.stopErr
}

func (f *fakeSandboxHostRuntime) RemoveSandbox(context.Context, *Sandbox, VMState) error {
	return f.removeErr
}

func (f *fakeSandboxHostRuntime) IsSandboxAlive(context.Context, *Sandbox, VMState) (bool, error) {
	return f.alive, f.aliveErr
}

func (f *fakeSandboxHostRuntime) Exec(context.Context, *Sandbox, VMState, ExecSpec) (ExecResult, error) {
	return f.execResult, f.execErr
}

func (f *fakeSandboxHostRuntime) ExecStream(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	if stream != nil {
		stream(ExecChunk{Text: "out\n", Stream: StdioStdout})
		stream(ExecChunk{Text: "err\n", Stream: StdioStderr})
	}
	return f.execResult, f.execErr
}

func (f *fakeSandboxHostRuntime) Stats(context.Context, *Sandbox, VMState) (SandboxStats, error) {
	return f.stats, f.statsErr
}

func (f *fakeSandboxHostRuntime) SignalGuestRuntime(context.Context, *Sandbox, VMState, string, RuntimeSignal) error {
	return f.signalErr
}

func (f *fakeSandboxHostRuntime) TerminateProcessTree(context.Context, *Sandbox, VMState, string) error {
	return f.terminateErr
}

func (f *fakeSandboxHostRuntime) InteractionCapabilities() RuntimeInteractionCapabilities {
	return f.caps
}

func (f *fakeSandboxHostRuntime) OpenInteraction(context.Context, *Sandbox, VMState, RuntimeStartSpec) (RuntimeInteraction, error) {
	return f.interaction, f.openErr
}

// fakeRuntimeInteraction is a deterministic RuntimeInteraction whose output is a
// fixed sequence followed by a terminal result.
type fakeRuntimeInteraction struct {
	mu       sync.Mutex
	outputs  []RuntimeOutputFrame
	result   RuntimeResult
	idx      int
	sent     []RuntimeInputFrame
	closed   bool
	closedCh chan struct{}
	sendErr  error
	closeErr error
}

func (f *fakeRuntimeInteraction) Send(frame RuntimeInputFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, frame)
	return f.sendErr
}

func (f *fakeRuntimeInteraction) CloseSend() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.closedCh)
	}
	return f.closeErr
}

func (f *fakeRuntimeInteraction) Recv() (RuntimeOutputFrame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx < len(f.outputs) {
		frame := f.outputs[f.idx]
		f.idx++
		return frame, nil
	}
	return RuntimeOutputFrame{}, io.EOF
}

// Wait blocks until the input side is closed, mirroring a real runtime that
// reports its terminal result only after stdin reaches EOF. This keeps the
// server's result message from racing ahead of forwarded input frames.
func (f *fakeRuntimeInteraction) Wait() (RuntimeResult, error) {
	<-f.closedCh
	return f.result, nil
}

func (f *fakeRuntimeInteraction) sentFrames() []RuntimeInputFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RuntimeInputFrame(nil), f.sent...)
}

func (f *fakeRuntimeInteraction) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// startSandboxHostTestServer starts a sandbox-host gRPC server on a temp unix
// socket backed by the given fake runtimes, returning a connector that dials it.
func startSandboxHostTestServer(t *testing.T, runtimes map[string]SandboxRuntime) *SandboxHostConnector {
	t.Helper()
	// Use a short path under /tmp: the unix socket address is bounded by the
	// 104-byte sockaddr_un limit, and the macOS per-test temp dir is too deep.
	dir, err := os.MkdirTemp("/tmp", "sandbox-host-test-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(sandboxHostMaxMsgSize),
		grpc.MaxSendMsgSize(sandboxHostMaxMsgSize),
	)
	sandboxhost.RegisterSandboxHostServer(grpcServer, &sandboxHostServer{runtimes: runtimes})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	return NewSandboxHostConnector(socketPath, dir, SandboxHostModeExternal)
}

func TestSandboxHostConnectorExternalModeRefusesToSpawn(t *testing.T) {
	// External mode dials a host that is already listening and never reaches the
	// spawn path; this is exercised by every startSandboxHostTestServer test.
	// Here we assert the opposite: with no listener, external mode must fail
	// with a clear error instead of spawning a host inside the caller's domain.
	dir, err := os.MkdirTemp("/tmp", "sandbox-host-external-test-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "missing.sock")

	connector := NewSandboxHostConnector(socketPath, dir, SandboxHostModeExternal)
	if _, err := connector.Client(context.Background()); err == nil {
		t.Fatalf("external mode Client() = nil error, want refuse-to-spawn error")
	}
}

func TestSandboxHostConnectorDefaultModeIsExternal(t *testing.T) {
	// An empty mode must resolve to external: spawning is opt-in for local
	// development, never the silent default for a deployment.
	if got := NewSandboxHostConnector("/tmp/never.sock", "/tmp", "").mode; got != SandboxHostModeExternal {
		t.Fatalf("default mode = %q, want %q", got, SandboxHostModeExternal)
	}
}

func newFakeSandboxHostRuntime() *fakeSandboxHostRuntime {
	return &fakeSandboxHostRuntime{
		needsCreate:  true,
		ensureResult: SandboxVMInfo{BoxID: "box-1", RuntimeCreated: true},
		execResult:   ExecResult{ExitCode: 0, Success: true, Output: "done"},
		alive:        true,
		stats:        SandboxStats{SandboxID: "sandbox-1", Driver: RuntimeDriverBoxlite},
		caps:         RuntimeInteractionCapabilities{NativeExec: true, Stdin: true, StdinEOF: true, TTY: true, AgentTurns: true},
	}
}

func TestRemoteSandboxRuntimeEnsureTwoPhase(t *testing.T) {
	fake := newFakeSandboxHostRuntime()
	fake.needsCreate = true
	connector := startSandboxHostTestServer(t, map[string]SandboxRuntime{RuntimeDriverBoxlite: fake})
	remote := NewRemoteSandboxRuntime(RuntimeDriverBoxlite, connector).(*remoteSandboxRuntime)

	sandbox := &Sandbox{
		Summary:         SandboxSummary{Driver: RuntimeDriverBoxlite, ID: "sandbox-1"},
		RuntimeEnvItems: []SandboxEnvVar{{Name: "FACADE", Value: "tok"}},
		ProxyEnv: func(context.Context) ([]SandboxEnvVar, error) {
			return []SandboxEnvVar{{Name: "PROXY", Value: "cred", Secret: true}}, nil
		},
	}
	info, err := remote.EnsureSandbox(context.Background(), sandbox, VMState{BoxName: "b"}, ProxyState{ProxyPath: "/p"})
	if err != nil {
		t.Fatalf("EnsureSandbox() error = %v", err)
	}
	if info.BoxID != "box-1" || !info.RuntimeCreated {
		t.Fatalf("EnsureSandbox() info = %#v", info)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.ensureCalls != 1 {
		t.Fatalf("EnsureSandbox calls = %d, want 1", fake.ensureCalls)
	}
	if fake.lastSandbox == nil || fake.lastSandbox.ProxyEnv != nil {
		t.Fatalf("host received ProxyEnv closure; must be resolved daemon-side")
	}
	wantEnv := []SandboxEnvVar{{Name: "FACADE", Value: "tok"}, {Name: "PROXY", Value: "cred", Secret: true}}
	if !envVarsEqual(fake.lastSandbox.RuntimeEnvItems, wantEnv) {
		t.Fatalf("host RuntimeEnvItems = %#v, want %#v", fake.lastSandbox.RuntimeEnvItems, wantEnv)
	}
	if fake.lastProxy.ProxyPath != "/p" {
		t.Fatalf("host proxyState = %#v", fake.lastProxy)
	}
}

func TestRemoteSandboxRuntimeEnsureReuseDoesNotMintProxy(t *testing.T) {
	fake := newFakeSandboxHostRuntime()
	fake.needsCreate = false
	connector := startSandboxHostTestServer(t, map[string]SandboxRuntime{RuntimeDriverBoxlite: fake})
	remote := NewRemoteSandboxRuntime(RuntimeDriverBoxlite, connector).(*remoteSandboxRuntime)

	sandbox := &Sandbox{
		Summary:         SandboxSummary{Driver: RuntimeDriverBoxlite, ID: "sandbox-1"},
		RuntimeEnvItems: []SandboxEnvVar{{Name: "FACADE", Value: "tok"}},
		ProxyEnv: func(context.Context) ([]SandboxEnvVar, error) {
			t.Fatalf("ProxyEnv must not be resolved on reuse")
			return nil, nil
		},
	}
	if _, err := remote.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{}); err != nil {
		t.Fatalf("EnsureSandbox() error = %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.ensureCalls != 1 {
		t.Fatalf("EnsureSandbox calls = %d, want 1", fake.ensureCalls)
	}
	if len(fake.lastSandbox.RuntimeEnvItems) != 1 {
		t.Fatalf("reuse path must not mint proxy items, got %#v", fake.lastSandbox.RuntimeEnvItems)
	}
}

func TestRemoteSandboxRuntimeErrorMapping(t *testing.T) {
	tests := []struct {
		name         string
		ensureErr    error
		wantSentinel error
	}{
		// A non-interaction RPC that yields Unimplemented must come back as the
		// generic capability sentinel, not the interaction one (the reviewer's
		// misleading-mapping concern). ErrRuntimeInteractionUnsupported is the
		// server-side source of Unimplemented here.
		{name: "unimplemented maps to capability unsupported", ensureErr: ErrRuntimeInteractionUnsupported, wantSentinel: ErrRuntimeCapabilityUnsupported},
		{name: "exec termination unconfirmed", ensureErr: ErrExecTerminationUnconfirmed, wantSentinel: ErrExecTerminationUnconfirmed},
		{name: "guest runtime gone", ensureErr: ErrGuestRuntimeGone, wantSentinel: ErrGuestRuntimeGone},
		{name: "guest runtime not ready", ensureErr: ErrGuestRuntimeNotReady, wantSentinel: ErrGuestRuntimeNotReady},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeSandboxHostRuntime()
			fake.needsCreate = true
			fake.ensureErr = tc.ensureErr
			connector := startSandboxHostTestServer(t, map[string]SandboxRuntime{RuntimeDriverBoxlite: fake})
			remote := NewRemoteSandboxRuntime(RuntimeDriverBoxlite, connector).(*remoteSandboxRuntime)

			_, err := remote.EnsureSandbox(context.Background(), &Sandbox{Summary: SandboxSummary{Driver: RuntimeDriverBoxlite}}, VMState{}, ProxyState{})
			if !errors.Is(err, tc.wantSentinel) {
				t.Fatalf("EnsureSandbox() error = %v, want sentinel %v", err, tc.wantSentinel)
			}
		})
	}
}

func TestRemoteSandboxRuntimePassthrough(t *testing.T) {
	fake := newFakeSandboxHostRuntime()
	connector := startSandboxHostTestServer(t, map[string]SandboxRuntime{RuntimeDriverBoxlite: fake})
	remote := NewRemoteSandboxRuntime(RuntimeDriverBoxlite, connector).(*remoteSandboxRuntime)
	sandbox := &Sandbox{Summary: SandboxSummary{Driver: RuntimeDriverBoxlite, ID: "sandbox-1"}}
	vmState := VMState{BoxName: "b"}
	ctx := context.Background()

	t.Run("exec", func(t *testing.T) {
		result, err := remote.Exec(ctx, sandbox, vmState, ExecSpec{Command: "sh"})
		if err != nil || result.ExitCode != 0 || result.Output != "done" {
			t.Fatalf("Exec() = %#v, %v", result, err)
		}
	})

	t.Run("exec stream", func(t *testing.T) {
		var chunks []ExecChunk
		result, err := remote.ExecStream(ctx, sandbox, vmState, ExecSpec{Command: "sh"}, func(c ExecChunk) {
			chunks = append(chunks, c)
		})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("ExecStream() = %#v, %v", result, err)
		}
		if len(chunks) != 2 || chunks[0].Text != "out\n" || chunks[1].Text != "err\n" {
			t.Fatalf("ExecStream() chunks = %#v", chunks)
		}
	})

	t.Run("stats", func(t *testing.T) {
		stats, err := remote.Stats(ctx, sandbox, vmState)
		if err != nil || stats.SandboxID != "sandbox-1" || stats.Driver != RuntimeDriverBoxlite {
			t.Fatalf("Stats() = %#v, %v", stats, err)
		}
	})

	t.Run("is alive", func(t *testing.T) {
		alive, err := remote.IsSandboxAlive(ctx, sandbox, vmState)
		if err != nil || !alive {
			t.Fatalf("IsSandboxAlive() = %v, %v", alive, err)
		}
	})

	t.Run("stop", func(t *testing.T) {
		missing, err := remote.StopSandbox(ctx, sandbox, vmState)
		if err != nil || missing {
			t.Fatalf("StopSandbox() = %v, %v", missing, err)
		}
	})

	t.Run("remove", func(t *testing.T) {
		if err := remote.RemoveSandbox(ctx, sandbox, vmState); err != nil {
			t.Fatalf("RemoveSandbox() error = %v", err)
		}
	})

	t.Run("signal guest runtime", func(t *testing.T) {
		if err := remote.SignalGuestRuntime(ctx, sandbox, vmState, "op-1", RuntimeSignalInterrupt); err != nil {
			t.Fatalf("SignalGuestRuntime() error = %v", err)
		}
	})

	t.Run("terminate process tree", func(t *testing.T) {
		if err := remote.TerminateProcessTree(ctx, sandbox, vmState, "marker-1"); err != nil {
			t.Fatalf("TerminateProcessTree() error = %v", err)
		}
	})

	t.Run("interaction capabilities", func(t *testing.T) {
		caps := remote.InteractionCapabilities()
		if !caps.NativeExec || !caps.Stdin || !caps.TTY {
			t.Fatalf("InteractionCapabilities() = %#v", caps)
		}
	})
}

func TestRemoteSandboxRuntimeLargeExecResult(t *testing.T) {
	// gRPC defaults to a 4 MiB message ceiling. A long run whose accumulated
	// output exceeds that must round-trip without ResourceExhausted; otherwise
	// the failure surfaces only after the chunks have already streamed.
	const size = 5 << 20 // 5 MiB
	big := strings.Repeat("x", size)
	fake := newFakeSandboxHostRuntime()
	fake.execResult = ExecResult{ExitCode: 0, Success: true, Output: big}
	connector := startSandboxHostTestServer(t, map[string]SandboxRuntime{RuntimeDriverBoxlite: fake})
	remote := NewRemoteSandboxRuntime(RuntimeDriverBoxlite, connector).(*remoteSandboxRuntime)
	sandbox := &Sandbox{Summary: SandboxSummary{Driver: RuntimeDriverBoxlite, ID: "sandbox-1"}}

	t.Run("exec", func(t *testing.T) {
		result, err := remote.Exec(context.Background(), sandbox, VMState{}, ExecSpec{Command: "sh"})
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		if len(result.Output) != size {
			t.Fatalf("Exec() output length = %d, want %d", len(result.Output), size)
		}
	})

	t.Run("exec stream", func(t *testing.T) {
		result, err := remote.ExecStream(context.Background(), sandbox, VMState{}, ExecSpec{Command: "sh"}, func(ExecChunk) {})
		if err != nil {
			t.Fatalf("ExecStream() error = %v", err)
		}
		if len(result.Output) != size {
			t.Fatalf("ExecStream() output length = %d, want %d", len(result.Output), size)
		}
	})
}

func TestRemoteSandboxRuntimeOpenInteraction(t *testing.T) {
	fake := newFakeSandboxHostRuntime()
	fake.interaction = &fakeRuntimeInteraction{
		outputs: []RuntimeOutputFrame{
			{Type: RuntimeOutputStdout, Data: []byte("hello\n")},
		},
		result:   RuntimeResult{OperationID: "op-1", ExitCode: 0, Success: true},
		closedCh: make(chan struct{}),
	}
	connector := startSandboxHostTestServer(t, map[string]SandboxRuntime{RuntimeDriverBoxlite: fake})
	remote := NewRemoteSandboxRuntime(RuntimeDriverBoxlite, connector).(*remoteSandboxRuntime)
	sandbox := &Sandbox{Summary: SandboxSummary{Driver: RuntimeDriverBoxlite, ID: "sandbox-1"}}

	interaction, err := remote.OpenInteraction(context.Background(), sandbox, VMState{}, RuntimeStartSpec{
		OperationID: "op-1", Kind: RuntimeOperationAgent, AttachStdin: true,
	})
	if err != nil {
		t.Fatalf("OpenInteraction() error = %v", err)
	}

	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputStdin, Data: []byte("x")}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if err := interaction.CloseSend(); err != nil {
		t.Fatalf("CloseSend() error = %v", err)
	}

	frame, err := interaction.Recv()
	if err != nil || frame.Type != RuntimeOutputStdout || string(frame.Data) != "hello\n" {
		t.Fatalf("Recv() = %#v, %v", frame, err)
	}
	if _, err := interaction.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() after close error = %v, want EOF", err)
	}

	result, err := interaction.Wait()
	if err != nil || result.OperationID != "op-1" || !result.Success {
		t.Fatalf("Wait() = %#v, %v", result, err)
	}

	fakeInteraction := fake.interaction.(*fakeRuntimeInteraction)
	if got := fakeInteraction.sentFrames(); len(got) != 1 || string(got[0].Data) != "x" {
		t.Fatalf("host interaction sent frames = %#v", got)
	}
	if !fakeInteraction.isClosed() {
		t.Fatalf("host interaction CloseSend was not forwarded")
	}
}

func TestSandboxHostStatusAndFromGRPCError(t *testing.T) {
	if sandboxHostStatus(nil) != nil {
		t.Fatalf("sandboxHostStatus(nil) must be nil")
	}
	serverCases := []struct {
		err  error
		code codes.Code
	}{
		{ErrRuntimeInteractionUnsupported, codes.Unimplemented},
		{ErrGuestRuntimeGone, codes.NotFound},
		{ErrGuestRuntimeNotReady, codes.FailedPrecondition},
		{ErrExecTerminationUnconfirmed, codes.Aborted},
		{errors.New("boom"), codes.Internal},
		{context.Canceled, codes.Canceled},
		{context.DeadlineExceeded, codes.DeadlineExceeded},
	}
	for _, tc := range serverCases {
		if code := status.Code(sandboxHostStatus(tc.err)); code != tc.code {
			t.Fatalf("sandboxHostStatus(%v) code = %v, want %v", tc.err, code, tc.code)
		}
	}

	clientCases := []struct {
		err         error
		want        error
		interaction bool
	}{
		{status.Error(codes.Unimplemented, "no"), ErrRuntimeCapabilityUnsupported, false},
		{status.Error(codes.Unimplemented, "no"), ErrRuntimeInteractionUnsupported, true},
		{status.Error(codes.NotFound, "gone"), ErrGuestRuntimeGone, false},
		{status.Error(codes.FailedPrecondition, "not ready"), ErrGuestRuntimeNotReady, false},
		{status.Error(codes.Aborted, "no"), ErrExecTerminationUnconfirmed, false},
		{status.Error(codes.Canceled, "canceled"), context.Canceled, false},
		{status.Error(codes.DeadlineExceeded, "deadline"), context.DeadlineExceeded, false},
	}
	for _, tc := range clientCases {
		var got error
		if tc.interaction {
			got = fromInteractionGRPCError(tc.err)
		} else {
			got = fromGRPCError(tc.err)
		}
		if !errors.Is(got, tc.want) {
			t.Fatalf("map(%v) = %v, want errors.Is %v", tc.err, got, tc.want)
		}
	}

	if err := fromGRPCError(status.Error(codes.Internal, "boom")); status.Code(err) != codes.Internal {
		t.Fatalf("fromGRPCError(Internal) must pass through, got %v", err)
	}
}

func TestSandboxHostHandshake(t *testing.T) {
	fake := newFakeSandboxHostRuntime()
	connector := startSandboxHostTestServer(t, map[string]SandboxRuntime{RuntimeDriverBoxlite: fake})
	client, err := connector.Client(context.Background())
	if err != nil {
		t.Fatalf("Client() error = %v", err)
	}
	resp, err := client.Handshake(context.Background(), &sandboxhost.HandshakeRequest{})
	if err != nil {
		t.Fatalf("Handshake() error = %v", err)
	}
	if resp.GetProtocolVersion() != sandboxHostProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", resp.GetProtocolVersion(), sandboxHostProtocolVersion)
	}
}

func envVarsEqual(a, b []SandboxEnvVar) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeInteractionStream is a minimal
// grpc.BidiStreamingClient[InteractionMessage, InteractionMessage] whose Recv
// drains a caller-supplied channel and counts how many frames the pump has
// consumed, so a test can deterministically reach the point where the output
// buffer is full and the pump is blocked on send.
type fakeInteractionStream struct {
	grpc.ClientStream
	recvCh chan *sandboxhost.InteractionMessage
	recv   atomic.Int32
}

func (f *fakeInteractionStream) Recv() (*sandboxhost.InteractionMessage, error) {
	msg, ok := <-f.recvCh
	if !ok {
		return nil, io.EOF
	}
	f.recv.Add(1)
	return msg, nil
}

func (f *fakeInteractionStream) Send(*sandboxhost.InteractionMessage) error { return nil }

func (f *fakeInteractionStream) CloseSend() error { return nil }

func TestRemoteInteractionPumpStopsOnCancelledContext(t *testing.T) {
	const frameCount = 32
	recvCh := make(chan *sandboxhost.InteractionMessage, frameCount)
	for i := 0; i < frameCount; i++ {
		recvCh <- &sandboxhost.InteractionMessage{
			Payload: &sandboxhost.InteractionMessage_Output{
				Output: &sandboxhost.RuntimeOutputFrame{Type: string(RuntimeOutputStdout), Data: []byte("x")},
			},
		}
	}
	stream := &fakeInteractionStream{recvCh: recvCh}
	ctx, cancel := context.WithCancel(context.Background())
	interaction := newRemoteInteraction(ctx, stream)

	// The output buffer holds 16 frames. Wait until the pump has consumed more
	// than that, so it is blocked sending into a full buffer that no one is
	// reading (a consumer that stopped calling Recv).
	deadline := time.Now().Add(5 * time.Second)
	for stream.recv.Load() <= 16 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stream.recv.Load() <= 16 {
		t.Fatalf("pump did not fill the output buffer, consumed = %d", stream.recv.Load())
	}

	cancel()

	waited := make(chan struct{})
	go func() {
		_, _ = interaction.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not exit after context cancellation")
	}
}
