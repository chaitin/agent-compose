package runs

import (
	"context"
	"fmt"
	"strings"
	"sync"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type ManualTriggerResolution struct {
	Request  RunAgentRequest
	Warnings []string
}

func (c *Controller) resolveTriggerForManualRun(ctx context.Context, req RunAgentRequest) (ManualTriggerResolution, error) {
	result := ManualTriggerResolution{Request: req}
	triggerID := strings.TrimSpace(req.TriggerID)
	if triggerID == "" || NormalizeSource(req.Source) != domain.ProjectRunSourceManual {
		return result, nil
	}
	if strings.TrimSpace(req.Command) != "" {
		return result, fmt.Errorf("%w: scheduler trigger cannot be combined with command", ErrInvalidRequest)
	}
	if c.configDB == nil {
		return result, fmt.Errorf("config store is required")
	}
	scheduler, definition, trigger, err := c.manualTriggerScheduler(ctx, req.ProjectID, req.AgentName, triggerID)
	if err != nil {
		return result, err
	}
	warnings := make([]string, 0, 2)
	if !scheduler.Enabled {
		warnings = append(warnings, fmt.Sprintf("scheduler %s is disabled; running trigger %s because it was requested manually", scheduler.SchedulerID, trigger.ID))
	}
	if !trigger.Enabled {
		warnings = append(warnings, fmt.Sprintf("trigger %s is disabled; running because it was requested manually", trigger.ID))
	}
	captured, err := c.captureManualTriggerAgentRequest(ctx, definition, trigger, req.PayloadJSON)
	if err != nil {
		return result, err
	}
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		result.Request.Prompt = prompt
	} else {
		result.Request.Prompt = captured.prompt
	}
	if strings.TrimSpace(result.Request.SchedulerID) == "" {
		result.Request.SchedulerID = scheduler.SchedulerID
	}
	if strings.TrimSpace(result.Request.OutputSchemaJSON) == "" {
		result.Request.OutputSchemaJSON = captured.request.OutputSchema
	}
	result.Request.Env = append(result.Request.Env, envVarSpecsFromSandboxEnv(schedulers.AgentSandboxEnv(captured.request))...)
	effectivePolicy := domain.SchedulerSandboxPolicyNew
	if strings.TrimSpace(definition.Summary.SandboxPolicy) != "" {
		effectivePolicy = schedulers.NormalizeSandboxPolicy(definition.Summary.SandboxPolicy)
	}
	if strings.TrimSpace(schedulers.AgentSandboxPolicy(captured.request)) != "" {
		effectivePolicy = schedulers.NormalizeSandboxPolicy(schedulers.AgentSandboxPolicy(captured.request))
	}
	// A run that carries a user's identity must not enter the trigger's sticky
	// sandbox: that sandbox is shared with the scheduler's unattended runs and
	// would keep the user's capability binding and data after this run ends.
	effectivePolicy = schedulers.SandboxPolicyForIdentity(ctx, effectivePolicy)
	if effectivePolicy == domain.SchedulerSandboxPolicySticky {
		configHash, err := schedulers.SchedulerSandboxConfigHash(definition)
		if err != nil {
			return result, err
		}
		result.Request.StickyBindingSchedulerID = definition.Summary.ID
		result.Request.StickyBindingTriggerID = trigger.ID
		result.Request.StickyBindingConfigHash = configHash
		result.Request.CleanupPolicy = agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING
	}
	result.Warnings = warnings
	return result, nil
}

func (c *Controller) manualTriggerScheduler(ctx context.Context, projectID, agentName, triggerID string) (domain.ProjectSchedulerRecord, domain.Scheduler, *domain.SchedulerTrigger, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	triggerID = strings.TrimSpace(triggerID)
	schedulers, err := c.configDB.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		return domain.ProjectSchedulerRecord{}, domain.Scheduler{}, nil, err
	}
	for _, scheduler := range schedulers {
		if strings.TrimSpace(scheduler.AgentName) != agentName || strings.TrimSpace(scheduler.ID) == "" {
			continue
		}
		definition, err := c.configDB.GetScheduler(ctx, scheduler.ID)
		if err != nil {
			return domain.ProjectSchedulerRecord{}, domain.Scheduler{}, nil, err
		}
		if !schedulerMatchesProjectAgent(definition, projectID, agentName, scheduler.SchedulerID) {
			continue
		}
		for index := range definition.Triggers {
			if definition.Triggers[index].ID != triggerID {
				continue
			}
			trigger := definition.Triggers[index]
			return scheduler, definition, &trigger, nil
		}
	}
	id := strings.Join([]string{projectID, agentName, triggerID}, "/")
	return domain.ProjectSchedulerRecord{}, domain.Scheduler{}, nil, domain.ResourceError(domain.ErrNotFound, "project trigger", id, fmt.Sprintf("project trigger %s not found", id), nil)
}

