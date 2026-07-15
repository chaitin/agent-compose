package driver

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	containerapi "github.com/docker/docker/api/types/container"
	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"agent-compose/pkg/networks"
)

func (r *dockerRuntime) networkPlanForSandbox(sandbox *Sandbox, topology dockerDaemonTopology, dockerClient *client.Client) (networks.Plan, error) {
	baseline := strings.TrimSpace(string(topology.networkMode))
	if sandbox == nil || sandbox.NetworkIntent == nil {
		return networks.BuildPlan(networks.Intent{}, baseline)
	}
	intent := sandbox.NetworkIntent
	definitions := make(map[string]networks.Definition, len(intent.Definitions))
	for _, definition := range intent.Definitions {
		definitions[definition.Name] = networks.Definition{Name: definition.Name, Driver: definition.Driver}
	}
	plan, err := networks.BuildPlan(networks.Intent{
		ProjectID:   intent.ProjectID,
		AgentName:   intent.AgentName,
		SandboxID:   sandbox.Summary.ID,
		Definitions: definitions,
		Attachments: intent.Attachments,
	}, baseline)
	if err != nil {
		return networks.Plan{}, fmt.Errorf("plan docker networks for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	if plan.Mode != networks.ModeMultiNetwork {
		return plan, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(dockerClient.DaemonHost()), "unix://") {
		return networks.Plan{}, fmt.Errorf("project named networks require a local Docker Engine, got %s", dockerClient.DaemonHost())
	}
	switch topology.kind {
	case dockerDaemonNative, dockerDaemonContainerHost:
		return plan, nil
	case dockerDaemonContainerBridge:
		return networks.Plan{}, fmt.Errorf("project named networks add bridges beyond baseline %q; the agent-compose daemon must run natively or with Docker host networking", baseline)
	default:
		detail := strings.TrimSpace(topology.detail)
		if detail == "" {
			detail = string(topology.kind)
		}
		return networks.Plan{}, fmt.Errorf("cannot verify daemon topology for project named networks: %s", detail)
	}
}

func ensureDockerManagedNetworks(ctx context.Context, dockerClient *client.Client, plan networks.Plan) error {
	if plan.Mode != networks.ModeMultiNetwork {
		return nil
	}
	for _, attachment := range plan.Attachments {
		if !attachment.Managed {
			continue
		}
		if err := ensureDockerManagedNetwork(ctx, dockerClient, attachment); err != nil {
			return err
		}
	}
	return nil
}

func ensureDockerManagedNetwork(ctx context.Context, dockerClient *client.Client, attachment networks.Attachment) error {
	inspect, err := dockerClient.NetworkInspect(ctx, attachment.RuntimeName, networkapi.InspectOptions{})
	if err == nil {
		return validateDockerManagedNetwork(inspect, attachment)
	}
	if !isDockerNotFound(err) {
		return fmt.Errorf("inspect docker network %s: %w", attachment.RuntimeName, err)
	}
	enableIPv4 := true
	enableIPv6 := false
	_, createErr := dockerClient.NetworkCreate(ctx, attachment.RuntimeName, networkapi.CreateOptions{
		Driver:     "bridge",
		EnableIPv4: &enableIPv4,
		EnableIPv6: &enableIPv6,
		Internal:   false,
		Attachable: false,
		Labels:     cloneStringMap(attachment.Labels),
	})
	if createErr != nil {
		inspect, err = dockerClient.NetworkInspect(ctx, attachment.RuntimeName, networkapi.InspectOptions{})
		if err != nil {
			return fmt.Errorf("create docker network %s: %w", attachment.RuntimeName, createErr)
		}
		return validateDockerManagedNetwork(inspect, attachment)
	}
	inspect, err = dockerClient.NetworkInspect(ctx, attachment.RuntimeName, networkapi.InspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect created docker network %s: %w", attachment.RuntimeName, err)
	}
	return validateDockerManagedNetwork(inspect, attachment)
}

func validateDockerManagedNetwork(inspect networkapi.Inspect, attachment networks.Attachment) error {
	if inspect.Driver != "bridge" || inspect.Internal || inspect.Scope != "local" || !inspect.EnableIPv4 || inspect.EnableIPv6 || inspect.Attachable || inspect.Ingress || inspect.ConfigOnly {
		return fmt.Errorf("docker network %s does not match managed bridge properties", attachment.RuntimeName)
	}
	for key, expected := range attachment.Labels {
		if inspect.Labels[key] != expected {
			return fmt.Errorf("docker network %s ownership label %s does not match", attachment.RuntimeName, key)
		}
	}
	return nil
}

func dockerContainerNetworkingConfig(plan networks.Plan) *networkapi.NetworkingConfig {
	if plan.Mode != networks.ModeMultiNetwork || len(plan.Attachments) == 0 {
		return nil
	}
	primary := plan.Attachments[0]
	return &networkapi.NetworkingConfig{EndpointsConfig: map[string]*networkapi.EndpointSettings{
		primary.RuntimeName: {
			Aliases:    append([]string(nil), primary.Aliases...),
			GwPriority: primary.GatewayPriority,
		},
	}}
}

func ensureDockerContainerAttachments(ctx context.Context, dockerClient *client.Client, containerInfo containerapi.InspectResponse, plan networks.Plan) error {
	if plan.Mode != networks.ModeMultiNetwork {
		return nil
	}
	existing := map[string]*networkapi.EndpointSettings{}
	if containerInfo.NetworkSettings != nil {
		for name, endpoint := range containerInfo.NetworkSettings.Networks {
			existing[name] = endpoint
		}
	}
	expected := make(map[string]struct{}, len(plan.Attachments))
	for _, attachment := range plan.Attachments {
		expected[attachment.RuntimeName] = struct{}{}
	}
	for networkName, endpoint := range existing {
		if _, ok := expected[networkName]; ok {
			continue
		}
		lookup := networkName
		if endpoint != nil && endpoint.NetworkID != "" {
			lookup = endpoint.NetworkID
		}
		networkInfo, err := dockerClient.NetworkInspect(ctx, lookup, networkapi.InspectOptions{})
		if isDockerNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect unexpected docker network %s on container %s: %w", networkName, containerInfo.ID, err)
		}
		if networkInfo.Labels[networks.ManagedLabel] == "true" && networkInfo.Labels[networks.ResourceLabel] == networks.ProjectNetworkResource {
			return fmt.Errorf("container %s has unexpected managed project network %s", containerInfo.ID, networkName)
		}
	}
	for _, attachment := range plan.Attachments {
		if endpoint := existing[attachment.RuntimeName]; endpoint != nil && endpoint.NetworkID != "" {
			networkInfo, err := dockerClient.NetworkInspect(ctx, attachment.RuntimeName, networkapi.InspectOptions{})
			if err != nil {
				return fmt.Errorf("inspect project network %s for container %s: %w", attachment.RuntimeName, containerInfo.ID, err)
			}
			if endpoint.NetworkID == networkInfo.ID {
				continue
			}
		}
		if err := dockerClient.NetworkConnect(ctx, attachment.RuntimeName, containerInfo.ID, &networkapi.EndpointSettings{
			Aliases:    append([]string(nil), attachment.Aliases...),
			GwPriority: attachment.GatewayPriority,
		}); err != nil {
			return fmt.Errorf("connect docker container %s to project network %s: %w", containerInfo.ID, attachment.RuntimeName, err)
		}
	}
	return nil
}

