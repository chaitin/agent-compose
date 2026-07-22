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
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
)

func TestMicrosandboxBaseDiskIdentityIncludesInputs(t *testing.T) {
	first, err := microsandboxBaseDiskIdentity(microsandboxImageSourceDocker, "sha256:image-a", "amd64", 6)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct {
		source string
		image  string
		arch   string
		size   int32
	}{
		{source: microsandboxImageSourceDocker, image: "image-b", arch: "amd64", size: 6},
		{source: microsandboxImageSourceDocker, image: "image-a", arch: "arm64", size: 6},
		{source: microsandboxImageSourceDocker, image: "image-a", arch: "amd64", size: 7},
		// Both sources report the config digest as the image id, so an
		// identity that ignored the source would reuse a base disk built by
		// the other extractor.
		{source: microsandboxImageSourceOCI, image: "image-a", arch: "amd64", size: 6},
	} {
		got, err := microsandboxBaseDiskIdentity(candidate.source, candidate.image, candidate.arch, candidate.size)
		if err != nil {
			t.Fatal(err)
		}
		if got == first {
			t.Fatalf("identity %q did not change for input %#v", got, candidate)
		}
	}
	if !strings.Contains(first, "base-v1-docker-image-a-amd64-6") {
		t.Fatalf("identity = %q", first)
	}
	if _, err := microsandboxBaseDiskIdentity("", "image-a", "amd64", 6); err == nil {
		t.Fatal("identity accepted an empty source")
	}
}

func TestMicrosandboxBaseDiskRejectsSymlinkedManifest(t *testing.T) {
	root := t.TempDir()
	base := microsandboxBaseDisk{
		Identity: "base-v1-image-amd64-1", ImageID: "image", ResolvedRef: "fixture:latest",
		Path: filepath.Join(root, "base.qcow2"), Manifest: filepath.Join(root, "base.json"),
		CacheRoot: root, DiskSizeGiB: 1,
	}
	if err := os.WriteFile(base.Path, []byte("qcow2"), 0o444); err != nil {
		t.Fatal(err)
	}
	manifest := microsandboxBaseDiskManifest{
		FormatVersion: microsandboxBaseDiskFormatVersion, Identity: base.Identity, ImageID: base.ImageID,
		ResolvedRef: base.ResolvedRef, Architecture: runtime.GOARCH, DiskSizeGiB: base.DiskSizeGiB,
		Path: base.Path, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(outside, data, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, base.Manifest); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if valid, err := validateMicrosandboxBaseDisk(context.Background(), base); err == nil || valid || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlinked manifest validation = %v, %v; want rejection", valid, err)
	}
}

