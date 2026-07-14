package driver

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

const (
	NetworkPublisherDocker = "docker"
	NetworkPublisherDirect = "direct"
)

func sandboxNetworkBindings(sandbox *Sandbox, publisher string) ([]SandboxPortBinding, error) {
	if sandbox == nil || sandbox.Network == nil || len(sandbox.Network.Bindings) == 0 {
		return nil, nil
	}
	bindings := make([]SandboxPortBinding, 0, len(sandbox.Network.Bindings))
	listeners := make(map[string]struct{}, len(sandbox.Network.Bindings))
	for i, binding := range sandbox.Network.Bindings {
		if actual := strings.TrimSpace(binding.Publisher); actual != publisher {
			return nil, fmt.Errorf("network binding %d requires publisher %q, runtime provides %q", i, actual, publisher)
		}
		hostIP := strings.TrimSpace(binding.HostIP)
		addr, err := netip.ParseAddr(hostIP)
		if err != nil || !addr.Is4() {
			return nil, fmt.Errorf("network binding %d has invalid IPv4 host address %q", i, binding.HostIP)
		}
		if binding.HostPort < 1 || binding.HostPort > 65535 {
			return nil, fmt.Errorf("network binding %d has invalid host port %d", i, binding.HostPort)
		}
		if binding.GuestPort < 1 || binding.GuestPort > 65535 {
			return nil, fmt.Errorf("network binding %d has invalid guest port %d", i, binding.GuestPort)
		}
		protocol := strings.ToLower(strings.TrimSpace(binding.Protocol))
		if protocol == "" {
			protocol = "tcp"
		}
		if protocol != "tcp" {
			return nil, fmt.Errorf("network binding %d uses unsupported protocol %q", i, binding.Protocol)
		}
		key := strings.Join([]string{hostIP, strconv.Itoa(binding.HostPort), protocol}, "|")
		if _, exists := listeners[key]; exists {
			return nil, fmt.Errorf("network binding %d duplicates listener %s:%d/%s", i, hostIP, binding.HostPort, protocol)
		}
		listeners[key] = struct{}{}
		binding.HostIP = hostIP
		binding.Protocol = protocol
		bindings = append(bindings, binding)
	}
	slices.SortFunc(bindings, func(a, b SandboxPortBinding) int {
		if compare := strings.Compare(a.HostIP, b.HostIP); compare != 0 {
			return compare
		}
		if a.HostPort != b.HostPort {
			return a.HostPort - b.HostPort
		}
		return a.GuestPort - b.GuestPort
	})
	return bindings, nil
}

func sandboxNetworkNames(sandbox *Sandbox) ([]string, error) {
	if sandbox == nil || sandbox.Network == nil {
		return nil, nil
	}
	result := make([]string, 0, len(sandbox.Network.Attachments))
	for i, attachment := range sandbox.Network.Attachments {
		name := strings.TrimSpace(attachment.RuntimeNetworkName)
		if name == "" {
			return nil, fmt.Errorf("network attachment %d has no runtime network name", i)
		}
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	slices.Sort(result)
	return result, nil
}

func sandboxNetworkEgressPolicy(sandbox *Sandbox) ([]string, string, bool, error) {
	if sandbox == nil || sandbox.Network == nil || len(sandbox.Network.Attachments) == 0 {
		return nil, "", false, nil
	}
	serviceCIDR := strings.TrimSpace(sandbox.Network.ServiceCIDR)
	prefix, err := netip.ParsePrefix(serviceCIDR)
	if err != nil || !prefix.Addr().Is4() {
		return nil, "", false, fmt.Errorf("sandbox network has invalid IPv4 service CIDR %q", sandbox.Network.ServiceCIDR)
	}
	allowed := make([]string, 0, len(sandbox.Network.AllowedAddresses))
	for i, value := range sandbox.Network.AllowedAddresses {
		address := strings.TrimSpace(value)
		addr, err := netip.ParseAddr(address)
		if err != nil || !addr.Is4() {
			return nil, "", false, fmt.Errorf("sandbox network allowed address %d is not a valid IPv4 address: %q", i, value)
		}
		if !slices.Contains(allowed, address) {
			allowed = append(allowed, address)
		}
	}
	if len(allowed) == 0 {
		return nil, "", false, fmt.Errorf("sandbox network has attachments but no allowed service addresses")
	}
	slices.Sort(allowed)
	return allowed, prefix.Masked().String(), true, nil
}
