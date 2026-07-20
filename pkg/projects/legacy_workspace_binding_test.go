package projects

import (
	"testing"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

func TestLegacyFileWorkspaceIDSupportsNamedAndReappliedInlineSpecs(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Workspaces: map[string]compose.WorkspaceSpec{
			"uploads": {Provider: "file", Path: "workspaces/legacy-file/content"},
		},
	}

	workspaceID, ok := legacyFileWorkspaceID(spec, compose.NormalizedAgentSpec{
		Workspace: &compose.WorkspaceSpec{Name: "uploads"},
	})
	if !ok || workspaceID != "legacy-file" {
		t.Fatalf("named legacy workspace = %q, %v", workspaceID, ok)
	}

	workspaceID, ok = legacyFileWorkspaceID(spec, compose.NormalizedAgentSpec{
		Workspace: &compose.WorkspaceSpec{Provider: "file", Path: "workspaces/legacy-file/content"},
	})
	if !ok || workspaceID != "legacy-file" {
		t.Fatalf("inline legacy workspace = %q, %v", workspaceID, ok)
	}

	if workspaceID, ok := legacyFileWorkspaceID(spec, compose.NormalizedAgentSpec{
		Workspace: &compose.WorkspaceSpec{Provider: "file", Path: "source"},
	}); ok || workspaceID != "" {
		t.Fatalf("ordinary local workspace was treated as legacy = %q, %v", workspaceID, ok)
	}
}

func TestLegacyFileWorkspaceBindingIsLimitedToSyntheticProjectIdentity(t *testing.T) {
	projectID, err := domain.StableProjectID(LegacyDefaultProjectName, "")
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	if !IsLegacyDefaultProject(domain.ProjectRecord{ID: projectID, Name: LegacyDefaultProjectName}) {
		t.Fatal("synthetic legacy project was not recognized")
	}
	if IsLegacyDefaultProject(domain.ProjectRecord{ID: projectID, Name: LegacyDefaultProjectName, SourcePath: "/repo"}) {
		t.Fatal("sourced project was treated as the synthetic legacy project")
	}
	if IsLegacyDefaultProject(domain.ProjectRecord{ID: "other", Name: LegacyDefaultProjectName}) {
		t.Fatal("different project identity was treated as the synthetic legacy project")
	}
}
