//go:build !boxlitecgo

package driver

import (
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/runtimecache"
)

func appendBoxliteRuntimeCacheSource(sources []runtimecache.Source, _ *appconfig.Config) []runtimecache.Source {
	return sources
}
