package driver

import (
	appconfig "agent-compose/internal/config"

	"github.com/samber/do/v2"
)

func NewBoxRuntime(di do.Injector) (BoxRuntime, error) {
	return newBoxRuntime(do.MustInvoke[*appconfig.Config](di))
}

func NewBoxliteRuntime(config *appconfig.Config) (BoxRuntime, error) {
	return newBoxRuntime(config)
}

func NewDockerRuntime(config *appconfig.Config) (BoxRuntime, error) {
	return newDockerRuntime(config)
}

func NewMicrosandboxRuntime(config *appconfig.Config) (BoxRuntime, error) {
	return newMicrosandboxRuntime(config)
}