func TestMicrosandboxBaseDiskRemovesManifestWithoutDisk(t *testing.T) {
	root := t.TempDir()
	base := microsandboxBaseDisk{
		Path:     filepath.Join(root, "missing-base.qcow2"),
		Manifest: filepath.Join(root, "missing-base.json"),
	}
	if err := os.WriteFile(base.Manifest, []byte("{}\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	valid, err := validateMicrosandboxBaseDisk(context.Background(), base)
	if err != nil || valid {
		t.Fatalf("manifest-only validation = %v, %v; want cache miss", valid, err)
	}
	if _, err := os.Lstat(base.Manifest); !os.IsNotExist(err) {
		t.Fatalf("orphan manifest remains after validation: %v", err)
	}
}

func TestMicrosandboxBaseDiskRejectsBackingFile(t *testing.T) {
	requireMicrosandboxQemuTools(t)
	root := t.TempDir()
	backingPath := filepath.Join(root, "backing.qcow2")
	if output, err := exec.Command("qemu-img", "create", "-f", "qcow2", backingPath, "64M").CombinedOutput(); err != nil {
		t.Fatalf("create backing disk: %v: %s", err, output)
	}
	base := microsandboxBaseDisk{
		Identity: "base-v1-image-amd64-1", ImageID: "image", ResolvedRef: "fixture:latest",
		Path: filepath.Join(root, "base.qcow2"), Manifest: filepath.Join(root, "base.json"),
		CacheRoot: root, DiskSizeGiB: 1,
	}
	if output, err := exec.Command("qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", backingPath, base.Path).CombinedOutput(); err != nil {
		t.Fatalf("create backed base disk: %v: %s", err, output)
	}
	if err := os.Chmod(base.Path, 0o444); err != nil {
		t.Fatal(err)
	}
	manifest := microsandboxBaseDiskManifest{
		FormatVersion: microsandboxBaseDiskFormatVersion, Identity: base.Identity, ImageID: base.ImageID,
		ResolvedRef: base.ResolvedRef, Architecture: runtime.GOARCH, DiskSizeGiB: base.DiskSizeGiB,
		Path: base.Path, CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base.Manifest, data, 0o444); err != nil {
		t.Fatal(err)
	}
	if valid, err := validateMicrosandboxBaseDisk(context.Background(), base); err == nil || valid || !strings.Contains(err.Error(), "self-contained qcow2") {
		t.Fatalf("backed base validation = %v, %v; want rejection", valid, err)
	}
}

func TestSmokeMicrosandboxBaseDiskBuild(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverMicrosandbox)
	requireMicrosandboxQemuTools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	runtimeDriver := &microsandboxRuntime{config: &appconfig.Config{DataRoot: root, SandboxDiskSizeGB: 1}}
	imageRef := firstNonEmpty(os.Getenv("SMOKE_MICROSANDBOX_DEFAULT_IMAGE"), os.Getenv("SMOKE_DEFAULT_IMAGE"), "debian:bookworm-slim")
	base, err := runtimeDriver.resolveMicrosandboxBaseDisk(ctx, imageRef, "never", time.Minute)
	if err != nil {
		t.Fatalf("build base disk from local Docker image %s: %v", imageRef, err)
	}
	if valid, err := validateMicrosandboxBaseDisk(ctx, base); err != nil || !valid {
		t.Fatalf("validate base disk: valid=%v err=%v", valid, err)
	}
	output, err := exec.Command("qemu-img", "info", "--output=json", base.Path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var info qemuImageInfo
	if err := json.Unmarshal(output, &info); err != nil {
		t.Fatal(err)
	}
	if info.Format != "qcow2" || info.BackingFilename != "" || info.FullBackingFilename != "" {
		t.Fatalf("base qemu info = %#v", info)
	}
	before, err := os.Stat(base.Path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := runtimeDriver.resolveMicrosandboxBaseDisk(ctx, imageRef, "never", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(again.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("base disk cache hit replaced the published file")
	}
	imageDir := filepath.Dir(filepath.Dir(base.Path))
	for _, legacy := range []string{filepath.Join(imageDir, "rootfs"), filepath.Join(imageDir, ".rootfs.ready")} {
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("legacy shared rootfs path %s was created: %v", legacy, err)
		}
	}
	t.Logf("microsandbox base build: image=%s path=%s allocated=%d", imageRef, base.Path, allocatedBytes(t, base.Path))
}

func TestMicrosandboxRootfsDisksUsePrivateQcow2Overlays(t *testing.T) {
	requireMicrosandboxQemuTools(t)
	runtimeDriver, base := newMicrosandboxRootfsDiskFixture(t)
	ctx := context.Background()
	first, err := runtimeDriver.ensureRootfsDisk(ctx, "sandbox-a", base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtimeDriver.ensureRootfsDisk(ctx, "sandbox-b", base)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || !second.Created || first.Path == second.Path {
		t.Fatalf("rootfs disks = %#v %#v", first, second)
	}
	for _, disk := range []string{first.Path, second.Path} {
		if err := validateQcowBacking(ctx, disk, base.Path); err != nil {
			t.Fatal(err)
		}
		if allocatedBytes(t, disk) > 2<<20 {
			t.Fatalf("fresh child disk %s allocated too much space: %d", disk, allocatedBytes(t, disk))
		}
	}
	qemuIOWrite(t, first.Path, 0, "0x11")
	qemuIOWrite(t, second.Path, 0, "0x22")
	if got := qemuIORead(t, first.Path, 0, 32); !strings.Contains(got, "11 11") {
		t.Fatalf("first child read = %q", got)
	}
	if got := qemuIORead(t, second.Path, 0, 32); !strings.Contains(got, "22 22") {
		t.Fatalf("second child read = %q", got)
	}
	if got := qemuIORead(t, base.Path, 0, 32); strings.Contains(got, "11 11") || strings.Contains(got, "22 22") {
		t.Fatalf("base disk was modified: %q", got)
	}
}

func TestMicrosandboxRootfsDiskConcurrentCreateConverges(t *testing.T) {
	requireMicrosandboxQemuTools(t)
	runtimeDriver, base := newMicrosandboxRootfsDiskFixture(t)
	var wg sync.WaitGroup
	results := make(chan microsandboxRootfsDiskResult, 2)
	errors := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := runtimeDriver.createLocks.lock("same-sandbox")
			defer unlock()
			result, err := runtimeDriver.ensureRootfsDisk(context.Background(), "same-sandbox", base)
			results <- result
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var path string
	var created int
	for result := range results {
		if path == "" {
			path = result.Path
		}
		if result.Path != path {
			t.Fatalf("concurrent create returned paths %q and %q", path, result.Path)
		}
		if result.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created count = %d, want 1", created)
	}
}

func TestMicrosandboxRootfsDiskRemovesIncompletePair(t *testing.T) {
	requireMicrosandboxQemuTools(t)
	runtimeDriver, base := newMicrosandboxRootfsDiskFixture(t)
	path := runtimeDriver.rootfsDiskPath("incomplete")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runtimeDriver.ensureRootfsDisk(context.Background(), "incomplete", base)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatalf("result = %#v, want recreated disk", result)
	}
}

func TestMicrosandboxRemoveRootfsDiskFilesCleansIncompletePairs(t *testing.T) {
	for _, test := range []struct {
		name    string
		disk    bool
		sidecar bool
	}{
		{name: "disk only", disk: true},
		{name: "sidecar only", sidecar: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			runtimeDriver := &microsandboxRuntime{config: &appconfig.Config{MicrosandboxHome: home}}
			path := runtimeDriver.rootfsDiskPath("incomplete")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if test.disk {
				if err := os.WriteFile(path, []byte("partial disk"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.sidecar {
				if err := os.WriteFile(path+".owner.json", []byte("partial sidecar"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := runtimeDriver.removeRootfsDiskFiles("incomplete"); err != nil {
				t.Fatal(err)
			}
			for _, candidate := range []string{path, path + ".owner.json"} {
				if _, err := os.Lstat(candidate); !os.IsNotExist(err) {
					t.Fatalf("incomplete rootfs resource %s remains: %v", candidate, err)
				}
			}
		})
	}
}

func TestMicrosandboxManagedResourcesIncludeIncompleteRootfsRemnants(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "rootfs-disks")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	diskOnly := filepath.Join(root, "disk-only.qcow2")
	sidecarOnly := filepath.Join(root, "sidecar-only.qcow2.owner.json")
	for path, data := range map[string][]byte{diskOnly: []byte("partial disk"), sidecarOnly: []byte("partial sidecar")} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resources := map[string]*ManagedRuntimeResource{}
	warnings := appendMicrosandboxDiskResources(&appconfig.Config{MicrosandboxHome: home}, resources, nil)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %#v, want two incomplete-resource warnings", warnings)
	}
	wantPaths := map[string]string{
		"incomplete-rootfs:disk-only.qcow2":               diskOnly,
		"incomplete-rootfs:sidecar-only.qcow2.owner.json": sidecarOnly,
	}
	for _, resource := range resources {
		wantPath, ok := wantPaths[resource.SandboxID]
		if !ok {
			t.Fatalf("unexpected resource = %#v", resource)
		}
		if !resource.OwnershipValid || !resource.Removable || len(resource.OwnedPaths) != 1 || resource.OwnedPaths[0] != wantPath {
			t.Fatalf("incomplete resource = %#v", resource)
		}
		delete(wantPaths, resource.SandboxID)
	}
	if len(wantPaths) != 0 {
		t.Fatalf("missing incomplete resources = %#v", wantPaths)
	}
}

func TestMicrosandboxRootfsDiskRejectsOwnershipAndBackingMismatch(t *testing.T) {
	requireMicrosandboxQemuTools(t)
	runtimeDriver, base := newMicrosandboxRootfsDiskFixture(t)
	result, err := runtimeDriver.ensureRootfsDisk(context.Background(), "sandbox-a", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtimeDriver.reuseOrCleanRootfsDisk(context.Background(), result.Path, "sandbox-b", base); err == nil {
		t.Fatal("different sandbox reused another sandbox's deterministic disk")
	}
	data, err := os.ReadFile(result.Path + ".owner.json")
	if err != nil {
		t.Fatal(err)
	}
	var ownership microsandboxDiskOwnership
	if err := json.Unmarshal(data, &ownership); err != nil {
		t.Fatal(err)
	}
	ownership.BackingPath = filepath.Join(filepath.Dir(base.Path), "moved-base.qcow2")
	data, err = json.Marshal(ownership)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.Path+".owner.json", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDriver.ensureRootfsDisk(context.Background(), "sandbox-a", base); err == nil {
		t.Fatal("rootfs disk with moved backing path was reused")
	}
}

func TestMicrosandboxRootfsOwnedPathRejectsSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	root := filepath.Join(home, "rootfs-disks")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "escape", "disk.qcow2")
	if err := os.WriteFile(filepath.Join(outside, "disk.qcow2"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateMicrosandboxRootfsOwnedPath(home, path); err == nil {
		t.Fatal("rootfs owned path accepted symlink escape")
	}
}

func newMicrosandboxRootfsDiskFixture(t *testing.T) (*microsandboxRuntime, microsandboxBaseDisk) {
	t.Helper()
	root := t.TempDir()
	basePath := filepath.Join(root, "cache", "base.qcow2")
	if err := os.MkdirAll(filepath.Dir(basePath), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("qemu-img", "create", "-f", "qcow2", basePath, "64M")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create base qcow2: %v: %s", err, output)
	}
	if err := os.Chmod(basePath, 0o444); err != nil {
		t.Fatal(err)
	}
	return &microsandboxRuntime{config: &appconfig.Config{MicrosandboxHome: filepath.Join(root, "home")}}, microsandboxBaseDisk{
		Identity: "base-v1-" + runtime.GOARCH + "-1", ImageID: "fixture", ResolvedRef: "fixture:latest",
		Path: basePath, DiskSizeGiB: 1,
	}
}

func requireMicrosandboxQemuTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"qemu-img", "qemu-io"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required: %v", tool, err)
		}
	}
}

func qemuIOWrite(t *testing.T, path string, offset int, pattern string) {
	t.Helper()
	command := exec.Command("qemu-io", "-f", "qcow2", "-c", "write -P "+pattern+" "+formatTestInt(offset)+" 4096", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("qemu write: %v: %s", err, output)
	}
}

func qemuIORead(t *testing.T, path string, offset, length int) string {
	t.Helper()
	output, err := exec.Command("qemu-io", "-f", "qcow2", "-c", "read -v "+formatTestInt(offset)+" "+formatTestInt(length), path).CombinedOutput()
	if err != nil {
		t.Fatalf("qemu read: %v: %s", err, output)
	}
	return string(output)
}

func formatTestInt(value int) string {
	return fmt.Sprintf("%d", value)
}

func allocatedBytes(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fileAllocatedBytes(info)
}
