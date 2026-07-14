package networks

import (
	"context"
	"fmt"
	"slices"
	"strings"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

const (
	VisibilityInternal = "internal"
	VisibilityExternal = "external"

	PublisherDocker = "docker"
	PublisherDirect = "direct"
)

type PublishAddressProvider interface {
	DefaultPublishAddress(context.Context) (string, error)
}

type PortAllocator interface {
	AllocateHostPort(context.Context, string) (int, error)
}

type Manager struct {
	PublishAddresses      PublishAddressProvider
	Ports                 PortAllocator
	DockerPublishAddress  string
	RuntimePublishAddress string
}

func (m *Manager) PrepareSandbox(ctx context.Context, sandbox *domain.Sandbox) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is required")
	}
	intent := sandbox.NetworkIntent
	if intent == nil || (len(intent.Attachments) == 0 && len(intent.Ports) == 0) {
		return nil
	}
	if m == nil {
		return fmt.Errorf("network manager is required")
	}
	if m.Ports == nil && (hasDynamicExposedPort(intent.Expose) || hasDynamicPublishedPort(intent.Ports)) {
		return fmt.Errorf("network port allocator is required")
	}

	state := &domain.SandboxNetworkState{}
	networkNames := make([]string, 0, len(intent.Attachments))
	for _, attachment := range intent.Attachments {
		state.Attachments = append(state.Attachments, domain.SandboxNetworkEndpoint{
			Name: attachment.Name,
		})
		networkNames = append(networkNames, attachment.Name)
	}

	existing := existingBindingPorts(sandbox.NetworkState)
	if len(networkNames) > 0 && len(intent.Expose) > 0 {
		publishAddress, err := m.internalPublishAddress(ctx, sandbox.Summary.Driver)
		if err != nil {
			return err
		}
		for _, port := range intent.Expose {
			binding, err := m.internalBinding(ctx, sandbox.Summary.ID, networkNames, publishAddress, sandbox.Summary.Driver, port, existing)
			if err != nil {
				return err
			}
			state.Bindings = append(state.Bindings, binding)
		}
	}
	for _, port := range intent.Ports {
		binding, err := m.externalBinding(ctx, sandbox.Summary.ID, sandbox.Summary.Driver, port, existing)
		if err != nil {
			return err
		}
		state.Bindings = append(state.Bindings, binding)
	}
	slices.SortFunc(state.Bindings, compareBindings)
	sandbox.NetworkState = state
	return nil
}

func (m *Manager) internalPublishAddress(ctx context.Context, runtimeDriver string) (string, error) {
	address := strings.TrimSpace(m.RuntimePublishAddress)
	if runtimeDriver == driverpkg.RuntimeDriverDocker {
		address = strings.TrimSpace(m.DockerPublishAddress)
	}
	if address != "" {
		return address, nil
	}
	if m.PublishAddresses == nil {
		return "", fmt.Errorf("network publish address provider is required")
	}
	address, err := m.PublishAddresses.DefaultPublishAddress(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve default network publish address: %w", err)
	}
	if strings.TrimSpace(address) == "" {
		return "", fmt.Errorf("default network publish address is empty")
	}
	return strings.TrimSpace(address), nil
}

func (m *Manager) internalBinding(ctx context.Context, sandboxID string, networks []string, hostIP, runtimeDriver string, port domain.SandboxNetworkPort, existing map[string]int) (domain.SandboxPortBinding, error) {
	binding := domain.SandboxPortBinding{
		Networks:   append([]string(nil), networks...),
		HostIP:     hostIP,
		HostPort:   port.HostPort,
		GuestPort:  port.Target,
		Protocol:   normalizeProtocol(port.Protocol),
		Visibility: VisibilityInternal,
		Publisher:  publisherForRuntime(runtimeDriver),
	}
	if binding.HostPort == 0 {
		binding.HostPort = existing[bindingKey(binding)]
	}
	if binding.HostPort == 0 {
		allocated, err := m.Ports.AllocateHostPort(ctx, sandboxID)
		if err != nil {
			return domain.SandboxPortBinding{}, fmt.Errorf("allocate internal host port: %w", err)
		}
		binding.HostPort = allocated
	}
	return binding, nil
}

func hasDynamicExposedPort(ports []domain.SandboxNetworkPort) bool {
	for _, port := range ports {
		if port.HostPort == 0 {
			return true
		}
	}
	return false
}

func (m *Manager) externalBinding(ctx context.Context, sandboxID, runtimeDriver string, port domain.SandboxPublishedPort, existing map[string]int) (domain.SandboxPortBinding, error) {
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
	return strings.Join([]string{binding.Visibility, strings.Join(binding.Networks, ","), binding.HostIP, fmt.Sprint(binding.GuestPort), normalizeProtocol(binding.Protocol)}, "|")
}

func compareBindings(a, b domain.SandboxPortBinding) int {
	if compare := strings.Compare(a.Visibility, b.Visibility); compare != 0 {
		return compare
	}
	if compare := strings.Compare(strings.Join(a.Networks, ","), strings.Join(b.Networks, ",")); compare != 0 {
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
