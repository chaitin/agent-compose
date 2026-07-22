//go:build linux && cgo && microsandboxcgo

package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateMicrosandboxLegacyDockerOwnedPathRejectsIntermediateSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	diskRoot := filepath.Join(home, "docker-disks")
	if err := os.MkdirAll(diskRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideDisk := filepath.Join(outside, "outside.raw")
	if err := os.WriteFile(outsideDisk, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(diskRoot, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("create symlink: %v", err)
	}

	err := validateMicrosandboxLegacyDockerOwnedPath(home, filepath.Join(link, filepath.Base(outsideDisk)))
	if err == nil || !strings.Contains(err.Error(), "through a symlink") {
		t.Fatalf("validateMicrosandboxLegacyDockerOwnedPath error = %v, want symlink escape rejection", err)
	}
	if data, readErr := os.ReadFile(outsideDisk); readErr != nil || string(data) != "outside" {
		t.Fatalf("outside disk changed: data=%q err=%v", data, readErr)
	}
}

func TestValidateMicrosandboxLegacyDockerOwnedPathAcceptsRegularFile(t *testing.T) {
	home := t.TempDir()
	diskRoot := filepath.Join(home, "docker-disks")
	if err := os.MkdirAll(diskRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(diskRoot, "sandbox.raw")
	if err := os.WriteFile(disk, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateMicrosandboxLegacyDockerOwnedPath(home, disk); err != nil {
		t.Fatalf("validateMicrosandboxLegacyDockerOwnedPath: %v", err)
	}
}
