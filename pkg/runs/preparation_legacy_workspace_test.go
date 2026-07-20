package runs

import (
	"context"
	"fmt"
	"testing"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
)

func TestPrepareProjectRunReusesManagedAgentWorkspacePreset(t *testing.T) {
	projectID, err := domain.StableProjectID(projects.LegacyDefaultProjectName, "")
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	workspace := domain.WorkspaceConfig{
		ID:         "legacy-file",
		Name:       "Uploaded files",
		Type:       "file",
		ConfigJSON: `{"root":"/data/workspaces/legacy-file/content"}`,
	}
	store := &legacyWorkspacePreparationStoreStub{
		project: domain.ProjectRecord{ID: projectID, Name: projects.LegacyDefaultProjectName},
		revision: domain.ProjectRevisionRecord{
			ProjectID: projectID,
			Revision:  1,
			SpecJSON:  `{"name":"legacy-v1-default","agents":[{"name":"worker","workspace":{"provider":"file","path":"workspaces/legacy-file/content"}}]}`,
		},
		agent:     domain.AgentDefinition{ID: "managed-agent", WorkspaceID: workspace.ID},
		workspace: workspace,
	}
	resolver := &recordingPreparationWorkspaceResolver{}
	prepared, err := PrepareProjectRun(context.Background(), store, resolver, domain.ProjectRunRecord{
		RunID:           "run-1",
		ProjectID:       store.project.ID,
		ProjectRevision: store.revision.Revision,
		AgentName:       "worker",
		ManagedAgentID:  store.agent.ID,
	}, nil)
	if err != nil {
		t.Fatalf("PrepareProjectRun returned error: %v", err)
	}
	if resolver.called {
		t.Fatal("v2 workspace resolver was called for a persisted legacy preset binding")
	}
	if store.workspaceCalls != 1 {
		t.Fatalf("workspace config calls = %d, want 1", store.workspaceCalls)
	}
	if prepared.WorkspaceConfig == nil || prepared.WorkspaceConfig.ID != workspace.ID {
		t.Fatalf("prepared workspace config = %#v", prepared.WorkspaceConfig)
	}
	if prepared.Workspace == nil || prepared.Workspace.ID != workspace.ID || prepared.Workspace.ConfigJSON != workspace.ConfigJSON {
		t.Fatalf("prepared workspace snapshot = %#v", prepared.Workspace)
	}
}

func TestPrepareProjectRunKeepsGitAndOrdinaryV2OnWorkspaceResolver(t *testing.T) {
	legacyProjectID, err := domain.StableProjectID(projects.LegacyDefaultProjectName, "")
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	tests := []struct {
		name      string
		project   domain.ProjectRecord
		specJSON  string
		workspace domain.WorkspaceConfig
	}{
		{
			name:      "legacy git workspace",
			project:   domain.ProjectRecord{ID: legacyProjectID, Name: projects.LegacyDefaultProjectName},
			specJSON:  `{"name":"legacy-v1-default","agents":[{"name":"worker","workspace":{"provider":"git","url":"https://example.test/repo.git","ref":"main","target":"."}}]}`,
			workspace: domain.WorkspaceConfig{ID: "legacy-git", Type: "git", ConfigJSON: `{"url":"https://example.test/repo.git"}`},
		},
		{
			name:      "ordinary v2 local workspace",
			project:   domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: "/project/source"},
			specJSON:  `{"name":"project","agents":[{"name":"worker","workspace":{"provider":"file","path":"workspaces/legacy-file/content"}}]}`,
			workspace: domain.WorkspaceConfig{ID: "legacy-file", Type: "file", ConfigJSON: `{}`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &legacyWorkspacePreparationStoreStub{
				project: tt.project,
				revision: domain.ProjectRevisionRecord{
					ProjectID: tt.project.ID,
					Revision:  1,
					SpecJSON:  tt.specJSON,
				},
				agent:     domain.AgentDefinition{ID: "managed-agent", WorkspaceID: tt.workspace.ID},
				workspace: tt.workspace,
			}
			resolver := &recordingPreparationWorkspaceResolver{}
			_, err := PrepareProjectRun(context.Background(), store, resolver, domain.ProjectRunRecord{
				RunID:           "run-1",
				ProjectID:       tt.project.ID,
				ProjectRevision: 1,
				AgentName:       "worker",
				ManagedAgentID:  store.agent.ID,
			}, nil)
			if err != nil {
				t.Fatalf("PrepareProjectRun returned error: %v", err)
			}
			if !resolver.called {
				t.Fatal("v2 workspace resolver was not called")
			}
			if store.workspaceCalls != 0 {
				t.Fatalf("legacy workspace config calls = %d, want 0", store.workspaceCalls)
			}
		})
	}
}

type legacyWorkspacePreparationStoreStub struct {
	project        domain.ProjectRecord
	revision       domain.ProjectRevisionRecord
	agent          domain.AgentDefinition
	workspace      domain.WorkspaceConfig
	workspaceCalls int
}

func (s *legacyWorkspacePreparationStoreStub) GetProject(context.Context, string) (domain.ProjectRecord, error) {
	return s.project, nil
}

func (s *legacyWorkspacePreparationStoreStub) GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error) {
	return s.revision, nil
}

func (s *legacyWorkspacePreparationStoreStub) GetAgentDefinition(context.Context, string) (domain.AgentDefinition, error) {
	return s.agent, nil
}

func (s *legacyWorkspacePreparationStoreStub) GetWorkspaceConfig(_ context.Context, id string) (domain.WorkspaceConfig, error) {
	s.workspaceCalls++
	if id != s.workspace.ID {
		return domain.WorkspaceConfig{}, fmt.Errorf("workspace %s not found", id)
	}
	return s.workspace, nil
}

func (s *legacyWorkspacePreparationStoreStub) ListGlobalEnv(context.Context) ([]domain.SandboxEnvVar, error) {
	return nil, nil
}

func (s *legacyWorkspacePreparationStoreStub) ListProjectVolumes(context.Context, string) (map[string]domain.VolumeRecord, error) {
	return nil, nil
}

type recordingPreparationWorkspaceResolver struct {
	called bool
}

func (r *recordingPreparationWorkspaceResolver) ResolveProjectRunWorkspace(context.Context, domain.ProjectRunRecord, domain.ProjectRecord, *compose.WorkspaceSpec, *compose.WorkspaceSpec) (*domain.WorkspaceConfig, *domain.SandboxWorkspace, error) {
	r.called = true
	return nil, nil, nil
}

var _ WorkspaceResolver = (*recordingPreparationWorkspaceResolver)(nil)
var _ PreparationStore = (*legacyWorkspacePreparationStoreStub)(nil)
var _ legacyWorkspacePreparationStore = (*legacyWorkspacePreparationStoreStub)(nil)
