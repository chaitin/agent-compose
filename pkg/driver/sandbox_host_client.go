package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/chaitin/agent-compose/pkg/driver/sandboxhost"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// remoteSandboxRuntime is the daemon-side SandboxRuntime that proxies a VM
// driver over the sandbox-host gRPC socket. It is the only code path the daemon
// uses for microsandbox and BoxLite: the VM runtimes are held by the
// sandbox-host process, never in the daemon.
type remoteSandboxRuntime struct {
	driver     string
	clientFunc func(context.Context) (sandboxhost.SandboxHostClient, error)
	version    func() int64
}

func newRemoteSandboxRuntime(driver string, clientFunc func(context.Context) (sandboxhost.SandboxHostClient, error), version func() int64) *remoteSandboxRuntime {
	if version == nil {
		version = func() int64 { return sandboxHostProtocolVersion }
	}
	return &remoteSandboxRuntime{driver: resolveRuntimeDriver(driver), clientFunc: clientFunc, version: version}
}

// NewRemoteSandboxRuntime returns a SandboxRuntime that proxies the named VM
// driver to the sandbox-host process behind connector. It implements the full
// SandboxRuntime surface plus the optional Stats, IsSandboxAlive,
// SignalGuestRuntime, TerminateProcessTree, and RuntimeInteractor interfaces.
func NewRemoteSandboxRuntime(driver string, connector *SandboxHostConnector) SandboxRuntime {
	return newRemoteSandboxRuntime(driver, connector.Client, connector.ProtocolVersion)
}

// requireDenyProtocolVersion fails closed when a sandbox declares deny entries
// but the negotiated host protocol predates deny support. A host below
// sandboxHostDenyProtocolVersion silently drops the entries, so accepting the
// sandbox would apply a policy without its refusals.
func requireDenyProtocolVersion(version int64, policy *SandboxNetworkPolicy) error {
	if policy != nil && len(policy.Deny) > 0 && version < sandboxHostDenyProtocolVersion {
		return fmt.Errorf("sandbox declares deny rules but the sandbox-host speaks protocol %d (deny requires %d): %w", version, sandboxHostDenyProtocolVersion, ErrSandboxNetworkEnforcementUnavailable)
	}
	return nil
}

// ErrRuntimeCapabilityUnsupported reports that the sandbox-host runtime does not
// implement an optional capability (stats, liveness, guest signals, or process
// tree termination). It is distinct from ErrRuntimeInteractionUnsupported, which
// is specific to the interaction surface: mapping every Unimplemented code to
// the interaction sentinel would make a "stats unsupported" refusal look like an
// interaction refusal.
var ErrRuntimeCapabilityUnsupported = errors.New("runtime capability unsupported")

// fromGRPCError reconstructs driver-boundary errors that were encoded as gRPC
// status codes by sandboxHostStatus. The daemon's error classification relies on
// sentinel matching, so the sentinel-backed cases are restored here.
func fromGRPCError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unimplemented:
		return fmt.Errorf("%w: %s", ErrRuntimeCapabilityUnsupported, status.Convert(err).Message())
	case codes.NotFound:
		return fmt.Errorf("%w: %s", ErrGuestRuntimeGone, status.Convert(err).Message())
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: %s", ErrGuestRuntimeNotReady, status.Convert(err).Message())
	case codes.Aborted:
		return fmt.Errorf("%w: %s", ErrExecTerminationUnconfirmed, status.Convert(err).Message())
	case codes.Canceled:
		return context.Canceled
	case codes.DeadlineExceeded:
		return context.DeadlineExceeded
	default:
		return err
	}
}

// fromInteractionGRPCError is fromGRPCError for the interaction surface: an
// Unimplemented refusal there means the runtime does not support interaction,
// not a generic capability, so it is restored as ErrRuntimeInteractionUnsupported
// instead of ErrRuntimeCapabilityUnsupported.
func fromInteractionGRPCError(err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.Unimplemented {
		return fmt.Errorf("%w: %s", ErrRuntimeInteractionUnsupported, status.Convert(err).Message())
	}
	return fromGRPCError(err)
}

