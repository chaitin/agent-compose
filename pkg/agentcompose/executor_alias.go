package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	executorpkg "agent-compose/pkg/executor"
	sessionspkg "agent-compose/pkg/sessions"
	"context"
	"path/filepath"
	"strings"

	"github.com/samber/do/v2"
)

type CellExecutionStream = executorpkg.CellExecutionStream
type AgentExecutionStream = executorpkg.AgentExecutionStream
type ExecuteAgentRequest = executorpkg.ExecuteAgentRequest

const (
	CellTypeShell      = executorpkg.CellTypeShell
	CellTypeJavaScript = executorpkg.CellTypeJavaScript
	CellTypePython     = executorpkg.CellTypePython
	CellTypeAgent      = executorpkg.CellTypeAgent
)

func newExecutorLLMFacadeEnvPreparer(do.Injector) (executorpkg.LLMFacadeEnvPreparer, error) {
	return executorLLMFacadeEnvPreparer, nil
}

func newExecutorStreamPublisher(di do.Injector) (executorpkg.StreamPublisher, error) {
	return do.MustInvoke[*sessionspkg.SessionStreamBroker](di), nil
}

func executorLLMFacadeEnvPreparer(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, agent, model, source, runID string) (map[string]string, error) {
	return ensureSessionLLMFacadeConfig(ctx, config, configDB, session, agent, model, source, runID)
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

func hostSessionDir(session *Session) string {
	return filepath.Dir(session.Summary.WorkspacePath)
}
