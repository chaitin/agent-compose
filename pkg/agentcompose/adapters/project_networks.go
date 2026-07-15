package adapters

import (
	"context"
	"fmt"

	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"agent-compose/pkg/networks"
)

const (
	dockerSandboxProjectLabel = "agent-compose.project_id"
	dockerSandboxDriverLabel  = "agent-compose.driver"
)

type dockerProjectNetworkClient interface {
	NetworkList(context.Context, networkapi.ListOptions) ([]networkapi.Summary, error)
	NetworkInspect(context.Context, string, networkapi.InspectOptions) (networkapi.Inspect, error)
	ContainerInspect(context.Context, string) (containerapi.InspectResponse, error)
	NetworkDisconnect(context.Context, string, string, bool) error
	NetworkRemove(context.Context, string) error
	Close() error
}

type DockerProjectNetworkCleaner struct {
	newClient func() (dockerProjectNetworkClient, error)
}

func NewDockerProjectNetworkCleaner() *DockerProjectNetworkCleaner {
	return &DockerProjectNetworkCleaner{newClient: func() (dockerProjectNetworkClient, error) {
		return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	}}
}

func (c *DockerProjectNetworkCleaner) CleanupProjectNetworks(ctx context.Context, projectID string) error {
	if c == nil || c.newClient == nil {
		return fmt.Errorf("docker project network cleaner is not configured")
	}
	dockerClient, err := c.newClient()
	if err != nil {
		return fmt.Errorf("connect Docker Engine for project network cleanup: %w", err)
	}
	defer func() { _ = dockerClient.Close() }()
	filter := filters.NewArgs(
		filters.Arg("label", networks.ManagedLabel+"=true"),
		filters.Arg("label", networks.ResourceLabel+"="+networks.ProjectNetworkResource),
		filters.Arg("label", networks.ProjectIDLabel+"="+projectID),
	)
	summaries, err := dockerClient.NetworkList(ctx, networkapi.ListOptions{Filters: filter})
	if err != nil {
		return fmt.Errorf("list Docker networks for project %s: %w", projectID, err)
	}
	for _, summary := range summaries {
		inspect, err := dockerClient.NetworkInspect(ctx, summary.ID, networkapi.InspectOptions{})
		if err != nil {
			return fmt.Errorf("inspect Docker network %s: %w", summary.ID, err)
		}
		if inspect.Labels[networks.ManagedLabel] != "true" || inspect.Labels[networks.ResourceLabel] != networks.ProjectNetworkResource || inspect.Labels[networks.ProjectIDLabel] != projectID {
			return fmt.Errorf("refuse to clean Docker network %s with mismatched ownership", inspect.Name)
		}
		for containerID := range inspect.Containers {
			containerInfo, err := dockerClient.ContainerInspect(ctx, containerID)
			if err != nil {
				return fmt.Errorf("inspect endpoint container %s on network %s: %w", containerID, inspect.Name, err)
			}
			labels := map[string]string(nil)
			if containerInfo.Config != nil {
				labels = containerInfo.Config.Labels
			}
			if labels[dockerSandboxProjectLabel] != projectID || labels[dockerSandboxDriverLabel] != "docker" {
				return fmt.Errorf("refuse to disconnect unknown endpoint container %s from project network %s", containerID, inspect.Name)
			}
			if err := dockerClient.NetworkDisconnect(ctx, inspect.ID, containerID, false); err != nil {
				return fmt.Errorf("disconnect container %s from project network %s: %w", containerID, inspect.Name, err)
			}
		}
		if err := dockerClient.NetworkRemove(ctx, inspect.ID); err != nil {
			return fmt.Errorf("remove project network %s: %w", inspect.Name, err)
		}
	}
	return nil
}
