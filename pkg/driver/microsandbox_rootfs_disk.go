//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-compose/pkg/imagecache"
)

type microsandboxRootfsDiskResult struct {
	Path    string
	Created bool
}

type qemuImageInfo struct {
	Format              string `json:"format"`
	BackingFilename     string `json:"backing-filename"`
	FullBackingFilename string `json:"full-backing-filename"`
}

func (r *microsandboxRuntime) rootfsDiskPath(sandboxID string) string {
	return filepath.Join(r.config.MicrosandboxHome, "rootfs-disks", microsandboxDockerDiskName(sandboxID)+".qcow2")
}

func (r *microsandboxRuntime) ensureRootfsDiskWithCacheLock(ctx context.Context, sandboxID string, base microsandboxBaseDisk) (microsandboxRootfsDiskResult, error) {
	cache, err := imagecache.New(imagecache.Config{
		Root: imageCacheRootForDriver(r.config), DefaultRegistry: r.config.ImageRegistry,
		InsecureRegistries: r.config.ImageInsecureRegistries,
	})
	if err != nil {
		return microsandboxRootfsDiskResult{}, fmt.Errorf("open image cache for microsandbox rootfs disk: %w", err)
	}
	unlock, err := cache.LockContext(ctx)
	if err != nil {
		return microsandboxRootfsDiskResult{}, fmt.Errorf("lock image cache for microsandbox rootfs disk: %w", err)
	}
	defer func() { _ = unlock() }()
	return r.ensureRootfsDisk(ctx, sandboxID, base)
}

func (r *microsandboxRuntime) ensureRootfsDisk(ctx context.Context, sandboxID string, base microsandboxBaseDisk) (microsandboxRootfsDiskResult, error) {
	path := r.rootfsDiskPath(sandboxID)
	root := filepath.Dir(path)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return microsandboxRootfsDiskResult{}, fmt.Errorf("create microsandbox rootfs-disks directory: %w", err)
	}
	if result, decided, err := r.reuseOrCleanRootfsDisk(ctx, path, sandboxID, base); decided || err != nil {
		return result, err
	}
	started := time.Now()
	tmp, err := os.CreateTemp(root, ".rootfs-*.qcow2")
	if err != nil {
		return microsandboxRootfsDiskResult{}, fmt.Errorf("create temporary microsandbox rootfs disk: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Close(); err != nil {
		return microsandboxRootfsDiskResult{}, err
	}
	if err := os.Remove(tmpPath); err != nil {
		return microsandboxRootfsDiskResult{}, err
	}
	args := []string{"create", "-f", "qcow2", "-F", "qcow2", "-b", base.Path, tmpPath}
	if output, err := exec.CommandContext(ctx, "qemu-img", args...).CombinedOutput(); err != nil {
		return microsandboxRootfsDiskResult{}, fmt.Errorf("create microsandbox rootfs disk for sandbox %s: %w: %s", sandboxID, err, strings.TrimSpace(string(output)))
	}
	if err := validateQcowBacking(ctx, tmpPath, base.Path); err != nil {
		return microsandboxRootfsDiskResult{}, err
	}
	if err := publishFileWithoutOverwrite(tmpPath, path); err != nil {
		if result, decided, reuseErr := r.reuseOrCleanRootfsDisk(ctx, path, sandboxID, base); decided || reuseErr != nil {
			return result, reuseErr
		}
		return microsandboxRootfsDiskResult{}, fmt.Errorf("publish microsandbox rootfs disk %s: %w", path, err)
	}
	if err := writeMicrosandboxRootfsDiskOwnership(path, sandboxID, base); err != nil {
		_ = os.Remove(path)
		return microsandboxRootfsDiskResult{}, fmt.Errorf("publish microsandbox rootfs disk ownership: %w", err)
	}
	info, _ := os.Stat(path)
	slog.Info("agent-compose microsandbox created rootfs disk", "sandbox_id", sandboxID, "path", path, "backing_path", base.Path, "cache_identity", base.Identity, "duration", time.Since(started), "size_bytes", fileAllocatedBytes(info))
	return microsandboxRootfsDiskResult{Path: path, Created: true}, nil
}

func (r *microsandboxRuntime) reuseOrCleanRootfsDisk(ctx context.Context, path, sandboxID string, base microsandboxBaseDisk) (microsandboxRootfsDiskResult, bool, error) {
	manifestPath := path + ".owner.json"
	_, diskErr := os.Lstat(path)
	_, manifestErr := os.Lstat(manifestPath)
	if os.IsNotExist(diskErr) && os.IsNotExist(manifestErr) {
		return microsandboxRootfsDiskResult{}, false, nil
	}
	if os.IsNotExist(diskErr) != os.IsNotExist(manifestErr) {
		slog.Warn("agent-compose microsandbox removing incomplete rootfs disk", "sandbox_id", sandboxID, "disk_path", path, "disk_error", diskErr, "sidecar_error", manifestErr)
		if err := removeMicrosandboxRootfsDiskPair(r.config.MicrosandboxHome, path, false); err != nil {
			return microsandboxRootfsDiskResult{}, true, err
		}
		return microsandboxRootfsDiskResult{}, false, nil
	}
	if diskErr != nil || manifestErr != nil {
		return microsandboxRootfsDiskResult{}, true, fmt.Errorf("inspect microsandbox rootfs disk %s: disk=%v sidecar=%v", path, diskErr, manifestErr)
	}
	ownership, err := readMicrosandboxRootfsDiskOwnership(r.config.MicrosandboxHome, manifestPath)
	if err != nil {
		return microsandboxRootfsDiskResult{}, true, err
	}
	if ownership.SandboxID != sandboxID || ownership.ResourceKind != microsandboxRootfsDiskKind || ownership.BaseIdentity != base.Identity || ownership.DiskSizeGiB != base.DiskSizeGiB || filepath.Clean(ownership.BackingPath) != filepath.Clean(base.Path) {
		return microsandboxRootfsDiskResult{}, true, fmt.Errorf("microsandbox rootfs disk ownership %s does not match sandbox or base disk", manifestPath)
	}
	if err := validateQcowBacking(ctx, path, base.Path); err != nil {
		return microsandboxRootfsDiskResult{}, true, err
	}
	slog.Info("agent-compose microsandbox reusing rootfs disk", "sandbox_id", sandboxID, "path", path, "backing_path", base.Path, "cache_identity", base.Identity)
	return microsandboxRootfsDiskResult{Path: path}, true, nil
}

