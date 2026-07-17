package driver

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"path/filepath"
	"strings"
)

type boxliteImageLayoutResult struct {
	ImageID     string
	ResolvedRef string
	RootfsPath  string
	Env         []string
}

type boxliteImageResolverOps struct {
	dockerAvailable   func(context.Context) bool
	dockerMaterialize func(context.Context, string) (boxliteImageLayoutResult, bool, error)
	ociMaterialize    func(context.Context, string) (boxliteImageLayoutResult, bool, error)
	// applyDockerPullPolicy refreshes/gates the local docker-daemon image per
	// pullPolicy before dockerMaterialize reads it. Optional; when nil the
	// docker short circuit keeps its prior (pullPolicy-unaware) behavior.
	applyDockerPullPolicy func(context.Context, string) error
}

func resolveBoxliteImageLayout(ctx context.Context, imageRef string, ops boxliteImageResolverOps) (boxliteImageLayoutResult, bool, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return boxliteImageLayoutResult{}, false, nil
	}
	if ops.dockerAvailable != nil && ops.dockerAvailable(ctx) && ops.dockerMaterialize != nil {
		// Apply pullPolicy at the docker-daemon layer first so pullPolicy=always
		// re-pulls an updated same-tag image (rather than the short circuit
		// reusing the stale local copy), and pullPolicy=never fails fast.
		if ops.applyDockerPullPolicy != nil {
			if err := ops.applyDockerPullPolicy(ctx, imageRef); err != nil {
				return boxliteImageLayoutResult{}, false, err
			}
		}
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
