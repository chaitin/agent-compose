package projects

import (
	"agent-compose/pkg/identity"
	"encoding/json"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
)

func NormalizeRecord(project domain.ProjectRecord) (domain.ProjectRecord, error) {
	project.ID = strings.TrimSpace(project.ID)
	project.Name = strings.TrimSpace(project.Name)
	project.ShortID = strings.TrimSpace(project.ShortID)
	project.SourcePath = domain.NormalizeProjectSourcePath(project.SourcePath)
	project.SourceJSON = strings.TrimSpace(project.SourceJSON)
	project.SpecHash = strings.TrimSpace(project.SpecHash)
	if project.ID == "" {
		return domain.ProjectRecord{}, fmt.Errorf("project id is required")
	}
	if project.Name == "" {
		return domain.ProjectRecord{}, fmt.Errorf("project name is required")
	}
	if project.ShortID == "" {
		project.ShortID = identity.ShortID(project.ID)
	}
	if project.SourceJSON == "" {
		sourceJSON, err := EncodeSourceJSON(project.SourcePath)
		if err != nil {
			return domain.ProjectRecord{}, err
		}
		project.SourceJSON = sourceJSON
	}
	if !json.Valid([]byte(project.SourceJSON)) {
		return domain.ProjectRecord{}, fmt.Errorf("project source_json must be valid JSON")
	}
	if project.CurrentRevision < 0 {
		return domain.ProjectRecord{}, fmt.Errorf("project current revision cannot be negative")
	}
	return project, nil
}

func NormalizeAgentRecord(agent domain.ProjectAgentRecord) (domain.ProjectAgentRecord, error) {
	agent.ID = strings.TrimSpace(agent.ID)
	agent.Name = strings.TrimSpace(agent.Name)
	agent.ShortID = strings.TrimSpace(agent.ShortID)
	agent.ProjectID = strings.TrimSpace(agent.ProjectID)
	agent.AgentName = strings.TrimSpace(agent.AgentName)
	agent.ManagedAgentID = strings.TrimSpace(agent.ManagedAgentID)
	agent.Provider = strings.TrimSpace(agent.Provider)
	agent.Model = strings.TrimSpace(agent.Model)
	agent.Image = strings.TrimSpace(agent.Image)
	agent.Driver = strings.TrimSpace(agent.Driver)
	agent.SpecJSON = strings.TrimSpace(agent.SpecJSON)
	if agent.ProjectID == "" || agent.AgentName == "" {
		return domain.ProjectAgentRecord{}, fmt.Errorf("project id and agent name are required")
	}
	if agent.Name == "" {
		agent.Name = agent.AgentName
	}
	if agent.ManagedAgentID == "" {
		managedAgentID, err := domain.StableManagedAgentID(agent.ProjectID, agent.AgentName)
		if err != nil {
			return domain.ProjectAgentRecord{}, err
		}
		agent.ManagedAgentID = managedAgentID
	}
	if agent.ID == "" {
		agent.ID = agent.ManagedAgentID
	}
	if agent.ManagedAgentID == "" {
		agent.ManagedAgentID = agent.ID
	}
	if agent.ShortID == "" {
		agent.ShortID = identity.ShortID(agent.ID)
	}
	if agent.Revision < 0 {
		return domain.ProjectAgentRecord{}, fmt.Errorf("project agent revision cannot be negative")
	}
	if agent.SpecJSON == "" {
		agent.SpecJSON = "{}"
	}
	if !json.Valid([]byte(agent.SpecJSON)) {
		return domain.ProjectAgentRecord{}, fmt.Errorf("project agent spec_json must be valid JSON")
	}
	return agent, nil
}

