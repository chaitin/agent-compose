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
