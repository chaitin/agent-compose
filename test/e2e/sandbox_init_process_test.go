//go:build linux

package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

// TestE2ESandboxInitProcessLifecycle exercises the real init process and the
// control protocol over a Unix socket without a container runtime. It keeps the
// E2E shape covering the init server/client boundary that the unit suite also
// exercises, and verifies the shutdown path used by drivers.
func TestE2ESandboxInitProcessLifecycle(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "init.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- sandboxinit.Run(ctx, sandboxinit.Options{ControlSocket: socket})
	}()
	waitForSocket(t, socket)
	client := sandboxinit.NewClient(socket)
	opCtx, opCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer opCancel()
	if err := client.Ping(opCtx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	session, err := client.Exec(opCtx, sandboxinit.Request{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf e2e-ok; exit 0"},
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	var stdout strings.Builder
	for {
		frame, err := session.Next(opCtx)
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		if frame.Type == sandboxinit.FrameStdout {
			stdout.Write(frame.Data)
		}
		if frame.Type == sandboxinit.FrameExit {
			if frame.Code != 0 || stdout.String() != "e2e-ok" {
				t.Fatalf("exit = %d stdout = %q, want 0/e2e-ok", frame.Code, stdout.String())
			}
			break
		}
	}
	_ = session.Close()

	marker := "e2e-init-marker"
	pidFile := filepath.Join(t.TempDir(), "detached.pid")
	detached, err := client.Exec(opCtx, sandboxinit.Request{
		Command: "/bin/sh",
		Args:    []string{"-c", "echo $$ > " + pidFile + "; exec sleep 300"},
		Marker:  marker,
	})
	if err != nil {
		t.Fatalf("Exec(detached) error = %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detached process did not record its pid")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := client.KillTree(opCtx, marker); err != nil {
		t.Fatalf("KillTree() error = %v", err)
	}
	// The kill must reach the session and the process tree, not merely return.
	var killedCode int
	for {
		frame, err := detached.Next(opCtx)
		if err != nil {
			t.Fatalf("detached.Next() error = %v", err)
		}
		if frame.Type == sandboxinit.FrameExit {
			killedCode = frame.Code
			break
		}
	}
	if killedCode == 0 {
		t.Fatal("detached process exited successfully after KillTree, want a signal exit")
	}
	_ = detached.Close()
	check, err := client.Exec(opCtx, sandboxinit.Request{
		Command: "/bin/sh",
		Args:    []string{"-c", "pid=$(cat " + pidFile + "); kill -0 $pid 2>/dev/null && echo ALIVE || echo GONE"},
	})
	if err != nil {
		t.Fatalf("Exec(kill_tree check) error = %v", err)
	}
	var checkOutput strings.Builder
	for {
		frame, err := check.Next(opCtx)
		if err != nil {
			t.Fatalf("check.Next() error = %v", err)
		}
		if frame.Type == sandboxinit.FrameStdout {
			checkOutput.Write(frame.Data)
		}
		if frame.Type == sandboxinit.FrameExit {
			break
		}
	}
	_ = check.Close()
	if strings.TrimSpace(checkOutput.String()) != "GONE" {
		t.Fatalf("detached process after KillTree = %q, want GONE", strings.TrimSpace(checkOutput.String()))
	}

	if err := client.Shutdown(opCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("init did not stop after shutdown")
	}
}

func waitForSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("init socket %s did not appear", socket)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
