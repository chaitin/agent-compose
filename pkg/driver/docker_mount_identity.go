package driver

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
)

func (r *dockerRuntime) dockerSandboxMountsMatch(info containerapi.InspectResponse, expected []mountapi.Mount, sandbox *Sandbox) bool {
	workspace, err := decodeSandboxWorkspaceMount(sandbox, RuntimeDriverDocker)
	if err != nil {
		return false
	}
	if workspace != nil {
		target := filepath.Join(r.config.GuestWorkspacePath, workspace.Target)
		for _, mount := range info.Mounts {
			if mount.Type != mountapi.TypeBind && runtimeMountPathsOverlap(mount.Destination, target) {
				return false
			}
		}
	}
	return dockerContainerMountsMatch(info, expected)
}

// Image-declared volumes are materialized at container creation and can hide a
// workspace bind, including its read-only protection. Reject them before Start.
func (r *dockerRuntime) validateCreatedDockerWorkspaceMounts(ctx context.Context, remover dockerContainerRemover, sandbox *Sandbox, info containerapi.InspectResponse, expected []mountapi.Mount) error {
	workspace, err := decodeSandboxWorkspaceMount(sandbox, RuntimeDriverDocker)
	if err == nil && (workspace == nil || r.dockerSandboxMountsMatch(info, expected, sandbox)) {
		return nil
	}
	cause := errors.Join(fmt.Errorf("created docker container %s mounts conflict with workspace delivery", info.ID), err)
	// This container was just created by this attempt. RemoveVolumes releases
	// only its anonymous volumes, preserving external binds and named volumes.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dockerStopFallbackActionTimeout)
	defer cancel()
	if err := remover.ContainerRemove(cleanupCtx, info.ID, containerapi.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !isDockerNotFound(err) {
		return errors.Join(cause, fmt.Errorf("remove rejected docker container: %w", err))
	}
	return cause
}

func dockerContainerMountsMatch(containerInfo containerapi.InspectResponse, expected []mountapi.Mount) bool {
	type mountIdentity struct {
		source   string
		readOnly bool
	}
	actual := make(map[string]mountIdentity, len(containerInfo.Mounts))
	for _, item := range containerInfo.Mounts {
		if item.Type != mountapi.TypeBind {
			continue
		}
		target := filepath.Clean(item.Destination)
		if _, duplicate := actual[target]; duplicate {
			return false
		}
		actual[target] = mountIdentity{source: filepath.Clean(item.Source), readOnly: !item.RW}
	}
	for _, item := range expected {
		if item.Type != mountapi.TypeBind {
			continue
		}
		target := filepath.Clean(item.Target)
		identity, ok := actual[target]
		if !ok || identity.source != filepath.Clean(item.Source) || identity.readOnly != item.ReadOnly {
			return false
		}
		if item.BindOptions != nil && item.BindOptions.ReadOnlyForceRecursive && !dockerContainerHasRecursiveReadOnly(containerInfo, item) {
			return false
		}
		delete(actual, target)
	}
	// Extra stale binds can still shadow the current workspace, even if every
	// expected entry was found. The comparison must work in both directions.
	return len(actual) == 0
}

func dockerContainerHasRecursiveReadOnly(info containerapi.InspectResponse, expected mountapi.Mount) bool {
	if info.ContainerJSONBase == nil || info.HostConfig == nil {
		return false
	}
	for _, mount := range info.HostConfig.Mounts {
		if mount.Type == mountapi.TypeBind && filepath.Clean(mount.Target) == filepath.Clean(expected.Target) {
			return filepath.Clean(mount.Source) == filepath.Clean(expected.Source) && mount.ReadOnly &&
				mount.BindOptions != nil && mount.BindOptions.ReadOnlyForceRecursive &&
				!mount.BindOptions.NonRecursive && !mount.BindOptions.ReadOnlyNonRecursive
		}
	}
	return false
}
