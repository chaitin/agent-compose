package images

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agent-compose/pkg/imagecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type OCIImageBackend struct {
	cache *imagecache.Cache
	now   func() time.Time
}

func NewOCIImageBackend(cache *imagecache.Cache) *OCIImageBackend {
	return &OCIImageBackend{
		cache: cache,
		now:   time.Now,
	}
}

func (b *OCIImageBackend) Cache() *imagecache.Cache {
	if b == nil {
		return nil
	}
	return b.cache
}

func (b *OCIImageBackend) ListImages(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
	cache, err := b.requireCache()
	if err != nil {
		return ImageListResult{}, err
	}
	result, err := cache.List(ctx, imagecache.ListRequest{
		Query: req.Query,
		All:   req.All,
	})
	if err != nil {
		return ImageListResult{}, b.wrapError("list images", "", err)
	}
	images := make([]*agentcomposev2.Image, 0, len(result.Images))
	for _, image := range result.Images {
		images = append(images, ociMetadataToProtoImage(image, b.inspectedAt()))
	}
	return ImageListResult{
		Images:      images,
		StoreStatus: b.storeStatus(),
	}, nil
}

func (b *OCIImageBackend) PullImage(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
	cache, err := b.requireCache()
	if err != nil {
		return ImagePullResult{}, err
	}
	imageRef := strings.TrimSpace(req.ImageRef)
	result, err := cache.Pull(ctx, imagecache.PullRequest{
		Reference: imageRef,
		Platform:  imageCachePlatform(req.Platform),
	})
	if err != nil {
		return ImagePullResult{}, b.wrapError("pull image", imageRef, err)
	}
	progress := make([]*agentcomposev2.ImagePullProgress, 0, len(result.Progress))
	for _, event := range result.Progress {
		progress = append(progress, &agentcomposev2.ImagePullProgress{
			Status:       event.Message,
			CurrentBytes: nonNegativeUint64(event.CurrentBytes),
			TotalBytes:   nonNegativeUint64(event.TotalBytes),
		})
	}
	return ImagePullResult{
		Image:       ociMetadataToProtoImage(result.Image, b.inspectedAt()),
		ResolvedRef: firstNonEmpty(result.ResolvedRef, result.Image.NormalizedRef, imageRef),
		Progress:    progress,
	}, nil
}

func (b *OCIImageBackend) InspectImage(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
	cache, err := b.requireCache()
	if err != nil {
		return ImageInspectResult{}, err
	}
	imageRef := strings.TrimSpace(req.ImageRef)
	result, err := cache.Inspect(ctx, imagecache.InspectRequest{Reference: imageRef})
	if err != nil {
		return ImageInspectResult{}, b.wrapError("inspect image", imageRef, err)
	}
	return ImageInspectResult{
		Image:       ociMetadataToProtoImage(result.Image, b.inspectedAt()),
		StoreStatus: b.storeStatus(),
	}, nil
}

func (b *OCIImageBackend) RemoveImage(ctx context.Context, req ImageRemoveRequest) (ImageRemoveResult, error) {
	cache, err := b.requireCache()
	if err != nil {
		return ImageRemoveResult{}, err
	}
	imageRef := strings.TrimSpace(req.ImageRef)
	result, err := cache.Remove(ctx, imagecache.RemoveRequest{
		Reference:     imageRef,
		Force:         req.Force,
		PruneChildren: req.PruneChildren,
	})
	if err != nil {
		return ImageRemoveResult{}, b.wrapError("remove image", imageRef, err)
	}
	return ImageRemoveResult{
		ImageRef:     imageRef,
		UntaggedRefs: result.UntaggedRefs,
		DeletedIDs:   result.DeletedIDs,
		Warnings:     result.Warnings,
	}, nil
}

func (b *OCIImageBackend) requireCache() (*imagecache.Cache, error) {
	if b == nil || b.cache == nil {
		return nil, imageBackendOpError{Op: "connect OCI image cache", Err: fmt.Errorf("OCI image cache is required")}
	}
	return b.cache, nil
}

func (b *OCIImageBackend) storeStatus() *agentcomposev2.ImageStoreStatus {
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

func (b *OCIImageBackend) inspectedAt() string {
	now := time.Now
	if b != nil && b.now != nil {
		now = b.now
	}
	return now().UTC().Format(time.RFC3339Nano)
}

func (b *OCIImageBackend) wrapError(op, imageRef string, err error) error {
	endpoint := ""
	if b != nil && b.cache != nil {
		endpoint = b.cache.OCILayoutPath()
	}
	return imageBackendOpError{Op: op, Endpoint: endpoint, ImageRef: imageRef, Err: err}
}

func imageCachePlatform(platform *agentcomposev2.ImagePlatform) imagecache.Platform {
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
