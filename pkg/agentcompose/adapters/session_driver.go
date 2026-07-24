package adapters

import (
	"context"
	"errors"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
)

type SandboxDriver struct {
	Config   *appconfig.Config
	Store    *sessionstore.Store
	ConfigDB *configstore.ConfigStore
	Runtimes RuntimeProvider
}

func NewSandboxDriver(config *appconfig.Config, store *sessionstore.Store, configDB *configstore.ConfigStore, runtimes RuntimeProvider) *SandboxDriver {
	return &SandboxDriver{Config: config, Store: store, ConfigDB: configDB, Runtimes: runtimes}
}

func (d *SandboxDriver) runtimeForSession(session *domain.Sandbox) (string, SandboxRuntime, error) {
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(session.Summary.Driver, d.Config.RuntimeDriver)
	if err != nil {
		return "", nil, err
	}
	runtime, err := d.Runtimes.ForDriver(driver)
	if err != nil {
		return "", nil, err
	}
	return driver, runtime, nil
}

func (d *SandboxDriver) ValidateSandboxRuntime(session *domain.Sandbox) error {
	_, _, err := d.runtimeForSession(session)
	return err
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
	if vmState.StartedAt.IsZero() && d.ConfigDB != nil {
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

	info, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}
	runtimeStarted = true

	return d.saveSandboxStartInfo(session, vmState, proxyState, info)
}

func (d *SandboxDriver) saveSandboxStartInfo(session *domain.Sandbox, vmState domain.VMState, proxyState domain.ProxyState, info domain.SandboxVMInfo) error {
	vmState, proxyState = sessions.ApplySessionStartInfo(vmState, proxyState, info, time.Now())
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
	if err := runtime.RemoveSandbox(ctx, session, vmState); err != nil {
		vmState.LastError = err.Error()
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}
	if d.ConfigDB != nil {
		if err := d.ConfigDB.RevokeLLMFacadeTokensForSandbox(ctx, session.Summary.ID); err != nil {
			vmState.LastError = err.Error()
			_ = d.Store.SaveVMState(session.Summary.ID, vmState)
			return err
		}
	}
	return nil
}

func (d *SandboxDriver) prepareSandboxStart(ctx context.Context, driver string, session *domain.Sandbox, vmState *domain.VMState) error {
	prepared, err := driverpkg.PrepareSandboxStart(ctx, d.Config, driver, execution.ToDriverSandbox(session), execution.ToDriverVMState(*vmState))
	if err != nil {
		return err
	}
	*vmState = execution.FromDriverVMState(prepared)
	return nil
}
