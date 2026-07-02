package agentcompose

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/executor"
	"agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage"
)

func newExecutorLLMFacadeEnvPreparer(do.Injector) (executor.LLMFacadeEnvPreparer, error) {
	return executorLLMFacadeEnvPreparer, nil
}

func newExecutorStreamPublisher(di do.Injector) (executor.StreamPublisher, error) {
	return do.MustInvoke[*sessions.SessionStreamBroker](di), nil
}

func executorLLMFacadeEnvPreparer(ctx context.Context, config *appconfig.Config, configDB *storage.ConfigStore, session *model.Session, agent, modelName, source, runID string) (map[string]string, error) {
	return ensureSessionLLMFacadeConfig(ctx, config, configDB, session, agent, modelName, source, runID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func hostSessionDir(session *model.Session) string {
	return filepath.Dir(session.Summary.WorkspacePath)
}
