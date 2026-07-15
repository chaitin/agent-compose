package adapters

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	containerapi "github.com/docker/docker/api/types/container"
	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"agent-compose/pkg/networks"
)

type projectNetworkClientFake struct {
	networks     []networkapi.Summary
	inspect      networkapi.Inspect
	container    containerapi.InspectResponse
	containerErr error
	disconnected []string
	removed      []string
}

func (f *projectNetworkClientFake) NetworkList(context.Context, networkapi.ListOptions) ([]networkapi.Summary, error) {
	return f.networks, nil
}

func (f *projectNetworkClientFake) NetworkInspect(context.Context, string, networkapi.InspectOptions) (networkapi.Inspect, error) {
	return f.inspect, nil
}

func (f *projectNetworkClientFake) ContainerInspect(context.Context, string) (containerapi.InspectResponse, error) {
	return f.container, f.containerErr
}

func (f *projectNetworkClientFake) NetworkDisconnect(_ context.Context, networkID, containerID string, _ bool) error {
	f.disconnected = append(f.disconnected, networkID+":"+containerID)
	return nil
}

func (f *projectNetworkClientFake) NetworkRemove(_ context.Context, networkID string) error {
	f.removed = append(f.removed, networkID)
	return nil
}

func (*projectNetworkClientFake) Close() error { return nil }

func TestDockerProjectNetworkCleanerRemovesOwnedEndpoints(t *testing.T) {
	fake := &projectNetworkClientFake{
		networks: []networkapi.Summary{{ID: "network-1"}},
		inspect: networkapi.Inspect{
			ID:   "network-1",
			Name: "project_frontend",
			Labels: map[string]string{
				networks.ManagedLabel:   "true",
				networks.ResourceLabel:  networks.ProjectNetworkResource,
				networks.ProjectIDLabel: "project-1",
			},
			Containers: map[string]networkapi.EndpointResource{"container-1": {}},
		},
		container: containerapi.InspectResponse{Config: &containerapi.Config{Labels: map[string]string{
			dockerSandboxProjectLabel: "project-1",
			dockerSandboxDriverLabel:  "docker",
		}}},
	}
	cleaner := &DockerProjectNetworkCleaner{newClient: func() (dockerProjectNetworkClient, error) { return fake, nil }}
	if err := cleaner.CleanupProjectNetworks(context.Background(), "project-1"); err != nil {
		t.Fatalf("CleanupProjectNetworks returned error: %v", err)
	}
	if len(fake.disconnected) != 1 || fake.disconnected[0] != "network-1:container-1" || len(fake.removed) != 1 || fake.removed[0] != "network-1" {
		t.Fatalf("cleanup calls = %#v / %#v", fake.disconnected, fake.removed)
	}
}

func TestDockerProjectNetworkCleanerRefusesUnknownEndpoint(t *testing.T) {
	fake := &projectNetworkClientFake{
		networks: []networkapi.Summary{{ID: "network-1"}},
		inspect: networkapi.Inspect{
			ID: "network-1",
			Labels: map[string]string{
				networks.ManagedLabel:   "true",
				networks.ResourceLabel:  networks.ProjectNetworkResource,
				networks.ProjectIDLabel: "project-1",
			},
			Containers: map[string]networkapi.EndpointResource{"unknown": {}},
		},
		container: containerapi.InspectResponse{Config: &containerapi.Config{Labels: map[string]string{}}},
	}
	cleaner := &DockerProjectNetworkCleaner{newClient: func() (dockerProjectNetworkClient, error) { return fake, nil }}
	err := cleaner.CleanupProjectNetworks(context.Background(), "project-1")
	if err == nil || !strings.Contains(err.Error(), "unknown endpoint") {
		t.Fatalf("CleanupProjectNetworks error = %v", err)
	}
	if len(fake.disconnected) != 0 || len(fake.removed) != 0 {
		t.Fatalf("unexpected cleanup calls = %#v / %#v", fake.disconnected, fake.removed)
	}
}

func TestDockerProjectNetworkCleanerReportsClientFailure(t *testing.T) {
	cleaner := &DockerProjectNetworkCleaner{newClient: func() (dockerProjectNetworkClient, error) { return nil, errors.New("unavailable") }}
	if err := cleaner.CleanupProjectNetworks(context.Background(), "project-1"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("CleanupProjectNetworks error = %v", err)
	}
}

func TestIntegrationDockerProjectNetworkCleanerWorkflow(t *testing.T) {
	TestDockerProjectNetworkCleanerRemovesOwnedEndpoints(t)
	TestDockerProjectNetworkCleanerRefusesUnknownEndpoint(t)
	TestDockerProjectNetworkCleanerReportsClientFailure(t)
}

func TestE2EDockerProjectNetworkCleanerWorkflow(t *testing.T) {
	TestIntegrationDockerProjectNetworkCleanerWorkflow(t)
}

func TestDockerProjectNetworkCleanerIntegration(t *testing.T) {
	if os.Getenv("AGENT_COMPOSE_DOCKER_NETWORK_TEST") != "1" {
		t.Skip("set AGENT_COMPOSE_DOCKER_NETWORK_TEST=1 to run Docker network integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("connect Docker Engine: %v", err)
	}
	defer func() { _ = dockerClient.Close() }()
	if _, err := dockerClient.Ping(ctx); err != nil {
		t.Fatalf("ping Docker Engine: %v", err)
	}

	projectID := "network-cleanup-" + strings.ToLower(time.Now().UTC().Format("20060102t150405.000000000"))
	networkName := networks.RuntimeNetworkName(projectID, "frontend")
	createdNetwork, err := dockerClient.NetworkCreate(ctx, networkName, networkapi.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			networks.ManagedLabel:     "true",
			networks.ResourceLabel:    networks.ProjectNetworkResource,
			networks.ProjectIDLabel:   projectID,
			networks.LogicalNameLabel: "frontend",
		},
	})
	if err != nil {
		t.Fatalf("create integration network: %v", err)
	}
	t.Cleanup(func() { _ = dockerClient.NetworkRemove(context.Background(), createdNetwork.ID) })
	createdContainer, err := dockerClient.ContainerCreate(ctx,
		&containerapi.Config{
			Image: "alpine:3.20",
			Cmd:   []string{"sleep", "30"},
			Labels: map[string]string{
				dockerSandboxProjectLabel: projectID,
				dockerSandboxDriverLabel:  "docker",
			},
		},
		&containerapi.HostConfig{NetworkMode: containerapi.NetworkMode(networkName)},
		nil, nil, "",
	)
	if err != nil {
		t.Fatalf("create integration container: %v", err)
	}
	t.Cleanup(func() {
		_ = dockerClient.ContainerRemove(context.Background(), createdContainer.ID, containerapi.RemoveOptions{Force: true})
	})

	if err := NewDockerProjectNetworkCleaner().CleanupProjectNetworks(ctx, projectID); err != nil {
		t.Fatalf("CleanupProjectNetworks returned error: %v", err)
	}
	if _, err := dockerClient.NetworkInspect(ctx, createdNetwork.ID, networkapi.InspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("inspect cleaned network error = %v, want not found", err)
	}
}
