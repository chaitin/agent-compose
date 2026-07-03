package runs

import owner "agent-compose/pkg/runs"

type (
	Coordinator            = owner.Coordinator
	ManagedAgentDefinition = owner.ManagedAgentDefinition
	Preparation            = owner.Preparation
	ProjectSessionRunStore = owner.ProjectSessionRunStore
	SessionResult          = owner.SessionResult
	SessionStore           = owner.SessionStore
	StableRunIDFunc        = owner.StableRunIDFunc
	StartRequest           = owner.StartRequest
	Store                  = owner.Store
	TransitionRequest      = owner.TransitionRequest
)

var (
	AgentSpecByName                  = owner.AgentSpecByName
	CleanLocalWorkspacePath          = owner.CleanLocalWorkspacePath
	ComposeWorkspaceSpecFromV2       = owner.ComposeWorkspaceSpecFromV2
	DecodeRevisionSpec               = owner.DecodeRevisionSpec
	EnvItemsFromV2                   = owner.EnvItemsFromV2
	ListProjectSessionStatuses       = owner.ListProjectSessionStatuses
	MergeEnvItems                    = owner.MergeEnvItems
	MergeSessionTags                 = owner.MergeSessionTags
	NewCoordinator                   = owner.NewCoordinator
	NormalizeSource                  = owner.NormalizeSource
	NormalizeStatus                  = owner.NormalizeStatus
	ResolveLocalProjectWorkspacePath = owner.ResolveLocalProjectWorkspacePath
	SessionTags                      = owner.SessionTags
	SessionTitle                     = owner.SessionTitle
	StatusIsTerminal                 = owner.StatusIsTerminal
	WorkspaceID                      = owner.WorkspaceID
	WorkspaceName                    = owner.WorkspaceName
)
