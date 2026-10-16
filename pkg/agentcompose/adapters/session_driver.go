package adapters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/chaitin/agent-compose/pkg/capmatrix"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/egress"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/telemetry"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

type SandboxDriver struct {
	Config   *appconfig.Config
	Store    *sandboxstore.Store
	ConfigDB *configstore.ConfigStore
	Runtimes RuntimeProvider
	// Capabilities is the frozen engine capability snapshot. StartSandboxVM
	// evaluates every declared isolation requirement against it before any
	// runtime state is created; nothing else may bypass that gate.
	Capabilities capmatrix.Snapshot
	recorder     *telemetry.Recorder
}

// SandboxDriverDeps is the required collaborator set for a SandboxDriver.
// Bundling it keeps the constructor within the repository's argument limit
// without turning the capability snapshot into an optional collaborator:
// StartSandboxVM gates on that snapshot before creating runtime state, so every
// caller must pass the snapshot it collected instead of defaulting to none.
type SandboxDriverDeps struct {
	Config       *appconfig.Config
	Store        *sandboxstore.Store
	ConfigDB     *configstore.ConfigStore
	Runtimes     RuntimeProvider
	Capabilities capmatrix.Snapshot
}

// SandboxDriverOption customizes the driver's optional collaborators.
type SandboxDriverOption func(*SandboxDriver)

// WithSandboxDriverRecorder attaches the daemon recorder, so guest-image
// resolution and pull export as a child span of the run that requested them. A
// nil recorder leaves the driver untraced.
func WithSandboxDriverRecorder(recorder *telemetry.Recorder) SandboxDriverOption {
	return func(driver *SandboxDriver) { driver.recorder = recorder }
}

func NewSandboxDriver(deps SandboxDriverDeps, opts ...SandboxDriverOption) *SandboxDriver {
	driver := &SandboxDriver{
		Config:       deps.Config,
		Store:        deps.Store,
		ConfigDB:     deps.ConfigDB,
		Runtimes:     deps.Runtimes,
		Capabilities: deps.Capabilities,
	}
	for _, option := range opts {
		option(driver)
	}
	return driver
}

func (d *SandboxDriver) runtimeForSession(session *domain.Sandbox) (string, SandboxRuntime, error) {
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(session.Summary.Driver, d.Config.RuntimeDriver)
	if err != nil {
		return "", nil, err
	}
	if !driverpkg.RuntimeDriverSupportsStoppedRuntimeRetention(driver) && sandboxes.EffectiveStoppedRuntimePolicy(session) == domain.StoppedRuntimePolicyRetain {
		return "", nil, domain.ClassifyError(
			domain.ErrFailedPrecondition,
			fmt.Sprintf("runtime driver %q does not support stopped_runtime_policy=%q; use stopped_runtime_policy=%q and sandbox_policy=sticky when the Pod must stay running", driver, domain.StoppedRuntimePolicyRetain, domain.StoppedRuntimePolicyRemove),
			nil,
		)
	}
	runtime, err := d.Runtimes.ForDriver(driver)
	if err != nil {
		return "", nil, err
	}
	return driver, runtime, nil
}

func (d *SandboxDriver) ValidateSandboxRuntime(session *domain.Sandbox) error {
	driver, _, err := d.runtimeForSession(session)
	if err != nil {
		return err
	}
	return workspaces.ValidateWorkspaceRuntimeDriver(session.Workspace, driver)
}

