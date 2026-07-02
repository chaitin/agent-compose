package image

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/docker/docker/api/types/filters"
	typesimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type DockerImageClient interface {
	ImageList(context.Context, typesimage.ListOptions) ([]typesimage.Summary, error)
	ImagePull(context.Context, string, typesimage.PullOptions) (io.ReadCloser, error)
	ImageInspect(context.Context, string, ...client.ImageInspectOption) (typesimage.InspectResponse, error)
	ImageRemove(context.Context, string, typesimage.RemoveOptions) ([]typesimage.DeleteResponse, error)
	DaemonHost() string
	Close() error
}

type DockerImageBackend struct {
	newClient func() (DockerImageClient, error)
	now       func() time.Time
}

func NewDockerImageBackend() *DockerImageBackend {
	return &DockerImageBackend{
		newClient: func() (DockerImageClient, error) {
			return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		},
		now: time.Now,
	}
}

func NewDockerImageBackendForTest(newClient func() (DockerImageClient, error), now func() time.Time) *DockerImageBackend {
	return &DockerImageBackend{newClient: newClient, now: now}
}

func (b *DockerImageBackend) ListImages(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return ImageListResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	options := typesimage.ListOptions{All: req.All, SharedSize: true}
	if query := strings.TrimSpace(req.Query); query != "" {
		options.Filters = filters.NewArgs(filters.Arg("reference", query))
	}
	images, err := dockerClient.ImageList(ctx, options)
	if err != nil {
		return ImageListResult{}, BackendOpError{Op: "list images", Endpoint: endpoint, Err: err}
	}
	result := make([]*agentcomposev2.Image, 0, len(images))
	for _, image := range images {
		result = append(result, dockerSummaryToProtoImage(image, b.inspectedAt(), ""))
	}
	return ImageListResult{
		Images: result,
		StoreStatus: &agentcomposev2.ImageStoreStatus{
			Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
			Available: true,
			Endpoint:  endpoint,
		},
	}, nil
}

