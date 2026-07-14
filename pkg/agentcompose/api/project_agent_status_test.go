package api

import (
	"testing"

	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestProjectAgentAvailabilityReflectsValidationAndDeclaration(t *testing.T) {
	items := ProjectAgentsToProto([]domain.ProjectAgentRecord{
		{ProjectID: "p", AgentName: "valid", ManagedAgentID: "managed-valid", Provider: "codex", SpecJSON: `{}`},
		{ProjectID: "p", AgentName: "disabled", ManagedAgentID: "managed-disabled", Provider: "codex", SpecJSON: `{"status":"disabled"}`},
		{ProjectID: "p", AgentName: "invalid", ManagedAgentID: "managed-invalid", Provider: "codex", SpecJSON: `{bad`},
	})
	if items[0].GetAvailability() != agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_AVAILABLE || items[0].GetHealth() != agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_HEALTHY {
		t.Fatalf("valid agent status = %#v", items[0])
	}
	if items[1].GetEnabled() || items[1].GetAvailability() != agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_UNAVAILABLE {
		t.Fatalf("disabled agent status = %#v", items[1])
	}
	if items[2].GetAvailability() != agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_VALIDATION_FAILED || items[2].GetHealth() != agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_AT_RISK {
		t.Fatalf("invalid agent status = %#v", items[2])
	}
}

func TestIntegrationProjectAgentAvailabilityReflectsCanonicalDisabledStatus(t *testing.T) {
	items := ProjectAgentsToProto([]domain.ProjectAgentRecord{
		{ProjectID: "p", AgentName: "disabled", ManagedAgentID: "managed-disabled", Provider: "codex", SpecJSON: `{"status":"disabled"}`},
	})
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].GetEnabled() || items[0].GetAvailability() != agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_UNAVAILABLE {
		t.Fatalf("disabled agent status = %#v", items[0])
	}
}
