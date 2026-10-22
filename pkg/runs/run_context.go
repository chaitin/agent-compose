package runs

import (
	"context"
	"fmt"
)

// daemonShutdownContextKey carries the daemon's shutdown signal through a run
// context. The run context itself must survive a daemon shutdown (so a
// detached task keeps running), but the attach exec that follows the task must
// disconnect: the attach context is derived from the run context without its
// cancellation and then closed when this signal fires.
type daemonShutdownContextKey struct{}

// WithDaemonShutdown attaches the daemon's shutdown signal to ctx as a value.
// shutdown is normally the daemon root context's Done channel, which closes
// when the process receives SIGTERM/SIGINT.
func WithDaemonShutdown(ctx context.Context, shutdown <-chan struct{}) context.Context {
	if shutdown == nil {
		return ctx
	}
	return context.WithValue(ctx, daemonShutdownContextKey{}, shutdown)
}

// DaemonShutdown returns the shutdown signal attached by WithDaemonShutdown.
func DaemonShutdown(ctx context.Context) (<-chan struct{}, bool) {
	shutdown, ok := ctx.Value(daemonShutdownContextKey{}).(<-chan struct{})
	return shutdown, ok && shutdown != nil
}

// runExecContext derives an exec context from a run context and, when the run
// context carries a daemon shutdown signal, also cancels the exec context when
// that signal fires. The legacy exec path uses it so an old-guest run is still
// terminated on daemon shutdown even though the run context no longer derives
// from the daemon root.
func runExecContext(ctx context.Context) (context.Context, context.CancelFunc) {
	execCtx, cancel := context.WithCancelCause(ctx)
	if shutdown, ok := DaemonShutdown(ctx); ok {
		go func() {
			select {
			case <-shutdown:
				cancel(ErrRunDisconnected)
			case <-execCtx.Done():
			}
		}()
	}
	return execCtx, func() { cancel(nil) }
}

// errDaemonShuttingDown is the cancellation cause for run work that, unlike a
// detached command, cannot outlive the daemon: sandbox preparation and agent
// runs. It wraps context.Canceled so the run is finalized as canceled.
var errDaemonShuttingDown = fmt.Errorf("daemon shutting down: %w", context.Canceled)

// cancelOnDaemonShutdown derives a context that is also cancelled when the
// shutdown signal carried by ctx fires. The run context deliberately survives
// shutdown so a detached command keeps running; everything else in the run
// uses this context to keep the pre-detach behaviour of stopping on shutdown.
func cancelOnDaemonShutdown(ctx context.Context) (context.Context, context.CancelFunc) {
	shutdown, ok := DaemonShutdown(ctx)
	if !ok {
		return ctx, func() {}
	}
	derived, cancel := context.WithCancelCause(ctx)
	go func() {
		select {
		case <-shutdown:
			cancel(errDaemonShuttingDown)
		case <-derived.Done():
		}
	}()
	return derived, func() { cancel(nil) }
}
