package app

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

	"connectrpc.com/connect"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/filters"
	typesimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"

	"agent-compose/pkg/imagecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) ListImages(ctx context.Context, req *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error) {
	backend, err := s.imageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, err
	}
	result, err := backend.ListImages(ctx, ImageListRequest{
		Query: strings.TrimSpace(req.Msg.GetQuery()),
		All:   req.Msg.GetAll(),
	})
	if err != nil {
		return nil, connectErrorForImageBackend("list images", "", err)
	}
	images, hasMore, nextOffset := paginateImages(result.Images, req.Msg.GetOffset(), req.Msg.GetLimit())
	return connect.NewResponse(&agentcomposev2.ListImagesResponse{
		Images:      images,
		TotalCount:  uint32(len(result.Images)),
		HasMore:     hasMore,
		NextOffset:  nextOffset,
		StoreStatus: result.StoreStatus,
	}), nil
}

func (s *Service) PullImage(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
	imageRef := strings.TrimSpace(req.Msg.GetImageRef())
	if imageRef == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_ref is required"))
	}
	backend, err := s.imageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, err
	}
	result, err := backend.PullImage(ctx, ImagePullRequest{
		ImageRef: imageRef,
		Platform: req.Msg.GetPlatform(),
	})
	if err != nil {
		return nil, connectErrorForImageBackend("pull image", imageRef, err)
	}
	return connect.NewResponse(&agentcomposev2.PullImageResponse{
		Image:       result.Image,
		Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
		ResolvedRef: result.ResolvedRef,
		Progress:    result.Progress,
		Warnings:    result.Warnings,
	}), nil
}

func (s *Service) InspectImage(ctx context.Context, req *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
	imageRef := strings.TrimSpace(req.Msg.GetImageRef())
	if imageRef == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_ref is required"))
	}
	backend, err := s.imageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, err
	}
	result, err := backend.InspectImage(ctx, ImageInspectRequest{ImageRef: imageRef})
	if err != nil {
		return nil, connectErrorForImageBackend("inspect image", imageRef, err)
	}
	return connect.NewResponse(&agentcomposev2.InspectImageResponse{
		Image:       result.Image,
		StoreStatus: result.StoreStatus,
	}), nil
}

func (s *Service) RemoveImage(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
	imageRef := strings.TrimSpace(req.Msg.GetImageRef())
	if imageRef == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_ref is required"))
	}
	backend, err := s.imageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, err
	}
	result, err := backend.RemoveImage(ctx, ImageRemoveRequest{
		ImageRef:      imageRef,
		Force:         req.Msg.GetForce(),
		PruneChildren: req.Msg.GetPruneChildren(),
	})
	if err != nil {
		return nil, connectErrorForImageBackend("remove image", imageRef, err)
	}
	return connect.NewResponse(&agentcomposev2.RemoveImageResponse{
		ImageRef:     result.ImageRef,
		UntaggedRefs: result.UntaggedRefs,
		DeletedIds:   result.DeletedIDs,
		Warnings:     result.Warnings,
	}), nil
}

func (s *Service) imageBackendForStore(store agentcomposev2.ImageStoreKind) (ImageBackend, error) {
	switch store {
	case agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_UNSPECIFIED:
		if s.autoImages != nil {
			return s.autoImages, nil
		}
		if s.images == nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("image backend is required"))
		}
		return s.images, nil
	case agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON:
		if s.images == nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("image backend is required"))
		}
		return s.images, nil
	case agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE:
		if s.ociImages == nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("OCI image backend is required"))
		}
		return s.ociImages, nil
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported image store %s", store.String()))
	}
}

type dockerImageClient interface {
	ImageList(context.Context, typesimage.ListOptions) ([]typesimage.Summary, error)
	ImagePull(context.Context, string, typesimage.PullOptions) (io.ReadCloser, error)
	ImageInspect(context.Context, string, ...client.ImageInspectOption) (typesimage.InspectResponse, error)
	ImageRemove(context.Context, string, typesimage.RemoveOptions) ([]typesimage.DeleteResponse, error)
	DaemonHost() string
	Close() error
}

