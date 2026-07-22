//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func stubMicrosandboxImageSourceOps(t *testing.T, dockerAvailable bool, dockerFound bool) (microsandboxImageSourceOps, *[]string) {
	t.Helper()
	var calls []string
	ops := microsandboxImageSourceOps{
		dockerAvailable: func(context.Context) bool { return dockerAvailable },
		applyDockerPullPolicy: func(context.Context, string) error {
			calls = append(calls, "pull-policy")
			return nil
		},
		dockerSource: func(context.Context, string) (microsandboxImageSource, bool, error) {
			calls = append(calls, "docker")
			if !dockerFound {
				return microsandboxImageSource{}, false, nil
			}
			return microsandboxImageSource{Kind: microsandboxImageSourceDocker, ImageID: "image", ResolvedRef: "fixture:latest"}, true, nil
		},
		ociSource: func(context.Context, string) (microsandboxImageSource, error) {
			calls = append(calls, "oci")
			return microsandboxImageSource{Kind: microsandboxImageSourceOCI, ImageID: "image", ResolvedRef: "fixture:latest"}, nil
		},
	}
	return ops, &calls
}

func TestMicrosandboxImageSourcePrefersDocker(t *testing.T) {
	ops, calls := stubMicrosandboxImageSourceOps(t, true, true)
	source, err := resolveMicrosandboxImageSource(context.Background(), "fixture:latest", ops)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != microsandboxImageSourceDocker {
		t.Fatalf("source kind = %q, want %q", source.Kind, microsandboxImageSourceDocker)
	}
	for _, call := range *calls {
		if call == "oci" {
			t.Fatalf("image cache was consulted while Docker resolved the image: %v", *calls)
		}
	}
}

func TestMicrosandboxImageSourceFallsBackWithoutDockerDaemon(t *testing.T) {
	ops, calls := stubMicrosandboxImageSourceOps(t, false, false)
	source, err := resolveMicrosandboxImageSource(context.Background(), "fixture:latest", ops)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != microsandboxImageSourceOCI {
		t.Fatalf("source kind = %q, want %q", source.Kind, microsandboxImageSourceOCI)
	}
	for _, call := range *calls {
		if call != "oci" {
			t.Fatalf("Docker was consulted while its daemon is unavailable: %v", *calls)
		}
	}
}

func TestMicrosandboxImageSourceFallsBackWhenDockerLacksImage(t *testing.T) {
	ops, calls := stubMicrosandboxImageSourceOps(t, true, false)
	source, err := resolveMicrosandboxImageSource(context.Background(), "fixture:latest", ops)
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != microsandboxImageSourceOCI {
		t.Fatalf("source kind = %q, want %q", source.Kind, microsandboxImageSourceOCI)
	}
	if len(*calls) != 3 || (*calls)[2] != "oci" {
		t.Fatalf("calls = %v, want pull policy and Docker before the image cache", *calls)
	}
}

// A pull policy failure is the configured source's real answer. Falling back
// would let pull_policy=never resolve an image the policy just refused.
func TestMicrosandboxImageSourceDoesNotFallBackOnPullPolicyFailure(t *testing.T) {
	ops, calls := stubMicrosandboxImageSourceOps(t, true, true)
	ops.applyDockerPullPolicy = func(context.Context, string) error {
		return fmt.Errorf("image is not present locally (pull_policy=never)")
	}
	if _, err := resolveMicrosandboxImageSource(context.Background(), "fixture:latest", ops); err == nil {
		t.Fatal("pull policy failure did not fail resolution")
	}
	for _, call := range *calls {
		if call == "oci" {
			t.Fatalf("image cache was consulted after a pull policy failure: %v", *calls)
		}
	}
}

func TestMicrosandboxImageSourceDoesNotFallBackOnDockerError(t *testing.T) {
	ops, calls := stubMicrosandboxImageSourceOps(t, true, true)
	ops.dockerSource = func(context.Context, string) (microsandboxImageSource, bool, error) {
		return microsandboxImageSource{}, false, fmt.Errorf("inspect failed")
	}
	if _, err := resolveMicrosandboxImageSource(context.Background(), "fixture:latest", ops); err == nil {
		t.Fatal("Docker inspect failure did not fail resolution")
	}
	for _, call := range *calls {
		if call == "oci" {
			t.Fatalf("image cache was consulted after a Docker error: %v", *calls)
		}
	}
}

// The image cache rootfs is shared with the BoxLite driver. Base disk
// construction reads it and must never release it, unlike the private export
// directory the Docker source creates.
func TestMicrosandboxOCIImageSourceNeverReleasesSharedRootfs(t *testing.T) {
	rootfs := t.TempDir()
	marker := filepath.Join(rootfs, "etc")
	if err := os.MkdirAll(marker, 0o755); err != nil {
		t.Fatal(err)
	}
	source := newMicrosandboxOCIImageSource("image", "fixture:latest", rootfs, nil)
	dir, release, err := source.materialize(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if dir != rootfs {
		t.Fatalf("materialized dir = %q, want the shared image cache rootfs %q", dir, rootfs)
	}
	release()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("release deleted the shared image cache rootfs: %v", err)
	}
}
