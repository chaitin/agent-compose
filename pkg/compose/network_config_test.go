package compose

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseNamedNetworksAndAgentNetworkFields(t *testing.T) {
	spec, err := Parse([]byte(`name: demo
networks:
  frontend: {}
  backend:
    driver: bridge
agents:
  api:
    networks: [frontend, backend]
    expose: ["8080"]
    ports: ["18080:8080"]
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(spec.Networks) != 2 || spec.Networks["backend"].Driver != "bridge" {
		t.Fatalf("networks = %#v", spec.Networks)
	}
	agent := spec.Agents["api"]
	if !reflect.DeepEqual(agent.Networks, []string{"frontend", "backend"}) ||
		!reflect.DeepEqual(agent.Expose, []string{"8080"}) ||
		!reflect.DeepEqual(agent.Ports, []string{"18080:8080"}) {
		t.Fatalf("agent network fields = %#v / %#v / %#v", agent.Networks, agent.Expose, agent.Ports)
	}
}

func TestNormalizeNamedNetworksAndPorts(t *testing.T) {
	spec := &ProjectSpec{
		Name: "demo",
		Networks: map[string]NamedNetworkSpec{
			"frontend": {},
			"backend":  {Driver: "BRIDGE"},
		},
		Agents: map[string]AgentSpec{
			"api": {
				Networks: []string{"frontend", "backend"},
				Expose:   []string{"8080"},
				Ports:    []string{"18080:8080", "0.0.0.0:19090:9090/tcp"},
			},
			"worker": {},
		},
	}
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Networks["frontend"].Driver != "bridge" || normalized.Networks["backend"].Driver != "bridge" || normalized.Networks["default"].Driver != "bridge" {
		t.Fatalf("normalized networks = %#v", normalized.Networks)
	}
	if len(normalized.Agents) != 2 || normalized.Agents[0].Name != "api" || normalized.Agents[1].Name != "worker" {
		t.Fatalf("normalized agents = %#v", normalized.Agents)
	}
	api := normalized.Agents[0]
	if strings.Join(api.Networks, ",") != "frontend,backend" {
		t.Fatalf("api networks = %#v", api.Networks)
	}
	if strings.Join(api.Expose, ",") != "8080/tcp" {
		t.Fatalf("api expose = %#v", api.Expose)
	}
	if strings.Join(api.Ports, ",") != "127.0.0.1:18080:8080/tcp,0.0.0.0:19090:9090/tcp" {
		t.Fatalf("api ports = %#v", api.Ports)
	}
	if strings.Join(normalized.Agents[1].Networks, ",") != "default" {
		t.Fatalf("worker networks = %#v", normalized.Agents[1].Networks)
	}
	canonical, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	for _, want := range []string{`"name":"backend","driver":"bridge"`, `"networks":["frontend","backend"]`, `"ports":["127.0.0.1:18080:8080/tcp"`} {
		if !strings.Contains(string(canonical), want) {
			t.Fatalf("canonical JSON %s missing %s", canonical, want)
		}
	}
}

func TestNormalizeWithoutNamedNetworksPreservesBaseline(t *testing.T) {
	normalized, err := Normalize(&ProjectSpec{Name: "demo", Agents: map[string]AgentSpec{"worker": {}}}, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Networks != nil || normalized.Agents[0].Networks != nil {
		t.Fatalf("baseline project unexpectedly gained named networks: %#v / %#v", normalized.Networks, normalized.Agents[0].Networks)
	}
}

func TestNormalizePublishedPortIsIdempotentForDynamicHostPort(t *testing.T) {
	first, err := normalizePublishedPorts("agents.api.ports", []string{"8080"})
	if err != nil {
		t.Fatalf("first normalization returned error: %v", err)
	}
	second, err := normalizePublishedPorts("agents.api.ports", first)
	if err != nil {
		t.Fatalf("second normalization returned error: %v", err)
	}
	if !reflect.DeepEqual(first, []string{"127.0.0.1:0:8080/tcp"}) || !reflect.DeepEqual(second, first) {
		t.Fatalf("published ports first=%#v second=%#v", first, second)
	}
}

func TestNormalizeNetworkValidation(t *testing.T) {
	tests := []struct {
		name string
		spec *ProjectSpec
		want string
	}{
		{
			name: "unsupported driver",
			spec: &ProjectSpec{Name: "demo", Networks: map[string]NamedNetworkSpec{"frontend": {Driver: "overlay"}}},
			want: "only bridge is supported",
		},
		{
			name: "missing top level",
			spec: &ProjectSpec{Name: "demo", Agents: map[string]AgentSpec{"worker": {Networks: []string{"frontend"}}}},
			want: "agent networks require top-level networks",
		},
		{
			name: "undefined attachment",
			spec: &ProjectSpec{Name: "demo", Networks: map[string]NamedNetworkSpec{"frontend": {}}, Agents: map[string]AgentSpec{"worker": {Networks: []string{"backend"}}}},
			want: `network "backend" is not defined`,
		},
		{
			name: "duplicate attachment",
			spec: &ProjectSpec{Name: "demo", Networks: map[string]NamedNetworkSpec{"frontend": {}}, Agents: map[string]AgentSpec{"worker": {Networks: []string{"frontend", "frontend"}}}},
			want: `duplicate network "frontend"`,
		},
		{
			name: "non docker agent",
			spec: &ProjectSpec{Name: "demo", Networks: map[string]NamedNetworkSpec{"frontend": {}}, Agents: map[string]AgentSpec{"worker": {Driver: &DriverSpec{Boxlite: &BoxliteDriverSpec{}}}}},
			want: "require every agent to use the docker driver",
		},
		{
			name: "invalid exposed protocol",
			spec: &ProjectSpec{Name: "demo", Agents: map[string]AgentSpec{"worker": {Expose: []string{"53/udp"}}}},
			want: "only tcp is supported",
		},
		{
			name: "invalid published host",
			spec: &ProjectSpec{Name: "demo", Agents: map[string]AgentSpec{"worker": {Ports: []string{"localhost:8080:80"}}}},
			want: `host IP "localhost" is invalid`,
		},
		{
			name: "unbracketed IPv6 published host",
			spec: &ProjectSpec{Name: "demo", Agents: map[string]AgentSpec{"worker": {Ports: []string{"::1:8080:80"}}}},
			want: "published port must be container, host:container, or host_ip:host:container",
		},
		{
			name: "bracketed IPv6 published host",
			spec: &ProjectSpec{Name: "demo", Agents: map[string]AgentSpec{"worker": {Ports: []string{"[::1]:8080:80"}}}},
			want: "published port must be container, host:container, or host_ip:host:container",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Normalize(tc.spec, NormalizeOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Normalize error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestIntegrationNamedNetworkConfigurationWorkflow(t *testing.T) {
	TestParseNamedNetworksAndAgentNetworkFields(t)
	TestNormalizeNamedNetworksAndPorts(t)
	TestNormalizeWithoutNamedNetworksPreservesBaseline(t)
	TestNormalizePublishedPortIsIdempotentForDynamicHostPort(t)
	TestNormalizeNetworkValidation(t)
}

func TestE2ENamedNetworkConfigurationWorkflow(t *testing.T) {
	TestIntegrationNamedNetworkConfigurationWorkflow(t)
}
