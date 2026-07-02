package image

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/client"

	appconfig "agent-compose/internal/config"
)

const defaultDockerPingTimeout = 750 * time.Millisecond

type DockerPingFunc func(context.Context) error

type AutoBackend struct {
	Mode          string
	Docker        Backend
	OCI           Backend
	PingDocker    DockerPingFunc
	PingTimeout   time.Duration
	LastSelection string
}

func NewAutoBackend(mode string, dockerBackend, ociBackend Backend) *AutoBackend {
	return &AutoBackend{
		Mode:        mode,
		Docker:      dockerBackend,
		OCI:         ociBackend,
		PingDocker:  pingDockerDaemon,
		PingTimeout: defaultDockerPingTimeout,
	}
}

func (b *AutoBackend) ListImages(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return ImageListResult{}, err
	}
	return backend.ListImages(ctx, req)
}

func (b *AutoBackend) PullImage(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return ImagePullResult{}, err
	}
	return backend.PullImage(ctx, req)
}

func (b *AutoBackend) InspectImage(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return ImageInspectResult{}, err
	}
	return backend.InspectImage(ctx, req)
}

func (b *AutoBackend) RemoveImage(ctx context.Context, req ImageRemoveRequest) (ImageRemoveResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return ImageRemoveResult{}, err
	}
	return backend.RemoveImage(ctx, req)
}

func (b *AutoBackend) backend(ctx context.Context) (Backend, error) {
	if b == nil {
		return nil, BackendOpError{Op: "select image backend", Err: fmt.Errorf("auto image backend is required")}
	}
	mode := strings.ToLower(strings.TrimSpace(b.Mode))
	if mode == "" {
		mode = appconfig.ImageStoreModeAuto
	}
	switch mode {
	case appconfig.ImageStoreModeDocker:
		b.LastSelection = appconfig.ImageStoreModeDocker
		return b.requireBackend(b.Docker, appconfig.ImageStoreModeDocker)
	case appconfig.ImageStoreModeOCI:
		b.LastSelection = appconfig.ImageStoreModeOCI
		return b.requireBackend(b.OCI, appconfig.ImageStoreModeOCI)
	case appconfig.ImageStoreModeAuto:
		if b.dockerAvailable(ctx) {
			b.LastSelection = appconfig.ImageStoreModeDocker
			return b.requireBackend(b.Docker, appconfig.ImageStoreModeDocker)
		}
		b.LastSelection = appconfig.ImageStoreModeOCI
		return b.requireBackend(b.OCI, appconfig.ImageStoreModeOCI)
	default:
		return nil, BackendOpError{Op: "select image backend", Err: fmt.Errorf("unsupported image store mode %q", b.Mode)}
	}
}

func (b *AutoBackend) requireBackend(backend Backend, name string) (Backend, error) {
	if backend == nil {
		return nil, BackendOpError{Op: "select image backend", Err: fmt.Errorf("%s image backend is required", name)}
	}
	return backend, nil
}

func (b *AutoBackend) dockerAvailable(ctx context.Context) bool {
	if b.Docker == nil {
		return false
	}
	ping := b.PingDocker
	if ping == nil {
		ping = pingDockerDaemon
	}
	timeout := b.PingTimeout
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
