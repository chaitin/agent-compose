package driver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	containerapi "github.com/docker/docker/api/types/container"
	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// dockerDaemonTopology is the L2 placement decision for Docker sandboxes: which
// network a sandbox joins and whether the daemon itself runs containerized.
type dockerDaemonTopology struct {
	networkMode   containerapi.NetworkMode
	containerized bool
}

func (r *dockerRuntime) dockerDaemonTopology(ctx context.Context, dockerClient *client.Client) (dockerDaemonTopology, error) {
	networkName, containerized, ambiguous, networkNames, err := r.dockerNetworkNameFromSelfContainer(ctx, dockerClient)
	if err != nil {
		slog.Warn("failed to inspect current docker container network; falling back to default docker network", "error", err)
		networkName, containerized, ambiguous, networkNames = "", false, false, nil
	}

	topology, err := selectDockerDaemonTopology(r.config.DockerSandboxNetwork, networkName, networkNames, containerized, ambiguous, func(name string) error {
		_, inspectErr := dockerClient.NetworkInspect(ctx, name, networkapi.InspectOptions{})
		return inspectErr
	})
	if err != nil {
		return dockerDaemonTopology{}, err
	}
	if topology.networkMode == containerapi.NetworkMode("default") {
		slog.Warn("current process is not running in an inspectable docker container; falling back to default docker network")
	}
	return topology, nil
}

// selectDockerDaemonTopology resolves the network a Docker sandbox joins from
// the explicit configuration and the daemon's self-inspected network. It is pure
// so the precedence and fail-closed behavior stay testable without a Docker
// engine. An explicit network wins, must exist, must not be a Docker built-in
// (host/none/bridge/default), and — when the daemon is containerized — must be
// one of the networks the daemon is attached to (networkExists reports the
// inspect error, daemonNetworks reports the attached names). Without an explicit
// network, the daemon's own network is used, falling back to Docker's default
// network when the daemon is not running in an inspectable container; a
// containerized daemon attached to more than one non-default network is
// ambiguous and fails closed instead of guessing, since the overlay injects
// DOCKER_SANDBOX_NETWORK whenever it dual-homes the daemon.
func selectDockerDaemonTopology(explicitNetwork, detectedNetwork string, daemonNetworks []string, containerized, ambiguous bool, networkExists func(string) error) (dockerDaemonTopology, error) {
	if name := strings.TrimSpace(explicitNetwork); name != "" {
		switch name {
		case "host", "none", "bridge", "default":
			return dockerDaemonTopology{}, fmt.Errorf("docker sandbox network %q is not an isolated user-defined network", name)
		}
		if err := networkExists(name); err != nil {
			return dockerDaemonTopology{}, fmt.Errorf("docker sandbox network %q unavailable: %w", name, err)
		}
		if containerized && !containsNetworkName(daemonNetworks, name) {
			return dockerDaemonTopology{}, fmt.Errorf("docker sandbox network %q is not attached to the daemon; attach the daemon to it or enable the docker-compose.sandbox-network.yml overlay", name)
		}
		return dockerDaemonTopology{networkMode: containerapi.NetworkMode(name), containerized: containerized}, nil
	}
	if ambiguous {
		return dockerDaemonTopology{}, fmt.Errorf("docker daemon is attached to multiple non-default networks; set DOCKER_SANDBOX_NETWORK or enable the docker-compose.sandbox-network.yml overlay")
	}
	if detectedNetwork == "" {
		return dockerDaemonTopology{networkMode: containerapi.NetworkMode("default")}, nil
	}
	return dockerDaemonTopology{networkMode: containerapi.NetworkMode(detectedNetwork), containerized: containerized}, nil
}

// containsNetworkName reports whether name is among the non-empty network names.
func containsNetworkName(names []string, name string) bool {
	for _, candidate := range names {
		if strings.TrimSpace(candidate) == name {
			return true
		}
	}
	return false
}

func (r *dockerRuntime) dockerNetworkNameFromSelfContainer(ctx context.Context, dockerClient *client.Client) (string, bool, bool, []string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", false, false, nil, nil
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return "", false, false, nil, nil
	}

	containerInfo, err := dockerClient.ContainerInspect(ctx, hostname)
	if err != nil {
		if isDockerNotFound(err) {
			return "", false, false, nil, nil
		}
		return "", false, false, nil, fmt.Errorf("inspect current docker container %s: %w", hostname, err)
	}
	networkName, ok, ambiguous := selectDockerNetworkName(containerInfo)
	return networkName, ok, ambiguous, dockerNetworkNames(containerInfo), nil
}

// dockerNetworkNames returns the non-empty network names a container is attached
// to, sorted for deterministic comparison.
func dockerNetworkNames(containerInfo containerapi.InspectResponse) []string {
	if containerInfo.NetworkSettings == nil || len(containerInfo.NetworkSettings.Networks) == 0 {
		return nil
	}
	networkNames := make([]string, 0, len(containerInfo.NetworkSettings.Networks))
	for name := range containerInfo.NetworkSettings.Networks {
		name = strings.TrimSpace(name)
		if name != "" {
			networkNames = append(networkNames, name)
		}
	}
	sort.Strings(networkNames)
	return networkNames
}

func selectDockerNetworkName(containerInfo containerapi.InspectResponse) (string, bool, bool) {
	networkNames := dockerNetworkNames(containerInfo)
	if len(networkNames) == 0 {
		return "", false, false
	}
	selected := ""
	userDefined := 0
	for _, name := range networkNames {
		switch name {
		case "bridge", "host", "none":
			continue
		default:
			userDefined++
			if selected == "" {
				selected = name
			}
		}
	}
	if selected == "" {
		// Every network is a Docker built-in (bridge/host/none); keep the
		// historical fallback to the first sorted name so a container on the
		// default bridge still resolves to a usable network.
		return networkNames[0], true, false
	}
	return selected, true, userDefined > 1
}

// dockerSandboxNetworkAttachEnabled reports whether EnsureSandbox should attach
// the container to the resolved network after creating or reusing it. It is
// disabled when an explicit sandbox network is configured: a newly-created
// container is already on that network, and an existing container is reused
// unchanged, so enabling DOCKER_SANDBOX_NETWORK never migrates already-created
// sandboxes.
func dockerSandboxNetworkAttachEnabled(containerized bool, explicitNetwork string) bool {
	return containerized && strings.TrimSpace(explicitNetwork) == ""
}

func ensureDockerContainerNetwork(ctx context.Context, dockerClient *client.Client, containerInfo containerapi.InspectResponse, networkName string) error {
	networkName = strings.TrimSpace(networkName)
	if networkName == "" || networkName == "default" {
		return nil
	}
	if containerInfo.NetworkSettings != nil {
		if _, ok := containerInfo.NetworkSettings.Networks[networkName]; ok {
			return nil
		}
	}
	if err := dockerClient.NetworkConnect(ctx, networkName, containerInfo.ID, nil); err != nil {
		return fmt.Errorf("connect docker container %s to daemon network %s: %w", containerInfo.ID, networkName, err)
	}
	return nil
}
