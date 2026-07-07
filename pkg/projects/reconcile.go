package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
)

const (
	ChangeActionCreated   = "created"
	ChangeActionUpdated   = "updated"
	ChangeActionRemoved   = "removed"
	ChangeActionUnchanged = "unchanged"
)

type Change struct {
	Action       string
	ResourceType string
	ResourceID   string
	Name         string
	Message      string
}

type ReconcileAgentDefinitionStore interface {
	GetAgentDefinitionIfExists(ctx context.Context, id string, includeDeleted bool) (domain.AgentDefinition, bool, error)
	UpsertManagedAgentDefinition(ctx context.Context, item domain.AgentDefinition) (domain.AgentDefinition, error)
	ListManagedAgentDefinitions(ctx context.Context, projectID string, includeDeleted bool) ([]domain.AgentDefinition, error)
	SetAgentDefinitionEnabled(ctx context.Context, id string, enabled bool) (domain.AgentDefinition, error)
}

type ReconcileSchedulerStore interface {
	GetProjectScheduler(ctx context.Context, projectID, schedulerID string) (domain.ProjectSchedulerRecord, error)
	UpsertProjectScheduler(ctx context.Context, scheduler domain.ProjectSchedulerRecord) (domain.ProjectSchedulerRecord, error)
	SetProjectSchedulerEnabled(ctx context.Context, projectID, schedulerID string, enabled bool) (domain.ProjectSchedulerRecord, error)
	ListProjectSchedulers(ctx context.Context, projectID string) ([]domain.ProjectSchedulerRecord, error)
	GetSchedulerExecutionIfExists(ctx context.Context, executionID string) (domain.Loader, bool, error)
	UpsertSchedulerExecution(ctx context.Context, item domain.Loader) (domain.Loader, error)
	ReplaceSchedulerExecutionTriggers(ctx context.Context, executionID string, triggers []domain.LoaderTrigger) ([]domain.LoaderTrigger, error)
	SetSchedulerExecutionEnabled(ctx context.Context, executionID string, enabled bool) error
}

type ReconcileSchedulerOptions struct {
	CleanupFailedManagedScheduler    func(ctx context.Context, scheduler domain.ProjectSchedulerRecord, executionID string)
	DisableSchedulerExecutionIfOwned func(ctx context.Context, executionID, projectID, schedulerID string) error
	RefreshSchedulerExecutions       func(ctx context.Context) error
}

func ReconcileManagedAgentDefinitions(ctx context.Context, store ReconcileAgentDefinitionStore, project domain.ProjectRecord, current []domain.AgentDefinition) ([]Change, bool, error) {
	if store == nil {
		return nil, false, fmt.Errorf("config store is required")
	}
	currentByID := make(map[string]domain.AgentDefinition, len(current))
	for _, agent := range current {
		currentByID[agent.ID] = agent
	}
	changes := make([]Change, 0, len(current))
	unchanged := true
	for _, agent := range current {
		existing, found, err := store.GetAgentDefinitionIfExists(ctx, agent.ID, true)
		if err != nil {
			return nil, false, fmt.Errorf("load managed agent definition %s: %w", agent.ID, err)
		}
		saved, err := store.UpsertManagedAgentDefinition(ctx, agent)
		if err != nil {
			return nil, false, fmt.Errorf("upsert managed agent definition %s: %w", agent.ID, err)
		}
		action := ManagedAgentDefinitionChangeAction(existing, found, agent)
		if action != ChangeActionUnchanged {
			unchanged = false
		}
		changes = append(changes, Change{
			Action:       action,
			ResourceType: "agent_definition",
			ResourceID:   saved.ID,
			Name:         saved.Name,
		})
	}

	existingManaged, err := store.ListManagedAgentDefinitions(ctx, project.ID, false)
	if err != nil {
		return nil, false, fmt.Errorf("list managed agent definitions: %w", err)
	}
	for _, existing := range existingManaged {
		if _, ok := currentByID[existing.ID]; ok {
			continue
		}
		if !existing.Enabled {
			continue
		}
		disabled, err := store.SetAgentDefinitionEnabled(ctx, existing.ID, false)
		if err != nil {
			return nil, false, fmt.Errorf("disable removed managed agent definition %s: %w", existing.ID, err)
		}
		unchanged = false
		changes = append(changes, Change{
			Action:       ChangeActionUpdated,
			ResourceType: "agent_definition",
			ResourceID:   disabled.ID,
			Name:         disabled.Name,
			Message:      "disabled because the agent is no longer present in the project spec",
		})
	}
	return changes, unchanged, nil
}

