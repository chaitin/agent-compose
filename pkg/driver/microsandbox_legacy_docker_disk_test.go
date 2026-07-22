//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/identity"
)

func TestMicrosandboxRemoveLegacyDockerDiskFilesOnlyTargetSandbox(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtimeDriver := &microsandboxRuntime{config: config}
	currentID := identity.Prefix + identity.NewRandomID(identity.ResourceSandbox)
	otherID := identity.NewRandomID(identity.ResourceSandbox)

	current := []string{
		runtimeDriver.legacyHashedDockerDiskPath(currentID),
		runtimeDriver.legacyUnhashedDockerDiskPath(currentID),
	}
	other := []string{
		runtimeDriver.legacyHashedDockerDiskPath(otherID),
		runtimeDriver.legacyUnhashedDockerDiskPath(otherID),
	}
	for _, path := range append(append([]string{}, current...), other...) {
		writeMicrosandboxPath(t, path, []byte("legacy disk"))
		writeMicrosandboxPath(t, path+".owner.json", []byte("legacy ownership"))
	}

	if err := runtimeDriver.removeLegacyDockerDiskFiles(currentID); err != nil {
		t.Fatalf("removeLegacyDockerDiskFiles: %v", err)
	}
	for _, path := range current {
		for _, candidate := range []string{path, path + ".owner.json"} {
			if _, err := os.Lstat(candidate); !os.IsNotExist(err) {
				t.Fatalf("target legacy resource %s remains, err=%v", candidate, err)
			}
		}
	}
	for _, path := range other {
		for _, candidate := range []string{path, path + ".owner.json"} {
			if _, err := os.Lstat(candidate); err != nil {
				t.Fatalf("other sandbox legacy resource %s missing: %v", candidate, err)
			}
		}
	}
}

func TestMicrosandboxLegacyHashedDockerDiskPathUsesIdentityHash(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtimeDriver := &microsandboxRuntime{config: config}
	sandboxID := identity.NewRandomID(identity.ResourceSandbox)
	hash, err := identity.Hash(sandboxID)
	if err != nil {
		t.Fatalf("hash sandbox id: %v", err)
	}

	got := runtimeDriver.legacyHashedDockerDiskPath(sandboxID)
	want := filepath.Join(config.MicrosandboxHome, "docker-disks", hash+".raw")
	if got != want {
		t.Fatalf("legacyHashedDockerDiskPath = %q, want %q", got, want)
	}
	if strings.ContainsAny(filepath.Base(got), ",:;") {
		t.Fatalf("legacy Docker disk basename = %q, want no runtime-forbidden characters", filepath.Base(got))
	}
}

func TestMicrosandboxRemoveLegacyDockerDiskFilesRejectsSymlink(t *testing.T) {
	config := testMicrosandboxConfig(t)
	runtimeDriver := &microsandboxRuntime{config: config}
	sandboxID := identity.NewRandomID(identity.ResourceSandbox)
	diskPath := runtimeDriver.legacyHashedDockerDiskPath(sandboxID)
	outside := writeMicrosandboxFile(t, t.TempDir(), "outside.raw")
	if err := os.MkdirAll(filepath.Dir(diskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, diskPath); err != nil {
		t.Skipf("create symlink: %v", err)
	}

	err := runtimeDriver.removeLegacyDockerDiskFiles(sandboxID)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("removeLegacyDockerDiskFiles error = %v, want symlink rejection", err)
	}
	if _, err := os.Lstat(diskPath); err != nil {
		t.Fatalf("legacy disk symlink removed after rejection: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside target removed after rejection: %v", err)
	}
}

func TestMicrosandboxManagedResourcesPruneOwnedLegacyDockerDisks(t *testing.T) {
	home := t.TempDir()
	runtimeDriver := &microsandboxRuntime{config: &appconfig.Config{MicrosandboxHome: home}}
	v2SandboxID := identity.NewRandomID(identity.ResourceSandbox)
	fixtures := []struct {
		sandboxID    string
		diskPath     string
		version      int
		resourceKind string
	}{
		{sandboxID: "legacy-v1", diskPath: runtimeDriver.legacyUnhashedDockerDiskPath("legacy-v1"), version: 1},
		{sandboxID: v2SandboxID, diskPath: runtimeDriver.legacyHashedDockerDiskPath(v2SandboxID), version: microsandboxDiskOwnershipVersion, resourceKind: microsandboxLegacyDockerDiskKind},
	}
	for _, fixture := range fixtures {
		writeMicrosandboxPath(t, fixture.diskPath, []byte("legacy disk"))
		writeLegacyDockerOwnership(t, fixture.diskPath, microsandboxDiskOwnership{
			Version: fixture.version, ResourceKind: fixture.resourceKind,
			SandboxID: fixture.sandboxID, DiskPath: fixture.diskPath, CreatedAt: time.Now().UTC(),
		})
	}

	resources := map[string]*ManagedRuntimeResource{}
	warnings := appendMicrosandboxDiskResources(&appconfig.Config{MicrosandboxHome: home}, resources, nil)
	if len(warnings) != 0 {
		t.Fatalf("legacy inventory warnings = %#v", warnings)
	}

	var ownedPaths []string
	for _, fixture := range fixtures {
		resource := managedResourceForSandbox(resources, fixture.sandboxID)
		if resource == nil || !resource.OwnershipValid || !resource.Removable {
			t.Fatalf("legacy resource %q = %#v", fixture.sandboxID, resource)
		}
		if len(resource.OwnedPaths) != 2 || resource.OwnedPaths[0] != fixture.diskPath || resource.OwnedPaths[1] != fixture.diskPath+".owner.json" {
			t.Fatalf("legacy resource %q paths = %#v", fixture.sandboxID, resource.OwnedPaths)
		}
		ownedPaths = append(ownedPaths, resource.OwnedPaths...)
	}
	if err := removeMicrosandboxManagedPaths(home, ownedPaths); err != nil {
		t.Fatalf("prune legacy paths: %v", err)
	}
	for _, path := range ownedPaths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("pruned legacy resource %s remains, err=%v", path, err)
		}
	}
}

