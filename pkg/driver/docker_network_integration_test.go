package driver

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"agent-compose/pkg/networks"
)

func TestDockerManagedNetworkAttachmentIntegration(t *testing.T) {
	ctx, dockerClient := dockerNetworkIntegrationClient(t)
	projectID := "network-integration-" + strings.ToLower(time.Now().UTC().Format("20060102t150405.000000000"))
	plan, err := networks.BuildPlan(networks.Intent{
		ProjectID: projectID,
		AgentName: "api",
		SandboxID: "sandbox-integration",
		Definitions: map[string]networks.Definition{
			"frontend": {Name: "frontend", Driver: "bridge"},
			"backend":  {Name: "backend", Driver: "bridge"},
		},
		Attachments: []string{"frontend", "backend"},
	}, "bridge")
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	for _, attachment := range plan.Attachments {
		attachment := attachment
		t.Cleanup(func() { _ = dockerClient.NetworkRemove(context.Background(), attachment.RuntimeName) })
	}
	if err := ensureDockerManagedNetworks(ctx, dockerClient, plan); err != nil {
		t.Fatalf("ensureDockerManagedNetworks returned error: %v", err)
	}
	create, err := dockerClient.ContainerCreate(ctx,
		&containerapi.Config{Image: "alpine:3.20", Cmd: []string{"sleep", "30"}},
		&containerapi.HostConfig{NetworkMode: containerapi.NetworkMode(plan.Attachments[0].RuntimeName)},
		dockerContainerNetworkingConfig(plan), nil, "",
	)
	if err != nil {
		t.Fatalf("create integration container: %v", err)
	}
	t.Cleanup(func() {
		_ = dockerClient.ContainerRemove(context.Background(), create.ID, containerapi.RemoveOptions{Force: true})
	})
	inspect, err := dockerClient.ContainerInspect(ctx, create.ID)
	if err != nil {
		t.Fatalf("inspect integration container: %v", err)
	}
	if err := ensureDockerContainerAttachments(ctx, dockerClient, inspect, plan); err != nil {
		t.Fatalf("ensureDockerContainerAttachments returned error: %v", err)
	}
	if err := dockerClient.ContainerStart(ctx, create.ID, containerapi.StartOptions{}); err != nil {
		t.Fatalf("start integration container: %v", err)
	}
	inspect, err = dockerClient.ContainerInspect(ctx, create.ID)
	if err != nil {
		t.Fatalf("inspect started integration container: %v", err)
	}
	if inspect.NetworkSettings == nil || len(inspect.NetworkSettings.Networks) != 2 {
		t.Fatalf("container networks = %#v", inspect.NetworkSettings)
	}
	for _, attachment := range plan.Attachments {
		endpoint := inspect.NetworkSettings.Networks[attachment.RuntimeName]
		if endpoint == nil || endpoint.NetworkID == "" || endpoint.IPAddress == "" {
			t.Fatalf("endpoint %s = %#v", attachment.RuntimeName, endpoint)
		}
	}
	state := dockerSandboxNetworkState(inspect, plan, true)
	if state == nil || len(state.Attachments) != 2 || !state.Attachments[0].Primary || state.Attachments[1].Primary {
		t.Fatalf("network state = %#v", state)
	}
}

