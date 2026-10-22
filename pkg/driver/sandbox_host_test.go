package driver

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

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
	f.closed = true
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

func (f *fakeRuntimeInteraction) Wait() (RuntimeResult, error) {
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
	grpcServer := grpc.NewServer()
	sandboxhost.RegisterSandboxHostServer(grpcServer, &sandboxHostServer{runtimes: runtimes})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	return NewSandboxHostConnector(socketPath, dir)
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
		{name: "interaction unsupported", ensureErr: ErrRuntimeInteractionUnsupported, wantSentinel: ErrRuntimeInteractionUnsupported},
		{name: "exec termination unconfirmed", ensureErr: ErrExecTerminationUnconfirmed, wantSentinel: ErrExecTerminationUnconfirmed},
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

func TestRemoteSandboxRuntimeOpenInteraction(t *testing.T) {
	fake := newFakeSandboxHostRuntime()
	fake.interaction = &fakeRuntimeInteraction{
		outputs: []RuntimeOutputFrame{
			{Type: RuntimeOutputStdout, Data: []byte("hello\n")},
		},
		result: RuntimeResult{OperationID: "op-1", ExitCode: 0, Success: true},
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
	if code := status.Code(sandboxHostStatus(ErrRuntimeInteractionUnsupported)); code != codes.Unimplemented {
		t.Fatalf("interaction unsupported code = %v, want Unimplemented", code)
	}
	if code := status.Code(sandboxHostStatus(ErrExecTerminationUnconfirmed)); code != codes.Aborted {
		t.Fatalf("exec termination code = %v, want Aborted", code)
	}
	if code := status.Code(sandboxHostStatus(errors.New("boom"))); code != codes.Internal {
		t.Fatalf("generic code = %v, want Internal", code)
	}

	if !errors.Is(fromGRPCError(status.Error(codes.Unimplemented, "no")), ErrRuntimeInteractionUnsupported) {
		t.Fatalf("fromGRPCError(Unimplemented) must map to interaction unsupported")
	}
	if !errors.Is(fromGRPCError(status.Error(codes.Aborted, "no")), ErrExecTerminationUnconfirmed) {
		t.Fatalf("fromGRPCError(Aborted) must map to exec termination")
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
