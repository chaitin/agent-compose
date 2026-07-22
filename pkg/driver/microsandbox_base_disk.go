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
	"runtime"
	"strings"
	"syscall"
	"time"

	"agent-compose/pkg/imagecache"
	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"golang.org/x/sys/unix"
)

const microsandboxBaseDiskFormatVersion = 1

type microsandboxBaseDisk struct {
	Identity    string
	Source      string
	ImageID     string
	ResolvedRef string
	Path        string
	Manifest    string
	CacheRoot   string
	Env         []string
	DiskSizeGiB int32
}

type microsandboxBaseDiskManifest struct {
	FormatVersion int       `json:"format_version"`
	Identity      string    `json:"identity"`
	Source        string    `json:"source"`
	ImageID       string    `json:"image_id"`
	ResolvedRef   string    `json:"resolved_ref"`
	Architecture  string    `json:"architecture"`
	DiskSizeGiB   int32     `json:"disk_size_gib"`
	Path          string    `json:"path"`
	CreatedAt     time.Time `json:"created_at"`
}

// microsandboxBaseDiskIdentity keys a base disk by everything that changes its
// bytes, including the image source. Source belongs here because the two
// sources lay the image out with different extractors -- the image cache
// resolves .wh. whiteouts itself while Docker exports an already-merged
// container filesystem -- and because both report the config digest as the
// image id, so the same image resolved either way would otherwise collide on
// one cache key. A published base disk is immutable and pinned by its
// overlays, which makes silently reusing the other extractor's output
// unrecoverable.
func microsandboxBaseDiskIdentity(source, imageID, architecture string, diskSizeGiB int32) (string, error) {
	source = strings.TrimSpace(source)
	imageID = strings.TrimSpace(strings.TrimPrefix(imageID, "sha256:"))
	architecture = strings.TrimSpace(architecture)
	if source == "" || imageID == "" || architecture == "" || diskSizeGiB <= 0 {
		return "", fmt.Errorf("microsandbox base disk identity requires source, image id, architecture, and positive disk size")
	}
	return fmt.Sprintf("base-v%d-%s-%s-%s-%d", microsandboxBaseDiskFormatVersion, sanitizeRuntimeName(source), sanitizeRuntimeName(imageID), sanitizeRuntimeName(architecture), diskSizeGiB), nil
}

func (r *microsandboxRuntime) resolveMicrosandboxBaseDisk(ctx context.Context, imageRef, pullPolicy string, pullTimeout time.Duration) (microsandboxBaseDisk, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return microsandboxBaseDisk{}, fmt.Errorf("microsandbox guest image is required")
	}
	source, err := r.resolveMicrosandboxImageSource(ctx, imageRef, pullPolicy, pullTimeout)
	if err != nil {
		return microsandboxBaseDisk{}, err
	}
	diskSizeGiB := configuredSandboxResources(r.config).DiskSizeGB
	identity, err := microsandboxBaseDiskIdentity(source.Kind, source.ImageID, runtime.GOARCH, diskSizeGiB)
	if err != nil {
		return microsandboxBaseDisk{}, err
	}
	cache, err := imagecache.New(imagecache.Config{
		Root: imageCacheRootForDriver(r.config), DefaultRegistry: r.config.ImageRegistry,
		InsecureRegistries: r.config.ImageInsecureRegistries,
	})
	if err != nil {
		return microsandboxBaseDisk{}, fmt.Errorf("open image cache for microsandbox base disk: %w", err)
	}
	cacheRoot := cache.MaterializationRoot()
	cacheDir := filepath.Join(cacheRoot, source.ImageID, "microsandbox")
	base := microsandboxBaseDisk{
		Identity: identity, Source: source.Kind, ImageID: source.ImageID, ResolvedRef: source.ResolvedRef,
		Path: filepath.Join(cacheDir, identity+".qcow2"), Manifest: filepath.Join(cacheDir, identity+".json"),
		CacheRoot: cacheRoot, Env: source.Env, DiskSizeGiB: diskSizeGiB,
	}

	resultChannel := r.baseBuilds.DoChan(identity+"\x00"+source.ImageID, func() (any, error) {
		return base, r.ensureMicrosandboxBaseDisk(ctx, source, base)
	})
	select {
	case <-ctx.Done():
		return microsandboxBaseDisk{}, ctx.Err()
	case result := <-resultChannel:
		if result.Err != nil {
			return microsandboxBaseDisk{}, result.Err
		}
		return result.Val.(microsandboxBaseDisk), nil
	}
}

