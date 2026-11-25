//go:build linux

package driver

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/chaitin/agent-compose/pkg/nfsvolume"
)

// mountNFSVolume mounts the export with the kernel NFS client, the way the
// Docker local volume driver does, so the runtime image needs no mount.nfs
// helper. The kernel does not resolve names, so the server is resolved here.
// nosuid and nodev keep a file planted on the server from gaining privileges
// on the host that serves the mount to the VM.
//
// The kernel mount cannot be interrupted, so this call blocks until it
// returns; ensureNFSMountPoint runs it in a tracked attempt that a caller can
// stop waiting for.
func mountNFSVolume(ctx context.Context, source nfsvolume.Source, mountPoint string, readOnly bool) error {
	addr := source.Server
	if net.ParseIP(addr) == nil {
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", addr)
		if err != nil {
			return fmt.Errorf("resolve nfs server %s: %w", addr, err)
		}
		if len(ips) == 0 {
			return fmt.Errorf("resolve nfs server %s: no addresses", addr)
		}
		addr = ips[0].String()
	}
	flags := uintptr(unix.MS_NOSUID | unix.MS_NODEV)
	if readOnly {
		flags |= unix.MS_RDONLY
	}
	return unix.Mount(source.Device(), mountPoint, "nfs", flags, source.KernelMountData(addr))
}

// nfsMountState reads this process's mount table, which is the one the VM
// runtime launched from this process sees.
func nfsMountState(path string) (bool, bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, false, fmt.Errorf("read mountinfo: %w", err)
	}
	return parseNFSMountState(string(data), path)
}

// parseNFSMountState finds the last mount on path in mountinfo text, which is
// the one visible there, and reports whether its file system type is nfs or
// nfs4.
func parseNFSMountState(mountinfo string, path string) (bool, bool, error) {
	clean := filepath.Clean(path)
	mounted, isNFS := false, false
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || filepath.Clean(unescapeMountInfoPath(fields[4])) != clean {
			continue
		}
		fsType := ""
		for i := 5; i < len(fields)-1; i++ {
			if fields[i] == "-" {
				fsType = fields[i+1]
				break
			}
		}
		mounted = true
		isNFS = fsType == "nfs" || fsType == "nfs4"
	}
	return mounted, isNFS, nil
}
