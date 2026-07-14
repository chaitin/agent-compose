package runs

import (
	"strings"
	"testing"

	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestSandboxNetworkIntentFromV2(t *testing.T) {
	run := domain.ProjectRunRecord{ProjectID: "project-1", ProjectName: "demo", AgentName: "api"}
	intent, err := SandboxNetworkIntentFromV2(run,
		[]*agentcomposev2.NamedNetworkSpec{{Name: "frontend", Driver: "port_mapping"}},
		&agentcomposev2.AgentSpec{
			Networks: []string{"frontend"},
			Expose:   []*agentcomposev2.ExposedPortSpec{{Target: 8080, Protocol: "tcp"}},
			Ports:    []*agentcomposev2.PublishedPortSpec{{HostIp: "127.0.0.1", Published: 19000, Target: 9000, Protocol: "tcp"}},
		},
	)
	if err != nil {
		t.Fatalf("SandboxNetworkIntentFromV2() error = %v", err)
	}
	if intent.ProjectID != "project-1" || intent.ProjectName != "demo" || intent.AgentName != "api" {
		t.Fatalf("identity = %#v", intent)
	}
	if len(intent.Attachments) != 1 || intent.Attachments[0].Name != "frontend" || intent.Attachments[0].Driver != "port_mapping" {
		t.Fatalf("attachments = %#v", intent.Attachments)
	}
	if len(intent.Expose) != 1 || intent.Expose[0].Target != 8080 || len(intent.Ports) != 1 || intent.Ports[0].Published != 19000 {
		t.Fatalf("ports = expose %#v published %#v", intent.Expose, intent.Ports)
	}
}

func TestSandboxNetworkIntentFromV2ReturnsNilWithoutConfiguration(t *testing.T) {
	intent, err := SandboxNetworkIntentFromV2(domain.ProjectRunRecord{}, nil, &agentcomposev2.AgentSpec{})
	if err != nil || intent != nil {
		t.Fatalf("SandboxNetworkIntentFromV2() = %#v, %v", intent, err)
	}
}

func TestSandboxNetworkIntentFromV2RejectsInvalidRevisionNetworkData(t *testing.T) {
	tests := []struct {
		name     string
		networks []*agentcomposev2.NamedNetworkSpec
		agent    *agentcomposev2.AgentSpec
		contains string
	}{
		{
			name:     "unknown attachment",
			networks: []*agentcomposev2.NamedNetworkSpec{{Name: "frontend", Driver: "port_mapping"}},
			agent:    &agentcomposev2.AgentSpec{Networks: []string{"missing"}},
			contains: "unknown project network",
		},
		{
			name:     "expose without attachment",
			agent:    &agentcomposev2.AgentSpec{Expose: []*agentcomposev2.ExposedPortSpec{{Target: 8080}}},
			contains: "requires at least one network attachment",
		},
		{
			name:     "udp",
			agent:    &agentcomposev2.AgentSpec{Ports: []*agentcomposev2.PublishedPortSpec{{Target: 8080, Protocol: "udp"}}},
			contains: "only TCP ports are supported",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SandboxNetworkIntentFromV2(domain.ProjectRunRecord{}, tt.networks, tt.agent)
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("SandboxNetworkIntentFromV2() error = %v", err)
			}
		})
	}
}
