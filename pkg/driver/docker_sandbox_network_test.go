package driver

import (
	"fmt"
	"reflect"
	"testing"

	containerapi "github.com/docker/docker/api/types/container"
	networkapi "github.com/docker/docker/api/types/network"
)

func TestSelectDockerNetworkNamePrefersUserDefinedNetwork(t *testing.T) {
	// Two user-defined networks make the alphabetical choice ambiguous.
	got, ok, ambiguous := selectDockerNetworkName(containerapi.InspectResponse{
		NetworkSettings: &containerapi.NetworkSettings{
			Networks: map[string]*networkapi.EndpointSettings{
				"bridge":             {},
				"playground_default": {},
				"z_custom":           {},
			},
		},
	})
	if !ok {
		t.Fatalf("selectDockerNetworkName returned ok=false")
	}
	if got != "playground_default" {
		t.Fatalf("selectDockerNetworkName returned %q, want playground_default", got)
	}
	if !ambiguous {
		t.Fatalf("selectDockerNetworkName returned ambiguous=false, want true")
	}
}

func TestSelectDockerNetworkNameSingleUserDefinedNetwork(t *testing.T) {
	got, ok, ambiguous := selectDockerNetworkName(containerapi.InspectResponse{
		NetworkSettings: &containerapi.NetworkSettings{
			Networks: map[string]*networkapi.EndpointSettings{
				"playground_default": {},
			},
		},
	})
	if !ok || got != "playground_default" {
		t.Fatalf("selectDockerNetworkName returned %q, %v; want playground_default, true", got, ok)
	}
	if ambiguous {
		t.Fatalf("selectDockerNetworkName returned ambiguous=true, want false")
	}
}

func TestSelectDockerNetworkNameFallsBackToDefaultNetwork(t *testing.T) {
	got, ok, ambiguous := selectDockerNetworkName(containerapi.InspectResponse{
		NetworkSettings: &containerapi.NetworkSettings{
			Networks: map[string]*networkapi.EndpointSettings{
				"bridge": {},
			},
		},
	})
	if !ok {
		t.Fatalf("selectDockerNetworkName returned ok=false")
	}
	if got != "bridge" {
		t.Fatalf("selectDockerNetworkName returned %q, want bridge", got)
	}
	if ambiguous {
		t.Fatalf("selectDockerNetworkName returned ambiguous=true, want false")
	}
}

func TestSelectDockerNetworkNameReturnsFalseWithoutNetworks(t *testing.T) {
	if got, ok, ambiguous := selectDockerNetworkName(containerapi.InspectResponse{}); ok || got != "" || ambiguous {
		t.Fatalf("selectDockerNetworkName returned %q, %v, %v; want empty, false, false", got, ok, ambiguous)
	}
	if got, ok, ambiguous := selectDockerNetworkName(containerapi.InspectResponse{
		NetworkSettings: &containerapi.NetworkSettings{},
	}); ok || got != "" || ambiguous {
		t.Fatalf("selectDockerNetworkName returned %q, %v, %v; want empty, false, false", got, ok, ambiguous)
	}
}

func TestDockerNetworkNamesReturnsSortedAttachedNames(t *testing.T) {
	got := dockerNetworkNames(containerapi.InspectResponse{
		NetworkSettings: &containerapi.NetworkSettings{
			Networks: map[string]*networkapi.EndpointSettings{
				"z_custom": {},
				"a_net":    {},
				"bridge":   {},
			},
		},
	})
	want := []string{"a_net", "bridge", "z_custom"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dockerNetworkNames() = %v, want %v", got, want)
	}
}

