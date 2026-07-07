package projects

import (
	"context"
	"database/sql"
	"errors"
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
	ListManagedLoaders(ctx context.Context, projectID string) ([]domain.Loader, error)
	SetProjectSchedulerEnabled(ctx context.Context, projectID, schedulerID string, enabled bool) (domain.ProjectSchedulerRecord, error)
	SetLoaderEnabled(ctx context.Context, loaderID string, enabled bool) error
}

type DownSessionStore interface {
	ListSessions(ctx context.Context, options domain.SessionListOptions) (domain.SessionListResult, error)
}

type DownOptions struct {
	Store          DownStore
	Sessions       DownSessionStore
	RefreshLoaders func(ctx context.Context) error
	StopSession    func(ctx context.Context, session *domain.Session) error
}

func DownProject(ctx context.Context, project domain.ProjectRecord, options DownOptions) ([]DownChange, error) {
	var changes []DownChange
	schedulerChanges, err := DisableProjectManagedSchedulers(ctx, project, options)
	if err != nil {
		return changes, err
	}
	changes = append(changes, schedulerChanges...)
	sessionChanges, err := StopProjectRunningSessions(ctx, project, options)
	if err != nil {
		return changes, err
	}
	changes = append(changes, sessionChanges...)
	return changes, nil
}

func DisableProjectManagedSchedulers(ctx context.Context, project domain.ProjectRecord, options DownOptions) ([]DownChange, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("project store is required")
	}
	managedLoaders, err := options.Store.ListManagedLoaders(ctx, project.ID)
	if err != nil {
		return nil, fmt.Errorf("list managed loaders for down %s: %w", project.Name, err)
	}
	var changes []DownChange
	for _, loader := range managedLoaders {
		projected := ProjectSchedulersFromManagedLoaders([]domain.Loader{loader})
		if len(projected) == 0 {
			continue
		}
		scheduler := projected[0]
		if !scheduler.Enabled {
			continue
		}
		if err := options.Store.SetLoaderEnabled(ctx, scheduler.ManagedLoaderID, false); err != nil {
			return changes, fmt.Errorf("disable managed loader %s: %w", scheduler.ManagedLoaderID, err)
		}
		if _, err := options.Store.SetProjectSchedulerEnabled(ctx, scheduler.ProjectID, scheduler.SchedulerID, false); err != nil && !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
			return changes, fmt.Errorf("disable project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
		}
		changes = append(changes, DownChange{
			Action:       DownChangeUpdated,
			ResourceType: "project_scheduler",
			ResourceID:   scheduler.SchedulerID,
			Name:         scheduler.AgentName,
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

func StopProjectRunningSessions(ctx context.Context, project domain.ProjectRecord, options DownOptions) ([]DownChange, error) {
	if options.Sessions == nil {
		return nil, fmt.Errorf("session store is required")
	}
	result, err := options.Sessions.ListSessions(ctx, domain.SessionListOptions{VMStatus: domain.VMStatusRunning, Limit: 1 << 30})
	if err != nil {
		return nil, fmt.Errorf("list running sessions for project down %s: %w", project.Name, err)
	}
	var changes []DownChange
	for _, session := range result.Sessions {
		if !SessionHasTag(session, "project", project.ID) {
			continue
		}
		if options.StopSession == nil {
			return changes, fmt.Errorf("session stopper is required")
		}
		if err := options.StopSession(ctx, session); err != nil {
			changes = append(changes, DownChange{
				Action:       DownChangeUnchanged,
				ResourceType: "session",
				ResourceID:   session.Summary.ID,
				Name:         session.Summary.Title,
				Message:      fmt.Sprintf("failed to stop by project down: %v", err),
			})
			continue
		}
		changes = append(changes, DownChange{
			Action:       DownChangeUpdated,
			ResourceType: "session",
			ResourceID:   session.Summary.ID,
			Name:         session.Summary.Title,
			Message:      "stopped by project down",
		})
	}
	return changes, nil
}

func SessionHasTag(session *domain.Session, name, value string) bool {
	if session == nil {
		return false
	}
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	for _, tag := range session.Summary.Tags {
		if strings.TrimSpace(tag.Name) == name && strings.TrimSpace(tag.Value) == value {
			return true
		}
	}
	return false
}
