package driver

import (
	appconfig "agent-compose/internal/config"
	"fmt"
	"strings"
)

const (
	RuntimeDriverBoxlite      = "boxlite"
	RuntimeDriverDocker       = "docker"
	RuntimeDriverMicrosandbox = "microsandbox"
)

func resolveRuntimeDriver(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return RuntimeDriverDocker
	case RuntimeDriverBoxlite:
		return RuntimeDriverBoxlite
	case RuntimeDriverDocker, "docker-engine":
		return RuntimeDriverDocker
	case "msb", RuntimeDriverMicrosandbox:
		return RuntimeDriverMicrosandbox
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func ResolveRuntimeDriver(value string) string {
	return resolveRuntimeDriver(value)
}

func validateRuntimeDriver(value string) error {
	switch resolveRuntimeDriver(value) {
	case RuntimeDriverBoxlite, RuntimeDriverDocker, RuntimeDriverMicrosandbox:
		return nil
	default:
		return fmt.Errorf("unsupported agent-compose runtime driver %q", strings.TrimSpace(value))
	}
}

func ValidateRuntimeDriver(value string) error {
	return validateRuntimeDriver(value)
}

func resolveSessionRuntimeDriver(value, fallback string) (string, error) {
	input := value
	if strings.TrimSpace(input) == "" {
		input = fallback
	}
	driver := resolveRuntimeDriver(input)
	if err := validateRuntimeDriver(driver); err != nil {
		return "", err
	}
	return driver, nil
}

func ResolveSessionRuntimeDriver(value, fallback string) (string, error) {
	return resolveSessionRuntimeDriver(value, fallback)
}

func defaultGuestImageForDriver(config *appconfig.Config, driver string) string {
	switch resolveRuntimeDriver(driver) {
	case RuntimeDriverMicrosandbox:
		return config.MicrosandboxDefaultImage
	case RuntimeDriverDocker:
		return firstNonEmpty(config.DockerDefaultImage, config.DefaultImage)
	}
	return config.DefaultImage
}

func DefaultGuestImageForDriver(config *appconfig.Config, driver string) string {
	return defaultGuestImageForDriver(config, driver)
}

func runtimeHomeForDriver(config *appconfig.Config, driver string) string {
	switch resolveRuntimeDriver(driver) {
	case RuntimeDriverMicrosandbox:
		return config.MicrosandboxHome
	case RuntimeDriverDocker:
		return config.DockerHome
	}
	return config.BoxliteHome
}

func RuntimeHomeForDriver(config *appconfig.Config, driver string) string {
	return runtimeHomeForDriver(config, driver)
}
