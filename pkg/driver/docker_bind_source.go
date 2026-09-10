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

type dockerBindSourcePolicy uint8

const (
	// Existing owned paths and declared volumes retain their configured host-path
	// contract, including deployments that explicitly address a remote Engine.
	dockerBindSourceConfigured dockerBindSourcePolicy = iota
	// A workspace promises to expose the daemon-validated project directory.
	// An unverified same-spelling path on another host cannot satisfy that promise.
	dockerBindSourceVerified
)

func (r *dockerRuntime) bindRuntimeMountSource(ctx context.Context, dockerClient *client.Client, hostPath string) (string, error) {
	return r.resolveDockerBindSource(ctx, dockerClient, hostPath, dockerBindSourceConfigured)
}

func (r *dockerRuntime) bindWorkspaceMountSource(ctx context.Context, dockerClient *client.Client, hostPath string) (string, error) {
	return r.resolveDockerBindSource(ctx, dockerClient, hostPath, dockerBindSourceVerified)
}

func (r *dockerRuntime) resolveDockerBindSource(ctx context.Context, dockerClient *client.Client, hostPath string, policy dockerBindSourcePolicy) (string, error) {
	hostPath = filepath.Clean(strings.TrimSpace(hostPath))
	if hostPath == "." || hostPath == "" {
		return "", fmt.Errorf("docker runtime mount source is empty")
	}
	if hostRoot := strings.TrimSpace(r.config.DockerHostSandboxRoot); hostRoot != "" {
		if runtimeMountPathWithin(hostPath, r.config.SandboxRoot) {
			relative, err := filepath.Rel(filepath.Clean(r.config.SandboxRoot), hostPath)
			if err != nil {
				return "", fmt.Errorf("resolve configured Docker source: %w", err)
			}
			return joinDockerHostPath(hostRoot, relative), nil
		}
		// The override only describes SandboxRoot. External workspace sources
		// must use their own shared mount or a verified local Engine.
		if policy == dockerBindSourceConfigured {
			return rebasePathUnderRoot(hostPath, r.config.SandboxRoot, hostRoot)
		}
	}
	if dockerClient == nil {
		if policy == dockerBindSourceConfigured {
			return hostPath, nil
		}
		return "", fmt.Errorf("docker client is required to resolve workspace mount source")
	}

	var hostname string
	if policy == dockerBindSourceVerified {
		probe := r.workspaceProcess
		if probe == nil {
			probe = currentDockerWorkspaceProcess
		}
		process, err := probe()
		if err != nil {
			return "", err
		}
		// A native daemon must never identify itself by an Engine container name.
		// Even a successful inspect of its hostname could be an unrelated container.
		if !process.containerized {
			return resolveNativeDockerWorkspaceSource(ctx, dockerClient, hostPath, process)
		}
		hostname = strings.TrimSpace(process.hostname)
		if hostname == "" {
			return "", fmt.Errorf("containerized daemon hostname is required for workspace mount mapping")
		}
	} else {
		// Configured bind paths retain their historical self-inspect/fallback policy.
		var err error
		hostname, err = os.Hostname()
		if err != nil {
			return hostPath, nil
		}
	}
	self, err := dockerClient.ContainerInspect(ctx, hostname)
	if err == nil {
		source, found, err := dockerContainerBindSource(self, hostPath)
		if err != nil {
			return "", err
		}
		if found {
			return source, nil
		}
		if policy == dockerBindSourceConfigured {
			return hostPath, nil
		}
		return "", fmt.Errorf("workspace source %q is not in a shared daemon container mount; container writable-layer sources cannot be mounted", hostPath)
	}
	if !isDockerNotFound(err) {
		return "", fmt.Errorf("inspect daemon container for Docker mount: %w", err)
	}
	if policy == dockerBindSourceConfigured {
		return hostPath, nil
	}
	return "", fmt.Errorf("workspace mount source cannot be mapped: daemon container is not inspectable by this Docker Engine")
}

func resolveNativeDockerWorkspaceSource(ctx context.Context, dockerClient *client.Client, hostPath string, process dockerWorkspaceProcess) (string, error) {
	info, err := dockerClient.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect Docker Engine for workspace mount: %w", err)
	}
	if !dockerWorkspaceLocalEngine(process, dockerClient.DaemonHost(), info) {
		return "", fmt.Errorf("workspace mounts require a local Docker Engine or an inspectable daemon container with a shared source mount; remote or unknown topology is unsupported")
	}
	return hostPath, nil
}

// dockerContainerBindSource uses the same longest-mount mapping for workspace,
// owned paths, and declared bind volumes. A nested tmpfs (or malformed mount)
// shadows its parent; mapping through that parent's source would expose the
// wrong files on the Engine host.
func dockerContainerBindSource(self containerapi.InspectResponse, hostPath string) (string, bool, error) {
	var best *containerapi.MountPoint
	var bestDestination string
	for i := range self.Mounts {
		mount := &self.Mounts[i]
		destination := filepath.Clean(strings.TrimSpace(mount.Destination))
		if destination == "." || !filepath.IsAbs(destination) {
			continue
		}
		if runtimeMountPathWithin(hostPath, destination) && len(destination) > len(bestDestination) {
			best, bestDestination = mount, destination
		}
	}
	if best == nil {
		return "", false, nil
	}
	if (best.Type != mountapi.TypeBind && best.Type != mountapi.TypeVolume) || strings.TrimSpace(best.Source) == "" {
		return "", false, fmt.Errorf("docker mount source %q is shadowed by an unshareable daemon container mount at %q", hostPath, bestDestination)
	}
	relative, err := filepath.Rel(bestDestination, hostPath)
	if err != nil {
		return "", false, fmt.Errorf("resolve shared Docker source: %w", err)
	}
	return joinDockerHostPath(best.Source, relative), true, nil
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

func rebasePathUnderRoot(path, oldRoot, newRoot string) (string, error) {
	relativeDir, err := relativePathUnderRoot(path, oldRoot)
	if err != nil {
		return "", err
	}
	return joinDockerHostPath(newRoot, relativeDir), nil
}

func joinDockerHostPath(root, relativePath string) string {
	root = strings.TrimSpace(root)
	relativePath = filepath.Clean(strings.TrimSpace(relativePath))
	if relativePath == "." || relativePath == "" {
		return root
	}
	if isWindowsHostPath(root) && strings.Contains(root, "\\") {
		return strings.TrimRight(root, `\/`) + `\` + strings.ReplaceAll(relativePath, "/", `\`)
	}
	if isWindowsHostPath(root) || strings.Contains(root, "/") {
		return strings.TrimRight(root, "/") + "/" + filepath.ToSlash(relativePath)
	}
	return filepath.Join(root, relativePath)
}

func isWindowsHostPath(path string) bool {
	if strings.HasPrefix(path, `\\`) {
		return true
	}
	if len(path) < 3 {
		return false
	}
	drive := path[0]
	if (drive < 'A' || drive > 'Z') && (drive < 'a' || drive > 'z') {
		return false
	}
	return path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

func relativePathUnderRoot(path, root string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	root = filepath.Clean(strings.TrimSpace(root))
	if path == "." || path == "" || root == "." || root == "" {
		return "", fmt.Errorf("path and root are required")
	}
	relativeDir, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve %s under %s: %w", path, root, err)
	}
	if relativeDir == "." || strings.HasPrefix(relativeDir, ".."+string(filepath.Separator)) || relativeDir == ".." || filepath.IsAbs(relativeDir) {
		return "", fmt.Errorf("path %s is outside root %s", path, root)
	}
	return relativeDir, nil
}