func TestMicrosandboxManagedResourcesRejectUnsafeLegacyDockerOwnership(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string) (string, string)
	}{
		{
			name: "incomplete ownership",
			setup: func(t *testing.T, home string) (string, string) {
				diskPath := filepath.Join(home, "docker-disks", "incomplete.raw")
				writeMicrosandboxPath(t, diskPath, []byte("legacy disk"))
				writeLegacyDockerOwnership(t, diskPath, microsandboxDiskOwnership{
					Version: microsandboxDiskOwnershipVersion, ResourceKind: microsandboxLegacyDockerDiskKind,
					SandboxID: "incomplete", DiskPath: diskPath,
				})
				return diskPath, ""
			},
		},
		{
			name: "path escape",
			setup: func(t *testing.T, home string) (string, string) {
				diskPath := filepath.Join(home, "docker-disks", "escape.raw")
				outside := writeMicrosandboxFile(t, t.TempDir(), "outside.raw")
				writeMicrosandboxPath(t, diskPath, []byte("legacy disk"))
				writeLegacyDockerOwnership(t, diskPath, microsandboxDiskOwnership{
					Version: microsandboxDiskOwnershipVersion, ResourceKind: microsandboxLegacyDockerDiskKind,
					SandboxID: "escape", DiskPath: outside, CreatedAt: time.Now().UTC(),
				})
				return diskPath, outside
			},
		},
		{
			name: "symlink disk",
			setup: func(t *testing.T, home string) (string, string) {
				diskPath := filepath.Join(home, "docker-disks", "symlink.raw")
				outside := writeMicrosandboxFile(t, t.TempDir(), "outside.raw")
				if err := os.MkdirAll(filepath.Dir(diskPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, diskPath); err != nil {
					t.Skipf("create symlink: %v", err)
				}
				writeLegacyDockerOwnership(t, diskPath, microsandboxDiskOwnership{
					Version: microsandboxDiskOwnershipVersion, ResourceKind: microsandboxLegacyDockerDiskKind,
					SandboxID: "symlink", DiskPath: diskPath, CreatedAt: time.Now().UTC(),
				})
				return diskPath, outside
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			diskPath, outside := test.setup(t, home)
			manifestPath := diskPath + ".owner.json"
			resources := map[string]*ManagedRuntimeResource{}
			warnings := appendMicrosandboxDiskResources(&appconfig.Config{MicrosandboxHome: home}, resources, nil)
			if len(warnings) != 1 {
				t.Fatalf("warnings = %#v, want one ownership warning", warnings)
			}
			if len(resources) != 1 {
				t.Fatalf("resources = %#v, want one blocked resource", resources)
			}
			var blocked *ManagedRuntimeResource
			for _, resource := range resources {
				blocked = resource
			}
			if blocked == nil || blocked.OwnershipValid || blocked.Removable || len(blocked.OwnedPaths) != 0 {
				t.Fatalf("blocked resource = %#v", blocked)
			}
			if err := RemoveMicrosandboxManagedResource(context.Background(), &appconfig.Config{MicrosandboxHome: home}, *blocked); err == nil || !strings.Contains(err.Error(), "ownership is incomplete") {
				t.Fatalf("remove blocked resource error = %v", err)
			}
			for _, path := range []string{diskPath, manifestPath} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("blocked resource %s was removed: %v", path, err)
				}
			}
			if outside != "" {
				if _, err := os.Stat(outside); err != nil {
					t.Fatalf("outside resource %s was removed: %v", outside, err)
				}
			}
		})
	}
}

func writeLegacyDockerOwnership(t *testing.T, diskPath string, ownership microsandboxDiskOwnership) {
	t.Helper()
	payload, err := json.Marshal(ownership)
	if err != nil {
		t.Fatal(err)
	}
	writeMicrosandboxPath(t, diskPath+".owner.json", payload)
}

func writeMicrosandboxPath(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func managedResourceForSandbox(resources map[string]*ManagedRuntimeResource, sandboxID string) *ManagedRuntimeResource {
	for _, resource := range resources {
		if resource.SandboxID == sandboxID {
			return resource
		}
	}
	return nil
}
