package images

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agent-compose/pkg/imagecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type OCIBackend struct {
	cache *imagecache.Cache
	now   func() time.Time
}

type OCIBackendOption func(*OCIBackend)

func NewOCIBackend(cache *imagecache.Cache, options ...OCIBackendOption) *OCIBackend {
	backend := &OCIBackend{
		cache: cache,
		now:   time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(backend)
		}
	}
	return backend
}

func WithOCIClock(now func() time.Time) OCIBackendOption {
	return func(backend *OCIBackend) {
		backend.now = now
	}
}

func (b *OCIBackend) HasCache() bool {
	return b != nil && b.cache != nil
}

func (b *OCIBackend) CacheRoot() string {
	if b == nil || b.cache == nil {
		return ""
	}
	return b.cache.Root()
}

func (b *OCIBackend) ListImages(ctx context.Context, req ListRequest) (ListResult, error) {
	cache, err := b.requireCache()
	if err != nil {
		return ListResult{}, err
	}
	result, err := cache.List(ctx, imagecache.ListRequest{
		Query: req.Query,
		All:   req.All,
	})
	if err != nil {
		return ListResult{}, b.wrapError("list images", "", err)
	}
	images := make([]*agentcomposev2.Image, 0, len(result.Images))
	for _, image := range result.Images {
		images = append(images, OCIMetadataToProtoImage(image, b.inspectedAt()))
	}
	return ListResult{
		Images:      images,
		StoreStatus: b.storeStatus(),
	}, nil
}

func (b *OCIBackend) PullImage(ctx context.Context, req PullRequest) (PullResult, error) {
	cache, err := b.requireCache()
	if err != nil {
		return PullResult{}, err
	}
	imageRef := strings.TrimSpace(req.ImageRef)
	result, err := cache.Pull(ctx, imagecache.PullRequest{
		Reference: imageRef,
		Platform:  ImageCachePlatform(req.Platform),
	})
	if err != nil {
		return PullResult{}, b.wrapError("pull image", imageRef, err)
	}
	progress := make([]*agentcomposev2.ImagePullProgress, 0, len(result.Progress))
	for _, event := range result.Progress {
		progress = append(progress, &agentcomposev2.ImagePullProgress{
			Status:       event.Message,
			CurrentBytes: NonNegativeUint64(event.CurrentBytes),
			TotalBytes:   NonNegativeUint64(event.TotalBytes),
		})
	}
	return PullResult{
		Image:       OCIMetadataToProtoImage(result.Image, b.inspectedAt()),
		ResolvedRef: FirstNonEmpty(result.ResolvedRef, result.Image.NormalizedRef, imageRef),
		Progress:    progress,
	}, nil
}

func (b *OCIBackend) InspectImage(ctx context.Context, req InspectRequest) (InspectResult, error) {
	cache, err := b.requireCache()
	if err != nil {
		return InspectResult{}, err
	}
	imageRef := strings.TrimSpace(req.ImageRef)
	result, err := cache.Inspect(ctx, imagecache.InspectRequest{Reference: imageRef})
	if err != nil {
		return InspectResult{}, b.wrapError("inspect image", imageRef, err)
	}
	return InspectResult{
		Image:       OCIMetadataToProtoImage(result.Image, b.inspectedAt()),
		StoreStatus: b.storeStatus(),
	}, nil
}

func (b *OCIBackend) RemoveImage(ctx context.Context, req RemoveRequest) (RemoveResult, error) {
	cache, err := b.requireCache()
	if err != nil {
		return RemoveResult{}, err
	}
	imageRef := strings.TrimSpace(req.ImageRef)
	result, err := cache.Remove(ctx, imagecache.RemoveRequest{
		Reference:     imageRef,
		Force:         req.Force,
		PruneChildren: req.PruneChildren,
	})
	if err != nil {
		return RemoveResult{}, b.wrapError("remove image", imageRef, err)
	}
	return RemoveResult{
		ImageRef:     imageRef,
		UntaggedRefs: result.UntaggedRefs,
		DeletedIDs:   result.DeletedIDs,
		Warnings:     result.Warnings,
	}, nil
}

func (b *OCIBackend) requireCache() (*imagecache.Cache, error) {
	if b == nil || b.cache == nil {
		return nil, OpError{Op: "connect OCI image cache", Err: fmt.Errorf("OCI image cache is required")}
	}
	return b.cache, nil
}

func (b *OCIBackend) storeStatus() *agentcomposev2.ImageStoreStatus {
	endpoint := ""
	if b != nil && b.cache != nil {
		endpoint = b.cache.OCILayoutPath()
	}
	return &agentcomposev2.ImageStoreStatus{
		Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
		Available: true,
		Endpoint:  endpoint,
	}
}

func (b *OCIBackend) inspectedAt() string {
	now := time.Now
	if b != nil && b.now != nil {
		now = b.now
	}
	return now().UTC().Format(time.RFC3339Nano)
}

func (b *OCIBackend) wrapError(op, imageRef string, err error) error {
	endpoint := ""
	if b != nil && b.cache != nil {
		endpoint = b.cache.OCILayoutPath()
	}
	return OpError{Op: op, Endpoint: endpoint, ImageRef: imageRef, Err: err}
}

func ImageCachePlatform(platform *agentcomposev2.ImagePlatform) imagecache.Platform {
	if platform == nil {
		return imagecache.Platform{}
	}
	return imagecache.Platform{
		OS:           strings.TrimSpace(platform.GetOs()),
		Architecture: strings.TrimSpace(platform.GetArchitecture()),
		Variant:      strings.TrimSpace(platform.GetVariant()),
		OSVersion:    strings.TrimSpace(platform.GetOsVersion()),
	}
}
