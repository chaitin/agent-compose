//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
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

// A cache hit discards the resolved source without materializing anything, so
// a source that parked a Docker client in its closure would leak one per
// sandbox creation on the most common path.
func TestMicrosandboxBaseDiskCacheHitSkipsMaterialize(t *testing.T) {
	requireMicrosandboxQemuTools(t)
	root := t.TempDir()
	cacheRoot := filepath.Join(root, "image-cache")
	base := microsandboxBaseDisk{
		Identity: "base-v1-docker-image-amd64-1", Source: microsandboxImageSourceDocker, ImageID: "image",
		ResolvedRef: "fixture:latest", CacheRoot: cacheRoot, DiskSizeGiB: 1,
	}
	baseDir := filepath.Join(cacheRoot, base.ImageID, "microsandbox")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base.Path = filepath.Join(baseDir, base.Identity+".qcow2")
	base.Manifest = filepath.Join(baseDir, base.Identity+".json")
	if output, err := exec.Command("qemu-img", "create", "-f", "qcow2", base.Path, "64M").CombinedOutput(); err != nil {
		t.Fatalf("create base qcow2: %v: %s", err, output)
	}
	if err := os.Chmod(base.Path, 0o444); err != nil {
		t.Fatal(err)
	}
	manifest := microsandboxBaseDiskManifest{
		FormatVersion: microsandboxBaseDiskFormatVersion, Identity: base.Identity, Source: base.Source,
		ImageID: base.ImageID, ResolvedRef: base.ResolvedRef, Architecture: runtime.GOARCH,
		DiskSizeGiB: base.DiskSizeGiB, Path: base.Path, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base.Manifest, data, 0o444); err != nil {
		t.Fatal(err)
	}

	source := microsandboxImageSource{
		Kind: microsandboxImageSourceDocker, ImageID: base.ImageID, ResolvedRef: base.ResolvedRef,
		materialize: func(context.Context, string) (string, func(), error) {
			t.Fatal("materialize ran for a base disk that was already cached")
			return "", nil, nil
		},
	}
	runtimeDriver := &microsandboxRuntime{config: &appconfig.Config{DataRoot: root}}
	if err := runtimeDriver.ensureMicrosandboxBaseDisk(context.Background(), source, base); err != nil {
		t.Fatalf("cached base disk was not reused: %v", err)
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
