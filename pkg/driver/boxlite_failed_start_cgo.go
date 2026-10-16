//go:build linux && cgo && boxlitecgo

package driver

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// boxliteFailedStartCleanupTimeout bounds releasing a box whose start failed.
// The caller's context has usually expired by then (that is one way a start
// fails), so cleanup gets its own bound instead of inheriting that expiry.
const boxliteFailedStartCleanupTimeout = 30 * time.Second

// releaseFailedBoxStart releases everything a failed start left behind.
//
// boxlite reports the failure but keeps the box's detached shim - and the bwrap
// jail holding the VM's /dev/kvm handle - alive, so repeated failures accumulate
// KVM handles until the host's kvm module can no longer be unloaded (test round
// D2b). The failure itself stays the caller's error: this only logs what it
// could not release, because a start that failed must not be reported as a start
// that failed for a cleanup reason.
func (r *cgoSandboxRuntime) releaseFailedBoxStart(ctx context.Context, sandbox *Sandbox, box *cgoBoxHandle) {
	lookup := ""
	if box != nil {
		if boxID, err := r.boxID(box); err == nil {
			lookup = boxID
		} else {
			slog.Warn("agent-compose boxlite failed start cleanup cannot read the box id", "sandbox_id", sandbox.Summary.ID, "error", err)
		}
	}
	if lookup == "" && sandbox != nil {
		lookup = strings.TrimSpace(sandbox.Summary.RuntimeRef)
	}

	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boxliteFailedStartCleanupTimeout)
	defer cancel()
	if lookup != "" {
		if err := r.removeBox(cleanupCtx, lookup, true); err != nil && !isBoxNotFound(err) {
			slog.Warn("agent-compose boxlite failed start cleanup could not remove the box",
				"sandbox_id", sandbox.Summary.ID, "box", lookup, "error", err)
		}
	}

	reaped, err := ReapBoxliteShimProcesses(r.config.BoxliteHome, lookup)
	switch {
	case err != nil:
		slog.Warn("agent-compose boxlite failed start cleanup could not release the shim process",
			"sandbox_id", sandbox.Summary.ID, "box", lookup, "error", err)
	case reaped:
		slog.Info("agent-compose boxlite failed start cleanup released the shim process",
			"sandbox_id", sandbox.Summary.ID, "box", lookup)
	}
}
