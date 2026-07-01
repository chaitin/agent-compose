package images

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestAutoImageBackendUsesDockerWhenAutoPingSucceeds(t *testing.T) {
	dockerCalled := false
	ociCalled := false
	backend := &AutoImageBackend{
		mode: appconfig.ImageStoreModeAuto,
		docker: &fakeImageBackend{listImages: func(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
			dockerCalled = true
			return ImageListResult{StoreStatus: &agentcomposev2.ImageStoreStatus{Store: agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON}}, nil
		}},
		oci: &fakeImageBackend{listImages: func(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
			ociCalled = true
			return ImageListResult{}, nil
		}},
		pingDocker:  func(ctx context.Context) error { return nil },
		pingTimeout: time.Second,
	}

	result, err := backend.ListImages(context.Background(), ImageListRequest{})
	if err != nil {
		t.Fatalf("ListImages returned error: %v", err)
	}
	if !dockerCalled || ociCalled || backend.lastSelection != appconfig.ImageStoreModeDocker || result.StoreStatus.GetStore() != agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON {
		t.Fatalf("selection docker=%v oci=%v last=%q result=%#v", dockerCalled, ociCalled, backend.lastSelection, result)
	}
}

func TestAutoImageBackendUsesOCIWhenAutoPingFails(t *testing.T) {
	dockerCalled := false
	ociCalled := false
	backend := &AutoImageBackend{
		mode: appconfig.ImageStoreModeAuto,
		docker: &fakeImageBackend{pullImage: func(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
			dockerCalled = true
			return ImagePullResult{}, nil
		}},
		oci: &fakeImageBackend{pullImage: func(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
			ociCalled = true
			return ImagePullResult{ResolvedRef: "oci"}, nil
		}},
		pingDocker:  func(ctx context.Context) error { return errors.New("docker unavailable") },
		pingTimeout: time.Second,
	}

	result, err := backend.PullImage(context.Background(), ImagePullRequest{ImageRef: "team/app:latest"})
	if err != nil {
		t.Fatalf("PullImage returned error: %v", err)
	}
	if dockerCalled || !ociCalled || backend.lastSelection != appconfig.ImageStoreModeOCI || result.ResolvedRef != "oci" {
		t.Fatalf("selection docker=%v oci=%v last=%q result=%#v", dockerCalled, ociCalled, backend.lastSelection, result)
	}
}

func TestAutoImageBackendForcedModesDoNotPing(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		run  func(*AutoImageBackend) error
		want string
	}{
		{
			name: appconfig.ImageStoreModeDocker,
			mode: appconfig.ImageStoreModeDocker,
			run: func(backend *AutoImageBackend) error {
				_, err := backend.InspectImage(context.Background(), ImageInspectRequest{ImageRef: "team/app:latest"})
				return err
			},
			want: appconfig.ImageStoreModeDocker,
		},
		{
			name: appconfig.ImageStoreModeOCI,
			mode: appconfig.ImageStoreModeOCI,
			run: func(backend *AutoImageBackend) error {
				_, err := backend.RemoveImage(context.Background(), ImageRemoveRequest{ImageRef: "team/app:latest"})
				return err
			},
			want: appconfig.ImageStoreModeOCI,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinged := false
			dockerCalled := false
			ociCalled := false
			backend := &AutoImageBackend{
				mode: tc.mode,
				docker: &fakeImageBackend{
					inspectImage: func(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
						dockerCalled = true
						return ImageInspectResult{}, nil
					},
				},
				oci: &fakeImageBackend{
					removeImage: func(ctx context.Context, req ImageRemoveRequest) (ImageRemoveResult, error) {
						ociCalled = true
						return ImageRemoveResult{}, nil
					},
				},
				pingDocker: func(ctx context.Context) error {
					pinged = true
					return nil
				},
			}
			if err := tc.run(backend); err != nil {
				t.Fatalf("operation returned error: %v", err)
			}
			if pinged || backend.lastSelection != tc.want {
				t.Fatalf("pinged=%v last=%q want=%q", pinged, backend.lastSelection, tc.want)
			}
			if tc.want == appconfig.ImageStoreModeDocker && !dockerCalled {
				t.Fatalf("docker backend was not called")
			}
			if tc.want == appconfig.ImageStoreModeOCI && !ociCalled {
				t.Fatalf("oci backend was not called")
			}
		})
	}
}

func TestImageServiceStorePriorityWithAutoBackend(t *testing.T) {
	calls := []string{}
	service := &Service{
		autoImages: &fakeImageBackend{listImages: func(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
			calls = append(calls, "auto")
			return ImageListResult{}, nil
		}},
		images: &fakeImageBackend{pullImage: func(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
			calls = append(calls, "docker")
			return ImagePullResult{}, nil
		}},
		ociImages: &fakeImageBackend{inspectImage: func(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
			calls = append(calls, "oci")
			return ImageInspectResult{}, nil
		}},
	}

	if _, err := service.ListImages(context.Background(), connect.NewRequest(&agentcomposev2.ListImagesRequest{})); err != nil {
		t.Fatalf("ListImages returned error: %v", err)
	}
	if _, err := service.PullImage(context.Background(), connect.NewRequest(&agentcomposev2.PullImageRequest{
		Store:    agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
		ImageRef: "team/app:latest",
	})); err != nil {
		t.Fatalf("PullImage returned error: %v", err)
	}
	if _, err := service.InspectImage(context.Background(), connect.NewRequest(&agentcomposev2.InspectImageRequest{
		Store:    agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
		ImageRef: "team/app:latest",
	})); err != nil {
		t.Fatalf("InspectImage returned error: %v", err)
	}
	want := []string{"auto", "docker", "oci"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for idx := range want {
		if calls[idx] != want[idx] {
			t.Fatalf("calls = %#v, want %#v", calls, want)
		}
	}
}
