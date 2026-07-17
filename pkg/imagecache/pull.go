package imagecache

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func (c *Cache) Pull(ctx context.Context, req PullRequest) (PullResult, error) {
	if err := ctx.Err(); err != nil {
		return PullResult{}, NewError(ErrorKindUnavailable, "pull", req.Reference, err)
	}
	ref, refInfo, err := c.parseRemoteReference(req.Reference)
	if err != nil {
		return PullResult{}, err
	}
	platform := completePlatform(req.Platform)
	v1Platform := toV1Platform(platform)

	progressCh := make(chan v1.Update, 64)
	progressDone := collectProgress(progressCh)
	remoteOptions := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithPlatform(v1Platform),
		remote.WithProgress(progressCh),
	}

	img, err := remote.Image(ref, remoteOptions...)
	if err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, mapPullError("pull", req.Reference, platform, err)
	}
	if err := ctx.Err(); err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, NewError(ErrorKindUnavailable, "pull", req.Reference, err)
	}

	manifestDigest, err := img.Digest()
	if err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, NewError(ErrorKindUnavailable, "pull", req.Reference, err)
	}
	configDigest, err := img.ConfigName()
	if err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, NewError(ErrorKindUnavailable, "pull", req.Reference, err)
	}
	mediaType, err := img.MediaType()
	if err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, NewError(ErrorKindUnavailable, "pull", req.Reference, err)
	}
	sizeBytes, err := img.Size()
	if err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, NewError(ErrorKindUnavailable, "pull", req.Reference, err)
	}
	configFile, err := img.ConfigFile()
	if err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, NewError(ErrorKindUnavailable, "pull", req.Reference, err)
	}
	metadataPlatform := platformFromConfig(configFile, platform)

	unlock, err := c.Lock()
	if err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, err
	}
	defer func() { _ = unlock() }()

	if err := c.ensureOCILayout(); err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, err
	}
	layoutPath := layout.Path(c.OCILayoutPath())
	if err := layoutPath.AppendImage(img, layout.WithPlatform(v1Platform), layout.WithAnnotations(map[string]string{
		"org.opencontainers.image.ref.name": refInfo.NormalizedRef,
	})); err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, NewError(ErrorKindInternal, "pull", req.Reference, err)
	}

	image, err := NewImageMetadata(MetadataInput{
		RequestedRef:    req.Reference,
		ManifestDigest:  manifestDigest.String(),
		ConfigDigest:    configDigest.String(),
		Platform:        metadataPlatform,
		MediaType:       string(mediaType),
		Labels:          configFile.Config.Labels,
		Env:             configFile.Config.Env,
		SizeBytes:       sizeBytes,
		CreatedAt:       configFile.Created.Time,
		PulledAt:        time.Now().UTC(),
		LayoutCachePath: c.OCILayoutPath(),
		DefaultRegistry: c.config.DefaultRegistry,
	})
	if err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, err
	}

	metadata, err := c.LoadMetadata()
	if err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, err
	}
	metadata.Images = upsertMetadataImage(metadata.Images, image)
	if err := c.SaveMetadata(metadata); err != nil {
		progress := finishProgress(progressCh, progressDone)
		return PullResult{Progress: progress}, err
	}

	progress := finishProgress(progressCh, progressDone)
	progress = append(progress, ProgressEvent{
		Message:      "pulled",
		CurrentBytes: sizeBytes,
		TotalBytes:   sizeBytes,
	})
	return PullResult{
		Image:       image,
		ResolvedRef: refInfo.Repository + "@" + manifestDigest.String(),
		Progress:    progress,
	}, nil
}

func (c *Cache) parseRemoteReference(value string) (name.Reference, ReferenceInfo, error) {
	options := c.referenceOptions(false)
	ref, err := name.ParseReference(strings.TrimSpace(value), options...)
	if err != nil {
		return nil, ReferenceInfo{}, NewError(ErrorKindInvalidReference, "parse", value, err)
	}
	if c.isInsecureRegistry(ref.Context().RegistryStr()) {
		options = c.referenceOptions(true)
		ref, err = name.ParseReference(strings.TrimSpace(value), options...)
		if err != nil {
			return nil, ReferenceInfo{}, NewError(ErrorKindInvalidReference, "parse", value, err)
		}
	}
	_, isDigest := ref.(name.Digest)
	return ref, ReferenceInfo{
		RequestedRef:  strings.TrimSpace(value),
		NormalizedRef: ref.Name(),
		Repository:    ref.Context().Name(),
		Identifier:    ref.Identifier(),
		IsDigest:      isDigest,
	}, nil
}

