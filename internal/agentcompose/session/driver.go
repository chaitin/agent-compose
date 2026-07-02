package session

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"strings"
	"time"
)

type VMInfo struct {
	BoxID      string
	JupyterURL string
	ProxyState *ProxyState
}

type Runtime interface {
	EnsureSession(context.Context, *Session, VMState, ProxyState) (VMInfo, error)
	StopSession(context.Context, *Session, VMState) (bool, error)
}

type RuntimeProvider interface {
	ForDriver(string) (Runtime, error)
}

type DriverStore interface {
	GetVMState(string) (VMState, error)
	SaveVMState(string, VMState) error
	GetProxyState(string) (ProxyState, error)
	SaveProxyState(string, ProxyState) error
}

type TokenRevoker interface {
	RevokeLLMFacadeTokensForSession(context.Context, string) error
}

type StartPreparer func(context.Context, string, *Session, *VMState) error

type VMDriver struct {
	Config       *appconfig.Config
	Store        DriverStore
	Runtimes     RuntimeProvider
	TokenRevoker TokenRevoker
	PrepareStart StartPreparer
}

func (d VMDriver) StartSessionVM(ctx context.Context, session *Session) error {
	ctx, cancel := context.WithTimeout(ctx, d.Config.SessionStartTimeout)
	defer cancel()

	driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, d.Config.RuntimeDriver)
	if err != nil {
		return err
	}
	runtime, err := d.Runtimes.ForDriver(driver)
	if err != nil {
		return err
	}

	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	proxyState, err := d.Store.GetProxyState(session.Summary.ID)
	if err != nil {
		return err
	}
	vmState.Driver = driver
	vmState.Mode = driver
	vmState.BoxName = FirstNonEmpty(vmState.BoxName, session.Summary.RuntimeRef)
	vmState.RuntimeHome = FirstNonEmpty(vmState.RuntimeHome, driverpkg.RuntimeHomeForDriver(d.Config, driver))
	if d.PrepareStart != nil {
		if err := d.PrepareStart(ctx, driver, session, &vmState); err != nil {
			vmState.LastError = err.Error()
			_ = d.Store.SaveVMState(session.Summary.ID, vmState)
			return err
		}
	}

	info, err := runtime.EnsureSession(ctx, session, vmState, proxyState)
	if err != nil {
		vmState.LastError = err.Error()
		vmState.StoppedAt = time.Time{}
		_ = d.Store.SaveVMState(session.Summary.ID, vmState)
		return err
	}

	return SaveSessionStartInfo(d.Store, session, vmState, proxyState, info, time.Now().UTC())
}

func SaveSessionStartInfo(store DriverStore, session *Session, vmState VMState, proxyState ProxyState, info VMInfo, now time.Time) error {
	vmState.BoxID = FirstNonEmpty(info.BoxID, vmState.BoxID)
	vmState.StartedAt = now.UTC()
	vmState.StoppedAt = time.Time{}
	vmState.LastError = ""
	vmState.BootstrapRef = FirstNonEmpty(info.JupyterURL, vmState.BootstrapRef)
	if err := store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return err
	}
	if info.ProxyState != nil {
		proxyState = *info.ProxyState
	}
	proxyState.JupyterURL = FirstNonEmpty(info.JupyterURL, proxyState.JupyterURL)
	return store.SaveProxyState(session.Summary.ID, proxyState)
}

func (d VMDriver) StopSessionVM(ctx context.Context, session *Session) error {
	ctx, cancel := context.WithTimeout(ctx, d.Config.SessionStopTimeout)
	defer cancel()

	driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, d.Config.RuntimeDriver)
	if err != nil {
		return err
	}
	runtime, err := d.Runtimes.ForDriver(driver)
	if err != nil {
		return err
	}

	vmState, err := d.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return err
	}
	missing, err := runtime.StopSession(ctx, session, vmState)
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
	if d.TokenRevoker != nil {
		if err := d.TokenRevoker.RevokeLLMFacadeTokensForSession(ctx, session.Summary.ID); err != nil {
			return err
		}
	}
	return d.Store.SaveVMState(session.Summary.ID, vmState)
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
