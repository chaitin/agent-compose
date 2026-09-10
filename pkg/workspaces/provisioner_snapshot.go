package workspaces

import (
	"context"
	"log/slog"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func (p *Provisioner) releaseReadySnapshot(ctx context.Context, sandbox *domain.Sandbox) {
	if sandbox.Workspace == nil || sandbox.Workspace.SnapshotID == "" || p.config == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := ReleaseTransientSnapshot(cleanupCtx, p.config, sandbox.Workspace); err != nil {
		// Ready is already durable. A reclaim failure must not invalidate the
		// usable workspace; the marker supports later cleanup/recovery retries.
		slog.Warn("failed to release provisioned workspace snapshot", "sandbox_id", sandbox.Summary.ID, "error", err)
	}
}
