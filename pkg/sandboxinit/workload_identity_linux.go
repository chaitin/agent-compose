//go:build linux

package sandboxinit

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// workloadIdentity is the optional non-root identity applied to every workload
// process. Init itself stays root so it can keep the control socket and mediate
// sockets with pidfd_getfd; only the exec target and its descendants drop.
type workloadIdentity struct {
	uid int
	gid int
}

func workloadIdentityFromEnv() (workloadIdentity, error) {
	rawUID := strings.TrimSpace(os.Getenv(WorkloadUIDEnvVar))
	rawGID := strings.TrimSpace(os.Getenv(WorkloadGIDEnvVar))
	if rawUID == "" && rawGID == "" {
		return workloadIdentity{}, nil
	}
	if rawUID == "" || rawGID == "" {
		return workloadIdentity{}, fmt.Errorf("%s and %s must be set together", WorkloadUIDEnvVar, WorkloadGIDEnvVar)
	}
	uid, err := parseIdentityID(WorkloadUIDEnvVar, rawUID)
	if err != nil {
		return workloadIdentity{}, err
	}
	gid, err := parseIdentityID(WorkloadGIDEnvVar, rawGID)
	if err != nil {
		return workloadIdentity{}, err
	}
	if uid == 0 || gid == 0 {
		return workloadIdentity{}, fmt.Errorf("%s and %s must be non-zero when set", WorkloadUIDEnvVar, WorkloadGIDEnvVar)
	}
	return workloadIdentity{uid: uid, gid: gid}, nil
}

func parseIdentityID(name, raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > 65535 {
		return 0, fmt.Errorf("%s must be a uid/gid between 0 and 65535, got %q", name, raw)
	}
	return value, nil
}

// prepareWorkloadOwnership hands the writable guest roots to the configured
// workload identity before any workload starts. It is deliberately limited to
// these roots; system paths stay root-owned and read-only.
func prepareWorkloadOwnership(identity workloadIdentity) error {
	if identity.uid == 0 {
		return nil
	}
	roots := []string{"/workspace", "/root", "/data/state", "/data/runtime", "/data/logs"}
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect workload root %s: %w", root, err)
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			if err := os.Chown(path, identity.uid, identity.gid); err != nil {
				return fmt.Errorf("chown %s: %w", path, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// applyWorkloadIdentity drops the exec child to the configured non-root uid/gid.
// It must run after the seccomp listener handoff and capability drop, because
// those operations need the init process's root capabilities.
func applyWorkloadIdentity(identity workloadIdentity) error {
	if identity.uid == 0 {
		return nil
	}
	if err := unix.Setgroups([]int{identity.gid}); err != nil {
		return fmt.Errorf("set workload supplementary groups: %w", err)
	}
	if err := unix.Setgid(identity.gid); err != nil {
		return fmt.Errorf("set workload gid: %w", err)
	}
	if err := unix.Setuid(identity.uid); err != nil {
		return fmt.Errorf("set workload uid: %w", err)
	}
	return nil
}
