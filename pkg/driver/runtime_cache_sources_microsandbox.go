//go:build cgo

package driver

import (
	"context"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/runtimecache"
)

type microsandboxRuntimeCacheSource struct {
	microsandboxHome string
}

func appendMicrosandboxRuntimeCacheSource(sources []runtimecache.Source, config *appconfig.Config) []runtimecache.Source {
	if config == nil {
		return sources
	}
	return append(sources, microsandboxRuntimeCacheSource{microsandboxHome: config.MicrosandboxHome})
}

func (s microsandboxRuntimeCacheSource) List(ctx context.Context) (runtimecache.ListResult, error) {
	return listMicrosandboxSessionEphemeralCaches(ctx, s.microsandboxHome, microsandboxCacheReferenceState{
		Unknown:  true,
		Warnings: []string{"microsandbox session references are not fully resolved"},
	})
}

func (s microsandboxRuntimeCacheSource) Remove(ctx context.Context, item runtimecache.Item) error {
	return microsandboxSessionEphemeralRemover{microsandboxHome: s.microsandboxHome}.Remove(ctx, item)
}
