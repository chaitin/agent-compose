package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
)

type SandboxRuntime interface {
	EnsureSandbox(context.Context, *domain.Sandbox, domain.VMState, domain.ProxyState) (domain.SandboxVMInfo, error)
	StopSandbox(context.Context, *domain.Sandbox, domain.VMState) (bool, error)
	RemoveSandbox(context.Context, *domain.Sandbox, domain.VMState) error
	Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error)
	ExecStream(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error)
}

type SandboxStatsRuntime interface {
	Stats(context.Context, *domain.Sandbox, domain.VMState) (domain.SandboxStats, error)
}

type SandboxGracefulStopRuntime interface {
	PrepareSandboxStop(context.Context, *domain.Sandbox, domain.VMState, time.Duration) (sandboxes.StopPreparationResult, error)
}

// GuestFileReader and GuestDirReader are implemented only by drivers with no
// shared filesystem between the daemon and the sandbox (currently just k8s
// - see docs/design/k8s_pod_runtime_driver_design.md §2.1). Drivers with a
// real mount (docker/boxlite/microsandbox) have no need for them; callers
// that want a driver-agnostic path should type-assert and fall back to
// reading the sandbox's local mounted path directly when unsupported.
type GuestFileReader interface {
	ReadGuestFile(context.Context, *domain.Sandbox, domain.VMState, string) ([]byte, error)
}

type GuestDirReader interface {
	ReadGuestDir(context.Context, *domain.Sandbox, domain.VMState, string, string) error
}

// GuestFileWriter and GuestDirWriter are the push-side counterparts to
// GuestFileReader/GuestDirReader, for the daemon-writes/guest-reads
// direction (prompts, skills, generated config) - see design doc §2.1/§6.
// Same driver support and fallback expectations as the reader interfaces.
type GuestFileWriter interface {
	WriteGuestFile(context.Context, *domain.Sandbox, domain.VMState, string, []byte) error
}

type GuestDirWriter interface {
	WriteGuestDir(context.Context, *domain.Sandbox, domain.VMState, string, string) error
}

type RuntimeProvider interface {
	ForDriver(string) (SandboxRuntime, error)
	ForSession(*domain.Sandbox) (SandboxRuntime, error)
}

type runtimeProvider struct {
	config   *appconfig.Config
	runtimes map[string]SandboxRuntime
}

type driverRuntimeAdapter struct {
	runtime    driverpkg.SandboxRuntime
	executions *sandboxExecutions
	// networkPolicies derives the driver-boundary egress policy from the
	// sandbox's declared compose network policy. Its zero value is inert: an
	// adapter built without a resolver resolves no declaration and leaves
	// NetworkPolicy nil (D3).
	networkPolicies driverSandboxNetworkPolicy
}

// guestFileRuntimeAdapter adds the no-shared-filesystem capabilities only to
// runtimes that actually implement them. Keeping these methods off
// driverRuntimeAdapter is important: otherwise every wrapped runtime satisfies
// GuestFileReader/GuestDirWriter and callers discover the unsupported
// capability only after starting an operation.
type guestFileRuntimeAdapter struct {
	driverRuntimeAdapter
}

// ProxyStateGetter is the read side of sandboxstore.Store's persisted
// ProxyState, narrowed to just what the k8s driver needs to resolve whether
// a sandbox's Pod should launch jupyter (see driverpkg.ProxyStateReader).
// *sandboxstore.Store satisfies this directly.
type ProxyStateGetter interface {
	GetProxyState(sandboxID string) (domain.ProxyState, error)
}

func NewRuntimeProvider(config *appconfig.Config, proxyStateGetter ProxyStateGetter, networkDeclarations SandboxNetworkDeclarationResolver) (RuntimeProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("runtime provider config is required")
	}
	if err := driverpkg.ValidateCompiledRuntimeDriver(config.RuntimeDriver); err != nil {
		return nil, classifyRuntimeProviderError(err)
	}

	boxliteRuntime, err := driverpkg.NewBoxliteRuntime(config)
	if err != nil {
		return nil, err
	}
	dockerRuntime, err := driverpkg.NewDockerRuntime(config)
	if err != nil {
		return nil, err
	}
	microsandboxRuntime, err := driverpkg.NewMicrosandboxRuntime(config)
	if err != nil {
		return nil, err
	}
	var proxyStateReader driverpkg.ProxyStateReader
	if proxyStateGetter != nil {
		proxyStateReader = func(sandboxID string) (driverpkg.ProxyState, error) {
			state, err := proxyStateGetter.GetProxyState(sandboxID)
			if err != nil {
				return driverpkg.ProxyState{}, err
			}
			return execution.ToDriverProxyState(state), nil
		}
	}
	k8sRuntime, err := driverpkg.NewK8sRuntime(config, proxyStateReader)
	if err != nil {
		return nil, err
	}
	executions := newSandboxExecutions()
	networkPolicies := driverSandboxNetworkPolicy{config: config, declarations: networkDeclarations}
	return &runtimeProvider{
		config: config,
		runtimes: map[string]SandboxRuntime{
			driverpkg.RuntimeDriverBoxlite:      driverRuntimeAdapter{runtime: boxliteRuntime, executions: executions, networkPolicies: networkPolicies},
			driverpkg.RuntimeDriverDocker:       driverRuntimeAdapter{runtime: dockerRuntime, executions: executions, networkPolicies: networkPolicies},
			driverpkg.RuntimeDriverMicrosandbox: driverRuntimeAdapter{runtime: microsandboxRuntime, executions: executions, networkPolicies: networkPolicies},
			driverpkg.RuntimeDriverK8s: guestFileRuntimeAdapter{driverRuntimeAdapter{
				runtime: k8sRuntime, executions: executions, networkPolicies: networkPolicies,
			}},
		},
	}, nil
}

