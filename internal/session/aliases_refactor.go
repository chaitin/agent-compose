package session

import (
	"context"

	eventtypes "agent-compose/internal/eventtypes"
	loadertypes "agent-compose/internal/loadertypes"
	modeldomain "agent-compose/internal/model"
	runtimedomain "agent-compose/internal/runtime"
	"agent-compose/pkg/capability"
)

type SessionTag = modeldomain.SessionTag
type SessionEnvVar = modeldomain.SessionEnvVar
type SessionSummary = modeldomain.SessionSummary
type SessionListOptions = modeldomain.SessionListOptions
type SessionListResult = modeldomain.SessionListResult
type SessionWorkspace = modeldomain.SessionWorkspace
type Session = modeldomain.Session
type WorkspaceConfig = modeldomain.WorkspaceConfig
type NotebookCell = modeldomain.NotebookCell
type SessionEvent = modeldomain.SessionEvent
type ExecChunk = modeldomain.ExecChunk
type ExecSpec = modeldomain.ExecSpec
type ExecResult = modeldomain.ExecResult
type ExecStreamWriter = modeldomain.ExecStreamWriter
type VMState = modeldomain.VMState
type ProxyState = modeldomain.ProxyState

const (
	SessionTypeManual = modeldomain.SessionTypeManual
	SessionTypeScript = modeldomain.SessionTypeScript
)

var mergeEnvItems = modeldomain.MergeEnvItems

type LoaderTopicEvent = eventtypes.LoaderTopicEvent
type TopicEventRecord = eventtypes.TopicEventRecord
type LoaderAgentRequest = loadertypes.LoaderAgentRequest
type LoaderAgentResult = loadertypes.LoaderAgentResult
type LoaderCommandRequest = loadertypes.LoaderCommandRequest
type LoaderCommandResult = loadertypes.LoaderCommandResult
type LoaderLLMRequest = loadertypes.LoaderLLMRequest
type LoaderLLMResult = loadertypes.LoaderLLMResult

type SessionVMInfo = runtimedomain.SessionVMInfo
type BoxRuntime = runtimedomain.BoxRuntime
type RuntimeProvider = runtimedomain.RuntimeProvider

type Store interface {
	CreateSession(context.Context, string, string, string, string, string, string, *SessionWorkspace, []SessionEnvVar, []SessionTag) (*Session, error)
	GetSession(context.Context, string) (*Session, error)
	ListSessions(context.Context, SessionListOptions) (SessionListResult, error)
	UpdateSession(context.Context, *Session) error
	AddEvent(context.Context, string, SessionEvent) error
	ListEvents(context.Context, string) ([]SessionEvent, error)
	GetVMState(string) (VMState, error)
	SaveVMState(string, VMState) error
	GetProxyState(string) (ProxyState, error)
	SaveProxyState(string, ProxyState) error
}

type ConfigStore interface {
	ListGlobalEnv(context.Context) ([]SessionEnvVar, error)
	GetWorkspaceConfig(context.Context, string) (WorkspaceConfig, error)
	RevokeLLMFacadeTokensForSession(context.Context, string) error
}

type LoaderBus interface {
	Publish(LoaderTopicEvent) bool
}

type CapabilityProvider interface {
	ProxyTarget() string
	CapabilityGuide(ctx context.Context, capsetID string) ([]byte, error)
	Status(context.Context) capability.Status
	ListCapsets(context.Context) ([]capability.Capset, error)
	Catalog(context.Context, string) (capability.Catalog, error)
}

type dashboardNotifier interface {
	Notify(string)
}
