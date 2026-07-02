package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	capabilitydomain "agent-compose/internal/agentcompose/capability"
	loaderdomain "agent-compose/internal/agentcompose/loader"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ManagedSchedulerStore interface {
	GetProjectScheduler(ctx context.Context, projectID, schedulerID string) (ProjectSchedulerRecord, error)
	UpsertProjectScheduler(ctx context.Context, scheduler ProjectSchedulerRecord) (ProjectSchedulerRecord, error)
	SetProjectSchedulerEnabled(ctx context.Context, projectID, schedulerID string, enabled bool) (ProjectSchedulerRecord, error)
	ListProjectSchedulers(ctx context.Context, projectID string) ([]ProjectSchedulerRecord, error)

	GetLoader(ctx context.Context, loaderID string) (loaderdomain.Definition, error)
	UpsertManagedLoader(ctx context.Context, loader loaderdomain.Definition) (loaderdomain.Definition, error)
	ReplaceLoaderTriggers(ctx context.Context, loaderID string, triggers []loaderdomain.Trigger) ([]loaderdomain.Trigger, error)
	SetLoaderEnabled(ctx context.Context, loaderID string, enabled bool) error
}

type LoaderRefresher interface {
	Refresh(ctx context.Context) error
}

func ReconcileManagedSchedulers(ctx context.Context, store ManagedSchedulerStore, refresher LoaderRefresher, project ProjectRecord, schedulers []ProjectSchedulerRecord, loaders []loaderdomain.Definition) ([]*agentcomposev2.ProjectChange, bool, error) {
	if store == nil {
		return nil, false, fmt.Errorf("config store is required")
	}
	currentByID := make(map[string]ProjectSchedulerRecord, len(schedulers))
	loadersByID := make(map[string]loaderdomain.Definition, len(loaders))
	for _, loader := range loaders {
		loadersByID[loader.Summary.ID] = loader
	}
	changes := make([]*agentcomposev2.ProjectChange, 0, len(schedulers)+len(loaders))
	unchanged := true
	for _, scheduler := range schedulers {
		currentByID[scheduler.SchedulerID] = scheduler
		existing, found, err := getProjectSchedulerIfExists(ctx, store, scheduler.ProjectID, scheduler.SchedulerID)
		if err != nil {
			return changes, false, fmt.Errorf("load project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
		}
		stagedScheduler := scheduler
		stagedScheduler.Enabled = false
		saved, err := store.UpsertProjectScheduler(ctx, stagedScheduler)
		if err != nil {
			return changes, false, fmt.Errorf("stage project scheduler %s/%s disabled: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
		}

		loader, ok := loadersByID[saved.ManagedLoaderID]
		if !ok {
			return changes, false, fmt.Errorf("managed loader %s for scheduler %s missing", saved.ManagedLoaderID, saved.SchedulerID)
		}
		existingLoader, loaderFound, err := getLoaderIfExists(ctx, store, loader.Summary.ID)
		if err != nil {
			return changes, false, fmt.Errorf("load managed loader %s: %w", loader.Summary.ID, err)
		}
		stagedLoader := loader
		stagedLoader.Summary.Enabled = false
		savedLoader, err := store.UpsertManagedLoader(ctx, stagedLoader)
		if err != nil {
			return changes, false, fmt.Errorf("stage managed loader %s disabled: %w", loader.Summary.ID, err)
		}
		if _, err := store.ReplaceLoaderTriggers(ctx, savedLoader.Summary.ID, loader.Triggers); err != nil {
			CleanupFailedManagedSchedulerReconcile(ctx, store, refresher, saved, savedLoader.Summary.ID)
			return changes, false, fmt.Errorf("replace managed loader triggers %s: %w", savedLoader.Summary.ID, err)
		}
		if loader.Summary.Enabled {
			if err := store.SetLoaderEnabled(ctx, savedLoader.Summary.ID, true); err != nil {
				CleanupFailedManagedSchedulerReconcile(ctx, store, refresher, saved, savedLoader.Summary.ID)
				return changes, false, fmt.Errorf("enable managed loader %s: %w", savedLoader.Summary.ID, err)
			}
		} else if err := store.SetLoaderEnabled(ctx, savedLoader.Summary.ID, false); err != nil {
			return changes, false, fmt.Errorf("disable managed loader %s: %w", savedLoader.Summary.ID, err)
		}
		if scheduler.Enabled {
			saved, err = store.SetProjectSchedulerEnabled(ctx, scheduler.ProjectID, scheduler.SchedulerID, true)
			if err != nil {
				CleanupFailedManagedSchedulerReconcile(ctx, store, refresher, stagedScheduler, savedLoader.Summary.ID)
				return changes, false, fmt.Errorf("enable project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
			}
		} else {
			saved = stagedScheduler
		}
		action := SchedulerChangeAction(existing, found, scheduler)
		if action != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED {
			unchanged = false
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       action,
			ResourceType: "project_scheduler",
			ResourceId:   saved.SchedulerID,
			Name:         saved.AgentName,
		})
		loaderAction := ManagedLoaderChangeAction(existingLoader, loaderFound, loader)
		if loaderAction != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED {
			unchanged = false
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       loaderAction,
			ResourceType: "loader",
			ResourceId:   savedLoader.Summary.ID,
			Name:         savedLoader.Summary.Name,
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
		if err := DisableManagedLoaderIfOwned(ctx, store, existing.ManagedLoaderID, project.ID, existing.SchedulerID); err != nil {
			return changes, false, fmt.Errorf("disable removed managed loader %s: %w", existing.ManagedLoaderID, err)
		}
		unchanged = false
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED,
			ResourceType: "project_scheduler",
			ResourceId:   disabled.SchedulerID,
			Name:         disabled.AgentName,
			Message:      "disabled because the scheduler is no longer present in the project spec",
		}, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED,
			ResourceType: "loader",
			ResourceId:   existing.ManagedLoaderID,
			Name:         existing.AgentName,
			Message:      "disabled because the scheduler is no longer present in the project spec",
		})
	}
	if refresher != nil {
		if err := refresher.Refresh(ctx); err != nil {
			return changes, false, fmt.Errorf("refresh loader manager: %w", err)
		}
	}
	return changes, unchanged, nil
}

