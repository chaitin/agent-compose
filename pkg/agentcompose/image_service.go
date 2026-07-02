package agentcompose

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	cerrdefs "github.com/containerd/errdefs"

	domainimage "agent-compose/internal/agentcompose/image"
	"agent-compose/pkg/imagecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ImageBackend = domainimage.Backend
type ImageListRequest = domainimage.ImageListRequest
type ImageListResult = domainimage.ImageListResult
type ImagePullRequest = domainimage.ImagePullRequest
type ImagePullResult = domainimage.ImagePullResult
type ImageInspectRequest = domainimage.ImageInspectRequest
type ImageInspectResult = domainimage.ImageInspectResult
type ImageRemoveRequest = domainimage.ImageRemoveRequest
type ImageRemoveResult = domainimage.ImageRemoveResult
type DockerImageBackend = domainimage.DockerImageBackend
type AutoImageBackend = domainimage.AutoBackend
type OCIImageBackend = domainimage.OCIBackend
type DockerImageClient = domainimage.DockerClient
type ImageBackendOpError = domainimage.BackendOpError
type imageBackendOpError = domainimage.BackendOpError

func NewDockerImageBackend() *DockerImageBackend { return domainimage.NewDockerImageBackend() }
func NewAutoImageBackend(mode string, dockerBackend, ociBackend ImageBackend) *AutoImageBackend {
	return domainimage.NewAutoBackend(mode, dockerBackend, ociBackend)
}
func NewOCIImageBackend(cache *imagecache.Cache) *OCIImageBackend {
	return domainimage.NewOCIBackend(cache)
}
func ociMetadataToProtoImage(image imagecache.ImageMetadata, inspectedAt string) *agentcomposev2.Image {
	return domainimage.OCIMetadataToProtoImage(image, inspectedAt)
}
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
