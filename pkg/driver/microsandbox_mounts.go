//go:build linux && cgo && microsandboxcgo

package driver

import (
	"log/slog"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

func (r *microsandboxRuntime) microsandboxCreateMounts(manifest RuntimeMountManifest, sandboxName string) map[string]microsandbox.MountConfig {
	mounts := make(map[string]microsandbox.MountConfig, len(manifest.Mounts)+1)
	for _, mount := range manifest.Mounts {
		bindMount := r.microsandboxBindMount(mount.HostPath, mount.ReadOnly)
		mounts[mount.GuestPath] = bindMount
		slog.Info(
			"agent-compose microsandbox configured bind mount",
			"sandbox", sandboxName,
			"guest_path", mount.GuestPath,
			"readonly", mount.ReadOnly,
			"quota_mib", bindMount.QuotaMiB,
			"configured_bind_quota_gb", r.config.SandboxDiskSizeGB,
			"sandbox_disk_size_gb", r.config.SandboxDiskSizeGB,
		)
	}

	// /run must be a per-VM tmpfs because msb guest init does not mount it.
	// The private root disk persists across stop/start, so runtime state written
	// under /run would otherwise outlive the VM. A stale
	// /run/docker/containerd/containerd.pid then makes dockerd kill its own
	// containerd and refuse to start. agentd recreates /run/microsandbox after
	// user tmpfs mounts are applied, so shadowing /run here is safe.
	mounts["/run"] = microsandbox.Mount.Tmpfs(microsandbox.TmpfsOptions{SizeMiB: 256})
	return mounts
}
