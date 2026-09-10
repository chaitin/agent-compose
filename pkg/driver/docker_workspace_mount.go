package driver

import (
	"context"

	mountapi "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

func (r *dockerRuntime) dockerRuntimeMounts(ctx context.Context, dockerClient *client.Client, sandbox *Sandbox) ([]mountapi.Mount, error) {
	workspace, err := workspaceRuntimeMountSpec(r.config, sandbox, RuntimeDriverDocker)
	if err != nil {
		return nil, err
	}
	return r.dockerRuntimeMountsForWorkspace(ctx, dockerClient, sandbox, workspace)
}

func (r *dockerRuntime) dockerRuntimeMountsForLiveSandbox(ctx context.Context, dockerClient *client.Client, sandbox *Sandbox) ([]mountapi.Mount, error) {
	mount, err := decodeSandboxWorkspaceMount(sandbox, RuntimeDriverDocker)
	if err != nil {
		return nil, err
	}
	workspace, err := workspaceRuntimeMountSpecFromSnapshot(r.config, sandbox, mount)
	if err != nil {
		return nil, err
	}
	return r.dockerRuntimeMountsForWorkspace(ctx, dockerClient, sandbox, workspace)
}

func (r *dockerRuntime) dockerRuntimeMountsForWorkspace(ctx context.Context, dockerClient *client.Client, sandbox *Sandbox, workspace *runtimeMountSpec) ([]mountapi.Mount, error) {
	manifest, err := loadRuntimeMountManifest(sandbox, RuntimeDriverDocker)
	if err != nil {
		return nil, err
	}
	if err := validateWorkspaceRuntimeManifest(r.config, sandbox, manifest, workspace); err != nil {
		return nil, err
	}
	mounts := make([]mountapi.Mount, 0, len(manifest.Mounts))
	for _, item := range manifest.Mounts {
		mount := mountapi.Mount{Type: mountapi.TypeBind, Target: item.GuestPath, ReadOnly: item.ReadOnly}
		if workspace != nil && item.GuestPath == workspace.guestPath {
			mount.Source, err = r.bindWorkspaceMountSource(ctx, dockerClient, item.HostPath)
			if err == nil && item.ReadOnly {
				err = dockerClient.NewVersionError(ctx, "1.44", "recursive read-only workspace mounts")
				mount.BindOptions = &mountapi.BindOptions{ReadOnlyForceRecursive: true}
			}
		} else {
			mount.Source, err = r.bindRuntimeMountSource(ctx, dockerClient, item.HostPath)
		}
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}
