//go:build linux && cgo && (boxlitecgo || microsandboxcgo)

package driver

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	containerapi "github.com/docker/docker/api/types/container"
	typesimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

type localDockerImageRootfs struct {
	ImageID     string
	ResolvedRef string
	RootfsPath  string
	Env         []string
}

type localDockerImageLayout struct {
	ImageID     string
	ResolvedRef string
	RootfsPath  string
	Env         []string
}

func init() {
	_ = localDockerImageLayout{}
	_ = materializeLocalDockerImageLayout
	_ = ociImageConfig{}
	_ = buildOCIConfigPayload
	_ = addOCIBlobFromFile
}

func materializeLocalDockerImageRootfs(ctx context.Context, dataRoot, imageRef string) (localDockerImageRootfs, bool, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return localDockerImageRootfs{}, false, nil
	}
	defer func() { _ = dockerClient.Close() }()
	return materializeLocalDockerImageRootfsWithClient(ctx, dataRoot, dockerClient, imageRef)
}

func materializeLocalDockerImageRootfsWithClient(ctx context.Context, dataRoot string, dockerClient *client.Client, imageRef string) (localDockerImageRootfs, bool, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return localDockerImageRootfs{}, false, nil
	}

	resolvedRef, ok, err := resolveLocalDockerImageRef(ctx, dockerClient, imageRef)
	if err != nil || !ok {
		return localDockerImageRootfs{}, false, nil
	}

	inspect, err := dockerClient.ImageInspect(ctx, resolvedRef)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return localDockerImageRootfs{}, false, nil
		}
		lowered := strings.ToLower(err.Error())
		if strings.Contains(lowered, "no such host") || strings.Contains(lowered, "cannot connect") || strings.Contains(lowered, "docker daemon") {
			return localDockerImageRootfs{}, false, nil
		}
		return localDockerImageRootfs{}, false, nil
	}

	imageEnv := inspectEnv(inspect)

	imageID := strings.TrimPrefix(inspect.ID, "sha256:")
	if imageID == "" {
		imageID = sanitizeRuntimeName(resolvedRef)
	}
	cacheDir := filepath.Join(dataRoot, "image-cache", imageID)
	rootfsDir := filepath.Join(cacheDir, "rootfs")
	readyFlag := filepath.Join(cacheDir, ".rootfs.ready")
	if _, err := os.Stat(readyFlag); err == nil {
		if info, statErr := os.Stat(rootfsDir); statErr == nil && info.IsDir() {
			env := loadImageEnvCache(cacheDir, imageEnv)
			return localDockerImageRootfs{ImageID: imageID, ResolvedRef: resolvedRef, RootfsPath: rootfsDir, Env: env}, true, nil
		}
	}
	_ = os.Remove(readyFlag)

	layout, layoutReady, _ := materializeLocalDockerImageLayoutWithClient(ctx, dataRoot, dockerClient, resolvedRef)
	if layoutReady {
		result, ok, err := materializeRootfsFromReadyLayout(cacheDir, rootfsDir, readyFlag, layout, resolvedRef)
		if err != nil || ok {
			return result, ok, err
		}
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("create image cache dir: %w", err)
	}
	lockPath := filepath.Join(cacheDir, ".lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("open image cache lock: %w", err)
	}
	defer func() { _ = lockFile.Close() }()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("lock image cache dir: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	if _, err := os.Stat(readyFlag); err == nil {
		if info, statErr := os.Stat(rootfsDir); statErr == nil && info.IsDir() {
			env := loadImageEnvCache(cacheDir, imageEnv)
			return localDockerImageRootfs{ImageID: imageID, ResolvedRef: resolvedRef, RootfsPath: rootfsDir, Env: env}, true, nil
		}
	}
	_ = os.Remove(readyFlag)

	containerResp, err := dockerClient.ContainerCreate(ctx, &containerapi.Config{Image: resolvedRef, Cmd: []string{"true"}}, nil, nil, nil, "")
	if err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("create export container from image %s: %w", resolvedRef, err)
	}
	defer func() {
		_ = dockerClient.ContainerRemove(context.Background(), containerResp.ID, containerapi.RemoveOptions{Force: true})
	}()

	exportStream, err := dockerClient.ContainerExport(ctx, containerResp.ID)
	if err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("export container filesystem for image %s: %w", resolvedRef, err)
	}
	defer func() { _ = exportStream.Close() }()

	tmpDir := filepath.Join(cacheDir, "rootfs.tmp")
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("create temp rootfs dir: %w", err)
	}
	rootfsTmpDir := filepath.Join(tmpDir, "layout")
	if err := os.MkdirAll(rootfsTmpDir, 0o755); err != nil {
		_ = os.RemoveAll(tmpDir)
		return localDockerImageRootfs{}, false, fmt.Errorf("create temp exported rootfs dir: %w", err)
	}
	if err := extractTarArchive(exportStream, rootfsTmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return localDockerImageRootfs{}, false, fmt.Errorf("extract exported rootfs for %s: %w", resolvedRef, err)
	}
	_ = os.RemoveAll(rootfsDir)
	if err := os.Rename(rootfsTmpDir, rootfsDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return localDockerImageRootfs{}, false, fmt.Errorf("activate exported rootfs for %s: %w", resolvedRef, err)
	}
	_ = os.RemoveAll(tmpDir)
	if err := os.WriteFile(readyFlag, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("write image cache ready flag: %w", err)
	}
	if err := writeImageEnvCache(cacheDir, imageEnv); err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("write image env cache: %w", err)
	}
	return localDockerImageRootfs{ImageID: imageID, ResolvedRef: resolvedRef, RootfsPath: rootfsDir, Env: imageEnv}, true, nil
}

