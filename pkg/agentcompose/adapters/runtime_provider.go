package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/egress"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/telemetry"
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

// SandboxRuntimePreflight is implemented by a driver whose runtime can prove it
// is usable before the start path creates any runtime state. StartSandboxVM asks
// the capability before EnsureSandbox, so a runtime that already knows it cannot
// start fails the sandbox with a classified reason instead of leaving partially
// created runtime state behind. Runtimes without it have nothing to check and
// are started directly.
type SandboxRuntimePreflight interface {
	PreflightSandboxRuntime(context.Context, *domain.Sandbox, domain.VMState) error
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
	recorder *telemetry.Recorder
	runtimes map[string]SandboxRuntime
	// egressProxy mediates a declaring sandbox's egress through the engine's
	// forward proxy. Its zero value is inert: no credential store, no sandbox
	// loader, no mediation, and the unenforced-declaration gate still applies.
	egressProxy SandboxEgressProxy
}

type driverRuntimeAdapter struct {
	runtime    driverpkg.SandboxRuntime
	executions *sandboxExecutions
	recorder   *telemetry.Recorder
	// networkPolicies derives the driver-boundary egress policy from the
	// sandbox's declared compose network policy. Its zero value is inert: an
	// adapter built without a resolver resolves no declaration and leaves
	// NetworkPolicy nil (D3).
	networkPolicies driverSandboxNetworkPolicy
	// egressProxy, when available, routes a declaring sandbox's HTTP traffic
	// through the engine's own forward proxy and marks the driver-boundary
	// policy as mediated, so the driver reports enforcement instead of warning
	// that the declaration is unenforced.
	egressProxy SandboxEgressProxy
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

// RuntimeProviderOption customizes how the provider decorates the driver
// runtimes it hands out.
type RuntimeProviderOption func(*runtimeProvider)

// WithRuntimeRecorder attaches the daemon recorder to every driver runtime, so
// driver operations export as child spans and metrics of the run that requested
// them. A nil recorder leaves the runtimes untraced.
func WithRuntimeRecorder(recorder *telemetry.Recorder) RuntimeProviderOption {
	return func(provider *runtimeProvider) { provider.recorder = recorder }
}

// WithSandboxEgressProxy attaches the engine-side egress proxy mediation: a
// sandbox that declares deny semantics is handed the proxy as its egress path
// and the driver-boundary policy records that the engine, not the driver,
// decides its connections. Without it, a sandbox whose driver cannot apply the
// declaration stays on the unenforced-declaration path.
func WithSandboxEgressProxy(mediation SandboxEgressProxy) RuntimeProviderOption {
	return func(provider *runtimeProvider) {
		mediation.config = provider.config
		provider.egressProxy = mediation
	}
}

func NewRuntimeProvider(config *appconfig.Config, proxyStateGetter ProxyStateGetter, networkDeclarations SandboxNetworkDeclarationResolver, opts ...RuntimeProviderOption) (RuntimeProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("runtime provider config is required")
	}
	if err := driverpkg.ValidateCompiledRuntimeDriver(config.RuntimeDriver); err != nil {
		return nil, classifyRuntimeProviderError(err)
	}
	provider := &runtimeProvider{config: config}
	for _, option := range opts {
		option(provider)
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
	// The mediation decides with the same resolved declaration and engine
	// endpoints as the driver-boundary policy, so both read one seam.
	provider.egressProxy.networkPolicies = networkPolicies
	provider.runtimes = map[string]SandboxRuntime{
		driverpkg.RuntimeDriverBoxlite:      driverRuntimeAdapter{runtime: boxliteRuntime, executions: executions, recorder: provider.recorder, networkPolicies: networkPolicies, egressProxy: provider.egressProxy},
		driverpkg.RuntimeDriverDocker:       driverRuntimeAdapter{runtime: dockerRuntime, executions: executions, recorder: provider.recorder, networkPolicies: networkPolicies, egressProxy: provider.egressProxy},
		driverpkg.RuntimeDriverMicrosandbox: driverRuntimeAdapter{runtime: microsandboxRuntime, executions: executions, recorder: provider.recorder, networkPolicies: networkPolicies, egressProxy: provider.egressProxy},
		driverpkg.RuntimeDriverK8s: guestFileRuntimeAdapter{driverRuntimeAdapter{
			runtime: k8sRuntime, executions: executions, recorder: provider.recorder, networkPolicies: networkPolicies, egressProxy: provider.egressProxy,
		}},
	}
	return provider, nil
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

// driverSandbox maps a domain sandbox to the driver-boundary value and attaches
// the network policy its project declared.
//
// The derived policy is not optional for any path that can reach a driver's
// EnsureSandbox: the guest-file paths materialize the runtime implicitly, so a
// sandbox handed to the driver without the policy would create a Pod or
// container before the enforcement gate ever saw the declaration. Every such
// call site must use this helper rather than execution.ToDriverSandbox directly.
func (r driverRuntimeAdapter) driverSandbox(ctx context.Context, session *domain.Sandbox) (*driverpkg.Sandbox, domain.SandboxEgressPolicy, error) {
	driverSandbox := execution.ToDriverSandbox(session)
	// The driver-boundary policy is derived from the project's normalized
	// declaration, not from the domain session, and only a declared policy sets
	// it. An undeclared sandbox keeps a nil policy and today's behavior (D3).
	// The effective policy is compiled from the same read, so the generation
	// recorded below always belongs to the declaration the driver was given.
	policy, effective, err := r.networkPolicies.resolve(ctx, session)
	if err != nil {
		return nil, domain.SandboxEgressPolicy{}, err
	}
	mediated := false
	if driverSandbox != nil {
		driverSandbox.NetworkPolicy = policy
		// A declaration that denies by default is only really enforced where the
		// engine is in the traffic path. Routing the guest through the forward
		// proxy is what puts it there, and the same step records the mediation
		// on the policy so no driver can believe in one the engine did not wire.
		//
		// The credential and the environment that carries it are resolved by the
		// driver, and only when it creates a runtime: a guest reads its
		// environment once, so issuing here on every ensure would mint live
		// credentials no guest could ever present. Whether the engine can mediate
		// at all is a pure fact about the deployment and this sandbox, so the
		// report below still knows it before the driver runs.
		if policy != nil && policy.DenyByDefault() && r.egressProxy.available(session) {
			driverSandbox.ProxyEnv = r.egressProxy.proxyEnvProvider(session)
			policy.MediatedByForwardProxy = true
			mediated = true
		}
	}
	// Record what this creation is about to install. The record is the only way
	// a later start can tell which policy the runtime that is running was built
	// for, because a declaration read at creation cannot be recovered from the
	// declaration that is in force then.
	record := r.egressRuntimePolicy(session, policy, effective, mediated)
	return driverSandbox, record, nil
}

// egressRuntimePolicy describes the effective policy this creation installs for
// a sandbox: the generation the proxy will decide against, and how the policy
// reaches the runtime. The generation comes from the same compilation the proxy
// uses (policyForSession), so a decision the proxy records carries a generation
// that can be compared with this one.
func (r driverRuntimeAdapter) egressRuntimePolicy(session *domain.Sandbox, policy *driverpkg.SandboxNetworkPolicy, effective egress.Policy, mediated bool) domain.SandboxEgressPolicy {
	application := egress.ApplicationOpen
	switch {
	case mediated:
		application = egress.ApplicationMediated
	case policy != nil && policy.DenyByDefault():
		// The declaration denies by default and the engine is not in the traffic
		// path, so whatever the driver does with it is what the runtime gets. A
		// driver that applies nothing leaves the sandbox unrestricted.
		if driverpkg.SandboxNetworkEnforcementFor(session.Summary.Driver).Strength == driverpkg.SandboxEgressStrengthNone {
			application = egress.ApplicationUnenforced
		} else {
			application = egress.ApplicationDriverApplied
		}
	}
	return sandboxEgressPolicyRecord(egress.RuntimePolicy{Generation: effective.Generation(), Application: application})
}

func (r driverRuntimeAdapter) EnsureSandbox(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, proxyState domain.ProxyState) (domain.SandboxVMInfo, error) {
	ctx, endSpan := startRuntimeSpan(ctx, r.recorder, telemetry.SpanSandboxEnsure, session, vmState.Driver)
	startedAt := time.Now()
	driverSandbox, egressPolicy, err := r.driverSandbox(ctx, session)
	if err != nil {
		endSpan(err)
		return domain.SandboxVMInfo{}, err
	}
	info, err := r.runtime.EnsureSandbox(ctx, driverSandbox, execution.ToDriverVMState(vmState), execution.ToDriverProxyState(proxyState))
	r.recorder.RecordSandboxCreate(ctx, runtimeDriverName(session, vmState.Driver), time.Since(startedAt), err)
	endSpan(err)
	if err != nil {
		return domain.SandboxVMInfo{}, err
	}
	domainInfo := execution.FromDriverSandboxVMInfo(info)
	domainInfo.EgressPolicy = egressPolicy
	return domainInfo, nil
}

// PreflightSandboxRuntime forwards the capability to the wrapped runtime and
// reports success for runtimes that do not implement it: a runtime with no way
// to judge its own usability must still be started rather than blocked. The
// driver receives the same driver-boundary projection EnsureSandbox gets, so a
// preflight cannot pass against a different view of the sandbox than the start
// it guards.
func (r driverRuntimeAdapter) PreflightSandboxRuntime(ctx context.Context, session *domain.Sandbox, vmState domain.VMState) error {
	preflight, ok := r.runtime.(interface {
		PreflightSandboxRuntime(context.Context, *driverpkg.Sandbox, driverpkg.VMState) error
	})
	if !ok {
		return nil
	}
	return preflight.PreflightSandboxRuntime(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState))
}

func (r driverRuntimeAdapter) StopSandbox(ctx context.Context, session *domain.Sandbox, vmState domain.VMState) (bool, error) {
	ctx, endSpan := startRuntimeSpan(ctx, r.recorder, telemetry.SpanSandboxStop, session, vmState.Driver)
	if r.executions != nil {
		r.executions.ensureBlocked(session.Summary.ID)
		// Preserve the runtime driver's complete stop deadline. The sandbox stop
		// is the final containment boundary for executions that have not yet
		// returned after cancellation.
		r.executions.cancel(session.Summary.ID)
	}
	missing, err := r.runtime.StopSandbox(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState))
	endSpan(err)
	return missing, err
}

