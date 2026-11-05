//go:build linux && cgo && boxlitecgo

package driver

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

// boxliteImageDiskReserveBytes mirrors boxlite's
// IMAGE_DISK_GUEST_BINARY_HEADROOM_BYTES. It is part of boxlite's disk cache
// key, so a warmup must use the same value or boxlite will not find the disk.
const boxliteImageDiskReserveBytes int64 = 512 * 1024 * 1024

// WarmBoxliteImageDiskCache pre-builds the ext4 image disk boxlite would
// otherwise build while extracting the OCI layout.
//
// boxlite v0.10.x extracts OCI layers through pathrs. On kernels without
// faccessat2(2) (Linux < 5.8, for example Kylin V10's 4.19 kernel) pathrs
// requests AT_SYMLINK_NOFOLLOW, gets ENOSYS from its fallback, and then
// recursively formats the failing file descriptor while building the error,
// ending in a stack overflow. The same layers can be extracted with the Go
// standard library, which only needs ordinary file syscalls, so warming the
// cache before boxlite starts keeps boxlite from entering that path.
//
// The cache layout and key are boxlite's:
//
//	<BOXLITE_HOME>/images/disk-images/sha256-<digest>-r<reserve>.ext4
//
// A no-op when the disk already exists.
func WarmBoxliteImageDiskCache(config *appconfig.Config, ociLayout string) error {
	ociLayout = strings.TrimSpace(ociLayout)
	if ociLayout == "" {
		return nil
	}
	if faccessat2Supported() {
		// Kernels with faccessat2(2) let pathrs use its normal safe resolver;
		// the warmup is only a compatibility path for older kernels.
		return nil
	}
	info, err := os.Stat(ociLayout)
	if err != nil {
		return nil // let boxlite report the missing path
	}
	if !info.IsDir() {
		return nil
	}
	boxliteHome := config.BoxliteHome
	if boxliteHome == "" {
		boxliteHome = filepath.Join(config.DataRoot, "boxlite")
	}
	cacheDir := filepath.Join(boxliteHome, "images", "disk-images")

	layers, err := ociLayerDigests(ociLayout)
	if err != nil {
		// The layout is boxlite's input; if it is not a readable OCI layout,
		// boxlite will produce the authoritative error.
		slog.Debug("boxlite image disk warmup skipped", "layout", ociLayout, "error", err)
		return nil
	}
	digest := ociImageDigest(layers)
	cachePath := filepath.Join(cacheDir, strings.Replace(digest, ":", "-", 1)+"-r"+strconv.FormatInt(boxliteImageDiskReserveBytes, 10)+".ext4")
	if _, err := os.Stat(cachePath); err == nil {
		return nil
	}

	tmpBase := filepath.Join(boxliteHome, "tmp")
	if err := os.MkdirAll(tmpBase, 0o755); err != nil {
		return fmt.Errorf("boxlite image disk warmup: create temp base: %w", err)
	}
	tmpDir, err := os.MkdirTemp(tmpBase, "image-warmup-")
	if err != nil {
		return fmt.Errorf("boxlite image disk warmup: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	merged := filepath.Join(tmpDir, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		return fmt.Errorf("boxlite image disk warmup: create merged dir: %w", err)
	}

	for _, layer := range layers {
		blob := filepath.Join(ociLayout, "blobs", "sha256", layer)
		if err := extractOCILayer(merged, blob); err != nil {
			return fmt.Errorf("boxlite image disk warmup: extract layer %s: %w", layer, err)
		}
	}

	sizeBytes, err := mergeTreeSize(merged)
	if err != nil {
		return fmt.Errorf("boxlite image disk warmup: measure merged rootfs: %w", err)
	}
	// Leave the same headroom boxlite reserves for the guest binary, with
	// room for ext4 metadata and journal.
	sizeBytes += boxliteImageDiskReserveBytes
	sizeBytes += sizeBytes / 10
	if sizeBytes < 1024*1024*1024 {
		sizeBytes = 1024 * 1024 * 1024
	}
	blocks := (sizeBytes + 4095) / 4096

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("boxlite image disk warmup: create cache dir: %w", err)
	}
	staged := filepath.Join(tmpDir, "image.ext4")
	cmd := exec.Command("mke2fs", "-t", "ext4", "-F", "-b", "4096", "-d", merged, staged, strconv.FormatInt(blocks, 10))
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("boxlite image disk warmup: mke2fs: %w: %s", err, strings.TrimSpace(output.String()))
	}
	if err := os.Rename(staged, cachePath); err != nil {
		return fmt.Errorf("boxlite image disk warmup: install %s: %w", cachePath, err)
	}
	slog.Info("agent-compose boxlite image disk warmup installed",
		"layout", ociLayout,
		"digest", digest,
		"path", cachePath,
		"rootfs_bytes", sizeBytes,
	)
	return nil
}