func materializeRootfsFromReadyLayout(cacheDir, rootfsDir, readyFlag string, layout localDockerImageLayout, resolvedRef string) (localDockerImageRootfs, bool, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("create image cache dir: %w", err)
	}
	lockPath := filepath.Join(cacheDir, ".lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("open image cache lock: %w", err)
	}
	defer func() { _ = lockFile.Close() }()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("lock image cache dir: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	if _, err := os.Stat(readyFlag); err == nil {
		if info, statErr := os.Stat(rootfsDir); statErr == nil && info.IsDir() {
			env := loadImageEnvCache(cacheDir, nil)
			return localDockerImageRootfs{ImageID: layout.ImageID, ResolvedRef: resolvedRef, RootfsPath: rootfsDir, Env: env}, true, nil
		}
	}
	_ = os.Remove(readyFlag)

	tmpDir := filepath.Join(cacheDir, "rootfs.tmp")
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("create temp rootfs dir: %w", err)
	}
	rootfsTmpDir := filepath.Join(tmpDir, "layout")
	if err := os.MkdirAll(rootfsTmpDir, 0o755); err != nil {
		_ = os.RemoveAll(tmpDir)
		return localDockerImageRootfs{}, false, fmt.Errorf("create temp extracted rootfs dir: %w", err)
	}
	if err := extractOCILayoutRootfs(layout.RootfsPath, rootfsTmpDir, resolvedRef); err != nil {
		_ = os.RemoveAll(tmpDir)
		return localDockerImageRootfs{}, false, nil
	}
	_ = os.RemoveAll(rootfsDir)
	if err := os.Rename(rootfsTmpDir, rootfsDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return localDockerImageRootfs{}, false, nil
	}
	_ = os.RemoveAll(tmpDir)
	if err := os.WriteFile(readyFlag, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return localDockerImageRootfs{}, false, fmt.Errorf("write image cache ready flag: %w", err)
	}
	env := loadImageEnvCache(cacheDir, nil)
	return localDockerImageRootfs{ImageID: layout.ImageID, ResolvedRef: resolvedRef, RootfsPath: rootfsDir, Env: env}, true, nil
}

func materializeLocalDockerImageLayout(ctx context.Context, dataRoot, imageRef string) (localDockerImageLayout, bool, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return localDockerImageLayout{}, false, nil
	}
	defer func() { _ = dockerClient.Close() }()
	return materializeLocalDockerImageLayoutWithClient(ctx, dataRoot, dockerClient, imageRef)
}

