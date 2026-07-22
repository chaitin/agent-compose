//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/imagecache"
	"github.com/docker/docker/client"
)

const (
	microsandboxImageSourceDocker = "docker"
	microsandboxImageSourceOCI    = "oci"
)

// microsandboxImageSource identifies a guest image and knows how to lay its
// filesystem out for base disk construction. Two sources exist because the two
// deployment shapes resolve images through different principals: the Docker
// daemon owns registry credentials in a compose deployment, while a native
// binary daemon may have no Docker daemon at all and resolves images through
// the agent-compose image cache with its own keychain.
type microsandboxImageSource struct {
	Kind        string
	ImageID     string
	ResolvedRef string
	Env         []string

	// materialize lays the image filesystem out under workDir and returns the
	// directory to build from plus a release func.
	//
	// Ownership differs per source and callers must not assume either shape:
	// the Docker source extracts into a private directory it created and the
	// release func removes it, while the OCI source returns the *shared* image
	// cache rootfs that BoxLite also reads, whose release func must do nothing.
	// Removing that directory would strand its ready flag and break unrelated
	// drivers.
	materialize func(ctx context.Context, workDir string) (string, func(), error)
}

type microsandboxImageSourceOps struct {
	dockerAvailable       func(context.Context) bool
	applyDockerPullPolicy func(context.Context, string) error
	dockerSource          func(context.Context, string) (microsandboxImageSource, bool, error)
	ociSource             func(context.Context, string) (microsandboxImageSource, error)
}

func (r *microsandboxRuntime) resolveMicrosandboxImageSource(ctx context.Context, imageRef, pullPolicy string, pullTimeout time.Duration) (microsandboxImageSource, error) {
	return resolveMicrosandboxImageSource(ctx, imageRef, microsandboxImageSourceOps{
		dockerAvailable: dockerDaemonAvailable,
		applyDockerPullPolicy: func(ctx context.Context, imageRef string) error {
			return applyDockerDaemonPullPolicy(ctx, imageRef, pullPolicy, pullTimeout)
		},
		dockerSource: dockerMicrosandboxImageSource,
		ociSource: func(ctx context.Context, imageRef string) (microsandboxImageSource, error) {
			return ociMicrosandboxImageSource(ctx, r.config, imageRef, pullPolicy)
		},
	})
}

// resolveMicrosandboxImageSource prefers the Docker daemon and falls back to
// the image cache, matching how the BoxLite driver resolves the same images so
// that switching RUNTIME_DRIVER does not change whether a deployment without a
// Docker daemon can start sandboxes at all.
//
// The fallback is deliberately loud. It changes which principal's credentials
// resolved the image, so it is logged, and the chosen source is recorded in the
// base disk cache identity and manifest -- the log says a switch happened, the
// identity says which side produced the artifact that is still on disk.
//
// A pull policy failure never falls back. "never" with a missing image is a
// real answer from the configured source, not a reason to ask a different one
// with different credentials; falling back there would silently defeat the
// policy.
func resolveMicrosandboxImageSource(ctx context.Context, imageRef string, ops microsandboxImageSourceOps) (microsandboxImageSource, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return microsandboxImageSource{}, fmt.Errorf("microsandbox guest image is required")
	}
	if ops.ociSource == nil {
		return microsandboxImageSource{}, fmt.Errorf("microsandbox image source resolution requires an image cache source")
	}
	if ops.dockerAvailable != nil && ops.dockerAvailable(ctx) && ops.dockerSource != nil {
		if ops.applyDockerPullPolicy != nil {
			if err := ops.applyDockerPullPolicy(ctx, imageRef); err != nil {
				return microsandboxImageSource{}, fmt.Errorf("microsandbox resolve guest image %s with Docker pull policy: %w", imageRef, err)
			}
		}
		source, ok, err := ops.dockerSource(ctx, imageRef)
		if err != nil {
			return microsandboxImageSource{}, err
		}
		if ok {
			return source, nil
		}
		slog.Warn("agent-compose microsandbox guest image is absent from the Docker daemon; resolving it through the agent-compose image cache, which authenticates with the daemon process credentials instead of the Docker daemon", "image", imageRef)
	} else {
		slog.Warn("agent-compose microsandbox cannot reach the Docker daemon; resolving the guest image through the agent-compose image cache, which authenticates with the daemon process credentials instead of the Docker daemon", "image", imageRef)
	}
	return ops.ociSource(ctx, imageRef)
}

