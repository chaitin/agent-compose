package agentcompose

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	projectdomain "agent-compose/internal/agentcompose/project"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) downProject(ctx context.Context, project ProjectRecord) ([]*agentcomposev2.ProjectChange, error) {
	var changes []*agentcomposev2.ProjectChange
	schedulerChanges, err := s.disableProjectManagedSchedulers(ctx, project)
	if err != nil {
		return changes, connect.NewError(connect.CodeInternal, err)
	}
	changes = append(changes, schedulerChanges...)
	sessionChanges, err := s.stopProjectRunningSessions(ctx, project)
	if err != nil {
		return changes, connect.NewError(connect.CodeInternal, err)
	}
	changes = append(changes, sessionChanges...)
	return changes, nil
}

func (s *Service) disableProjectManagedSchedulers(ctx context.Context, project ProjectRecord) ([]*agentcomposev2.ProjectChange, error) {
	return projectdomain.DisableManagedSchedulersForDown(ctx, s.configDB, s.loaders, project)
}

func (s *Service) stopProjectRunningSessions(ctx context.Context, project ProjectRecord) ([]*agentcomposev2.ProjectChange, error) {
	if s.store == nil {
		return nil, fmt.Errorf("session store is required")
	}
	result, err := s.store.ListSessions(ctx, SessionListOptions{VMStatus: VMStatusRunning, Limit: 1 << 30})
	if err != nil {
		return nil, fmt.Errorf("list running sessions for project down %s: %w", project.Name, err)
	}
	var changes []*agentcomposev2.ProjectChange
	for _, session := range result.Sessions {
		if !projectSessionHasTag(session, "project", project.ID) {
			continue
		}
		if err := s.stopProjectRunSession(ctx, session); err != nil {
			changes = append(changes, &agentcomposev2.ProjectChange{
				Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED,
				ResourceType: "session",
				ResourceId:   session.Summary.ID,
				Name:         session.Summary.Title,
				Message:      fmt.Sprintf("failed to stop by project down: %v", err),
			})
			continue
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED,
			ResourceType: "session",
			ResourceId:   session.Summary.ID,
			Name:         session.Summary.Title,
			Message:      "stopped by project down",
		})
	}
	return changes, nil
}

func projectSessionHasTag(session *Session, name, value string) bool {
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
