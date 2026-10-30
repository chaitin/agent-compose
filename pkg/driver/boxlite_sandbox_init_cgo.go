//go:build linux && cgo && boxlitecgo

package driver

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

const (
	boxliteInitGuestDir    = "/.agent-compose/run"
	boxliteInitGuestSocket = boxliteInitGuestDir + "/init.sock"
)

func (r *cgoSandboxRuntime) sandboxInitEnabled(vmState VMState) bool {
	return SandboxInitEnabledFor(r.config, vmState)
}

func (r *cgoSandboxRuntime) sandboxInitClient(sandbox *Sandbox) *sandboxinit.Client {
	return sandboxinit.NewClient(sandboxInitHostSocket(r.config, sandbox.Summary.ID))
}

func (r *cgoSandboxRuntime) sandboxInitConfigured(sandbox *Sandbox, vmState VMState) bool {
	if sandbox == nil {
		return false
	}
	// A recorded state is authoritative. Without one, the host directory is
	// the marker for an init runtime created before the state existed. If
	// neither is present, an existing box is legacy even when the daemon flag is
	// now on: reusing it as init would wait for a socket that was never created.
	if state := strings.TrimSpace(vmState.SandboxInitState); state != "" {
		return state == SandboxInitStateEnabled
	}
	if _, err := os.Stat(sandboxInitHostDir(r.config, sandbox.Summary.ID)); err == nil {
		return true
	}
	return vmState.StartedAt.IsZero() && SandboxInitEnabledFor(r.config, vmState)
}

func (r *cgoSandboxRuntime) waitForSandboxInit(ctx context.Context, sandbox *Sandbox) error {
	timeout := 2 * time.Minute
	if r.config != nil && r.config.SandboxStartTimeout > 0 {
		timeout = r.config.SandboxStartTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := r.sandboxInitClient(sandbox)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := client.Ping(waitCtx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-waitCtx.Done():
			if lastErr == nil {
				lastErr = waitCtx.Err()
			}
			return fmt.Errorf("boxlite init did not become ready within %s: %w", timeout, lastErr)
		case <-ticker.C:
		}
	}
}