func NormalizeSchedulerRecord(scheduler domain.ProjectSchedulerRecord) (domain.ProjectSchedulerRecord, error) {
	scheduler.ID = strings.TrimSpace(scheduler.ID)
	scheduler.ShortID = strings.TrimSpace(scheduler.ShortID)
	scheduler.ProjectID = strings.TrimSpace(scheduler.ProjectID)
	scheduler.SchedulerID = strings.TrimSpace(scheduler.SchedulerID)
	scheduler.AgentName = strings.TrimSpace(scheduler.AgentName)
	scheduler.ManagedLoaderID = strings.TrimSpace(scheduler.ManagedLoaderID)
	scheduler.SpecJSON = strings.TrimSpace(scheduler.SpecJSON)
	if scheduler.ProjectID == "" || scheduler.AgentName == "" {
		return domain.ProjectSchedulerRecord{}, fmt.Errorf("project id and agent name are required")
	}
	if scheduler.SchedulerID == "" {
		schedulerID, err := domain.StableProjectSchedulerID(scheduler.ProjectID, scheduler.AgentName, "")
		if err != nil {
			return domain.ProjectSchedulerRecord{}, err
		}
		scheduler.SchedulerID = schedulerID
	}
	if scheduler.ID == "" {
		scheduler.ID = scheduler.SchedulerID
	}
	if scheduler.SchedulerID == "" {
		scheduler.SchedulerID = scheduler.ID
	}
	if scheduler.ShortID == "" {
		scheduler.ShortID = identity.ShortID(scheduler.ID)
	}
	if scheduler.ManagedLoaderID == "" {
		loaderID, err := domain.StableManagedLoaderID(scheduler.ProjectID, scheduler.AgentName, "")
		if err != nil {
			return domain.ProjectSchedulerRecord{}, err
		}
		scheduler.ManagedLoaderID = loaderID
	}
	if scheduler.Revision < 0 {
		return domain.ProjectSchedulerRecord{}, fmt.Errorf("project scheduler revision cannot be negative")
	}
	if scheduler.TriggerCount < 0 {
		return domain.ProjectSchedulerRecord{}, fmt.Errorf("project scheduler trigger count cannot be negative")
	}
	if scheduler.SpecJSON == "" {
		scheduler.SpecJSON = "{}"
	}
	if !json.Valid([]byte(scheduler.SpecJSON)) {
		return domain.ProjectSchedulerRecord{}, fmt.Errorf("project scheduler spec_json must be valid JSON")
	}
	return scheduler, nil
}

func NormalizeRunRecord(run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	run.RunID = strings.TrimSpace(run.RunID)
	run.ProjectID = strings.TrimSpace(run.ProjectID)
	run.ProjectName = strings.TrimSpace(run.ProjectName)
	run.AgentName = strings.TrimSpace(run.AgentName)
	run.ManagedAgentID = strings.TrimSpace(run.ManagedAgentID)
	run.Source = strings.TrimSpace(run.Source)
	run.SchedulerID = strings.TrimSpace(run.SchedulerID)
	run.TriggerID = strings.TrimSpace(run.TriggerID)
	run.Status = NormalizeRunStatus(run.Status)
	run.SessionID = strings.TrimSpace(run.SessionID)
	run.ResultJSON = strings.TrimSpace(run.ResultJSON)
	run.LogsPath = strings.TrimSpace(run.LogsPath)
	run.ArtifactsDir = strings.TrimSpace(run.ArtifactsDir)
	run.Driver = strings.TrimSpace(run.Driver)
	run.ImageRef = strings.TrimSpace(run.ImageRef)
	if run.RunID == "" || run.ProjectID == "" {
		return domain.ProjectRunRecord{}, fmt.Errorf("project run id and project id are required")
	}
	if run.ProjectRevision < 0 {
		return domain.ProjectRunRecord{}, fmt.Errorf("project run revision cannot be negative")
	}
	if run.ResultJSON == "" {
		run.ResultJSON = "{}"
	}
	if !json.Valid([]byte(run.ResultJSON)) {
		return domain.ProjectRunRecord{}, fmt.Errorf("project run result_json must be valid JSON")
	}
	return run, nil
}

func NormalizeRunStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case domain.ProjectRunStatusPending:
		return domain.ProjectRunStatusPending
	case domain.ProjectRunStatusRunning:
		return domain.ProjectRunStatusRunning
	case domain.ProjectRunStatusSucceeded:
		return domain.ProjectRunStatusSucceeded
	case domain.ProjectRunStatusFailed:
		return domain.ProjectRunStatusFailed
	case domain.ProjectRunStatusCanceled:
		return domain.ProjectRunStatusCanceled
	default:
		return domain.ProjectRunStatusPending
	}
}

func NormalizeRunStatusFilter(statuses []string) []string {
	seen := make(map[string]struct{}, len(statuses))
	normalized := make([]string, 0, len(statuses))
	for _, status := range statuses {
		status = strings.ToLower(strings.TrimSpace(status))
		if status == "" {
			continue
		}
		switch status {
		case domain.ProjectRunStatusPending, domain.ProjectRunStatusRunning, domain.ProjectRunStatusSucceeded, domain.ProjectRunStatusFailed, domain.ProjectRunStatusCanceled:
		default:
			continue
		}
		if _, ok := seen[status]; ok {
			continue
		}
		seen[status] = struct{}{}
		normalized = append(normalized, status)
	}
	return normalized
}

func RecordMatchesQuery(item domain.ProjectRecord, query string) bool {
	return strings.Contains(strings.ToLower(item.ID), query) ||
		strings.Contains(strings.ToLower(item.Name), query) ||
		strings.Contains(strings.ToLower(item.SourcePath), query)
}
