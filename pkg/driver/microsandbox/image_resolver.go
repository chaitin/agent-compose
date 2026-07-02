package microsandboxdriver

import (
	drivertypes "agent-compose/pkg/driver/types"
	"context"
	"strings"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/imagecache"
)

type microsandboxRootFSResult struct {
	ImageID     string
	ResolvedRef string
	RootFSPath  string
}

type microsandboxImageResolverOps struct {
	dockerAvailable   func(context.Context) bool
	dockerMaterialize func(context.Context, string) (microsandboxRootFSResult, bool, error)
	ociMaterialize    func(context.Context, string) (microsandboxRootFSResult, bool, error)
}

func resolveMicrosandboxRootFS(ctx context.Context, imageRef string, ops microsandboxImageResolverOps) (microsandboxRootFSResult, bool, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return microsandboxRootFSResult{}, false, nil
	}
	if ops.dockerAvailable != nil && ops.dockerAvailable(ctx) && ops.dockerMaterialize != nil {
		rootfs, ok, err := ops.dockerMaterialize(ctx, imageRef)
		if err != nil || ok {
			return rootfs, ok, err
		}
	}
	if ops.ociMaterialize == nil {
		return microsandboxRootFSResult{}, false, nil
	}
	return ops.ociMaterialize(ctx, imageRef)
}

func materializeMicrosandboxOCIRootFS(ctx context.Context, config *appconfig.Config, imageRef string) (microsandboxRootFSResult, bool, error) {
	cache, err := imagecache.New(imagecache.Config{
		Root:               drivertypes.ImageCacheRootForDriver(config),
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return microsandboxRootFSResult{}, false, err
	}
	result, err := cache.MaterializeRootFS(ctx, imageRef)
	if imagecache.IsKind(err, imagecache.ErrorKindNotFound) {
		if _, pullErr := cache.Pull(ctx, imagecache.PullRequest{Reference: imageRef}); pullErr != nil {
			return microsandboxRootFSResult{}, false, pullErr
		}
		result, err = cache.MaterializeRootFS(ctx, imageRef)
	}
	if err != nil {
		return microsandboxRootFSResult{}, false, err
	}
	return microsandboxRootFSResult{
		ImageID:     result.ImageID,
		ResolvedRef: result.ResolvedRef,
		RootFSPath:  result.RootFSPath,
	}, true, nil
}
