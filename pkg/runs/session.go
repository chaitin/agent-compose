package runs

import (
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
)

type SessionResult struct {
	Session  *domain.Session
	Created  bool
	Warnings []string
}

func SessionTitle(run domain.ProjectRunRecord) string {
	project := strings.TrimSpace(run.ProjectName)
	if project == "" {
		project = strings.TrimSpace(run.ProjectID)
	}
	agent := strings.TrimSpace(run.AgentName)
	if agent == "" {
		agent = "agent"
	}
	return strings.TrimSpace(fmt.Sprintf("%s/%s run", project, agent))
}

func SessionTags(run domain.ProjectRunRecord) []domain.SessionTag {
	tags := []domain.SessionTag{
		{Name: "project", Value: strings.TrimSpace(run.ProjectID)},
		{Name: "agent", Value: strings.TrimSpace(run.AgentName)},
		{Name: "run_id", Value: strings.TrimSpace(run.RunID)},
		{Name: "source", Value: NormalizeSource(run.Source)},
	}
	if schedulerID := strings.TrimSpace(run.SchedulerID); schedulerID != "" {
		tags = append(tags, domain.SessionTag{Name: "scheduler_id", Value: schedulerID})
	}
	return tags
}

func MergeSessionTags(existing, additions []domain.SessionTag) []domain.SessionTag {
	result := append([]domain.SessionTag(nil), existing...)
	for _, addition := range additions {
		addition.Name = strings.TrimSpace(addition.Name)
		addition.Value = strings.TrimSpace(addition.Value)
		if addition.Name == "" {
			continue
		}
		found := false
		for _, current := range result {
			if strings.TrimSpace(current.Name) == addition.Name && strings.TrimSpace(current.Value) == addition.Value {
				found = true
				break
			}
		}
		if !found {
			result = append(result, addition)
		}
	}
	return result
}

func WorkspaceID(run domain.ProjectRunRecord, provider string) string {
	return domain.StableReadableID("workspace", run.AgentName+"-"+provider, run.RunID+"|workspace|"+provider)
}

func WorkspaceName(run domain.ProjectRunRecord, provider string) string {
	name := strings.TrimSpace(run.ProjectName)
	if name == "" {
		name = strings.TrimSpace(run.ProjectID)
	}
	agent := strings.TrimSpace(run.AgentName)
	if agent == "" {
		agent = "agent"
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s %s run workspace", name, agent, provider))
}
