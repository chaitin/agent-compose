//go:build linux && cgo && boxlitecgo

package driver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/imagecache"
)

func materializeBoxliteOCIImageLayout(ctx context.Context, config *appconfig.Config, imageRef string, pullPolicy string) (boxliteImageLayoutResult, bool, error) {
	cache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRootForDriver(config),
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return boxliteImageLayoutResult{}, false, err
	}

	switch strings.ToLower(strings.TrimSpace(pullPolicy)) {
	case "always":
		// Bound only the pull with ImagePullTimeout (matches the docker path); the
		// subsequent materialize/unpack keeps the parent ctx.
		pullCtx := ctx
		if config.ImagePullTimeout > 0 {
			var pullCancel context.CancelFunc
			pullCtx, pullCancel = context.WithTimeout(ctx, config.ImagePullTimeout)
			defer pullCancel()
		}
		if _, pullErr := cache.Pull(pullCtx, imagecache.PullRequest{Reference: imageRef}); pullErr != nil {
			result, localErr := cache.MaterializeOCILayout(ctx, imageRef)
			if localErr == nil {
				slog.Warn("boxlite guest image pull failed, using cached local image", "image", imageRef, "pull_error", pullErr)
				return boxliteImageLayoutResult{
					ImageID:     result.ImageID,
					ResolvedRef: result.ResolvedRef,
					RootfsPath:  result.LayoutPath,
					Env:         result.Env,
				}, true, nil
			}
			return boxliteImageLayoutResult{}, false, fmt.Errorf("guest image %s: pull failed (%w) and not found locally: %v", imageRef, pullErr, localErr)
		}
		result, err := cache.MaterializeOCILayout(ctx, imageRef)
		if err != nil {
			return boxliteImageLayoutResult{}, false, err
		}
		return boxliteImageLayoutResult{
			ImageID:     result.ImageID,
			ResolvedRef: result.ResolvedRef,
			RootfsPath:  result.LayoutPath,
			Env:         result.Env,
		}, true, nil

	case "never":
		result, err := cache.MaterializeOCILayout(ctx, imageRef)
		if imagecache.IsKind(err, imagecache.ErrorKindNotFound) {
			return boxliteImageLayoutResult{}, false, fmt.Errorf("guest image %s: not found locally (pull_policy=never)", imageRef)
		}
		if err != nil {
			return boxliteImageLayoutResult{}, false, err
		}
		return boxliteImageLayoutResult{
			ImageID:     result.ImageID,
			ResolvedRef: result.ResolvedRef,
			RootfsPath:  result.LayoutPath,
			Env:         result.Env,
		}, true, nil

	default:
		// "missing" or empty: existing behavior
		result, err := cache.MaterializeOCILayout(ctx, imageRef)
		if imagecache.IsKind(err, imagecache.ErrorKindNotFound) {
			if _, pullErr := cache.Pull(ctx, imagecache.PullRequest{Reference: imageRef}); pullErr != nil {
				return boxliteImageLayoutResult{}, false, pullErr
			}
			result, err = cache.MaterializeOCILayout(ctx, imageRef)
		}
		if err != nil {
			return boxliteImageLayoutResult{}, false, err
		}
		return boxliteImageLayoutResult{
			ImageID:     result.ImageID,
			ResolvedRef: result.ResolvedRef,
			RootfsPath:  result.LayoutPath,
			Env:         result.Env,
		}, true, nil
	}
}