func dockerMicrosandboxImageSource(ctx context.Context, imageRef string) (microsandboxImageSource, bool, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return microsandboxImageSource{}, false, fmt.Errorf("microsandbox create Docker client for image %s: %w", imageRef, err)
	}
	closeClient := true
	defer func() {
		if closeClient {
			_ = dockerClient.Close()
		}
	}()
	resolvedRef, ok, err := resolveLocalDockerImageRef(ctx, dockerClient, imageRef)
	if err != nil {
		return microsandboxImageSource{}, false, fmt.Errorf("microsandbox inspect Docker image %s: %w", imageRef, err)
	}
	if !ok {
		return microsandboxImageSource{}, false, nil
	}
	inspect, err := dockerClient.ImageInspect(ctx, resolvedRef)
	if err != nil {
		return microsandboxImageSource{}, false, fmt.Errorf("microsandbox inspect resolved Docker image %s: %w", resolvedRef, err)
	}
	imageID := strings.TrimPrefix(strings.TrimSpace(inspect.ID), "sha256:")
	if imageID == "" {
		return microsandboxImageSource{}, false, fmt.Errorf("microsandbox resolved Docker image %s has no image id", resolvedRef)
	}
	closeClient = false
	return microsandboxImageSource{
		Kind: microsandboxImageSourceDocker, ImageID: imageID, ResolvedRef: resolvedRef, Env: inspectEnv(inspect),
		materialize: func(ctx context.Context, workDir string) (string, func(), error) {
			defer func() { _ = dockerClient.Close() }()
			exportDir, err := os.MkdirTemp(workDir, ".base-export-*")
			if err != nil {
				return "", nil, fmt.Errorf("create microsandbox image export directory: %w", err)
			}
			release := func() { _ = os.RemoveAll(exportDir) }
			if err := exportDockerImageFilesystem(ctx, dockerClient, resolvedRef, exportDir); err != nil {
				release()
				return "", nil, err
			}
			return exportDir, release, nil
		},
	}, true, nil
}

func ociMicrosandboxImageSource(ctx context.Context, config *appconfig.Config, imageRef, pullPolicy string) (microsandboxImageSource, error) {
	cache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRootForDriver(config),
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return microsandboxImageSource{}, fmt.Errorf("open image cache for microsandbox guest image %s: %w", imageRef, err)
	}
	result, err := materializeMicrosandboxOCIRootFS(ctx, cache, config, imageRef, pullPolicy)
	if err != nil {
		return microsandboxImageSource{}, err
	}
	imageID := strings.TrimPrefix(strings.TrimSpace(result.ImageID), "sha256:")
	if imageID == "" {
		return microsandboxImageSource{}, fmt.Errorf("microsandbox image cache entry for %s has no image id", imageRef)
	}
	return newMicrosandboxOCIImageSource(imageID, result.ResolvedRef, result.RootFSPath, result.Env), nil
}

// newMicrosandboxOCIImageSource wraps an already materialized image cache
// rootfs. Its release func is empty on purpose: that directory is the shared
// `<image cache>/<image id>/rootfs` the BoxLite driver also reads, and removing
// it would strand its ready flag behind an empty path.
func newMicrosandboxOCIImageSource(imageID, resolvedRef, rootfsPath string, env []string) microsandboxImageSource {
	rootfsPath = filepath.Clean(rootfsPath)
	return microsandboxImageSource{
		Kind: microsandboxImageSourceOCI, ImageID: imageID, ResolvedRef: resolvedRef, Env: env,
		materialize: func(context.Context, string) (string, func(), error) {
			return rootfsPath, func() {}, nil
		},
	}
}

type microsandboxOCIRootFS struct {
	ImageID     string
	ResolvedRef string
	RootFSPath  string
	Env         []string
}

// materializeMicrosandboxOCIRootFS applies pullPolicy against the image cache.
// It mirrors the semantics the Docker daemon applies for the docker source so
// that a fallback does not silently change what a policy means.
func materializeMicrosandboxOCIRootFS(ctx context.Context, cache *imagecache.Cache, config *appconfig.Config, imageRef, pullPolicy string) (microsandboxOCIRootFS, error) {
	policy := strings.ToLower(strings.TrimSpace(pullPolicy))

	// always: pull first, bounding only the pull with ImagePullTimeout, then
	// materialize. A failed pull falls back to any cached copy, matching the
	// docker and boxlite paths.
	if policy == "always" {
		pullCtx := ctx
		if config.ImagePullTimeout > 0 {
			var cancel context.CancelFunc
			pullCtx, cancel = context.WithTimeout(ctx, config.ImagePullTimeout)
			defer cancel()
		}
		if _, pullErr := cache.Pull(pullCtx, imagecache.PullRequest{Reference: imageRef}); pullErr != nil {
			result, matErr := cache.MaterializeRootFS(ctx, imageRef)
			if matErr != nil {
				return microsandboxOCIRootFS{}, fmt.Errorf("microsandbox guest image %s: pull failed (%w) and it is not cached locally", imageRef, pullErr)
			}
			return microsandboxOCIRootFSResult(result), nil
		}
		result, err := cache.MaterializeRootFS(ctx, imageRef)
		if err != nil {
			return microsandboxOCIRootFS{}, err
		}
		return microsandboxOCIRootFSResult(result), nil
	}

	result, err := cache.MaterializeRootFS(ctx, imageRef)
	if imagecache.IsKind(err, imagecache.ErrorKindNotFound) {
		if policy == "never" {
			return microsandboxOCIRootFS{}, fmt.Errorf("microsandbox guest image %s: not cached locally (pull_policy=never)", imageRef)
		}
		if _, pullErr := cache.Pull(ctx, imagecache.PullRequest{Reference: imageRef}); pullErr != nil {
			return microsandboxOCIRootFS{}, pullErr
		}
		result, err = cache.MaterializeRootFS(ctx, imageRef)
	}
	if err != nil {
		return microsandboxOCIRootFS{}, err
	}
	return microsandboxOCIRootFSResult(result), nil
}

func microsandboxOCIRootFSResult(result imagecache.MaterializationResult) microsandboxOCIRootFS {
	return microsandboxOCIRootFS{
		ImageID:     result.ImageID,
		ResolvedRef: result.ResolvedRef,
		RootFSPath:  filepath.Clean(result.RootFSPath),
		Env:         result.Env,
	}
}