func (r *microsandboxRuntime) ensureMicrosandboxBaseDisk(ctx context.Context, source microsandboxImageSource, base microsandboxBaseDisk) error {
	if err := ensureMicrosandboxBaseCacheDirectory(base.CacheRoot, base.Path); err != nil {
		return err
	}
	if valid, err := validateMicrosandboxBaseDisk(ctx, base); err != nil {
		return err
	} else if valid {
		slog.Info("agent-compose microsandbox base disk cache hit", "image", base.ResolvedRef, "image_id", base.ImageID, "cache_identity", base.Identity, "path", base.Path)
		return nil
	}
	started := time.Now()
	// The source owns the layout directory: the docker source hands back a
	// private export it wants removed, the OCI source hands back the shared
	// image cache rootfs it wants left alone. Only release() knows which.
	rootfsDir, release, err := source.materialize(ctx, filepath.Dir(base.Path))
	if err != nil {
		return err
	}
	defer release()
	if err := validateMicrosandboxExportedRootfs(rootfsDir, base.ResolvedRef); err != nil {
		return err
	}
	rawPath, err := buildMicrosandboxRawDisk(ctx, rootfsDir, base)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(rawPath) }()
	qcowPath, err := convertMicrosandboxBaseDisk(ctx, rawPath, base.Path)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(qcowPath) }()
	if err := publishMicrosandboxBaseDisk(ctx, qcowPath, base); err != nil {
		return err
	}
	info, _ := os.Stat(base.Path)
	slog.Info("agent-compose microsandbox built base disk", "image", base.ResolvedRef, "image_id", base.ImageID, "cache_identity", base.Identity, "path", base.Path, "duration", time.Since(started), "size_bytes", fileAllocatedBytes(info))
	return nil
}

func ensureMicrosandboxBaseCacheDirectory(cacheRoot, target string) error {
	root, err := filepath.Abs(cacheRoot)
	if err != nil {
		return err
	}
	parent, err := filepath.Abs(filepath.Dir(target))
	if err != nil {
		return err
	}
	if !microsandboxPathWithinRoot(root, parent) || filepath.Clean(root) == filepath.Clean(parent) {
		return fmt.Errorf("microsandbox base disk path %s escapes image cache root", target)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create microsandbox base disk cache directory: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve microsandbox image cache root: %w", err)
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve microsandbox base disk directory: %w", err)
	}
	if !microsandboxPathWithinRoot(canonicalRoot, canonicalParent) || filepath.Clean(canonicalRoot) == filepath.Clean(canonicalParent) {
		return fmt.Errorf("microsandbox base disk path %s escapes image cache root through a symlink", target)
	}
	return nil
}

func validateMicrosandboxExportedRootfs(root, imageRef string) error {
	for _, name := range []string{"etc", "tmp", "var"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("microsandbox exported image %s is missing required directory /%s", imageRef, name)
		}
	}
	return nil
}

