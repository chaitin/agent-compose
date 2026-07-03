package agentcompose

import (
	"context"
	"fmt"

	"agent-compose/pkg/execution"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type loaderHostEvents struct {
	manager *LoaderManager
}

func (e loaderHostEvents) Add(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	return e.manager.addLoaderEvent(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
}

func (e loaderHostEvents) AddRecord(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) (domain.LoaderEvent, error) {
	return e.manager.addLoaderEventRecord(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
}

type loaderHostSessions struct {
	manager *LoaderManager
}

func (s loaderHostSessions) Ensure(ctx context.Context, loader domain.Loader, request domain.LoaderAgentRequest, titleOverridesSession bool) (*domain.Session, string, error) {
	return s.manager.sessionRunnerComponent().Ensure(ctx, loader, request, titleOverridesSession)
}

func (s loaderHostSessions) Load(ctx context.Context, sessionID string) (*domain.Session, error) {
	return s.manager.store.GetSession(ctx, sessionID)
}

func (s loaderHostSessions) Shutdown(ctx context.Context, sessionID string) error {
	return s.manager.shutdownLoaderSession(ctx, sessionID)
}

type loaderHostAgentDefinitions struct {
	manager *LoaderManager
}

func (r loaderHostAgentDefinitions) ResolveLoaderAgentDefinition(ctx context.Context, loader domain.Loader) (*domain.AgentDefinition, error) {
	return r.manager.loaderAgentDefinition(ctx, loader)
}

type loaderHostAgentExecutor struct {
	manager *LoaderManager
}

func (e loaderHostAgentExecutor) ExecuteAgent(ctx context.Context, session *domain.Session, request loaders.HostAgentExecutionRequest) (domain.NotebookCell, error) {
	cell, _, _, err := e.manager.executor.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{
		Agent:             request.Provider,
		AgentDefinitionID: request.AgentDefinitionID,
		Model:             request.Model,
		RunID:             request.RunID,
		Message:           request.Prompt,
		Timeout:           request.Timeout,
		OutputSchemaJSON:  request.OutputSchemaJSON,
	})
	return cell, err
}

type loaderHostCommandExecutor struct {
	manager *LoaderManager
}

func (e loaderHostCommandExecutor) ExecuteLoaderCommand(ctx context.Context, session *domain.Session, request domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	return e.manager.executor.ExecuteLoaderCommand(ctx, session, request)
}

type loaderHostProjectAgentRunner struct {
	manager *LoaderManager
}

func (r loaderHostProjectAgentRunner) RunProjectAgent(ctx context.Context, request loaders.HostProjectAgentRequest) (domain.ProjectRunRecord, error, error) {
	return r.manager.projectAgentRunnerComponent().RunProjectAgent(ctx, &agentcomposev2.RunAgentRequest{
		ProjectId:        request.ProjectID,
		AgentName:        request.AgentName,
		Prompt:           request.Prompt,
		Source:           agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER,
		SchedulerId:      request.SchedulerID,
		TriggerId:        request.TriggerID,
		OutputSchemaJson: request.OutputSchemaJSON,
		ClientRequestId:  request.ClientRequestID,
	}, nil)
}

type loaderHostLLMRunner struct {
	manager *LoaderManager
}

func (r loaderHostLLMRunner) Generate(ctx context.Context, prompt, model, outputSchema string) (domain.LoaderLLMResult, error) {
	if r.manager == nil || r.manager.llm == nil {
		return domain.LoaderLLMResult{}, fmt.Errorf("llm client is unavailable")
	}
	result, err := r.manager.llm.Generate(ctx, prompt, model, outputSchema)
	if err != nil {
		return domain.LoaderLLMResult{}, err
	}
	return domain.LoaderLLMResult{
		Text:         result.Text,
		Model:        result.Model,
		ResponseID:   result.ResponseID,
		FinishReason: result.FinishReason,
	}, nil
}

func (m *LoaderManager) loaderHostDependencies() loaders.RunHostDependencies {
	return loaders.RunHostDependencies{
		Store:                   m.configDB,
		Events:                  loaderHostEvents{manager: m},
		Sessions:                loaderHostSessions{manager: m},
		AgentDefinitions:        loaderHostAgentDefinitions{manager: m},
		AgentExecutor:           loaderHostAgentExecutor{manager: m},
		CommandExecutor:         loaderHostCommandExecutor{manager: m},
		ProjectAgentRunner:      loaderHostProjectAgentRunner{manager: m},
		LLM:                     loaderHostLLMRunner{manager: m},
		SessionRPC:              m.sessions,
		Publisher:               m,
		CommandRequiresCleanup:  loaders.CommandRequestRequiresCleanup,
		LinkedSessionIDFromJSON: loaderSessionRPCLinkedSessionID,
	}
}

func (m *LoaderManager) newLoaderRuntimeHost(loader domain.Loader, run *domain.LoaderRunSummary, triggerEvent loaders.TriggerEventMetadata) *loaders.RuntimeHost {
	return loaders.NewRuntimeHost(m.loaderHostDependencies(), loader, run, triggerEvent)
}

func (h *loaderRunHost) runtimeHost() *loaders.RuntimeHost {
	h.commandSessionIDsMutex.Lock()
	defer h.commandSessionIDsMutex.Unlock()
	if h.runtimeHostCore == nil {
		h.runtimeHostCore = h.manager.newLoaderRuntimeHost(h.loader, h.run, h.triggerEvent)
	}
	return h.runtimeHostCore
}
