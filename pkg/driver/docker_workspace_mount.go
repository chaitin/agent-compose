package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/system"
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

func (r *dockerRuntime) bindWorkspaceMountSource(ctx context.Context, dockerClient *client.Client, hostPath string) (string, error) {
	if dockerClient == nil {
		return "", fmt.Errorf("docker client is required to resolve workspace mount source")
	}
	process, err := currentDockerWorkspaceProcess()
	if err != nil {
		return "", err
	}
	self, err := dockerClient.ContainerInspect(ctx, process.hostname)
	if err == nil {
		return dockerWorkspaceContainerSource(self, hostPath)
	}
	if !isDockerNotFound(err) {
		return "", fmt.Errorf("inspect daemon container for workspace mount: %w", err)
	}
	if process.containerized {
		return "", fmt.Errorf("workspace mount source cannot be mapped: daemon container is not inspectable by this Docker Engine")
	}
	info, err := dockerClient.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect Docker Engine for workspace mount: %w", err)
	}
	if !dockerWorkspaceLocalEngine(process, dockerClient.DaemonHost(), info) {
		return "", fmt.Errorf("workspace mounts require a local Docker Engine or an inspectable daemon container with a shared source mount; remote or unknown topology is unsupported")
	}
	return hostPath, nil
}

func dockerWorkspaceContainerSource(self containerapi.InspectResponse, hostPath string) (string, error) {
	var bestSource, bestDestination string
	for _, mount := range self.Mounts {
		if mount.Type != mountapi.TypeBind && mount.Type != mountapi.TypeVolume {
			continue
		}
		if strings.TrimSpace(mount.Source) == "" || strings.TrimSpace(mount.Destination) == "" {
			continue
		}
		destination := filepath.Clean(mount.Destination)
		if runtimeMountPathWithin(hostPath, destination) && len(destination) > len(bestDestination) {
			bestSource, bestDestination = mount.Source, destination
		}
	}
	if bestSource == "" {
		return "", fmt.Errorf("workspace source %q is not in a shared daemon container mount; container writable-layer sources cannot be mounted", hostPath)
	}
	relative, err := filepath.Rel(bestDestination, hostPath)
	if err != nil {
		return "", fmt.Errorf("resolve shared workspace source: %w", err)
	}
	return joinDockerHostPath(bestSource, relative), nil
}

type dockerWorkspaceProcess struct {
	platform      string
	hostname      string
	containerized bool
}

func currentDockerWorkspaceProcess() (dockerWorkspaceProcess, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return dockerWorkspaceProcess{}, fmt.Errorf("resolve daemon hostname for workspace mount: %w", err)
	}
	process := dockerWorkspaceProcess{platform: runtime.GOOS, hostname: hostname}
	if runtime.GOOS != "linux" {
		return process, nil
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv", "/run/systemd/container"} {
		if _, err := os.Stat(marker); err == nil {
			process.containerized = true
			return process, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return dockerWorkspaceProcess{}, fmt.Errorf("inspect daemon container marker: %w", err)
		}
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return dockerWorkspaceProcess{}, fmt.Errorf("inspect daemon container cgroup: %w", err)
	}
	for _, marker := range []string{"docker", "kubepods", "containerd", "libpod"} {
		if strings.Contains(string(data), marker) {
			process.containerized = true
			break
		}
	}
	return process, nil
}

func dockerWorkspaceLocalEngine(process dockerWorkspaceProcess, endpoint string, info system.Info) bool {
	if process.containerized || info.OSType != "linux" || !strings.HasPrefix(endpoint, "unix://") {
		return false
	}
	// Socket location alone cannot establish that the Engine shares our files.
	// Support the native Linux daemon and the two maintained macOS integrations;
	// forwarded sockets and other topologies have no automatic sharing contract.
	if process.platform == "linux" {
		return process.hostname != "" && process.hostname == info.Name
	}
	if process.platform == "darwin" {
		return info.OperatingSystem == "Docker Desktop" || info.OperatingSystem == "OrbStack"
	}
	return false
}
