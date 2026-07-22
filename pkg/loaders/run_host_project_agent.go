package loaders

import (
	"context"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"

	"github.com/google/uuid"
)

type HostProjectAgentRequest struct {
	LoaderID          string
	ProjectID         string
	AgentName         string
	Prompt            string
	SchedulerID       string
	TriggerID         string
	OutputSchemaJSON  string
	ClientRequestID   string
	Volumes           []domain.VolumeMountSpec
	SandboxPolicy     string
	SandboxConfigHash string
}

type HostProjectAgentRunner interface {
	RunProjectAgent(ctx context.Context, request HostProjectAgentRequest) (domain.ProjectRunRecord, error, error)
}

func (h *RuntimeHost) ProjectAgent(ctx context.Context, prompt string, request domain.LoaderAgentRequest) (domain.LoaderAgentResult, error) {
	sandboxPolicy := domain.LoaderSandboxPolicyNew
	if strings.TrimSpace(h.loader.Summary.SandboxPolicy) != "" {
		sandboxPolicy = domain.NormalizeLoaderSandboxPolicy(h.loader.Summary.SandboxPolicy)
	}
	if strings.TrimSpace(domain.LoaderAgentSandboxPolicy(request)) != "" {
		sandboxPolicy = domain.NormalizeLoaderSandboxPolicy(domain.LoaderAgentSandboxPolicy(request))
	}
	configHash, err := LoaderSandboxConfigHash(h.loader)
	if err != nil {
		return domain.LoaderAgentResult{}, err
	}
	run, execErr, err := h.deps.ProjectAgentRunner.RunProjectAgent(ctx, HostProjectAgentRequest{
		LoaderID:          h.loader.Summary.ID,
		ProjectID:         h.loader.Summary.ManagedProjectID,
		AgentName:         h.loader.Summary.ManagedAgentName,
		Prompt:            prompt,
		SchedulerID:       h.loader.Summary.ManagedSchedulerID,
		TriggerID:         h.execution.TriggerID,
		OutputSchemaJSON:  request.OutputSchema,
		ClientRequestID:   h.nextProjectAgentRunID(),
		Volumes:           request.Volumes,
		SandboxPolicy:     sandboxPolicy,
		SandboxConfigHash: configHash,
	})
	if err != nil {
		return domain.LoaderAgentResult{}, err
	}
	result, jsonErr := AgentResultFromProjectRun(run, request.OutputSchema)
	if jsonErr != nil && execErr == nil {
		execErr = jsonErr
	}
	level := "info"
	eventName := "loader.agent.completed"
	if execErr != nil || run.Status != domain.ProjectRunStatusSucceeded {
		level = "error"
		eventName = "loader.agent.failed"
		result.Text = firstHostNonEmpty(result.Text, run.Error, execErrString(execErr))
	}
	_ = h.addLinkedLoaderEvent(ctx, eventName, level, firstHostNonEmpty(result.Text, fmt.Sprintf("%s completed", result.Agent)), result, result.SandboxID, result.CellID, result.AgentThreadID)
	h.publishAgentCompleted(result, &run)
	if execErr != nil {
		return result, execErr
	}
	return result, nil
}

func (h *RuntimeHost) nextProjectAgentRunID() string {
	baseID := firstHostNonEmpty(h.execution.ID, uuid.NewString())
	return fmt.Sprintf("%s:agent:%d", baseID, h.projectAgentRunSequence.Add(1))
}

func (h *RuntimeHost) useProjectManagedAgentRun(request domain.LoaderAgentRequest) bool {
	if strings.TrimSpace(h.loader.Summary.ManagedProjectID) == "" || strings.TrimSpace(h.loader.Summary.ManagedAgentName) == "" {
		return false
	}
	if strings.TrimSpace(request.Agent) != "" || request.Timeout > 0 {
		return false
	}
	return !AgentRequestOverridesSession(request, true)
}
