package model_test

import (
	"agent-compose/pkg/identity"
	"path/filepath"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestProjectStableIDHelpers(t *testing.T) {
	projectID, err := domain.StableProjectID("demo", filepath.Join("tmp", "agent-compose.yml"))
	if err != nil {
		t.Fatalf("domain.StableProjectID returned error: %v", err)
	}
	sameProjectID, err := domain.StableProjectID("demo", filepath.Join("tmp", "agent-compose.yml"))
	if err != nil {
		t.Fatalf("second domain.StableProjectID returned error: %v", err)
	}
	if sameProjectID != projectID {
		t.Fatalf("project id changed: %q != %q", sameProjectID, projectID)
	}
	otherProjectID, err := domain.StableProjectID("demo", filepath.Join("other", "agent-compose.yml"))
	if err != nil {
		t.Fatalf("other domain.StableProjectID returned error: %v", err)
	}
	if otherProjectID == projectID {
		t.Fatalf("project id did not include source path: %q", projectID)
	}

	agentID, err := domain.StableManagedAgentID(projectID, "reviewer")
	if err != nil {
		t.Fatalf("domain.StableManagedAgentID returned error: %v", err)
	}
	if again, err := domain.StableManagedAgentID(projectID, "reviewer"); err != nil || again != agentID {
		t.Fatalf("stable agent id = %q, %v; want %q", again, err, agentID)
	}
	schedulerID, err := domain.StableProjectSchedulerID(projectID, "reviewer", "")
	if err != nil {
		t.Fatalf("domain.StableProjectSchedulerID returned error: %v", err)
	}
	loaderID, err := domain.StableManagedLoaderID(projectID, "reviewer", "")
	if err != nil {
		t.Fatalf("domain.StableManagedLoaderID returned error: %v", err)
	}
	runID, err := domain.StableProjectRunID(projectID, "reviewer", domain.ProjectRunSourceManual, "client-request-1")
	if err != nil {
		t.Fatalf("domain.StableProjectRunID returned error: %v", err)
	}
	otherRunID, err := domain.StableProjectRunID(projectID, "reviewer", domain.ProjectRunSourceManual, "client-request-2")
	if err != nil {
		t.Fatalf("other domain.StableProjectRunID returned error: %v", err)
	}
	for label, id := range map[string]string{
		"project":   projectID,
		"agent":     agentID,
		"scheduler": schedulerID,
		"loader":    loaderID,
		"run":       runID,
	} {
		if !identity.IsID(id) {
			t.Fatalf("%s id = %q, want sha256 id", label, id)
		}
		if shortID := identity.ShortID(id); !identity.IsShortID(shortID) {
			t.Fatalf("%s short id = %q, want 12-char hex short id", label, shortID)
		}
	}
	if otherRunID == runID {
		t.Fatalf("run id did not include idempotency key: %q", runID)
	}
	if _, err := domain.StableProjectID("Demo", ""); err == nil {
		t.Fatalf("domain.StableProjectID accepted non-normalized project name")
	}
	if _, err := domain.StableManagedAgentID(projectID, "Bad Agent"); err == nil {
		t.Fatalf("domain.StableManagedAgentID accepted non-normalized agent name")
	}
}