func TestSelectDockerDaemonTopologyHonorsExplicitSandboxNetwork(t *testing.T) {
	exists := func(string) error { return nil }
	missing := func(name string) error { return fmt.Errorf("network %s not found", name) }

	tests := []struct {
		name              string
		explicit          string
		detected          string
		daemonNetworks    []string
		containerized     bool
		ambiguous         bool
		networkExists     func(string) error
		wantNetwork       containerapi.NetworkMode
		wantContainerized bool
		wantErr           bool
	}{
		{
			name:              "explicit network wins and preserves containerized",
			explicit:          "sandbox-net",
			detected:          "adp_internal",
			daemonNetworks:    []string{"adp_internal", "sandbox-net"},
			containerized:     true,
			networkExists:     exists,
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: true,
		},
		{
			name:              "explicit network is trimmed",
			explicit:          "  sandbox-net  ",
			detected:          "adp_internal",
			daemonNetworks:    []string{"adp_internal", "sandbox-net"},
			containerized:     true,
			networkExists:     exists,
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: true,
		},
		{
			name:           "missing explicit network fails closed",
			explicit:       "sandbox-net",
			detected:       "adp_internal",
			daemonNetworks: []string{"adp_internal"},
			containerized:  true,
			networkExists:  missing,
			wantErr:        true,
		},
		{
			name:              "empty explicit network keeps the daemon network",
			explicit:          "",
			detected:          "adp_internal",
			containerized:     true,
			networkExists:     missing,
			wantNetwork:       containerapi.NetworkMode("adp_internal"),
			wantContainerized: true,
		},
		{
			name:              "whitespace-only explicit network keeps the daemon network",
			explicit:          "   ",
			detected:          "adp_internal",
			containerized:     true,
			networkExists:     missing,
			wantNetwork:       containerapi.NetworkMode("adp_internal"),
			wantContainerized: true,
		},
		{
			name:          "no explicit and no detected network falls back to default",
			explicit:      "",
			detected:      "",
			containerized: false,
			networkExists: missing,
			wantNetwork:   containerapi.NetworkMode("default"),
		},
		{
			name:          "ambiguous network without explicit network fails closed",
			explicit:      "",
			detected:      "agent-compose-sandbox",
			containerized: true,
			ambiguous:     true,
			networkExists: missing,
			wantErr:       true,
		},
		{
			name:          "explicit host network is rejected",
			explicit:      "host",
			detected:      "adp_internal",
			containerized: true,
			networkExists: exists,
			wantErr:       true,
		},
		{
			name:          "explicit none network is rejected",
			explicit:      "none",
			detected:      "adp_internal",
			containerized: true,
			networkExists: exists,
			wantErr:       true,
		},
		{
			name:          "explicit bridge network is rejected",
			explicit:      "bridge",
			detected:      "adp_internal",
			containerized: true,
			networkExists: exists,
			wantErr:       true,
		},
		{
			name:          "explicit default network is rejected",
			explicit:      "default",
			detected:      "adp_internal",
			containerized: true,
			networkExists: exists,
			wantErr:       true,
		},
		{
			name:           "explicit network not attached to containerized daemon fails closed",
			explicit:       "sandbox-net",
			detected:       "adp_internal",
			daemonNetworks: []string{"adp_internal"},
			containerized:  true,
			networkExists:  exists,
			wantErr:        true,
		},
		{
			name:              "explicit network not attached to native daemon is allowed",
			explicit:          "sandbox-net",
			detected:          "adp_internal",
			daemonNetworks:    []string{"adp_internal"},
			containerized:     false,
			networkExists:     exists,
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: false,
		},
		{
			name:              "explicit network wins even when detection is ambiguous",
			explicit:          "sandbox-net",
			detected:          "agent-compose-sandbox",
			daemonNetworks:    []string{"adp_internal", "agent-compose-sandbox", "sandbox-net"},
			containerized:     true,
			ambiguous:         true,
			networkExists:     exists,
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectDockerDaemonTopology(test.explicit, test.detected, test.daemonNetworks, test.containerized, test.ambiguous, test.networkExists)
			if test.wantErr {
				if err == nil {
					t.Fatal("selectDockerDaemonTopology() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("selectDockerDaemonTopology() error = %v, want nil", err)
			}
			if got.networkMode != test.wantNetwork {
				t.Fatalf("networkMode = %q, want %q", got.networkMode, test.wantNetwork)
			}
			if got.containerized != test.wantContainerized {
				t.Fatalf("containerized = %v, want %v", got.containerized, test.wantContainerized)
			}
		})
	}
}

func TestSelectDockerDaemonTopologyTrimsExplicitNetworkForInspect(t *testing.T) {
	var inspected string
	_, err := selectDockerDaemonTopology("  sandbox-net  ", "adp_internal", []string{"adp_internal", "sandbox-net"}, true, false, func(name string) error {
		inspected = name
		return nil
	})
	if err != nil {
		t.Fatalf("selectDockerDaemonTopology() error = %v, want nil", err)
	}
	if inspected != "sandbox-net" {
		t.Fatalf("networkExists received %q, want trimmed %q", inspected, "sandbox-net")
	}
}

func TestDockerSandboxNetworkAttachEnabled(t *testing.T) {
	tests := []struct {
		name            string
		containerized   bool
		explicitNetwork string
		want            bool
	}{
		{name: "containerized daemon without explicit network attaches", containerized: true, explicitNetwork: "", want: true},
		{name: "containerized daemon with explicit network does not attach", containerized: true, explicitNetwork: "sandbox-net", want: false},
		{name: "native daemon without explicit network does not attach", containerized: false, explicitNetwork: "", want: false},
		{name: "whitespace-only explicit network attaches", containerized: true, explicitNetwork: "   ", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dockerSandboxNetworkAttachEnabled(test.containerized, test.explicitNetwork); got != test.want {
				t.Fatalf("dockerSandboxNetworkAttachEnabled(%v, %q) = %v, want %v", test.containerized, test.explicitNetwork, got, test.want)
			}
		})
	}
}