func (d *SandboxDriver) StartSandboxVM(ctx context.Context, session *domain.Sandbox) (resultErr error) {
	if session == nil || session.Summary.VMStatus == domain.VMStatusDeleting {
		return domain.ClassifyError(domain.ErrConflict, "sandbox is being deleted", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, d.Config.SandboxStartTimeout)
	defer cancel()

	driver, runtime, err := d.runtimeForSession(session)
	if err != nil {
		return err
	}
	if err := d.enforceIsolationPreflight(driver, session); err != nil {
		return err
	}

	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	// The driver-boundary VMState is an explicit projection, so preparing the
	// start replaces vmState with a value that does not carry the egress record.
	// It is a fact about the runtime, not about the driver's view of it, so it is
	// held here and written back below.
	previousEgressPolicy := vmState.EgressPolicy
	runtimeStarted := false
	freshRuntime := vmState.StartedAt.IsZero() || sandboxes.RuntimeReleaseIntentional(session)
	if freshRuntime && d.ConfigDB != nil {
		defer func() {
			if resultErr == nil || runtimeStarted {
				return
			}
			if err := d.ConfigDB.RevokeLLMFacadeTokensForSandbox(context.WithoutCancel(ctx), session.Summary.ID); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}()
	}
	proxyState, err := d.Store.GetProxyState(session.Summary.ID)
	if err != nil {
		return err
	}
	vmState.Driver = driver
	vmState.Mode = driver
	vmState.BoxName = firstNonEmpty(vmState.BoxName, session.Summary.RuntimeRef)
	vmState.RuntimeHome = firstNonEmpty(vmState.RuntimeHome, driverpkg.RuntimeHomeForDriver(d.Config, driver))
	if err := d.prepareSandboxStart(ctx, driver, session, &vmState); err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}
	// prepareSandboxStart replaces vmState with the driver-boundary projection,
	// which does not carry the egress record. Put it back before the save below:
	// that start-attempt fence and the EnsureSandbox failure path both persist
	// this value, and a start that never reaches recordEgressRuntimePolicy must
	// not erase the fact about the runtime that is still running.
	vmState.EgressPolicy = previousEgressPolicy
	// Persist a start-attempt fence before asking the runtime to start. Runtime
	// creation can partially succeed before returning an error; retaining the
	// stop timestamp preserves resume semantics, while the newer attempt time
	// prevents destructive cleanup until another stop is confirmed.
	vmState.StartAttemptedAt = time.Now().UTC()
	if err := d.Store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return err
	}

	if sandboxes.EffectiveStoppedRuntimeState(session) == domain.StoppedRuntimeStateReleasePending {
		if _, err := d.releaseSandboxRuntime(ctx, session, runtime, vmState); err != nil {
			return err
		}
		vmState, err = d.Store.GetVMState(session.Summary.ID)
		if err != nil {
			return err
		}
	}
	runtimeVMState := vmState
	if sandboxes.RuntimeReleaseIntentional(session) {
		runtimeVMState.StoppedAt = time.Time{}
	}
	// A runtime that can tell it is unusable is asked before any runtime state is
	// created, so the sandbox fails with the reason the runtime gave instead of
	// leaving a partially created runtime that cleanup then has to chase.
	if preflight, ok := runtime.(SandboxRuntimePreflight); ok {
		if err := preflight.PreflightSandboxRuntime(ctx, session, runtimeVMState); err != nil {
			vmState.LastError = err.Error()
			_ = d.Store.SaveVMState(session.Summary.ID, vmState)
			return err
		}
	}
	info, err := runtime.EnsureSandbox(ctx, session, runtimeVMState, proxyState)
	if err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}
	runtimeStarted = true
	// Two facts say the policy resolved now is the one the running runtime has:
	// the driver reporting that it built the runtime in this call, and this
	// start having had no runtime to reuse at all (the sandbox never started, or
	// its runtime was released). Drivers that cannot report creation are still
	// covered for their first runtime, and every later start of a runtime with
	// no record is reported instead of assumed.
	runtimeHasCurrentPolicy := freshRuntime || info.RuntimeCreated
	if err := d.recordEgressRuntimePolicy(session, &vmState, previousEgressPolicy, info.EgressPolicy, runtimeHasCurrentPolicy); err != nil {
		return err
	}

	if err := d.saveSandboxStartInfo(session, vmState, proxyState, info); err != nil {
		return err
	}
	if !freshRuntime {
		return nil
	}
	if err := sandboxes.MarkSandboxRuntimeOwned(d.Config.SandboxRoot, session); err != nil {
		removed, rollbackErr := d.rollbackStartedSandboxRuntime(ctx, driver, runtime, session)
		if removed {
			runtimeStarted = false
		}
		if rollbackErr != nil {
			rollbackErr = fmt.Errorf("rollback unowned sandbox runtime: %w", rollbackErr)
		}
		return errors.Join(fmt.Errorf("mark sandbox runtime owned: %w", err), rollbackErr)
	}
	return nil
}

