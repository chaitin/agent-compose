package runtimes

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"time"

	"github.com/samber/do/v2"

	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
)

type Driver interface {
	StartSessionVM(context.Context, *Session) error
	StopSessionVM(context.Context, *Session) error
}

type SessionDriver struct {
	config             *appconfig.Config
	store              *storage.Store
	configDB           *storage.ConfigStore
	runtimes           RuntimeProvider
	runtimeEnvPreparer SessionRuntimeEnvPreparer
}

type SessionRuntimeEnvPreparer func(context.Context, *model.Session) ([]model.SessionEnvVar, error)

func NewSessionDriver(config *appconfig.Config, store *storage.Store, configDB *storage.ConfigStore, runtimes RuntimeProvider, preparer SessionRuntimeEnvPreparer) *SessionDriver {
	return &SessionDriver{
		config:             config,
		store:              store,
		configDB:           configDB,
		runtimes:           runtimes,
		runtimeEnvPreparer: preparer,
	}
}

func NewDriver(di do.Injector) (Driver, error) {
	preparer, _ := do.Invoke[SessionRuntimeEnvPreparer](di)
	return NewSessionDriver(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*storage.Store](di),
		do.MustInvoke[*storage.ConfigStore](di),
		do.MustInvoke[RuntimeProvider](di),
		preparer,
	), nil
}

func (d *SessionDriver) StartSessionVM(ctx context.Context, session *Session) error {
	ctx, cancel := context.WithTimeout(ctx, d.config.SessionStartTimeout)
	defer cancel()

	driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, d.config.RuntimeDriver)
	if err != nil {
		return err
	}
	runtime, err := d.runtimes.ForDriver(driver)
	if err != nil {
		return err
	}

	vmState, err := d.store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	proxyState, err := d.store.GetProxyState(session.Summary.ID)
	if err != nil {
		return err
	}
	vmState.Driver = driver
	vmState.Mode = driver
	vmState.BoxName = firstNonEmpty(vmState.BoxName, session.Summary.RuntimeRef)
	vmState.RuntimeHome = firstNonEmpty(vmState.RuntimeHome, driverpkg.RuntimeHomeForDriver(d.config, driver))
	if err := d.prepareSessionStart(ctx, driver, session, &vmState); err != nil {
		vmState.LastError = err.Error()
		_ = d.store.SaveVMState(session.Summary.ID, vmState)
		return err
	}

	info, err := runtime.EnsureSession(ctx, session, vmState, proxyState)
	if err != nil {
		vmState.LastError = err.Error()
		vmState.StoppedAt = time.Time{}
		_ = d.store.SaveVMState(session.Summary.ID, vmState)
		return err
	}

	return d.saveSessionStartInfo(session, vmState, proxyState, info)
}

func (d *SessionDriver) saveSessionStartInfo(session *Session, vmState VMState, proxyState ProxyState, info SessionVMInfo) error {
	vmState.BoxID = firstNonEmpty(info.BoxID, vmState.BoxID)
	vmState.StartedAt = time.Now().UTC()
	vmState.StoppedAt = time.Time{}
	vmState.LastError = ""
	vmState.BootstrapRef = firstNonEmpty(info.JupyterURL, vmState.BootstrapRef)
	if err := d.store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return err
	}
	if info.ProxyState != nil {
		proxyState = *info.ProxyState
	}
	proxyState.JupyterURL = firstNonEmpty(info.JupyterURL, proxyState.JupyterURL)
	return d.store.SaveProxyState(session.Summary.ID, proxyState)
}

func (d *SessionDriver) StopSessionVM(ctx context.Context, session *Session) error {
	ctx, cancel := context.WithTimeout(ctx, d.config.SessionStopTimeout)
	defer cancel()

	driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, d.config.RuntimeDriver)
	if err != nil {
		return err
	}
	runtime, err := d.runtimes.ForDriver(driver)
	if err != nil {
		return err
	}

	vmState, err := d.store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	missing, err := runtime.StopSession(ctx, session, vmState)
	if err != nil {
		vmState.LastError = err.Error()
		_ = d.store.SaveVMState(session.Summary.ID, vmState)
		return err
	}

	vmState.StoppedAt = time.Now().UTC()
	vmState.LastError = ""
	if missing {
		vmState.BoxID = ""
	}
	if d.configDB != nil {
		if err := d.configDB.RevokeLLMFacadeTokensForSession(ctx, session.Summary.ID); err != nil {
			return err
		}
	}
	return d.store.SaveVMState(session.Summary.ID, vmState)
}

func (d *SessionDriver) prepareSessionStart(ctx context.Context, driver string, session *Session, vmState *VMState) error {
	prepared, err := driverpkg.PrepareSessionStart(ctx, d.config, driver, ToDriverSession(session), ToDriverVMState(*vmState))
	if err != nil {
		return err
	}
	if d.runtimeEnvPreparer != nil {
		managedEnv, err := d.runtimeEnvPreparer(ctx, session)
		if err != nil {
			return err
		}
		if len(managedEnv) > 0 {
			session.RuntimeEnvItems = managedEnv
		}
	}
	*vmState = FromDriverVMState(prepared)
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