func materializeLocalDockerImageLayoutWithClient(ctx context.Context, dataRoot string, dockerClient *client.Client, imageRef string) (localDockerImageLayout, bool, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return localDockerImageLayout{}, false, nil
	}

	resolvedRef, ok, err := resolveLocalDockerImageRef(ctx, dockerClient, imageRef)
	if err != nil || !ok {
		return localDockerImageLayout{}, false, nil
	}

	inspect, err := dockerClient.ImageInspect(ctx, resolvedRef)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return localDockerImageLayout{}, false, nil
		}
		lowered := strings.ToLower(err.Error())
		if strings.Contains(lowered, "no such host") || strings.Contains(lowered, "cannot connect") || strings.Contains(lowered, "docker daemon") {
			return localDockerImageLayout{}, false, nil
		}
		return localDockerImageLayout{}, false, nil
	}

	imageEnv := inspectEnv(inspect)

	imageID := strings.TrimPrefix(inspect.ID, "sha256:")
	if imageID == "" {
		imageID = sanitizeRuntimeName(resolvedRef)
	}
	cacheDir := filepath.Join(dataRoot, "image-cache", imageID)
	rootfsDir := filepath.Join(cacheDir, "oci")
	readyFlag := filepath.Join(cacheDir, ".ready")
	if _, err := os.Stat(readyFlag); err == nil && isValidOCILayout(rootfsDir) {
		env := loadImageEnvCache(cacheDir, imageEnv)
		return localDockerImageLayout{ImageID: imageID, ResolvedRef: resolvedRef, RootfsPath: rootfsDir, Env: env}, true, nil
	}
	_ = os.Remove(readyFlag)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return localDockerImageLayout{}, false, fmt.Errorf("create image cache dir: %w", err)
	}
	lockPath := filepath.Join(cacheDir, ".lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return localDockerImageLayout{}, false, fmt.Errorf("open image cache lock: %w", err)
	}
	defer func() { _ = lockFile.Close() }()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return localDockerImageLayout{}, false, fmt.Errorf("lock image cache dir: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	if _, err := os.Stat(readyFlag); err == nil && isValidOCILayout(rootfsDir) {
		env := loadImageEnvCache(cacheDir, imageEnv)
		return localDockerImageLayout{ImageID: imageID, ResolvedRef: resolvedRef, RootfsPath: rootfsDir, Env: env}, true, nil
	}
	_ = os.Remove(readyFlag)

	tmpDir := filepath.Join(cacheDir, "oci.tmp")
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return localDockerImageLayout{}, false, fmt.Errorf("create temp rootfs dir: %w", err)
	}
	ociDir := filepath.Join(tmpDir, "layout")
	if err := buildOCILayoutFromLocalImage(ctx, dockerClient, inspect, ociDir, resolvedRef); err != nil {
		_ = os.RemoveAll(tmpDir)
		return localDockerImageLayout{}, false, fmt.Errorf("build oci layout for %s: %w", resolvedRef, err)
	}
	_ = os.RemoveAll(rootfsDir)
	if err := os.Rename(ociDir, rootfsDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return localDockerImageLayout{}, false, fmt.Errorf("activate oci layout for %s: %w", resolvedRef, err)
	}
	_ = os.RemoveAll(tmpDir)
	if err := os.WriteFile(readyFlag, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return localDockerImageLayout{}, false, fmt.Errorf("write image cache ready flag: %w", err)
	}
	if err := writeImageEnvCache(cacheDir, imageEnv); err != nil {
		return localDockerImageLayout{}, false, fmt.Errorf("write image env cache: %w", err)
	}
	return localDockerImageLayout{ImageID: imageID, ResolvedRef: resolvedRef, RootfsPath: rootfsDir, Env: imageEnv}, true, nil
}

func isValidOCILayout(rootfsDir string) bool {
	for _, name := range []string{"oci-layout", "index.json"} {
		if _, err := os.Stat(filepath.Join(rootfsDir, name)); err != nil {
			return false
		}
	}
	manifest, err := loadOCILayoutManifest(rootfsDir, "")
	if err != nil {
		return false
	}
	if strings.TrimSpace(manifest.Config.Digest) != "" {
		if _, err := os.Stat(ociLayoutBlobPath(rootfsDir, manifest.Config.Digest)); err != nil {
			return false
		}
	}
	for _, layer := range manifest.Layers {
		if _, err := os.Stat(ociLayoutBlobPath(rootfsDir, layer.Digest)); err != nil {
			return false
		}
	}
	return true
}

