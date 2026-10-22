package runs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCancelOnDaemonShutdownCancelsWhenShutdownFires(t *testing.T) {
	shutdown := make(chan struct{})
	// The run context survives shutdown; only the derived context stops.
	runCtx := WithDaemonShutdown(context.Background(), shutdown)
	derived, cancel := cancelOnDaemonShutdown(runCtx)
	defer cancel()

	close(shutdown)
	select {
	case <-derived.Done():
	case <-time.After(time.Second):
		t.Fatal("derived context was not cancelled on shutdown")
	}
	if !errors.Is(context.Cause(derived), context.Canceled) || !errors.Is(context.Cause(derived), errDaemonShuttingDown) {
		t.Fatalf("cause = %v, want daemon shutdown wrapping context.Canceled", context.Cause(derived))
	}
	if runCtx.Err() != nil {
		t.Fatalf("run context cancelled: %v", runCtx.Err())
	}
}

func TestCancelOnDaemonShutdownWithoutSignalReturnsContext(t *testing.T) {
	ctx := context.Background()
	derived, cancel := cancelOnDaemonShutdown(ctx)
	defer cancel()
	if derived != ctx {
		t.Fatal("want the same context when no shutdown signal is attached")
	}
}
