package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const boxliteVolumeBridgeSessionPath = "volumes"

// BoxLite currently has an upstream multi-directory mount issue that can fail
// VM startup with libkrun IrqsExhausted errors. Until BoxLite can consume the
// normal multi-mount manifest reliably, user volumes are exposed through the
// existing directory-only /data mount. Host sources are bind-mounted into the
// session directory, then guest-side symlinks expose those bridge directories
// at the requested target paths.
//
// Upstream issue: https://github.com/boxlite-ai/boxlite/issues/935
func prepareBoxliteVolumeBridge(session *Sandbox) error {
	entries := boxliteVolumeBridgeEntries(session)
	if len(entries) == 0 {
		return nil
	}
	root := filepath.Join(hostSandboxDir(session), boxliteVolumeBridgeSessionPath)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create boxlite volume bridge dir: %w", err)
	}
	mounted := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.nfs {
			// The export is mounted directly on the bridge path by the
			// process that starts the VM; see ensureSandboxNFSMounts.
			if err := os.MkdirAll(entry.hostBridgePath, nfsMountPointMode); err != nil {
				rollbackBoxliteVolumeBridgeMounts(mounted)
				return fmt.Errorf("create boxlite nfs volume mount point %s: %w", entry.hostBridgePath, err)
			}
			continue
		}
		if err := ensureBoxliteVolumeBridgeSource(entry.hostPath); err != nil {
			rollbackBoxliteVolumeBridgeMounts(mounted)
			return err
		}
		if err := ensureBoxliteVolumeBridgeMount(entry.hostBridgePath, entry.hostPath, entry.readOnly); err != nil {
			rollbackBoxliteVolumeBridgeMounts(append(mounted, entry.hostBridgePath))
			return err
		}
		mounted = append(mounted, entry.hostBridgePath)
	}
	return nil
}

type boxliteVolumeBridgeMountFunc func(sourcePath string, targetPath string, readOnly bool) error
type boxliteVolumeBridgeUnmountFunc func(targetPath string) error

var boxliteVolumeBridgeMounter boxliteVolumeBridgeMountFunc = mountBoxliteVolumeBridgeSource
var boxliteVolumeBridgeUnmounter boxliteVolumeBridgeUnmountFunc = unmountBoxliteVolumeBridgeMount

type boxliteVolumeBridgeEntry struct {
	id             string
	hostPath       string
	hostBridgePath string
	guestSource    string
	guestTarget    string
	readOnly       bool
	nfs            bool
}

func boxliteVolumeBridgeEntries(session *Sandbox) []boxliteVolumeBridgeEntry {
	if session == nil || len(session.VolumeMounts) == 0 {
		return nil
	}
	sessionDir := hostSandboxDir(session)
	if strings.TrimSpace(sessionDir) == "" {
		return nil
	}
	entries := make([]boxliteVolumeBridgeEntry, 0, len(session.VolumeMounts))
	for _, mount := range session.VolumeMounts {
		hostPath := strings.TrimSpace(mount.HostPath)
		guestTarget := filepath.Clean(strings.TrimSpace(mount.Target))
		if hostPath == "" || guestTarget == "." || guestTarget == "" {
			continue
		}
		id := boxliteVolumeBridgeID(mount)
		hostBridgePath := filepath.Join(sessionDir, boxliteVolumeBridgeSessionPath, id)
		guestSource := filepath.Clean(filepath.Join(directoryOnlyGuestSandboxPath, boxliteVolumeBridgeSessionPath, id))
		entries = append(entries, boxliteVolumeBridgeEntry{
			id:             id,
			hostPath:       hostPath,
			hostBridgePath: hostBridgePath,
			guestSource:    guestSource,
			guestTarget:    guestTarget,
			readOnly:       mount.ReadOnly,
			nfs:            isNFSVolumeMount(mount),
		})
	}
	return entries
}

func boxliteVolumeGuestSymlinkCommands(session *Sandbox) []string {
	entries := boxliteVolumeBridgeEntries(session)
	if len(entries) == 0 {
		return nil
	}
	commands := make([]string, 0, len(entries))
	for _, entry := range entries {
		commands = append(commands, directoryOnlySymlinkCommand(entry.guestSource, entry.guestTarget, false, true))
	}
	return commands
}

