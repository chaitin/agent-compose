package driver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const boxliteVolumeBridgeSessionPath = "volumes"

// BoxLite currently has an upstream multi-directory mount issue that can fail
// VM startup with libkrun IrqsExhausted errors. Until BoxLite can consume the
// normal multi-mount manifest reliably, user volumes are exposed through the
// existing directory-only /data mount by creating host-side session symlinks
// and guest-side target symlinks.
//
// Upstream issue: https://github.com/boxlite-ai/boxlite/issues/935
func prepareBoxliteVolumeSymlinkBridge(session *Session) error {
	entries := boxliteVolumeSymlinkEntries(session)
	if len(entries) == 0 {
		return nil
	}
	root := filepath.Join(hostSessionDir(session), boxliteVolumeBridgeSessionPath)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create boxlite volume bridge dir: %w", err)
	}
	for _, entry := range entries {
		if err := ensureBoxliteVolumeBridgeSource(entry.hostPath); err != nil {
			return err
		}
		if err := ensureBoxliteVolumeBridgeSymlink(entry.hostBridgePath, entry.hostPath); err != nil {
			return err
		}
	}
	return nil
}

type boxliteVolumeSymlinkEntry struct {
	id             string
	hostPath       string
	hostBridgePath string
	guestSource    string
	guestTarget    string
}

func boxliteVolumeSymlinkEntries(session *Session) []boxliteVolumeSymlinkEntry {
	if session == nil || len(session.VolumeMounts) == 0 {
		return nil
	}
	sessionDir := hostSessionDir(session)
	if strings.TrimSpace(sessionDir) == "" {
		return nil
	}
	entries := make([]boxliteVolumeSymlinkEntry, 0, len(session.VolumeMounts))
	for _, mount := range session.VolumeMounts {
		hostPath := strings.TrimSpace(mount.HostPath)
		guestTarget := filepath.Clean(strings.TrimSpace(mount.Target))
		if hostPath == "" || guestTarget == "." || guestTarget == "" {
			continue
		}
		id := boxliteVolumeBridgeID(mount)
		hostBridgePath := filepath.Join(sessionDir, boxliteVolumeBridgeSessionPath, id)
		guestSource := filepath.Clean(filepath.Join(directoryOnlyGuestSessionPath, boxliteVolumeBridgeSessionPath, id))
		entries = append(entries, boxliteVolumeSymlinkEntry{
			id:             id,
			hostPath:       hostPath,
			hostBridgePath: hostBridgePath,
			guestSource:    guestSource,
			guestTarget:    guestTarget,
		})
	}
	return entries
}

func boxliteVolumeGuestSymlinkCommands(session *Session) []string {
	entries := boxliteVolumeSymlinkEntries(session)
	if len(entries) == 0 {
		return nil
	}
	commands := make([]string, 0, len(entries))
	for _, entry := range entries {
		commands = append(commands, directoryOnlySymlinkCommand(entry.guestSource, entry.guestTarget, false, true))
	}
	return commands
}

func boxliteVolumeBridgeID(mount SessionVolumeMount) string {
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

func ensureBoxliteVolumeBridgeSymlink(linkPath, targetPath string) error {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("create boxlite volume bridge parent %s: %w", filepath.Dir(linkPath), err)
	}
	info, err := os.Lstat(linkPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("boxlite volume bridge path already exists and is not a symlink: %s (%s)", linkPath, info.Mode().Type())
		}
		current, err := os.Readlink(linkPath)
		if err != nil {
			return fmt.Errorf("read boxlite volume bridge symlink %s: %w", linkPath, err)
		}
		if current == targetPath {
			return nil
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("replace stale boxlite volume bridge symlink %s: %w", linkPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat boxlite volume bridge path %s: %w", linkPath, err)
	}
	if err := os.Symlink(targetPath, linkPath); err != nil {
		return fmt.Errorf("create boxlite volume bridge symlink %s -> %s: %w", linkPath, targetPath, err)
	}
	return nil
}
