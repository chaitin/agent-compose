package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	microsandboxRootfsOwnershipSuffix  = ".qcow2.owner.json"
	microsandboxRootfsOwnershipVersion = 2
	microsandboxRootfsOwnershipKind    = "microsandbox-rootfs"
)

type CombinedMaterializedDependencies []MaterializedDependencyProvider

func (providers CombinedMaterializedDependencies) MaterializedDependencies(ctx context.Context) ([]MaterializedDependency, []string, error) {
	var dependencies []MaterializedDependency
	var warnings []string
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		items, providerWarnings, err := provider.MaterializedDependencies(ctx)
		warnings = AppendWarnings(warnings, providerWarnings...)
		if err != nil {
			return nil, warnings, err
		}
		dependencies = append(dependencies, items...)
	}
	return dependencies, warnings, nil
}

type MicrosandboxRootfsDependencies struct {
	Home string
	// BaseRoot is the image cache materialization root that legitimate base
	// disks live under. A sidecar is only trusted to name a base disk when its
	// backing file resolves inside this root; anything else is reported as an
	// unresolved dependency instead.
	BaseRoot string
}

func (p MicrosandboxRootfsDependencies) MaterializedDependencies(ctx context.Context) ([]MaterializedDependency, []string, error) {
	home := strings.TrimSpace(p.Home)
	if home == "" {
		return nil, nil, nil
	}
	root := filepath.Join(home, "rootfs-disks")
	rootInfo, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("inspect microsandbox rootfs dependencies %s: %w", root, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, nil, fmt.Errorf("microsandbox rootfs dependencies %s is not a regular directory", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("read microsandbox rootfs dependencies %s: %w", root, err)
	}
	var dependencies []MaterializedDependency
	var warnings []string
	// A sidecar this scan cannot read or trust never fails the whole inventory:
	// one damaged file must not take down `cache ls` for every other domain.
	// It is reported as unresolved instead, which keeps base disk removal
	// blocked until the operator repairs or removes it.
	unresolved := func(path string, reason error) {
		message := fmt.Sprintf("microsandbox rootfs ownership %s cannot safely identify its base disk: %v", path, reason)
		warnings = AppendWarnings(warnings, message)
		dependencies = append(dependencies, MaterializedDependency{Unresolved: true, Reason: message})
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, warnings, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), microsandboxRootfsOwnershipSuffix) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			unresolved(path, err)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			unresolved(path, fmt.Errorf("not a regular file"))
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			unresolved(path, err)
			continue
		}
		var sidecar struct {
			Version      int    `json:"version"`
			ResourceKind string `json:"resource_kind"`
			SandboxID    string `json:"sandbox_id"`
			BaseIdentity string `json:"base_cache_identity"`
			BackingPath  string `json:"backing_file_path"`
		}
		if err := json.Unmarshal(data, &sidecar); err != nil {
			unresolved(path, err)
			continue
		}
		if sidecar.Version != microsandboxRootfsOwnershipVersion || sidecar.ResourceKind != microsandboxRootfsOwnershipKind || strings.TrimSpace(sidecar.SandboxID) == "" {
			unresolved(path, fmt.Errorf("ownership record is not a version %d %s record with a sandbox id", microsandboxRootfsOwnershipVersion, microsandboxRootfsOwnershipKind))
			continue
		}
		backingPath, err := p.baseDiskPath(sidecar.BackingPath, strings.TrimSpace(sidecar.BaseIdentity))
		if err != nil {
			unresolved(path, err)
			continue
		}
		dependencies = append(dependencies, MaterializedDependency{SandboxID: sidecar.SandboxID, Identity: sidecar.BaseIdentity, Path: backingPath, Status: "owned"})
	}
	return dependencies, warnings, nil
}

// baseDiskPath confirms that a sidecar names a base disk this cache could have
// published, and returns the reference key for it. The sidecar lives in a
// driver-owned directory, so its backing path is untrusted input: without the
// containment check any writer there could pin an unrelated cache entry as
// referenced and block its removal forever.
//
// Containment stays lexical on purpose. The scanner keys its reference map on
// the paths it builds from ReadDir, so canonicalizing here would silently stop
// matching whenever DATA_ROOT itself contains a symlink.
func (p MicrosandboxRootfsDependencies) baseDiskPath(backingPath, identity string) (string, error) {
	baseRoot := strings.TrimSpace(p.BaseRoot)
	if baseRoot == "" {
		return "", fmt.Errorf("image cache materialization root is unknown")
	}
	if identity == "" {
		return "", fmt.Errorf("base cache identity is empty")
	}
	backingPath = strings.TrimSpace(backingPath)
	if !filepath.IsAbs(backingPath) {
		return "", fmt.Errorf("backing file path %q is not absolute", backingPath)
	}
	clean := filepath.Clean(backingPath)
	if !pathWithinRoot(baseRoot, clean) {
		return "", fmt.Errorf("backing file %s is outside the image cache materialization root %s", clean, baseRoot)
	}
	// Legitimate base disks are published as
	// <materialization root>/<image id>/<microsandbox dir>/<identity>.qcow2.
	relative, err := filepath.Rel(baseRoot, clean)
	if err != nil {
		return "", fmt.Errorf("backing file %s is not relative to %s: %w", clean, baseRoot, err)
	}
	segments := strings.Split(relative, string(filepath.Separator))
	if len(segments) != 3 || segments[1] != microsandboxBaseDiskDirName {
		return "", fmt.Errorf("backing file %s is not a microsandbox base disk path under %s", clean, baseRoot)
	}
	if name := segments[2]; name != identity+microsandboxBaseDiskSuffix || !strings.HasPrefix(name, microsandboxBaseDiskPrefix) {
		return "", fmt.Errorf("backing file %s does not match base cache identity %q", clean, identity)
	}
	return clean, nil
}
