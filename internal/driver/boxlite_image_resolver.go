package driver

import (
	appconfig "agent-compose/internal/config"
	"context"
	"path/filepath"
	"strings"
)

type boxliteImageLayoutResult struct {
	ImageID     string
	ResolvedRef string
	RootfsPath  string
}

type boxliteImageResolverOps struct {
	dockerAvailable   func(context.Context) bool
	dockerMaterialize func(context.Context, string) (boxliteImageLayoutResult, bool, error)
	ociMaterialize    func(context.Context, string) (boxliteImageLayoutResult, bool, error)
}

func resolveBoxliteImageLayout(ctx context.Context, imageRef string, ops boxliteImageResolverOps) (boxliteImageLayoutResult, bool, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return boxliteImageLayoutResult{}, false, nil
	}
	if ops.dockerAvailable != nil && ops.dockerAvailable(ctx) && ops.dockerMaterialize != nil {
		layout, ok, err := ops.dockerMaterialize(ctx, imageRef)
		if err != nil || ok {
			return layout, ok, err
		}
	}
	if ops.ociMaterialize == nil {
		return boxliteImageLayoutResult{}, false, nil
	}
	return ops.ociMaterialize(ctx, imageRef)
}

func imageCacheRootForDriver(config *appconfig.Config) string {
	if config == nil {
		return filepath.Join(".", "data", "images")
	}
	if root := strings.TrimSpace(config.ImageCacheRoot); root != "" {
		return root
	}
	if dataRoot := strings.TrimSpace(config.DataRoot); dataRoot != "" {
		return filepath.Join(dataRoot, "images")
	}
	return filepath.Join(".", "data", "images")
}
