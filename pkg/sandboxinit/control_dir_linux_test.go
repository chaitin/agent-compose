//go:build linux

package sandboxinit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInsecureDirectoryReason(t *testing.T) {
	t.Run("private directory", func(t *testing.T) {
		dir := t.TempDir()
		reason, err := insecureDirectoryReason(dir)
		if err != nil || reason != "" {
			t.Fatalf("insecureDirectoryReason() = %q, %v; want safe", reason, err)
		}
	})
	t.Run("read-only owner directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		reason, err := insecureDirectoryReason(dir)
		if err != nil || reason != "" {
			t.Fatalf("insecureDirectoryReason(0755) = %q, %v; want safe", reason, err)
		}
	})
	t.Run("group writable", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o770); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		reason, err := insecureDirectoryReason(dir)
		if err != nil || !strings.Contains(reason, "group- or world-writable") {
			t.Fatalf("insecureDirectoryReason(0770) = %q, %v; want an unsafe reason", reason, err)
		}
	})
	t.Run("world writable", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		reason, err := insecureDirectoryReason(dir)
		if err != nil || !strings.Contains(reason, "0777") {
			t.Fatalf("insecureDirectoryReason(0777) = %q, %v; want the mode in the reason", reason, err)
		}
	})
	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		reason, err := insecureDirectoryReason(path)
		if err != nil || !strings.Contains(reason, "not a directory") {
			t.Fatalf("insecureDirectoryReason(file) = %q, %v; want not-a-directory", reason, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		reason, err := insecureDirectoryReason(filepath.Join(t.TempDir(), "missing"))
		if err != nil || !strings.Contains(reason, "does not exist") {
			t.Fatalf("insecureDirectoryReason(missing) = %q, %v; want missing", reason, err)
		}
	})
}

func TestRunRejectsWorldWritableControlDirectory(t *testing.T) {
	controlDir := filepath.Join(t.TempDir(), "control")
	if err := os.Mkdir(controlDir, 0o700); err != nil {
		t.Fatalf("create control dir: %v", err)
	}
	if err := os.Chmod(controlDir, 0o777); err != nil {
		t.Fatalf("make control dir world-writable: %v", err)
	}
	socket := filepath.Join(controlDir, "init.sock")
	err := Run(context.Background(), Options{ControlSocket: socket})
	if err == nil || !strings.Contains(err.Error(), "refusing sandbox init control directory") {
		t.Fatalf("Run() error = %v, want a control directory rejection", err)
	}
	if _, statErr := os.Stat(socket); !os.IsNotExist(statErr) {
		t.Fatalf("socket exists after rejection: %v", statErr)
	}
}

func TestRunCreatesPrivateControlDirectory(t *testing.T) {
	controlDir := filepath.Join(t.TempDir(), "nested", "control")
	socket := filepath.Join(controlDir, "init.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{ControlSocket: socket})
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("init socket %s did not appear", socket)
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := os.Stat(controlDir)
	if err != nil {
		cancel()
		t.Fatalf("stat control dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		cancel()
		t.Fatalf("control dir mode = %04o, want 0700", info.Mode().Perm())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("init did not stop after cancellation")
	}
}
