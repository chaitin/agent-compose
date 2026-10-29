//go:build linux && cgo && boxlitecgo

package driver

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

const (
	boxliteInitGuestDir    = "/.agent-compose/run"
	boxliteInitGuestSocket = boxliteInitGuestDir + "/init.sock"
)

func (r *cgoSandboxRuntime) sandboxInitEnabled() bool {
	return r != nil && r.config != nil && r.config.SandboxInitEnabled
}

func (r *cgoSandboxRuntime) sandboxInitClient(sandbox *Sandbox) *sandboxinit.Client {
	return sandboxinit.NewClient(sandboxInitHostSocket(r.config, sandbox.Summary.ID))
}

func (r *cgoSandboxRuntime) sandboxInitConfigured(sandbox *Sandbox) bool {
	_, err := os.Stat(sandboxInitHostDir(r.config, sandbox.Summary.ID))
	return err == nil
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