// resolveSandboxCreateEnv resolves the sandbox's ProxyEnv closure and folds the
// result into RuntimeEnvItems so it can cross the RPC boundary. It returns a
// shallow copy with ProxyEnv cleared; the input is not mutated.
func resolveSandboxCreateEnv(ctx context.Context, sandbox *Sandbox) (*Sandbox, error) {
	if sandbox == nil || sandbox.ProxyEnv == nil {
		return sandbox, nil
	}
	proxyItems, err := sandbox.ProxyEnv(ctx)
	if err != nil {
		return nil, err
	}
	out := *sandbox
	out.ProxyEnv = nil
	if len(proxyItems) > 0 {
		out.RuntimeEnvItems = append(append([]SandboxEnvVar(nil), sandbox.RuntimeEnvItems...), proxyItems...)
	}
	return &out, nil
}

func (r *remoteSandboxRuntime) EnsureSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState, proxyState ProxyState) (SandboxVMInfo, error) {
	client, err := r.clientFunc(ctx)
	if err != nil {
		return SandboxVMInfo{}, err
	}
	if err := requireDenyProtocolVersion(r.version(), sandbox.NetworkPolicy); err != nil {
		return SandboxVMInfo{}, err
	}
	needs, err := client.NeedsCreate(ctx, &sandboxhost.SandboxRequest{
		Sandbox: marshalSandbox(sandbox),
		VmState: marshalVMState(vmState),
	})
	if err != nil {
		return SandboxVMInfo{}, fromGRPCError(err)
	}
	ensureReq := func(s *Sandbox) *sandboxhost.EnsureRequest {
		return &sandboxhost.EnsureRequest{
			Sandbox:    marshalSandbox(s),
			VmState:    marshalVMState(vmState),
			ProxyState: marshalProxyState(proxyState),
		}
	}
	if needs.GetNeedsCreate() {
		createSandbox, resolveErr := resolveSandboxCreateEnv(ctx, sandbox)
		if resolveErr != nil {
			return SandboxVMInfo{}, resolveErr
		}
		info, createErr := client.Create(ctx, ensureReq(createSandbox))
		if createErr != nil {
			return SandboxVMInfo{}, fromGRPCError(createErr)
		}
		return unmarshalSandboxVMInfo(info), nil
	}
	info, ensureErr := client.Ensure(ctx, ensureReq(sandbox))
	if ensureErr != nil {
		return SandboxVMInfo{}, fromGRPCError(ensureErr)
	}
	return unmarshalSandboxVMInfo(info), nil
}

func (r *remoteSandboxRuntime) StopSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState) (bool, error) {
	client, err := r.clientFunc(ctx)
	if err != nil {
		return false, err
	}
	resp, err := client.StopSandbox(ctx, &sandboxhost.SandboxRequest{
		Sandbox: marshalSandbox(sandbox),
		VmState: marshalVMState(vmState),
	})
	if err != nil {
		return false, fromGRPCError(err)
	}
	return resp.GetMissing(), nil
}

func (r *remoteSandboxRuntime) RemoveSandbox(ctx context.Context, sandbox *Sandbox, vmState VMState) error {
	client, err := r.clientFunc(ctx)
	if err != nil {
		return err
	}
	_, err = client.RemoveSandbox(ctx, &sandboxhost.SandboxRequest{
		Sandbox: marshalSandbox(sandbox),
		VmState: marshalVMState(vmState),
	})
	return fromGRPCError(err)
}

func (r *remoteSandboxRuntime) IsSandboxAlive(ctx context.Context, sandbox *Sandbox, vmState VMState) (bool, error) {
	client, err := r.clientFunc(ctx)
	if err != nil {
		return false, err
	}
	resp, err := client.IsSandboxAlive(ctx, &sandboxhost.SandboxRequest{
		Sandbox: marshalSandbox(sandbox),
		VmState: marshalVMState(vmState),
	})
	if err != nil {
		return false, fromGRPCError(err)
	}
	return resp.GetAlive(), nil
}

