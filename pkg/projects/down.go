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

type DownSessionStore interface {
	ListSessions(ctx context.Context, options domain.SessionListOptions) (domain.SessionListResult, error)
}

type DownOptions struct {
	Store                      DownStore
	Sessions                   DownSessionStore
	DisableSchedulerExecution  func(ctx context.Context, executionID, projectID, schedulerID string) error
	RefreshSchedulerExecutions func(ctx context.Context) error
	StopSession                func(ctx context.Context, session *domain.Session) error
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
		if options.DisableSchedulerExecution != nil {
			if err := options.DisableSchedulerExecution(ctx, scheduler.ManagedLoaderID, project.ID, scheduler.SchedulerID); err != nil {
				return changes, fmt.Errorf("disable scheduler execution %s: %w", scheduler.ManagedLoaderID, err)
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
	if len(changes) > 0 && options.RefreshSchedulerExecutions != nil {
		if err := options.RefreshSchedulerExecutions(ctx); err != nil {
			return changes, fmt.Errorf("refresh scheduler execution manager after project down: %w", err)
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