// recordEgressRuntimePolicy keeps the sandbox's record of the egress policy its
// runtime was created under, and reports when the policy now in force is not the
// one that created the runtime that is running.
//
// The record describes the *runtime*, so it is replaced only when the start
// establishes that the running runtime was built under the policy resolved now,
// which is what runtimeHasCurrentPolicy carries. On any other start the
// difference is reported instead of recorded away: a sandbox that started with
// an open declaration keeps its egress until its runtime is recreated, and
// hiding that would make the engine's account of it less true the longer it ran.
//
// The caller owns runtimeHasCurrentPolicy because only it knows both facts: the
// driver reports whether it built the runtime in this call
// (SandboxVMInfo.RuntimeCreated), and the start path knows whether there was a
// runtime to reuse at all.
func (d *SandboxDriver) recordEgressRuntimePolicy(session *domain.Sandbox, vmState *domain.VMState, previousRecord, current domain.SandboxEgressPolicy, runtimeHasCurrentPolicy bool) error {
	// The start path only *ensures* a runtime: a retained one is started again
	// with the configuration it was created with. Without a fact saying the
	// runtime was built now, the record keeps the policy the runtime has rather
	// than the one the engine would install, which is the direction that cannot
	// claim enforcement that is not there.
	vmState.EgressPolicy = previousRecord
	if !current.Recorded() {
		return nil
	}
	if runtimeHasCurrentPolicy {
		vmState.EgressPolicy = current
		return nil
	}
	previous := egressRuntimePolicyFromRecord(previousRecord)
	// A runtime carrying no record cannot be credited with the policy resolved
	// now: either it was created before the engine kept this record, or a driver
	// that does not report creation built it. Writing the current policy there
	// would claim an isolation nobody installed, so the record stays empty and
	// the report below is what states that this runtime's egress cannot be
	// accounted for.
	currentPolicy := egressRuntimePolicyFromRecord(current)
	if previous.Application == egress.ApplicationMediated && currentPolicy.Application == egress.ApplicationMediated {
		// A mediated runtime is decided by the declaration in force on every
		// request, so the generation it is decided by is the current one.
		vmState.EgressPolicy = current
	}
	drift, changed := egress.RuntimePolicyDriftBetween(previous, currentPolicy)
	if !changed {
		return nil
	}
	attributes := []any{
		"sandbox_id", session.Summary.ID,
		"driver", vmState.Driver,
		"previous_generation", uint64(drift.Previous),
		"current_generation", uint64(drift.Current),
		"previous_application", string(previous.Application),
		"current_application", string(currentPolicy.Application),
	}
	if drift.DecidedByCurrentPolicy {
		slog.Info("sandbox egress policy differs from the one its runtime was created with", attributes...)
		return nil
	}
	slog.Warn("sandbox egress is not decided by the policy now in force", append(attributes, "reason", drift.Summary)...)
	return nil
}

