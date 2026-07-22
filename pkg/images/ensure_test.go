package images

import (
	"context"
	"errors"
	"testing"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/imagecache"
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
