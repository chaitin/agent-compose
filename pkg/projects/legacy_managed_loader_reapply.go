package projects

import (
	"context"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
)

// preserveLegacyManagedLoaderIdentities keeps adopted v1 loader IDs attached
// when the synthetic project is edited through the ordinary v2 ApplyProject
// API. The initial migration already carries explicit overrides.
func (c *Controller) preserveLegacyManagedLoaderIdentities(ctx context.Context, project domain.ProjectRecord, normalized NormalizedProject) (NormalizedProject, error) {
	if len(normalized.managedLoaderOverrides) != 0 || !IsLegacyDefaultProject(project) || normalized.Spec == nil {
		return normalized, nil
	}
	store, ok := c.store.(ReconcileSchedulerStore)
	if !ok {
		return NormalizedProject{}, fmt.Errorf("scheduler store is required")
	}
	schedulers, err := store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return NormalizedProject{}, fmt.Errorf("list existing schedulers: %w", err)
	}
	declared := make(map[string]struct{}, len(normalized.Spec.Agents))
	for _, agent := range normalized.Spec.Agents {
		if agent.Scheduler != nil {
			declared[agent.Name] = struct{}{}
		}
	}
	overrides := make(map[string]domain.Loader, len(schedulers))
	for _, scheduler := range schedulers {
		if _, ok := declared[scheduler.AgentName]; !ok || strings.TrimSpace(scheduler.ManagedLoaderID) == "" {
			continue
		}
		loader, found, err := store.GetLoaderIfExists(ctx, scheduler.ManagedLoaderID)
		if err != nil {
			return NormalizedProject{}, fmt.Errorf("load scheduler %s loader %s: %w", scheduler.SchedulerID, scheduler.ManagedLoaderID, err)
		}
		if !found || !managedLoaderMatchesProjectScheduler(loader, project.ID, scheduler) {
			continue
		}
		overrides[scheduler.AgentName] = loader
	}
	if len(overrides) != 0 {
		normalized.managedLoaderOverrides = overrides
	}
	return normalized, nil
}

func managedLoaderMatchesProjectScheduler(loader domain.Loader, projectID string, scheduler domain.ProjectSchedulerRecord) bool {
	return strings.TrimSpace(loader.Summary.ManagedProjectID) == strings.TrimSpace(projectID) &&
		strings.TrimSpace(loader.Summary.ManagedAgentName) == strings.TrimSpace(scheduler.AgentName) &&
		strings.TrimSpace(loader.Summary.ManagedSchedulerID) == strings.TrimSpace(scheduler.SchedulerID)
}
