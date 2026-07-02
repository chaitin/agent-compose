package agentcompose

import (
	sessiondomain "agent-compose/internal/agentcompose/session"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"time"

	"github.com/samber/do/v2"
)

type Driver interface {
	StartSessionVM(context.Context, *Session) error
	StopSessionVM(context.Context, *Session) error
}

type SessionDriver struct {
	config   *appconfig.Config
	store    *Store
	configDB *ConfigStore
	runtimes RuntimeProvider
}

func NewDriver(di do.Injector) (Driver, error) {
	return &SessionDriver{
		config:   do.MustInvoke[*appconfig.Config](di),
		store:    do.MustInvoke[*Store](di),
		configDB: do.MustInvoke[*ConfigStore](di),
		runtimes: do.MustInvoke[RuntimeProvider](di),
	}, nil
}

func (d *SessionDriver) StartSessionVM(ctx context.Context, session *Session) error {
	return d.domainDriver().StartSessionVM(ctx, session)
}

func (d *SessionDriver) saveSessionStartInfo(session *Session, vmState VMState, proxyState ProxyState, info SessionVMInfo) error {
	return sessiondomain.SaveSessionStartInfo(d.store, session, vmState, proxyState, info, time.Now().UTC())
}

func (d *SessionDriver) StopSessionVM(ctx context.Context, session *Session) error {
	return d.domainDriver().StopSessionVM(ctx, session)
}

func (d *SessionDriver) prepareSessionStart(ctx context.Context, driver string, session *Session, vmState *VMState) error {
	prepared, err := driverpkg.PrepareSessionStart(ctx, d.config, driver, toDriverSession(session), toDriverVMState(*vmState))
	if err != nil {
		return err
	}
	managedEnv, err := ensureSessionLLMFacadeConfig(ctx, d.config, d.configDB, session, "codex", "", "session", "")
	if err != nil {
		return err
	}
	if len(managedEnv) > 0 {
		session.RuntimeEnvItems = envItemsFromMap(managedEnv, false)
	}
	*vmState = fromDriverVMState(prepared)
	return nil
}

func (d *SessionDriver) domainDriver() sessiondomain.VMDriver {
	var revoker sessiondomain.TokenRevoker
	if d.configDB != nil {
		revoker = d.configDB
	}
	return sessiondomain.VMDriver{
		Config:       d.config,
		Store:        d.store,
		Runtimes:     sessionRuntimeProvider{provider: d.runtimes},
		TokenRevoker: revoker,
		PrepareStart: d.prepareSessionStart,
	}
}

type sessionRuntimeProvider struct {
	provider RuntimeProvider
}

func (p sessionRuntimeProvider) ForDriver(driver string) (sessiondomain.Runtime, error) {
	return p.provider.ForDriver(driver)
}
