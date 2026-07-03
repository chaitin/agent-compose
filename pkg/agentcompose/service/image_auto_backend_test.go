package agentcompose

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/images"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestAutoImageBackendUsesDockerWhenAutoPingSucceeds(t *testing.T) {
	dockerCalled := false
	ociCalled := false
	backend := images.NewAutoBackend(
		appconfig.ImageStoreModeAuto,
		&fakeImageBackend{listImages: func(ctx context.Context, req images.ListRequest) (images.ListResult, error) {
			dockerCalled = true
			return images.ListResult{StoreStatus: &agentcomposev2.ImageStoreStatus{Store: agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON}}, nil
		}},
		&fakeImageBackend{listImages: func(ctx context.Context, req images.ListRequest) (images.ListResult, error) {
			ociCalled = true
			return images.ListResult{}, nil
		}},
		images.WithDockerPing(func(ctx context.Context) error { return nil }),
		images.WithDockerPingTimeout(time.Second),
	)

	result, err := backend.ListImages(context.Background(), images.ListRequest{})
	if err != nil {
		t.Fatalf("ListImages returned error: %v", err)
	}
	if !dockerCalled || ociCalled || backend.LastSelection() != appconfig.ImageStoreModeDocker || result.StoreStatus.GetStore() != agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON {
		t.Fatalf("selection docker=%v oci=%v last=%q result=%#v", dockerCalled, ociCalled, backend.LastSelection(), result)
	}
}

func TestAutoImageBackendUsesOCIWhenAutoPingFails(t *testing.T) {
	dockerCalled := false
	ociCalled := false
	backend := images.NewAutoBackend(
		appconfig.ImageStoreModeAuto,
		&fakeImageBackend{pullImage: func(ctx context.Context, req images.PullRequest) (images.PullResult, error) {
			dockerCalled = true
			return images.PullResult{}, nil
		}},
		&fakeImageBackend{pullImage: func(ctx context.Context, req images.PullRequest) (images.PullResult, error) {
			ociCalled = true
			return images.PullResult{ResolvedRef: "oci"}, nil
		}},
		images.WithDockerPing(func(ctx context.Context) error { return errors.New("docker unavailable") }),
		images.WithDockerPingTimeout(time.Second),
	)

	result, err := backend.PullImage(context.Background(), images.PullRequest{ImageRef: "team/app:latest"})
	if err != nil {
		t.Fatalf("PullImage returned error: %v", err)
	}
	if dockerCalled || !ociCalled || backend.LastSelection() != appconfig.ImageStoreModeOCI || result.ResolvedRef != "oci" {
		t.Fatalf("selection docker=%v oci=%v last=%q result=%#v", dockerCalled, ociCalled, backend.LastSelection(), result)
	}
}

func TestAutoImageBackendForcedModesDoNotPing(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		run  func(*images.AutoBackend) error
		want string
	}{
		{
			name: appconfig.ImageStoreModeDocker,
			mode: appconfig.ImageStoreModeDocker,
			run: func(backend *images.AutoBackend) error {
				_, err := backend.InspectImage(context.Background(), images.InspectRequest{ImageRef: "team/app:latest"})
				return err
			},
			want: appconfig.ImageStoreModeDocker,
		},
		{
			name: appconfig.ImageStoreModeOCI,
			mode: appconfig.ImageStoreModeOCI,
			run: func(backend *images.AutoBackend) error {
				_, err := backend.RemoveImage(context.Background(), images.RemoveRequest{ImageRef: "team/app:latest"})
				return err
			},
			want: appconfig.ImageStoreModeOCI,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinged := false
			dockerCalled := false
			ociCalled := false
			backend := images.NewAutoBackend(
				tc.mode,
				&fakeImageBackend{
					inspectImage: func(ctx context.Context, req images.InspectRequest) (images.InspectResult, error) {
						dockerCalled = true
						return images.InspectResult{}, nil
					},
				},
				&fakeImageBackend{
					removeImage: func(ctx context.Context, req images.RemoveRequest) (images.RemoveResult, error) {
						ociCalled = true
						return images.RemoveResult{}, nil
					},
				},
				images.WithDockerPing(func(ctx context.Context) error {
					pinged = true
					return nil
				}),
			)
			if err := tc.run(backend); err != nil {
				t.Fatalf("operation returned error: %v", err)
			}
			if pinged || backend.LastSelection() != tc.want {
				t.Fatalf("pinged=%v last=%q want=%q", pinged, backend.LastSelection(), tc.want)
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
		autoImages: &fakeImageBackend{listImages: func(ctx context.Context, req images.ListRequest) (images.ListResult, error) {
			calls = append(calls, "auto")
			return images.ListResult{}, nil
		}},
		images: &fakeImageBackend{pullImage: func(ctx context.Context, req images.PullRequest) (images.PullResult, error) {
			calls = append(calls, "docker")
			return images.PullResult{}, nil
		}},
		ociImages: &fakeImageBackend{inspectImage: func(ctx context.Context, req images.InspectRequest) (images.InspectResult, error) {
			calls = append(calls, "oci")
			return images.InspectResult{}, nil
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