func (b *DockerImageBackend) PullImage(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
	imageRef := strings.TrimSpace(req.ImageRef)
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return ImagePullResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	reader, err := dockerClient.ImagePull(ctx, imageRef, typesimage.PullOptions{Platform: dockerPlatformString(req.Platform)})
	if err != nil {
		return ImagePullResult{}, BackendOpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	progress, err := consumeDockerImagePullProgress(reader)
	closeErr := reader.Close()
	if err != nil {
		return ImagePullResult{}, BackendOpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	if closeErr != nil {
		return ImagePullResult{}, BackendOpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: closeErr}
	}

	inspect, err := dockerClient.ImageInspect(ctx, imageRef)
	if err != nil {
		return ImagePullResult{}, BackendOpError{Op: "inspect pulled image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	image := dockerInspectToProtoImage(inspect, b.inspectedAt(), imageRef)
	return ImagePullResult{
		Image:       image,
		ResolvedRef: firstNonEmpty(image.GetResolvedRef(), imageRef),
		Progress:    progress,
	}, nil
}

func (b *DockerImageBackend) InspectImage(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
	imageRef := strings.TrimSpace(req.ImageRef)
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return ImageInspectResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	image, err := dockerClient.ImageInspect(ctx, imageRef)
	if err != nil {
		return ImageInspectResult{}, BackendOpError{Op: "inspect image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	return ImageInspectResult{
		Image: dockerInspectToProtoImage(image, b.inspectedAt(), imageRef),
		StoreStatus: &agentcomposev2.ImageStoreStatus{
			Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
			Available: true,
			Endpoint:  endpoint,
		},
	}, nil
}

func (b *DockerImageBackend) RemoveImage(ctx context.Context, req ImageRemoveRequest) (ImageRemoveResult, error) {
	imageRef := strings.TrimSpace(req.ImageRef)
	dockerClient, endpoint, err := b.client()
	if err != nil {
		return ImageRemoveResult{}, err
	}
	defer func() { _ = dockerClient.Close() }()

	deleted, err := dockerClient.ImageRemove(ctx, imageRef, typesimage.RemoveOptions{
		Force:         req.Force,
		PruneChildren: req.PruneChildren,
	})
	if err != nil {
		return ImageRemoveResult{}, BackendOpError{Op: "remove image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	result := ImageRemoveResult{ImageRef: imageRef}
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

func (b *DockerImageBackend) client() (DockerImageClient, string, error) {
	if b == nil || b.newClient == nil {
		return nil, "", BackendOpError{Op: "connect docker daemon", Endpoint: dockerEndpointFromEnv(), Err: fmt.Errorf("docker image client factory is required")}
	}
	dockerClient, err := b.newClient()
	endpoint := dockerEndpointFromEnv()
	if dockerClient != nil && strings.TrimSpace(dockerClient.DaemonHost()) != "" {
		endpoint = dockerClient.DaemonHost()
	}
	if err != nil {
		return nil, "", BackendOpError{Op: "connect docker daemon", Endpoint: endpoint, Err: err}
	}
	return dockerClient, endpoint, nil
}

func (b *DockerImageBackend) inspectedAt() string {
	now := time.Now
	if b != nil && b.now != nil {
		now = b.now
	}
	return now().UTC().Format(time.RFC3339Nano)
}

func dockerSummaryToProtoImage(image typesimage.Summary, inspectedAt, imageRef string) *agentcomposev2.Image {
	repoTags := cleanDockerRefs(image.RepoTags)
	repoDigests := cleanDockerRefs(image.RepoDigests)
	ref := firstNonEmpty(strings.TrimSpace(imageRef), firstString(repoTags), firstString(repoDigests), strings.TrimSpace(image.ID))
	return &agentcomposev2.Image{
		ImageId:            image.ID,
		ImageRef:           ref,
		ResolvedRef:        firstNonEmpty(firstString(repoDigests), firstString(repoTags), strings.TrimSpace(image.ID)),
		RepoTags:           repoTags,
		RepoDigests:        repoDigests,
		Store:              agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
		AvailabilityStatus: agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE,
		SizeBytes:          nonNegativeUint64(image.Size),
		VirtualSizeBytes:   nonNegativeUint64(image.Size),
		CreatedAt:          unixSecondsString(image.Created),
		InspectedAt:        inspectedAt,
		Dangling:           dockerImageDangling(repoTags, repoDigests),
		ContainerCount:     nonNegativeUint64(image.Containers),
		Docker: &agentcomposev2.DockerImageStatus{
			Local:           true,
			ParentId:        image.ParentID,
			SharedSizeBytes: image.SharedSize,
		},
		Labels: cloneStringMap(image.Labels),
	}
}

func dockerInspectToProtoImage(image typesimage.InspectResponse, inspectedAt, imageRef string) *agentcomposev2.Image {
	repoTags := cleanDockerRefs(image.RepoTags)
	repoDigests := cleanDockerRefs(image.RepoDigests)
	labels := map[string]string(nil)
	if image.Config != nil {
		labels = cloneStringMap(image.Config.Labels)
	}
	return &agentcomposev2.Image{
		ImageId:            image.ID,
		ImageRef:           firstNonEmpty(strings.TrimSpace(imageRef), firstString(repoTags), firstString(repoDigests), image.ID),
		ResolvedRef:        firstNonEmpty(firstString(repoDigests), firstString(repoTags), image.ID),
		RepoTags:           repoTags,
		RepoDigests:        repoDigests,
		Store:              agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
		AvailabilityStatus: agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE,
		Platform: &agentcomposev2.ImagePlatform{
			Os:           image.Os,
			Architecture: image.Architecture,
			Variant:      image.Variant,
			OsVersion:    image.OsVersion,
		},
		SizeBytes:        nonNegativeUint64(image.Size),
		VirtualSizeBytes: nonNegativeUint64(image.Size),
		CreatedAt:        image.Created,
		InspectedAt:      inspectedAt,
		Dangling:         dockerImageDangling(repoTags, repoDigests),
		Docker: &agentcomposev2.DockerImageStatus{
			Local:    true,
			ParentId: "",
		},
		Labels: labels,
	}
}

func consumeDockerImagePullProgress(reader io.Reader) ([]*agentcomposev2.ImagePullProgress, error) {
	decoder := json.NewDecoder(reader)
	var progress []*agentcomposev2.ImagePullProgress
	for {
		var payload struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			Progress    string `json:"progress"`
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
			Detail struct {
				Current uint64 `json:"current"`
				Total   uint64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := decoder.Decode(&payload); err != nil {
			if err == io.EOF {
				return progress, nil
			}
			return progress, err
		}
		if payload.Error != "" {
			return progress, errors.New(strings.TrimSpace(payload.Error))
		}
		if payload.ErrorDetail != nil && strings.TrimSpace(payload.ErrorDetail.Message) != "" {
			return progress, errors.New(strings.TrimSpace(payload.ErrorDetail.Message))
		}
		if payload.ID == "" && payload.Status == "" && payload.Progress == "" {
			continue
		}
		progress = append(progress, &agentcomposev2.ImagePullProgress{
			Id:           payload.ID,
			Status:       payload.Status,
			Progress:     payload.Progress,
			CurrentBytes: payload.Detail.Current,
			TotalBytes:   payload.Detail.Total,
		})
	}
}

func cleanDockerRefs(refs []string) []string {
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == "<none>:<none>" || ref == "<none>@<none>" {
			continue
		}
		result = append(result, ref)
	}
	slices.Sort(result)
	return result
}

func dockerImageDangling(tags, digests []string) bool {
	return len(tags) == 0 && len(digests) == 0
}

func unixSecondsString(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.Unix(value, 0).UTC().Format(time.RFC3339Nano)
}

func dockerPlatformString(platform *agentcomposev2.ImagePlatform) string {
	if platform == nil {
		return ""
	}
	parts := []string{strings.TrimSpace(platform.GetOs()), strings.TrimSpace(platform.GetArchitecture())}
	if parts[0] == "" || parts[1] == "" {
		return ""
	}
	if variant := strings.TrimSpace(platform.GetVariant()); variant != "" {
		parts = append(parts, variant)
	}
	return strings.Join(parts, "/")
}

func dockerEndpointFromEnv() string {
	if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); host != "" {
		return host
	}
	if host := strings.TrimSpace(client.DefaultDockerHost); host != "" {
		return host
	}
	return "docker daemon"
}
