package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	mountapi "github.com/docker/docker/api/types/mount"

	"github.com/chaitin/agent-compose/pkg/nfsvolume"
)

// An nfs volume reaches each runtime as a SandboxVolumeMount whose HostPath
// is an nfs:// reference, not a host directory. Each runtime mounts it its own
// way:
//
//   - Docker asks the Engine to mount it as a local-driver NFS volume, so the
//     daemon needs no mount privileges and the mount lives in the Engine's
//     namespace, where the container can see it.
//   - BoxLite and Microsandbox mount it with the kernel, from the process that
//     runs the VM (the sandbox-host in the KVM deployment), at a mount point
//     under <sandbox>/volumes/. That directory is where BoxLite's volume
//     bridge already lives, so the cleanup that unmounts bridge mounts before
//     a sandbox directory is deleted also unmounts the export. Deleting the
//     sandbox directory therefore never recurses into the server's files.
//   - Kubernetes mounts it as an inline NFS volume of the Pod.

const nfsMountPointMode = 0o755

// sandboxNFSMount is one nfs volume mount of a sandbox with its decoded
// export and the host mount point VM runtimes use for it.
type sandboxNFSMount struct {
	mount      SandboxVolumeMount
	source     nfsvolume.Source
	mountPoint string
}

func isNFSVolumeMount(mount SandboxVolumeMount) bool {
	return strings.ToLower(strings.TrimSpace(mount.Type)) == "volume" &&
		strings.ToLower(strings.TrimSpace(mount.Driver)) == nfsvolume.DriverName
}

// nfsVolumeMountPoint is the host directory a VM runtime mounts the export
// on. It shares the BoxLite bridge path, so BoxLite's guest symlink for the
// mount points at the export without a second bind mount.
func nfsVolumeMountPoint(session *Sandbox, mount SandboxVolumeMount) string {
	return filepath.Join(hostSandboxDir(session), boxliteVolumeBridgeSessionPath, boxliteVolumeBridgeID(mount))
}

// sandboxNFSMounts decodes every nfs volume mount of the sandbox. A reference
// that does not decode is an error rather than a skipped mount: running the
// agent without a volume it declared would hide the failure.
func sandboxNFSMounts(session *Sandbox) ([]sandboxNFSMount, error) {
	if session == nil {
		return nil, nil
	}
	var out []sandboxNFSMount
	for _, mount := range session.VolumeMounts {
		if !isNFSVolumeMount(mount) {
			continue
		}
		source, err := nfsvolume.ParseRef(mount.HostPath)
		if err != nil {
			return nil, fmt.Errorf("sandbox %s volume %q: %w", session.Summary.ID, mount.Source, err)
		}
		mountPoint := nfsVolumeMountPoint(session, mount)
		if strings.TrimSpace(hostSandboxDir(session)) == "" || !filepath.IsAbs(mountPoint) {
			return nil, fmt.Errorf("sandbox %s volume %q: sandbox directory is unknown", session.Summary.ID, mount.Source)
		}
		out = append(out, sandboxNFSMount{mount: mount, source: source, mountPoint: mountPoint})
	}
	return out, nil
}

type nfsVolumeMountFunc func(ctx context.Context, source nfsvolume.Source, mountPoint string, readOnly bool) error

// nfsVolumeMountState reports whether path is a mount point and, if so,
// whether an NFS file system is mounted there.
type nfsVolumeMountStateFunc func(path string) (mounted bool, isNFS bool, err error)

var (
	nfsVolumeMounter      nfsVolumeMountFunc      = mountNFSVolume
	nfsVolumeMountStateOf nfsVolumeMountStateFunc = nfsMountState
)

// ensureSandboxNFSMounts mounts the sandbox's nfs volumes on their host mount
// points for a VM runtime. It runs in the process that starts the VM, right
// before the VM starts, and is idempotent: an export that is still mounted
// from an earlier start is kept. On failure it unmounts what this call
// mounted, so a half-prepared sandbox does not keep server connections.
func ensureSandboxNFSMounts(ctx context.Context, session *Sandbox) error {
	mounts, err := sandboxNFSMounts(session)
	if err != nil || len(mounts) == 0 {
		return err
	}
	var mounted []string
	for _, item := range mounts {
		if err := os.MkdirAll(item.mountPoint, nfsMountPointMode); err != nil {
			rollbackBoxliteVolumeBridgeMounts(mounted)
			return fmt.Errorf("create nfs volume mount point %s: %w", item.mountPoint, err)
		}
		mountedNow, err := ensureNFSMountPoint(ctx, item)
		if err != nil {
			rollbackBoxliteVolumeBridgeMounts(mounted)
			return err
		}
		if mountedNow {
			mounted = append(mounted, item.mountPoint)
		}
	}
	return nil
}

// validateSandboxNFSMounts rejects undecodable nfs references while the mount
// manifest is built, before any runtime is asked to start the sandbox.
func validateSandboxNFSMounts(session *Sandbox) error {
	_, err := sandboxNFSMounts(session)
	return err
}

const (
	dockerNFSVolumeManagedLabel = "agent-compose.managed"
	dockerNFSVolumeLabel        = "agent-compose.nfs-volume"
)

// dockerNFSVolumeMounts maps each nfs mount point that appears in the Docker
// mount manifest to the Engine-managed volume mount that replaces the bind.
func dockerNFSVolumeMounts(session *Sandbox) (map[string]sandboxNFSMount, error) {
	mounts, err := sandboxNFSMounts(session)
	if err != nil || len(mounts) == 0 {
		return nil, err
	}
	out := make(map[string]sandboxNFSMount, len(mounts))
	for _, item := range mounts {
		key, err := filepath.Abs(item.mountPoint)
		if err != nil {
			return nil, fmt.Errorf("resolve nfs volume mount point %s: %w", item.mountPoint, err)
		}
		out[key] = item
	}
	return out, nil
}

func isDockerNFSVolumeName(name string) bool {
	return strings.HasPrefix(name, nfsvolume.DockerVolumeNamePrefix)
}

// dockerNFSVolumeMount asks the Engine to mount the export through the local
// volume driver. The Engine creates the named volume on first use and reuses
// it afterwards; the name covers every mount setting, so a reused volume
// always carries the settings of this export.
func dockerNFSVolumeMount(item sandboxNFSMount, target string, readOnly bool) mountapi.Mount {
	return mountapi.Mount{
		Type:     mountapi.TypeVolume,
		Source:   item.source.DockerVolumeName(),
		Target:   target,
		ReadOnly: readOnly,
		VolumeOptions: &mountapi.VolumeOptions{
			Labels: map[string]string{
				dockerNFSVolumeManagedLabel: "true",
				dockerNFSVolumeLabel:        item.source.Ref(),
			},
			DriverConfig: &mountapi.Driver{Name: "local", Options: item.source.DockerVolumeOptions()},
			// Docker otherwise copies the image's files at the target into an
			// empty volume when the container is created, even for a
			// read-only mount, which would write them to the shared export.
			NoCopy: true,
		},
	}
}
