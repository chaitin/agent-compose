package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	executorpkg "agent-compose/pkg/executor"
	"context"
	"path/filepath"
	"strings"
	"time"

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

type Executor struct {
	config    *appconfig.Config
	store     *Store
	configDB  *ConfigStore
	runtimes  RuntimeProvider
	streams   *SessionStreamBroker
	component *executorpkg.Executor
}

func NewExecutor(di do.Injector) (*Executor, error) {
	return &Executor{
		config:   do.MustInvoke[*appconfig.Config](di),
		store:    do.MustInvoke[*Store](di),
		configDB: do.MustInvoke[*ConfigStore](di),
		runtimes: do.MustInvoke[RuntimeProvider](di),
		streams:  do.MustInvoke[*SessionStreamBroker](di),
	}, nil
}

func newExecutorLLMFacadeEnvPreparer(do.Injector) (executorpkg.LLMFacadeEnvPreparer, error) {
	return executorLLMFacadeEnvPreparer, nil
}

func newExecutorStreamPublisher(di do.Injector) (executorpkg.StreamPublisher, error) {
	return do.MustInvoke[*SessionStreamBroker](di).componentBroker(), nil
}

func (e *Executor) componentExecutor() *executorpkg.Executor {
	if e == nil {
		return nil
	}
	if e.component == nil {
		e.component = executorpkg.New(e.config, e.store, e.configDB, e.runtimes, e.streams, executorLLMFacadeEnvPreparer)
	}
	return e.component
}

func executorLLMFacadeEnvPreparer(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, agent, model, source, runID string) (map[string]string, error) {
	return ensureSessionLLMFacadeConfig(ctx, config, configDB, session, agent, model, source, runID)
}

func (e *Executor) ExecuteCell(ctx context.Context, session *Session, cellType, source string) (NotebookCell, error) {
	return e.componentExecutor().ExecuteCell(ctx, session, cellType, source)
}

func (e *Executor) ExecuteCellStream(ctx context.Context, session *Session, cellType, source string, stream CellExecutionStream) (NotebookCell, error) {
	return e.componentExecutor().ExecuteCellStream(ctx, session, cellType, source, stream)
}

func (e *Executor) ExecuteAgent(ctx context.Context, session *Session, agent, message string) (NotebookCell, SessionEvent, SessionEvent, error) {
	return e.componentExecutor().ExecuteAgent(ctx, session, agent, message)
}

func (e *Executor) ExecuteAgentStream(ctx context.Context, session *Session, agent, message string, stream AgentExecutionStream) (NotebookCell, SessionEvent, SessionEvent, error) {
	return e.componentExecutor().ExecuteAgentStream(ctx, session, agent, message, stream)
}

func (e *Executor) ExecuteAgentWithTimeout(ctx context.Context, session *Session, agent, message string, timeout time.Duration) (NotebookCell, SessionEvent, SessionEvent, error) {
	return e.componentExecutor().ExecuteAgentWithTimeout(ctx, session, agent, message, timeout)
}

func (e *Executor) ExecuteAgentRequest(ctx context.Context, session *Session, request ExecuteAgentRequest) (NotebookCell, SessionEvent, SessionEvent, error) {
	return e.componentExecutor().ExecuteAgentRequest(ctx, session, request)
}

func (e *Executor) ExecuteLoaderCommand(ctx context.Context, session *Session, request LoaderCommandRequest) (LoaderCommandResult, error) {
	return e.componentExecutor().ExecuteLoaderCommand(ctx, session, request)
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
