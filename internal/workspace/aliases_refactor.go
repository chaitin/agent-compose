package workspace

import (
	"context"

	modeldomain "agent-compose/internal/model"
)

type Session = modeldomain.Session
type WorkspaceConfig = modeldomain.WorkspaceConfig

type ConfigStore interface {
	GetWorkspaceConfig(context.Context, string) (WorkspaceConfig, error)
}