func validateQcowBacking(ctx context.Context, path, expectedBacking string) error {
	output, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", path).Output()
	if err != nil {
		return fmt.Errorf("inspect qcow2 disk %s: %w", path, err)
	}
	var info qemuImageInfo
	if err := json.Unmarshal(output, &info); err != nil {
		return fmt.Errorf("decode qcow2 disk info %s: %w", path, err)
	}
	backing := strings.TrimSpace(info.FullBackingFilename)
	if backing == "" {
		backing = strings.TrimSpace(info.BackingFilename)
		if !filepath.IsAbs(backing) {
			backing = filepath.Join(filepath.Dir(path), backing)
		}
	}
	if info.Format != "qcow2" || filepath.Clean(backing) != filepath.Clean(expectedBacking) {
		return fmt.Errorf("qcow2 disk %s backing %s does not match expected %s", path, backing, expectedBacking)
	}
	return nil
}

func readMicrosandboxRootfsDiskOwnership(home, manifestPath string) (microsandboxDiskOwnership, error) {
	if err := validateMicrosandboxRootfsOwnedPath(home, manifestPath); err != nil {
		return microsandboxDiskOwnership{}, err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return microsandboxDiskOwnership{}, err
	}
	var ownership microsandboxDiskOwnership
	if err := json.Unmarshal(data, &ownership); err != nil {
		return ownership, fmt.Errorf("decode microsandbox rootfs disk ownership %s: %w", manifestPath, err)
	}
	if ownership.Version != microsandboxDiskOwnershipVersion || ownership.ResourceKind != microsandboxRootfsDiskKind || strings.TrimSpace(ownership.SandboxID) == "" || strings.TrimSpace(ownership.DiskPath) == "" || strings.TrimSpace(ownership.BaseIdentity) == "" || strings.TrimSpace(ownership.BackingPath) == "" || ownership.DiskSizeGiB <= 0 || ownership.CreatedAt.IsZero() {
		return ownership, fmt.Errorf("microsandbox rootfs disk ownership %s is incomplete", manifestPath)
	}
	if err := validateMicrosandboxRootfsOwnedPath(home, ownership.DiskPath); err != nil {
		return ownership, err
	}
	if filepath.Clean(manifestPath) != filepath.Clean(ownership.DiskPath+".owner.json") {
		return ownership, fmt.Errorf("microsandbox rootfs ownership sidecar does not match disk path")
	}
	return ownership, nil
}

func removeMicrosandboxRootfsDiskPair(home, path string, requireOwnership bool) error {
	if requireOwnership {
		if _, err := readMicrosandboxRootfsDiskOwnership(home, path+".owner.json"); err != nil {
			return err
		}
	}
	for _, candidate := range []string{path, path + ".owner.json"} {
		if err := validateMicrosandboxRootfsOwnedPath(home, candidate); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := os.Remove(candidate); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove microsandbox rootfs resource %s: %w", candidate, err)
		}
	}
	return nil
}

func (r *microsandboxRuntime) removeRootfsDiskFiles(sandboxID string) error {
	path := r.rootfsDiskPath(sandboxID)
	manifestPath := path + ".owner.json"
	_, diskErr := os.Lstat(path)
	_, manifestErr := os.Lstat(manifestPath)
	switch {
	case os.IsNotExist(diskErr) && os.IsNotExist(manifestErr):
		return nil
	case diskErr == nil && os.IsNotExist(manifestErr):
		slog.Warn("agent-compose microsandbox removing rootfs disk without ownership sidecar", "sandbox_id", sandboxID, "disk_path", path)
		return removeMicrosandboxRootfsDiskPair(r.config.MicrosandboxHome, path, false)
	case os.IsNotExist(diskErr) && manifestErr == nil:
		slog.Warn("agent-compose microsandbox removing rootfs ownership sidecar without disk", "sandbox_id", sandboxID, "sidecar_path", manifestPath)
		return removeMicrosandboxRootfsDiskPair(r.config.MicrosandboxHome, path, false)
	case diskErr != nil:
		return fmt.Errorf("inspect microsandbox rootfs disk %s: %w", path, diskErr)
	case manifestErr != nil:
		return fmt.Errorf("inspect microsandbox rootfs sidecar %s: %w", manifestPath, manifestErr)
	}
	ownership, err := readMicrosandboxRootfsDiskOwnership(r.config.MicrosandboxHome, manifestPath)
	if err != nil {
		return err
	}
	if ownership.SandboxID != sandboxID {
		return fmt.Errorf("microsandbox rootfs disk %s is owned by sandbox %s, not %s", path, ownership.SandboxID, sandboxID)
	}
	return removeMicrosandboxRootfsDiskPair(r.config.MicrosandboxHome, path, false)
}
