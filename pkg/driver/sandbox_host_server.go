package driver

import (
	"context"
	"errors"
	"fmt"
	"io"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/driver/sandboxhost"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sandboxHostProtocolVersion is the wire contract version the sandbox-host
// speaks. It is monotonically increasing; the daemon accepts this version and
// the previous one (N and N-1). Bump it only with a corresponding daemon-side
// compatibility window.
//
// Version 2 added NetworkPolicy.deny so declared refusals reach the VM driver.
// A host that predates it silently drops deny entries, so the daemon fails
// closed (rather than applying a policy without its denials) when a sandbox
// declares deny rules against an N-1 host.
const sandboxHostProtocolVersion = 2

// sandboxHostDenyProtocolVersion is the first protocol version whose wire
// NetworkPolicy carries deny entries. A daemon driving a host below it refuses
// a deny-declaring sandbox instead of letting the host silently ignore the
// refusal.
const sandboxHostDenyProtocolVersion = 2

// sandboxHostMaxMsgSize is the gRPC message ceiling shared by the daemon and
// the sandbox-host. gRPC defaults to 4 MiB, which the accumulated ExecResult
// output exceeds on ordinary long runs (the guest journal already allows up to
// 8 MiB of merged stdout+stderr). The value is applied symmetrically so a large
// result never fails with ResourceExhausted after its chunks have streamed.
const sandboxHostMaxMsgSize = 64 << 20

// sandboxHostServer serves the sandbox-host gRPC contract by dispatching to the
// VM runtimes it holds in-process. It is the boundary the design document calls
// the sandbox-host: the long-lived process that owns VM state so the daemon can
// be upgraded without tearing VMs down.
type sandboxHostServer struct {
	sandboxhost.UnimplementedSandboxHostServer
	runtimes map[string]SandboxRuntime
}

func newSandboxHostServer(config *appconfig.Config) (*sandboxHostServer, error) {
	boxlite, err := newSandboxRuntime(config)
	if err != nil {
		return nil, err
	}
	microsandbox, err := newMicrosandboxRuntime(config)
	if err != nil {
		return nil, err
	}
	return &sandboxHostServer{
		runtimes: map[string]SandboxRuntime{
			RuntimeDriverBoxlite:      boxlite,
			RuntimeDriverMicrosandbox: microsandbox,
		},
	}, nil
}

func (s *sandboxHostServer) runtimeFor(sandbox *Sandbox) (SandboxRuntime, error) {
	if sandbox == nil {
		return nil, status.Error(codes.InvalidArgument, "sandbox is required")
	}
	driver := resolveRuntimeDriver(sandbox.Summary.Driver)
	runtime, ok := s.runtimes[driver]
	if !ok {
		return nil, status.Errorf(codes.Unimplemented, "sandbox-host does not serve driver %q", driver)
	}
	return runtime, nil
}

func (s *sandboxHostServer) runtimeForDriver(driver string) (SandboxRuntime, error) {
	runtime, ok := s.runtimes[resolveRuntimeDriver(driver)]
	if !ok {
		return nil, status.Errorf(codes.Unimplemented, "sandbox-host does not serve driver %q", driver)
	}
	return runtime, nil
}

// sandboxHostStatus converts a driver-boundary error into a gRPC status. The
// interaction-unsupported error is preserved as Unimplemented so the daemon can
// reconstruct the capability refusal; exec-termination-unconfirmed rides
// Aborted; context cancellation and deadline expiry keep their gRPC codes so
// the daemon can reconstruct errors.Is(err, context.Canceled) /
// context.DeadlineExceeded; everything else rides Internal with its message.
func sandboxHostStatus(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrRuntimeInteractionUnsupported):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, ErrExecTerminationUnconfirmed):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func (s *sandboxHostServer) Handshake(context.Context, *sandboxhost.HandshakeRequest) (*sandboxhost.HandshakeResponse, error) {
	return &sandboxhost.HandshakeResponse{ProtocolVersion: sandboxHostProtocolVersion}, nil
}

func (s *sandboxHostServer) NeedsCreate(ctx context.Context, req *sandboxhost.SandboxRequest) (*sandboxhost.NeedsCreateResponse, error) {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return nil, err
	}
	checker, ok := runtime.(SandboxNeedsCreate)
	if !ok {
		// A runtime that cannot answer existence must be treated as needing a
		// create so the daemon resolves the create-time environment.
		return &sandboxhost.NeedsCreateResponse{NeedsCreate: true}, nil
	}
	needs, err := checker.NeedsCreate(ctx, sandbox, vmState)
	if err != nil {
		return nil, sandboxHostStatus(err)
	}
	return &sandboxhost.NeedsCreateResponse{NeedsCreate: needs}, nil
}