func (r *remoteSandboxRuntime) Exec(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec) (ExecResult, error) {
	client, err := r.clientFunc(ctx)
	if err != nil {
		return ExecResult{}, err
	}
	resp, err := client.Exec(ctx, &sandboxhost.ExecRequest{
		Sandbox: marshalSandbox(sandbox),
		VmState: marshalVMState(vmState),
		Spec:    marshalExecSpec(spec),
	})
	if err != nil {
		return ExecResult{}, fromGRPCError(err)
	}
	return unmarshalExecResult(resp), nil
}

func (r *remoteSandboxRuntime) ExecStream(ctx context.Context, sandbox *Sandbox, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	client, err := r.clientFunc(ctx)
	if err != nil {
		return ExecResult{}, err
	}
	recv, err := client.ExecStream(ctx, &sandboxhost.ExecRequest{
		Sandbox: marshalSandbox(sandbox),
		VmState: marshalVMState(vmState),
		Spec:    marshalExecSpec(spec),
	})
	if err != nil {
		return ExecResult{}, fromGRPCError(err)
	}
	for {
		resp, recvErr := recv.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				return ExecResult{}, errors.New("exec stream ended without a result")
			}
			return ExecResult{}, fromGRPCError(recvErr)
		}
		switch payload := resp.GetPayload().(type) {
		case *sandboxhost.ExecStreamResponse_Chunk:
			if stream != nil {
				stream(unmarshalExecChunk(payload.Chunk))
			}
		case *sandboxhost.ExecStreamResponse_Result:
			return unmarshalExecResult(payload.Result), nil
		}
	}
}

func (r *remoteSandboxRuntime) Stats(ctx context.Context, sandbox *Sandbox, vmState VMState) (SandboxStats, error) {
	client, err := r.clientFunc(ctx)
	if err != nil {
		return SandboxStats{}, err
	}
	resp, err := client.Stats(ctx, &sandboxhost.SandboxRequest{
		Sandbox: marshalSandbox(sandbox),
		VmState: marshalVMState(vmState),
	})
	if err != nil {
		return SandboxStats{}, fromGRPCError(err)
	}
	return unmarshalSandboxStats(resp), nil
}

func (r *remoteSandboxRuntime) SignalGuestRuntime(ctx context.Context, sandbox *Sandbox, vmState VMState, executionID string, signal RuntimeSignal) error {
	client, err := r.clientFunc(ctx)
	if err != nil {
		return err
	}
	_, err = client.SignalGuestRuntime(ctx, &sandboxhost.SignalGuestRuntimeRequest{
		Sandbox:     marshalSandbox(sandbox),
		VmState:     marshalVMState(vmState),
		ExecutionId: executionID,
		Signal:      string(signal),
	})
	return fromGRPCError(err)
}

func (r *remoteSandboxRuntime) TerminateProcessTree(ctx context.Context, sandbox *Sandbox, vmState VMState, marker string) error {
	client, err := r.clientFunc(ctx)
	if err != nil {
		return err
	}
	_, err = client.TerminateProcessTree(ctx, &sandboxhost.TerminateProcessTreeRequest{
		Sandbox: marshalSandbox(sandbox),
		VmState: marshalVMState(vmState),
		Marker:  marker,
	})
	return fromGRPCError(err)
}

func (r *remoteSandboxRuntime) InteractionCapabilities() RuntimeInteractionCapabilities {
	// The capability probe carries no caller context (the RuntimeInteractor
	// interface is context-free), so bound it explicitly: an unreachable or
	// wedged host must not stall a start path indefinitely. A transport failure
	// degrades to no interaction support, matching a local runtime that cannot
	// answer the probe.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := r.clientFunc(ctx)
	if err != nil {
		return RuntimeInteractionCapabilities{}
	}
	caps, err := client.InteractionCapabilities(ctx, &sandboxhost.DriverRequest{Driver: r.driver})
	if err != nil {
		return RuntimeInteractionCapabilities{}
	}
	return unmarshalRuntimeInteractionCapabilities(caps)
}