// ociLayerDigests reads index.json -> manifest -> layer digests.
func ociLayerDigests(ociLayout string) ([]string, error) {
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := readJSONFile(filepath.Join(ociLayout, "index.json"), &index); err != nil {
		return nil, err
	}
	if len(index.Manifests) == 0 {
		return nil, fmt.Errorf("index.json has no manifests")
	}
	manifestDigest := strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")
	if manifestDigest == "" || manifestDigest == index.Manifests[0].Digest {
		return nil, fmt.Errorf("index.json manifest digest is not sha256")
	}
	var manifest struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := readJSONFile(filepath.Join(ociLayout, "blobs", "sha256", manifestDigest), &manifest); err != nil {
		return nil, err
	}
	layers := make([]string, 0, len(manifest.Layers))
	for _, layer := range manifest.Layers {
		digest := strings.TrimPrefix(layer.Digest, "sha256:")
		if digest == "" || digest == layer.Digest {
			return nil, fmt.Errorf("layer digest is not sha256: %q", layer.Digest)
		}
		layers = append(layers, digest)
	}
	return layers, nil
}

func readJSONFile(file string, value any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func ociImageDigest(layers []string) string {
	hasher := sha256.New()
	for _, layer := range layers {
		hasher.Write([]byte("sha256:" + layer))
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func extractOCILayer(root, blob string) error {
	file, err := os.Open(blob)
	if err != nil {
		return err
	}
	defer file.Close()

	var reader io.Reader = file
	header := make([]byte, 2)
	if n, _ := io.ReadFull(file, header); n == 2 && header[0] == 0x1f && header[1] == 0x8b {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		gz, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer gz.Close()
		reader = gz
	} else if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	tarReader := tar.NewReader(reader)
	for {
		entry, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(entry.Name, "./"))
		if name == "." || name == "" {
			continue
		}
		base := path.Base(name)
		if base == ".wh..wh..opq" {
			dir := filepath.Join(root, filepath.FromSlash(path.Dir(name)))
			if err := clearWarmupDirectory(dir); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(base, ".wh.") {
			target := filepath.Join(root, filepath.FromSlash(path.Join(path.Dir(name), strings.TrimPrefix(base, ".wh."))))
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			continue
		}
		target, err := safeJoin(root, name)
		if err != nil {
			return err
		}
		switch entry.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(entry.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(entry.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tarReader)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.RemoveAll(target)
			if err := os.Symlink(entry.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			source, err := safeJoin(root, strings.TrimPrefix(entry.Linkname, "./"))
			if err != nil {
				return err
			}
			_ = os.RemoveAll(target)
			if err := os.Link(source, target); err != nil {
				return err
			}
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			// Device nodes and FIFOs are not part of the image rootfs
			// contract; the guest runtime creates them as needed.
			continue
		default:
			continue
		}
		_ = os.Lchown(target, entry.Uid, entry.Gid)
		if entry.Typeflag != tar.TypeSymlink {
			_ = os.Chmod(target, os.FileMode(entry.Mode))
		}
	}
}

func safeJoin(root, name string) (string, error) {
	cleanRoot := filepath.Clean(root)
	target := filepath.Join(cleanRoot, filepath.FromSlash(strings.TrimPrefix(name, "/")))
	if target != cleanRoot && !strings.HasPrefix(target, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("layer entry escapes rootfs: %s", name)
	}
	return target, nil
}

func clearWarmupDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func mergeTreeSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// faccessat2Supported reports whether the kernel can service the syscall
// pathrs uses for AT_SYMLINK_NOFOLLOW checks. Linux < 5.8 has no
// faccessat2(2); rustix then returns ENOSYS for a flags-carrying accessat,
// and pathrs 0.2.x recurses while formatting that error.
//
// faccessat2 is syscall 439 on x86_64 and aarch64, the only architectures
// boxlite supports. EPERM is treated as unsupported so a seccomp filter that
// blocks the syscall still takes the warmup path instead of crashing.
func faccessat2Supported() bool {
	dirfd, err := syscall.Open("/proc", syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return true
	}
	defer syscall.Close(dirfd)
	path, err := syscall.BytePtrFromString("thread-self")
	if err != nil {
		return true
	}
	const (
		sysFaccessat2     = 439
		atSymlinkNofollow = 0x100
	)
	_, _, errno := syscall.Syscall6(
		sysFaccessat2,
		uintptr(dirfd),
		uintptr(unsafe.Pointer(path)),
		0, // F_OK
		atSymlinkNofollow,
		0,
		0,
	)
	return errno != syscall.ENOSYS && errno != syscall.EPERM
}
