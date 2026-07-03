package images

import (
	"slices"
	"strings"
	"time"

	"agent-compose/pkg/imagecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func OCIMetadataToProtoImage(image imagecache.ImageMetadata, inspectedAt string) *agentcomposev2.Image {
	repoTags := CleanOCIRefs(image.RepoTags)
	repoDigests := CleanOCIRefs(image.RepoDigests)
	imageID := FirstNonEmpty(image.ConfigDigest, image.CacheKey, image.ManifestDigest)
	resolvedRef := FirstNonEmpty(FirstString(repoDigests), image.ManifestDigest, image.NormalizedRef, imageID)
	return &agentcomposev2.Image{
		ImageId:            imageID,
		ImageRef:           FirstNonEmpty(image.RequestedRef, image.NormalizedRef, FirstString(repoTags), FirstString(repoDigests), imageID),
		ResolvedRef:        resolvedRef,
		RepoTags:           repoTags,
		RepoDigests:        repoDigests,
		Store:              agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
		AvailabilityStatus: agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE,
		Platform: &agentcomposev2.ImagePlatform{
			Os:           image.Platform.OS,
			Architecture: image.Platform.Architecture,
			Variant:      image.Platform.Variant,
			OsVersion:    image.Platform.OSVersion,
		},
		SizeBytes:        NonNegativeUint64(image.SizeBytes),
		VirtualSizeBytes: NonNegativeUint64(image.SizeBytes),
		CreatedAt:        TimeString(image.CreatedAt),
		InspectedAt:      FirstNonEmpty(inspectedAt, TimeString(image.PulledAt)),
		Dangling:         len(repoTags) == 0 && len(repoDigests) == 0,
		Oci: &agentcomposev2.OCIImageStatus{
			LayoutCached:   image.LayoutCachePath != "",
			RootfsCached:   image.RootFSCachePath != "",
			CacheKey:       image.CacheKey,
			ManifestDigest: image.ManifestDigest,
			ConfigDigest:   image.ConfigDigest,
			MediaType:      image.MediaType,
		},
		Labels: CloneStringMap(image.Labels),
	}
}

func CleanOCIRefs(refs []string) []string {
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		result = append(result, ref)
	}
	slices.Sort(result)
	return result
}

func TimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func FirstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func NonNegativeUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func CloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
