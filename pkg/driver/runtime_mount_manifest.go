package driver

import (
	appconfig "agent-compose/pkg/config"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	defaultassets "agent-compose/assets"
)

const runtimeMountManifestVersion = 1
const directoryOnlyGuestSessionPath = "/data"

const DirectoryOnlyGuestSessionPath = directoryOnlyGuestSessionPath

type RuntimeMountManifest struct {
	Version int            `json:"version"`
	Driver  string         `json:"driver"`
	Mounts  []RuntimeMount `json:"mounts"`
}

type RuntimeMount struct {
	HostPath  string `json:"hostPath"`
	GuestPath string `json:"guestPath"`
	Type      string `json:"type"`
	ReadOnly  bool   `json:"readOnly"`
}

type runtimeMountSpec struct {
	hostPath  string
	guestPath string
	isFile    bool
	readOnly  bool
}

type directoryOnlyExposure string

const (
	directoryOnlyExposureNone          directoryOnlyExposure = ""
	directoryOnlyExposureSymlink       directoryOnlyExposure = "symlink"
	directoryOnlyExposureAlreadyInData directoryOnlyExposure = "already-in-data"
)

type logicalRuntimeMountEntry struct {
	sessionPath           string
	guestPath             string
	isFile                bool
	directoryOnlyExposure directoryOnlyExposure
}

func runtimeMountManifestPath(session *Session) string {
	return filepath.Join(hostSessionDir(session), "vm", "mount-manifest.json")
}

func prepareRuntimeMountManifest(config *appconfig.Config, session *Session, driver string) (RuntimeMountManifest, error) {
	appconfig.ApplyDefaultGuestPaths(config)
	driver = resolveRuntimeDriver(driver)
	if err := validateRuntimeDriver(driver); err != nil {
		return RuntimeMountManifest{}, err
	}
	if err := initializeSessionHomeDefaults(session); err != nil {
		return RuntimeMountManifest{}, err
	}
	if err := initializeSessionRuntimeMountDirs(config, session); err != nil {
		return RuntimeMountManifest{}, err
	}
	manifest, err := buildRuntimeMountManifest(config, session, driver)
	if err != nil {
		return RuntimeMountManifest{}, err
	}
	path := runtimeMountManifestPath(session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return RuntimeMountManifest{}, fmt.Errorf("create runtime mount manifest dir: %w", err)
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return RuntimeMountManifest{}, fmt.Errorf("marshal runtime mount manifest: %w", err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return RuntimeMountManifest{}, fmt.Errorf("write runtime mount manifest: %w", err)
	}
	return manifest, nil
}

func PrepareRuntimeMountManifest(config *appconfig.Config, session *Session, driver string) (RuntimeMountManifest, error) {
	return prepareRuntimeMountManifest(config, session, driver)
}

func loadRuntimeMountManifest(session *Session, expectedDriver string) (RuntimeMountManifest, error) {
	path := runtimeMountManifestPath(session)
	data, err := os.ReadFile(path)
	if err != nil {
		return RuntimeMountManifest{}, fmt.Errorf("read runtime mount manifest: %w", err)
	}
	var manifest RuntimeMountManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return RuntimeMountManifest{}, fmt.Errorf("decode runtime mount manifest: %w", err)
	}
	if manifest.Version != runtimeMountManifestVersion {
		return RuntimeMountManifest{}, fmt.Errorf("unsupported runtime mount manifest version %d", manifest.Version)
	}
	manifest.Driver = resolveRuntimeDriver(manifest.Driver)
	if err := validateRuntimeDriver(manifest.Driver); err != nil {
		return RuntimeMountManifest{}, fmt.Errorf("invalid runtime mount manifest driver: %w", err)
	}
	if strings.TrimSpace(expectedDriver) != "" {
		expectedDriver = resolveRuntimeDriver(expectedDriver)
		if err := validateRuntimeDriver(expectedDriver); err != nil {
			return RuntimeMountManifest{}, err
		}
		if manifest.Driver != expectedDriver {
			return RuntimeMountManifest{}, fmt.Errorf("runtime mount manifest driver %q does not match expected driver %q", manifest.Driver, expectedDriver)
		}
	}
	for _, mount := range manifest.Mounts {
		if mount.Type != "bind" {
			return RuntimeMountManifest{}, fmt.Errorf("unsupported runtime mount type %q", mount.Type)
		}
		if mount.HostPath == "" || !filepath.IsAbs(mount.HostPath) {
			return RuntimeMountManifest{}, fmt.Errorf("runtime mount host path must be absolute: %q", mount.HostPath)
		}
		if mount.GuestPath == "" || !filepath.IsAbs(mount.GuestPath) {
			return RuntimeMountManifest{}, fmt.Errorf("runtime mount guest path must be absolute: %q", mount.GuestPath)
		}
	}
	return manifest, nil
}

func LoadRuntimeMountManifest(session *Session, expectedDriver string) (RuntimeMountManifest, error) {
	return loadRuntimeMountManifest(session, expectedDriver)
}

