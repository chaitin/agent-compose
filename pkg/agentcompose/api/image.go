package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"agent-compose/pkg/images"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ImageBackendSelector interface {
	ImageBackendForStore(agentcomposev2.ImageStoreKind) (images.Backend, error)
}

type ImageHandler struct {
	backends ImageBackendSelector
}

func NewImageHandler(backends ImageBackendSelector) *ImageHandler {
	return &ImageHandler{backends: backends}
}

func (h *ImageHandler) ListImages(ctx context.Context, req *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error) {
	backend, err := h.backends.ImageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, err
	}
	result, err := backend.ListImages(ctx, images.ListRequest{
		Query: strings.TrimSpace(req.Msg.GetQuery()),
		All:   req.Msg.GetAll(),
	})
	if err != nil {
		return nil, ConnectErrorForImageBackend("list images", "", err)
	}
	images, hasMore, nextOffset := images.PaginateProtoImages(result.Images, req.Msg.GetOffset(), req.Msg.GetLimit())
	return connect.NewResponse(&agentcomposev2.ListImagesResponse{
		Images:      images,
		TotalCount:  uint32(len(result.Images)),
		HasMore:     hasMore,
		NextOffset:  nextOffset,
		StoreStatus: result.StoreStatus,
	}), nil
}

func (h *ImageHandler) PullImage(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
	imageRef := strings.TrimSpace(req.Msg.GetImageRef())
	if imageRef == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_ref is required"))
	}
	backend, err := h.backends.ImageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, err
	}
	inspect, err := backend.InspectImage(ctx, images.InspectRequest{ImageRef: imageRef})
	if err == nil {
		return connect.NewResponse(&agentcomposev2.PullImageResponse{
			Image:       inspect.Image,
			Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
			ResolvedRef: firstNonEmptyImageRef(inspect.Image.GetResolvedRef(), inspect.Image.GetImageRef(), imageRef),
			Warnings:    []string{fmt.Sprintf("skipped pull: image %s already exists locally", imageRef)},
		}), nil
	}
	if !images.IsNotFound(err) {
		return nil, ConnectErrorForImageBackend("inspect image before pull", imageRef, err)
	}
	result, err := backend.PullImage(ctx, images.PullRequest{
		ImageRef: imageRef,
		Platform: req.Msg.GetPlatform(),
	})
	if err != nil {
		return nil, ConnectErrorForImageBackend("pull image", imageRef, err)
	}
	return connect.NewResponse(&agentcomposev2.PullImageResponse{
		Image:       result.Image,
		Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
		ResolvedRef: result.ResolvedRef,
		Progress:    result.Progress,
		Warnings:    result.Warnings,
	}), nil
}

func firstNonEmptyImageRef(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (h *ImageHandler) InspectImage(ctx context.Context, req *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
	imageRef := strings.TrimSpace(req.Msg.GetImageRef())
	if imageRef == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_ref is required"))
	}
	backend, err := h.backends.ImageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, err
	}
	result, err := backend.InspectImage(ctx, images.InspectRequest{ImageRef: imageRef})
	if err != nil {
		return nil, ConnectErrorForImageBackend("inspect image", imageRef, err)
	}
	return connect.NewResponse(&agentcomposev2.InspectImageResponse{
		Image:       result.Image,
		StoreStatus: result.StoreStatus,
	}), nil
}

func (h *ImageHandler) RemoveImage(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
	imageRef := strings.TrimSpace(req.Msg.GetImageRef())
	if imageRef == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image_ref is required"))
	}
	backend, err := h.backends.ImageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, err
	}
	result, err := backend.RemoveImage(ctx, images.RemoveRequest{
		ImageRef:      imageRef,
		Force:         req.Msg.GetForce(),
		PruneChildren: req.Msg.GetPruneChildren(),
	})
	if err != nil {
		return nil, ConnectErrorForImageBackend("remove image", imageRef, err)
	}
	return connect.NewResponse(&agentcomposev2.RemoveImageResponse{
		ImageRef:     result.ImageRef,
		UntaggedRefs: result.UntaggedRefs,
		DeletedIds:   result.DeletedIDs,
		Warnings:     result.Warnings,
	}), nil
}

func (h *ImageHandler) BuildImage(ctx context.Context, req *connect.Request[agentcomposev2.BuildImageRequest], stream *connect.ServerStream[agentcomposev2.BuildImageEvent]) error {
	tags := normalizeImageBuildStrings(req.Msg.GetTags())
	if len(tags) == 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one tag is required"))
	}
	backend, err := h.backends.ImageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return err
	}
	buildBackend, ok := backend.(images.BuildBackend)
	if !ok {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("build image: %w", images.ErrBuildUnsupported))
	}
	_, err = buildBackend.BuildImage(ctx, images.BuildRequest{
		ContextDir: strings.TrimSpace(req.Msg.GetContextDir()),
		Dockerfile: strings.TrimSpace(req.Msg.GetDockerfile()),
		Tags:       tags,
		BuildArgs:  cloneImageBuildArgs(req.Msg.GetBuildArgs()),
		Target:     strings.TrimSpace(req.Msg.GetTarget()),
		Platform:   req.Msg.GetPlatform(),
		NoCache:    req.Msg.GetNoCache(),
		Pull:       req.Msg.GetPull(),
	}, stream)
	if err != nil {
		if errors.Is(err, images.ErrBuildUnsupported) {
			return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("build image: %w", err))
		}
		return ConnectErrorForImageBackend("build image", firstNonEmptyImageRef(tags...), err)
	}
	return nil
}

func ConnectErrorForImageBackend(op, imageRef string, err error) error {
	if err == nil {
		return nil
	}
	if backendErr, kind, ok := images.ClassifyBackendError(err); ok {
		if imageRef != "" && backendErr.ImageRef == "" {
			backendErr.ImageRef = imageRef
		}
		if op != "" && backendErr.Op == "" {
			backendErr.Op = op
		}
		code := connect.CodeUnavailable
		switch kind {
		case images.ErrorKindNotFound:
			code = connect.CodeNotFound
		case images.ErrorKindInvalidReference:
			code = connect.CodeInvalidArgument
		case images.ErrorKindConflict:
			code = connect.CodeFailedPrecondition
		case images.ErrorKindInternal:
			code = connect.CodeInternal
		case images.ErrorKindUnavailable:
			code = connect.CodeUnavailable
		}
		return connect.NewError(code, backendErr)
	}
	return connect.NewError(connect.CodeUnknown, err)
}

func normalizeImageBuildStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneImageBuildArgs(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		result[key] = value
	}
	return result
}
