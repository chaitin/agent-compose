package images

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/docker/docker/api/types/filters"
	typesimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type DockerClient interface {
	ImageList(context.Context, typesimage.ListOptions) ([]typesimage.Summary, error)
	ImagePull(context.Context, string, typesimage.PullOptions) (io.ReadCloser, error)
	ImageInspect(context.Context, string, ...client.ImageInspectOption) (typesimage.InspectResponse, error)
	ImageRemove(context.Context, string, typesimage.RemoveOptions) ([]typesimage.DeleteResponse, error)
	DaemonHost() string
	Close() error
}

type DockerClientFactory func() (DockerClient, error)

type DockerBackendOption func(*DockerBackend)

type DockerBackend struct {
	newClient DockerClientFactory
	now       func() time.Time
}

func NewDockerBackend(options ...DockerBackendOption) *DockerBackend {
	backend := &DockerBackend{
		newClient: func() (DockerClient, error) {
			return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		},
		now: time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(backend)
		}
	}
	return backend
}

func WithDockerClientFactory(factory DockerClientFactory) DockerBackendOption {
	return func(backend *DockerBackend) {
		backend.newClient = factory
	}
}

func WithDockerClock(now func() time.Time) DockerBackendOption {
	return func(backend *DockerBackend) {
		backend.now = now
	}
}

func (b *DockerBackend) ListImages(ctx context.Context, req ListRequest) (ListResult, error) {
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return ListResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	options := typesimage.ListOptions{All: req.All, SharedSize: true}
	if query := strings.TrimSpace(req.Query); query != "" {
		options.Filters = filters.NewArgs(filters.Arg("reference", query))
	}
	images, err := dockerClient.ImageList(ctx, options)
	if err != nil {
		return ListResult{}, OpError{Op: "list images", Endpoint: endpoint, Err: err}
	}
	result := make([]*agentcomposev2.Image, 0, len(images))
	for _, image := range images {
		result = append(result, DockerSummaryToProtoImage(image, b.inspectedAt(), ""))
	}
	return ListResult{
		Images: result,
		StoreStatus: &agentcomposev2.ImageStoreStatus{
			Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
			Available: true,
			Endpoint:  endpoint,
		},
	}, nil
}

func (b *DockerBackend) PullImage(ctx context.Context, req PullRequest) (PullResult, error) {
	imageRef := strings.TrimSpace(req.ImageRef)
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return PullResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	reader, err := dockerClient.ImagePull(ctx, imageRef, typesimage.PullOptions{Platform: DockerPlatformString(req.Platform)})
	if err != nil {
		return PullResult{}, OpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	progress, err := ConsumeDockerImagePullProgress(reader)
	closeErr := reader.Close()
	if err != nil {
		return PullResult{}, OpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	if closeErr != nil {
		return PullResult{}, OpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: closeErr}
	}

	inspect, err := dockerClient.ImageInspect(ctx, imageRef)
	if err != nil {
		return PullResult{}, OpError{Op: "inspect pulled image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	image := DockerInspectToProtoImage(inspect, b.inspectedAt(), imageRef)
	return PullResult{
		Image:       image,
		ResolvedRef: FirstNonEmpty(image.GetResolvedRef(), imageRef),
		Progress:    progress,
	}, nil
}

func (b *DockerBackend) InspectImage(ctx context.Context, req InspectRequest) (InspectResult, error) {
	imageRef := strings.TrimSpace(req.ImageRef)
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return InspectResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	image, err := dockerClient.ImageInspect(ctx, imageRef)
	if err != nil {
		return InspectResult{}, OpError{Op: "inspect image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	return InspectResult{
		Image: DockerInspectToProtoImage(image, b.inspectedAt(), imageRef),
		StoreStatus: &agentcomposev2.ImageStoreStatus{
			Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
			Available: true,
			Endpoint:  endpoint,
		},
	}, nil
}

func (b *DockerBackend) RemoveImage(ctx context.Context, req RemoveRequest) (RemoveResult, error) {
	imageRef := strings.TrimSpace(req.ImageRef)
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return RemoveResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	deleted, err := dockerClient.ImageRemove(ctx, imageRef, typesimage.RemoveOptions{
		Force:         req.Force,
		PruneChildren: req.PruneChildren,
	})
	if err != nil {
		return RemoveResult{}, OpError{Op: "remove image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	result := RemoveResult{ImageRef: imageRef}
	for _, item := range deleted {
		if item.Untagged != "" {
			result.UntaggedRefs = append(result.UntaggedRefs, item.Untagged)
		}
		if item.Deleted != "" {
			result.DeletedIDs = append(result.DeletedIDs, item.Deleted)
		}
	}
	slices.Sort(result.UntaggedRefs)
	slices.Sort(result.DeletedIDs)
	return result, nil
}

func (b *DockerBackend) client() (DockerClient, string, error) {
	if b == nil || b.newClient == nil {
		return nil, "", OpError{Op: "connect docker daemon", Endpoint: DockerEndpointFromEnv(), Err: fmt.Errorf("docker image client factory is required")}
	}
	dockerClient, err := b.newClient()
	endpoint := DockerEndpointFromEnv()
	if dockerClient != nil && strings.TrimSpace(dockerClient.DaemonHost()) != "" {
		endpoint = dockerClient.DaemonHost()
	}
	if err != nil {
		return nil, "", OpError{Op: "connect docker daemon", Endpoint: endpoint, Err: err}
	}
	return dockerClient, endpoint, nil
}

func (b *DockerBackend) inspectedAt() string {
	now := time.Now
	if b != nil && b.now != nil {
		now = b.now
	}
	return now().UTC().Format(time.RFC3339Nano)
}