func TestDockerManagedNetworkIsolationIntegration(t *testing.T) {
	ctx, dockerClient := dockerNetworkIntegrationClient(t)
	projectID := "network-isolation-" + strings.ToLower(time.Now().UTC().Format("20060102t150405.000000000"))
	definitions := map[string]networks.Definition{
		"frontend": {Name: "frontend", Driver: "bridge"},
		"backend":  {Name: "backend", Driver: "bridge"},
	}
	buildPlan := func(agentName, sandboxID string, attachments ...string) networks.Plan {
		t.Helper()
		plan, err := networks.BuildPlan(networks.Intent{
			ProjectID:   projectID,
			AgentName:   agentName,
			SandboxID:   sandboxID,
			Definitions: definitions,
			Attachments: attachments,
		}, "bridge")
		if err != nil {
			t.Fatalf("BuildPlan(%s) returned error: %v", agentName, err)
		}
		return plan
	}

	apiPlan := buildPlan("api", "sandbox-api", "frontend", "backend")
	frontendPlan := buildPlan("frontend-client", "sandbox-frontend", "frontend")
	backendPlan := buildPlan("backend-client", "sandbox-backend", "backend")
	for _, attachment := range apiPlan.Attachments {
		attachment := attachment
		t.Cleanup(func() { _ = dockerClient.NetworkRemove(context.Background(), attachment.RuntimeName) })
	}
	apiID := createDockerNetworkIntegrationContainer(t, ctx, dockerClient, apiPlan)
	frontendID := createDockerNetworkIntegrationContainer(t, ctx, dockerClient, frontendPlan)
	backendID := createDockerNetworkIntegrationContainer(t, ctx, dockerClient, backendPlan)

	assertDockerExecResult(t, ctx, dockerClient, frontendID, true, "ping", "-c", "1", "-W", "2", "api")
	assertDockerExecResult(t, ctx, dockerClient, backendID, true, "ping", "-c", "1", "-W", "2", "api")
	assertDockerExecResult(t, ctx, dockerClient, apiID, true, "ping", "-c", "1", "-W", "2", "frontend-client")
	assertDockerExecResult(t, ctx, dockerClient, apiID, true, "ping", "-c", "1", "-W", "2", "backend-client")
	assertDockerExecResult(t, ctx, dockerClient, frontendID, false, "ping", "-c", "1", "-W", "2", "backend-client")
	assertDockerExecResult(t, ctx, dockerClient, backendID, false, "ping", "-c", "1", "-W", "2", "frontend-client")
}

func TestDockerManagedNetworkReconcileAfterRecreationIntegration(t *testing.T) {
	ctx, dockerClient := dockerNetworkIntegrationClient(t)
	projectID := "network-reconcile-" + strings.ToLower(time.Now().UTC().Format("20060102t150405.000000000"))
	plan, err := networks.BuildPlan(networks.Intent{
		ProjectID: projectID,
		AgentName: "api",
		SandboxID: "sandbox-reconcile",
		Definitions: map[string]networks.Definition{
			"frontend": {Name: "frontend", Driver: "bridge"},
			"backend":  {Name: "backend", Driver: "bridge"},
		},
		Attachments: []string{"frontend", "backend"},
	}, "bridge")
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	for _, attachment := range plan.Attachments {
		attachment := attachment
		t.Cleanup(func() { _ = dockerClient.NetworkRemove(context.Background(), attachment.RuntimeName) })
	}
	containerID := createDockerNetworkIntegrationContainer(t, ctx, dockerClient, plan)
	stopTimeout := 1
	if err := dockerClient.ContainerStop(ctx, containerID, containerapi.StopOptions{Timeout: &stopTimeout}); err != nil {
		t.Fatalf("stop integration container: %v", err)
	}
	for _, attachment := range plan.Attachments {
		if err := dockerClient.NetworkDisconnect(ctx, attachment.RuntimeName, containerID, false); err != nil {
			t.Fatalf("disconnect integration container from %s: %v", attachment.RuntimeName, err)
		}
		if err := dockerClient.NetworkRemove(ctx, attachment.RuntimeName); err != nil {
			t.Fatalf("remove integration network %s: %v", attachment.RuntimeName, err)
		}
	}
	if err := ensureDockerManagedNetworks(ctx, dockerClient, plan); err != nil {
		t.Fatalf("recreate managed networks: %v", err)
	}
	containerInfo, err := dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect stopped integration container: %v", err)
	}
	if err := ensureDockerContainerAttachments(ctx, dockerClient, containerInfo, plan); err != nil {
		t.Fatalf("reconcile recreated network attachments: %v", err)
	}
	if err := dockerClient.ContainerStart(ctx, containerID, containerapi.StartOptions{}); err != nil {
		t.Fatalf("restart integration container: %v", err)
	}
	containerInfo, err = dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect restarted integration container: %v", err)
	}
	for _, attachment := range plan.Attachments {
		endpoint := containerInfo.NetworkSettings.Networks[attachment.RuntimeName]
		if endpoint == nil || endpoint.NetworkID == "" || endpoint.IPAddress == "" {
			t.Fatalf("reconciled endpoint %s = %#v", attachment.RuntimeName, endpoint)
		}
	}
}