type DockerImageBackend struct {
	newClient func() (dockerImageClient, error)
	now       func() time.Time
}

func NewDockerImageBackend() *DockerImageBackend {
	return &DockerImageBackend{
		newClient: func() (dockerImageClient, error) {
			return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		},
		now: time.Now,
	}
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
		return ImageListResult{}, imageBackendOpError{Op: "list images", Endpoint: endpoint, Err: err}
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
		return ImagePullResult{}, imageBackendOpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	progress, err := consumeDockerImagePullProgress(reader)
	closeErr := reader.Close()
	if err != nil {
		return ImagePullResult{}, imageBackendOpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
	}
	if closeErr != nil {
		return ImagePullResult{}, imageBackendOpError{Op: "pull image", Endpoint: endpoint, ImageRef: imageRef, Err: closeErr}
	}

	inspect, err := dockerClient.ImageInspect(ctx, imageRef)
	if err != nil {
		return ImagePullResult{}, imageBackendOpError{Op: "inspect pulled image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
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
		return ImageInspectResult{}, imageBackendOpError{Op: "inspect image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
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
		return ImageRemoveResult{}, imageBackendOpError{Op: "remove image", Endpoint: endpoint, ImageRef: imageRef, Err: err}
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

func (b *DockerImageBackend) client() (dockerImageClient, string, error) {
	if b == nil || b.newClient == nil {
		return nil, "", imageBackendOpError{Op: "connect docker daemon", Endpoint: dockerEndpointFromEnv(), Err: fmt.Errorf("docker image client factory is required")}
	}
	dockerClient, err := b.newClient()
	endpoint := dockerEndpointFromEnv()
	if dockerClient != nil && strings.TrimSpace(dockerClient.DaemonHost()) != "" {
		endpoint = dockerClient.DaemonHost()
	}
	if err != nil {
		return nil, "", imageBackendOpError{Op: "connect docker daemon", Endpoint: endpoint, Err: err}
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

type imageBackendOpError struct {
	Op       string
	Endpoint string
	ImageRef string
	Err      error
}

func (e imageBackendOpError) Error() string {
	parts := []string{strings.TrimSpace(e.Op)}
	if e.ImageRef != "" {
		parts = append(parts, fmt.Sprintf("image %s", e.ImageRef))
	}
	if e.Endpoint != "" {
		parts = append(parts, fmt.Sprintf("endpoint %s", e.Endpoint))
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return strings.Join(parts, ": ")
}

func (e imageBackendOpError) Unwrap() error {
	return e.Err
}

func connectErrorForImageBackend(op, imageRef string, err error) error {
	if err == nil {
		return nil
	}
	var backendErr imageBackendOpError
	if errors.As(err, &backendErr) {
		if imageRef != "" && backendErr.ImageRef == "" {
			backendErr.ImageRef = imageRef
		}
		if op != "" && backendErr.Op == "" {
			backendErr.Op = op
		}
		code := connect.CodeUnavailable
		if cerrdefs.IsNotFound(backendErr.Err) {
			code = connect.CodeNotFound
		}
		switch imagecache.Kind(backendErr.Err) {
		case imagecache.ErrorKindNotFound:
			code = connect.CodeNotFound
		case imagecache.ErrorKindInvalidReference:
			code = connect.CodeInvalidArgument
		case imagecache.ErrorKindConflict:
			code = connect.CodeFailedPrecondition
		case imagecache.ErrorKindInternal:
			code = connect.CodeInternal
		case imagecache.ErrorKindUnavailable:
			code = connect.CodeUnavailable
		}
		return connect.NewError(code, backendErr)
	}
	return connect.NewError(connect.CodeUnknown, err)
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

func paginateImages(images []*agentcomposev2.Image, offset, limit uint32) ([]*agentcomposev2.Image, bool, uint32) {
	total := uint32(len(images))
	if offset > total {
		offset = total
	}
	if limit == 0 {
		limit = total - offset
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return images[offset:end], end < total, end
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

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func nonNegativeUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
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

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
