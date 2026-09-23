package projects

import (
	"encoding/json"
	"fmt"
	"github.com/chaitin/agent-compose/pkg/identity"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/projectdef"
)

func NormalizeRecord(project domain.ProjectRecord) (domain.ProjectRecord, error) {
	project.ID = strings.TrimSpace(project.ID)
	project.Name = strings.TrimSpace(project.Name)
	project.ShortID = strings.TrimSpace(project.ShortID)
	project.SourcePath = NormalizeProjectSourcePath(project.SourcePath)
	project.SourceJSON = strings.TrimSpace(project.SourceJSON)
	project.SpecHash = strings.TrimSpace(project.SpecHash)
	if project.ID == "" {
		return domain.ProjectRecord{}, fmt.Errorf("project id is required")
	}
	if project.Name == "" {
		return domain.ProjectRecord{}, fmt.Errorf("project name is required")
	}
	if !projectdef.IsProjectName(project.Name) {
		return domain.ProjectRecord{}, fmt.Errorf("project name %q must match %s", project.Name, projectdef.ProjectNamePattern)
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
	if agent.ID == "" {
		agentID, err := StableProjectAgentID(agent.ProjectID, agent.AgentName)
		if err != nil {
			return domain.ProjectAgentRecord{}, err
		}
		agent.ID = agentID
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
	scheduler.SpecJSON = strings.TrimSpace(scheduler.SpecJSON)
	if scheduler.ProjectID == "" || scheduler.AgentName == "" {
		return domain.ProjectSchedulerRecord{}, fmt.Errorf("project id and agent name are required")
	}
	if scheduler.ID == "" {
		scheduler.ID = scheduler.SchedulerID
	}
	if scheduler.ID == "" {
		schedulerID, err := StableProjectSchedulerID(scheduler.ProjectID, scheduler.AgentName, "")
		if err != nil {
			return domain.ProjectSchedulerRecord{}, err
		}
		scheduler.ID = schedulerID
	}
	// SchedulerID is the public name for the native scheduler identity. Keep the
	// two model fields aligned while the database retains its compatibility
	// scheduler_id column.
	scheduler.SchedulerID = scheduler.ID
	if scheduler.ShortID == "" {
		scheduler.ShortID = identity.ShortID(scheduler.ID)
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
	run.AgentID = strings.TrimSpace(run.AgentID)
	run.Source = strings.TrimSpace(run.Source)
	run.SchedulerID = strings.TrimSpace(run.SchedulerID)
	run.SchedulerRunID = strings.TrimSpace(run.SchedulerRunID)
	run.TriggerID = strings.TrimSpace(run.TriggerID)
	run.Status = NormalizeRunStatus(run.Status)
	run.SandboxID = strings.TrimSpace(run.SandboxID)
	run.ResultJSON = strings.TrimSpace(run.ResultJSON)
	run.LogsPath = strings.TrimSpace(run.LogsPath)
	run.ArtifactsDir = strings.TrimSpace(run.ArtifactsDir)
	run.CleanupPolicy = NormalizeRunCleanupPolicy(run.CleanupPolicy)
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

func NormalizeRunCleanupPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case domain.ProjectRunCleanupKeepRunning:
		return domain.ProjectRunCleanupKeepRunning
	case domain.ProjectRunCleanupRemoveOnCompletion:
		return domain.ProjectRunCleanupRemoveOnCompletion
	default:
		return domain.ProjectRunCleanupStopOnCompletion
	}
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