func TestDockerManagedNetworkReconcileRejectsUnexpectedManagedAttachmentIntegration(t *testing.T) {
	ctx, dockerClient := dockerNetworkIntegrationClient(t)
	projectID := "network-drift-" + strings.ToLower(time.Now().UTC().Format("20060102t150405.000000000"))
	definitions := map[string]networks.Definition{
		"frontend": {Name: "frontend", Driver: "bridge"},
		"backend":  {Name: "backend", Driver: "bridge"},
	}
	frontendPlan, err := networks.BuildPlan(networks.Intent{
		ProjectID: projectID, AgentName: "api", SandboxID: "sandbox-drift",
		Definitions: definitions, Attachments: []string{"frontend"},
	}, "bridge")
	if err != nil {
		t.Fatalf("BuildPlan frontend returned error: %v", err)
	}
	backendPlan, err := networks.BuildPlan(networks.Intent{
		ProjectID: projectID, AgentName: "worker", SandboxID: "sandbox-worker",
		Definitions: definitions, Attachments: []string{"backend"},
	}, "bridge")
	if err != nil {
		t.Fatalf("BuildPlan backend returned error: %v", err)
	}
	for _, plan := range []networks.Plan{frontendPlan, backendPlan} {
		for _, attachment := range plan.Attachments {
			attachment := attachment
			t.Cleanup(func() { _ = dockerClient.NetworkRemove(context.Background(), attachment.RuntimeName) })
		}
	}
	containerID := createDockerNetworkIntegrationContainer(t, ctx, dockerClient, frontendPlan)
	if err := ensureDockerManagedNetworks(ctx, dockerClient, backendPlan); err != nil {
		t.Fatalf("ensure backend network: %v", err)
	}
	if err := dockerClient.NetworkConnect(ctx, backendPlan.Attachments[0].RuntimeName, containerID, nil); err != nil {
		t.Fatalf("attach unexpected managed network: %v", err)
	}
	containerInfo, err := dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect drifted integration container: %v", err)
	}
	if err := ensureDockerContainerAttachments(ctx, dockerClient, containerInfo, frontendPlan); err == nil || !strings.Contains(err.Error(), "unexpected managed project network") {
		t.Fatalf("reconcile drift error = %v", err)
	}
}

func TestDockerBaselinePortBindingIntegration(t *testing.T) {
	ctx, dockerClient := dockerNetworkIntegrationClient(t)
	plan, err := networks.BuildPlan(networks.Intent{}, "bridge")
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	intent := &SandboxNetworkIntent{
		Expose: []string{"9090/tcp"},
		Ports:  []string{"127.0.0.1:0:8080/tcp"},
	}
	exposed, bindings, err := mergeDockerNetworkPortConfig(nil, nil, intent)
	if err != nil {
		t.Fatalf("mergeDockerNetworkPortConfig returned error: %v", err)
	}
	created, err := dockerClient.ContainerCreate(ctx,
		&containerapi.Config{Image: "alpine:3.20", Cmd: []string{"sleep", "30"}, ExposedPorts: exposed},
		&containerapi.HostConfig{NetworkMode: "bridge", PortBindings: bindings},
		nil, nil, "",
	)
	if err != nil {
		t.Fatalf("create baseline port integration container: %v", err)
	}
	t.Cleanup(func() {
		_ = dockerClient.ContainerRemove(context.Background(), created.ID, containerapi.RemoveOptions{Force: true})
	})
	if err := dockerClient.ContainerStart(ctx, created.ID, containerapi.StartOptions{}); err != nil {
		t.Fatalf("start baseline port integration container: %v", err)
	}
	containerInfo, err := dockerClient.ContainerInspect(ctx, created.ID)
	if err != nil {
		t.Fatalf("inspect baseline port integration container: %v", err)
	}
	if _, ok := containerInfo.Config.ExposedPorts["9090/tcp"]; !ok {
		t.Fatalf("exposed ports = %#v", containerInfo.Config.ExposedPorts)
	}
	state := dockerSandboxNetworkState(containerInfo, plan, true)
	if state == nil || state.Mode != string(networks.ModeSingleBridge) || len(state.PortBindings) != 1 {
		t.Fatalf("baseline network state = %#v", state)
	}
	binding := state.PortBindings[0]
	if binding.ContainerPort != "8080/tcp" || binding.HostIP != "127.0.0.1" || binding.HostPort == "" {
		t.Fatalf("dynamic port binding = %#v", binding)
	}
}

