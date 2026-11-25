package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"
)

func TestNewConfigRejectsSandboxArchiveRootInsideSandboxRoot(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		symlink bool
	}{
		{name: "same directory", path: "."},
		{name: "nested directory", path: "archives"},
		{name: "symlink into sandbox", path: "archives", symlink: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			sandboxRoot := filepath.Join(root, "sandboxes")
			t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
			t.Setenv("SANDBOX_ROOT", sandboxRoot)
			archiveRoot := filepath.Join(sandboxRoot, test.path)
			if test.symlink {
				if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "archive-link")
				if err := os.Symlink(archiveRoot, link); err != nil {
					t.Skipf("create archive root symlink: %v", err)
				}
				archiveRoot = link
			}
			t.Setenv("SANDBOX_ARCHIVE_ROOT", archiveRoot)
			di := do.New()
			do.ProvideValue(di, slog.Default())
			_, err := NewConfig(di)
			if err == nil || !strings.Contains(err.Error(), "SANDBOX_ARCHIVE_ROOT") {
				t.Fatalf("NewConfig error = %v, want unsafe archive root rejection", err)
			}
		})
	}
}

func TestNewConfigAcceptsSandboxArchiveRootOutsideSandboxRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("SANDBOX_ROOT", filepath.Join(root, "sandboxes"))
	t.Setenv("SANDBOX_ARCHIVE_ROOT", filepath.Join(root, "archives"))
	di := do.New()
	do.ProvideValue(di, slog.Default())
	if _, err := NewConfig(di); err != nil {
		t.Fatalf("NewConfig returned error for external archive root: %v", err)
	}
}

func TestNewConfigDefaultsRunLogArchiveOffUnderTheDataRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatal(err)
	}
	if config.RunLogRetentionTTL != 0 {
		t.Fatalf("RunLogRetentionTTL = %v, want disabled by default", config.RunLogRetentionTTL)
	}
	if want := filepath.Join(config.DataRoot, "archives", "logs"); config.RunLogArchiveRoot != want {
		t.Fatalf("RunLogArchiveRoot = %q, want %q", config.RunLogArchiveRoot, want)
	}
}

func TestNewConfigRejectsRunLogArchiveRootInsideTheRunsDirectory(t *testing.T) {
	for _, path := range []string{"runs", filepath.Join("runs", "archive")} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			dataRoot := filepath.Join(root, "data")
			t.Setenv("DATA_ROOT", dataRoot)
			t.Setenv("RUN_LOG_ARCHIVE_ROOT", filepath.Join(dataRoot, path))
			di := do.New()
			do.ProvideValue(di, slog.Default())
			if _, err := NewConfig(di); err == nil || !strings.Contains(err.Error(), "RUN_LOG_ARCHIVE_ROOT") {
				t.Fatalf("NewConfig error = %v, want unsafe run log archive root rejection", err)
			}
		})
	}
}

func TestNewConfigReadsRunLogRetentionTTL(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("RUN_LOG_RETENTION_TTL", "720h")
	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatal(err)
	}
	if config.RunLogRetentionTTL != 720*time.Hour {
		t.Fatalf("RunLogRetentionTTL = %v", config.RunLogRetentionTTL)
	}
}
