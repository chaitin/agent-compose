//go:build linux || darwin

package workspaces

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const ownedCleanupCrossUIDRootEnv = "AGENT_COMPOSE_TEST_OWNED_CLEANUP_CROSS_UID_ROOT"

func TestRemoveOwnedDirectoryCrossUIDIntegration(t *testing.T) {
	if root := os.Getenv(ownedCleanupCrossUIDRootEnv); root != "" {
		checkOwnedCleanupCrossUID(t, root)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root to create guest-owned fixtures and execute cleanup as UID 1000")
	}
	root, err := os.MkdirTemp("", "agent-compose-owned-cleanup-cross-uid-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove cross-UID fixture: %v", err)
		}
	})
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(root, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	prepareOwnedCleanupCrossUID(t, root)
	// Go's temporary build parent may be root-only. Copy this test binary into
	// the fixture so the unprivileged child can execute it without changing it.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	childBinary := filepath.Join(root, "cleanup.test")
	if err := os.WriteFile(childBinary, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, childBinary, "-test.run=^TestRemoveOwnedDirectoryCrossUIDIntegration$", "-test.v")
	command.Env = append(os.Environ(), ownedCleanupCrossUIDRootEnv+"="+root)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: 1000, Groups: []uint32{}}}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("cleanup child as UID 1000: %v\n%s", err, output)
	}
	t.Logf("root-created guest fixture, real cleanup child UID 1000:\n%s", output)
}

func prepareOwnedCleanupCrossUID(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"foreign-empty", "foreign-empty-readonly", "foreign-empty-inaccessible", "foreign-writable", "foreign-writable-owner-readonly", "owned-readonly", "foreign-protected", "external-symlink"} {
		parent := filepath.Join(root, name)
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(parent, 1000, 1000); err != nil {
			t.Fatal(err)
		}
		if name == "external-symlink" {
			continue
		}
		directory := filepath.Join(parent, "guest-directory")
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if name != "foreign-empty" && name != "foreign-empty-readonly" && name != "foreign-empty-inaccessible" {
			if err := os.WriteFile(filepath.Join(directory, "file"), []byte("guest contents"), 0o444); err != nil {
				t.Fatal(err)
			}
		}
		switch name {
		case "foreign-empty-readonly":
			if err := os.Chmod(directory, 0o555); err != nil {
				t.Fatal(err)
			}
		case "foreign-empty-inaccessible":
			if err := os.Chmod(directory, 0o000); err != nil {
				t.Fatal(err)
			}
		case "foreign-writable":
			if err := os.Chmod(directory, 0o777); err != nil {
				t.Fatal(err)
			}
		case "foreign-writable-owner-readonly":
			if err := os.Chmod(directory, 0o577); err != nil {
				t.Fatal(err)
			}
		case "owned-readonly":
			if err := os.Chown(directory, 1000, 1000); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(directory, 0o555); err != nil {
				t.Fatal(err)
			}
		}
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("outside contents"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "external-symlink", "link")); err != nil {
		t.Fatal(err)
	}
}

func checkOwnedCleanupCrossUID(t *testing.T, root string) {
	t.Helper()
	if os.Geteuid() != 1000 || os.Getegid() != 1000 {
		t.Fatalf("cleanup identity = %d:%d, want 1000:1000", os.Geteuid(), os.Getegid())
	}
	for _, name := range []string{"foreign-empty", "foreign-empty-readonly", "foreign-empty-inaccessible", "foreign-writable", "foreign-writable-owner-readonly", "owned-readonly", "external-symlink"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			if err := RemoveOwnedDirectory(path); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owned tree remains after cleanup: %v", err)
			}
		})
	}
	t.Run("foreign-protected", func(t *testing.T) {
		path := filepath.Join(root, "foreign-protected")
		if err := RemoveOwnedDirectory(path); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("foreign nonempty 0755 cleanup error = %v, want permission error", err)
		}
		assertOwnedCleanupOutsideFile(t, filepath.Join(path, "guest-directory"), 0o755, "file", "guest contents")
	})
	assertOwnedCleanupOutsideFile(t, filepath.Join(root, "outside"), 0o555, "keep", "outside contents")
}

func assertOwnedCleanupOutsideFile(t *testing.T, directory string, mode os.FileMode, name, contents string) {
	t.Helper()
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("preserved directory %s mode: %v, error %v", directory, info, err)
	}
	path := filepath.Join(directory, name)
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("preserved file %s mode: %v, error %v", path, info, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != contents {
		t.Fatalf("preserved file %s contents = %q, error %v", path, got, err)
	}
}
