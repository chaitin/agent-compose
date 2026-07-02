package run

import (
	"fmt"
	"strings"

	projectdomain "agent-compose/internal/agentcompose/project"
	"agent-compose/pkg/agentcompose/domain"
)

type Tag struct {
	Name  string
	Value string
}

func SessionTitle(run projectdomain.ProjectRunRecord) string {
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

func SessionTags(run projectdomain.ProjectRunRecord) []Tag {
	tags := []Tag{
		{Name: "project", Value: strings.TrimSpace(run.ProjectID)},
		{Name: "agent", Value: strings.TrimSpace(run.AgentName)},
		{Name: "run_id", Value: strings.TrimSpace(run.RunID)},
		{Name: "source", Value: domain.NormalizeProjectRunSource(run.Source)},
	}
	if schedulerID := strings.TrimSpace(run.SchedulerID); schedulerID != "" {
		tags = append(tags, Tag{Name: "scheduler_id", Value: schedulerID})
	}
	return tags
}

func MergeTags(existing, additions []Tag) []Tag {
	result := append([]Tag(nil), existing...)
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