func (s *sandboxHostServer) Create(ctx context.Context, req *sandboxhost.EnsureRequest) (*sandboxhost.SandboxVMInfo, error) {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	proxyState := unmarshalProxyState(req.GetProxyState())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return nil, err
	}
	// Creation must survive the initiating RPC's cancellation: the daemon may
	// restart mid-create (base-disk builds can take tens of minutes), and a new
	// daemon's re-ensure must find the create still in progress and wait for it
	// rather than starting a second one. The runtime's own ensure/create lock
	// serializes concurrent callers.
	info, err := runtime.EnsureSandbox(context.WithoutCancel(ctx), sandbox, vmState, proxyState)
	if err != nil {
		return nil, sandboxHostStatus(err)
	}
	return marshalSandboxVMInfo(info), nil
}

func (s *sandboxHostServer) Ensure(ctx context.Context, req *sandboxhost.EnsureRequest) (*sandboxhost.SandboxVMInfo, error) {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	proxyState := unmarshalProxyState(req.GetProxyState())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return nil, err
	}
	info, err := runtime.EnsureSandbox(ctx, sandbox, vmState, proxyState)
	if err != nil {
		return nil, sandboxHostStatus(err)
	}
	return marshalSandboxVMInfo(info), nil
}

func (s *sandboxHostServer) StopSandbox(ctx context.Context, req *sandboxhost.SandboxRequest) (*sandboxhost.StopSandboxResponse, error) {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return nil, err
	}
	missing, err := runtime.StopSandbox(ctx, sandbox, vmState)
	if err != nil {
		return nil, sandboxHostStatus(err)
	}
	return &sandboxhost.StopSandboxResponse{Missing: missing}, nil
}

func (s *sandboxHostServer) RemoveSandbox(ctx context.Context, req *sandboxhost.SandboxRequest) (*sandboxhost.RemoveSandboxResponse, error) {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return nil, err
	}
	if err := runtime.RemoveSandbox(ctx, sandbox, vmState); err != nil {
		return nil, sandboxHostStatus(err)
	}
	return &sandboxhost.RemoveSandboxResponse{}, nil
}

func (s *sandboxHostServer) IsSandboxAlive(ctx context.Context, req *sandboxhost.SandboxRequest) (*sandboxhost.IsSandboxAliveResponse, error) {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return nil, err
	}
	aliveRuntime, ok := runtime.(interface {
		IsSandboxAlive(context.Context, *Sandbox, VMState) (bool, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "runtime does not support sandbox liveness checks")
	}
	alive, err := aliveRuntime.IsSandboxAlive(ctx, sandbox, vmState)
	if err != nil {
		return nil, sandboxHostStatus(err)
	}
	return &sandboxhost.IsSandboxAliveResponse{Alive: alive}, nil
}

func (s *sandboxHostServer) Exec(ctx context.Context, req *sandboxhost.ExecRequest) (*sandboxhost.ExecResult, error) {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	spec := unmarshalExecSpec(req.GetSpec())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return nil, err
	}
	result, err := runtime.Exec(ctx, sandbox, vmState, spec)
	if err != nil {
		return nil, sandboxHostStatus(err)
	}
	return marshalExecResult(result), nil
}

func (s *sandboxHostServer) ExecStream(req *sandboxhost.ExecRequest, stream sandboxhost.SandboxHost_ExecStreamServer) error {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	spec := unmarshalExecSpec(req.GetSpec())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return err
	}
	result, err := runtime.ExecStream(stream.Context(), sandbox, vmState, spec, func(chunk ExecChunk) {
		_ = stream.Send(&sandboxhost.ExecStreamResponse{
			Payload: &sandboxhost.ExecStreamResponse_Chunk{Chunk: marshalExecChunk(chunk)},
		})
	})
	if err != nil {
		return sandboxHostStatus(err)
	}
	return stream.Send(&sandboxhost.ExecStreamResponse{
		Payload: &sandboxhost.ExecStreamResponse_Result{Result: marshalExecResult(result)},
	})
}

func (s *sandboxHostServer) Stats(ctx context.Context, req *sandboxhost.SandboxRequest) (*sandboxhost.SandboxStats, error) {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return nil, err
	}
	statsRuntime, ok := runtime.(interface {
		Stats(context.Context, *Sandbox, VMState) (SandboxStats, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "runtime does not support sandbox stats")
	}
	stats, err := statsRuntime.Stats(ctx, sandbox, vmState)
	if err != nil {
		return nil, sandboxHostStatus(err)
	}
	return marshalSandboxStats(stats), nil
}

