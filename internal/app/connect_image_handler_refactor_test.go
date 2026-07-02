package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	typesimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"agent-compose/pkg/imagecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestImageServiceListImagesUsesBackendAndPaginates(t *testing.T) {
	testImageServiceListImagesUsesBackendAndPaginates(t)
}

func TestIntegrationImageServiceListImagesUsesBackendAndPaginates(t *testing.T) {
	testImageServiceListImagesUsesBackendAndPaginates(t)
}

func TestE2EImageServiceListImagesUsesBackendAndPaginates(t *testing.T) {
	testImageServiceListImagesUsesBackendAndPaginates(t)
}

func testImageServiceListImagesUsesBackendAndPaginates(t *testing.T) {
	t.Helper()
	backend := &fakeImageBackend{
		listImages: func(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
			if req.Query != "agent" || !req.All {
				t.Fatalf("ListImages backend request = %#v", req)
			}
			return ImageListResult{
				Images: []*agentcomposev2.Image{
					{ImageId: "sha256:one", ImageRef: "one:latest"},
					{ImageId: "sha256:two", ImageRef: "two:latest"},
					{ImageId: "sha256:three", ImageRef: "three:latest"},
				},
				StoreStatus: &agentcomposev2.ImageStoreStatus{
					Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
					Available: true,
					Endpoint:  "unix:///var/run/docker.sock",
				},
			}, nil
		},
	}
	service := &Service{images: backend}

	resp, err := service.ListImages(context.Background(), connect.NewRequest(&agentcomposev2.ListImagesRequest{
		Query:  "agent",
		All:    true,
		Offset: 1,
		Limit:  1,
	}))
	if err != nil {
		t.Fatalf("ListImages returned error: %v", err)
	}
	if resp.Msg.GetTotalCount() != 3 || !resp.Msg.GetHasMore() || resp.Msg.GetNextOffset() != 2 {
		t.Fatalf("ListImages page metadata = %#v", resp.Msg)
	}
	if len(resp.Msg.GetImages()) != 1 || resp.Msg.GetImages()[0].GetImageRef() != "two:latest" {
		t.Fatalf("ListImages page = %#v", resp.Msg.GetImages())
	}
	if !resp.Msg.GetStoreStatus().GetAvailable() || resp.Msg.GetStoreStatus().GetEndpoint() == "" {
		t.Fatalf("ListImages store status = %#v", resp.Msg.GetStoreStatus())
	}
}

func TestImageServiceDockerUnavailableErrorIncludesEndpointAndImage(t *testing.T) {
	testImageServiceDockerUnavailableErrorIncludesEndpointAndImage(t)
}

func TestIntegrationImageServiceDockerUnavailableErrorIncludesEndpointAndImage(t *testing.T) {
	testImageServiceDockerUnavailableErrorIncludesEndpointAndImage(t)
}

func TestE2EImageServiceDockerUnavailableErrorIncludesEndpointAndImage(t *testing.T) {
	testImageServiceDockerUnavailableErrorIncludesEndpointAndImage(t)
}

func testImageServiceDockerUnavailableErrorIncludesEndpointAndImage(t *testing.T) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "tcp://docker.example:2375")
	service := &Service{images: NewDockerImageBackendForTest(
		func() (dockerImageClient, error) {
			return nil, errors.New("docker daemon unavailable")
		},
		nil,
	)}

	_, err := service.PullImage(context.Background(), connect.NewRequest(&agentcomposev2.PullImageRequest{ImageRef: "alpine:3.20"}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("PullImage error code = %s, want unavailable; err=%v", connect.CodeOf(err), err)
	}
	for _, want := range []string{"tcp://docker.example:2375", "alpine:3.20", "docker daemon unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("PullImage error %q does not contain %q", err.Error(), want)
		}
	}
}

func TestDockerImageBackendListPullInspectRemove(t *testing.T) {
	testDockerImageBackendListPullInspectRemove(t)
}

func TestIntegrationDockerImageBackendListPullInspectRemove(t *testing.T) {
	testDockerImageBackendListPullInspectRemove(t)
}

func TestE2EDockerImageBackendListPullInspectRemove(t *testing.T) {
	testDockerImageBackendListPullInspectRemove(t)
}

