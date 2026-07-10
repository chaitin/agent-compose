package runs

import (
	"context"
	"fmt"
	"strings"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
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
	scheduler, loader, trigger, err := c.manualTriggerLoader(ctx, req.ProjectID, req.AgentName, triggerID)
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
	captured, err := c.captureManualTriggerAgentRequest(ctx, loader, trigger, req.PayloadJSON)
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
	result.Request.Env = append(result.Request.Env, envVarSpecsFromSandboxEnv(domain.LoaderAgentSandboxEnv(captured.request))...)
	effectivePolicy := domain.LoaderSandboxPolicyNew
	if strings.TrimSpace(loader.Summary.SandboxPolicy) != "" {
		effectivePolicy = domain.NormalizeLoaderSandboxPolicy(loader.Summary.SandboxPolicy)
	}
	if strings.TrimSpace(domain.LoaderAgentSandboxPolicy(captured.request)) != "" {
		effectivePolicy = domain.NormalizeLoaderSandboxPolicy(domain.LoaderAgentSandboxPolicy(captured.request))
	}
	if effectivePolicy == domain.LoaderSandboxPolicySticky {
		result.Request.StickyBindingLoaderID = loader.Summary.ID
		result.Request.StickyBindingTriggerID = trigger.ID
		result.Request.CleanupPolicy = agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING
	}
	result.Warnings = warnings
	return result, nil
}

func (c *Controller) manualTriggerLoader(ctx context.Context, projectID, agentName, triggerID string) (domain.ProjectSchedulerRecord, domain.Loader, *domain.LoaderTrigger, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	triggerID = strings.TrimSpace(triggerID)
	schedulers, err := c.configDB.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		return domain.ProjectSchedulerRecord{}, domain.Loader{}, nil, err
	}
	for _, scheduler := range schedulers {
		if strings.TrimSpace(scheduler.AgentName) != agentName || strings.TrimSpace(scheduler.ManagedLoaderID) == "" {
			continue
		}
		loader, err := c.configDB.GetLoader(ctx, scheduler.ManagedLoaderID)
		if err != nil {
			return domain.ProjectSchedulerRecord{}, domain.Loader{}, nil, err
		}
		if !managedLoaderMatchesProjectAgent(loader, projectID, agentName, scheduler.SchedulerID) {
			continue
		}
		for index := range loader.Triggers {
			if loader.Triggers[index].ID != triggerID {
				continue
			}
			trigger := loader.Triggers[index]
			return scheduler, loader, &trigger, nil
		}
	}
	id := strings.Join([]string{projectID, agentName, triggerID}, "/")
	return domain.ProjectSchedulerRecord{}, domain.Loader{}, nil, domain.ResourceError(domain.ErrNotFound, "project trigger", id, fmt.Sprintf("project trigger %s not found", id), nil)
}

func managedLoaderMatchesProjectAgent(loader domain.Loader, projectID, agentName, schedulerID string) bool {
	summary := loader.Summary
	return strings.TrimSpace(summary.ManagedProjectID) == strings.TrimSpace(projectID) &&
		strings.TrimSpace(summary.ManagedAgentName) == strings.TrimSpace(agentName) &&
		strings.TrimSpace(summary.ManagedSchedulerID) == strings.TrimSpace(schedulerID)
}

type capturedManualTriggerAgentRequest struct {
	prompt  string
	request domain.LoaderAgentRequest
}

func (c *Controller) captureManualTriggerAgentRequest(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON string) (capturedManualTriggerAgentRequest, error) {
	if c.loaderEngine == nil {
		return capturedManualTriggerAgentRequest{}, fmt.Errorf("loader engine is required")
	}
	payloadJSON = strings.TrimSpace(payloadJSON)
	if payloadJSON == "" {
		payloadJSON = `{}`
	}
	host := &manualTriggerCaptureHost{}
	_, err := c.loaderEngine.Execute(ctx, loaders.LoaderExecutionRequest{
		Runtime:     loader.Summary.Runtime,
		Script:      loader.Script,
		Trigger:     trigger,
		PayloadJSON: payloadJSON,
	}, host)
	if err != nil {
		return capturedManualTriggerAgentRequest{}, fmt.Errorf("%w: resolve trigger %s prompt: %v", domain.ErrInvalidArgument, trigger.ID, err)
	}
	if host.calls != 1 || strings.TrimSpace(host.prompt) == "" {
		return capturedManualTriggerAgentRequest{}, fmt.Errorf("%w: trigger %s must call scheduler.agent exactly once", domain.ErrInvalidArgument, trigger.ID)
	}
	return capturedManualTriggerAgentRequest{prompt: host.prompt, request: host.request}, nil
}

type manualTriggerCaptureHost struct {
	calls   int
	prompt  string
	request domain.LoaderAgentRequest
}

func (h *manualTriggerCaptureHost) Log(context.Context, string, any) error { return nil }

func (h *manualTriggerCaptureHost) PublishEvent(context.Context, string, string) (domain.TopicEventRecord, error) {
	return domain.TopicEventRecord{}, fmt.Errorf("scheduler.event.publish is unavailable during manual trigger resolution")
}

func (h *manualTriggerCaptureHost) Agent(_ context.Context, prompt string, request domain.LoaderAgentRequest) (domain.LoaderAgentResult, error) {
	h.calls++
	h.prompt = strings.TrimSpace(prompt)
	h.request = request
	return domain.LoaderAgentResult{Text: h.prompt, FinalText: h.prompt, Success: true}, nil
}

func (h *manualTriggerCaptureHost) Command(context.Context, domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	return domain.LoaderCommandResult{}, fmt.Errorf("scheduler.command is unavailable during manual trigger resolution")
}

func (h *manualTriggerCaptureHost) LLM(context.Context, string, domain.LoaderLLMRequest) (domain.LoaderLLMResult, error) {
	return domain.LoaderLLMResult{}, fmt.Errorf("scheduler.llm is unavailable during manual trigger resolution")
}

func (h *manualTriggerCaptureHost) StateGet(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (h *manualTriggerCaptureHost) StateSet(context.Context, string, string) error { return nil }

func (h *manualTriggerCaptureHost) StateDelete(context.Context, string) error { return nil }

func (h *manualTriggerCaptureHost) CallSessionRPC(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("scheduler.session is unavailable during manual trigger resolution")
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
