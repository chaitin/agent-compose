package schedulers

import (
	"context"
	"fmt"
	"strings"

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
}

type HostProjectAgentRunner interface {
	RunProjectAgent(ctx context.Context, request HostProjectAgentRequest) (domain.ProjectRunRecord, error, error)
}

func (h *RuntimeHost) ProjectAgent(ctx context.Context, prompt string, request domain.SchedulerAgentRequest) (domain.SchedulerAgentResult, error) {
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
	run, execErr, err := h.deps.ProjectAgentRunner.RunProjectAgent(ctx, HostProjectAgentRequest{
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
	})
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

func (h *RuntimeHost) useProjectAgentRun(request domain.SchedulerAgentRequest) bool {
	if strings.TrimSpace(h.scheduler.Summary.ProjectID) == "" || strings.TrimSpace(h.scheduler.Summary.AgentName) == "" {
		return false
	}
	if strings.TrimSpace(request.Agent) != "" || request.Timeout > 0 {
		return false
	}
	return !AgentRequestOverridesSession(request, true)
}
