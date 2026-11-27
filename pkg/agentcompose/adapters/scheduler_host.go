package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

type SchedulerHostEvents struct {
	Controller *schedulers.Controller
}

func (e SchedulerHostEvents) Add(ctx context.Context, event schedulers.SchedulerEventInput) error {
	if e.Controller == nil {
		return fmt.Errorf("scheduler controller is unavailable")
	}
	return e.Controller.AddSchedulerEvent(ctx, event)
}

func (e SchedulerHostEvents) AddRecord(ctx context.Context, event schedulers.SchedulerEventInput) (domain.SchedulerEvent, error) {
	if e.Controller == nil {
		return domain.SchedulerEvent{}, fmt.Errorf("scheduler controller is unavailable")
	}
	return e.Controller.AddSchedulerEventRecord(ctx, event)
}

type SchedulerHostCommandExecutor struct {
	Executor *SchedulerCommandExecutor
}

func (e SchedulerHostCommandExecutor) ExecuteSchedulerCommand(ctx context.Context, session *domain.Sandbox, request domain.SchedulerCommandRequest) (domain.SchedulerCommandResult, error) {
	if e.Executor == nil {
		return domain.SchedulerCommandResult{}, fmt.Errorf("scheduler command executor is unavailable")
	}
	return e.Executor.ExecuteSchedulerCommand(ctx, session, request)
}

type SchedulerHostLLMRunner struct {
	Client *LLMClient
}

func (r SchedulerHostLLMRunner) Generate(ctx context.Context, request schedulers.HostLLMGenerateRequest) (domain.SchedulerLLMResult, error) {
	if r.Client == nil {
		return domain.SchedulerLLMResult{}, fmt.Errorf("llm client is unavailable")
	}
	result, err := r.Client.Generate(ctx, request.Prompt, request.Model, request.OutputSchema)
	if err != nil {
		return domain.SchedulerLLMResult{}, err
	}
	return domain.SchedulerLLMResult{
		Text:         result.Text,
		Model:        result.Model,
		ResponseID:   result.ResponseID,
		FinishReason: result.FinishReason,
	}, nil
}

func SchedulerSandboxRPCLinkedSandboxID(method, requestJSON, responseJSON string) string {
	if value := schedulerSandboxIDFromJSON(responseJSON); value != "" {
		return value
	}
	if strings.TrimSpace(method) == "ListSandboxes" {
		return ""
	}
	return schedulerSandboxIDFromJSON(requestJSON)
}

func schedulerSandboxIDFromJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	if value, ok := payload["sandboxId"].(string); ok {
		return strings.TrimSpace(value)
	}
	sandboxValue, ok := payload["sandbox"].(map[string]any)
	if !ok {
		return ""
	}
	summaryValue, ok := sandboxValue["summary"].(map[string]any)
	if !ok {
		return ""
	}
	if value, ok := summaryValue["sandboxId"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
