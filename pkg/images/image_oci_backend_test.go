package images

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"agent-compose/pkg/imagecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestOCIImageBackendListInspectRemoveWithMetadata(t *testing.T) {
	cache := newAgentcomposeImageCache(t, "")
	image := saveAgentcomposeOCIMetadata(t, cache, "team/app:latest")
	backend := NewOCIImageBackend(cache)
	backend.now = func() time.Time { return time.Date(2026, 6, 11, 13, 14, 15, 0, time.UTC) }

	list, err := backend.ListImages(context.Background(), ImageListRequest{Query: "team"})
	if err != nil {
		t.Fatalf("ListImages returned error: %v", err)
	}
	if len(list.Images) != 1 || list.Images[0].GetStore() != agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE {
		t.Fatalf("ListImages result = %#v", list)
	}
	if list.StoreStatus.GetStore() != agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE || !list.StoreStatus.GetAvailable() || list.StoreStatus.GetEndpoint() != cache.OCILayoutPath() {
		t.Fatalf("store status = %#v", list.StoreStatus)
	}

	inspect, err := backend.InspectImage(context.Background(), ImageInspectRequest{ImageRef: image.ConfigDigest})
	if err != nil {
		t.Fatalf("InspectImage returned error: %v", err)
	}
	if inspect.Image.GetImageId() != image.ConfigDigest || inspect.Image.GetInspectedAt() != "2026-06-11T13:14:15Z" {
		t.Fatalf("InspectImage result = %#v", inspect.Image)
	}

	remove, err := backend.RemoveImage(context.Background(), ImageRemoveRequest{ImageRef: "team/app:latest", PruneChildren: true})
	if err != nil {
		t.Fatalf("RemoveImage returned error: %v", err)
	}
	if remove.ImageRef != "team/app:latest" || len(remove.UntaggedRefs) != 1 || len(remove.DeletedIDs) != 1 || len(remove.Warnings) == 0 {
		t.Fatalf("RemoveImage result = %#v", remove)
	}
}

func TestImageServiceOCIStoreUsesOCIBackend(t *testing.T) {
	called := false
	service := &Service{
		images: &fakeImageBackend{
			listImages: func(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
				t.Fatalf("docker backend should not be used for explicit OCI store")
				return ImageListResult{}, nil
			},
		},
		ociImages: &fakeImageBackend{
			listImages: func(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
				called = true
				return ImageListResult{
					Images: []*agentcomposev2.Image{{ImageId: "sha256:oci", ImageRef: "team/app:latest"}},
					StoreStatus: &agentcomposev2.ImageStoreStatus{
						Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
						Available: true,
						Endpoint:  "/cache/oci",
					},
				}, nil
			},
		},
	}

	resp, err := service.ListImages(context.Background(), connect.NewRequest(&agentcomposev2.ListImagesRequest{
		Store: agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
	}))
	if err != nil {
		t.Fatalf("ListImages returned error: %v", err)
	}
	if !called || len(resp.Msg.GetImages()) != 1 || resp.Msg.GetStoreStatus().GetStore() != agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE {
		t.Fatalf("ListImages response = %#v called=%v", resp.Msg, called)
	}
}