func testDockerImageBackendListPullInspectRemove(t *testing.T) {
	t.Helper()
	fake := &fakeDockerImageClient{
		endpoint: "unix:///tmp/docker.sock",
		listImages: []typesimage.Summary{{
			ID:          "sha256:list",
			RepoTags:    []string{"agent:latest"},
			RepoDigests: []string{"agent@sha256:abc"},
			Created:     1781136000,
			Size:        1024,
			SharedSize:  64,
			Containers:  2,
			Labels:      map[string]string{"role": "test"},
		}},
		pullReader: io.NopCloser(strings.NewReader(`{"id":"layer1","status":"Downloading","progressDetail":{"current":3,"total":9}}` + "\n")),
		inspectImage: typesimage.InspectResponse{
			ID:           "sha256:inspect",
			RepoTags:     []string{"agent:latest"},
			RepoDigests:  []string{"agent@sha256:def"},
			Created:      "2026-06-11T00:00:00Z",
			Architecture: "amd64",
			Os:           "linux",
			Size:         2048,
			Config:       &dockerspec.DockerOCIImageConfig{ImageConfig: ocispec.ImageConfig{Labels: map[string]string{"role": "inspect"}}},
		},
		removeResponse: []typesimage.DeleteResponse{{Untagged: "agent:latest"}, {Deleted: "sha256:inspect"}},
	}
	backend := NewDockerImageBackendForTest(
		func() (dockerImageClient, error) { return fake, nil },
		func() time.Time { return time.Date(2026, 6, 11, 1, 2, 3, 0, time.UTC) },
	)
	ctx := context.Background()

	listResp, err := backend.ListImages(ctx, ImageListRequest{Query: "agent", All: true})
	if err != nil {
		t.Fatalf("ListImages returned error: %v", err)
	}
	if !fake.listOptions.All || fake.listOptions.Filters.Get("reference")[0] != "agent" {
		t.Fatalf("docker list options = %#v", fake.listOptions)
	}
	if len(listResp.Images) != 1 || listResp.Images[0].GetImageId() != "sha256:list" || listResp.Images[0].GetDocker().GetSharedSizeBytes() != 64 {
		t.Fatalf("ListImages result = %#v", listResp.Images)
	}

	pullResp, err := backend.PullImage(ctx, ImagePullRequest{
		ImageRef: "agent:latest",
		Platform: &agentcomposev2.ImagePlatform{
			Os:           "linux",
			Architecture: "amd64",
		},
	})
	if err != nil {
		t.Fatalf("PullImage returned error: %v", err)
	}
	if fake.pullRef != "agent:latest" || fake.pullOptions.Platform != "linux/amd64" {
		t.Fatalf("docker pull request = %q %#v", fake.pullRef, fake.pullOptions)
	}
	if pullResp.ResolvedRef != "agent@sha256:def" || len(pullResp.Progress) != 1 || pullResp.Progress[0].GetCurrentBytes() != 3 {
		t.Fatalf("PullImage result = %#v", pullResp)
	}

	inspectResp, err := backend.InspectImage(ctx, ImageInspectRequest{ImageRef: "agent:latest"})
	if err != nil {
		t.Fatalf("InspectImage returned error: %v", err)
	}
	if inspectResp.Image.GetPlatform().GetArchitecture() != "amd64" || inspectResp.Image.GetLabels()["role"] != "inspect" {
		t.Fatalf("InspectImage result = %#v", inspectResp.Image)
	}

	removeResp, err := backend.RemoveImage(ctx, ImageRemoveRequest{ImageRef: "agent:latest", Force: true, PruneChildren: true})
	if err != nil {
		t.Fatalf("RemoveImage returned error: %v", err)
	}
	if fake.removeRef != "agent:latest" || !fake.removeOptions.Force || !fake.removeOptions.PruneChildren {
		t.Fatalf("docker remove request = %q %#v", fake.removeRef, fake.removeOptions)
	}
	if len(removeResp.UntaggedRefs) != 1 || removeResp.UntaggedRefs[0] != "agent:latest" || len(removeResp.DeletedIDs) != 1 {
		t.Fatalf("RemoveImage result = %#v", removeResp)
	}
}