func mergeDockerNetworkPortConfig(exposed nat.PortSet, bindings nat.PortMap, intent *SandboxNetworkIntent) (nat.PortSet, nat.PortMap, error) {
	if intent == nil || (len(intent.Expose) == 0 && len(intent.Ports) == 0) {
		return exposed, bindings, nil
	}
	if exposed == nil {
		exposed = nat.PortSet{}
	}
	if bindings == nil {
		bindings = nat.PortMap{}
	}
	for _, raw := range intent.Expose {
		port, err := parseCanonicalContainerPort(raw)
		if err != nil {
			return nil, nil, err
		}
		exposed[port] = struct{}{}
	}
	for _, raw := range intent.Ports {
		parts := strings.Split(raw, ":")
		if len(parts) != 3 {
			return nil, nil, fmt.Errorf("invalid canonical published port %q", raw)
		}
		hostPort, err := strconv.Atoi(parts[1])
		if err != nil || hostPort < 0 || hostPort > 65535 {
			return nil, nil, fmt.Errorf("invalid canonical host port %q", parts[1])
		}
		port, err := parseCanonicalContainerPort(parts[2])
		if err != nil {
			return nil, nil, err
		}
		exposed[port] = struct{}{}
		hostPortValue := ""
		if hostPort > 0 {
			hostPortValue = strconv.Itoa(hostPort)
		}
		bindings[port] = append(bindings[port], nat.PortBinding{HostIP: parts[0], HostPort: hostPortValue})
	}
	return exposed, bindings, nil
}

func parseCanonicalContainerPort(raw string) (nat.Port, error) {
	portRaw, protocol, ok := strings.Cut(strings.TrimSpace(raw), "/")
	if !ok || protocol != "tcp" {
		return "", fmt.Errorf("invalid canonical container port %q", raw)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid canonical container port %q", raw)
	}
	return nat.NewPort(protocol, portRaw)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func dockerSandboxNetworkState(containerInfo containerapi.InspectResponse, plan networks.Plan, configured bool) *SandboxNetworkState {
	if !configured {
		return nil
	}
	state := &SandboxNetworkState{Mode: string(plan.Mode), ReconciledAt: time.Now().UTC()}
	for index, attachment := range plan.Attachments {
		item := SandboxNetworkAttachmentState{
			LogicalName: attachment.LogicalName,
			RuntimeName: attachment.RuntimeName,
			Aliases:     append([]string(nil), attachment.Aliases...),
			Primary:     index == 0,
		}
		if containerInfo.NetworkSettings != nil {
			if endpoint := containerInfo.NetworkSettings.Networks[attachment.RuntimeName]; endpoint != nil {
				item.NetworkID = endpoint.NetworkID
				item.IPv4Address = endpoint.IPAddress
			}
		}
		state.Attachments = append(state.Attachments, item)
	}
	if containerInfo.NetworkSettings == nil {
		return state
	}
	ports := make([]string, 0, len(containerInfo.NetworkSettings.Ports))
	for port := range containerInfo.NetworkSettings.Ports {
		ports = append(ports, string(port))
	}
	sort.Strings(ports)
	for _, rawPort := range ports {
		port := nat.Port(rawPort)
		for _, binding := range containerInfo.NetworkSettings.Ports[port] {
			state.PortBindings = append(state.PortBindings, SandboxPortBindingState{
				ContainerPort: rawPort,
				HostIP:        binding.HostIP,
				HostPort:      binding.HostPort,
			})
		}
	}
	return state
}