func boxliteVolumeBridgeID(mount SandboxVolumeMount) string {
	id := strings.TrimSpace(mount.ID)
	if isSafeBoxliteVolumeBridgeID(id) {
		return id
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(mount.Type),
		strings.TrimSpace(mount.VolumeID),
		strings.TrimSpace(mount.Source),
		strings.TrimSpace(mount.Target),
		strings.TrimSpace(mount.HostPath),
	}, "\x00")))
	return "mount-" + hex.EncodeToString(sum[:])[:24]
}

func isSafeBoxliteVolumeBridgeID(id string) bool {
	if id == "" || id == "." || id == ".." || filepath.IsAbs(id) {
		return false
	}
	return filepath.Base(id) == id
}

func ensureBoxliteVolumeBridgeSource(hostPath string) error {
	info, err := os.Stat(hostPath)
	if err != nil {
		return fmt.Errorf("stat boxlite volume bridge source %s: %w", hostPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("boxlite volume bridge source is not a directory: %s", hostPath)
	}
	return nil
}

func ensureBoxliteVolumeBridgeMount(bridgePath, sourcePath string, readOnly bool) error {
	if err := os.MkdirAll(filepath.Dir(bridgePath), 0o755); err != nil {
		return fmt.Errorf("create boxlite volume bridge parent %s: %w", filepath.Dir(bridgePath), err)
	}
	info, err := os.Lstat(bridgePath)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(bridgePath); err != nil {
				return fmt.Errorf("replace stale boxlite volume bridge symlink %s: %w", bridgePath, err)
			}
		} else if !info.IsDir() {
			return fmt.Errorf("boxlite volume bridge path already exists and is not a directory: %s (%s)", bridgePath, info.Mode().Type())
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat boxlite volume bridge path %s: %w", bridgePath, err)
	}
	if err := os.MkdirAll(bridgePath, 0o755); err != nil {
		return fmt.Errorf("create boxlite volume bridge path %s: %w", bridgePath, err)
	}
	if err := boxliteVolumeBridgeMounter(sourcePath, bridgePath, readOnly); err != nil {
		return fmt.Errorf("mount boxlite volume bridge %s -> %s: %w", sourcePath, bridgePath, err)
	}
	return nil
}

func rollbackBoxliteVolumeBridgeMounts(paths []string) {
	for i := len(paths) - 1; i >= 0; i-- {
		_ = boxliteVolumeBridgeUnmounter(paths[i])
	}
}

// CleanupBoxliteVolumeBridgeMounts releases host-side bridge mounts under a
// session directory. It is safe to call for sessions that do not use BoxLite or
// do not have volume bridges.
func CleanupBoxliteVolumeBridgeMounts(sessionDir string) error {
	root := filepath.Join(strings.TrimSpace(sessionDir), boxliteVolumeBridgeSessionPath)
	if strings.TrimSpace(sessionDir) == "" {
		return nil
	}
	mounts, err := boxliteVolumeBridgeMountPoints(root)
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		if err := boxliteVolumeBridgeUnmounter(mount); err != nil {
			return fmt.Errorf("unmount boxlite volume bridge %s: %w", mount, err)
		}
	}
	return nil
}

func boxliteVolumeBridgeMountPoints(root string) ([]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read mountinfo: %w", err)
	}
	cleanRoot := filepath.Clean(root)
	if cleanRoot == "." || cleanRoot == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid boxlite volume bridge root: %q", root)
	}
	prefix := cleanRoot + string(filepath.Separator)
	mounts := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mountPath := filepath.Clean(unescapeMountInfoPath(fields[4]))
		if mountPath == cleanRoot || strings.HasPrefix(mountPath, prefix) {
			mounts = append(mounts, mountPath)
		}
	}
	sortMountPathsDesc(mounts)
	return mounts, nil
}

func sortMountPathsDesc(paths []string) {
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0 && len(paths[j]) > len(paths[j-1]); j-- {
			paths[j], paths[j-1] = paths[j-1], paths[j]
		}
	}
}

func unescapeMountInfoPath(path string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(path)
}