func (r driverRuntimeAdapter) RemoveSandbox(ctx context.Context, session *domain.Sandbox, vmState domain.VMState) error {
	ctx, endSpan := startRuntimeSpan(ctx, r.recorder, telemetry.SpanSandboxRemove, session, vmState.Driver)
	err := r.runtime.RemoveSandbox(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState))
	endSpan(err)
	return err
}

func (r driverRuntimeAdapter) Exec(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, spec domain.ExecSpec) (domain.ExecResult, error) {
	ctx, endSpan := startRuntimeSpan(ctx, r.recorder, telemetry.SpanSandboxExec, session, vmState.Driver)
	execCtx, marked, finish, err := r.beginExecution(ctx, session, spec)
	if err != nil {
		endSpan(err)
		return domain.ExecResult{}, err
	}
	defer finish()
	result, err := r.runtime.Exec(execCtx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), execution.ToDriverExecSpec(marked))
	reportMeasuredLowerLayerIsolationFacts(session.Summary.ID, vmState.Driver, result.SecurityFacts)
	endSpan(err)
	return execution.FromDriverExecResult(result), classifyExecTerminationError(err)
}

func (r driverRuntimeAdapter) ExecStream(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, spec domain.ExecSpec, stream domain.ExecStreamWriter) (domain.ExecResult, error) {
	ctx, endSpan := startRuntimeSpan(ctx, r.recorder, telemetry.SpanSandboxExec, session, vmState.Driver)
	execCtx, marked, finish, err := r.beginExecution(ctx, session, spec)
	if err != nil {
		endSpan(err)
		return domain.ExecResult{}, err
	}
	defer finish()
	driverStream := func(chunk driverpkg.ExecChunk) {
		if stream != nil {
			stream(domain.ExecChunk{Text: chunk.Text, Stream: domainStreamFromDriver(chunk.Stream)})
		}
	}
	result, err := r.runtime.ExecStream(execCtx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), execution.ToDriverExecSpec(marked), driverStream)
	reportMeasuredLowerLayerIsolationFacts(session.Summary.ID, vmState.Driver, result.SecurityFacts)
	endSpan(err)
	return execution.FromDriverExecResult(result), classifyExecTerminationError(err)
}

