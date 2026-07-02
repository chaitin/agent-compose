package agentcompose

import (
	"context"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/model"
	"agent-compose/pkg/runtimes"
	"agent-compose/pkg/storage"
)

func newSessionRuntimeEnvPreparer(di do.Injector) (runtimes.SessionRuntimeEnvPreparer, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	configDB := do.MustInvoke[*storage.ConfigStore](di)
	return func(ctx context.Context, session *model.Session) ([]model.SessionEnvVar, error) {
		return sessionRuntimeEnvPreparer(ctx, config, configDB, session)
	}, nil
}

func sessionRuntimeEnvPreparer(ctx context.Context, config *appconfig.Config, configDB *storage.ConfigStore, session *model.Session) ([]model.SessionEnvVar, error) {
	managedEnv, err := ensureSessionLLMFacadeConfig(ctx, config, configDB, session, "codex", "", "session", "")
	if err != nil {
		return nil, err
	}
	return envItemsFromMap(managedEnv, false), nil
}
