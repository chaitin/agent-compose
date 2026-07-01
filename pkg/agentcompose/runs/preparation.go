package runs

import "agent-compose/pkg/agentcompose/domain"

type Preparation struct {
	EnvItems         []domain.SessionEnvVar
	ProviderEnvItems []domain.SessionEnvVar
	CapsetIDs        []string
	WorkspaceConfig  *domain.WorkspaceConfig
	Workspace        *domain.SessionWorkspace
}
