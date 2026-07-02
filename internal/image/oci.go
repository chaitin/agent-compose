package image

import (
	"slices"
	"strings"
	"time"

	"agent-compose/pkg/imagecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func ociMetadataToProtoImage(image imagecache.ImageMetadata, inspectedAt string) *agentcomposev2.Image {
	repoTags := cleanOCIRefs(image.RepoTags)
	repoDigests := cleanOCIRefs(image.RepoDigests)
	imageID := firstNonEmpty(image.ConfigDigest, image.CacheKey, image.ManifestDigest)
	resolvedRef := firstNonEmpty(firstString(repoDigests), image.ManifestDigest, image.NormalizedRef, imageID)
	return &agentcomposev2.Image{
		ImageId:            imageID,
		ImageRef:           firstNonEmpty(image.RequestedRef, image.NormalizedRef, firstString(repoTags), firstString(repoDigests), imageID),
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
		SizeBytes:        nonNegativeUint64(image.SizeBytes),
		VirtualSizeBytes: nonNegativeUint64(image.SizeBytes),
		CreatedAt:        timeString(image.CreatedAt),
		InspectedAt:      firstNonEmpty(inspectedAt, timeString(image.PulledAt)),
		Dangling:         len(repoTags) == 0 && len(repoDigests) == 0,
		Oci: &agentcomposev2.OCIImageStatus{
			LayoutCached:   image.LayoutCachePath != "",
			RootfsCached:   image.RootFSCachePath != "",
			CacheKey:       image.CacheKey,
			ManifestDigest: image.ManifestDigest,
			ConfigDigest:   image.ConfigDigest,
			MediaType:      image.MediaType,
		},
		Labels: cloneStringMap(image.Labels),
	}
}

func OCIMetadataToProtoImage(image imagecache.ImageMetadata, inspectedAt string) *agentcomposev2.Image {
	return ociMetadataToProtoImage(image, inspectedAt)
}

func cleanOCIRefs(refs []string) []string {
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

func timeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
