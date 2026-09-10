package driver

import (
	"fmt"
	"path/filepath"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

func sandboxWorkspaceMount(sandbox *Sandbox, driver string) (*workspaces.FileWorkspaceMountConfig, error) {
	mount, err := decodeSandboxWorkspaceMount(sandbox, driver)
	if err != nil || mount == nil {
		return nil, err
	}
	if err := workspaces.ValidateFileWorkspaceMount(*mount); err != nil {
		return nil, fmt.Errorf("validate sandbox workspace mount: %w", err)
	}
	return mount, nil
}

// Existing mounts retain their directory references even when the source path
// disappears. Inspecting their identity must not require a new bind to succeed.
func decodeSandboxWorkspaceMount(sandbox *Sandbox, driver string) (*workspaces.FileWorkspaceMountConfig, error) {
	if sandbox == nil || sandbox.Workspace == nil {
		return nil, nil
	}
	mount, err := workspaces.DecodeFileWorkspaceMount(sandbox.Workspace.ConfigJSON)
	if err != nil {
		return nil, fmt.Errorf("decode sandbox workspace mount: %w", err)
	}
	if mount == nil {
		return nil, nil
	}
	if !strings.EqualFold(strings.TrimSpace(sandbox.Workspace.Type), "file") {
		return nil, fmt.Errorf("workspace mount mode requires provider file")
	}
	if resolveRuntimeDriver(driver) != RuntimeDriverDocker {
		return nil, fmt.Errorf("workspace mode mount is not supported by driver %q; use copy or docker", driver)
	}
	return mount, nil
}

func workspaceRuntimeMountSpec(config *appconfig.Config, sandbox *Sandbox, driver string) (*runtimeMountSpec, error) {
	mount, err := sandboxWorkspaceMount(sandbox, driver)
	if err != nil || mount == nil {
		return nil, err
	}
	return workspaceRuntimeMountSpecFromSnapshot(config, sandbox, mount)
}

func workspaceRuntimeMountSpecFromSnapshot(config *appconfig.Config, sandbox *Sandbox, mount *workspaces.FileWorkspaceMountConfig) (*runtimeMountSpec, error) {
	if mount == nil {
		return nil, nil
	}
	if config == nil {
		return nil, fmt.Errorf("runtime config is required for workspace mounts")
	}
	localConfig := *config
	config = &localConfig
	appconfig.ApplyDefaultGuestPaths(config)
	guestRoot := filepath.Clean(config.GuestWorkspacePath)
	target := filepath.Join(guestRoot, mount.Target)
	if !filepath.IsAbs(guestRoot) || !runtimeMountPathWithin(target, guestRoot) {
		return nil, fmt.Errorf("workspace mount target %q is outside guest workspace %q", target, guestRoot)
	}
	for _, entry := range runtimeMountEntries(config) {
		if entry.sandboxPath == "workspace" {
			continue
		}
		if runtimeMountPathsOverlap(guestRoot, entry.guestPath) {
			return nil, fmt.Errorf("workspace root %q overlaps runtime path %q", guestRoot, entry.guestPath)
		}
	}
	if runtimeMountPathsOverlap(guestRoot, config.GuestHomePath) {
		return nil, fmt.Errorf("workspace root %q overlaps guest home %q", guestRoot, config.GuestHomePath)
	}
	for _, volume := range sandbox.VolumeMounts {
		if runtimeMountPathsOverlap(target, volume.Target) {
			return nil, fmt.Errorf("workspace mount target %q overlaps volume target %q", target, volume.Target)
		}
	}
	return &runtimeMountSpec{
		hostPath: mount.SourcePath, guestPath: target, readOnly: mount.ReadOnly, mustExist: true,
	}, nil
}

func runtimeMountPathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func runtimeMountPathsOverlap(first, second string) bool {
	return runtimeMountPathWithin(first, second) || runtimeMountPathWithin(second, first)
}

func applyWorkspaceRuntimeMount(config *appconfig.Config, specs []runtimeMountSpec, workspace *runtimeMountSpec) []runtimeMountSpec {
	if workspace == nil {
		return specs
	}
	out := append([]runtimeMountSpec(nil), specs...)
	guestRoot := filepath.Clean(config.GuestWorkspacePath)
	if filepath.Clean(workspace.guestPath) == guestRoot {
		// The first logical entry is the owned workspace. Layout validation has
		// already rejected every other mount that could cover this target.
		for index := range out {
			if filepath.Clean(out[index].guestPath) == guestRoot {
				out[index] = *workspace
				return out
			}
		}
	}
	return append(out, *workspace)
}

// validateWorkspaceRuntimeManifest refuses stale delivery descriptions rather
// than silently mounting whichever source was last written to the manifest.
func validateWorkspaceRuntimeManifest(config *appconfig.Config, sandbox *Sandbox, manifest RuntimeMountManifest, workspace *runtimeMountSpec) error {
	if workspace == nil {
		return nil
	}
	localConfig := *config
	config = &localConfig
	specs := applyWorkspaceRuntimeMount(config, runtimeMountSpecsForDocker(config, sandbox), workspace)
	if len(specs) != len(manifest.Mounts) {
		return fmt.Errorf("runtime mount manifest does not match workspace delivery: mount count changed")
	}
	actual := make(map[string]RuntimeMount, len(manifest.Mounts))
	for _, mount := range manifest.Mounts {
		if _, duplicate := actual[mount.GuestPath]; duplicate {
			return fmt.Errorf("runtime mount manifest has duplicate target %q", mount.GuestPath)
		}
		actual[mount.GuestPath] = mount
	}
	for _, spec := range specs {
		hostPath, err := filepath.Abs(spec.hostPath)
		if err != nil {
			return fmt.Errorf("resolve runtime mount source: %w", err)
		}
		want := RuntimeMount{HostPath: hostPath, GuestPath: filepath.Clean(spec.guestPath), Type: "bind", ReadOnly: spec.readOnly}
		if got, ok := actual[want.GuestPath]; !ok || got != want {
			return fmt.Errorf("runtime mount manifest target %q does not match persisted workspace delivery", want.GuestPath)
		}
	}
	return nil
}
