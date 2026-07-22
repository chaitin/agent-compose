//go:build linux && cgo && microsandboxcgo

package driver

import (
	"fmt"
	"os"
	"path/filepath"
)

// legacyHashedDockerDiskPath identifies the final hashed path used by the
// retired per-sandbox /var/lib/docker disk provisioning flow. New sandboxes do
// not create or mount this resource.
func (r *microsandboxRuntime) legacyHashedDockerDiskPath(sandboxID string) string {
	return filepath.Join(r.config.MicrosandboxHome, "docker-disks", microsandboxDiskName(sandboxID)+".raw")
}

// legacyUnhashedDockerDiskPath identifies the earlier path format that used a
// sandbox ID directly as the filename.
func (r *microsandboxRuntime) legacyUnhashedDockerDiskPath(sandboxID string) string {
	return filepath.Join(r.config.MicrosandboxHome, "docker-disks", sandboxID+".raw")
}

func (r *microsandboxRuntime) removeLegacyDockerDiskFiles(sandboxID string) error {
	diskPaths := []string{r.legacyHashedDockerDiskPath(sandboxID)}
	if unhashed := r.legacyUnhashedDockerDiskPath(sandboxID); unhashed != diskPaths[0] {
		diskPaths = append(diskPaths, unhashed)
	}

	var existing []string
	for _, diskPath := range diskPaths {
		for _, candidate := range []string{diskPath, diskPath + ".owner.json"} {
			if _, err := os.Lstat(candidate); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return fmt.Errorf("inspect legacy microsandbox Docker disk resource %s: %w", candidate, err)
			}
			if err := validateMicrosandboxLegacyDockerOwnedPath(r.config.MicrosandboxHome, candidate); err != nil {
				return fmt.Errorf("validate legacy microsandbox Docker disk resource %s: %w", candidate, err)
			}
			existing = append(existing, candidate)
		}
	}

	for _, path := range existing {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove legacy microsandbox Docker disk resource %s: %w", path, err)
		}
	}
	return nil
}
