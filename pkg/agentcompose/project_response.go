package agentcompose

import (
	"time"

	projectdomain "agent-compose/internal/agentcompose/project"
	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func projectResponse(project ProjectRecord, spec *agentcomposev2.ProjectSpec, agents []ProjectAgentRecord, schedulers []ProjectSchedulerRecord) *agentcomposev2.Project {
	return projectdomain.ProjectResponse(project, spec, agents, schedulers)
}

func projectSummaryResponse(project ProjectRecord, agents []ProjectAgentRecord, schedulers []ProjectSchedulerRecord) *agentcomposev2.ProjectSummary {
	return projectdomain.ProjectSummaryResponse(project, agents, schedulers)
}

func projectRevisionResponse(revision ProjectRevisionRecord, spec *agentcomposev2.ProjectSpec) *agentcomposev2.ProjectRevision {
	return projectdomain.ProjectRevisionResponse(revision, spec)
}

func projectAgentResponses(agents []ProjectAgentRecord) []*agentcomposev2.ProjectAgent {
	return projectdomain.ProjectAgentResponses(agents)
}

func projectSchedulerResponses(schedulers []ProjectSchedulerRecord) []*agentcomposev2.ProjectScheduler {
	return projectdomain.ProjectSchedulerResponses(schedulers)
}

// ProjectSpecResponse converts a normalized compose spec into the v2 ProjectSpec API shape.
func ProjectSpecResponse(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	return projectdomain.ProjectSpecResponse(spec)
}

func formatProjectTime(value time.Time) string {
	return projectdomain.FormatTime(value)
}