func dockerNetworkIntegrationClient(t *testing.T) (context.Context, *client.Client) {
	t.Helper()
	if os.Getenv("AGENT_COMPOSE_DOCKER_NETWORK_TEST") != "1" {
		t.Skip("set AGENT_COMPOSE_DOCKER_NETWORK_TEST=1 to run Docker network integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("connect Docker Engine: %v", err)
	}
	t.Cleanup(func() { _ = dockerClient.Close() })
	if _, err := dockerClient.Ping(ctx); err != nil {
		t.Fatalf("ping Docker Engine: %v", err)
	}
	return ctx, dockerClient
}

func createDockerNetworkIntegrationContainer(t *testing.T, ctx context.Context, dockerClient *client.Client, plan networks.Plan) string {
	t.Helper()
	if err := ensureDockerManagedNetworks(ctx, dockerClient, plan); err != nil {
		t.Fatalf("ensureDockerManagedNetworks returned error: %v", err)
	}
	create, err := dockerClient.ContainerCreate(ctx,
		&containerapi.Config{Image: "alpine:3.20", Cmd: []string{"sleep", "30"}},
		&containerapi.HostConfig{NetworkMode: containerapi.NetworkMode(plan.Attachments[0].RuntimeName)},
		dockerContainerNetworkingConfig(plan), nil, "",
	)
	if err != nil {
		t.Fatalf("create integration container: %v", err)
	}
	t.Cleanup(func() {
		_ = dockerClient.ContainerRemove(context.Background(), create.ID, containerapi.RemoveOptions{Force: true})
	})
	inspect, err := dockerClient.ContainerInspect(ctx, create.ID)
	if err != nil {
		t.Fatalf("inspect integration container: %v", err)
	}
	if err := ensureDockerContainerAttachments(ctx, dockerClient, inspect, plan); err != nil {
		t.Fatalf("ensureDockerContainerAttachments returned error: %v", err)
	}
	if err := dockerClient.ContainerStart(ctx, create.ID, containerapi.StartOptions{}); err != nil {
		t.Fatalf("start integration container: %v", err)
	}
	return create.ID
}

func assertDockerExecResult(t *testing.T, ctx context.Context, dockerClient *client.Client, containerID string, wantSuccess bool, command ...string) {
	t.Helper()
	execResponse, err := dockerClient.ContainerExecCreate(ctx, containerID, containerapi.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          command,
	})
	if err != nil {
		t.Fatalf("create Docker exec %q: %v", command, err)
	}
	attach, err := dockerClient.ContainerExecAttach(ctx, execResponse.ID, containerapi.ExecAttachOptions{})
	if err != nil {
		t.Fatalf("attach Docker exec %q: %v", command, err)
	}
	defer attach.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		t.Fatalf("read Docker exec %q: %v", command, err)
	}
	inspect, err := dockerClient.ContainerExecInspect(ctx, execResponse.ID)
	if err != nil {
		t.Fatalf("inspect Docker exec %q: %v", command, err)
	}
	succeeded := inspect.ExitCode == 0
	if succeeded != wantSuccess {
		t.Fatalf("Docker exec %q exit code = %d, stdout = %q, stderr = %q, want success = %t", command, inspect.ExitCode, stdout.String(), stderr.String(), wantSuccess)
	}
}
