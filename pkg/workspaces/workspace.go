package workspaces

import (
	"context"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type WorkspaceConfigStore interface {
	GetWorkspaceConfig(ctx context.Context, id string) (domain.WorkspaceConfig, error)
}

// Store is retained as a compatibility alias for workspace config readers.
type Store = WorkspaceConfigStore

func materializeSessionWorkspace(ctx context.Context, config *appconfig.Config, configDB WorkspaceConfigStore, session *domain.Sandbox) error {
	workspaceID := strings.TrimSpace(session.WorkspaceID)
	if session.Workspace != nil && strings.TrimSpace(session.Workspace.ID) != "" {
		workspace := domain.WorkspaceConfig{
			ID:            strings.TrimSpace(session.Workspace.ID),
			Name:          session.Workspace.Name,
			Type:          session.Workspace.Type,
			ConfigJSON:    session.Workspace.ConfigJSON,
			SnapshotID:    session.Workspace.SnapshotID,
			SnapshotLease: session.Workspace.SnapshotLease,
		}
		if workspaceID == "" {
			session.WorkspaceID = workspace.ID
		}
		return materializeWorkspaceConfig(ctx, config, session, workspace)
	}
	if workspaceID == "" {
		return nil
	}
	workspace, err := configDB.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return err
	}
	return materializeWorkspaceConfig(ctx, config, session, workspace)
}

func materializeWorkspaceConfig(ctx context.Context, config *appconfig.Config, session *domain.Sandbox, workspace domain.WorkspaceConfig) error {
	impl, err := newWorkspace(config, workspace)
	if err != nil {
		return err
	}
	return impl.Prepare(ctx, session)
}
