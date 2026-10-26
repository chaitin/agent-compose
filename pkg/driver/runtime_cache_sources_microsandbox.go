//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"fmt"

	"github.com/chaitin/agent-compose/pkg/cache"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

const microsandboxImageCacheKind = "microsandbox-image"

// This cache source still drives the microsandbox SDK in-process in the daemon
// (Image.List/Remove) instead of going through the sandbox-host RPC. It is a
// metadata-only control-plane operation against the shared MicrosandboxHome and
// does not need a live runtime handle or KVM; Image.Remove carries force=false
// as the SDK-level in-use guard. See the known-exception note in
// docs/design/daemon-upgrade-sandbox-survival.zh-CN.md §3.4; move it onto the
// sandbox-host RPC when the contract grows cache methods.

type microsandboxRuntimeCacheSource struct{}

func appendMicrosandboxRuntimeCacheSource(sources []cache.Source, config *appconfig.Config) []cache.Source {
	if config == nil {
		return sources
	}
	return append(sources, microsandboxRuntimeCacheSource{})
}

func (microsandboxRuntimeCacheSource) List(ctx context.Context) (cache.ListResult, error) {
	handles, err := microsandbox.Image.List(ctx)
	if err != nil {
		return cache.ListResult{}, fmt.Errorf("list microsandbox image cache: %w", err)
	}
	result := cache.ListResult{Items: make([]cache.Item, 0, len(handles))}
	for _, handle := range handles {
		if handle == nil {
			continue
		}
		item := cache.Item{
			Domain:         cache.DomainRuntimeDerivedCache,
			Driver:         cache.DriverMicrosandbox,
			Kind:           microsandboxImageCacheKind,
			ImageID:        handle.ManifestDigest(),
			ImageRef:       handle.Reference(),
			ResolvedRef:    handle.ManifestDigest(),
			Status:         cache.StatusUnused,
			LastUsedAt:     handle.LastUsedAt().UTC(),
			LastUsedSource: "microsandbox-sdk",
		}
		if size := handle.SizeBytes(); size != nil && *size > 0 {
			item.SizeBytes = uint64(*size)
		}
		cacheID, idErr := cache.GenerateCacheID(item)
		if idErr != nil {
			item.Status = cache.StatusUnknown
			item.Warnings = append(item.Warnings, idErr.Error())
		} else {
			item.CacheID = cacheID
		}
		result.Items = append(result.Items, cache.EvaluateProtection(item))
	}
	return result, nil
}

func (source microsandboxRuntimeCacheSource) Remove(ctx context.Context, item cache.Item) error {
	if item.Domain != cache.DomainRuntimeDerivedCache || item.Driver != cache.DriverMicrosandbox || item.Kind != microsandboxImageCacheKind {
		return fmt.Errorf("microsandbox image remover rejected cache inventory item")
	}
	latest, err := source.List(ctx)
	if err != nil {
		return err
	}
	resolved, err := cache.ResolveCacheID(latest.Items, item.CacheID)
	if err != nil {
		return err
	}
	for _, candidate := range latest.Items {
		if candidate.CacheID != resolved {
			continue
		}
		candidate = cache.EvaluateProtection(candidate)
		if !candidate.Removable {
			return fmt.Errorf("microsandbox image is no longer safely removable")
		}
		// force=false is the final SDK-level in-use guard and is intentionally not
		// coupled to CacheService's execution --force flag.
		return microsandbox.Image.Remove(ctx, candidate.ImageRef, false)
	}
	return fmt.Errorf("%w: %s", cache.ErrCacheNotFound, item.CacheID)
}
