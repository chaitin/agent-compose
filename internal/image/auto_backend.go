package image

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/client"

	appconfig "agent-compose/pkg/config"
)

const defaultDockerPingTimeout = 750 * time.Millisecond

type DockerPingFunc func(context.Context) error

type AutoImageBackend struct {
	mode          string
	docker        ImageBackend
	oci           ImageBackend
	pingDocker    DockerPingFunc
	pingTimeout   time.Duration
	lastSelection string
}

func NewAutoImageBackend(mode string, dockerBackend, ociBackend ImageBackend) *AutoImageBackend {
	return &AutoImageBackend{
		mode:        mode,
		docker:      dockerBackend,
		oci:         ociBackend,
		pingDocker:  pingDockerDaemon,
		pingTimeout: defaultDockerPingTimeout,
	}
}

func (b *AutoImageBackend) ListImages(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return ImageListResult{}, err
	}
	return backend.ListImages(ctx, req)
}

func (b *AutoImageBackend) PullImage(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return ImagePullResult{}, err
	}
	return backend.PullImage(ctx, req)
}

func (b *AutoImageBackend) InspectImage(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return ImageInspectResult{}, err
	}
	return backend.InspectImage(ctx, req)
}

func (b *AutoImageBackend) RemoveImage(ctx context.Context, req ImageRemoveRequest) (ImageRemoveResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return ImageRemoveResult{}, err
	}
	return backend.RemoveImage(ctx, req)
}

func (b *AutoImageBackend) backend(ctx context.Context) (ImageBackend, error) {
	if b == nil {
		return nil, imageBackendOpError{Op: "select image backend", Err: fmt.Errorf("auto image backend is required")}
	}
	mode := strings.ToLower(strings.TrimSpace(b.mode))
	if mode == "" {
		mode = appconfig.ImageStoreModeAuto
	}
	switch mode {
	case appconfig.ImageStoreModeDocker:
		b.lastSelection = appconfig.ImageStoreModeDocker
		return b.requireBackend(b.docker, appconfig.ImageStoreModeDocker)
	case appconfig.ImageStoreModeOCI:
		b.lastSelection = appconfig.ImageStoreModeOCI
		return b.requireBackend(b.oci, appconfig.ImageStoreModeOCI)
	case appconfig.ImageStoreModeAuto:
		if b.dockerAvailable(ctx) {
			b.lastSelection = appconfig.ImageStoreModeDocker
			return b.requireBackend(b.docker, appconfig.ImageStoreModeDocker)
		}
		b.lastSelection = appconfig.ImageStoreModeOCI
		return b.requireBackend(b.oci, appconfig.ImageStoreModeOCI)
	default:
		return nil, imageBackendOpError{Op: "select image backend", Err: fmt.Errorf("unsupported image store mode %q", b.mode)}
	}
}

func (b *AutoImageBackend) requireBackend(backend ImageBackend, name string) (ImageBackend, error) {
	if backend == nil {
		return nil, imageBackendOpError{Op: "select image backend", Err: fmt.Errorf("%s image backend is required", name)}
	}
	return backend, nil
}

func (b *AutoImageBackend) dockerAvailable(ctx context.Context) bool {
	if b.docker == nil {
		return false
	}
	ping := b.pingDocker
	if ping == nil {
		ping = pingDockerDaemon
	}
	timeout := b.pingTimeout
	if timeout <= 0 {
		timeout = defaultDockerPingTimeout
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return ping(pingCtx) == nil
}

func pingDockerDaemon(ctx context.Context) error {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer func() { _ = dockerClient.Close() }()
	_, err = dockerClient.Ping(ctx)
	return err
}
