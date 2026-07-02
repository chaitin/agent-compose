package driver

import (
	appconfig "agent-compose/pkg/config"
	drivertypes "agent-compose/pkg/driver/types"
)

const (
	RuntimeDriverBoxlite      = drivertypes.RuntimeDriverBoxlite
	RuntimeDriverDocker       = drivertypes.RuntimeDriverDocker
	RuntimeDriverMicrosandbox = drivertypes.RuntimeDriverMicrosandbox
)

func ResolveRuntimeDriver(value string) string {
	return drivertypes.ResolveRuntimeDriver(value)
}

func ValidateRuntimeDriver(value string) error {
	return drivertypes.ValidateRuntimeDriver(value)
}

func ResolveSessionRuntimeDriver(value, fallback string) (string, error) {
	return drivertypes.ResolveSessionRuntimeDriver(value, fallback)
}

func DefaultGuestImageForDriver(config *appconfig.Config, driver string) string {
	return drivertypes.DefaultGuestImageForDriver(config, driver)
}

func RuntimeHomeForDriver(config *appconfig.Config, driver string) string {
	return drivertypes.RuntimeHomeForDriver(config, driver)
}