func (d *SandboxDriver) rollbackStartedSandboxRuntime(ctx context.Context, driver string, runtime SandboxRuntime, session *domain.Sandbox) (bool, error) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), driverpkg.SandboxStopContextTimeout(driver, d.Config.SandboxStopTimeout))
	defer cancel()
	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return false, err
	}
	if err := runtime.RemoveSandbox(rollbackCtx, session, vmState); err != nil {
		return false, err
	}
	// Removal succeeded, so no start fence from this unowned runtime should
	// survive and make a retry look like a retained-runtime resume.
	vmState.BoxID = ""
	vmState.StartedAt = time.Time{}
	vmState.StartAttemptedAt = time.Time{}
	vmState.StoppedAt = time.Time{}
	vmState.LastError = ""
	stateErr := d.Store.SaveVMState(session.Summary.ID, vmState)
	ownershipErr := sandboxes.MarkSandboxRuntimeReleased(d.Config.SandboxRoot, session)
	return true, errors.Join(stateErr, ownershipErr)
}

func (d *SandboxDriver) saveSandboxStartInfo(session *domain.Sandbox, vmState domain.VMState, proxyState domain.ProxyState, info domain.SandboxVMInfo) error {
	vmState, proxyState = sandboxes.ApplySessionStartInfo(vmState, proxyState, info, time.Now())
	if err := d.Store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return err
	}
	return d.Store.SaveProxyState(session.Summary.ID, proxyState)
}

func (d *SandboxDriver) StopSandboxVM(ctx context.Context, session *domain.Sandbox) error {
	driver, runtime, err := d.runtimeForSession(session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, driverpkg.SandboxStopContextTimeout(driver, d.Config.SandboxStopTimeout))
	defer cancel()

	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	missing, err := runtime.StopSandbox(ctx, session, vmState)
	if err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}

	vmState.StoppedAt = time.Now().UTC()
	vmState.LastError = ""
	if missing {
		vmState.BoxID = ""
	}
	return d.Store.SaveVMState(session.Summary.ID, vmState)
}

func (d *SandboxDriver) PrepareSandboxStop(ctx context.Context, session *domain.Sandbox, vmState domain.VMState, gracePeriod time.Duration) (sandboxes.StopPreparationResult, error) {
	_, runtime, err := d.runtimeForSession(session)
	if err != nil {
		return sandboxes.StopPreparationResult{}, err
	}
	preparer, ok := runtime.(SandboxGracefulStopRuntime)
	if !ok {
		return sandboxes.StopPreparationResult{}, domain.ClassifyError(domain.ErrUnsupported, "runtime does not support graceful sandbox stop", nil)
	}
	return preparer.PrepareSandboxStop(ctx, session, vmState, gracePeriod)
}

func (d *SandboxDriver) BeginSandboxForceStop(session *domain.Sandbox) error {
	_, runtime, err := d.runtimeForSession(session)
	if err != nil {
		return err
	}
	if initiator, ok := runtime.(interface{ BeginSandboxForceStop(*domain.Sandbox) }); ok {
		initiator.BeginSandboxForceStop(session)
	}
	return nil
}

func (d *SandboxDriver) FinishSandboxStop(session *domain.Sandbox) {
	_, runtime, err := d.runtimeForSession(session)
	if err != nil {
		return
	}
	if finalizer, ok := runtime.(interface{ FinishSandboxStop(*domain.Sandbox) }); ok {
		finalizer.FinishSandboxStop(session)
	}
}

func (d *SandboxDriver) RemoveSandboxVM(ctx context.Context, session *domain.Sandbox) error {
	driver, runtime, err := d.runtimeForSession(session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, driverpkg.SandboxStopContextTimeout(driver, d.Config.SandboxStopTimeout))
	defer cancel()
	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	vmState, err = d.releaseSandboxRuntime(ctx, session, runtime, vmState)
	if err != nil {
		return err
	}
	return d.revokeReleasedRuntimeTokens(ctx, session.Summary.ID, vmState)
}