func ReconcileManagedSchedulers(ctx context.Context, store ReconcileSchedulerStore, project domain.ProjectRecord, schedulers []domain.ProjectSchedulerRecord, executions []domain.Loader, options ReconcileSchedulerOptions) ([]Change, bool, error) {
	if store == nil {
		return nil, false, fmt.Errorf("config store is required")
	}
	currentByID := make(map[string]domain.ProjectSchedulerRecord, len(schedulers))
	executionsByID := make(map[string]domain.Loader, len(executions))
	for _, execution := range executions {
		executionsByID[execution.Summary.ID] = execution
	}
	changes := make([]Change, 0, len(schedulers)+len(executions))
	unchanged := true
	for _, scheduler := range schedulers {
		currentByID[scheduler.SchedulerID] = scheduler
		existing, found, err := projectSchedulerIfExists(ctx, store, scheduler.ProjectID, scheduler.SchedulerID)
		if err != nil {
			return changes, false, fmt.Errorf("load project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
		}
		stagedScheduler := scheduler
		stagedScheduler.Enabled = false
		saved, err := store.UpsertProjectScheduler(ctx, stagedScheduler)
		if err != nil {
			return changes, false, fmt.Errorf("stage project scheduler %s/%s disabled: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
		}

		execution, ok := executionsByID[saved.ManagedLoaderID]
		if !ok {
			return changes, false, fmt.Errorf("scheduler execution %s for scheduler %s missing", saved.ManagedLoaderID, saved.SchedulerID)
		}
		existingExecution, executionFound, err := store.GetSchedulerExecutionIfExists(ctx, execution.Summary.ID)
		if err != nil {
			return changes, false, fmt.Errorf("load scheduler execution %s: %w", execution.Summary.ID, err)
		}
		stagedExecution := execution
		stagedExecution.Summary.Enabled = false
		savedExecution, err := store.UpsertSchedulerExecution(ctx, stagedExecution)
		if err != nil {
			return changes, false, fmt.Errorf("stage scheduler execution %s disabled: %w", execution.Summary.ID, err)
		}
		if _, err := store.ReplaceSchedulerExecutionTriggers(ctx, savedExecution.Summary.ID, execution.Triggers); err != nil {
			cleanupFailedManagedScheduler(ctx, options, saved, savedExecution.Summary.ID)
			return changes, false, fmt.Errorf("replace scheduler execution triggers %s: %w", savedExecution.Summary.ID, err)
		}
		if execution.Summary.Enabled {
			if err := store.SetSchedulerExecutionEnabled(ctx, savedExecution.Summary.ID, true); err != nil {
				cleanupFailedManagedScheduler(ctx, options, saved, savedExecution.Summary.ID)
				return changes, false, fmt.Errorf("enable scheduler execution %s: %w", savedExecution.Summary.ID, err)
			}
		} else if err := store.SetSchedulerExecutionEnabled(ctx, savedExecution.Summary.ID, false); err != nil {
			return changes, false, fmt.Errorf("disable scheduler execution %s: %w", savedExecution.Summary.ID, err)
		}
		if scheduler.Enabled {
			saved, err = store.SetProjectSchedulerEnabled(ctx, scheduler.ProjectID, scheduler.SchedulerID, true)
			if err != nil {
				cleanupFailedManagedScheduler(ctx, options, stagedScheduler, savedExecution.Summary.ID)
				return changes, false, fmt.Errorf("enable project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
			}
		} else {
			saved = stagedScheduler
		}
		action := SchedulerChangeAction(existing, found, scheduler)
		if action != ChangeActionUnchanged {
			unchanged = false
		}
		changes = append(changes, Change{
			Action:       action,
			ResourceType: "project_scheduler",
			ResourceID:   saved.SchedulerID,
			Name:         saved.AgentName,
		})
		executionAction := SchedulerExecutionChangeAction(existingExecution, executionFound, execution)
		if executionAction != ChangeActionUnchanged {
			unchanged = false
		}
		changes = append(changes, Change{
			Action:       executionAction,
			ResourceType: "loader",
			ResourceID:   savedExecution.Summary.ID,
			Name:         savedExecution.Summary.Name,
		})
	}
	existingSchedulers, err := store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return changes, false, fmt.Errorf("list project schedulers: %w", err)
	}
	for _, existing := range existingSchedulers {
		if _, ok := currentByID[existing.SchedulerID]; ok {
			continue
		}
		if !existing.Enabled {
			continue
		}
		disabled, err := store.SetProjectSchedulerEnabled(ctx, existing.ProjectID, existing.SchedulerID, false)
		if err != nil {
			return changes, false, fmt.Errorf("disable removed project scheduler %s/%s: %w", existing.ProjectID, existing.SchedulerID, err)
		}
		if options.DisableSchedulerExecutionIfOwned != nil {
			if err := options.DisableSchedulerExecutionIfOwned(ctx, existing.ManagedLoaderID, project.ID, existing.SchedulerID); err != nil {
				return changes, false, fmt.Errorf("disable removed scheduler execution %s: %w", existing.ManagedLoaderID, err)
			}
		}
		unchanged = false
		changes = append(changes, Change{
			Action:       ChangeActionRemoved,
			ResourceType: "project_scheduler",
			ResourceID:   disabled.SchedulerID,
			Name:         disabled.AgentName,
			Message:      "disabled because the scheduler is no longer present in the project spec",
		}, Change{
			Action:       ChangeActionRemoved,
			ResourceType: "loader",
			ResourceID:   existing.ManagedLoaderID,
			Name:         existing.AgentName,
			Message:      "disabled because the scheduler is no longer present in the project spec",
		})
	}
	if options.RefreshSchedulerExecutions != nil {
		if err := options.RefreshSchedulerExecutions(ctx); err != nil {
			return changes, false, fmt.Errorf("refresh scheduler execution manager: %w", err)
		}
	}
	return changes, unchanged, nil
}

func cleanupFailedManagedScheduler(ctx context.Context, options ReconcileSchedulerOptions, scheduler domain.ProjectSchedulerRecord, executionID string) {
	if options.CleanupFailedManagedScheduler != nil {
		options.CleanupFailedManagedScheduler(ctx, scheduler, executionID)
	}
}

func projectSchedulerIfExists(ctx context.Context, store ReconcileSchedulerStore, projectID, schedulerID string) (domain.ProjectSchedulerRecord, bool, error) {
	scheduler, err := store.GetProjectScheduler(ctx, projectID, schedulerID)
	if err == nil {
		return scheduler, true, nil
	}
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ProjectSchedulerRecord{}, false, nil
	}
	return domain.ProjectSchedulerRecord{}, false, err
}

func ManagedAgentDefinitionChangeAction(existing domain.AgentDefinition, found bool, current domain.AgentDefinition) string {
	if !found {
		return ChangeActionCreated
	}
	if !existing.DeletedAt.IsZero() || !existing.Enabled {
		return ChangeActionUpdated
	}
	if ManagedAgentDefinitionUnchanged(existing, current) {
		return ChangeActionUnchanged
	}
	return ChangeActionUpdated
}

func SchedulerChangeAction(existing domain.ProjectSchedulerRecord, found bool, current domain.ProjectSchedulerRecord) string {
	if !found {
		return ChangeActionCreated
	}
	if SchedulerRecordUnchanged(existing, current) {
		return ChangeActionUnchanged
	}
	return ChangeActionUpdated
}

func SchedulerExecutionChangeAction(existing domain.Loader, found bool, current domain.Loader) string {
	if !found {
		return ChangeActionCreated
	}
	if SchedulerExecutionUnchanged(existing, current) {
		return ChangeActionUnchanged
	}
	return ChangeActionUpdated
}

func ProjectAgentChangeAction(existing domain.ProjectAgentRecord, found bool, current domain.ProjectAgentRecord) string {
	if !found {
		return ChangeActionCreated
	}
	if ProjectAgentRecordUnchanged(existing, current) {
		return ChangeActionUnchanged
	}
	return ChangeActionUpdated
}

func DisableSchedulerExecutionIfOwned(ctx context.Context, store ReconcileSchedulerStore, executionID, projectID, schedulerID string) error {
	executionID = strings.TrimSpace(executionID)
	if executionID == "" {
		return nil
	}
	execution, found, err := store.GetSchedulerExecutionIfExists(ctx, executionID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if execution.Summary.ManagedProjectID != strings.TrimSpace(projectID) || execution.Summary.ManagedSchedulerID != strings.TrimSpace(schedulerID) {
		return nil
	}
	if !execution.Summary.Enabled {
		return nil
	}
	return store.SetSchedulerExecutionEnabled(ctx, executionID, false)
}
