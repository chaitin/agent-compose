//go:build linux && cgo && microsandboxcgo

package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type microsandboxDiskOwnership struct {
	Version      int       `json:"version"`
	ResourceKind string    `json:"resource_kind,omitempty"`
	SandboxID    string    `json:"sandbox_id"`
	DiskPath     string    `json:"disk_path"`
	BaseIdentity string    `json:"base_cache_identity,omitempty"`
	BackingPath  string    `json:"backing_file_path,omitempty"`
	DiskSizeGiB  int32     `json:"disk_size_gib,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

const (
	microsandboxDiskOwnershipVersion = 2
	microsandboxLegacyDockerDiskKind = "microsandbox-docker-data"
	microsandboxRootfsDiskKind       = "microsandbox-rootfs"
)

func writeMicrosandboxRootfsDiskOwnership(diskPath, sandboxID string, base microsandboxBaseDisk) error {
	return writeMicrosandboxDiskOwnershipRecord(microsandboxDiskOwnership{
		Version: microsandboxDiskOwnershipVersion, ResourceKind: microsandboxRootfsDiskKind,
		SandboxID: sandboxID, DiskPath: diskPath, BaseIdentity: base.Identity,
		BackingPath: base.Path, DiskSizeGiB: base.DiskSizeGiB,
	})
}

func writeMicrosandboxDiskOwnershipRecord(record microsandboxDiskOwnership) error {
	diskPath, sandboxID := record.DiskPath, record.SandboxID
	diskPath = filepath.Clean(strings.TrimSpace(diskPath))
	sandboxID = strings.TrimSpace(sandboxID)
	if diskPath == "." || sandboxID == "" {
		return fmt.Errorf("microsandbox disk ownership requires disk path and sandbox id")
	}
	manifestPath := diskPath + ".owner.json"
	createdAt := time.Now().UTC()
	if data, err := os.ReadFile(manifestPath); err == nil {
		var existing microsandboxDiskOwnership
		if json.Unmarshal(data, &existing) == nil && existing.SandboxID == sandboxID && !existing.CreatedAt.IsZero() {
			createdAt = existing.CreatedAt
		}
	}
	record.Version = microsandboxDiskOwnershipVersion
	record.SandboxID = sandboxID
	record.DiskPath = diskPath
	record.CreatedAt = createdAt
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(manifestPath), ".disk-owner-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, manifestPath); err != nil {
		return err
	}
	return nil
}