func loadDirectoryRuntimeMountManifest(session *Session, expectedDriver string) (RuntimeMountManifest, error) {
	manifest, err := loadRuntimeMountManifest(session, expectedDriver)
	if err != nil {
		return RuntimeMountManifest{}, err
	}
	for _, mount := range manifest.Mounts {
		info, err := os.Stat(mount.HostPath)
		if err != nil {
			return RuntimeMountManifest{}, fmt.Errorf("stat runtime mount directory source %s: %w", mount.HostPath, err)
		}
		if !info.IsDir() {
			return RuntimeMountManifest{}, fmt.Errorf("runtime mount directory source is a file: %s", mount.HostPath)
		}
	}
	return manifest, nil
}

func LoadDirectoryRuntimeMountManifest(session *Session, expectedDriver string) (RuntimeMountManifest, error) {
	return loadDirectoryRuntimeMountManifest(session, expectedDriver)
}

func buildRuntimeMountManifest(config *appconfig.Config, session *Session, driver string) (RuntimeMountManifest, error) {
	appconfig.ApplyDefaultGuestPaths(config)
	driver = resolveRuntimeDriver(driver)
	if err := validateRuntimeDriver(driver); err != nil {
		return RuntimeMountManifest{}, err
	}
	if driver == RuntimeDriverBoxlite {
		if err := prepareBoxliteVolumeSymlinkBridge(session); err != nil {
			return RuntimeMountManifest{}, err
		}
	}
	specs := runtimeMountSpecsForDriver(config, session, driver)
	mounts := make([]RuntimeMount, 0, len(specs))
	for _, spec := range specs {
		if err := ensureRuntimeMountSource(spec); err != nil {
			return RuntimeMountManifest{}, err
		}
		hostPath, err := filepath.Abs(spec.hostPath)
		if err != nil {
			return RuntimeMountManifest{}, fmt.Errorf("resolve runtime mount host path %s: %w", spec.hostPath, err)
		}
		mounts = append(mounts, RuntimeMount{
			HostPath:  hostPath,
			GuestPath: filepath.Clean(spec.guestPath),
			Type:      "bind",
			ReadOnly:  spec.readOnly,
		})
	}
	return RuntimeMountManifest{Version: runtimeMountManifestVersion, Driver: driver, Mounts: mounts}, nil
}

func BuildRuntimeMountManifest(config *appconfig.Config, session *Session, driver string) (RuntimeMountManifest, error) {
	return buildRuntimeMountManifest(config, session, driver)
}

func runtimeMountSpecsForDriver(config *appconfig.Config, session *Session, driver string) []runtimeMountSpec {
	switch resolveRuntimeDriver(driver) {
	case RuntimeDriverDocker:
		return runtimeMountSpecsForDocker(config, session)
	case RuntimeDriverBoxlite:
		return runtimeMountSpecsForBoxlite(config, session)
	case RuntimeDriverMicrosandbox:
		return runtimeMountSpecsForMicrosandbox(config, session)
	default:
		return nil
	}
}

func runtimeMountEntries(config *appconfig.Config) []logicalRuntimeMountEntry {
	appconfig.ApplyDefaultGuestPaths(config)
	return []logicalRuntimeMountEntry{
		{sessionPath: "workspace", guestPath: config.GuestWorkspacePath, directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "state", guestPath: config.GuestStateRoot, directoryOnlyExposure: directoryOnlyExposureAlreadyInData},
		{sessionPath: "runtime", guestPath: config.GuestRuntimeRoot, directoryOnlyExposure: directoryOnlyExposureAlreadyInData},
		{sessionPath: "logs", guestPath: config.GuestLogRoot, directoryOnlyExposure: directoryOnlyExposureAlreadyInData},
		{sessionPath: "home/.codex", guestPath: filepath.Join(config.GuestHomePath, ".codex"), directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "home/.claude", guestPath: filepath.Join(config.GuestHomePath, ".claude"), directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "home/.opencode", guestPath: filepath.Join(config.GuestHomePath, ".opencode"), directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "home/.claude.json", guestPath: filepath.Join(config.GuestHomePath, ".claude.json"), isFile: true, directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "home/.gitconfig", guestPath: filepath.Join(config.GuestHomePath, ".gitconfig"), isFile: true, directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "home/.gemini", guestPath: filepath.Join(config.GuestHomePath, ".gemini"), directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "home/.config/claude", guestPath: filepath.Join(config.GuestHomePath, ".config", "claude"), directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "home/.config/Claude", guestPath: filepath.Join(config.GuestHomePath, ".config", "Claude"), directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "home/.config/gemini", guestPath: filepath.Join(config.GuestHomePath, ".config", "gemini"), directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "home/.config/opencode", guestPath: filepath.Join(config.GuestHomePath, ".config", "opencode"), directoryOnlyExposure: directoryOnlyExposureSymlink},
		{sessionPath: "home/.local/share/gemini", guestPath: filepath.Join(config.GuestHomePath, ".local", "share", "gemini"), directoryOnlyExposure: directoryOnlyExposureSymlink},
	}
}

