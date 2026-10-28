package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/images"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
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
		return nil, ConnectErrorForDomainKind(err, ResourceImage)
	}
	result, err := backend.ListImages(ctx, images.ListRequest{
		Query: strings.TrimSpace(req.Msg.GetQuery()),
		All:   req.Msg.GetAll(),
	})
	if err != nil {
		return nil, ConnectErrorForImageBackend("list images", "", apierror.ReasonImageBackendUnavailable, err)
	}
	page, total, err := paginateList(result.Images, req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.ListImagesResponse{
		Images:      page,
		Total:       total,
		StoreStatus: result.StoreStatus,
	}), nil
}

func (h *ImageHandler) PullImage(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
	imageRef := strings.TrimSpace(req.Msg.GetImageRef())
	if imageRef == "" {
		return nil, WriteErrorNotAppliedForCode(connect.CodeInvalidArgument, ResourceImage, fmt.Errorf("image_ref is required"))
	}
	backend, err := h.backends.ImageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, ConnectErrorForDomainWrite(apierror.NotApplied(err), ResourceImage)
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
		return nil, ConnectErrorForImageBackendWrite("inspect image before pull", imageRef, apierror.ReasonImageBackendUnavailable, err)
	}
	result, err := backend.PullImage(ctx, images.PullRequest{
		ImageRef: imageRef,
		Platform: req.Msg.GetPlatform(),
	})
	if err != nil {
		return nil, ConnectErrorForImageBackendWrite("pull image", imageRef, apierror.ReasonImagePullFailed, err)
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
		return nil, NewOperationError(connect.CodeInvalidArgument, apierror.ReasonInvalidRequest, apierror.EffectUnspecified, fmt.Errorf("image_ref is required"))
	}
	backend, err := h.backends.ImageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, ConnectErrorForDomainKind(err, ResourceImage)
	}
	result, err := backend.InspectImage(ctx, images.InspectRequest{ImageRef: imageRef})
	if err != nil {
		return nil, ConnectErrorForImageBackend("inspect image", imageRef, apierror.ReasonImageBackendUnavailable, err)
	}
	return connect.NewResponse(&agentcomposev2.InspectImageResponse{
		Image:       result.Image,
		StoreStatus: result.StoreStatus,
	}), nil
}

func (h *ImageHandler) RemoveImage(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
	imageRef := strings.TrimSpace(req.Msg.GetImageRef())
	if imageRef == "" {
		return nil, WriteErrorNotAppliedForCode(connect.CodeInvalidArgument, ResourceImage, fmt.Errorf("image_ref is required"))
	}
	backend, err := h.backends.ImageBackendForStore(req.Msg.GetStore())
	if err != nil {
		return nil, ConnectErrorForDomainWrite(apierror.NotApplied(err), ResourceImage)
	}
	result, err := backend.RemoveImage(ctx, images.RemoveRequest{
		ImageRef:      imageRef,
		Force:         req.Msg.GetForce(),
		PruneChildren: req.Msg.GetPruneChildren(),
	})
	if err != nil {
		return nil, ConnectErrorForImageBackendWrite("remove image", imageRef, apierror.ReasonImageBackendUnavailable, err)
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
	PrepareStreamingHeaders(stream.ResponseHeader())
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
		BuildArgs:  req.Msg.GetBuildArgs(),
		Target:     strings.TrimSpace(req.Msg.GetTarget()),
		Platform:   req.Msg.GetPlatform(),
		NoCache:    req.Msg.GetNoCache(),
		Pull:       req.Msg.GetPull(),
	}, stream)
	if err != nil {
		if errors.Is(err, images.ErrBuildUnsupported) {
			return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("build image: %w", err))
		}
		return ConnectErrorForImageBackendWrite("build image", firstNonEmptyImageRef(tags...), apierror.ReasonImageBuildFailed, err)
	}
	return nil
}

// ConnectErrorForImageBackend maps a backend failure for a read RPC: it
// carries a reason but no effect. fallback names the operation class the
// caller was performing, so the mapper never has to parse Op text.
func ConnectErrorForImageBackend(op, imageRef string, fallback apierror.Reason, err error) error {
	code, reason, cause := classifyImageBackendError(op, imageRef, fallback, err)
	if cause == nil {
		return nil
	}
	return NewOperationError(code, reason, apierror.EffectUnspecified, cause)
}

// ConnectErrorForImageBackendWrite maps a backend failure for a write RPC. A
// reference that never existed and a reference the backend rejected changed
// nothing; every other failure may have left the image store partially
// touched, so it reports UNKNOWN. The NOT_APPLIED claim is still routed through
// the apierror marker so that package stays the single source of truth.
func ConnectErrorForImageBackendWrite(op, imageRef string, fallback apierror.Reason, err error) error {
	code, reason, cause := classifyImageBackendError(op, imageRef, fallback, err)
	if cause == nil {
		return nil
	}
	if reason == apierror.ReasonImageNotFound || reason == apierror.ReasonImageReferenceInvalid {
		cause = apierror.NotApplied(cause)
	}
	return NewOperationError(code, reason, apierror.EffectFor(cause), cause)
}

func classifyImageBackendError(op, imageRef string, fallback apierror.Reason, err error) (connect.Code, apierror.Reason, error) {
	if err == nil {
		return 0, apierror.ReasonUnspecified, nil
	}
	if errors.Is(err, context.Canceled) {
		return connect.CodeCanceled, apierror.ReasonOperationCanceled, err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return connect.CodeDeadlineExceeded, apierror.ReasonOperationTimeout, err
	}
	backendErr, kind, ok := images.ClassifyBackendError(err)
	if !ok {
		// The backend could not classify its own failure; that is a daemon
		// bug, not an image condition.
		return connect.CodeInternal, apierror.ReasonInternal, err
	}
	if imageRef != "" && backendErr.ImageRef == "" {
		backendErr.ImageRef = imageRef
	}
	if backendErr.Op == "" {
		backendErr.Op = op
	}
	switch kind {
	case images.ErrorKindNotFound:
		return connect.CodeNotFound, apierror.ReasonImageNotFound, backendErr
	case images.ErrorKindInvalidReference:
		return connect.CodeInvalidArgument, apierror.ReasonImageReferenceInvalid, backendErr
	case images.ErrorKindConflict:
		return connect.CodeFailedPrecondition, apierror.ReasonImageInUse, backendErr
	case images.ErrorKindInternal:
		return connect.CodeInternal, apierror.ReasonInternal, backendErr
	case images.ErrorKindUnavailable:
		return connect.CodeUnavailable, imageFallbackReason(fallback), backendErr
	default:
		return connect.CodeUnavailable, imageFallbackReason(fallback), backendErr
	}
}

// imageFallbackReason keeps an operation-class reason only when it is one of
// the image categories; anything else falls back to the backend category.
func imageFallbackReason(fallback apierror.Reason) apierror.Reason {
	switch fallback {
	case apierror.ReasonImagePullFailed, apierror.ReasonImageBuildFailed, apierror.ReasonImageBackendUnavailable:
		return fallback
	default:
		return apierror.ReasonImageBackendUnavailable
	}
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
