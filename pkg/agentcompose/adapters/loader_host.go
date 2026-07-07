package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-compose/pkg/execution"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

type LoaderHostEvents struct {
	Controller *loaders.Controller
}

func (e LoaderHostEvents) Add(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	if e.Controller == nil {
		return fmt.Errorf("scheduler execution controller is unavailable")
	}
	return e.Controller.AddSchedulerExecutionEvent(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
}

func (e LoaderHostEvents) AddRecord(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) (domain.LoaderEvent, error) {
	if e.Controller == nil {
		return domain.LoaderEvent{}, fmt.Errorf("scheduler execution controller is unavailable")
	}
	return e.Controller.AddSchedulerExecutionEventRecord(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
}

type LoaderHostAgentExecutor struct {
	Executor *AgentExecutor
}

func (e LoaderHostAgentExecutor) ExecuteAgent(ctx context.Context, session *domain.Session, request loaders.HostAgentExecutionRequest) (domain.NotebookCell, error) {
	if e.Executor == nil {
		return domain.NotebookCell{}, fmt.Errorf("agent executor is unavailable")
	}
	cell, _, _, err := e.Executor.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{
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

type LoaderHostCommandExecutor struct {
	Executor *LoaderCommandExecutor
}

func (e LoaderHostCommandExecutor) ExecuteLoaderCommand(ctx context.Context, session *domain.Session, request domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	if e.Executor == nil {
		return domain.LoaderCommandResult{}, fmt.Errorf("loader command executor is unavailable")
	}
	return e.Executor.ExecuteLoaderCommand(ctx, session, request)
}

type LoaderHostLLMRunner struct {
	Client *LLMClient
}

func (r LoaderHostLLMRunner) Generate(ctx context.Context, prompt, model, outputSchema string) (domain.LoaderLLMResult, error) {
	if r.Client == nil {
		return domain.LoaderLLMResult{}, fmt.Errorf("llm client is unavailable")
	}
	result, err := r.Client.Generate(ctx, prompt, model, outputSchema)
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

func LoaderSessionRPCLinkedSessionID(method, requestJSON, responseJSON string) string {
	if value := loaderSessionIDFromJSON(responseJSON); value != "" {
		return value
	}
	if strings.TrimSpace(method) == "ListSessions" {
		return ""
	}
	return loaderSessionIDFromJSON(requestJSON)
}

func loaderSessionIDFromJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	if value, ok := payload["sessionId"].(string); ok {
		return strings.TrimSpace(value)
	}
	sessionValue, ok := payload["session"].(map[string]any)
	if !ok {
		return ""
	}
	summaryValue, ok := sessionValue["summary"].(map[string]any)
	if !ok {
		return ""
	}
	if value, ok := summaryValue["sessionId"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
