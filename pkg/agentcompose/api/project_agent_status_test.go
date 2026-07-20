package api

import (
	"testing"

	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestProjectAgentAvailabilityReflectsValidationAndDeclaration(t *testing.T) {
	items := ProjectAgentsToProto([]domain.ProjectAgentRecord{
		{ProjectID: "p", AgentName: "valid", ManagedAgentID: "managed-valid", Provider: "codex", SpecJSON: `{}`},
		{ProjectID: "p", AgentName: "disabled", ManagedAgentID: "managed-disabled", Provider: "codex", SpecJSON: `{"enabled":false}`},
		{ProjectID: "p", AgentName: "invalid", ManagedAgentID: "managed-invalid", Provider: "codex", SpecJSON: `{bad`},
	})
	if items[0].GetAvailability() != agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_AVAILABLE || items[0].GetHealth() != agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_HEALTHY {
		t.Fatalf("enabled agent availability = %#v", items[0])
	}
	if items[1].GetEnabled() || items[1].GetAvailability() != agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_UNAVAILABLE {
		t.Fatalf("disabled agent availability = %#v", items[1])
	}
	if items[2].GetAvailability() != agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_VALIDATION_FAILED || items[2].GetHealth() != agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_AT_RISK {
		t.Fatalf("invalid agent availability = %#v", items[2])
	}
}

func TestIntegrationProjectAgentAvailabilityReflectsCanonicalDisabledEnablement(t *testing.T) {
	items := ProjectAgentsToProto([]domain.ProjectAgentRecord{
		{ProjectID: "p", AgentName: "disabled", ManagedAgentID: "managed-disabled", Provider: "codex", SpecJSON: `{"enabled":false}`},
	})
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if items[0].GetEnabled() || items[0].GetAvailability() != agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_UNAVAILABLE {
		t.Fatalf("disabled agent availability = %#v", items[0])
	}
}

func TestProjectAgentIncludesPresentationMetadataFromCanonicalSpec(t *testing.T) {
	items := ProjectAgentsToProto([]domain.ProjectAgentRecord{{
		ProjectID:      "p",
		AgentName:      "legacy-agent-bfe5286dc77f",
		ManagedAgentID: "managed-agent",
		Provider:       "codex",
		SpecJSON:       `{"name":"legacy-agent-bfe5286dc77f","display_name":"通用助手","description":"处理日常通用任务","enabled":true}`,
	}})
	if len(items) != 1 || items[0].GetDisplayName() != "通用助手" || items[0].GetDescription() != "处理日常通用任务" {
		t.Fatalf("project agent metadata = %#v", items)
	}
}

func TestProjectSchedulerIncludesPresentationMetadataFromCanonicalSpec(t *testing.T) {
	items := ProjectSchedulersToProto([]domain.ProjectSchedulerRecord{{
		ProjectID:   "p",
		AgentName:   "legacy-agent-bfe5286dc77f",
		SchedulerID: "scheduler",
		SpecJSON:    `{"enabled":true,"display_name":"每日巡检","description":"每天汇总巡检结果"}`,
	}})
	if len(items) != 1 || items[0].GetDisplayName() != "每日巡检" || items[0].GetDescription() != "每天汇总巡检结果" {
		t.Fatalf("project scheduler metadata = %#v", items)
	}
}
