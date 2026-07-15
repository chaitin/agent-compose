package api

import (
	"reflect"
	"testing"
	"time"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"gopkg.in/yaml.v3"
)

func TestProjectNetworkProtoYAMLRoundTrip(t *testing.T) {
	normalized, err := compose.Normalize(&compose.ProjectSpec{
		Name: "demo",
		Networks: map[string]compose.NamedNetworkSpec{
			"frontend": {},
			"backend":  {Driver: "bridge"},
		},
		Agents: map[string]compose.AgentSpec{
			"api": {
				Networks: []string{"frontend", "backend"},
				Expose:   []string{"8080"},
				Ports:    []string{"8080"},
			},
		},
	}, compose.NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	protoSpec, err := ProjectSpecToProtoChecked(normalized)
	if err != nil {
		t.Fatalf("ProjectSpecToProtoChecked returned error: %v", err)
	}
	shape, issues := ProjectSpecYAMLShape(protoSpec)
	if len(issues) > 0 {
		t.Fatalf("ProjectSpecYAMLShape issues = %#v", issues)
	}
	networksShape, ok := shape["networks"].(map[string]any)
	if !ok || len(networksShape) != 2 {
		t.Fatalf("network shape = %#v", shape["networks"])
	}
	agentsShape := shape["agents"].(map[string]any)
	apiShape := agentsShape["api"].(map[string]any)
	if !reflect.DeepEqual(apiShape["networks"], []string{"frontend", "backend"}) || !reflect.DeepEqual(apiShape["expose"], []string{"8080/tcp"}) || !reflect.DeepEqual(apiShape["ports"], []string{"127.0.0.1:0:8080/tcp"}) {
		t.Fatalf("api network shape = %#v", apiShape)
	}
	data, err := yaml.Marshal(shape)
	if err != nil {
		t.Fatalf("yaml.Marshal returned error: %v", err)
	}
	roundTripped, err := compose.Parse(data)
	if err != nil {
		t.Fatalf("compose.Parse returned error: %v", err)
	}
	if _, err := compose.Normalize(roundTripped, compose.NormalizeOptions{}); err != nil {
		t.Fatalf("second Normalize returned error: %v", err)
	}
}

func TestNamedNetworkYAMLMapRejectsDuplicates(t *testing.T) {
	_, issues := NamedNetworkYAMLMap([]*agentcomposev2.NamedNetworkSpec{{Name: "frontend"}, {Name: "frontend"}})
	if len(issues) != 1 {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestNamedNetworkYAMLMapRejectsMissingName(t *testing.T) {
	_, issues := NamedNetworkYAMLMap([]*agentcomposev2.NamedNetworkSpec{{Driver: "bridge"}})
	if len(issues) != 1 || issues[0].GetPath() != "networks[0].name" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestSandboxNetworkStateToProtoMapsRuntimeDetails(t *testing.T) {
	reconciledAt := time.Date(2026, 7, 15, 10, 20, 30, 0, time.UTC)
	aliases := []string{"api", "api-sandbox"}
	state := &domain.SandboxNetworkState{
		Mode: "multi-network",
		Attachments: []domain.SandboxNetworkAttachmentState{{
			LogicalName: "frontend", RuntimeName: "agent-compose-p-project-1-frontend", NetworkID: "network-1",
			Aliases: aliases, IPv4Address: "172.30.0.2", Primary: true,
		}},
		PortBindings: []domain.SandboxPortBindingState{{ContainerPort: "8080/tcp", HostIP: "127.0.0.1", HostPort: "49152"}},
		ReconciledAt: reconciledAt,
	}
	result := SandboxNetworkStateToProto(state)
	if result.GetMode() != "multi-network" || !result.GetReconciledAt().AsTime().Equal(reconciledAt) || len(result.GetAttachments()) != 1 || len(result.GetPortBindings()) != 1 {
		t.Fatalf("network state proto = %#v", result)
	}
	attachment := result.GetAttachments()[0]
	if attachment.GetLogicalName() != "frontend" || attachment.GetRuntimeName() != "agent-compose-p-project-1-frontend" || attachment.GetNetworkId() != "network-1" || attachment.GetIpv4Address() != "172.30.0.2" || !attachment.GetPrimary() || !reflect.DeepEqual(attachment.GetAliases(), aliases) {
		t.Fatalf("network attachment proto = %#v", attachment)
	}
	binding := result.GetPortBindings()[0]
	if binding.GetContainerPort() != "8080/tcp" || binding.GetHostIp() != "127.0.0.1" || binding.GetHostPort() != "49152" {
		t.Fatalf("network port binding proto = %#v", binding)
	}
	aliases[0] = "mutated"
	if attachment.GetAliases()[0] != "api" {
		t.Fatalf("network attachment aliases were not cloned: %#v", attachment.GetAliases())
	}
	if SandboxNetworkStateToProto(nil) != nil {
		t.Fatal("nil network state returned non-nil proto")
	}
}

func TestIntegrationProjectNetworkProtoWorkflow(t *testing.T) {
	TestProjectNetworkProtoYAMLRoundTrip(t)
	TestNamedNetworkYAMLMapRejectsDuplicates(t)
	TestNamedNetworkYAMLMapRejectsMissingName(t)
	TestSandboxNetworkStateToProtoMapsRuntimeDetails(t)
	TestV2ListSandboxesUsesOpaquePagination(t)
}

func TestE2EProjectNetworkProtoWorkflow(t *testing.T) {
	TestIntegrationProjectNetworkProtoWorkflow(t)
}