func DisableManagedSchedulersForDown(ctx context.Context, store ManagedSchedulerStore, refresher LoaderRefresher, project ProjectRecord) ([]*agentcomposev2.ProjectChange, error) {
	if store == nil {
		return nil, fmt.Errorf("config store is required")
	}
	schedulers, err := store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return nil, fmt.Errorf("list project schedulers for down %s: %w", project.Name, err)
	}
	var changes []*agentcomposev2.ProjectChange
	for _, scheduler := range schedulers {
		if !scheduler.Enabled {
			continue
		}
		disabled, err := store.SetProjectSchedulerEnabled(ctx, scheduler.ProjectID, scheduler.SchedulerID, false)
		if err != nil {
			return changes, fmt.Errorf("disable project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
		}
		if err := DisableManagedLoaderIfOwned(ctx, store, scheduler.ManagedLoaderID, project.ID, scheduler.SchedulerID); err != nil {
			return changes, fmt.Errorf("disable managed loader %s: %w", scheduler.ManagedLoaderID, err)
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED,
			ResourceType: "project_scheduler",
			ResourceId:   disabled.SchedulerID,
			Name:         disabled.AgentName,
			Message:      "disabled by project down",
		})
		if scheduler.ManagedLoaderID != "" {
			changes = append(changes, &agentcomposev2.ProjectChange{
				Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED,
				ResourceType: "loader",
				ResourceId:   scheduler.ManagedLoaderID,
				Name:         scheduler.AgentName,
				Message:      "disabled by project down",
			})
		}
	}
	if len(changes) > 0 && refresher != nil {
		if err := refresher.Refresh(ctx); err != nil {
			return changes, fmt.Errorf("refresh loader manager after project down: %w", err)
		}
	}
	return changes, nil
}

func CleanupFailedManagedSchedulerReconcile(ctx context.Context, store ManagedSchedulerStore, refresher LoaderRefresher, scheduler ProjectSchedulerRecord, loaderID string) {
	if store == nil {
		return
	}
	if strings.TrimSpace(loaderID) != "" {
		_ = store.SetLoaderEnabled(ctx, loaderID, false)
	}
	if strings.TrimSpace(scheduler.ProjectID) != "" && strings.TrimSpace(scheduler.SchedulerID) != "" {
		_, _ = store.SetProjectSchedulerEnabled(ctx, scheduler.ProjectID, scheduler.SchedulerID, false)
	}
	if refresher != nil {
		_ = refresher.Refresh(ctx)
	}
}

func DisableManagedLoaderIfOwned(ctx context.Context, store ManagedSchedulerStore, loaderID, projectID, schedulerID string) error {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return nil
	}
	loader, found, err := getLoaderIfExists(ctx, store, loaderID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if loader.Summary.ManagedProjectID != strings.TrimSpace(projectID) || loader.Summary.ManagedSchedulerID != strings.TrimSpace(schedulerID) {
		return nil
	}
	if !loader.Summary.Enabled {
		return nil
	}
	return store.SetLoaderEnabled(ctx, loaderID, false)
}

func getProjectSchedulerIfExists(ctx context.Context, store ManagedSchedulerStore, projectID, schedulerID string) (ProjectSchedulerRecord, bool, error) {
	scheduler, err := store.GetProjectScheduler(ctx, projectID, schedulerID)
	if err == nil {
		return scheduler, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectSchedulerRecord{}, false, nil
	}
	return ProjectSchedulerRecord{}, false, err
}

