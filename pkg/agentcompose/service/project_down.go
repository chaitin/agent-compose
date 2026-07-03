package agentcompose

import (
	"context"

	"connectrpc.com/connect"

	"agent-compose/pkg/projects"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) downProject(ctx context.Context, project ProjectRecord) ([]*agentcomposev2.ProjectChange, error) {
	changes, err := projects.DownProject(ctx, project, projects.DownOptions{
		Store:                s.configDB,
		Sessions:             s.store,
		DisableManagedLoader: s.disableManagedLoaderIfOwned,
		RefreshLoaders:       s.refreshLoaders,
		StopSession:          s.stopProjectRunSession,
	})
	if err != nil {
		return projectDownChangesToProto(changes), connect.NewError(connect.CodeInternal, err)
	}
	return projectDownChangesToProto(changes), nil
}

func (s *Service) refreshLoaders(ctx context.Context) error {
	if s == nil || s.loaders == nil {
		return nil
	}
	return s.loaders.Refresh(ctx)
}

func projectDownChangesToProto(changes []projects.DownChange) []*agentcomposev2.ProjectChange {
	result := make([]*agentcomposev2.ProjectChange, 0, len(changes))
	for _, change := range changes {
		result = append(result, &agentcomposev2.ProjectChange{
			Action:       projectDownChangeActionToProto(change.Action),
			ResourceType: change.ResourceType,
			ResourceId:   change.ResourceID,
			Name:         change.Name,
			Message:      change.Message,
		})
	}
	return result
}

func projectDownChangeActionToProto(action string) agentcomposev2.ProjectChangeAction {
	switch action {
	case projects.DownChangeUpdated:
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
	case projects.DownChangeUnchanged:
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	default:
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNSPECIFIED
	}
}
