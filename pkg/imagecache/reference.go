package imagecache

import (
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
)

type ReferenceInfo struct {
	RequestedRef  string
	NormalizedRef string
	Repository    string
	Identifier    string
	IsDigest      bool
}

type MetadataInput struct {
	RequestedRef      string
	ManifestDigest    string
	ConfigDigest      string
	Platform          Platform
	MediaType         string
	Labels            map[string]string
	Env               []string
	SizeBytes         int64
	CreatedAt         time.Time
	PulledAt          time.Time
	LayoutCachePath   string
	RootFSCachePath   string
	DefaultRegistry   string
	AdditionalTags    []string
	AdditionalDigests []string
}

func ParseReference(value string) (string, error) {
	info, err := NormalizeReference(value, "")
	if err != nil {
		return "", err
	}
	return info.NormalizedRef, nil
}

func (c *Cache) NormalizeReference(value string) (ReferenceInfo, error) {
	return NormalizeReference(value, c.config.DefaultRegistry)
}

func NormalizeReference(value, defaultRegistry string) (ReferenceInfo, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return ReferenceInfo{}, NewError(ErrorKindInvalidReference, "parse", value, nil)
	}
	options := []name.Option{name.WeakValidation}
	if defaultRegistry = strings.TrimSpace(defaultRegistry); defaultRegistry != "" {
		options = append(options, name.WithDefaultRegistry(defaultRegistry))
	}
	ref, err := name.ParseReference(value, options...)
	if err != nil {
		return ReferenceInfo{}, NewError(ErrorKindInvalidReference, "parse", value, err)
	}
	_, isDigest := ref.(name.Digest)
	return ReferenceInfo{
		RequestedRef:  value,
		NormalizedRef: ref.Name(),
		Repository:    ref.Context().Name(),
		Identifier:    ref.Identifier(),
		IsDigest:      isDigest,
	}, nil
}

func NewImageMetadata(input MetadataInput) (ImageMetadata, error) {
	ref, err := NormalizeReference(input.RequestedRef, input.DefaultRegistry)
	if err != nil {
		return ImageMetadata{}, err
	}
	pulledAt := input.PulledAt
	if pulledAt.IsZero() {
		pulledAt = time.Now().UTC()
	}
	metadata := ImageMetadata{
		CacheKey:        firstNonEmpty(input.ConfigDigest, input.ManifestDigest),
		RequestedRef:    ref.RequestedRef,
		NormalizedRef:   ref.NormalizedRef,
		ManifestDigest:  input.ManifestDigest,
		ConfigDigest:    input.ConfigDigest,
		Platform:        input.Platform,
		MediaType:       input.MediaType,
		Labels:          cloneStringMap(input.Labels),
		Env:             cloneStringSlice(input.Env),
		SizeBytes:       input.SizeBytes,
		CreatedAt:       input.CreatedAt,
		PulledAt:        pulledAt,
		LayoutCachePath: input.LayoutCachePath,
		RootFSCachePath: input.RootFSCachePath,
	}
	if ref.IsDigest {
		metadata.RepoDigests = append(metadata.RepoDigests, ref.NormalizedRef)
	} else {
		metadata.RepoTags = append(metadata.RepoTags, ref.NormalizedRef)
		if input.ManifestDigest != "" {
			metadata.RepoDigests = append(metadata.RepoDigests, ref.Repository+"@"+input.ManifestDigest)
		}
	}
	metadata.RepoTags = appendUnique(metadata.RepoTags, input.AdditionalTags...)
	metadata.RepoDigests = appendUnique(metadata.RepoDigests, input.AdditionalDigests...)
	return metadata, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}

func appendUnique(values []string, additional ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additional))
	result := make([]string, 0, len(values)+len(additional))
	for _, value := range append(values, additional...) {
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
	if len(result) == 0 {
		return nil
	}
	return result
}