func (p *runtimeProvider) ForDriver(driver string) (SandboxRuntime, error) {
	driver = driverpkg.ResolveRuntimeDriver(driver)
	if err := driverpkg.ValidateRuntimeDriver(driver); err != nil {
		return nil, err
	}
	if err := driverpkg.ValidateCompiledRuntimeDriver(driver); err != nil {
		return nil, classifyRuntimeProviderError(err)
	}
	runtime, ok := p.runtimes[driver]
	if !ok {
		return nil, fmt.Errorf("agent-compose runtime %q is not configured", driver)
	}
	return runtime, nil
}

func classifyRuntimeProviderError(err error) error {
	if errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
		return domain.ClassifyError(domain.ErrUnsupported, "", err)
	}
	return err
}

func (p *runtimeProvider) ForSession(session *domain.Sandbox) (SandboxRuntime, error) {
	if session == nil {
		return nil, fmt.Errorf("session is required")
	}
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(session.Summary.Driver, p.config.RuntimeDriver)
	if err != nil {
		return nil, err
	}
	return p.ForDriver(driver)
}

func (r driverRuntimeAdapter) EnsureSandbox(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, proxyState domain.ProxyState) (domain.SandboxVMInfo, error) {
	driverSandbox := execution.ToDriverSandbox(session)
	// The driver-boundary policy is derived from the project's normalized
	// declaration, not from the domain session, and only a declared policy sets
	// it. An undeclared sandbox keeps a nil policy and today's behavior (D3).
	policy, err := r.networkPolicies.policy(ctx, session)
	if err != nil {
		return domain.SandboxVMInfo{}, err
	}
	if driverSandbox != nil {
		driverSandbox.NetworkPolicy = policy
	}
	info, err := r.runtime.EnsureSandbox(ctx, driverSandbox, execution.ToDriverVMState(vmState), execution.ToDriverProxyState(proxyState))
	if err != nil {
		return domain.SandboxVMInfo{}, err
	}
	return execution.FromDriverSandboxVMInfo(info), nil
}

func (r driverRuntimeAdapter) StopSandbox(ctx context.Context, session *domain.Sandbox, vmState domain.VMState) (bool, error) {
	if r.executions != nil {
		r.executions.ensureBlocked(session.Summary.ID)
		// Preserve the runtime driver's complete stop deadline. The sandbox stop
		// is the final containment boundary for executions that have not yet
		// returned after cancellation.
		r.executions.cancel(session.Summary.ID)
	}
	missing, err := r.runtime.StopSandbox(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState))
	return missing, err
}

func (r driverRuntimeAdapter) RemoveSandbox(ctx context.Context, session *domain.Sandbox, vmState domain.VMState) error {
	return r.runtime.RemoveSandbox(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState))
}

func (r driverRuntimeAdapter) Exec(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, spec domain.ExecSpec) (domain.ExecResult, error) {
	execCtx, marked, finish, err := r.beginExecution(ctx, session, spec)
	if err != nil {
		return domain.ExecResult{}, err
	}
	defer finish()
	result, err := r.runtime.Exec(execCtx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), execution.ToDriverExecSpec(marked))
	return execution.FromDriverExecResult(result), classifyExecTerminationError(err)
}

