package run

import (
	"context"

	agentdomain "agent-compose/internal/agent"
	sqlitestore "agent-compose/internal/persistence/sqlite"
	projecttypes "agent-compose/internal/projecttypes"
)

type ProjectRecord = sqlitestore.ProjectRecord
type ProjectAgentRecord = sqlitestore.ProjectAgentRecord
type AgentDefinition = agentdomain.AgentDefinition
type ProjectRunRecord = projecttypes.ProjectRunRecord

const (
	ProjectRunStatusPending   = projecttypes.ProjectRunStatusPending
	ProjectRunStatusRunning   = projecttypes.ProjectRunStatusRunning
	ProjectRunStatusSucceeded = projecttypes.ProjectRunStatusSucceeded
	ProjectRunStatusFailed    = projecttypes.ProjectRunStatusFailed
	ProjectRunStatusCanceled  = projecttypes.ProjectRunStatusCanceled
	ProjectRunSourceManual    = projecttypes.ProjectRunSourceManual
	ProjectRunSourceScheduler = projecttypes.ProjectRunSourceScheduler
	ProjectRunSourceAPI       = projecttypes.ProjectRunSourceAPI
)

type ConfigStore interface {
	GetProject(context.Context, string) (ProjectRecord, error)
	GetProjectAgent(context.Context, string, string) (ProjectAgentRecord, error)
	GetAgentDefinition(context.Context, string) (AgentDefinition, error)
	CreateProjectRun(context.Context, ProjectRunRecord) (ProjectRunRecord, error)
	GetProjectRun(context.Context, string) (ProjectRunRecord, error)
	UpdateProjectRun(context.Context, ProjectRunRecord) (ProjectRunRecord, error)
}

var StableProjectRunID = sqlitestore.StableProjectRunID