// TerminateProcessTree forwards a marker-based process-tree termination to the
// wrapped driver when it supports it, converting the domain sandbox/VM state to
// the driver boundary. It returns nil for drivers without the capability so the
// detached-execution fallback stays a best effort.
func (r driverRuntimeAdapter) TerminateProcessTree(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, marker string) error {
	terminator, ok := r.runtime.(driverpkg.ProcessTreeTerminator)
	if !ok {
		return nil
	}
	return terminator.TerminateProcessTree(ctx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), marker)
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
	execCtx, endSpan := startRuntimeSpan(execCtx, r.recorder, telemetry.SpanSandboxInteraction, session, vmState.Driver)
	interaction, err := interactor.OpenInteraction(execCtx, execution.ToDriverSandbox(session), execution.ToDriverVMState(vmState), marked)
	endSpan(err)
	if err != nil {
		finish()
		return nil, err
	}
	tracked := &trackedRuntimeInteraction{
		RuntimeInteraction: interaction,
		finish:             finish,
		done:               make(chan struct{}),
		sandboxID:          session.Summary.ID,
		driverName:         vmState.Driver,
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
	driverSandbox, _, err := r.driverSandbox(ctx, session)
	if err != nil {
		return nil, err
	}
	return reader.ReadGuestFile(ctx, driverSandbox, execution.ToDriverVMState(vmState), guestPath)
}

