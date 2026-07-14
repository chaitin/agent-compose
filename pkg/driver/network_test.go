package driver

import (
	"strings"
	"testing"
)

func TestSandboxNetworkBindingsValidatesAndSortsPublisher(t *testing.T) {
	sandbox := &Sandbox{Network: &SandboxNetwork{Bindings: []SandboxPortBinding{
		{HostIP: "10.0.0.1", HostPort: 32000, GuestPort: 80, Publisher: NetworkPublisherDocker},
		{HostIP: "10.0.0.1", HostPort: 31999, GuestPort: 79, Protocol: "TCP", Publisher: NetworkPublisherDocker},
	}}}
	bindings, err := sandboxNetworkBindings(sandbox, NetworkPublisherDocker)
	if err != nil {
		t.Fatalf("sandboxNetworkBindings() error = %v", err)
	}
	if len(bindings) != 2 || bindings[0].HostPort != 31999 || bindings[1].HostPort != 32000 || bindings[0].Protocol != "tcp" {
		t.Fatalf("bindings = %#v", bindings)
	}
}

func TestSandboxNetworkBindingsRejectsInvalidBinding(t *testing.T) {
	tests := []struct {
		name     string
		bindings []SandboxPortBinding
		contains string
	}{
		{"host IP", []SandboxPortBinding{{HostIP: "bad", HostPort: 32000, GuestPort: 80, Publisher: NetworkPublisherDirect}}, "invalid IPv4"},
		{"host port", []SandboxPortBinding{{HostIP: "127.0.0.1", GuestPort: 80, Publisher: NetworkPublisherDirect}}, "invalid host port"},
		{"guest port", []SandboxPortBinding{{HostIP: "127.0.0.1", HostPort: 32000, Publisher: NetworkPublisherDirect}}, "invalid guest port"},
		{"protocol", []SandboxPortBinding{{HostIP: "127.0.0.1", HostPort: 32000, GuestPort: 80, Protocol: "udp", Publisher: NetworkPublisherDirect}}, "unsupported protocol"},
		{"publisher", []SandboxPortBinding{{HostIP: "127.0.0.1", HostPort: 32000, GuestPort: 80, Publisher: NetworkPublisherDocker}}, "requires publisher"},
		{"duplicate", []SandboxPortBinding{
			{HostIP: "127.0.0.1", HostPort: 32000, GuestPort: 80, Publisher: NetworkPublisherDirect},
			{HostIP: "127.0.0.1", HostPort: 32000, GuestPort: 81, Publisher: NetworkPublisherDirect},
		}, "duplicates listener"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sandboxNetworkBindings(&Sandbox{Network: &SandboxNetwork{Bindings: tt.bindings}}, NetworkPublisherDirect)
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("sandboxNetworkBindings() error = %v", err)
			}
		})
	}
}

func TestSandboxNetworkNamesDeduplicatesAndSorts(t *testing.T) {
	names, err := sandboxNetworkNames(&Sandbox{
		Network: &SandboxNetwork{Attachments: []SandboxNetworkEndpoint{
			{RuntimeNetworkName: "project_b"}, {RuntimeNetworkName: "project_a"}, {RuntimeNetworkName: "project_b"},
		}},
	})
	if err != nil || len(names) != 2 || names[0] != "project_a" || names[1] != "project_b" {
		t.Fatalf("sandboxNetworkNames() = %#v, %v", names, err)
	}
}

func TestSandboxNetworkEgressPolicy(t *testing.T) {
	allowed, serviceCIDR, enabled, err := sandboxNetworkEgressPolicy(&Sandbox{
		Network: &SandboxNetwork{
			ServiceCIDR:      "10.254.0.1/16",
			Attachments:      []SandboxNetworkEndpoint{{Name: "frontend"}},
			AllowedAddresses: []string{"10.254.2.2", "10.254.1.1", "10.254.2.2"},
		},
	})
	if err != nil || !enabled || serviceCIDR != "10.254.0.0/16" || len(allowed) != 2 || allowed[0] != "10.254.1.1" {
		t.Fatalf("sandboxNetworkEgressPolicy() = %#v, %q, %v, %v", allowed, serviceCIDR, enabled, err)
	}
	if _, _, _, err := sandboxNetworkEgressPolicy(&Sandbox{Network: &SandboxNetwork{ServiceCIDR: "bad", Attachments: []SandboxNetworkEndpoint{{Name: "frontend"}}}}); err == nil {
		t.Fatal("sandboxNetworkEgressPolicy() accepted invalid service CIDR")
	}
}
