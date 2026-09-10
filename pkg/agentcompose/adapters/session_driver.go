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
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

type SandboxDriver struct {
	Config   *appconfig.Config
	Store    *sandboxstore.Store
	ConfigDB *configstore.ConfigStore
	Runtimes RuntimeProvider
}

func NewSandboxDriver(config *appconfig.Config, store *sandboxstore.Store, configDB *configstore.ConfigStore, runtimes RuntimeProvider) *SandboxDriver {
	return &SandboxDriver{Config: config, Store: store, ConfigDB: configDB, Runtimes: runtimes}
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

	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
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
	info, err := runtime.EnsureSandbox(ctx, session, runtimeVMState, proxyState)
	if err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}
	runtimeStarted = true

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

func (d *SandboxDriver) prepareSandboxStart(ctx context.Context, driver string, session *domain.Sandbox, vmState *domain.VMState) error {
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
