package projects

import (
	"context"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
)

const (
	DownChangeUpdated   = "updated"
	DownChangeUnchanged = "unchanged"
)

type DownChange struct {
	Action       string
	ResourceType string
	ResourceID   string
	Name         string
	Message      string
}

type DownStore interface {
	ListProjectSchedulers(ctx context.Context, projectID string) ([]domain.ProjectSchedulerRecord, error)
	SetProjectSchedulerEnabled(ctx context.Context, projectID, schedulerID string, enabled bool) (domain.ProjectSchedulerRecord, error)
}

type DownSandboxStore interface {
	ListSandboxes(ctx context.Context, options domain.SandboxListOptions) (domain.SandboxListResult, error)
}

type DownOptions struct {
	Store                DownStore
	Sandboxes            DownSandboxStore
	DisableManagedLoader func(ctx context.Context, loaderID, projectID, schedulerID string) error
	RefreshLoaders       func(ctx context.Context) error
	StopSandbox          func(ctx context.Context, sandbox *domain.Sandbox) error
}

func DownProject(ctx context.Context, project domain.ProjectRecord, options DownOptions) ([]DownChange, error) {
	var changes []DownChange
	schedulerChanges, err := DisableProjectManagedSchedulers(ctx, project, options)
	if err != nil {
		return changes, err
	}
	changes = append(changes, schedulerChanges...)
	sandboxChanges, err := StopProjectRunningSandboxes(ctx, project, options)
	if err != nil {
		return changes, err
	}
	changes = append(changes, sandboxChanges...)
	return changes, nil
}

func DisableProjectManagedSchedulers(ctx context.Context, project domain.ProjectRecord, options DownOptions) ([]DownChange, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("project store is required")
	}
	schedulers, err := options.Store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return nil, fmt.Errorf("list project schedulers for down %s: %w", project.Name, err)
	}
	var changes []DownChange
	for _, scheduler := range schedulers {
		if !scheduler.Enabled {
			continue
		}
		disabled, err := options.Store.SetProjectSchedulerEnabled(ctx, scheduler.ProjectID, scheduler.SchedulerID, false)
		if err != nil {
			return changes, fmt.Errorf("disable project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
		}
		if options.DisableManagedLoader != nil {
			if err := options.DisableManagedLoader(ctx, scheduler.ManagedLoaderID, project.ID, scheduler.SchedulerID); err != nil {
				return changes, fmt.Errorf("disable managed loader %s: %w", scheduler.ManagedLoaderID, err)
			}
		}
		changes = append(changes, DownChange{
			Action:       DownChangeUpdated,
			ResourceType: "project_scheduler",
			ResourceID:   disabled.SchedulerID,
			Name:         disabled.AgentName,
			Message:      "disabled by project down",
		})
		if scheduler.ManagedLoaderID != "" {
			changes = append(changes, DownChange{
				Action:       DownChangeUpdated,
				ResourceType: "loader",
				ResourceID:   scheduler.ManagedLoaderID,
				Name:         scheduler.AgentName,
				Message:      "disabled by project down",
			})
		}
	}
	if len(changes) > 0 && options.RefreshLoaders != nil {
		if err := options.RefreshLoaders(ctx); err != nil {
			return changes, fmt.Errorf("refresh loader manager after project down: %w", err)
		}
	}
	return changes, nil
}

func StopProjectRunningSandboxes(ctx context.Context, project domain.ProjectRecord, options DownOptions) ([]DownChange, error) {
	if options.Sandboxes == nil {
		return nil, fmt.Errorf("sandbox store is required")
	}
	result, err := options.Sandboxes.ListSandboxes(ctx, domain.SandboxListOptions{VMStatus: domain.VMStatusRunning, Limit: 1 << 30})
	if err != nil {
		return nil, fmt.Errorf("list running sandboxes for project down %s: %w", project.Name, err)
	}
	var changes []DownChange
	for _, sandbox := range result.Sandboxes {
		if !SandboxBelongsToProject(sandbox, project.ID) {
			continue
		}
		if options.StopSandbox == nil {
			return changes, fmt.Errorf("sandbox stopper is required")
		}
		if err := options.StopSandbox(ctx, sandbox); err != nil {
			changes = append(changes, DownChange{
				Action:       DownChangeUnchanged,
				ResourceType: "sandbox",
				ResourceID:   sandbox.Summary.ID,
				Name:         sandbox.Summary.Title,
				Message:      fmt.Sprintf("failed to stop by project down: %v", err),
			})
			continue
		}
		changes = append(changes, DownChange{
			Action:       DownChangeUpdated,
			ResourceType: "sandbox",
			ResourceID:   sandbox.Summary.ID,
			Name:         sandbox.Summary.Title,
			Message:      "stopped by project down",
		})
	}
	return changes, nil
}

func SandboxBelongsToProject(sandbox *domain.Sandbox, projectID string) bool {
	if sandbox == nil {
		return false
	}
	if canonical := sandboxTagValue(sandbox, "project"); canonical != "" {
		return canonical == strings.TrimSpace(projectID)
	}
	return sandboxTagValue(sandbox, "project_id") == strings.TrimSpace(projectID)
}

func sandboxTagValue(sandbox *domain.Sandbox, name string) string {
	if sandbox == nil {
		return ""
	}
	for _, tag := range sandbox.Summary.Tags {
		if strings.TrimSpace(tag.Name) == strings.TrimSpace(name) {
			return strings.TrimSpace(tag.Value)
		}
	}
	return ""
}

func SandboxHasTag(sandbox *domain.Sandbox, name, value string) bool {
	if sandbox == nil {
		return false
	}
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	for _, tag := range sandbox.Summary.Tags {
		if strings.TrimSpace(tag.Name) == name && strings.TrimSpace(tag.Value) == value {
			return true
		}
	}
	return false
}