func TestImageServiceOCIStoreRequiresBackendWithoutUnimplemented(t *testing.T) {
	service := &Service{images: &fakeImageBackend{}}
	_, err := service.ListImages(context.Background(), connect.NewRequest(&agentcomposev2.ListImagesRequest{
		Store: agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
	}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("ListImages code = %s, want internal; err=%v", connect.CodeOf(err), err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "unimplemented") {
		t.Fatalf("ListImages returned unimplemented-style error: %v", err)
	}
}

func TestImageServiceMapsOCIBackendErrors(t *testing.T) {
	service := &Service{ociImages: &fakeImageBackend{
		inspectImage: func(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
			return ImageInspectResult{}, imageBackendOpError{
				Op:       "inspect image",
				Endpoint: "/cache/oci",
				ImageRef: req.ImageRef,
				Err:      imagecache.NewError(imagecache.ErrorKindInvalidReference, "inspect", req.ImageRef, errors.New("bad ref")),
			}
		},
	}}
	_, err := service.InspectImage(context.Background(), connect.NewRequest(&agentcomposev2.InspectImageRequest{
		Store:    agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
		ImageRef: "bad ref",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("InspectImage code = %s, want invalid argument; err=%v", connect.CodeOf(err), err)
	}
}

func TestIntegrationOCIImageBackendPullFromLocalRegistry(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	refString := host + "/team/app:latest"

	img := newAgentcomposeRegistryImage(t)
	ref, err := name.ParseReference(refString, name.Insecure)
	if err != nil {
		t.Fatalf("ParseReference returned error: %v", err)
	}
	if err := remote.Write(ref, img, remote.WithContext(ctx)); err != nil {
		t.Fatalf("remote.Write returned error: %v", err)
	}

	cache := newAgentcomposeImageCache(t, host)
	backend := NewOCIImageBackend(cache)
	pull, err := backend.PullImage(ctx, ImagePullRequest{
		ImageRef: refString,
		Platform: &agentcomposev2.ImagePlatform{
			Os:           "linux",
			Architecture: "amd64",
		},
	})
	if err != nil {
		t.Fatalf("PullImage returned error: %v", err)
	}
	if pull.Image.GetStore() != agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE || pull.Image.GetOci().GetManifestDigest() == "" || pull.ResolvedRef == "" {
		t.Fatalf("PullImage result = %#v", pull)
	}
	if len(pull.Progress) == 0 {
		t.Fatalf("PullImage progress was empty")
	}
	inspect, err := backend.InspectImage(ctx, ImageInspectRequest{ImageRef: refString})
	if err != nil {
		t.Fatalf("InspectImage after pull returned error: %v", err)
	}
	if inspect.Image.GetImageId() != pull.Image.GetImageId() {
		t.Fatalf("InspectImage = %#v, pull = %#v", inspect.Image, pull.Image)
	}
}

func newAgentcomposeImageCache(t *testing.T, insecureRegistry string) *imagecache.Cache {
	t.Helper()
	insecure := []string(nil)
	if strings.TrimSpace(insecureRegistry) != "" {
		insecure = []string{insecureRegistry}
	}
	cache, err := imagecache.New(imagecache.Config{
		Root:               filepath.Join(t.TempDir(), "images"),
		InsecureRegistries: insecure,
	})
	if err != nil {
		t.Fatalf("imagecache.New returned error: %v", err)
	}
	return cache
}

func saveAgentcomposeOCIMetadata(t *testing.T, cache *imagecache.Cache, requestedRef string) imagecache.ImageMetadata {
	t.Helper()
	image, err := imagecache.NewImageMetadata(imagecache.MetadataInput{
		RequestedRef:      requestedRef,
		ManifestDigest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ConfigDigest:      "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Platform:          imagecache.Platform{OS: "linux", Architecture: "amd64"},
		MediaType:         string(types.OCIManifestSchema1),
		Labels:            map[string]string{"role": "oci"},
		SizeBytes:         128,
		CreatedAt:         time.Date(2026, 6, 11, 13, 0, 0, 0, time.UTC),
		LayoutCachePath:   cache.OCILayoutPath(),
		DefaultRegistry:   "index.docker.io",
		AdditionalDigests: []string{"index.docker.io/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	if err != nil {
		t.Fatalf("NewImageMetadata returned error: %v", err)
	}
	if err := cache.SaveMetadata(imagecache.MetadataFile{Images: []imagecache.ImageMetadata{image}}); err != nil {
		t.Fatalf("SaveMetadata returned error: %v", err)
	}
	return image
}

func newAgentcomposeRegistryImage(t *testing.T) v1.Image {
	t.Helper()
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatalf("random.Image returned error: %v", err)
	}
	configFile, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile returned error: %v", err)
	}
	configFile.OS = "linux"
	configFile.Architecture = "amd64"
	configFile.Created = v1.Time{Time: time.Date(2026, 6, 11, 14, 0, 0, 0, time.UTC)}
	configFile.Config.Labels = map[string]string{"role": "oci-pull"}
	img, err = mutate.ConfigFile(img, configFile)
	if err != nil {
		t.Fatalf("mutate.ConfigFile returned error: %v", err)
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.OCIConfigJSON)
	return img
}