func extractOCILayoutRootfs(layoutDir, dstDir, imageRef string) error {
	manifest, err := loadOCILayoutManifest(layoutDir, imageRef)
	if err != nil {
		return err
	}
	for _, layer := range manifest.Layers {
		reader, err := openOCILayoutBlobTarReader(layoutDir, layer.Digest)
		if err != nil {
			return fmt.Errorf("open layer %s: %w", layer.Digest, err)
		}
		if err := applyLayerTarArchive(reader, dstDir); err != nil {
			_ = reader.Close()
			return fmt.Errorf("apply layer %s: %w", layer.Digest, err)
		}
		if err := reader.Close(); err != nil {
			return fmt.Errorf("close layer %s: %w", layer.Digest, err)
		}
	}
	return nil
}

func loadOCILayoutManifest(layoutDir, imageRef string) (ociManifest, error) {
	indexPath := filepath.Join(layoutDir, "index.json")
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		return ociManifest{}, fmt.Errorf("read oci index %s: %w", indexPath, err)
	}
	var index ociIndex
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		return ociManifest{}, fmt.Errorf("decode oci index %s: %w", indexPath, err)
	}
	if len(index.Manifests) == 0 {
		return ociManifest{}, fmt.Errorf("oci index %s has no manifests", indexPath)
	}

	selected := index.Manifests[0]
	trimmedRef := strings.TrimSpace(imageRef)
	if trimmedRef != "" {
		for _, manifest := range index.Manifests {
			if strings.TrimSpace(manifest.Annotations["org.opencontainers.image.ref.name"]) == trimmedRef {
				selected = manifest
				break
			}
		}
	}

	manifestBytes, err := os.ReadFile(ociLayoutBlobPath(layoutDir, selected.Digest))
	if err != nil {
		return ociManifest{}, fmt.Errorf("read oci manifest %s: %w", selected.Digest, err)
	}
	var manifest ociManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return ociManifest{}, fmt.Errorf("decode oci manifest %s: %w", selected.Digest, err)
	}
	return manifest, nil
}

func openOCILayoutBlobTarReader(layoutDir, digest string) (io.ReadCloser, error) {
	blobPath := ociLayoutBlobPath(layoutDir, digest)
	file, err := os.Open(blobPath)
	if err != nil {
		return nil, err
	}

	buffered := bufio.NewReader(file)
	magic, err := buffered.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = file.Close()
		return nil, err
	}
	if len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gzipReader, err := gzip.NewReader(buffered)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		return &compoundReadCloser{Reader: gzipReader, closers: []io.Closer{gzipReader, file}}, nil
	}
	return &compoundReadCloser{Reader: buffered, closers: []io.Closer{file}}, nil
}

func ociLayoutBlobPath(layoutDir, digest string) string {
	parts := strings.SplitN(strings.TrimSpace(digest), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return filepath.Join(layoutDir, "blobs", "invalid", sanitizeRuntimeName(digest))
	}
	return filepath.Join(layoutDir, "blobs", parts[0], parts[1])
}

type compoundReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (r *compoundReadCloser) Close() error {
	var firstErr error
	for _, closer := range r.closers {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func applyLayerTarArchive(src io.Reader, dstDir string) error {
	reader := tar.NewReader(src)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if header == nil {
			continue
		}

		relPath := filepath.Clean(strings.TrimPrefix(header.Name, "./"))
		if relPath == "." || relPath == "" {
			continue
		}
		if filepath.IsAbs(relPath) {
			return fmt.Errorf("tar entry %q must be relative", header.Name)
		}
		if err := ensureTarRelativePath(relPath); err != nil {
			return fmt.Errorf("invalid tar entry %q: %w", header.Name, err)
		}

		base := filepath.Base(relPath)
		dir := filepath.Dir(relPath)
		if base == ".wh..wh..opq" {
			if dir == "." {
				dir = ""
			}
			if err := clearDirectory(filepath.Join(dstDir, dir)); err != nil {
				return fmt.Errorf("apply opaque whiteout for %s: %w", header.Name, err)
			}
			continue
		}
		if strings.HasPrefix(base, ".wh.") {
			whiteoutTarget := strings.TrimPrefix(base, ".wh.")
			if whiteoutTarget == "" {
				continue
			}
			targetPath := filepath.Join(dstDir, dir, whiteoutTarget)
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("apply whiteout for %s: %w", header.Name, err)
			}
			continue
		}

		targetPath := filepath.Join(dstDir, relPath)
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("prepare parent dir for %s: %w", relPath, err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, header.FileInfo().Mode().Perm()); err != nil {
				return fmt.Errorf("create dir %s: %w", relPath, err)
			}
		case tar.TypeReg:
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove existing file %s: %w", relPath, err)
			}
			file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, header.FileInfo().Mode().Perm())
			if err != nil {
				return fmt.Errorf("create file %s: %w", relPath, err)
			}
			if _, err := io.Copy(file, reader); err != nil {
				_ = file.Close()
				return fmt.Errorf("write file %s: %w", relPath, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("close file %s: %w", relPath, err)
			}
		case tar.TypeSymlink:
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove existing path %s: %w", relPath, err)
			}
			if err := os.Symlink(header.Linkname, targetPath); err != nil {
				return fmt.Errorf("create symlink %s -> %s: %w", relPath, header.Linkname, err)
			}
		case tar.TypeLink:
			linkTarget := filepath.Clean(strings.TrimPrefix(header.Linkname, "./"))
			if err := ensureTarRelativePath(linkTarget); err != nil {
				return fmt.Errorf("invalid hardlink target for %s: %w", relPath, err)
			}
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove existing hardlink path %s: %w", relPath, err)
			}
			if err := os.Link(filepath.Join(dstDir, linkTarget), targetPath); err != nil {
				return fmt.Errorf("create hardlink %s -> %s: %w", relPath, linkTarget, err)
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader, tar.TypeGNULongLink, tar.TypeGNULongName:
			continue
		default:
			return fmt.Errorf("unsupported tar entry type %q for %s", string(header.Typeflag), relPath)
		}
	}
}

func clearDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(dir, 0o755)
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

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Platform    *ociPlatform      `json:"platform,omitempty"`
}

type ociPlatform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
	OSVersion    string `json:"os.version,omitempty"`
}

type ociManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        ociDescriptor   `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}

type ociIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Manifests     []ociDescriptor `json:"manifests"`
}

type ociImageConfig struct {
	Architecture string                 `json:"architecture"`
	OS           string                 `json:"os"`
	RootFS       map[string]interface{} `json:"rootfs"`
	Config       map[string]interface{} `json:"config"`
}

func buildOCILayoutFromLocalImage(ctx context.Context, dockerClient *client.Client, inspect typesimage.InspectResponse, dstDir, imageRef string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("create oci layout dir: %w", err)
	}

	saveStream, err := dockerClient.ImageSave(ctx, []string{imageRef})
	if err != nil {
		return fmt.Errorf("save docker image %s: %w", imageRef, err)
	}
	defer func() { _ = saveStream.Close() }()

	if err := extractTarArchive(saveStream, dstDir); err != nil {
		return fmt.Errorf("extract saved docker image %s: %w", imageRef, err)
	}
	if err := normalizeSavedOCILayout(dstDir, inspect, imageRef); err != nil {
		return fmt.Errorf("normalize saved oci layout for %s: %w", imageRef, err)
	}
	return nil
}

func extractTarArchive(src io.Reader, dstDir string) error {
	reader := tar.NewReader(src)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if header == nil {
			continue
		}

		relPath := filepath.Clean(strings.TrimPrefix(header.Name, "./"))
		if relPath == "." || relPath == "" {
			continue
		}
		if filepath.IsAbs(relPath) {
			return fmt.Errorf("tar entry %q must be relative", header.Name)
		}
		if err := ensureTarRelativePath(relPath); err != nil {
			return fmt.Errorf("invalid tar entry %q: %w", header.Name, err)
		}

		targetPath := filepath.Join(dstDir, relPath)
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("prepare parent dir for %s: %w", relPath, err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, header.FileInfo().Mode().Perm()); err != nil {
				return fmt.Errorf("create dir %s: %w", relPath, err)
			}
		case tar.TypeReg:
			file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, header.FileInfo().Mode().Perm())
			if err != nil {
				return fmt.Errorf("create file %s: %w", relPath, err)
			}
			if _, err := io.Copy(file, reader); err != nil {
				_ = file.Close()
				return fmt.Errorf("write file %s: %w", relPath, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("close file %s: %w", relPath, err)
			}
		case tar.TypeSymlink:
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove existing path %s: %w", relPath, err)
			}
			if err := os.Symlink(header.Linkname, targetPath); err != nil {
				return fmt.Errorf("create symlink %s -> %s: %w", relPath, header.Linkname, err)
			}
		case tar.TypeLink:
			linkTarget := filepath.Clean(strings.TrimPrefix(header.Linkname, "./"))
			if err := ensureTarRelativePath(linkTarget); err != nil {
				return fmt.Errorf("invalid hardlink target for %s: %w", relPath, err)
			}
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove existing hardlink path %s: %w", relPath, err)
			}
			if err := os.Link(filepath.Join(dstDir, linkTarget), targetPath); err != nil {
				return fmt.Errorf("create hardlink %s -> %s: %w", relPath, linkTarget, err)
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader, tar.TypeGNULongLink, tar.TypeGNULongName:
			continue
		default:
			return fmt.Errorf("unsupported tar entry type %q for %s", string(header.Typeflag), relPath)
		}
	}
}

func ensureTarRelativePath(relPath string) error {
	if relPath == "." || relPath == "" {
		return nil
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("path escapes destination")
	}
	return nil
}

func normalizeSavedOCILayout(layoutDir string, inspect typesimage.InspectResponse, imageRef string) error {
	if !isValidOCILayout(layoutDir) {
		return fmt.Errorf("saved docker image does not contain an OCI layout at %s", layoutDir)
	}

	indexPath := filepath.Join(layoutDir, "index.json")
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("read oci index %s: %w", indexPath, err)
	}
	var index ociIndex
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		return fmt.Errorf("decode oci index %s: %w", indexPath, err)
	}
	if len(index.Manifests) == 0 {
		return fmt.Errorf("oci index %s has no manifests", indexPath)
	}

	platform := buildOCIPlatform(inspect)
	changed := false
	for i := range index.Manifests {
		merged, updated := mergeOCIPlatform(index.Manifests[i].Platform, platform)
		if updated {
			index.Manifests[i].Platform = merged
			changed = true
		}
		if strings.TrimSpace(imageRef) == "" {
			continue
		}
		if index.Manifests[i].Annotations == nil {
			index.Manifests[i].Annotations = map[string]string{}
		}
		if _, ok := index.Manifests[i].Annotations["org.opencontainers.image.ref.name"]; !ok {
			index.Manifests[i].Annotations["org.opencontainers.image.ref.name"] = imageRef
			changed = true
		}
	}
	if !changed {
		return nil
	}

	normalizedIndexBytes, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("encode oci index %s: %w", indexPath, err)
	}
	if err := os.WriteFile(indexPath, normalizedIndexBytes, 0o644); err != nil {
		return fmt.Errorf("write oci index %s: %w", indexPath, err)
	}
	return nil
}

func mergeOCIPlatform(current, fallback *ociPlatform) (*ociPlatform, bool) {
	if fallback == nil {
		return current, false
	}
	if current == nil {
		return cloneOCIPlatform(fallback), true
	}
	merged := cloneOCIPlatform(current)
	changed := false
	if strings.TrimSpace(merged.Architecture) == "" && strings.TrimSpace(fallback.Architecture) != "" {
		merged.Architecture = fallback.Architecture
		changed = true
	}
	if strings.TrimSpace(merged.OS) == "" && strings.TrimSpace(fallback.OS) != "" {
		merged.OS = fallback.OS
		changed = true
	}
	if strings.TrimSpace(merged.Variant) == "" && strings.TrimSpace(fallback.Variant) != "" {
		merged.Variant = fallback.Variant
		changed = true
	}
	if strings.TrimSpace(merged.OSVersion) == "" && strings.TrimSpace(fallback.OSVersion) != "" {
		merged.OSVersion = fallback.OSVersion
		changed = true
	}
	return merged, changed
}

func cloneOCIPlatform(platform *ociPlatform) *ociPlatform {
	if platform == nil {
		return nil
	}
	copy := *platform
	return &copy
}

func buildOCIPlatform(inspect typesimage.InspectResponse) *ociPlatform {
	platform := &ociPlatform{
		Architecture: firstNonEmptyOCI(strings.TrimSpace(inspect.Architecture), "amd64"),
		OS:           firstNonEmptyOCI(strings.TrimSpace(inspect.Os), "linux"),
	}
	if variant := strings.TrimSpace(inspect.Variant); variant != "" {
		platform.Variant = variant
	}
	if osVersion := strings.TrimSpace(inspect.OsVersion); osVersion != "" {
		platform.OSVersion = osVersion
	}
	return platform
}

func buildOCIConfigPayload(inspect typesimage.InspectResponse, diffIDs []string) ([]byte, error) {
	configMap := map[string]interface{}{}
	if inspect.Config != nil {
		if len(inspect.Config.Env) > 0 {
			configMap["Env"] = inspect.Config.Env
		}
		if len(inspect.Config.Cmd) > 0 {
			configMap["Cmd"] = inspect.Config.Cmd
		}
		if len(inspect.Config.Entrypoint) > 0 {
			configMap["Entrypoint"] = inspect.Config.Entrypoint
		}
		if inspect.Config.WorkingDir != "" {
			configMap["WorkingDir"] = inspect.Config.WorkingDir
		}
		if inspect.Config.User != "" {
			configMap["User"] = inspect.Config.User
		}
		if len(inspect.Config.ExposedPorts) > 0 {
			exposed := make(map[string]map[string]string, len(inspect.Config.ExposedPorts))
			for port := range inspect.Config.ExposedPorts {
				exposed[string(port)] = map[string]string{}
			}
			configMap["ExposedPorts"] = exposed
		}
	}
	if len(configMap) == 0 {
		configMap["Env"] = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	}
	resolvedDiffIDs := append([]string(nil), diffIDs...)
	if len(resolvedDiffIDs) == 0 {
		resolvedDiffIDs = []string{}
	}
	payload := ociImageConfig{
		Architecture: firstNonEmptyOCI(inspect.Architecture, "amd64"),
		OS:           firstNonEmptyOCI(inspect.Os, "linux"),
		RootFS: map[string]interface{}{
			"type":     "layers",
			"diff_ids": resolvedDiffIDs,
		},
		Config: configMap,
	}
	return json.Marshal(payload)
}

func addOCIBlobFromFile(srcPath, blobsDir, mediaType string) (ociDescriptor, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return ociDescriptor{}, err
	}
	return addOCIBlobFromBytes(data, blobsDir, mediaType)
}

func addOCIBlobFromBytes(data []byte, blobsDir, mediaType string) (ociDescriptor, error) {
	digest := sha256.Sum256(data)
	hexDigest := fmt.Sprintf("%x", digest[:])
	blobPath := filepath.Join(blobsDir, hexDigest)
	if err := os.WriteFile(blobPath, data, 0o644); err != nil {
		return ociDescriptor{}, err
	}
	return ociDescriptor{MediaType: mediaType, Digest: "sha256:" + hexDigest, Size: int64(len(data))}, nil
}

func firstNonEmptyOCI(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sanitizeRuntimeName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "image"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "image"
	}
	return result
}

// imageEnvCacheFileName is the basename of the file that stores the image's ENV
// list alongside the rootfs cache.
const imageEnvCacheFileName = ".env.json"

func inspectEnv(inspect typesimage.InspectResponse) []string {
	if inspect.Config == nil || len(inspect.Config.Env) == 0 {
		return nil
	}
	env := make([]string, len(inspect.Config.Env))
	copy(env, inspect.Config.Env)
	return env
}

// writeImageEnvCache writes the image environment variables to cacheDir.
func writeImageEnvCache(cacheDir string, env []string) error {
	if len(env) == 0 {
		return nil
	}
	path := filepath.Join(cacheDir, imageEnvCacheFileName)
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("encode env cache: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// loadImageEnvCache reads the cached image environment variables from cacheDir.
// When fresh is non-nil and the cache file does not exist, it writes fresh first.
func loadImageEnvCache(cacheDir string, fresh []string) []string {
	path := filepath.Join(cacheDir, imageEnvCacheFileName)
	if fresh != nil {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_ = writeImageEnvCache(cacheDir, fresh)
			return fresh
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("agent-compose failed to read cached image env; image ENV will not be passed to guest", "cache_dir", cacheDir, "error", err)
		return nil
	}
	var env []string
	if err := json.Unmarshal(data, &env); err != nil {
		slog.Warn("agent-compose failed to decode cached image env; image ENV will not be passed to guest", "cache_dir", cacheDir, "error", err)
		return nil
	}
	return env
}