func TestOCIMetadataToProtoImage(t *testing.T) {
	createdAt := time.Date(2026, 6, 11, 3, 4, 5, 0, time.UTC)
	pulledAt := createdAt.Add(time.Hour)
	image := ociMetadataToProtoImage(imagecache.ImageMetadata{
		CacheKey:        "sha256:config",
		RequestedRef:    "team/app",
		NormalizedRef:   "registry.example/team/app:latest",
		RepoTags:        []string{"registry.example/team/app:stable", "registry.example/team/app:latest"},
		RepoDigests:     []string{"registry.example/team/app@sha256:manifest"},
		ManifestDigest:  "sha256:manifest",
		ConfigDigest:    "sha256:config",
		Platform:        imagecache.Platform{OS: "linux", Architecture: "amd64", Variant: "v3", OSVersion: "6.1"},
		MediaType:       "application/vnd.oci.image.manifest.v1+json",
		Labels:          map[string]string{"role": "oci"},
		SizeBytes:       2048,
		CreatedAt:       createdAt,
		PulledAt:        pulledAt,
		LayoutCachePath: "/cache/oci",
		RootFSCachePath: "/cache/rootfs",
	}, "2026-06-11T05:00:00Z")

	if image.GetImageId() != "sha256:config" || image.GetImageRef() != "team/app" || image.GetResolvedRef() != "registry.example/team/app@sha256:manifest" {
		t.Fatalf("image refs = %#v", image)
	}
	if image.GetStore() != agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE || image.GetDocker() != nil || image.GetOci() == nil {
		t.Fatalf("image store/status = %#v", image)
	}
	if image.GetAvailabilityStatus() != agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE {
		t.Fatalf("availability = %s", image.GetAvailabilityStatus())
	}
	if image.GetPlatform().GetOs() != "linux" || image.GetPlatform().GetArchitecture() != "amd64" || image.GetPlatform().GetVariant() != "v3" || image.GetPlatform().GetOsVersion() != "6.1" {
		t.Fatalf("platform = %#v", image.GetPlatform())
	}
	if image.GetSizeBytes() != 2048 || image.GetVirtualSizeBytes() != 2048 || image.GetCreatedAt() != "2026-06-11T03:04:05Z" || image.GetInspectedAt() != "2026-06-11T05:00:00Z" {
		t.Fatalf("size/time fields = %#v", image)
	}
	if image.GetOci().GetCacheKey() != "sha256:config" || !image.GetOci().GetLayoutCached() || !image.GetOci().GetRootfsCached() || image.GetOci().GetManifestDigest() != "sha256:manifest" || image.GetOci().GetConfigDigest() != "sha256:config" || image.GetOci().GetMediaType() == "" {
		t.Fatalf("oci status = %#v", image.GetOci())
	}
	if image.GetLabels()["role"] != "oci" || image.GetRepoTags()[0] != "registry.example/team/app:latest" {
		t.Fatalf("labels/tags = %#v %#v", image.GetLabels(), image.GetRepoTags())
	}
}

func TestDockerImageBackendOperationErrorsIncludeEndpointAndImage(t *testing.T) {
	testDockerImageBackendOperationErrorsIncludeEndpointAndImage(t)
}

func TestIntegrationDockerImageBackendOperationErrorsIncludeEndpointAndImage(t *testing.T) {
	testDockerImageBackendOperationErrorsIncludeEndpointAndImage(t)
}

func TestE2EDockerImageBackendOperationErrorsIncludeEndpointAndImage(t *testing.T) {
	testDockerImageBackendOperationErrorsIncludeEndpointAndImage(t)
}

