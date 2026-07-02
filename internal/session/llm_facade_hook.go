package session

import (
	appconfig "agent-compose/pkg/config"
	"context"
)

type sessionLLMFacadeConfigurer interface {
	EnsureSessionLLMFacadeConfig(ctx context.Context, config *appconfig.Config, configDB ConfigStore, session *Session, agent, model, source, runID string) (map[string]string, error)
}

var defaultSessionLLMFacadeConfigurer sessionLLMFacadeConfigurer = noopSessionLLMFacadeConfigurer{}

type noopSessionLLMFacadeConfigurer struct{}

func (noopSessionLLMFacadeConfigurer) EnsureSessionLLMFacadeConfig(context.Context, *appconfig.Config, ConfigStore, *Session, string, string, string, string) (map[string]string, error) {
	return nil, nil
}

func ensureSessionLLMFacadeConfig(ctx context.Context, config *appconfig.Config, configDB ConfigStore, session *Session, agent, model, source, runID string) (map[string]string, error) {
	if defaultSessionLLMFacadeConfigurer == nil {
		return nil, nil
	}
	return defaultSessionLLMFacadeConfigurer.EnsureSessionLLMFacadeConfig(ctx, config, configDB, session, agent, model, source, runID)
}