func buildMicrosandboxRawDisk(ctx context.Context, exportDir string, base microsandboxBaseDisk) (string, error) {
	raw, err := os.CreateTemp(filepath.Dir(base.Path), ".base-*.raw")
	if err != nil {
		return "", fmt.Errorf("create microsandbox temporary raw disk: %w", err)
	}
	rawPath := raw.Name()
	if err := raw.Truncate(int64(base.DiskSizeGiB) << 30); err != nil {
		_ = raw.Close()
		_ = os.Remove(rawPath)
		return "", fmt.Errorf("size microsandbox raw disk to %d GiB: %w", base.DiskSizeGiB, err)
	}
	if err := raw.Close(); err != nil {
		_ = os.Remove(rawPath)
		return "", fmt.Errorf("close microsandbox temporary raw disk: %w", err)
	}
	if output, err := exec.CommandContext(ctx, "mkfs.ext4", "-F", "-q", "-d", exportDir, rawPath).CombinedOutput(); err != nil {
		_ = os.Remove(rawPath)
		return "", fmt.Errorf("build microsandbox ext4 base disk for %s (increase SANDBOX_DISK_SIZE_GB if the image does not fit): %w: %s", base.ResolvedRef, err, strings.TrimSpace(string(output)))
	}
	return rawPath, nil
}

func convertMicrosandboxBaseDisk(ctx context.Context, rawPath, targetPath string) (string, error) {
	qcow, err := os.CreateTemp(filepath.Dir(targetPath), ".base-*.qcow2")
	if err != nil {
		return "", fmt.Errorf("create microsandbox temporary qcow2 disk: %w", err)
	}
	qcowPath := qcow.Name()
	if err := qcow.Close(); err != nil {
		_ = os.Remove(qcowPath)
		return "", fmt.Errorf("close microsandbox temporary qcow2 disk: %w", err)
	}
	if output, err := exec.CommandContext(ctx, "qemu-img", "convert", "-f", "raw", "-O", "qcow2", rawPath, qcowPath).CombinedOutput(); err != nil {
		_ = os.Remove(qcowPath)
		return "", fmt.Errorf("convert microsandbox base disk to qcow2: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := os.Chmod(qcowPath, 0o444); err != nil {
		_ = os.Remove(qcowPath)
		return "", fmt.Errorf("make microsandbox base disk read-only: %w", err)
	}
	return qcowPath, nil
}

func publishMicrosandboxBaseDisk(ctx context.Context, qcowPath string, base microsandboxBaseDisk) error {
	manifest := microsandboxBaseDiskManifest{FormatVersion: microsandboxBaseDiskFormatVersion, Identity: base.Identity, Source: base.Source, ImageID: base.ImageID, ResolvedRef: base.ResolvedRef, Architecture: runtime.GOARCH, DiskSizeGiB: base.DiskSizeGiB, Path: base.Path, CreatedAt: time.Now().UTC()}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode microsandbox base disk manifest: %w", err)
	}
	manifestTmp, err := os.CreateTemp(filepath.Dir(base.Manifest), ".base-manifest-*.json")
	if err != nil {
		return fmt.Errorf("create microsandbox base disk manifest: %w", err)
	}
	manifestTmpPath := manifestTmp.Name()
	defer func() { _ = os.Remove(manifestTmpPath) }()
	if err := manifestTmp.Chmod(0o444); err != nil {
		_ = manifestTmp.Close()
		return err
	}
	if _, err := manifestTmp.Write(append(manifestData, '\n')); err != nil {
		_ = manifestTmp.Close()
		return err
	}
	if err := manifestTmp.Sync(); err != nil {
		_ = manifestTmp.Close()
		return err
	}
	if err := manifestTmp.Close(); err != nil {
		return err
	}
	if err := publishFileWithoutOverwrite(qcowPath, base.Path); err != nil {
		if valid, validateErr := validateMicrosandboxBaseDisk(ctx, base); validateErr == nil && valid {
			return nil
		}
		return fmt.Errorf("publish microsandbox base disk: %w", err)
	}
	if err := publishFileWithoutOverwrite(manifestTmpPath, base.Manifest); err != nil {
		_ = os.Remove(base.Path)
		return fmt.Errorf("publish microsandbox base disk manifest: %w", err)
	}
	return nil
}

