package driver

import (
	appconfig "agent-compose/pkg/config"
	boxlitedriver "agent-compose/pkg/driver/boxlite"
	dockerdriver "agent-compose/pkg/driver/docker"
	driverimage "agent-compose/pkg/driver/image"
	microsandboxdriver "agent-compose/pkg/driver/microsandbox"
	driversession "agent-compose/pkg/driver/session"
	drivertypes "agent-compose/pkg/driver/types"
	"context"

	"github.com/samber/do/v2"
)

// refactor aliases
type SessionEnvVar = drivertypes.SessionEnvVar
type SessionSummary = drivertypes.SessionSummary
type Session = drivertypes.Session
type VMState = drivertypes.VMState
type ProxyState = drivertypes.ProxyState
type ExecChunk = drivertypes.ExecChunk
type ExecSpec = drivertypes.ExecSpec
type ExecResult = drivertypes.ExecResult
type ExecStreamWriter = drivertypes.ExecStreamWriter
type SessionVMInfo = drivertypes.SessionVMInfo
type BoxRuntime = drivertypes.BoxRuntime

func NewBoxRuntime(di do.Injector) (BoxRuntime, error) {
	return boxlitedriver.NewRuntime(do.MustInvoke[*appconfig.Config](di))
}

func NewBoxliteRuntime(config *appconfig.Config) (BoxRuntime, error) {
	return boxlitedriver.NewRuntime(config)
}

func NewDockerRuntime(config *appconfig.Config) (BoxRuntime, error) {
	return dockerdriver.NewRuntime(config)
}

func NewMicrosandboxRuntime(config *appconfig.Config) (BoxRuntime, error) {
	return microsandboxdriver.NewRuntime(config)
}

func JupyterConnectTarget(proxyState ProxyState) (string, int) {
	return boxlitedriver.JupyterConnectTarget(proxyState)
}

func JupyterConnectAddress(proxyState ProxyState) string {
	return boxlitedriver.JupyterConnectAddress(proxyState)
}

func PrepareSessionStart(ctx context.Context, config *appconfig.Config, driver string, session *Session, vmState VMState) (VMState, error) {
	return driversession.PrepareSessionStart(ctx, config, driver, session, vmState)
}

func ResolveSessionGuestImage(values ...string) string {
	return driverimage.ResolveSessionGuestImage(values...)
}

func LLMProviderKeyName(name string) bool {
	return drivertypes.LLMProviderKeyName(name)
}
