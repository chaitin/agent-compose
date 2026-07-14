package networks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

const (
	DeploymentNative          = "native"
	DeploymentContainerHost   = "container_host"
	DeploymentContainerBridge = "container_bridge"

	VisibilityInternal = "internal"
	VisibilityExternal = "external"

	PublisherDocker = "docker"
	PublisherDirect = "direct"

	IsolationNotApplicable = "not_applicable"
	IsolationUnprotected   = "unprotected"
	IsolationEnforced      = "enforced"

	DefaultServiceCIDR = "10.254.0.0/16"
)

var ErrUnsupported = errors.New("network capability is unsupported")

type Infrastructure interface {
	Deployment(context.Context) (string, error)
	EnsureNetwork(context.Context, NetworkRequest) (NetworkAccess, error)
}

type PortAllocator interface {
	AllocateHostPort(context.Context, string) (int, error)
}

type IsolationPolicy interface {
	Evaluate(context.Context, *domain.Sandbox, *domain.SandboxNetworkState) (string, error)
}

type IsolationPreflight interface {
	Validate(context.Context, *domain.Sandbox) error
}

type NetworkRequest struct {
	ProjectID   string
	ProjectName string
	NetworkName string
	ServiceCIDR string
}

type NetworkAccess struct {
	RuntimeNetworkName string
	HostGateway        string
	DaemonAddress      string
}

type Manager struct {
	Infrastructure Infrastructure
	Ports          PortAllocator
	Isolation      IsolationPolicy
	ServiceCIDR    string
}

func (m *Manager) PrepareSandbox(ctx context.Context, sandbox *domain.Sandbox) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is required")
	}
	intent := sandbox.NetworkIntent
	if intent == nil || (len(intent.Attachments) == 0 && len(intent.Ports) == 0) {
		return nil
	}
	if m == nil || m.Infrastructure == nil {
		return fmt.Errorf("network infrastructure is required")
	}
	if m.Ports == nil && (len(intent.Expose) > 0 || hasDynamicPublishedPort(intent.Ports)) {
		return fmt.Errorf("network port allocator is required")
	}
	deployment, err := m.Infrastructure.Deployment(ctx)
	if err != nil {
		return fmt.Errorf("detect daemon network deployment: %w", err)
	}
	if err := validateDeployment(deployment); err != nil {
		return err
	}
	if deployment == DeploymentContainerBridge && sandbox.Summary.Driver != driverpkg.RuntimeDriverDocker && len(intent.Ports) > 0 {
		return fmt.Errorf("%w: %s ports require a native or host-network daemon", ErrUnsupported, sandbox.Summary.Driver)
	}
	if preflight, ok := m.Isolation.(IsolationPreflight); ok && len(intent.Attachments) > 0 {
		if err := preflight.Validate(ctx, sandbox); err != nil {
			return fmt.Errorf("validate sandbox network isolation: %w", err)
		}
	}
	state := &domain.SandboxNetworkState{
		Deployment:  deployment,
		ServiceCIDR: firstNonEmpty(strings.TrimSpace(m.ServiceCIDR), DefaultServiceCIDR),
	}
	existing := existingBindingPorts(sandbox.NetworkState)
	for _, attachment := range intent.Attachments {
		access, err := m.Infrastructure.EnsureNetwork(ctx, NetworkRequest{
			ProjectID:   intent.ProjectID,
			ProjectName: intent.ProjectName,
			NetworkName: attachment.Name,
			ServiceCIDR: state.ServiceCIDR,
		})
		if err != nil {
			return fmt.Errorf("ensure network %s: %w", attachment.Name, err)
		}
		endpoint := domain.SandboxNetworkEndpoint{
			Name:               attachment.Name,
			RuntimeNetworkName: strings.TrimSpace(access.RuntimeNetworkName),
			HostGateway:        strings.TrimSpace(access.HostGateway),
			DaemonAddress:      strings.TrimSpace(access.DaemonAddress),
		}
		if endpoint.RuntimeNetworkName == "" || endpoint.HostGateway == "" {
			return fmt.Errorf("network %s returned incomplete access information", attachment.Name)
		}
		state.Attachments = append(state.Attachments, endpoint)
		state.AllowedAddresses = appendAddress(state.AllowedAddresses, endpoint.HostGateway)
		state.AllowedAddresses = appendAddress(state.AllowedAddresses, endpoint.DaemonAddress)
		for _, port := range intent.Expose {
			binding, err := m.internalBinding(ctx, sandbox.Summary.ID, sandbox.Summary.Driver, deployment, endpoint, port, existing)
			if err != nil {
				return err
			}
			state.Bindings = append(state.Bindings, binding)
		}
	}
	for _, port := range intent.Ports {
		binding, err := m.externalBinding(ctx, sandbox.Summary.ID, sandbox.Summary.Driver, deployment, port, existing)
		if err != nil {
			return err
		}
		state.Bindings = append(state.Bindings, binding)
	}
	slices.Sort(state.AllowedAddresses)
	slices.SortFunc(state.Bindings, compareBindings)
	state.Isolation = IsolationNotApplicable
	if len(state.Attachments) > 0 {
		state.Isolation = IsolationUnprotected
		if m.Isolation != nil {
			isolation, err := m.Isolation.Evaluate(ctx, sandbox, state)
			if err != nil {
				return fmt.Errorf("apply sandbox network isolation: %w", err)
			}
			if isolation != IsolationEnforced && isolation != IsolationUnprotected {
				return fmt.Errorf("network isolation policy returned invalid status %q", isolation)
			}
			state.Isolation = isolation
		}
	}
	sandbox.NetworkState = state
	return nil
}