func exportDockerImageFilesystem(ctx context.Context, dockerClient *client.Client, imageRef, destination string) error {
	containerResp, err := dockerClient.ContainerCreate(ctx, &containerapi.Config{Image: imageRef, Cmd: []string{"true"}}, nil, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create Docker export container from image %s: %w", imageRef, err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = dockerClient.ContainerRemove(cleanupCtx, containerResp.ID, containerapi.RemoveOptions{Force: true})
	}()
	stream, err := dockerClient.ContainerExport(ctx, containerResp.ID)
	if err != nil {
		return fmt.Errorf("export Docker image filesystem %s: %w", imageRef, err)
	}
	defer func() { _ = stream.Close() }()
	if err := extractTarArchive(stream, destination); err != nil {
		return fmt.Errorf("extract Docker image filesystem %s: %w", imageRef, err)
	}
	return nil
}

func validateMicrosandboxBaseDisk(ctx context.Context, base microsandboxBaseDisk) (bool, error) {
	// Incomplete-pair cleanup must run only inside the per-identity baseBuilds
	// singleflight (or after it has completed), never alongside publication.
	diskInfo, diskErr := os.Lstat(base.Path)
	manifestInfo, manifestErr := os.Lstat(base.Manifest)
	if os.IsNotExist(diskErr) && os.IsNotExist(manifestErr) {
		return false, nil
	}
	if diskErr != nil || manifestErr != nil {
		if diskErr == nil && manifestErr != nil && os.IsNotExist(manifestErr) {
			if err := os.Remove(base.Path); err != nil {
				return false, fmt.Errorf("remove incomplete microsandbox base disk: %w", err)
			}
			return false, nil
		}
		if manifestErr == nil && diskErr != nil && os.IsNotExist(diskErr) {
			if err := os.Remove(base.Manifest); err != nil {
				return false, fmt.Errorf("remove incomplete microsandbox base disk manifest: %w", err)
			}
			return false, nil
		}
		return false, fmt.Errorf("microsandbox base disk cache is incomplete: disk=%v manifest=%v", diskErr, manifestErr)
	}
	if diskInfo.Mode()&os.ModeSymlink != 0 || !diskInfo.Mode().IsRegular() {
		return false, fmt.Errorf("microsandbox base disk %s is not a regular file", base.Path)
	}
	if manifestInfo.Mode()&os.ModeSymlink != 0 || !manifestInfo.Mode().IsRegular() {
		return false, fmt.Errorf("microsandbox base disk manifest %s is not a regular file", base.Manifest)
	}
	manifestData, err := os.ReadFile(base.Manifest)
	if err != nil {
		return false, fmt.Errorf("read microsandbox base disk manifest %s: %w", base.Manifest, err)
	}
	var manifest microsandboxBaseDiskManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return false, fmt.Errorf("decode microsandbox base disk manifest %s: %w", base.Manifest, err)
	}
	if manifest.FormatVersion != microsandboxBaseDiskFormatVersion || manifest.Identity != base.Identity || manifest.Source != base.Source || manifest.ImageID != base.ImageID || manifest.Architecture != runtime.GOARCH || manifest.DiskSizeGiB != base.DiskSizeGiB || filepath.Clean(manifest.Path) != filepath.Clean(base.Path) || manifest.CreatedAt.IsZero() {
		return false, fmt.Errorf("microsandbox base disk manifest %s does not match requested cache identity", base.Manifest)
	}
	if diskInfo.Mode().Perm()&0o222 != 0 {
		return false, fmt.Errorf("microsandbox base disk %s is writable", base.Path)
	}
	output, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", base.Path).Output()
	if err != nil {
		return false, fmt.Errorf("inspect microsandbox base disk %s: %w", base.Path, err)
	}
	var imageInfo qemuImageInfo
	if err := json.Unmarshal(output, &imageInfo); err != nil {
		return false, fmt.Errorf("decode microsandbox base disk info %s: %w", base.Path, err)
	}
	if imageInfo.Format != "qcow2" || strings.TrimSpace(imageInfo.BackingFilename) != "" || strings.TrimSpace(imageInfo.FullBackingFilename) != "" {
		return false, fmt.Errorf("microsandbox base disk %s must be a self-contained qcow2 image", base.Path)
	}
	return true, nil
}

func publishFileWithoutOverwrite(source, target string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
}

func fileAllocatedBytes(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Blocks * 512
	}
	return info.Size()
}
