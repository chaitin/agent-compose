package agentcompose

import (
	"context"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/runtimes"
)

type SessionVMInfo = runtimes.SessionVMInfo
type BoxRuntime = runtimes.BoxRuntime
type RuntimeProvider = runtimes.RuntimeProvider
type Driver = runtimes.Driver
type SessionDriver = runtimes.SessionDriver

func NewRuntimeProvider(di do.Injector) (RuntimeProvider, error) {
	return runtimes.NewRuntimeProvider(di)
}

func NewDriver(di do.Injector) (Driver, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	configDB := do.MustInvoke[*ConfigStore](di)
	return runtimes.NewSessionDriver(
		config,
		do.MustInvoke[*Store](di),
		configDB,
		do.MustInvoke[RuntimeProvider](di),
		func(ctx context.Context, session *Session) ([]SessionEnvVar, error) {
			return sessionRuntimeEnvPreparer(ctx, config, configDB, session)
		},
	), nil
}

func sessionRuntimeEnvPreparer(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session) ([]SessionEnvVar, error) {
	managedEnv, err := ensureSessionLLMFacadeConfig(ctx, config, configDB, session, "codex", "", "session", "")
	if err != nil {
		return nil, err
	}
	return envItemsFromMap(managedEnv, false), nil
}
