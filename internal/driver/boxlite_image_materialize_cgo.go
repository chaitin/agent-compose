//go:build boxlitecgo

package driver

import (
	"context"

	appconfig "agent-compose/internal/config"
	"agent-compose/internal/imagecache"
)

func materializeBoxliteOCIImageLayout(ctx context.Context, config *appconfig.Config, imageRef string) (boxliteImageLayoutResult, bool, error) {
	cache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRootForDriver(config),
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return boxliteImageLayoutResult{}, false, err
	}
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
	}, true, nil
}
