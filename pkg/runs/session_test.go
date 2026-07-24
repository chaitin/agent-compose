package runs

import (
	"testing"

	domain "agent-compose/pkg/model"
)

func TestSandboxTagsPersistCapabilityScope(t *testing.T) {
	tags := SandboxTags(domain.ProjectRunRecord{
		ProjectID:      " project-1 ",
		AgentName:      " worker ",
		ManagedAgentID: " agent-1 ",
		RunID:          " run-1 ",
	})

	values := make(map[string]string, len(tags))
	for _, tag := range tags {
		values[tag.Name] = tag.Value
	}
	if values["project"] != "project-1" {
		t.Fatalf("project tag = %q", values["project"])
	}
	if values[domain.AgentSandboxTagID] != "agent-1" {
		t.Fatalf("managed agent tag = %q", values[domain.AgentSandboxTagID])
	}
}
