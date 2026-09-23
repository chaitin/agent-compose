package api

import (
	"encoding/json"
	"path/filepath"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func ProjectToProto(project domain.ProjectRecord, spec *agentcomposev2.ProjectSpec, agents []domain.ProjectAgentRecord, schedulers []domain.ProjectSchedulerRecord) *agentcomposev2.Project {
	agents = currentProjectAgents(project, agents)
	schedulers = currentProjectSchedulers(project, schedulers)
	return &agentcomposev2.Project{
		Summary:    ProjectSummaryToProto(project, agents, schedulers),
		Spec:       spec,
		Agents:     ProjectAgentsToProto(agents),
		Schedulers: ProjectSchedulersToProto(schedulers),
	}
}

func ProjectSummaryToProto(project domain.ProjectRecord, agents []domain.ProjectAgentRecord, schedulers []domain.ProjectSchedulerRecord) *agentcomposev2.ProjectSummary {
	agents = currentProjectAgents(project, agents)
	schedulers = currentProjectSchedulers(project, schedulers)
	return projectSummaryWithCountsToProto(project, uint32(len(agents)), uint32(len(schedulers)))
}

func projectSummaryWithCountsToProto(project domain.ProjectRecord, agentCount, schedulerCount uint32) *agentcomposev2.ProjectSummary {
	return &agentcomposev2.ProjectSummary{
		ProjectId:       project.ID,
		Name:            project.Name,
		SourcePath:      project.SourcePath,
		CurrentRevision: uint64(project.CurrentRevision),
		SpecHash:        project.SpecHash,
		AgentCount:      agentCount,
		SchedulerCount:  schedulerCount,
		CreatedAt:       FormatProjectTime(project.CreatedAt),
		UpdatedAt:       FormatProjectTime(project.UpdatedAt),
		RemovedAt:       FormatProjectTime(project.RemovedAt),
	}
}

func currentProjectAgents(project domain.ProjectRecord, agents []domain.ProjectAgentRecord) []domain.ProjectAgentRecord {
	if project.CurrentRevision <= 0 {
		return agents
	}
	current := make([]domain.ProjectAgentRecord, 0, len(agents))
	for _, agent := range agents {
		if agent.Revision == project.CurrentRevision {
			current = append(current, agent)
		}
	}
	return current
}

func currentProjectSchedulers(project domain.ProjectRecord, schedulers []domain.ProjectSchedulerRecord) []domain.ProjectSchedulerRecord {
	if project.CurrentRevision <= 0 {
		return schedulers
	}
	current := make([]domain.ProjectSchedulerRecord, 0, len(schedulers))
	for _, scheduler := range schedulers {
		if scheduler.Revision == project.CurrentRevision {
			current = append(current, scheduler)
		}
	}
	return current
}

func ProjectRevisionToProto(revision domain.ProjectRevisionRecord, spec *agentcomposev2.ProjectSpec) *agentcomposev2.ProjectRevision {
	return &agentcomposev2.ProjectRevision{
		ProjectId: revision.ProjectID,
		Revision:  uint64(revision.Revision),
		SpecHash:  revision.SpecHash,
		Spec:      spec,
		CreatedAt: FormatProjectTime(revision.CreatedAt),
	}
}

func ProjectAgentsToProto(agents []domain.ProjectAgentRecord) []*agentcomposev2.ProjectAgent {
	items := make([]*agentcomposev2.ProjectAgent, 0, len(agents))
	for _, agent := range agents {
		enabled, displayName, description, specErr := projectAgentSpecMetadata(agent.SpecJSON)
		availability := agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_AVAILABLE
		health := agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_HEALTHY
		if specErr != nil || strings.TrimSpace(agent.ID) == "" || strings.TrimSpace(agent.AgentName) == "" || strings.TrimSpace(agent.Provider) == "" {
			availability = agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_VALIDATION_FAILED
			health = agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_AT_RISK
		} else if !enabled {
			availability = agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_UNAVAILABLE
		}
		items = append(items, &agentcomposev2.ProjectAgent{
			ProjectId:        agent.ProjectID,
			AgentName:        agent.AgentName,
			DisplayName:      displayName,
			Description:      description,
			ManagedAgentId:   agent.ID,
			Provider:         agent.Provider,
			Model:            agent.Model,
			Image:            agent.Image,
			Driver:           agent.Driver,
			SchedulerEnabled: agent.SchedulerEnabled,
			Enabled:          enabled, Availability: availability, Health: health,
		})
	}
	return items
}

func projectAgentSpecMetadata(specJSON string) (bool, string, string, error) {
	var raw struct {
		Enabled     *bool  `json:"enabled"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(specJSON), &raw); err != nil {
		return false, "", "", err
	}
	displayName := strings.TrimSpace(raw.DisplayName)
	description := strings.TrimSpace(raw.Description)
	if raw.Enabled == nil {
		return true, displayName, description, nil
	}
	return *raw.Enabled, displayName, description, nil
}

func ProjectSchedulersToProto(schedulers []domain.ProjectSchedulerRecord) []*agentcomposev2.ProjectScheduler {
	items := make([]*agentcomposev2.ProjectScheduler, 0, len(schedulers))
	for _, scheduler := range schedulers {
		displayName, description := projectSchedulerPresentation(scheduler.SpecJSON)
		items = append(items, &agentcomposev2.ProjectScheduler{
			ProjectId:    scheduler.ProjectID,
			AgentName:    scheduler.AgentName,
			SchedulerId:  scheduler.SchedulerID,
			Enabled:      scheduler.Enabled,
			TriggerCount: uint32(scheduler.TriggerCount),
			DisplayName:  displayName,
			Description:  description,
		})
	}
	return items
}

func projectSchedulerPresentation(specJSON string) (string, string) {
	var presentation struct {
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
	}
	if json.Unmarshal([]byte(specJSON), &presentation) != nil {
		return "", ""
	}
	return strings.TrimSpace(presentation.DisplayName), strings.TrimSpace(presentation.Description)
}

func ProjectServiceSourcePath(source *agentcomposev2.ProjectSource) string {
	if source == nil {
		return ""
	}
	if composePath := strings.TrimSpace(source.GetComposePath()); composePath != "" {
		return composePath
	}
	if projectDir := strings.TrimSpace(source.GetProjectDir()); projectDir != "" {
		return filepath.Join(projectDir, "agent-compose.yml")
	}
	return ""
}