func (r guestFileRuntimeAdapter) ReadGuestDir(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, guestDir, hostDestDir string) error {
	reader, ok := r.runtime.(interface {
		ReadGuestDir(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string, string) error
	})
	if !ok {
		return domain.ClassifyError(domain.ErrUnsupported, "runtime does not support reading guest directories directly", nil)
	}
	driverSandbox, _, err := r.driverSandbox(ctx, session)
	if err != nil {
		return err
	}
	return reader.ReadGuestDir(ctx, driverSandbox, execution.ToDriverVMState(vmState), guestDir, hostDestDir)
}

func (r guestFileRuntimeAdapter) WriteGuestFile(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, guestPath string, content []byte) error {
	writer, ok := r.runtime.(interface {
		WriteGuestFile(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string, []byte) error
	})
	if !ok {
		return domain.ClassifyError(domain.ErrUnsupported, "runtime does not support writing guest files directly", nil)
	}
	driverSandbox, _, err := r.driverSandbox(ctx, session)
	if err != nil {
		return err
	}
	return writer.WriteGuestFile(ctx, driverSandbox, execution.ToDriverVMState(vmState), guestPath, content)
}

func (r guestFileRuntimeAdapter) WriteGuestDir(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, hostSrcDir, guestDir string) error {
	writer, ok := r.runtime.(interface {
		WriteGuestDir(context.Context, *driverpkg.Sandbox, driverpkg.VMState, string, string) error
	})
	if !ok {
		return domain.ClassifyError(domain.ErrUnsupported, "runtime does not support writing guest directories directly", nil)
	}
	driverSandbox, _, err := r.driverSandbox(ctx, session)
	if err != nil {
		return err
	}
	return writer.WriteGuestDir(ctx, driverSandbox, execution.ToDriverVMState(vmState), hostSrcDir, guestDir)
}
