package driver

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	containerapi "github.com/docker/docker/api/types/container"
	networkapi "github.com/docker/docker/api/types/network"
)

func testDockerNetworkInspect(internal bool, gateways ...string) func(string) (dockerSandboxNetworkInfo, error) {
	return func(string) (dockerSandboxNetworkInfo, error) {
		return dockerSandboxNetworkInfo{Internal: internal, Gateways: gateways}, nil
	}
}

func testMissingDockerNetwork(name string) (dockerSandboxNetworkInfo, error) {
	return dockerSandboxNetworkInfo{}, fmt.Errorf("network %s not found", name)
}

func testDaemonNetworks(names ...string) map[string]dockerDaemonEndpoint {
	endpoints := make(map[string]dockerDaemonEndpoint, len(names))
	for _, name := range names {
		endpoints[name] = dockerDaemonEndpoint{}
	}
	return endpoints
}

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

func TestDockerDaemonEndpointsCapturesAliasesAndAddresses(t *testing.T) {
	got := dockerDaemonEndpoints(containerapi.InspectResponse{
		ContainerJSONBase: &containerapi.ContainerJSONBase{Name: "/agent-compose"},
		NetworkSettings: &containerapi.NetworkSettings{
			Networks: map[string]*networkapi.EndpointSettings{
				"sandbox-net": {
					Aliases:           []string{"daemon", "agent-compose"},
					DNSNames:          []string{"agent-compose"},
					IPAddress:         "172.20.0.2",
					GlobalIPv6Address: "fd00::2",
				},
				"default": {},
			},
		},
	})
	want := map[string]dockerDaemonEndpoint{
		"sandbox-net": {aliases: []string{"agent-compose", "daemon"}, addresses: []string{"172.20.0.2", "fd00::2"}},
		"default":     {aliases: []string{"agent-compose"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dockerDaemonEndpoints() = %#v, want %#v", got, want)
	}
	if endpoints := dockerDaemonEndpoints(containerapi.InspectResponse{}); endpoints != nil {
		t.Fatalf("dockerDaemonEndpoints() without networks = %v, want nil", endpoints)
	}
}

func TestDockerSandboxNetworkInfoFromInspect(t *testing.T) {
	got := dockerSandboxNetworkInfoFromInspect(networkapi.Inspect{
		Internal: true,
		IPAM: networkapi.IPAM{Config: []networkapi.IPAMConfig{
			{Gateway: "172.20.0.1"},
			{Gateway: ""},
			{Gateway: "10.0.0.1"},
		}},
	})
	want := dockerSandboxNetworkInfo{Internal: true, Gateways: []string{"10.0.0.1", "172.20.0.1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dockerSandboxNetworkInfoFromInspect() = %#v, want %#v", got, want)
	}
	if got := dockerSandboxNetworkInfoFromInspect(networkapi.Inspect{}); got.Internal || len(got.Gateways) != 0 {
		t.Fatalf("dockerSandboxNetworkInfoFromInspect() = %#v, want the zero facts", got)
	}
}

func TestSelectDockerDaemonTopologyHonorsExplicitSandboxNetwork(t *testing.T) {
	tests := []struct {
		name              string
		explicit          string
		detected          string
		daemonEndpoints   map[string]dockerDaemonEndpoint
		containerized     bool
		ambiguous         bool
		networkInspect    func(string) (dockerSandboxNetworkInfo, error)
		wantNetwork       containerapi.NetworkMode
		wantContainerized bool
		wantErr           bool
	}{
		{
			name:              "explicit network wins and preserves containerized",
			explicit:          "sandbox-net",
			detected:          "adp_internal",
			daemonEndpoints:   testDaemonNetworks("adp_internal", "sandbox-net"),
			containerized:     true,
			networkInspect:    testDockerNetworkInspect(false),
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: true,
		},
		{
			name:              "explicit network is trimmed",
			explicit:          "  sandbox-net  ",
			detected:          "adp_internal",
			daemonEndpoints:   testDaemonNetworks("adp_internal", "sandbox-net"),
			containerized:     true,
			networkInspect:    testDockerNetworkInspect(false),
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: true,
		},
		{
			name:            "missing explicit network fails closed",
			explicit:        "sandbox-net",
			detected:        "adp_internal",
			daemonEndpoints: testDaemonNetworks("adp_internal"),
			containerized:   true,
			networkInspect:  testMissingDockerNetwork,
			wantErr:         true,
		},
		{
			name:              "empty explicit network keeps the daemon network",
			explicit:          "",
			detected:          "adp_internal",
			containerized:     true,
			networkInspect:    testMissingDockerNetwork,
			wantNetwork:       containerapi.NetworkMode("adp_internal"),
			wantContainerized: true,
		},
		{
			name:              "whitespace-only explicit network keeps the daemon network",
			explicit:          "   ",
			detected:          "adp_internal",
			containerized:     true,
			networkInspect:    testMissingDockerNetwork,
			wantNetwork:       containerapi.NetworkMode("adp_internal"),
			wantContainerized: true,
		},
		{
			name:           "no explicit and no detected network falls back to default",
			explicit:       "",
			detected:       "",
			containerized:  false,
			networkInspect: testMissingDockerNetwork,
			wantNetwork:    containerapi.NetworkMode("default"),
		},
		{
			name:           "ambiguous network without explicit network fails closed",
			explicit:       "",
			detected:       "agent-compose-sandbox",
			containerized:  true,
			ambiguous:      true,
			networkInspect: testMissingDockerNetwork,
			wantErr:        true,
		},
		{
			name:           "explicit host network is rejected",
			explicit:       "host",
			detected:       "adp_internal",
			containerized:  true,
			networkInspect: testDockerNetworkInspect(false),
			wantErr:        true,
		},
		{
			name:           "explicit none network is rejected",
			explicit:       "none",
			detected:       "adp_internal",
			containerized:  true,
			networkInspect: testDockerNetworkInspect(false),
			wantErr:        true,
		},
		{
			name:           "explicit bridge network is rejected",
			explicit:       "bridge",
			detected:       "adp_internal",
			containerized:  true,
			networkInspect: testDockerNetworkInspect(false),
			wantErr:        true,
		},
		{
			name:           "explicit default network is rejected",
			explicit:       "default",
			detected:       "adp_internal",
			containerized:  true,
			networkInspect: testDockerNetworkInspect(false),
			wantErr:        true,
		},
		{
			name:            "explicit network not attached to containerized daemon fails closed",
			explicit:        "sandbox-net",
			detected:        "adp_internal",
			daemonEndpoints: testDaemonNetworks("adp_internal"),
			containerized:   true,
			networkInspect:  testDockerNetworkInspect(false),
			wantErr:         true,
		},
		{
			name:              "explicit network not attached to native daemon is allowed",
			explicit:          "sandbox-net",
			detected:          "adp_internal",
			daemonEndpoints:   testDaemonNetworks("adp_internal"),
			containerized:     false,
			networkInspect:    testDockerNetworkInspect(false),
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: false,
		},
		{
			name:              "explicit network wins even when detection is ambiguous",
			explicit:          "sandbox-net",
			detected:          "agent-compose-sandbox",
			daemonEndpoints:   testDaemonNetworks("adp_internal", "agent-compose-sandbox", "sandbox-net"),
			containerized:     true,
			ambiguous:         true,
			networkInspect:    testDockerNetworkInspect(false),
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: true,
		},
		{
			name:              "non-internal explicit network stays accepted without strict mode",
			explicit:          "sandbox-net",
			daemonEndpoints:   testDaemonNetworks("sandbox-net"),
			containerized:     true,
			networkInspect:    testDockerNetworkInspect(false),
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectDockerDaemonTopology(dockerDaemonTopologyInput{
				explicitNetwork: test.explicit,
				detectedNetwork: test.detected,
				daemonEndpoints: test.daemonEndpoints,
				containerized:   test.containerized,
				ambiguous:       test.ambiguous,
				networkInspect:  test.networkInspect,
			})
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
			if got.strict {
				t.Fatalf("strict = true without the strict switch")
			}
		})
	}
}

func TestSelectDockerDaemonTopologyStrictInternalNetwork(t *testing.T) {
	const aliasBaseURL = "http://agent-compose:7410"
	internal := testDockerNetworkInspect(true, "172.20.0.1")
	notInternal := testDockerNetworkInspect(false, "172.20.0.1")
	daemonOnStrict := map[string]dockerDaemonEndpoint{
		"sandbox-net": {aliases: []string{"agent-compose"}, addresses: []string{"172.20.0.2"}},
	}

	tests := []struct {
		name              string
		explicit          string
		detected          string
		daemonEndpoints   map[string]dockerDaemonEndpoint
		containerized     bool
		ambiguous         bool
		runtimeBaseURL    string
		networkInspect    func(string) (dockerSandboxNetworkInfo, error)
		wantNetwork       containerapi.NetworkMode
		wantContainerized bool
		wantErrSubstring  string
	}{
		{
			name:              "internal network accepted",
			explicit:          "sandbox-net",
			detected:          "adp_internal",
			daemonEndpoints:   daemonOnStrict,
			containerized:     true,
			runtimeBaseURL:    aliasBaseURL,
			networkInspect:    internal,
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: true,
		},
		{
			name:              "strict explicit network wins over detected and ambiguous",
			explicit:          "sandbox-net",
			detected:          "adp_internal",
			daemonEndpoints:   daemonOnStrict,
			containerized:     true,
			ambiguous:         true,
			runtimeBaseURL:    aliasBaseURL,
			networkInspect:    internal,
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: true,
		},
		{
			name:              "native daemon with its gateway base URL is accepted",
			explicit:          "sandbox-net",
			runtimeBaseURL:    "http://172.20.0.1:7410",
			networkInspect:    internal,
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: false,
		},
		{
			name:              "native daemon with a DNS alias cannot be verified and is accepted",
			explicit:          "sandbox-net",
			runtimeBaseURL:    aliasBaseURL,
			networkInspect:    internal,
			wantNetwork:       containerapi.NetworkMode("sandbox-net"),
			wantContainerized: false,
		},
		{
			name:             "non-internal network is rejected",
			explicit:         "sandbox-net",
			daemonEndpoints:  daemonOnStrict,
			containerized:    true,
			runtimeBaseURL:   aliasBaseURL,
			networkInspect:   notInternal,
			wantErrSubstring: "is not internal",
		},
		{
			name:             "missing network is rejected",
			explicit:         "sandbox-net",
			daemonEndpoints:  daemonOnStrict,
			containerized:    true,
			runtimeBaseURL:   aliasBaseURL,
			networkInspect:   testMissingDockerNetwork,
			wantErrSubstring: "unavailable",
		},
		{
			name:             "unset network does not fall back",
			explicit:         "",
			detected:         "adp_internal",
			daemonEndpoints:  testDaemonNetworks("adp_internal"),
			containerized:    true,
			runtimeBaseURL:   aliasBaseURL,
			networkInspect:   internal,
			wantErrSubstring: "refusing to fall back",
		},
		{
			name:             "whitespace-only network does not fall back",
			explicit:         "   ",
			detected:         "adp_internal",
			containerized:    true,
			runtimeBaseURL:   aliasBaseURL,
			networkInspect:   internal,
			wantErrSubstring: "DOCKER_SANDBOX_NETWORK",
		},
		{
			name:             "built-in bridge is rejected",
			explicit:         "bridge",
			runtimeBaseURL:   aliasBaseURL,
			networkInspect:   internal,
			wantErrSubstring: "built-in network",
		},
		{
			name:             "built-in host is rejected",
			explicit:         "host",
			runtimeBaseURL:   aliasBaseURL,
			networkInspect:   internal,
			wantErrSubstring: "built-in network",
		},
		{
			name:             "built-in none is rejected",
			explicit:         "none",
			runtimeBaseURL:   aliasBaseURL,
			networkInspect:   internal,
			wantErrSubstring: "built-in network",
		},
		{
			name:             "built-in default is rejected",
			explicit:         "default",
			runtimeBaseURL:   aliasBaseURL,
			networkInspect:   internal,
			wantErrSubstring: "built-in network",
		},
		{
			name:             "network not attached to a containerized daemon is rejected",
			explicit:         "sandbox-net",
			daemonEndpoints:  testDaemonNetworks("adp_internal"),
			containerized:    true,
			runtimeBaseURL:   aliasBaseURL,
			networkInspect:   internal,
			wantErrSubstring: "not attached to the daemon",
		},
		{
			name:             "missing network inspector is rejected",
			explicit:         "sandbox-net",
			daemonEndpoints:  daemonOnStrict,
			containerized:    true,
			runtimeBaseURL:   aliasBaseURL,
			wantErrSubstring: "no Docker network inspector",
		},
		{
			name:             "unset runtime base URL is rejected",
			explicit:         "sandbox-net",
			daemonEndpoints:  daemonOnStrict,
			containerized:    true,
			networkInspect:   internal,
			wantErrSubstring: "AGENT_COMPOSE_RUNTIME_BASE_URL",
		},
		{
			name:             "loopback runtime base URL is rejected",
			explicit:         "sandbox-net",
			daemonEndpoints:  daemonOnStrict,
			containerized:    true,
			runtimeBaseURL:   "http://127.0.0.1:7410",
			networkInspect:   internal,
			wantErrSubstring: "loopback",
		},
		{
			name:             "localhost runtime base URL is rejected",
			explicit:         "sandbox-net",
			daemonEndpoints:  daemonOnStrict,
			containerized:    true,
			runtimeBaseURL:   "http://localhost:7410",
			networkInspect:   internal,
			wantErrSubstring: "loopback",
		},
		{
			name:             "non-http runtime base URL is rejected",
			explicit:         "sandbox-net",
			daemonEndpoints:  daemonOnStrict,
			containerized:    true,
			runtimeBaseURL:   "ftp://agent-compose:7410",
			networkInspect:   internal,
			wantErrSubstring: "absolute http(s)",
		},
		{
			name:             "host that is not a daemon alias is rejected",
			explicit:         "sandbox-net",
			daemonEndpoints:  daemonOnStrict,
			containerized:    true,
			runtimeBaseURL:   "http://elsewhere:7410",
			networkInspect:   internal,
			wantErrSubstring: "not an alias",
		},
		{
			name:             "containerized address that is neither gateway nor daemon is rejected",
			explicit:         "sandbox-net",
			daemonEndpoints:  daemonOnStrict,
			containerized:    true,
			runtimeBaseURL:   "http://172.20.0.99:7410",
			networkInspect:   internal,
			wantErrSubstring: "neither the internal network gateway",
		},
		{
			name:             "native address that is not the gateway is rejected",
			explicit:         "sandbox-net",
			containerized:    false,
			runtimeBaseURL:   "http://172.20.0.99:7410",
			networkInspect:   internal,
			wantErrSubstring: "not the gateway",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectDockerDaemonTopology(dockerDaemonTopologyInput{
				explicitNetwork: test.explicit,
				detectedNetwork: test.detected,
				daemonEndpoints: test.daemonEndpoints,
				containerized:   test.containerized,
				ambiguous:       test.ambiguous,
				strict:          true,
				runtimeBaseURL:  test.runtimeBaseURL,
				networkInspect:  test.networkInspect,
			})
			if test.wantErrSubstring != "" {
				if err == nil {
					t.Fatalf("selectDockerDaemonTopology() error = nil, want one containing %q", test.wantErrSubstring)
				}
				if !strings.Contains(err.Error(), test.wantErrSubstring) {
					t.Fatalf("selectDockerDaemonTopology() error = %q, want it to contain %q", err, test.wantErrSubstring)
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
			if !got.strict {
				t.Fatalf("strict = false, want true for the selected strict profile")
			}
		})
	}
}

func TestSelectDockerDaemonTopologyStrictRejectsBuiltinWithoutInspecting(t *testing.T) {
	inspected := false
	_, err := selectDockerDaemonTopology(dockerDaemonTopologyInput{
		explicitNetwork: "bridge",
		containerized:   false,
		strict:          true,
		runtimeBaseURL:  "http://172.20.0.1:7410",
		networkInspect: func(string) (dockerSandboxNetworkInfo, error) {
			inspected = true
			return dockerSandboxNetworkInfo{Internal: true}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("selectDockerDaemonTopology() error = %v, want the built-in rejection", err)
	}
	if inspected {
		t.Fatalf("selectDockerDaemonTopology() inspected a built-in network, want no daemon round trip")
	}
}

func TestDockerSandboxNetworkStrictRequested(t *testing.T) {
	tests := []struct {
		name   string
		config *appconfig.Config
		want   bool
	}{
		{name: "nil config", config: nil, want: false},
		{name: "strict switch unset", config: &appconfig.Config{SandboxInitEgress: "enforce"}, want: false},
		{name: "audit mode ignores the switch", config: &appconfig.Config{DockerSandboxNetworkStrict: true, SandboxInitEgress: "audit"}, want: false},
		{name: "off mode ignores the switch", config: &appconfig.Config{DockerSandboxNetworkStrict: true, SandboxInitEgress: "off"}, want: false},
		{name: "unset egress mode ignores the switch", config: &appconfig.Config{DockerSandboxNetworkStrict: true}, want: false},
		{name: "enforce mode selects the strict profile", config: &appconfig.Config{DockerSandboxNetworkStrict: true, SandboxInitEgress: "enforce"}, want: true},
		{name: "enforce mode is trimmed", config: &appconfig.Config{DockerSandboxNetworkStrict: true, SandboxInitEgress: " enforce "}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DockerSandboxNetworkStrictRequested(test.config); got != test.want {
				t.Fatalf("DockerSandboxNetworkStrictRequested(%+v) = %v, want %v", test.config, got, test.want)
			}
		})
	}
}

// TestDockerSandboxNetworkStrictIsInertOutsideEnforceMode drives the whole
// selection with the value the predicate produces for audit and off mode: the
// configured internal-network requirement and the base URL requirement must not
// apply until enforce mode is on.
func TestDockerSandboxNetworkStrictIsInertOutsideEnforceMode(t *testing.T) {
	for _, mode := range []string{"audit", "off", ""} {
		config := &appconfig.Config{DockerSandboxNetworkStrict: true, SandboxInitEgress: mode}
		strict := DockerSandboxNetworkStrictRequested(config)
		got, err := selectDockerDaemonTopology(dockerDaemonTopologyInput{
			explicitNetwork: "non-internal-net",
			containerized:   false,
			strict:          strict,
			runtimeBaseURL:  "",
			networkInspect:  testDockerNetworkInspect(false),
		})
		if err != nil {
			t.Fatalf("mode %q: selectDockerDaemonTopology() error = %v, want nil", mode, err)
		}
		if got.strict {
			t.Fatalf("mode %q: strict = true, want the placement unchanged", mode)
		}
		if got.networkMode != containerapi.NetworkMode("non-internal-net") {
			t.Fatalf("mode %q: networkMode = %q, want the explicit network", mode, got.networkMode)
		}
	}
}

func TestSelectDockerDaemonTopologyTrimsExplicitNetworkForInspect(t *testing.T) {
	var inspected string
	_, err := selectDockerDaemonTopology(dockerDaemonTopologyInput{
		explicitNetwork: "  sandbox-net  ",
		detectedNetwork: "adp_internal",
		daemonEndpoints: testDaemonNetworks("adp_internal", "sandbox-net"),
		containerized:   true,
		networkInspect: func(name string) (dockerSandboxNetworkInfo, error) {
			inspected = name
			return dockerSandboxNetworkInfo{}, nil
		},
	})
	if err != nil {
		t.Fatalf("selectDockerDaemonTopology() error = %v, want nil", err)
	}
	if inspected != "sandbox-net" {
		t.Fatalf("networkInspect received %q, want trimmed %q", inspected, "sandbox-net")
	}
}

func TestValidateDockerSandboxStrictNetwork(t *testing.T) {
	tests := []struct {
		name             string
		networkName      string
		networks         map[string]*networkapi.EndpointSettings
		wantErrSubstring string
	}{
		{
			name:        "only the strict network is accepted",
			networkName: "sandbox-net",
			networks:    map[string]*networkapi.EndpointSettings{"sandbox-net": {}},
		},
		{
			name:             "strict network plus the default network is rejected",
			networkName:      "sandbox-net",
			networks:         map[string]*networkapi.EndpointSettings{"sandbox-net": {}, "bridge": {}},
			wantErrSubstring: "requires only the internal network",
		},
		{
			name:             "missing strict network is rejected",
			networkName:      "sandbox-net",
			networks:         map[string]*networkapi.EndpointSettings{"bridge": {}},
			wantErrSubstring: "requires only the internal network",
		},
		{
			name:             "container without networks is rejected",
			networkName:      "sandbox-net",
			wantErrSubstring: "reports no networks",
		},
		{
			name:             "empty strict network name is rejected",
			networkName:      "  ",
			wantErrSubstring: "resolved network name is empty",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var settings *containerapi.NetworkSettings
			if test.networks != nil {
				settings = &containerapi.NetworkSettings{Networks: test.networks}
			}
			err := validateDockerSandboxStrictNetwork(containerapi.InspectResponse{
				ContainerJSONBase: &containerapi.ContainerJSONBase{ID: "container-1"},
				NetworkSettings:   settings,
			}, test.networkName)
			if test.wantErrSubstring == "" {
				if err != nil {
					t.Fatalf("validateDockerSandboxStrictNetwork() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErrSubstring) {
				t.Fatalf("validateDockerSandboxStrictNetwork() error = %v, want it to contain %q", err, test.wantErrSubstring)
			}
		})
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
