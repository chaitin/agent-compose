//go:build !cgo

package driver

import (
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/runtimecache"
)

func appendMicrosandboxRuntimeCacheSource(sources []runtimecache.Source, _ *appconfig.Config) []runtimecache.Source {
	return sources
}