func logicalRuntimeMountHostPath(session *Session, entry logicalRuntimeMountEntry) string {
	return filepath.Join(hostSessionDir(session), filepath.FromSlash(entry.sessionPath))
}

func runtimeMountSpecsForDocker(config *appconfig.Config, session *Session) []runtimeMountSpec {
	entries := runtimeMountEntries(config)
	specs := make([]runtimeMountSpec, 0, len(entries))
	for _, entry := range entries {
		specs = append(specs, runtimeMountSpec{
			hostPath:  logicalRuntimeMountHostPath(session, entry),
			guestPath: entry.guestPath,
			isFile:    entry.isFile,
		})
	}
	return append(specs, sessionVolumeMountSpecs(session)...)
}

func runtimeMountSpecsForBoxlite(config *appconfig.Config, session *Session) []runtimeMountSpec {
	if len(runtimeMountEntries(config)) == 0 {
		return nil
	}
	return []runtimeMountSpec{
		{hostPath: hostSessionDir(session), guestPath: directoryOnlyGuestSessionPath},
	}
}

func runtimeMountSpecsForMicrosandbox(config *appconfig.Config, session *Session) []runtimeMountSpec {
	specs := runtimeMountSpecsForBoxlite(config, session)
	return append(specs, sessionVolumeMountSpecs(session)...)
}

func sessionVolumeMountSpecs(session *Session) []runtimeMountSpec {
	if session == nil || len(session.VolumeMounts) == 0 {
		return nil
	}
	specs := make([]runtimeMountSpec, 0, len(session.VolumeMounts))
	for _, mount := range session.VolumeMounts {
		hostPath := strings.TrimSpace(mount.HostPath)
		guestPath := filepath.Clean(strings.TrimSpace(mount.Target))
		if hostPath == "" || guestPath == "." || guestPath == "" {
			continue
		}
		specs = append(specs, runtimeMountSpec{
			hostPath:  hostPath,
			guestPath: guestPath,
			readOnly:  mount.ReadOnly,
		})
	}
	return specs
}

func ensureRuntimeMountSource(spec runtimeMountSpec) error {
	if spec.isFile {
		if err := os.MkdirAll(filepath.Dir(spec.hostPath), 0o755); err != nil {
			return fmt.Errorf("create runtime mount file parent %s: %w", filepath.Dir(spec.hostPath), err)
		}
		info, err := os.Stat(spec.hostPath)
		if err != nil {
			return fmt.Errorf("runtime mount file source missing %s: %w", spec.hostPath, err)
		}
		if info.IsDir() {
			return fmt.Errorf("runtime mount file source is a directory: %s", spec.hostPath)
		}
		return nil
	}
	if err := os.MkdirAll(spec.hostPath, 0o755); err != nil {
		return fmt.Errorf("create runtime mount source %s: %w", spec.hostPath, err)
	}
	info, err := os.Stat(spec.hostPath)
	if err != nil {
		return fmt.Errorf("stat runtime mount source %s: %w", spec.hostPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime mount directory source is a file: %s", spec.hostPath)
	}
	return nil
}

func initializeSessionHomeDefaults(session *Session) error {
	home := hostSessionHome(session)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("create session home: %w", err)
	}
	for _, item := range []string{".codex", ".claude", ".claude.json", ".gitconfig"} {
		target := filepath.Join(home, item)
		if _, err := os.Stat(target); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat default home target %s: %w", target, err)
		}
		if err := copyEmbeddedHomeAsset(item, target); err != nil {
			return err
		}
	}
	return nil
}

func initializeSessionRuntimeMountDirs(config *appconfig.Config, session *Session) error {
	for _, entry := range runtimeMountEntries(config) {
		if entry.isFile {
			continue
		}
		dir := logicalRuntimeMountHostPath(session, entry)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create session runtime mount dir %s: %w", dir, err)
		}
	}
	return nil
}

func copyEmbeddedHomeAsset(assetPath, target string) error {
	info, err := fs.Stat(defaultassets.DefaultHomeFS, assetPath)
	if err != nil {
		return fmt.Errorf("stat default home asset %s: %w", assetPath, err)
	}
	if info.IsDir() {
		return copyEmbeddedHomeDir(assetPath, target)
	}
	return copyEmbeddedHomeFile(assetPath, target, info.Mode())
}

func copyEmbeddedHomeDir(assetPath, target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create default home dir %s: %w", target, err)
	}
	return fs.WalkDir(defaultassets.DefaultHomeFS, assetPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(assetPath, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if entry.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyEmbeddedHomeFile(path, dst, info.Mode())
	})
}

func copyEmbeddedHomeFile(assetPath, target string, mode fs.FileMode) error {
	data, err := fs.ReadFile(defaultassets.DefaultHomeFS, assetPath)
	if err != nil {
		return fmt.Errorf("read default home asset %s: %w", assetPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create default home file parent %s: %w", filepath.Dir(target), err)
	}
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	if perm&0o200 == 0 {
		perm |= 0o200
	}
	if err := os.WriteFile(target, data, perm); err != nil {
		return fmt.Errorf("write default home file %s: %w", target, err)
	}
	return nil
}