func (d *SandboxDriver) ReleaseSandboxRuntime(ctx context.Context, session *domain.Sandbox) error {
	driver, runtime, err := d.runtimeForSession(session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, driverpkg.SandboxStopContextTimeout(driver, d.Config.SandboxStopTimeout))
	defer cancel()
	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	vmState, err = d.releaseSandboxRuntime(ctx, session, runtime, vmState)
	if err != nil {
		return err
	}
	return d.revokeReleasedRuntimeTokens(ctx, session.Summary.ID, vmState)
}

func (d *SandboxDriver) releaseSandboxRuntime(ctx context.Context, session *domain.Sandbox, runtime SandboxRuntime, vmState domain.VMState) (domain.VMState, error) {
	if err := runtime.RemoveSandbox(ctx, session, vmState); err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return vmState, err
	}
	vmState.BoxID = ""
	vmState.LastError = ""
	if err := d.Store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return vmState, err
	}
	return vmState, nil
}

func (d *SandboxDriver) revokeReleasedRuntimeTokens(ctx context.Context, sandboxID string, vmState domain.VMState) error {
	if d.ConfigDB == nil {
		return nil
	}
	if err := d.ConfigDB.RevokeLLMFacadeTokensForSandbox(ctx, sandboxID); err != nil {
		vmState.LastError = err.Error()
		if saveErr := d.Store.SaveVMState(sandboxID, vmState); saveErr != nil {
			return errors.Join(err, fmt.Errorf("persist facade token revocation failure: %w", saveErr))
		}
		return err
	}
	return nil
}

// prepareSandboxStart resolves the guest image and, for the Docker driver,
// pulls it when the pull policy requires it. VM drivers only resolve the ref
// here; their materialization stays under the sandbox.ensure span.
func (d *SandboxDriver) prepareSandboxStart(ctx context.Context, driver string, session *domain.Sandbox, vmState *domain.VMState) (err error) {
	ctx, endSpan := startRuntimeSpan(ctx, d.recorder, telemetry.SpanImagePull, session, driver)
	defer func() { endSpan(err) }()
	prepared, err := driverpkg.PrepareSandboxStart(ctx, d.Config, driverpkg.SandboxStartTarget{
		Driver:  driver,
		Session: execution.ToDriverSandbox(session),
		VMState: execution.ToDriverVMState(*vmState),
	})
	if err != nil {
		return err
	}
	*vmState = execution.FromDriverVMState(prepared)
	return nil
}

// enforceIsolationPreflight is the fail-closed gate on the sandbox start path.
// StartSandboxVM is the entry point that starts a sandbox and creates its
// runtime state, and this gate runs before any of that state is written, so the
// start path cannot create a runtime without a declared-requirement check.
//
// It is not the only caller of RuntimeProvider.EnsureSandbox: post-start paths
// such as the guest-skills projection re-enter EnsureSandbox on an
// already-started sandbox, where the idempotent ensure cannot create state that
// the gate has not already approved. Those revisits deliberately do not
// re-evaluate the declaration.
//
// D3 default: the current declaration surface yields an empty requirement set,
// so the gate returns immediately and the default path is byte-for-byte
// unchanged. It is not skipped; it is satisfied.
func (d *SandboxDriver) enforceIsolationPreflight(driver string, session *domain.Sandbox) error {
	requirements, err := capmatrix.ParseIsolationRequirements(session.IsolationRequirements)
	if err != nil {
		return domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("invalid isolation requirement: %v", err), err)
	}
	if len(requirements) == 0 {
		return nil
	}
	decision := d.Capabilities.EvaluateStartPreflight(driver, requirements, nil)
	for _, degradation := range decision.Degradations {
		slog.Warn("isolation requirement is not enforced",
			"sandbox_id", session.Summary.ID,
			"driver", driver,
			"dimension", string(degradation.Dimension),
			"reason", string(degradation.Reason),
			"detail", degradation.Detail,
		)
	}
	if decision.Allowed {
		return nil
	}
	return domain.ClassifyError(domain.ErrFailedPrecondition, decision.FailureReason(), nil)
}