func (m *Manager) internalBinding(ctx context.Context, sandboxID, runtimeDriver, deployment string, endpoint domain.SandboxNetworkEndpoint, port domain.SandboxNetworkPort, existing map[string]int) (domain.SandboxPortBinding, error) {
	hostIP := endpoint.HostGateway
	if runtimeDriver != driverpkg.RuntimeDriverDocker && deployment == DeploymentContainerBridge {
		hostIP = endpoint.DaemonAddress
	}
	if strings.TrimSpace(hostIP) == "" {
		return domain.SandboxPortBinding{}, fmt.Errorf("network %s has no bind address for runtime %s", endpoint.Name, runtimeDriver)
	}
	binding := domain.SandboxPortBinding{
		Network:    endpoint.Name,
		HostIP:     hostIP,
		GuestPort:  port.Target,
		Protocol:   normalizeProtocol(port.Protocol),
		Visibility: VisibilityInternal,
		Publisher:  publisherForRuntime(runtimeDriver),
	}
	binding.HostPort = existing[bindingKey(binding)]
	if binding.HostPort == 0 {
		allocated, err := m.Ports.AllocateHostPort(ctx, sandboxID)
		if err != nil {
			return domain.SandboxPortBinding{}, fmt.Errorf("allocate internal host port: %w", err)
		}
		binding.HostPort = allocated
	}
	return binding, nil
}

func (m *Manager) externalBinding(ctx context.Context, sandboxID, runtimeDriver, deployment string, port domain.SandboxPublishedPort, existing map[string]int) (domain.SandboxPortBinding, error) {
	if deployment == DeploymentContainerBridge && runtimeDriver != driverpkg.RuntimeDriverDocker {
		return domain.SandboxPortBinding{}, fmt.Errorf("%w: %s ports require a native or host-network daemon", ErrUnsupported, runtimeDriver)
	}
	binding := domain.SandboxPortBinding{
		HostIP:     firstNonEmpty(strings.TrimSpace(port.HostIP), "127.0.0.1"),
		HostPort:   port.Published,
		GuestPort:  port.Target,
		Protocol:   normalizeProtocol(port.Protocol),
		Visibility: VisibilityExternal,
		Publisher:  publisherForRuntime(runtimeDriver),
	}
	if binding.HostPort == 0 {
		binding.HostPort = existing[bindingKey(binding)]
	}
	if binding.HostPort == 0 {
		allocated, err := m.Ports.AllocateHostPort(ctx, sandboxID)
		if err != nil {
			return domain.SandboxPortBinding{}, fmt.Errorf("allocate published host port: %w", err)
		}
		binding.HostPort = allocated
	}
	return binding, nil
}

func hasDynamicPublishedPort(ports []domain.SandboxPublishedPort) bool {
	for _, port := range ports {
		if port.Published == 0 {
			return true
		}
	}
	return false
}

func existingBindingPorts(state *domain.SandboxNetworkState) map[string]int {
	result := make(map[string]int)
	if state == nil {
		return result
	}
	for _, binding := range state.Bindings {
		if binding.HostPort > 0 {
			result[bindingKey(binding)] = binding.HostPort
		}
	}
	return result
}

func bindingKey(binding domain.SandboxPortBinding) string {
	return strings.Join([]string{binding.Visibility, binding.Network, binding.HostIP, fmt.Sprint(binding.GuestPort), normalizeProtocol(binding.Protocol)}, "|")
}

func compareBindings(a, b domain.SandboxPortBinding) int {
	if compare := strings.Compare(a.Visibility, b.Visibility); compare != 0 {
		return compare
	}
	if compare := strings.Compare(a.Network, b.Network); compare != 0 {
		return compare
	}
	if compare := strings.Compare(a.HostIP, b.HostIP); compare != 0 {
		return compare
	}
	if a.HostPort != b.HostPort {
		return a.HostPort - b.HostPort
	}
	return a.GuestPort - b.GuestPort
}

func appendAddress(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func publisherForRuntime(runtimeDriver string) string {
	if runtimeDriver == driverpkg.RuntimeDriverDocker {
		return PublisherDocker
	}
	return PublisherDirect
}

func normalizeProtocol(protocol string) string {
	if strings.TrimSpace(protocol) == "" {
		return "tcp"
	}
	return strings.ToLower(strings.TrimSpace(protocol))
}

func validateDeployment(deployment string) error {
	switch deployment {
	case DeploymentNative, DeploymentContainerHost, DeploymentContainerBridge:
		return nil
	default:
		return fmt.Errorf("unknown daemon network deployment %q", deployment)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
