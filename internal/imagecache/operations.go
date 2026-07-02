package imagecache

import (
	"context"
	"fmt"
	"strings"
)

func (c *Cache) List(ctx context.Context, req ListRequest) (ListResult, error) {
	if err := ctx.Err(); err != nil {
		return ListResult{}, NewError(ErrorKindUnavailable, "list", "", err)
	}
	metadata, err := c.LoadMetadata()
	if err != nil {
		return ListResult{}, err
	}
	query := strings.TrimSpace(req.Query)
	images := make([]ImageMetadata, 0, len(metadata.Images))
	for _, image := range metadata.Images {
		if query == "" || matchesQuery(image, query) {
			images = append(images, image)
		}
	}
	return ListResult{Images: images}, nil
}

func (c *Cache) Inspect(ctx context.Context, req InspectRequest) (InspectResult, error) {
	if err := ctx.Err(); err != nil {
		return InspectResult{}, NewError(ErrorKindUnavailable, "inspect", req.Reference, err)
	}
	metadata, err := c.LoadMetadata()
	if err != nil {
		return InspectResult{}, err
	}
	image, ok := lookupImage(metadata.Images, req.Reference)
	if !ok {
		return InspectResult{}, NewError(ErrorKindNotFound, "inspect", req.Reference, fmt.Errorf("image not found"))
	}
	return InspectResult{Image: image}, nil
}

func (c *Cache) Remove(ctx context.Context, req RemoveRequest) (RemoveResult, error) {
	if err := ctx.Err(); err != nil {
		return RemoveResult{}, NewError(ErrorKindUnavailable, "remove", req.Reference, err)
	}
	unlock, err := c.Lock()
	if err != nil {
		return RemoveResult{}, err
	}
	defer func() { _ = unlock() }()

	metadata, err := c.LoadMetadata()
	if err != nil {
		return RemoveResult{}, err
	}
	target, ok := lookupImage(metadata.Images, req.Reference)
	if !ok {
		return RemoveResult{}, NewError(ErrorKindNotFound, "remove", req.Reference, fmt.Errorf("image not found"))
	}
	identity := imageIdentity(target)
	if identity == "" {
		identity = req.Reference
	}
	related := indexesWithIdentity(metadata.Images, identity)
	if len(related) > 1 && !req.Force {
		return RemoveResult{}, NewError(ErrorKindConflict, "remove", req.Reference, fmt.Errorf("image has %d references; use force to remove", len(related)))
	}

	removeIndexes := map[int]struct{}{}
	if req.Force {
		for _, idx := range related {
			removeIndexes[idx] = struct{}{}
		}
	} else {
		idx := indexOfImage(metadata.Images, target)
		if idx >= 0 {
			removeIndexes[idx] = struct{}{}
		}
	}

	result := RemoveResult{}
	remaining := make([]ImageMetadata, 0, len(metadata.Images)-len(removeIndexes))
	for idx, image := range metadata.Images {
		if _, remove := removeIndexes[idx]; remove {
			result.UntaggedRefs = append(result.UntaggedRefs, removedRefName(image))
			continue
		}
		remaining = append(remaining, image)
	}
	if !hasIdentity(remaining, identity) {
		if target.ConfigDigest != "" {
			result.DeletedIDs = append(result.DeletedIDs, target.ConfigDigest)
		} else if target.ManifestDigest != "" {
			result.DeletedIDs = append(result.DeletedIDs, target.ManifestDigest)
		} else if target.CacheKey != "" {
			result.DeletedIDs = append(result.DeletedIDs, target.CacheKey)
		}
	}
	if len(result.DeletedIDs) > 0 {
		result.Warnings = append(result.Warnings, "blob cleanup is deferred; only metadata references were removed")
	}
	if req.PruneChildren {
		result.Warnings = append(result.Warnings, "prune_children is ignored by the OCI cache metadata store")
	}
	metadata.Images = remaining
	if err := c.SaveMetadata(metadata); err != nil {
		return RemoveResult{}, err
	}
	return result, nil
}

func lookupImage(images []ImageMetadata, query string) (ImageMetadata, bool) {
	query = normalizeLookupValue(query)
	if query == "" {
		return ImageMetadata{}, false
	}
	for _, image := range images {
		if imageMatchesLookup(image, query) {
			return image, true
		}
	}
	return ImageMetadata{}, false
}

func matchesQuery(image ImageMetadata, query string) bool {
	query = normalizeLookupValue(query)
	if query == "" {
		return true
	}
	if imageMatchesLookup(image, query) {
		return true
	}
	for _, value := range imageLookupValues(image) {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func imageMatchesLookup(image ImageMetadata, query string) bool {
	for _, value := range imageLookupValues(image) {
		value = normalizeLookupValue(value)
		if value == query {
			return true
		}
		if strings.TrimPrefix(value, "sha256:") == strings.TrimPrefix(query, "sha256:") {
			return true
		}
	}
	return false
}

func imageLookupValues(image ImageMetadata) []string {
	values := []string{
		image.CacheKey,
		image.RequestedRef,
		image.NormalizedRef,
		image.ManifestDigest,
		image.ConfigDigest,
	}
	values = append(values, image.RepoTags...)
	values = append(values, image.RepoDigests...)
	return values
}

func normalizeLookupValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func imageIdentity(image ImageMetadata) string {
	for _, value := range []string{image.ManifestDigest, image.ConfigDigest, image.CacheKey} {
		value = normalizeLookupValue(value)
		if value != "" {
			return value
		}
	}
	return normalizeLookupValue(image.NormalizedRef)
}

func indexesWithIdentity(images []ImageMetadata, identity string) []int {
	identity = normalizeLookupValue(identity)
	indexes := []int{}
	for idx, image := range images {
		if imageIdentity(image) == identity {
			indexes = append(indexes, idx)
		}
	}
	return indexes
}

func hasIdentity(images []ImageMetadata, identity string) bool {
	return len(indexesWithIdentity(images, identity)) > 0
}

func indexOfImage(images []ImageMetadata, target ImageMetadata) int {
	for idx, image := range images {
		if imageIdentity(image) == imageIdentity(target) && image.RequestedRef == target.RequestedRef && image.NormalizedRef == target.NormalizedRef {
			return idx
		}
	}
	return -1
}

func removedRefName(image ImageMetadata) string {
	for _, value := range []string{image.RequestedRef, image.NormalizedRef, image.ManifestDigest, image.ConfigDigest, image.CacheKey} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown"
}
