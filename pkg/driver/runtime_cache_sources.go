package driver

import (
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/runtimecache"
)

func NewRuntimeCacheSources(config *appconfig.Config) []runtimecache.Source {
	var sources []runtimecache.Source
	sources = appendBoxliteRuntimeCacheSource(sources, config)
	sources = appendMicrosandboxRuntimeCacheSource(sources, config)
	return sources
}
