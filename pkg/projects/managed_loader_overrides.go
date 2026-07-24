package projects

import (
	"fmt"
	"strings"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

type legacyManagedLoaderOverride struct {
	Loader           domain.Loader
	BaselineAgentEnv []domain.SandboxEnvVar
	BaselineKnown    bool
}

// applyManagedLoaderOverrideBuilds adopts legacy loader identities at the
// project artifact boundary. Ordinary project specs never populate overrides.
func applyManagedLoaderOverrideBuilds(project domain.ProjectRecord, revision int64, builds []SchedulerBuild, overrides map[string]legacyManagedLoaderOverride) ([]SchedulerBuild, error) {
	if len(overrides) == 0 {
		return builds, nil
	}
	for index := range builds {
		override, ok := overrides[builds[index].Scheduler.AgentName]
		if !ok {
			continue
		}
		loaderID := strings.TrimSpace(override.Loader.Summary.ID)
		if loaderID == "" {
			return nil, fmt.Errorf("legacy loader for agent %s has no id", builds[index].Scheduler.AgentName)
		}

		loader := mergeManagedLoaderOverride(builds[index].Loader, override)
		loader.Summary.ManagedProjectID = project.ID
		loader.Summary.ManagedRevision = revision
		loader.Summary.ManagedAgentName = builds[index].Scheduler.AgentName
		loader.Summary.ManagedSchedulerID = builds[index].Scheduler.SchedulerID

		builds[index].Scheduler.ManagedLoaderID = loaderID
		builds[index].Scheduler.Enabled = loader.Summary.Enabled
		builds[index].Scheduler.TriggerCount = len(loader.Triggers)
		builds[index].Loader = loader
		builds[index].ValidationTriggers = append([]domain.LoaderTrigger(nil), loader.Triggers...)
	}
	return builds, nil
}

func mergeManagedLoaderOverride(current domain.Loader, override legacyManagedLoaderOverride) domain.Loader {
	loader := loaders.CloneLoader(current)
	loader.Summary.ID = override.Loader.Summary.ID
	// Keep the compiled managed Agent binding so ProjectSpec Agent changes are
	// used at runtime. The legacy Loader identity and task-local state remain
	// attached to the adopted Loader below.
	loader.Summary.WorkspaceID = override.Loader.Summary.WorkspaceID
	loader.Summary.CreatedAt = override.Loader.Summary.CreatedAt
	loader.Summary.UpdatedAt = override.Loader.Summary.UpdatedAt
	loader.Summary.LastError = override.Loader.Summary.LastError
	loader.Summary.RunCount = override.Loader.Summary.RunCount
	loader.Summary.EventCount = override.Loader.Summary.EventCount
	loader.Summary.LatestRunAt = override.Loader.Summary.LatestRunAt
	loader.Summary.CapsetIDs = append([]string(nil), current.Summary.CapsetIDs...)
	loader.Volumes = append([]domain.VolumeMountSpec(nil), current.Volumes...)
	loader.EnvItems = mergeLegacyManagedLoaderEnv(current.EnvItems, override)

	previousTriggers := make(map[string]domain.LoaderTrigger, len(override.Loader.Triggers))
	for _, trigger := range override.Loader.Triggers {
		previousTriggers[trigger.ID] = trigger
	}
	for index := range loader.Triggers {
		loader.Triggers[index].LoaderID = loader.Summary.ID
		previous, ok := previousTriggers[loader.Triggers[index].ID]
		if !ok {
			continue
		}
		loader.Triggers[index].Enabled = previous.Enabled
		loader.Triggers[index].NextFireAt = previous.NextFireAt
		loader.Triggers[index].LastFiredAt = previous.LastFiredAt
	}
	return loader
}

func mergeLegacyManagedLoaderEnv(candidateAgentEnv []domain.SandboxEnvVar, override legacyManagedLoaderOverride) []domain.SandboxEnvVar {
	loaderEnv := domain.NormalizeEnvItems(override.Loader.EnvItems)
	if !override.BaselineKnown {
		return loaderEnv
	}
	// Loader env is the higher-priority compatibility layer. Preserve it only
	// when the incoming ProjectSpec left the corresponding Agent env key
	// unchanged; an explicit Agent change must be allowed to take effect.
	baselineByName := sandboxEnvItemsByName(override.BaselineAgentEnv)
	candidateByName := sandboxEnvItemsByName(candidateAgentEnv)
	preserved := make([]domain.SandboxEnvVar, 0, len(loaderEnv))
	for _, item := range loaderEnv {
		baseline, baselineExists := baselineByName[item.Name]
		candidate, candidateExists := candidateByName[item.Name]
		if baselineExists == candidateExists && (!baselineExists || baseline == candidate) {
			preserved = append(preserved, item)
		}
	}
	return preserved
}

func sandboxEnvItemsByName(items []domain.SandboxEnvVar) map[string]domain.SandboxEnvVar {
	normalized := domain.NormalizeEnvItems(items)
	byName := make(map[string]domain.SandboxEnvVar, len(normalized))
	for _, item := range normalized {
		byName[item.Name] = item
	}
	return byName
}

func applyManagedLoaderOverrides(project domain.ProjectRecord, revision int64, schedulers []domain.ProjectSchedulerRecord, managedLoaders []domain.Loader, overrides map[string]legacyManagedLoaderOverride) ([]domain.ProjectSchedulerRecord, []domain.Loader, error) {
	if len(overrides) == 0 {
		return schedulers, managedLoaders, nil
	}
	if len(schedulers) != len(managedLoaders) {
		return nil, nil, fmt.Errorf("project scheduler and managed loader counts differ")
	}
	builds := make([]SchedulerBuild, 0, len(schedulers))
	for index := range schedulers {
		builds = append(builds, SchedulerBuild{Scheduler: schedulers[index], Loader: managedLoaders[index]})
	}
	builds, err := applyManagedLoaderOverrideBuilds(project, revision, builds, overrides)
	if err != nil {
		return nil, nil, err
	}
	return SchedulerRecords(builds), SchedulerLoaders(builds), nil
}

func syncProjectAgentSchedulerState(agents []domain.ProjectAgentRecord, schedulers []domain.ProjectSchedulerRecord) {
	enabledByAgent := make(map[string]bool, len(schedulers))
	for _, scheduler := range schedulers {
		enabledByAgent[scheduler.AgentName] = scheduler.Enabled
	}
	for index := range agents {
		if enabled, ok := enabledByAgent[agents[index].AgentName]; ok {
			agents[index].SchedulerEnabled = enabled
		}
	}
}
