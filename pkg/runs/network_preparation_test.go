package runs

import (
	"reflect"
	"testing"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestNetworkIntentFromProjectSpec(t *testing.T) {
	agent := &agentcomposev2.AgentSpec{
		Name:     "api",
		Networks: []string{"frontend", "backend"},
		Expose:   []string{"8080/tcp"},
		Ports:    []string{"127.0.0.1:18080:8080/tcp"},
	}
	intent := networkIntentFromProjectSpec("project-1", 7, "api", &agentcomposev2.ProjectSpec{
		Networks: []*agentcomposev2.NamedNetworkSpec{
			{Name: "backend", Driver: "bridge"},
			{Name: "frontend", Driver: "bridge"},
		},
	}, agent)
	if intent == nil || intent.Version != 1 || intent.ProjectID != "project-1" || intent.ProjectRevision != 7 || intent.AgentName != "api" {
		t.Fatalf("intent = %#v", intent)
	}
	if !reflect.DeepEqual(intent.Attachments, agent.Networks) || !reflect.DeepEqual(intent.Expose, agent.Expose) || !reflect.DeepEqual(intent.Ports, agent.Ports) {
		t.Fatalf("intent lists = %#v", intent)
	}
	if len(intent.Definitions) != 2 || intent.Definitions[0].Name != "backend" || intent.Definitions[1].Name != "frontend" {
		t.Fatalf("definitions = %#v", intent.Definitions)
	}
}

func TestNetworkIntentFromProjectSpecKeepsBaselineWithoutNamedNetworks(t *testing.T) {
	if intent := networkIntentFromProjectSpec("project-1", 1, "api", &agentcomposev2.ProjectSpec{}, &agentcomposev2.AgentSpec{Name: "api"}); intent != nil {
		t.Fatalf("baseline intent = %#v, want nil", intent)
	}
}

func TestNetworkIntentFromProjectSpecKeepsPortOnlyConfigurationOnBaseline(t *testing.T) {
	intent := networkIntentFromProjectSpec("project-1", 3, "api", &agentcomposev2.ProjectSpec{}, &agentcomposev2.AgentSpec{
		Name:   "api",
		Expose: []string{"8080/tcp"},
		Ports:  []string{"127.0.0.1:0:8080/tcp"},
	})
	if intent == nil || intent.ProjectRevision != 3 || len(intent.Attachments) != 0 || !reflect.DeepEqual(intent.Expose, []string{"8080/tcp"}) || !reflect.DeepEqual(intent.Ports, []string{"127.0.0.1:0:8080/tcp"}) {
		t.Fatalf("port-only intent = %#v", intent)
	}
}

func TestIntegrationProjectRunNetworkIntentWorkflow(t *testing.T) {
	TestNetworkIntentFromProjectSpec(t)
	TestNetworkIntentFromProjectSpecKeepsBaselineWithoutNamedNetworks(t)
	TestNetworkIntentFromProjectSpecKeepsPortOnlyConfigurationOnBaseline(t)
}

func TestE2EProjectRunNetworkIntentWorkflow(t *testing.T) {
	TestIntegrationProjectRunNetworkIntentWorkflow(t)
}
