package workspaces

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceFileCopyCloneAndFallbackHaveIndependentWrites(t *testing.T) {
	for _, strategy := range []string{"platform", "fallback"} {
		t.Run(strategy, func(t *testing.T) {
			root := t.TempDir()
			sourcePath := filepath.Join(root, "source")
			destination := filepath.Join(root, "destination")
			if err := os.WriteFile(sourcePath, []byte("original contents"), 0o751); err != nil {
				t.Fatal(err)
			}
			source, err := os.Open(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = source.Close() }()
			clone := cloneWorkspaceFile
			if strategy == "fallback" {
				clone = func(_ *os.File, dst string) error {
					if err := os.WriteFile(dst, []byte("partial"), 0o600); err != nil {
						return err
					}
					return errors.ErrUnsupported
				}
			}
			if err := copyWorkspaceFile(context.Background(), source, destination, 0o751, clone); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(destination)
			if err != nil || string(got) != "original contents" {
				t.Fatalf("copied file = %q, %v", got, err)
			}
			info, err := os.Stat(destination)
			if err != nil {
				t.Fatal(err)
			}
			original, err := source.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o751 || os.SameFile(info, original) {
				t.Fatalf("copy permissions/identity = %v", info)
			}
			if err := os.WriteFile(destination, []byte("guest edit"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err = os.ReadFile(sourcePath)
			if err != nil || string(got) != "original contents" {
				t.Fatalf("guest edit changed source: %q %v", got, err)
			}
			if err := os.WriteFile(sourcePath, []byte("host edit"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err = os.ReadFile(destination)
			if err != nil || string(got) != "guest edit" {
				t.Fatalf("host edit changed guest: %q %v", got, err)
			}
		})
	}
}

func TestWorkspaceFileCopyPropagatesFailureAndCancellation(t *testing.T) {
	for _, scenario := range []string{"clone-failure", "canceled-before", "canceled-during-fallback", "canceled-after-clone"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			sourcePath := filepath.Join(root, "source")
			destination := filepath.Join(root, "destination")
			if err := os.WriteFile(sourcePath, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			source, err := os.Open(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = source.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := os.ErrPermission
			if scenario != "clone-failure" {
				want = context.Canceled
			}
			if scenario == "canceled-before" {
				cancel()
			}
			clone := func(_ *os.File, dst string) error {
				if err := os.WriteFile(dst, []byte("partial"), 0o600); err != nil {
					return err
				}
				if scenario == "clone-failure" {
					return os.ErrPermission
				}
				cancel()
				if scenario == "canceled-after-clone" {
					return nil
				}
				return errors.ErrUnsupported
			}
			if err := copyWorkspaceFile(ctx, source, destination, 0o600, clone); !errors.Is(err, want) {
				t.Fatalf("copy error = %v, want %v", err, want)
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial destination survives: %v", err)
			}
		})
	}
}

func TestWorkspaceTreeCopyPreservesModesAndRejectsAllSymlinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	for _, dir := range []string{src, dst, filepath.Join(src, "nested")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "empty"), nil, 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "nested"), 0o550); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(src, "nested"), 0o750)
		_ = os.Chmod(filepath.Join(dst, "nested"), 0o750)
	})
	source, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	if err := CopyRootDirectoryContentsContext(context.Background(), source, dst); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{"nested", 0o550}, {"nested/empty", 0o751}} {
		info, err := os.Stat(filepath.Join(dst, item.path))
		if err != nil || info.Mode().Perm() != item.mode {
			t.Fatalf("mode %s = %v, %v", item.path, info, err)
		}
	}
	for _, target := range []string{"nested/empty", "nested", "missing", "../outside", t.TempDir()} {
		if err := os.Symlink(target, filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
		if err := CopyRootEntry(context.Background(), source, "link", filepath.Join(dst, "link")); err == nil {
			t.Fatalf("accepted symlink %q", target)
		}
		if err := os.Remove(filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
	}
}
