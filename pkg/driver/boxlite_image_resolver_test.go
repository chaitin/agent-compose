package driver

import (
	"context"
	"errors"
	"testing"
)

func TestResolveBoxliteImageLayoutUsesDockerWhenAvailable(t *testing.T) {
	dockerCalled := false
	ociCalled := false
	result, ok, err := resolveBoxliteImageLayout(context.Background(), "guest:latest", boxliteImageResolverOps{
		dockerAvailable: func(ctx context.Context) bool { return true },
		dockerMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			dockerCalled = true
			return boxliteImageLayoutResult{ImageID: "docker-id", ResolvedRef: "guest@sha256:docker", RootfsPath: "/docker/oci"}, true, nil
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			ociCalled = true
			return boxliteImageLayoutResult{}, false, nil
		},
	})
	if err != nil || !ok {
		t.Fatalf("resolveBoxliteImageLayout = %#v ok=%v err=%v", result, ok, err)
	}
	if !dockerCalled || ociCalled || result.RootfsPath != "/docker/oci" {
		t.Fatalf("dockerCalled=%v ociCalled=%v result=%#v", dockerCalled, ociCalled, result)
	}
}

func TestResolveBoxliteImageLayoutUsesOCIWhenDockerUnavailable(t *testing.T) {
	dockerCalled := false
	ociCalled := false
	result, ok, err := resolveBoxliteImageLayout(context.Background(), "guest:latest", boxliteImageResolverOps{
		dockerAvailable: func(ctx context.Context) bool { return false },
		dockerMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			dockerCalled = true
			return boxliteImageLayoutResult{}, false, nil
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			ociCalled = true
			return boxliteImageLayoutResult{ImageID: "oci-id", ResolvedRef: "guest@sha256:oci", RootfsPath: "/cache/oci"}, true, nil
		},
	})
	if err != nil || !ok {
		t.Fatalf("resolveBoxliteImageLayout = %#v ok=%v err=%v", result, ok, err)
	}
	if dockerCalled || !ociCalled || result.RootfsPath != "/cache/oci" {
		t.Fatalf("dockerCalled=%v ociCalled=%v result=%#v", dockerCalled, ociCalled, result)
	}
}

func TestResolveBoxliteImageLayoutFallsBackToOCIOnDockerMiss(t *testing.T) {
	ociCalled := false
	result, ok, err := resolveBoxliteImageLayout(context.Background(), "guest:latest", boxliteImageResolverOps{
		dockerAvailable: func(ctx context.Context) bool { return true },
		dockerMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			return boxliteImageLayoutResult{}, false, nil
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			ociCalled = true
			return boxliteImageLayoutResult{ImageID: "oci-id", ResolvedRef: "guest@sha256:oci", RootfsPath: "/cache/oci"}, true, nil
		},
	})
	if err != nil || !ok || !ociCalled || result.RootfsPath != "/cache/oci" {
		t.Fatalf("resolveBoxliteImageLayout = %#v ok=%v err=%v ociCalled=%v", result, ok, err, ociCalled)
	}
}

func TestResolveBoxliteImageLayoutPropagatesDockerError(t *testing.T) {
	wantErr := errors.New("docker materialize failed")
	_, _, err := resolveBoxliteImageLayout(context.Background(), "guest:latest", boxliteImageResolverOps{
		dockerAvailable: func(ctx context.Context) bool { return true },
		dockerMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			return boxliteImageLayoutResult{}, false, wantErr
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			t.Fatalf("oci materialization should not run after Docker error")
			return boxliteImageLayoutResult{}, false, nil
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolveBoxliteImageLayout err = %v, want %v", err, wantErr)
	}
}

// Regression: pullPolicy must be applied at the docker-daemon layer BEFORE the
// docker short circuit materializes a (possibly stale) local image. Without this
// pullPolicy=always never re-pulls an updated same-tag image.
func TestResolveBoxliteImageLayoutAppliesPullPolicyBeforeDockerMaterialize(t *testing.T) {
	var order []string
	_, ok, err := resolveBoxliteImageLayout(context.Background(), "guest:latest", boxliteImageResolverOps{
		dockerAvailable: func(ctx context.Context) bool { return true },
		applyDockerPullPolicy: func(ctx context.Context, imageRef string) error {
			order = append(order, "pull")
			return nil
		},
		dockerMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			order = append(order, "materialize")
			return boxliteImageLayoutResult{RootfsPath: "/docker/oci"}, true, nil
		},
	})
	if err != nil || !ok {
		t.Fatalf("resolve ok=%v err=%v", ok, err)
	}
	if len(order) != 2 || order[0] != "pull" || order[1] != "materialize" {
		t.Fatalf("expected pull before materialize, got %v", order)
	}
}

// Regression: when the docker-layer pull policy errors (e.g. never + absent, or
// always pull failure with no local cache), materialization must be skipped and
// the error propagated — not silently fall through to a stale image.
func TestResolveBoxliteImageLayoutPullPolicyErrorSkipsMaterialize(t *testing.T) {
	wantErr := errors.New("not found locally (pull_policy=never)")
	_, _, err := resolveBoxliteImageLayout(context.Background(), "guest:latest", boxliteImageResolverOps{
		dockerAvailable:       func(ctx context.Context) bool { return true },
		applyDockerPullPolicy: func(ctx context.Context, imageRef string) error { return wantErr },
		dockerMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			t.Fatalf("dockerMaterialize must not run after pull-policy error")
			return boxliteImageLayoutResult{}, false, nil
		},
		ociMaterialize: func(ctx context.Context, imageRef string) (boxliteImageLayoutResult, bool, error) {
			t.Fatalf("ociMaterialize must not run after pull-policy error")
			return boxliteImageLayoutResult{}, false, nil
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestImageCacheRootForDriverDefaultsToDataRoot(t *testing.T) {
	if got := imageCacheRootForDriver(testPrepareSessionStartConfig("/tmp/data-root")); got != "/tmp/data-root/images" {
		t.Fatalf("imageCacheRootForDriver = %q", got)
	}
}
