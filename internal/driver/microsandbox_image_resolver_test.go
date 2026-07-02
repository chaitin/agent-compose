package driver

import (
	"context"
	"errors"
	"testing"
)

func TestResolveMicrosandboxRootFSUsesDockerWhenAvailable(t *testing.T) {
	dockerCalled := false
	ociCalled := false
	result, ok, err := resolveMicrosandboxRootFS(context.Background(), "guest:latest", microsandboxImageResolverOps{
		dockerAvailable: func(ctx context.Context) bool { return true },
		dockerMaterialize: func(ctx context.Context, imageRef string) (microsandboxRootFSResult, bool, error) {
			dockerCalled = true
			return microsandboxRootFSResult{ImageID: "docker-id", ResolvedRef: "guest@sha256:docker", RootFSPath: "/docker/rootfs"}, true, nil
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (microsandboxRootFSResult, bool, error) {
			ociCalled = true
			return microsandboxRootFSResult{}, false, nil
		},
	})
	if err != nil || !ok {
		t.Fatalf("resolveMicrosandboxRootFS = %#v ok=%v err=%v", result, ok, err)
	}
	if !dockerCalled || ociCalled || result.RootFSPath != "/docker/rootfs" {
		t.Fatalf("dockerCalled=%v ociCalled=%v result=%#v", dockerCalled, ociCalled, result)
	}
}

func TestResolveMicrosandboxRootFSUsesOCIWhenDockerUnavailable(t *testing.T) {
	dockerCalled := false
	ociCalled := false
	result, ok, err := resolveMicrosandboxRootFS(context.Background(), "guest:latest", microsandboxImageResolverOps{
		dockerAvailable: func(ctx context.Context) bool { return false },
		dockerMaterialize: func(ctx context.Context, imageRef string) (microsandboxRootFSResult, bool, error) {
			dockerCalled = true
			return microsandboxRootFSResult{}, false, nil
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (microsandboxRootFSResult, bool, error) {
			ociCalled = true
			return microsandboxRootFSResult{ImageID: "oci-id", ResolvedRef: "guest@sha256:oci", RootFSPath: "/cache/rootfs"}, true, nil
		},
	})
	if err != nil || !ok {
		t.Fatalf("resolveMicrosandboxRootFS = %#v ok=%v err=%v", result, ok, err)
	}
	if dockerCalled || !ociCalled || result.RootFSPath != "/cache/rootfs" {
		t.Fatalf("dockerCalled=%v ociCalled=%v result=%#v", dockerCalled, ociCalled, result)
	}
}

func TestResolveMicrosandboxRootFSFallsBackToOCIOnDockerMiss(t *testing.T) {
	ociCalled := false
	result, ok, err := resolveMicrosandboxRootFS(context.Background(), "guest:latest", microsandboxImageResolverOps{
		dockerAvailable: func(ctx context.Context) bool { return true },
		dockerMaterialize: func(ctx context.Context, imageRef string) (microsandboxRootFSResult, bool, error) {
			return microsandboxRootFSResult{}, false, nil
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (microsandboxRootFSResult, bool, error) {
			ociCalled = true
			return microsandboxRootFSResult{ImageID: "oci-id", ResolvedRef: "guest@sha256:oci", RootFSPath: "/cache/rootfs"}, true, nil
		},
	})
	if err != nil || !ok || !ociCalled || result.RootFSPath != "/cache/rootfs" {
		t.Fatalf("resolveMicrosandboxRootFS = %#v ok=%v err=%v ociCalled=%v", result, ok, err, ociCalled)
	}
}

func TestResolveMicrosandboxRootFSPropagatesDockerError(t *testing.T) {
	wantErr := errors.New("docker rootfs failed")
	_, _, err := resolveMicrosandboxRootFS(context.Background(), "guest:latest", microsandboxImageResolverOps{
		dockerAvailable: func(ctx context.Context) bool { return true },
		dockerMaterialize: func(ctx context.Context, imageRef string) (microsandboxRootFSResult, bool, error) {
			return microsandboxRootFSResult{}, false, wantErr
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (microsandboxRootFSResult, bool, error) {
			t.Fatalf("oci materialization should not run after Docker error")
			return microsandboxRootFSResult{}, false, nil
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolveMicrosandboxRootFS err = %v, want %v", err, wantErr)
	}
}
