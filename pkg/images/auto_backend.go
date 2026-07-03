package images

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/client"

	appconfig "agent-compose/pkg/config"
)

const DefaultDockerPingTimeout = 750 * time.Millisecond

type DockerPingFunc func(context.Context) error

type AutoBackendOption func(*AutoBackend)

type AutoBackend struct {
	mode          string
	docker        Backend
	oci           Backend
	pingDocker    DockerPingFunc
	pingTimeout   time.Duration
	lastSelection string
}

func NewAutoBackend(mode string, dockerBackend, ociBackend Backend, options ...AutoBackendOption) *AutoBackend {
	backend := &AutoBackend{
		mode:        mode,
		docker:      dockerBackend,
		oci:         ociBackend,
		pingDocker:  PingDockerDaemon,
		pingTimeout: DefaultDockerPingTimeout,
	}
	for _, option := range options {
		if option != nil {
			option(backend)
		}
	}
	return backend
}

func WithDockerPing(ping DockerPingFunc) AutoBackendOption {
	return func(backend *AutoBackend) {
		backend.pingDocker = ping
	}
}

func WithDockerPingTimeout(timeout time.Duration) AutoBackendOption {
	return func(backend *AutoBackend) {
		backend.pingTimeout = timeout
	}
}

func (b *AutoBackend) Mode() string {
	if b == nil {
		return ""
	}
	return b.mode
}

func (b *AutoBackend) HasDockerBackend() bool {
	return b != nil && b.docker != nil
}

func (b *AutoBackend) HasOCIBackend() bool {
	return b != nil && b.oci != nil
}

func (b *AutoBackend) LastSelection() string {
	if b == nil {
		return ""
	}
	return b.lastSelection
}

func (b *AutoBackend) ListImages(ctx context.Context, req ListRequest) (ListResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return ListResult{}, err
	}
	return backend.ListImages(ctx, req)
}

func (b *AutoBackend) PullImage(ctx context.Context, req PullRequest) (PullResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return PullResult{}, err
	}
	return backend.PullImage(ctx, req)
}

func (b *AutoBackend) InspectImage(ctx context.Context, req InspectRequest) (InspectResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return InspectResult{}, err
	}
	return backend.InspectImage(ctx, req)
}

func (b *AutoBackend) RemoveImage(ctx context.Context, req RemoveRequest) (RemoveResult, error) {
	backend, err := b.backend(ctx)
	if err != nil {
		return RemoveResult{}, err
	}
	return backend.RemoveImage(ctx, req)
}

func (b *AutoBackend) backend(ctx context.Context) (Backend, error) {
	if b == nil {
		return nil, OpError{Op: "select image backend", Err: fmt.Errorf("auto image backend is required")}
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
		return nil, OpError{Op: "select image backend", Err: fmt.Errorf("unsupported image store mode %q", b.mode)}
	}
}

func (b *AutoBackend) requireBackend(backend Backend, name string) (Backend, error) {
	if backend == nil {
		return nil, OpError{Op: "select image backend", Err: fmt.Errorf("%s image backend is required", name)}
	}
	return backend, nil
}

func (b *AutoBackend) dockerAvailable(ctx context.Context) bool {
	if b.docker == nil {
		return false
	}
	ping := b.pingDocker
	if ping == nil {
		ping = PingDockerDaemon
	}
	timeout := b.pingTimeout
	if timeout <= 0 {
		timeout = DefaultDockerPingTimeout
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return ping(pingCtx) == nil
}

func PingDockerDaemon(ctx context.Context) error {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer func() { _ = dockerClient.Close() }()
	_, err = dockerClient.Ping(ctx)
	return err
}
