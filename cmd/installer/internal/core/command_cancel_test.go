//go:build unix

package core

import (
	"context"
	"io"
	"testing"
	"time"
)

// `docker` runs the compose plugin as a grandchild that inherits the output
// pipe. Cancelling must reach it too: killing only the direct child leaves the
// pipe open and Wait blocks until the grandchild exits on its own.
func TestExecRunnerCancelStopsGrandchildHoldingOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := ExecRunner{Output: io.Discard}
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	started := time.Now()
	go func() {
		// The backgrounded sleep inherits stdout and outlives its parent shell.
		done <- runner.Run(ctx, "", "sh", "-c", "sleep 60 & sleep 60")
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled command reported success")
		}
		if elapsed := time.Since(started); elapsed > commandCancelGrace {
			t.Fatalf("cancellation took %s, want well under %s", elapsed, commandCancelGrace)
		}
	case <-time.After(commandCancelGrace + 3*time.Second):
		t.Fatal("cancelled command never returned; output pipe is still held open")
	}
}
