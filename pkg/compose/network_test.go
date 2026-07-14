package compose

import (
	"strings"
	"testing"
)

func TestNormalizeComposeNetworksAndTCPPorts(t *testing.T) {
	spec := mustParseNetworkCompose(t, `
name: network-demo
networks:
  frontend: {}
  backend:
    driver: port_mapping
agents:
  api:
    networks: [frontend, backend]
    expose: ["9000"]
    ports:
      - "127.0.0.1:19000:9000"
      - "9001"
  worker:
    networks: [backend]
`)

	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got := normalized.Networks["frontend"].Driver; got != NetworkDriverPortMapping {
		t.Fatalf("frontend driver = %q, want %q", got, NetworkDriverPortMapping)
	}
	api := normalized.Agents[0]
	if api.Name != "api" || strings.Join(api.Networks, ",") != "frontend,backend" {
		t.Fatalf("api networks = %#v", api.Networks)
	}
	if len(api.Expose) != 1 || api.Expose[0] != (ExposedPortSpec{Target: 9000, Protocol: "tcp"}) {
		t.Fatalf("api expose = %#v", api.Expose)
	}
	if len(api.Ports) != 2 {
		t.Fatalf("api ports = %#v", api.Ports)
	}
	if got := api.Ports[0]; got != (PublishedPortSpec{HostIP: "127.0.0.1", Published: 0, Target: 9001, Protocol: "tcp"}) {
		t.Fatalf("dynamic port = %#v", got)
	}
	if got := api.Ports[1]; got != (PublishedPortSpec{HostIP: "127.0.0.1", Published: 19000, Target: 9000, Protocol: "tcp"}) {
		t.Fatalf("fixed port = %#v", got)
	}
}

func TestNormalizeComposeNetworkAddsImplicitDefault(t *testing.T) {
	spec := mustParseNetworkCompose(t, `
name: network-default
agents:
  api:
    expose: ["8080"]
  worker: {}
`)
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got := normalized.Networks["default"].Driver; got != NetworkDriverPortMapping {
		t.Fatalf("default driver = %q", got)
	}
	for _, agent := range normalized.Agents {
		if len(agent.Networks) != 1 || agent.Networks[0] != "default" {
			t.Fatalf("%s networks = %#v", agent.Name, agent.Networks)
		}
	}
}

func TestNormalizeComposePortsWithoutNetworksDoesNotEnableInternalNetworking(t *testing.T) {
	spec := mustParseNetworkCompose(t, `
name: external-only
agents:
  api:
    ports: ["19000:9000"]
`)
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(normalized.Networks) != 0 || len(normalized.Agents[0].Networks) != 0 {
		t.Fatalf("ports unexpectedly enabled internal networking: project=%#v agent=%#v", normalized.Networks, normalized.Agents[0].Networks)
	}
}

func TestNormalizeComposeNetworkValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "unknown network",
			raw:  "name: demo\nnetworks:\n  frontend: {}\nagents:\n  api:\n    networks: [missing]\n",
			want: `unknown network "missing"`,
		},
		{
			name: "unsupported driver",
			raw:  "name: demo\nnetworks:\n  frontend:\n    driver: bridge\nagents:\n  api: {}\n",
			want: `unsupported network driver "bridge"`,
		},
		{
			name: "udp expose",
			raw:  "name: demo\nagents:\n  api:\n    expose: [\"53/udp\"]\n",
			want: "only TCP ports are supported",
		},
		{
			name: "invalid host IP",
			raw:  "name: demo\nagents:\n  api:\n    ports: [\"localhost:19000:9000\"]\n",
			want: "host IP must be a valid IPv4 address",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, err := Parse([]byte(test.raw))
			if err == nil {
				_, err = Normalize(spec, NormalizeOptions{})
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Normalize() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestComposeNetworkCanonicalRoundTrip(t *testing.T) {
	spec := mustParseNetworkCompose(t, `
name: round-trip
networks:
  backend: {}
agents:
  api:
    networks: [backend]
    expose: ["9000"]
    ports: ["0.0.0.0:19000:9000"]
`)
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	data, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON() error = %v", err)
	}
	if !strings.Contains(string(data), `"networks":[{"name":"backend","driver":"port_mapping"}]`) {
		t.Fatalf("canonical JSON missing ordered networks: %s", data)
	}
	if !strings.Contains(string(data), `"ports":[{"host_ip":"0.0.0.0","published":19000,"target":9000,"protocol":"tcp"}]`) {
		t.Fatalf("canonical JSON missing port mapping: %s", data)
	}
}

func mustParseNetworkCompose(t *testing.T, raw string) *ProjectSpec {
	t.Helper()
	spec, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return spec
}