func (c *Cache) referenceOptions(insecure bool) []name.Option {
	options := []name.Option{name.WeakValidation}
	if defaultRegistry := strings.TrimSpace(c.config.DefaultRegistry); defaultRegistry != "" {
		options = append(options, name.WithDefaultRegistry(defaultRegistry))
	}
	if insecure {
		options = append(options, name.Insecure)
	}
	return options
}

func (c *Cache) isInsecureRegistry(registry string) bool {
	registry = normalizeRegistryHost(registry)
	for _, configured := range c.config.InsecureRegistries {
		if normalizeRegistryHost(configured) == registry {
			return true
		}
	}
	return false
}

func normalizeRegistryHost(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimSuffix(value, "/")
	return value
}

func completePlatform(platform Platform) Platform {
	if strings.TrimSpace(platform.OS) == "" {
		platform.OS = runtime.GOOS
	}
	if strings.TrimSpace(platform.Architecture) == "" {
		platform.Architecture = runtime.GOARCH
	}
	return platform
}

func toV1Platform(platform Platform) v1.Platform {
	return v1.Platform{
		Architecture: platform.Architecture,
		OS:           platform.OS,
		OSVersion:    platform.OSVersion,
		OSFeatures:   append([]string(nil), platform.OSFeatures...),
		Variant:      platform.Variant,
		Features:     append([]string(nil), platform.Features...),
	}
}

func platformFromConfig(config *v1.ConfigFile, fallback Platform) Platform {
	if config == nil {
		return fallback
	}
	platform := Platform{
		OS:           firstNonEmpty(config.OS, fallback.OS),
		Architecture: firstNonEmpty(config.Architecture, fallback.Architecture),
		Variant:      firstNonEmpty(config.Variant, fallback.Variant),
		OSVersion:    firstNonEmpty(config.OSVersion, fallback.OSVersion),
		OSFeatures:   append([]string(nil), config.OSFeatures...),
		Features:     append([]string(nil), fallback.Features...),
	}
	if len(platform.OSFeatures) == 0 {
		platform.OSFeatures = append([]string(nil), fallback.OSFeatures...)
	}
	return platform
}

func (c *Cache) ensureOCILayout() error {
	if err := c.Ensure(); err != nil {
		return err
	}
	if _, err := layout.ImageIndexFromPath(c.OCILayoutPath()); err == nil {
		return nil
	}
	if _, err := layout.Write(c.OCILayoutPath(), empty.Index); err != nil {
		return NewError(ErrorKindInternal, "pull", c.OCILayoutPath(), err)
	}
	return nil
}

func collectProgress(updates <-chan v1.Update) <-chan []ProgressEvent {
	done := make(chan []ProgressEvent, 1)
	go func() {
		events := []ProgressEvent{}
		for update := range updates {
			event := ProgressEvent{
				Message:      "pulling",
				CurrentBytes: update.Complete,
				TotalBytes:   update.Total,
			}
			if update.Error != nil {
				event.Message = update.Error.Error()
			}
			events = append(events, event)
		}
		done <- events
	}()
	return done
}

func finishProgress(updates chan v1.Update, done <-chan []ProgressEvent) []ProgressEvent {
	close(updates)
	return <-done
}

func mapPullError(operation, reference string, platform Platform, err error) error {
	if err == nil {
		return nil
	}
	message := fmt.Errorf("platform %s/%s%s: %w", platform.OS, platform.Architecture, platformVariantSuffix(platform), err)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return NewError(ErrorKindUnavailable, operation, reference, message)
	}
	var transportErr *transport.Error
	if errors.As(err, &transportErr) {
		switch transportErr.StatusCode {
		case http.StatusNotFound:
			return NewError(ErrorKindNotFound, operation, reference, message)
		case http.StatusBadRequest:
			return NewError(ErrorKindInvalidReference, operation, reference, message)
		default:
			return NewError(ErrorKindUnavailable, operation, reference, message)
		}
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "no child with platform") || strings.Contains(lower, "no matching manifest") {
		return NewError(ErrorKindNotFound, operation, reference, message)
	}
	if strings.Contains(lower, "invalid reference") {
		return NewError(ErrorKindInvalidReference, operation, reference, message)
	}
	return NewError(ErrorKindUnavailable, operation, reference, message)
}

func platformVariantSuffix(platform Platform) string {
	if strings.TrimSpace(platform.Variant) == "" {
		return ""
	}
	return "/" + platform.Variant
}

func upsertMetadataImage(images []ImageMetadata, image ImageMetadata) []ImageMetadata {
	for idx, existing := range images {
		if existing.RequestedRef == image.RequestedRef || existing.NormalizedRef == image.NormalizedRef {
			updated := append([]ImageMetadata(nil), images...)
			updated[idx] = image
			return updated
		}
	}
	return append(images, image)
}