func (r driverRuntimeAdapter) ExecStream(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, spec domain.ExecSpec, stream domain.ExecStreamWriter) (domain.ExecResult, error) {
	execCtx, marked, finish, err := r.beginExecution(ctx, session, spec)
	if err != nil {
		return domain.ExecResult{}, err
	}
	defer finish()
	driverStream := func(chunk driverpkg.ExecChunk) {
		if stream != nil {
			stream(domain.ExecChunk{Text: chunk.Text, Stream: domainStreamFromDriver(chunk.Stream)})
		}
	}
	result, err := r.runtime.ExecStream(execCtx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), execution.ToDriverExecSpec(marked), driverStream)
	return execution.FromDriverExecResult(result), classifyExecTerminationError(err)
}

func classifyExecTerminationError(err error) error {
	if errors.Is(err, driverpkg.ErrExecTerminationUnconfirmed) {
		return domain.ClassifyError(domain.ErrExecTerminationUnconfirmed, "", err)
	}
	return err
}

func (r driverRuntimeAdapter) OpenInteraction(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, spec driverpkg.RuntimeStartSpec) (driverpkg.RuntimeInteraction, error) {
	interactor, ok := r.runtime.(driverpkg.RuntimeInteractor)
	if !ok {
		return driverpkg.UnsupportedRuntimeInteraction(vmState.Driver, driverpkg.RuntimeInteractionCapabilities{}, spec)
	}
	execCtx, marked, finish, err := r.beginInteraction(ctx, session, spec)
	if err != nil {
		return nil, err
	}
	interaction, err := interactor.OpenInteraction(execCtx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), marked)
	if err != nil {
		finish()
		return nil, err
	}
	tracked := &trackedRuntimeInteraction{
		RuntimeInteraction: interaction,
		finish:             finish,
		done:               make(chan struct{}),
	}
	go tracked.finishWhenContextEnds(execCtx)
	return tracked, nil
}

func domainStreamFromDriver(stream driverpkg.StdioStream) domain.StdioStream {
	if driverpkg.NormalizeStdioStream(stream) == driverpkg.StdioStderr {
		return domain.StdioStderr
	}
	return domain.StdioStdout
}

func (r driverRuntimeAdapter) Stats(ctx context.Context, session *domain.Sandbox, vmState domain.VMState) (domain.SandboxStats, error) {
	statsRuntime, ok := r.runtime.(interface {
		Stats(context.Context, *driverpkg.Sandbox, driverpkg.VMState) (driverpkg.SandboxStats, error)
	})
	if !ok {
		return domain.SandboxStats{}, domain.ClassifyError(domain.ErrUnsupported, "sandbox stats are unsupported by this runtime driver", nil)
	}
	stats, err := statsRuntime.Stats(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState))
	return execution.FromDriverSandboxStats(stats), err
}

func (r driverRuntimeAdapter) IsSandboxAlive(ctx context.Context, session *domain.Sandbox, vmState domain.VMState) (bool, error) {
	aliveRuntime, ok := r.runtime.(interface {
		IsSandboxAlive(context.Context, *driverpkg.Sandbox, driverpkg.VMState) (bool, error)
	})
	if !ok {
		return false, domain.ClassifyError(domain.ErrUnsupported, "runtime does not support sandbox liveness checks", nil)
	}
	return aliveRuntime.IsSandboxAlive(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState))
}

func (r guestFileRuntimeAdapter) ReadGuestFile(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, guestPath string) ([]byte, error) {
	reader, ok := r.runtime.(interface {
		ReadGuestFile(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string) ([]byte, error)
	})
	if !ok {
		return nil, domain.ClassifyError(domain.ErrUnsupported, "runtime does not support reading guest files directly", nil)
	}
	return reader.ReadGuestFile(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), guestPath)
}

func (r guestFileRuntimeAdapter) ReadGuestDir(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, guestDir, hostDestDir string) error {
	reader, ok := r.runtime.(interface {
		ReadGuestDir(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string, string) error
	})
	if !ok {
		return domain.ClassifyError(domain.ErrUnsupported, "runtime does not support reading guest directories directly", nil)
	}
	return reader.ReadGuestDir(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), guestDir, hostDestDir)
}

func (r guestFileRuntimeAdapter) WriteGuestFile(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, guestPath string, content []byte) error {
	writer, ok := r.runtime.(interface {
		WriteGuestFile(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string, []byte) error
	})
	if !ok {
		return domain.ClassifyError(domain.ErrUnsupported, "runtime does not support writing guest files directly", nil)
	}
	return writer.WriteGuestFile(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), guestPath, content)
}

func (r guestFileRuntimeAdapter) WriteGuestDir(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, hostSrcDir, guestDir string) error {
	writer, ok := r.runtime.(interface {
		WriteGuestDir(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string, string) error
	})
	if !ok {
		return domain.ClassifyError(domain.ErrUnsupported, "runtime does not support writing guest directories directly", nil)
	}
	return writer.WriteGuestDir(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), hostSrcDir, guestDir)
}
