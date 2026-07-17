//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"fmt"
	"strings"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/imagecache"
)

type microsandboxRootFSResult struct {
	ImageID     string
	ResolvedRef string
	RootFSPath  string
	Env         []string
}

type microsandboxImageResolverOps struct {
	dockerAvailable   func(context.Context) bool
	dockerMaterialize func(context.Context, string) (microsandboxRootFSResult, bool, error)
	ociMaterialize    func(context.Context, string) (microsandboxRootFSResult, bool, error)
	// applyDockerPullPolicy refreshes/gates the local docker-daemon image per
	// pullPolicy before dockerMaterialize reads it. Optional; when nil the
	// docker short circuit keeps its prior (pullPolicy-unaware) behavior.
	applyDockerPullPolicy func(context.Context, string) error
}

func resolveMicrosandboxRootFS(ctx context.Context, imageRef string, ops microsandboxImageResolverOps) (microsandboxRootFSResult, bool, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return microsandboxRootFSResult{}, false, nil
	}
	if ops.dockerAvailable != nil && ops.dockerAvailable(ctx) && ops.dockerMaterialize != nil {
		// Apply pullPolicy at the docker-daemon layer first so that
		// pullPolicy=always re-pulls an updated same-tag image (instead of the
		// short circuit silently reusing the stale local copy), and
		// pullPolicy=never fails fast when the image is absent.
		if ops.applyDockerPullPolicy != nil {
			if err := ops.applyDockerPullPolicy(ctx, imageRef); err != nil {
				return microsandboxRootFSResult{}, false, err
			}
		}
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

func materializeMicrosandboxOCIRootFS(ctx context.Context, config *appconfig.Config, imageRef, pullPolicy string) (microsandboxRootFSResult, bool, error) {
	cache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRootForDriver(config),
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return microsandboxRootFSResult{}, false, err
	}

	policy := strings.ToLower(strings.TrimSpace(pullPolicy))

	// always: pull first (best-effort — fall back to any cached copy on failure),
	// then materialize. Bound only the pull with ImagePullTimeout (matches the
	// docker and boxlite OCI paths); materialize keeps the parent ctx.
	if policy == "always" {
		pullCtx := ctx
		if config.ImagePullTimeout > 0 {
			var pullCancel context.CancelFunc
			pullCtx, pullCancel = context.WithTimeout(ctx, config.ImagePullTimeout)
			defer pullCancel()
		}
		if _, pullErr := cache.Pull(pullCtx, imagecache.PullRequest{Reference: imageRef}); pullErr != nil {
			result, matErr := cache.MaterializeRootFS(ctx, imageRef)
			if matErr == nil {
				return microsandboxRootFSResult{ImageID: result.ImageID, ResolvedRef: result.ResolvedRef, RootFSPath: result.RootFSPath, Env: result.Env}, true, nil
			}
			return microsandboxRootFSResult{}, false, fmt.Errorf("guest image %s: pull failed (%w) and not found locally", imageRef, pullErr)
		}
		result, matErr := cache.MaterializeRootFS(ctx, imageRef)
		if matErr != nil {
			return microsandboxRootFSResult{}, false, matErr
		}
		return microsandboxRootFSResult{ImageID: result.ImageID, ResolvedRef: result.ResolvedRef, RootFSPath: result.RootFSPath, Env: result.Env}, true, nil
	}

	result, err := cache.MaterializeRootFS(ctx, imageRef)
	if imagecache.IsKind(err, imagecache.ErrorKindNotFound) {
		// never: do not pull; report not-found.
		if policy == "never" {
			return microsandboxRootFSResult{}, false, fmt.Errorf("guest image %s: not found locally (pull_policy=never)", imageRef)
		}
		// missing/empty: pull only when absent (prior default behavior).
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
		Env:         result.Env,
	}, true, nil
}
