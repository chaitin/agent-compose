package agentcompose

import (
	runtimedomain "agent-compose/internal/agentcompose/runtime"
	sessiondomain "agent-compose/internal/agentcompose/session"
	appconfig "agent-compose/internal/config"

	"github.com/samber/do/v2"
)

type SessionVMInfo = sessiondomain.VMInfo

type BoxRuntime = runtimedomain.BoxRuntime
type sessionAliveRuntime = runtimedomain.SessionAliveRuntime
type RuntimeProvider = runtimedomain.Provider

func NewRuntimeProvider(di do.Injector) (RuntimeProvider, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	return runtimedomain.NewProvider(config)
}