func (r *remoteSandboxRuntime) OpenInteraction(ctx context.Context, sandbox *Sandbox, vmState VMState, spec RuntimeStartSpec) (RuntimeInteraction, error) {
	if err := r.InteractionCapabilities().ValidateStartSpec(r.driver, spec); err != nil {
		return nil, err
	}
	client, err := r.clientFunc(ctx)
	if err != nil {
		return nil, err
	}
	stream, err := client.OpenInteraction(ctx)
	if err != nil {
		return nil, fromInteractionGRPCError(err)
	}
	if err := stream.Send(&sandboxhost.InteractionMessage{
		Payload: &sandboxhost.InteractionMessage_Open{
			Open: &sandboxhost.OpenInteractionRequest{
				Sandbox: marshalSandbox(sandbox),
				VmState: marshalVMState(vmState),
				Spec:    marshalRuntimeStartSpec(spec),
			},
		},
	}); err != nil {
		return nil, fromInteractionGRPCError(err)
	}
	return newRemoteInteraction(ctx, stream), nil
}

// remoteInteraction adapts the sandbox-host interaction bidirectional stream to
// the RuntimeInteraction interface. A pump goroutine owns stream.Receive and
// fans output frames out to Recv while stashing the terminal result for Wait.
type remoteInteraction struct {
	stream    sandboxhost.SandboxHost_OpenInteractionClient
	ctx       context.Context
	output    chan RuntimeOutputFrame
	result    RuntimeResult
	resultErr error
	done      chan struct{}
	sendMu    sync.Mutex
}

func newRemoteInteraction(ctx context.Context, stream sandboxhost.SandboxHost_OpenInteractionClient) *remoteInteraction {
	ri := &remoteInteraction{
		stream: stream,
		ctx:    ctx,
		output: make(chan RuntimeOutputFrame, 16),
		done:   make(chan struct{}),
	}
	go ri.pump()
	return ri
}

func (ri *remoteInteraction) pump() {
	defer close(ri.done)
	defer close(ri.output)
	for {
		msg, err := ri.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			ri.resultErr = fromInteractionGRPCError(err)
			return
		}
		switch payload := msg.GetPayload().(type) {
		case *sandboxhost.InteractionMessage_Output:
			// A consumer that stops reading (for example, a frontend that
			// disconnects and never calls Recv again) must not strand this pump
			// on a full output buffer: once ctx is cancelled, drop the frame and
			// exit instead of blocking forever.
			select {
			case ri.output <- unmarshalRuntimeOutputFrame(payload.Output):
			case <-ri.ctx.Done():
				ri.resultErr = ri.ctx.Err()
				return
			}
		case *sandboxhost.InteractionMessage_Result:
			if payload.Result != nil {
				ri.result = *unmarshalRuntimeResult(payload.Result)
			}
			return
		}
	}
}

func (ri *remoteInteraction) Send(frame RuntimeInputFrame) error {
	ri.sendMu.Lock()
	defer ri.sendMu.Unlock()
	return ri.stream.Send(&sandboxhost.InteractionMessage{
		Payload: &sandboxhost.InteractionMessage_Input{Input: marshalRuntimeInputFrame(frame)},
	})
}

func (ri *remoteInteraction) CloseSend() error {
	ri.sendMu.Lock()
	defer ri.sendMu.Unlock()
	if err := ri.stream.Send(&sandboxhost.InteractionMessage{
		Payload: &sandboxhost.InteractionMessage_CloseSend{CloseSend: &sandboxhost.CloseSendSignal{}},
	}); err != nil {
		return err
	}
	return ri.stream.CloseSend()
}

func (ri *remoteInteraction) Recv() (RuntimeOutputFrame, error) {
	frame, ok := <-ri.output
	if !ok {
		return RuntimeOutputFrame{}, io.EOF
	}
	return frame, nil
}

func (ri *remoteInteraction) Wait() (RuntimeResult, error) {
	<-ri.done
	return ri.result, ri.resultErr
}
