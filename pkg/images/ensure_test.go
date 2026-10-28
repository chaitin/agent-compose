package images

import (
	"context"
	"errors"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/imagecache"
)

func TestEnsureDriverImageUsesDockerBackendFromAutoBackend(t *testing.T) {
	docker := &ensureBackend{pullErr: errors.New("registry unavailable")}
	oci := &ensureBackend{inspectErr: OpError{
		Op:       "inspect image",
		ImageRef: "guest:latest",
		Err:      imagecache.NewError(imagecache.ErrorKindNotFound, "inspect", "guest:latest", errors.New("missing")),
	}}
	pingCalls := 0
	backend := NewAutoBackend(appconfig.ImageStoreModeAuto, docker, oci, WithDockerPing(func(context.Context) error {
		pingCalls++
		if pingCalls == 1 {
			return errors.New("docker unavailable")
		}
		return nil
	}))

	err := EnsureDriverImage(context.Background(), &appconfig.Config{}, backend, EnsureRequest{
		Driver:      "docker",
		ImageRef:    "guest:latest",
		ProjectName: "project",
		AgentName:   "agent",
	})
	if err != nil {
		t.Fatalf("EnsureDriverImage() error = %v", err)
	}
	if docker.inspectCalls != 1 || docker.pullCalls != 0 {
		t.Fatalf("docker calls inspect=%d pull=%d, want inspect=1 pull=0", docker.inspectCalls, docker.pullCalls)
	}
	if oci.inspectCalls != 0 || oci.pullCalls != 0 {
		t.Fatalf("OCI calls inspect=%d pull=%d, want inspect=0 pull=0", oci.inspectCalls, oci.pullCalls)
	}
	if pingCalls != 0 {
		t.Fatalf("docker ping calls = %d, want 0", pingCalls)
	}
}

func TestEnsureDriverImageCarriesFailureSentinels(t *testing.T) {
	notFound := func(op string) error {
		return OpError{Op: op, ImageRef: "guest:latest", Err: imagecache.NewError(imagecache.ErrorKindNotFound, op, "guest:latest", errors.New("missing"))}
	}
	tests := []struct {
		name    string
		backend *ensureBackend
		want    error
	}{
		{
			name:    "inspect and pull both report not found",
			backend: &ensureBackend{inspectErr: notFound("inspect image"), pullErr: notFound("pull image")},
			want:    ErrImageNotFound,
		},
		{
			name:    "pull fails for another reason",
			backend: &ensureBackend{inspectErr: notFound("inspect image"), pullErr: errors.New("registry unavailable")},
			want:    ErrPullFailed,
		},
		{
			name:    "inspect fails for another reason",
			backend: &ensureBackend{inspectErr: errors.New("daemon unavailable")},
			want:    ErrInspectFailed,
		},
		{
			name: "inspect rejects the reference",
			backend: &ensureBackend{inspectErr: OpError{
				Op:       "inspect image",
				ImageRef: "bad ref",
				Err:      imagecache.NewError(imagecache.ErrorKindInvalidReference, "parse", "bad ref", errors.New("invalid")),
			}},
			want: ErrInvalidReference,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := EnsureDriverImage(context.Background(), &appconfig.Config{}, tc.backend, EnsureRequest{
				Driver:      "docker",
				ImageRef:    "guest:latest",
				ProjectName: "project",
				AgentName:   "agent",
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("EnsureDriverImage() error = %v, want errors.Is(_, %v)", err, tc.want)
			}
		})
	}
}

type ensureBackend struct {
	inspectErr   error
	pullErr      error
	inspectCalls int
	pullCalls    int
}

func (*ensureBackend) ListImages(context.Context, ListRequest) (ListResult, error) {
	return ListResult{}, nil
}

func (b *ensureBackend) PullImage(context.Context, PullRequest) (PullResult, error) {
	b.pullCalls++
	return PullResult{}, b.pullErr
}

func (b *ensureBackend) InspectImage(context.Context, InspectRequest) (InspectResult, error) {
	b.inspectCalls++
	return InspectResult{}, b.inspectErr
}

func (*ensureBackend) RemoveImage(context.Context, RemoveRequest) (RemoveResult, error) {
	return RemoveResult{}, nil
}
