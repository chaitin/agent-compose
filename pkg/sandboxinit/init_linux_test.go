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

func startTestInit(t *testing.T, mainShell string) (context.CancelFunc, string) {
	t.Helper()
	dir := t.TempDir()
	socket := filepath.Join(dir, "init.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{ControlSocket: socket, MainShell: mainShell})
	}()
	deadline := time.Now().Add(5 * time.Second)
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
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("init did not stop")
		}
	})
	return cancel, socket
}

func TestInitExecStreamsStdoutStderrAndExit(t *testing.T) {
	_, socket := startTestInit(t, "sleep 30")
	client := NewClient(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := client.Exec(ctx, Request{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf out; printf err >&2; exit 7"},
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr strings.Builder
	exitCode := -1
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		switch frame.Type {
		case FrameStdout:
			stdout.Write(frame.Data)
		case FrameStderr:
			stderr.Write(frame.Data)
		case FrameExit:
			exitCode = frame.Code
		default:
			t.Fatalf("unexpected frame type %q", frame.Type)
		}
		if frame.Type == FrameExit {
			break
		}
	}
	if stdout.String() != "out" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "out")
	}
	if stderr.String() != "err" {
		t.Fatalf("stderr = %q, want %q", stderr.String(), "err")
	}
	if exitCode != 7 {
		t.Fatalf("exit code = %d, want 7", exitCode)
	}
}

func TestInitPingAndList(t *testing.T) {
	_, socket := startTestInit(t, "sleep 30")
	client := NewClient(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	processes, err := client.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(processes) != 1 || !processes[0].Main {
		t.Fatalf("List() = %#v, want exactly the main process", processes)
	}
}

func TestInitKillTreeByMarker(t *testing.T) {
	_, socket := startTestInit(t, "sleep 30")
	client := NewClient(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const marker = "test-marker"
	session, err := client.Exec(ctx, Request{
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 30"},
		Marker:  marker,
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	if err := client.KillTree(ctx, marker); err != nil {
		t.Fatalf("KillTree() error = %v", err)
	}
	// The exec session must observe termination after the tree kill.
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatalf("Next() after KillTree error = %v", err)
		}
		if frame.Type == FrameExit {
			break
		}
	}
}

func TestInitTTYStreamsAndExits(t *testing.T) {
	_, socket := startTestInit(t, "sleep 30")
	client := NewClient(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := client.Exec(ctx, Request{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf tty-ok; exit 0"},
		TTY:     true,
		Rows:    24,
		Cols:    80,
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	var output strings.Builder
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		if frame.Type == FrameStdout {
			output.Write(frame.Data)
		}
		if frame.Type == FrameExit {
			if frame.Code != 0 {
				t.Fatalf("exit code = %d, want 0", frame.Code)
			}
			break
		}
	}
	if !strings.Contains(output.String(), "tty-ok") {
		t.Fatalf("tty output = %q, want tty-ok", output.String())
	}
}

func TestInitImmediateExitDoesNotLoseTheExitFrame(t *testing.T) {
	_, socket := startTestInit(t, "sleep 30")
	client := NewClient(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for i := 0; i < 50; i++ {
		session, err := client.Exec(ctx, Request{Command: "/bin/true"})
		if err != nil {
			t.Fatalf("Exec() iteration %d error = %v", i, err)
		}
		for {
			frame, err := session.Next(ctx)
			if err != nil {
				t.Fatalf("Next() iteration %d error = %v", i, err)
			}
			if frame.Type == FrameExit {
				break
			}
		}
		_ = session.Close()
	}
}
