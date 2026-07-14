package adapters

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	containerapi "github.com/docker/docker/api/types/container"
	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

func TestDockerPublishAddressProviderDefaultPublishAddress(t *testing.T) {
	fake := &fakeDockerNetworkAPI{network: networkapi.Inspect{
		Name: "bridge",
		IPAM: networkapi.IPAM{Config: []networkapi.IPAMConfig{
			{Subnet: "172.17.0.0/16", Gateway: "172.17.0.1"},
			{Subnet: "fd00::/64", Gateway: "fd00::1"},
		}},
	}}
	provider := &DockerPublishAddressProvider{client: func() (dockerNetworkAPI, error) { return fake, nil }}
	got, err := provider.DefaultPublishAddress(context.Background())
	if err != nil || got != "172.17.0.1" {
		t.Fatalf("DefaultPublishAddress() = %q, %v", got, err)
	}
}

func TestDockerPublishAddressProviderRejectsMissingIPv4Gateway(t *testing.T) {
	fake := &fakeDockerNetworkAPI{network: networkapi.Inspect{
		Name: "bridge",
		IPAM: networkapi.IPAM{Config: []networkapi.IPAMConfig{{Subnet: "fd00::/64", Gateway: "fd00::1"}}},
	}}
	provider := &DockerPublishAddressProvider{client: func() (dockerNetworkAPI, error) { return fake, nil }}
	_, err := provider.DefaultPublishAddress(context.Background())
	if err == nil || !strings.Contains(err.Error(), "has no IPv4 gateway") {
		t.Fatalf("DefaultPublishAddress() error = %v", err)
	}
}

func TestIntegrationDockerPublishAddressProviderDataPlane(t *testing.T) {
	if os.Getenv("AGENT_COMPOSE_DOCKER_NETWORK_INTEGRATION") != "1" {
		t.Skip("set AGENT_COMPOSE_DOCKER_NETWORK_INTEGRATION=1 to run the Docker network data-plane test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	provider := NewDockerPublishAddressProvider()
	publishAddress, err := provider.DefaultPublishAddress(ctx)
	if err != nil {
		t.Fatalf("DefaultPublishAddress() error = %v", err)
	}
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dockerClient.Close() }()

	guestPort := nat.Port("80/tcp")
	target, err := dockerClient.ContainerCreate(ctx,
		&containerapi.Config{Image: "nginx:alpine", ExposedPorts: nat.PortSet{guestPort: struct{}{}}},
		&containerapi.HostConfig{
			NetworkMode:  "bridge",
			PortBindings: nat.PortMap{guestPort: []nat.PortBinding{{HostIP: publishAddress, HostPort: ""}}},
		}, nil, nil, "",
	)
	if err != nil {
		t.Fatalf("create target container: %v", err)
	}
	t.Cleanup(func() {
		_ = dockerClient.ContainerRemove(context.Background(), target.ID, containerapi.RemoveOptions{Force: true})
	})
	if err := dockerClient.ContainerStart(ctx, target.ID, containerapi.StartOptions{}); err != nil {
		t.Fatalf("start target container: %v", err)
	}
	inspected, err := dockerClient.ContainerInspect(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	published := inspected.NetworkSettings.Ports[guestPort]
	if len(published) == 0 || strings.TrimSpace(published[0].HostPort) == "" {
		t.Fatalf("target published ports = %#v", published)
	}
	url := fmt.Sprintf("http://%s:%s", publishAddress, published[0].HostPort)
	source, err := dockerClient.ContainerCreate(ctx,
		&containerapi.Config{
			Image: "curlimages/curl:latest", Tty: true,
			Cmd: []string{"-fsS", "--retry", "10", "--retry-connrefused", "--retry-delay", "1", url},
		},
		&containerapi.HostConfig{NetworkMode: "bridge"}, nil, nil, "",
	)
	if err != nil {
		t.Fatalf("create source container: %v", err)
	}
	t.Cleanup(func() {
		_ = dockerClient.ContainerRemove(context.Background(), source.ID, containerapi.RemoveOptions{Force: true})
	})
	if err := dockerClient.ContainerStart(ctx, source.ID, containerapi.StartOptions{}); err != nil {
		t.Fatalf("start source container: %v", err)
	}
	wait, waitErr := dockerClient.ContainerWait(ctx, source.ID, containerapi.WaitConditionNotRunning)
	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatal(err)
		}
	case result := <-wait:
		if result.StatusCode != 0 {
			t.Fatalf("source exit code = %d", result.StatusCode)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	logs, err := dockerClient.ContainerLogs(ctx, source.ID, containerapi.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logs.Close() }()
	output, err := io.ReadAll(logs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "Welcome to nginx") {
		t.Fatalf("source output does not contain nginx response: %q", output)
	}
}

type fakeDockerNetworkAPI struct {
	network networkapi.Inspect
}

func (f *fakeDockerNetworkAPI) NetworkInspect(context.Context, string, networkapi.InspectOptions) (networkapi.Inspect, error) {
	return f.network, nil
}

func (f *fakeDockerNetworkAPI) Close() error { return nil }
