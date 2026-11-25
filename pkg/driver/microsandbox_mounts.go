//go:build linux && cgo && microsandboxcgo

package driver

import (
	"log/slog"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

func (r *microsandboxRuntime) microsandboxCreateMounts(manifest RuntimeMountManifest, sandboxName string, nfsMounts map[string]sandboxNFSMount) map[string]microsandbox.MountConfig {
	mounts := make(map[string]microsandbox.MountConfig, len(manifest.Mounts)+1)
	for _, mount := range manifest.Mounts {
		bindMount := r.microsandboxBindMount(mount.HostPath, mount.ReadOnly)
		if _, ok := nfsMounts[mount.HostPath]; ok {
			bindMount = microsandboxNFSBindMount(bindMount)
		}
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

// microsandboxNFSBindMount adapts the bind of an nfs mount point. Strict stat
// virtualization keeps guest ownership and modes in an xattr overlay and
// leaves host inodes at private 0600/0700 modes, so files an agent writes
// would look private to every other client of the export; an NFS server also
// may not take the overlay xattr at all. With virtualization off the guest
// sees and sets the server's own metadata, which the server enforces, as on
// any other client. nosuid and nodev repeat the host mount's flags in the
// guest.
func microsandboxNFSBindMount(mount microsandbox.MountConfig) microsandbox.MountConfig {
	mount.StatVirtualization = microsandbox.StatVirtualizationOff
	mount.Nosuid = true
	mount.Nodev = true
	return mount
}
