package agentcompose

import (
	"context"

	projectdomain "agent-compose/internal/agentcompose/project"
)

func (s *ConfigStore) ensureProjectSchema(ctx context.Context) error {
	return projectdomain.EnsureSchema(ctx, s.db)
}
