package networks

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildPlanKeepsSingleBridgeBaseline(t *testing.T) {
	plan, err := BuildPlan(Intent{}, "compose_default")
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	if plan.Mode != ModeSingleBridge || plan.RequiresDaemonHost || len(plan.Attachments) != 1 || plan.Attachments[0].RuntimeName != "compose_default" || plan.Attachments[0].Managed {
		t.Fatalf("single bridge plan = %#v", plan)
	}
}

func TestBuildPlanTreatsOneAdditionalBridgeAsMultiNetwork(t *testing.T) {
	plan, err := BuildPlan(Intent{
		ProjectID:   "project-123",
		AgentName:   "api",
		SandboxID:   "sandbox-abcdef1234567890",
		Definitions: map[string]Definition{"frontend": {Name: "frontend", Driver: "bridge"}},
		Attachments: []string{"frontend"},
	}, "compose_default")
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	if plan.Mode != ModeMultiNetwork || !plan.RequiresDaemonHost || len(plan.Attachments) != 1 {
		t.Fatalf("multi-network plan = %#v", plan)
	}
	attachment := plan.Attachments[0]
	if attachment.RuntimeName != "agent-compose-p-project-123-frontend" || attachment.GatewayPriority != 100 || !attachment.Managed {
		t.Fatalf("attachment = %#v", attachment)
	}
	if !reflect.DeepEqual(attachment.Aliases, []string{"api", "api-sandbox-abcd"}) {
		t.Fatalf("aliases = %#v", attachment.Aliases)
	}
	if attachment.Labels[ProjectIDLabel] != "project-123" || attachment.Labels[LogicalNameLabel] != "frontend" {
		t.Fatalf("labels = %#v", attachment.Labels)
	}
}

func TestBuildPlanPreservesAttachmentOrder(t *testing.T) {
	plan, err := BuildPlan(Intent{
		ProjectID: "p1",
		Definitions: map[string]Definition{
			"frontend": {Driver: "bridge"},
			"backend":  {Driver: "bridge"},
		},
		Attachments: []string{"backend", "frontend"},
	}, "bridge")
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	if plan.Attachments[0].LogicalName != "backend" || plan.Attachments[0].GatewayPriority != 100 || plan.Attachments[1].LogicalName != "frontend" || plan.Attachments[1].GatewayPriority != 0 {
		t.Fatalf("attachments = %#v", plan.Attachments)
	}
}

func TestBuildPlanRejectsInvalidIntent(t *testing.T) {
	tests := []Intent{
		{Attachments: []string{"frontend"}, Definitions: map[string]Definition{"frontend": {Driver: "bridge"}}},
		{ProjectID: "p1", Attachments: []string{"missing"}, Definitions: map[string]Definition{}},
		{ProjectID: "p1", Attachments: []string{"frontend", "frontend"}, Definitions: map[string]Definition{"frontend": {Driver: "bridge"}}},
		{ProjectID: "p1", Attachments: []string{"frontend"}, Definitions: map[string]Definition{"frontend": {Driver: "overlay"}}},
	}
	for _, intent := range tests {
		if _, err := BuildPlan(intent, "bridge"); err == nil {
			t.Fatalf("BuildPlan(%#v) returned nil error", intent)
		}
	}
}

func TestRuntimeNetworkNameDistinguishesLongProjectNetworks(t *testing.T) {
	projectID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	frontend := RuntimeNetworkName(projectID, "frontend")
	backend := RuntimeNetworkName(projectID, "backend")
	if frontend == backend {
		t.Fatalf("runtime network names collided: %q", frontend)
	}
	for _, name := range []string{frontend, backend} {
		if len(name) > 63 {
			t.Fatalf("runtime network name length = %d: %q", len(name), name)
		}
		if !strings.Contains(name, projectID[:12]) {
			t.Fatalf("runtime network name %q does not contain project short ID", name)
		}
	}
}

func TestIntegrationNamedNetworkPlanWorkflow(t *testing.T) {
	TestBuildPlanKeepsSingleBridgeBaseline(t)
	TestBuildPlanTreatsOneAdditionalBridgeAsMultiNetwork(t)
	TestBuildPlanPreservesAttachmentOrder(t)
	TestBuildPlanRejectsInvalidIntent(t)
	TestRuntimeNetworkNameDistinguishesLongProjectNetworks(t)
}

func TestE2ENamedNetworkPlanWorkflow(t *testing.T) {
	TestIntegrationNamedNetworkPlanWorkflow(t)
}
