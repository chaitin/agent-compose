package schedulers

import (
	"context"
	"fmt"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"

	"github.com/google/uuid"
)

type HostProjectAgentRequest struct {
	SchedulerID        string
	SchedulerRunID     string
	ProjectID          string
	AgentName          string
	Prompt             string
	ProjectSchedulerID string
	TriggerID          string
	OutputSchemaJSON   string
	ClientRequestID    string
	Volumes            []domain.VolumeMountSpec
	SandboxPolicy      string
	SandboxConfigHash  string
	// SandboxID runs the agent in a sandbox the scheduler prepared for a
	// request that sets its own sandbox; see ProjectAgent.
	SandboxID string
	// Provider and Timeout carry the script's agent and timeout options.
	Provider string
	Timeout  time.Duration
}

type HostProjectAgentRunner interface {
	RunProjectAgent(ctx context.Context, request HostProjectAgentRequest) (domain.ProjectRunRecord, error, error)
}

// ProjectAgent runs one agent turn as an Agent Run of the scheduler's project
// agent, so every scheduler.agent call has a run record and a run log. The
// script's agent and timeout options become the run's provider and timeout.
// Options that describe the sandbox itself (title, driver, image, workspace,
// sandbox env, volumes, jupyter) get a sandbox the scheduler prepares, which
// the run uses and the scheduler stops afterwards.
func (h *RuntimeHost) ProjectAgent(ctx context.Context, prompt string, request domain.SchedulerAgentRequest) (domain.SchedulerAgentResult, error) {
	if strings.TrimSpace(h.scheduler.Summary.ProjectID) == "" || strings.TrimSpace(h.scheduler.Summary.AgentName) == "" {
		return domain.SchedulerAgentResult{}, fmt.Errorf("scheduler.agent needs a scheduler of a project agent")
	}
	sandboxPolicy := domain.SchedulerSandboxPolicyNew
	if strings.TrimSpace(h.scheduler.Summary.SandboxPolicy) != "" {
		sandboxPolicy = NormalizeSandboxPolicy(h.scheduler.Summary.SandboxPolicy)
	}
	if strings.TrimSpace(AgentSandboxPolicy(request)) != "" {
		sandboxPolicy = NormalizeSandboxPolicy(AgentSandboxPolicy(request))
	}
	configHash, err := SchedulerSandboxConfigHash(h.scheduler)
	if err != nil {
		return domain.SchedulerAgentResult{}, err
	}
	runRequest := HostProjectAgentRequest{
		SchedulerID:        h.scheduler.Summary.ID,
		SchedulerRunID:     h.execution.ID,
		ProjectID:          h.scheduler.Summary.ProjectID,
		AgentName:          h.scheduler.Summary.AgentName,
		Prompt:             prompt,
		ProjectSchedulerID: h.scheduler.Summary.ProjectSchedulerID,
		TriggerID:          h.execution.TriggerID,
		OutputSchemaJSON:   request.OutputSchema,
		ClientRequestID:    h.nextProjectAgentRunID(),
		Volumes:            request.Volumes,
		SandboxPolicy:      sandboxPolicy,
		SandboxConfigHash:  configHash,
		Provider:           domain.NormalizeAgentKind(request.Agent),
		Timeout:            request.Timeout,
	}
	var prepared *domain.Sandbox
	if AgentRequestOverridesSession(request, true) {
		session, eventType, err := h.deps.Sessions.Ensure(ctx, h.scheduler, request, true)
		if err != nil {
			return domain.SchedulerAgentResult{}, err
		}
		if eventType != "" {
			_ = h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
				EventType:       eventType,
				Level:           "info",
				Message:         "scheduler sandbox ready",
				Payload:         map[string]any{"sandboxId": session.Summary.ID},
				LinkedSandboxID: session.Summary.ID,
			})
		}
		prepared = session
		runRequest.SandboxID, runRequest.Volumes = session.Summary.ID, nil
	}
	run, execErr, err := h.deps.ProjectAgentRunner.RunProjectAgent(ctx, runRequest)
	if prepared != nil {
		defer h.shutdownSessionAndRecordEvent(ctx, prepared.Summary.ID, "scheduler sandbox after agent run", "scheduler sandbox stopped after agent run")
	}
	if err != nil {
		return domain.SchedulerAgentResult{}, err
	}
	result, jsonErr := AgentResultFromProjectRun(run, request.OutputSchema)
	if jsonErr != nil && execErr == nil {
		execErr = jsonErr
	}
	level := "info"
	eventName := "scheduler.agent.completed"
	if execErr != nil || run.Status != domain.ProjectRunStatusSucceeded {
		level = "error"
		eventName = "scheduler.agent.failed"
		result.Text = firstHostNonEmpty(result.Text, run.Error, execErrString(execErr))
	}
	_ = h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
		EventType:           eventName,
		Level:               level,
		Message:             firstHostNonEmpty(result.Text, fmt.Sprintf("%s completed", result.Agent)),
		Payload:             result,
		LinkedSandboxID:     result.SandboxID,
		LinkedCellID:        result.CellID,
		LinkedAgentThreadID: result.AgentThreadID,
	})
	h.publishAgentCompleted(ctx, result, &run)
	if execErr != nil {
		return result, execErr
	}
	return result, nil
}

func (h *RuntimeHost) nextProjectAgentRunID() string {
	baseID := firstHostNonEmpty(h.execution.ID, uuid.NewString())
	return fmt.Sprintf("%s:agent:%d", baseID, h.projectAgentRunSequence.Add(1))
}