func schedulerMatchesProjectAgent(definition domain.Scheduler, projectID, agentName, schedulerID string) bool {
	summary := definition.Summary
	return strings.TrimSpace(summary.ProjectID) == strings.TrimSpace(projectID) &&
		strings.TrimSpace(summary.AgentName) == strings.TrimSpace(agentName) &&
		strings.TrimSpace(summary.ProjectSchedulerID) == strings.TrimSpace(schedulerID)
}

type capturedManualTriggerAgentRequest struct {
	prompt  string
	request domain.SchedulerAgentRequest
}

func (c *Controller) captureManualTriggerAgentRequest(ctx context.Context, definition domain.Scheduler, trigger *domain.SchedulerTrigger, payloadJSON string) (capturedManualTriggerAgentRequest, error) {
	if c.schedulerEngine == nil {
		return capturedManualTriggerAgentRequest{}, fmt.Errorf("scheduler engine is required")
	}
	payloadJSON = strings.TrimSpace(payloadJSON)
	if payloadJSON == "" {
		payloadJSON = `{}`
	}
	host := &manualTriggerCaptureHost{}
	_, err := c.schedulerEngine.Execute(ctx, schedulers.SchedulerExecutionRequest{
		Runtime:     definition.Summary.Runtime,
		Script:      definition.Script,
		Trigger:     trigger,
		PayloadJSON: payloadJSON,
		// Capturing a prompt only observes what the trigger would request, so
		// the script's own delays must not stall the resolve API.
		DryRun: true,
	}, host)
	if err != nil {
		return capturedManualTriggerAgentRequest{}, fmt.Errorf("%w: resolve trigger %s prompt: %w", domain.ErrInvalidArgument, trigger.ID, err)
	}
	calls, prompt, request := host.captured()
	if calls != 1 || strings.TrimSpace(prompt) == "" {
		return capturedManualTriggerAgentRequest{}, fmt.Errorf("%w: trigger %s must call scheduler.agent exactly once", domain.ErrInvalidArgument, trigger.ID)
	}
	return capturedManualTriggerAgentRequest{prompt: prompt, request: request}, nil
}

// manualTriggerCaptureHost records the single scheduler.agent call a manual
// trigger is expected to make. scheduler.agent.async reaches the host from its
// own goroutine, so several calls can arrive concurrently and the fields are
// guarded.
type manualTriggerCaptureHost struct {
	mu      sync.Mutex
	calls   int
	prompt  string
	request domain.SchedulerAgentRequest
}

// captured reports the recorded call count and payload.
func (h *manualTriggerCaptureHost) captured() (int, string, domain.SchedulerAgentRequest) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls, h.prompt, h.request
}

func (h *manualTriggerCaptureHost) Log(context.Context, string, any) error { return nil }

func (h *manualTriggerCaptureHost) PublishEvent(context.Context, string, string) (domain.TopicEventRecord, error) {
	return domain.TopicEventRecord{}, fmt.Errorf("scheduler.event.publish is unavailable during manual trigger resolution")
}

func (h *manualTriggerCaptureHost) Agent(_ context.Context, prompt string, request domain.SchedulerAgentRequest) (domain.SchedulerAgentResult, error) {
	trimmed := strings.TrimSpace(prompt)
	h.mu.Lock()
	h.calls++
	h.prompt = trimmed
	h.request = request
	h.mu.Unlock()
	return domain.SchedulerAgentResult{Text: trimmed, FinalText: trimmed, Success: true}, nil
}

func (h *manualTriggerCaptureHost) Command(context.Context, domain.SchedulerCommandRequest) (domain.SchedulerCommandResult, error) {
	return domain.SchedulerCommandResult{}, fmt.Errorf("scheduler.command is unavailable during manual trigger resolution")
}

func (h *manualTriggerCaptureHost) LLM(context.Context, string, domain.SchedulerLLMRequest) (domain.SchedulerLLMResult, error) {
	return domain.SchedulerLLMResult{}, fmt.Errorf("scheduler.llm is unavailable during manual trigger resolution")
}

func (h *manualTriggerCaptureHost) StateGet(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (h *manualTriggerCaptureHost) StateSet(context.Context, string, string) error { return nil }

func (h *manualTriggerCaptureHost) StateDelete(context.Context, string) error { return nil }

func (h *manualTriggerCaptureHost) CallSandboxRPC(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("scheduler.sandbox is unavailable during manual trigger resolution")
}

func envVarSpecsFromSandboxEnv(items []domain.SandboxEnvVar) []*agentcomposev2.EnvVarSpec {
	result := make([]*agentcomposev2.EnvVarSpec, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		result = append(result, &agentcomposev2.EnvVarSpec{Name: name, Value: item.Value, Secret: item.Secret})
	}
	return result
}