func getLoaderIfExists(ctx context.Context, store ManagedSchedulerStore, loaderID string) (loaderdomain.Definition, bool, error) {
	loader, err := store.GetLoader(ctx, loaderID)
	if err == nil {
		return loader, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return loaderdomain.Definition{}, false, nil
	}
	return loaderdomain.Definition{}, false, err
}

func SchedulerChangeAction(existing ProjectSchedulerRecord, found bool, current ProjectSchedulerRecord) agentcomposev2.ProjectChangeAction {
	if !found {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	}
	if existing.ManagedLoaderID == current.ManagedLoaderID &&
		existing.Revision == current.Revision &&
		existing.Enabled == current.Enabled &&
		existing.TriggerCount == current.TriggerCount &&
		existing.SpecJSON == current.SpecJSON {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	}
	return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
}

func ManagedLoaderChangeAction(existing loaderdomain.Definition, found bool, current loaderdomain.Definition) agentcomposev2.ProjectChangeAction {
	if !found {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	}
	if existing.Summary.Name == current.Summary.Name &&
		existing.Summary.Description == current.Summary.Description &&
		existing.Summary.Enabled == current.Summary.Enabled &&
		existing.Summary.Runtime == current.Summary.Runtime &&
		existing.Summary.WorkspaceID == current.Summary.WorkspaceID &&
		existing.Summary.AgentID == current.Summary.AgentID &&
		existing.Summary.Driver == current.Summary.Driver &&
		existing.Summary.GuestImage == current.Summary.GuestImage &&
		existing.Summary.DefaultAgent == current.Summary.DefaultAgent &&
		existing.Summary.SessionPolicy == current.Summary.SessionPolicy &&
		existing.Summary.ConcurrencyPolicy == current.Summary.ConcurrencyPolicy &&
		existing.Summary.ManagedProjectID == current.Summary.ManagedProjectID &&
		existing.Summary.ManagedRevision == current.Summary.ManagedRevision &&
		existing.Summary.ManagedAgentName == current.Summary.ManagedAgentName &&
		existing.Summary.ManagedSchedulerID == current.Summary.ManagedSchedulerID &&
		existing.Script == current.Script &&
		sameLoaderEnvItems(existing.EnvItems, current.EnvItems) &&
		SameStringSlices(existing.Summary.CapsetIDs, current.Summary.CapsetIDs) &&
		SameLoaderTriggerSpecs(existing.Triggers, current.Triggers) {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	}
	return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
}

func SameLoaderTriggerSpecs(a, b []loaderdomain.Trigger) bool {
	a = NormalizeComparableLoaderTriggers(a)
	b = NormalizeComparableLoaderTriggers(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID ||
			a[i].Kind != b[i].Kind ||
			a[i].Topic != b[i].Topic ||
			a[i].IntervalMs != b[i].IntervalMs ||
			a[i].AutoID != b[i].AutoID ||
			a[i].SpecJSON != b[i].SpecJSON {
			return false
		}
	}
	return true
}

func NormalizeComparableLoaderTriggers(items []loaderdomain.Trigger) []loaderdomain.Trigger {
	cloned := append([]loaderdomain.Trigger(nil), items...)
	for i := range cloned {
		cloned[i].ID = strings.TrimSpace(cloned[i].ID)
		cloned[i].Kind = strings.TrimSpace(cloned[i].Kind)
		cloned[i].Topic = strings.TrimSpace(cloned[i].Topic)
		cloned[i].SpecJSON = strings.TrimSpace(cloned[i].SpecJSON)
	}
	slices.SortFunc(cloned, func(a, b loaderdomain.Trigger) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return cloned
}

func sameLoaderEnvItems(a, b []loaderdomain.EnvVar) bool {
	a = normalizeLoaderEnvItems(a)
	b = normalizeLoaderEnvItems(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func normalizeLoaderEnvItems(items []loaderdomain.EnvVar) []loaderdomain.EnvVar {
	normalized := make([]loaderdomain.EnvVar, 0, len(items))
	for _, item := range items {
		item.Name = strings.TrimSpace(item.Name)
		item.Value = strings.TrimSpace(item.Value)
		if item.Name == "" {
			continue
		}
		normalized = append(normalized, item)
	}
	slices.SortFunc(normalized, func(a, b loaderdomain.EnvVar) int {
		return strings.Compare(a.Name, b.Name)
	})
	return normalized
}

func SameStringSlices(a, b []string) bool {
	a = capabilitydomain.NormalizeCapsetIDs(a)
	b = capabilitydomain.NormalizeCapsetIDs(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
