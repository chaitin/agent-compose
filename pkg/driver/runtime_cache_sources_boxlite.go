//go:build boxlitecgo

package driver

import (
	"context"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/runtimecache"
)

type boxliteRuntimeCacheSource struct {
	boxliteHome string
}

func appendBoxliteRuntimeCacheSource(sources []runtimecache.Source, config *appconfig.Config) []runtimecache.Source {
	if config == nil {
		return sources
	}
	return append(sources, boxliteRuntimeCacheSource{boxliteHome: config.BoxliteHome})
}

func (s boxliteRuntimeCacheSource) List(ctx context.Context) (runtimecache.ListResult, error) {
	return listBoxliteRuntimeDerivedCaches(ctx, s.boxliteHome)
}

func (s boxliteRuntimeCacheSource) Remove(ctx context.Context, item runtimecache.Item) error {
	return boxliteRuntimeDerivedRemover{boxliteHome: s.boxliteHome}.Remove(ctx, item)
}
