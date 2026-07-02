package agentcompose

import (
	"context"

	projectdomain "agent-compose/internal/agentcompose/project"
	"agent-compose/pkg/compose"
)

type projectManagedSchedulerBuild = projectdomain.ManagedSchedulerBuild

func projectAgentRecordsFromSpec(projectID string, revision int64, spec *compose.NormalizedProjectSpec) ([]ProjectAgentRecord, error) {
	return projectdomain.AgentRecordsFromSpec(projectID, revision, spec)
}

func projectManagedAgentDefinitionsFromSpec(project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]AgentDefinition, error) {
	return projectdomain.ManagedAgentDefinitionsFromSpec(project, revision, spec)
}

func projectManagedAgentDefinitionFromSpec(project ProjectRecord, revision int64, agent compose.NormalizedAgentSpec) (AgentDefinition, error) {
	return projectdomain.ManagedAgentDefinitionFromSpec(project, revision, agent)
}

func sessionEnvItemsFromCompose(values map[string]compose.EnvVarSpec) []SessionEnvVar {
	return projectdomain.SessionEnvItemsFromCompose(values)
}

func projectManagedSchedulerRecords(builds []projectManagedSchedulerBuild) []ProjectSchedulerRecord {
	return projectdomain.ManagedSchedulerRecords(builds)
}

func projectManagedSchedulerLoaders(builds []projectManagedSchedulerBuild) []Loader {
	return projectdomain.ManagedSchedulerLoaders(builds)
}

func projectManagedSchedulerBuildsFromSpec(project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]projectManagedSchedulerBuild, error) {
	return projectdomain.ManagedSchedulerBuildsFromSpec(context.Background(), project, revision, spec, nil)
}

func projectManagedLoaderFromScheduler(project ProjectRecord, scheduler ProjectSchedulerRecord, agent compose.NormalizedAgentSpec) (Loader, error) {
	return projectdomain.ManagedLoaderFromScheduler(project, scheduler, agent)
}

func projectManagedLoaderTriggersAndScript(projectID, agentName, schedulerName string, scheduler *compose.NormalizedSchedulerSpec) ([]LoaderTrigger, string, error) {
	triggerBuilds, script, err := projectdomain.ManagedLoaderTriggersAndScript(projectID, agentName, schedulerName, scheduler)
	if err != nil {
		return nil, "", err
	}
	triggers := make([]LoaderTrigger, 0, len(triggerBuilds))
	for _, trigger := range triggerBuilds {
		triggers = append(triggers, loaderTriggerFromProjectBuild(trigger))
	}
	return triggers, script, nil
}

func projectManagedLoaderTriggerAndRegistration(id, agentName string, trigger compose.NormalizedTriggerSpec) (LoaderTrigger, string, error) {
	triggerBuild, err := projectdomain.ManagedLoaderTriggerAndRegistration(id, agentName, trigger)
	if err != nil {
		return LoaderTrigger{}, "", err
	}
	return loaderTriggerFromProjectBuild(triggerBuild), triggerBuild.Registration, nil
}

func loaderTriggerFromProjectBuild(trigger projectdomain.SchedulerTriggerBuild) LoaderTrigger {
	return projectdomain.LoaderTriggerFromSchedulerBuild(trigger)
}

func jsStringLiteral(value string) string {
	return projectdomain.JSStringLiteral(value)
}