func (s *sandboxHostServer) SignalGuestRuntime(ctx context.Context, req *sandboxhost.SignalGuestRuntimeRequest) (*sandboxhost.SignalGuestRuntimeResponse, error) {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return nil, err
	}
	signaler, ok := runtime.(interface {
		SignalGuestRuntime(context.Context, *Sandbox, VMState, string, RuntimeSignal) error
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "runtime does not support guest runtime signals")
	}
	if err := signaler.SignalGuestRuntime(ctx, sandbox, vmState, req.GetExecutionId(), RuntimeSignal(req.GetSignal())); err != nil {
		return nil, sandboxHostStatus(err)
	}
	return &sandboxhost.SignalGuestRuntimeResponse{}, nil
}

func (s *sandboxHostServer) TerminateProcessTree(ctx context.Context, req *sandboxhost.TerminateProcessTreeRequest) (*sandboxhost.TerminateProcessTreeResponse, error) {
	sandbox := unmarshalSandbox(req.GetSandbox())
	vmState := unmarshalVMState(req.GetVmState())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return nil, err
	}
	terminator, ok := runtime.(ProcessTreeTerminator)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "runtime does not support process-tree termination")
	}
	if err := terminator.TerminateProcessTree(ctx, sandbox, vmState, req.GetMarker()); err != nil {
		return nil, sandboxHostStatus(err)
	}
	return &sandboxhost.TerminateProcessTreeResponse{}, nil
}

func (s *sandboxHostServer) InteractionCapabilities(ctx context.Context, req *sandboxhost.DriverRequest) (*sandboxhost.RuntimeInteractionCapabilities, error) {
	runtime, err := s.runtimeForDriver(req.GetDriver())
	if err != nil {
		return nil, err
	}
	interactor, ok := runtime.(RuntimeInteractor)
	if !ok {
		return &sandboxhost.RuntimeInteractionCapabilities{}, nil
	}
	return marshalRuntimeInteractionCapabilities(interactor.InteractionCapabilities()), nil
}

func (s *sandboxHostServer) OpenInteraction(stream sandboxhost.SandboxHost_OpenInteractionServer) error {
	open, err := stream.Recv()
	if err != nil {
		return err
	}
	openReq := open.GetOpen()
	if openReq == nil {
		return status.Error(codes.InvalidArgument, "first interaction message must be open")
	}
	sandbox := unmarshalSandbox(openReq.GetSandbox())
	vmState := unmarshalVMState(openReq.GetVmState())
	spec := unmarshalRuntimeStartSpec(openReq.GetSpec())
	runtime, err := s.runtimeFor(sandbox)
	if err != nil {
		return err
	}
	interactor, ok := runtime.(RuntimeInteractor)
	if !ok {
		return status.Error(codes.Unimplemented, "runtime does not support interactions")
	}
	interaction, err := interactor.OpenInteraction(stream.Context(), sandbox, vmState, spec)
	if err != nil {
		return sandboxHostStatus(err)
	}
	interaction = GuardRuntimeInteractionInput(interaction)

	// Pump the local interaction's output back over the stream, ending with the
	// final result. The input direction is driven by the handler loop below.
	resultCh := make(chan error, 1)
	go func() {
		for {
			frame, recvErr := interaction.Recv()
			if recvErr != nil {
				if errors.Is(recvErr, io.EOF) {
					break
				}
				resultCh <- recvErr
				return
			}
			if sendErr := stream.Send(&sandboxhost.InteractionMessage{
				Payload: &sandboxhost.InteractionMessage_Output{Output: marshalRuntimeOutputFrame(frame)},
			}); sendErr != nil {
				resultCh <- sendErr
				return
			}
		}
		result, waitErr := interaction.Wait()
		if waitErr != nil {
			resultCh <- waitErr
			return
		}
		resultCh <- stream.Send(&sandboxhost.InteractionMessage{
			Payload: &sandboxhost.InteractionMessage_Result{Result: marshalRuntimeResult(&result)},
		})
	}()

	for {
		msg, recvErr := stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				return <-resultCh
			}
			return recvErr
		}
		switch payload := msg.GetPayload().(type) {
		case *sandboxhost.InteractionMessage_Input:
			if sendErr := interaction.Send(unmarshalRuntimeInputFrame(payload.Input)); sendErr != nil {
				return sendErr
			}
		case *sandboxhost.InteractionMessage_CloseSend:
			if closeErr := interaction.CloseSend(); closeErr != nil {
				return closeErr
			}
		default:
			return status.Error(codes.InvalidArgument, fmt.Sprintf("unexpected interaction message %T", msg.GetPayload()))
		}
	}
}