func testDockerImageBackendOperationErrorsIncludeEndpointAndImage(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name string
		run  func(*DockerImageBackend) error
	}{
		{
			name: "pull",
			run: func(backend *DockerImageBackend) error {
				_, err := backend.PullImage(context.Background(), ImagePullRequest{ImageRef: "broken:pull"})
				return err
			},
		},
		{
			name: "inspect",
			run: func(backend *DockerImageBackend) error {
				_, err := backend.InspectImage(context.Background(), ImageInspectRequest{ImageRef: "broken:inspect"})
				return err
			},
		},
		{
			name: "remove",
			run: func(backend *DockerImageBackend) error {
				_, err := backend.RemoveImage(context.Background(), ImageRemoveRequest{ImageRef: "broken:remove"})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDockerImageClient{
				endpoint:     "unix:///tmp/docker.sock",
				pullErr:      errors.New("pull failed"),
				inspectErr:   errors.New("inspect failed"),
				removeErr:    errors.New("remove failed"),
				pullReader:   io.NopCloser(strings.NewReader(`{}`)),
				inspectImage: typesimage.InspectResponse{ID: "sha256:unused"},
			}
			backend := NewDockerImageBackendForTest(func() (dockerImageClient, error) { return fake, nil }, nil)
			err := tc.run(backend)
			if err == nil {
				t.Fatal("operation returned nil error")
			}
			for _, want := range []string{"unix:///tmp/docker.sock", "broken:" + tc.name, tc.name + " failed"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("%s error %q does not contain %q", tc.name, err.Error(), want)
				}
			}
		})
	}
}

type fakeImageBackend struct {
	listImages   func(context.Context, ImageListRequest) (ImageListResult, error)
	pullImage    func(context.Context, ImagePullRequest) (ImagePullResult, error)
	inspectImage func(context.Context, ImageInspectRequest) (ImageInspectResult, error)
	removeImage  func(context.Context, ImageRemoveRequest) (ImageRemoveResult, error)
}

func (b *fakeImageBackend) ListImages(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
	if b.listImages == nil {
		return ImageListResult{}, errors.New("ListImages fake is not configured")
	}
	return b.listImages(ctx, req)
}

func (b *fakeImageBackend) PullImage(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
	if b.pullImage == nil {
		return ImagePullResult{}, errors.New("PullImage fake is not configured")
	}
	return b.pullImage(ctx, req)
}

func (b *fakeImageBackend) InspectImage(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
	if b.inspectImage == nil {
		return ImageInspectResult{}, errors.New("InspectImage fake is not configured")
	}
	return b.inspectImage(ctx, req)
}

func (b *fakeImageBackend) RemoveImage(ctx context.Context, req ImageRemoveRequest) (ImageRemoveResult, error) {
	if b.removeImage == nil {
		return ImageRemoveResult{}, errors.New("RemoveImage fake is not configured")
	}
	return b.removeImage(ctx, req)
}

type fakeDockerImageClient struct {
	endpoint       string
	listImages     []typesimage.Summary
	listErr        error
	listOptions    typesimage.ListOptions
	pullReader     io.ReadCloser
	pullErr        error
	pullRef        string
	pullOptions    typesimage.PullOptions
	inspectImage   typesimage.InspectResponse
	inspectErr     error
	inspectRef     string
	removeResponse []typesimage.DeleteResponse
	removeErr      error
	removeRef      string
	removeOptions  typesimage.RemoveOptions
	closed         bool
}

func (c *fakeDockerImageClient) ImageList(ctx context.Context, options typesimage.ListOptions) ([]typesimage.Summary, error) {
	c.listOptions = options
	return c.listImages, c.listErr
}

func (c *fakeDockerImageClient) ImagePull(ctx context.Context, ref string, options typesimage.PullOptions) (io.ReadCloser, error) {
	c.pullRef = ref
	c.pullOptions = options
	if c.pullErr != nil {
		return nil, c.pullErr
	}
	if c.pullReader == nil {
		c.pullReader = io.NopCloser(strings.NewReader(`{}`))
	}
	return c.pullReader, nil
}

func (c *fakeDockerImageClient) ImageInspect(ctx context.Context, imageID string, opts ...client.ImageInspectOption) (typesimage.InspectResponse, error) {
	c.inspectRef = imageID
	return c.inspectImage, c.inspectErr
}

func (c *fakeDockerImageClient) ImageRemove(ctx context.Context, imageID string, options typesimage.RemoveOptions) ([]typesimage.DeleteResponse, error) {
	c.removeRef = imageID
	c.removeOptions = options
	return c.removeResponse, c.removeErr
}

func (c *fakeDockerImageClient) DaemonHost() string {
	return c.endpoint
}

func (c *fakeDockerImageClient) Close() error {
	c.closed = true
	return nil
}
