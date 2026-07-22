package projects_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/samber/do/v2"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/internal/testutil"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/workspaces"
)

func TestIntegrationLegacyLoaderFileWorkspacePreservesSourceAndSchedulerBindings(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:           root,
		DbAddr:             filepath.Join(root, "data.db"),
		RuntimeDriver:      driverpkg.RuntimeDriverDocker,
		DockerDefaultImage: "guest:latest",
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("create config store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.DB().Close(); err != nil {
			t.Errorf("close config store: %v", err)
		}
	})

	workspace, err := store.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         "loader-upload",
		Name:       "Loader upload",
		Type:       "file",
		ConfigJSON: workspaces.DefaultFileConfigJSON(config, "loader-upload"),
	})
	if err != nil {
		t.Fatalf("create legacy workspace: %v", err)
	}
	content, err := workspaces.OpenFileWorkspaceContent(config, workspace)
	if err != nil {
		t.Fatalf("open legacy workspace content: %v", err)
	}
	if err := os.WriteFile(filepath.Join(content.AbsRoot, "migration.txt"), []byte("preserved\n"), 0o600); err != nil {
		if closeErr := content.Root.Close(); closeErr != nil {
			t.Errorf("close legacy workspace content after write failure: %v", closeErr)
		}
		t.Fatalf("write legacy workspace content: %v", err)
	}
	if err := content.Root.Close(); err != nil {
		t.Fatalf("close legacy workspace content: %v", err)
	}

	source, err := store.CreateAgentDefinition(ctx, domain.AgentDefinition{
		ID: "workspace-less-source", Name: "worker", Enabled: true, Provider: "codex",
		Driver: driverpkg.RuntimeDriverDocker, GuestImage: "guest:latest",
	})
	if err != nil {
		t.Fatalf("create source agent: %v", err)
	}
	loader, err := store.CreateLoader(ctx, domain.Loader{
		Summary: domain.LoaderSummary{
			ID: "loader-with-upload", Name: "uploaded task", Enabled: true,
			Runtime: domain.LoaderRuntimeScheduler, AgentID: source.ID, WorkspaceID: workspace.ID,
			Driver: driverpkg.RuntimeDriverDocker, GuestImage: "guest:latest", DefaultAgent: "codex",
		},
		Script: `scheduler.interval("uploaded", function uploaded() {}, 60000);`,
	})
	if err != nil {
		t.Fatalf("create loader: %v", err)
	}
	engine := &loaders.QJSLoaderEngine{}
	validation, err := engine.Validate(ctx, loader.Summary.Runtime, loader.Script)
	if err != nil {
		t.Fatalf("validate loader: %v", err)
	}
	if _, err := store.ReplaceLoaderTriggers(ctx, loader.Summary.ID, validation.Triggers); err != nil {
		t.Fatalf("persist loader triggers: %v", err)
	}

	controller := projects.NewController(projects.ControllerDependencies{
		Config: config, Store: store, Images: legacyProjectImageBackend{},
		Loaders: legacyProjectLoaderValidator{engine: engine}, Volumes: legacyProjectVolumeManager{},
	})
	result, err := controller.SyncLegacyDefaultProject(ctx)
	if err != nil {
		t.Fatalf("SyncLegacyDefaultProject returned error: %v", err)
	}
	if !result.Applied || len(result.Agents) != 2 || len(result.Schedulers) != 1 {
		t.Fatalf("migration result = %#v", result)
	}

	var sourceSpec, schedulerSpec *compose.NormalizedAgentSpec
	for index := range result.RevisionSpec.Agents {
		agent := &result.RevisionSpec.Agents[index]
		if agent.Scheduler == nil {
			sourceSpec = agent
		} else {
			schedulerSpec = agent
		}
	}
	if sourceSpec == nil || sourceSpec.Workspace != nil {
		t.Fatalf("workspace-less source changed: %#v", sourceSpec)
	}
	if schedulerSpec == nil || schedulerSpec.Workspace == nil {
		t.Fatalf("compatibility scheduler workspace = %#v", schedulerSpec)
	}

	var schedulerAgent domain.ProjectAgentRecord
	for _, agent := range result.Agents {
		if agent.AgentName == schedulerSpec.Name {
			schedulerAgent = agent
		}
	}
	if schedulerAgent.ManagedAgentID == "" {
		t.Fatalf("scheduler agent record not found: %#v", result.Agents)
	}
	definition, err := store.GetAgentDefinition(ctx, schedulerAgent.ManagedAgentID)
	if err != nil {
		t.Fatalf("load compatibility scheduler agent: %v", err)
	}
	if definition.WorkspaceID != workspace.ID {
		t.Fatalf("scheduler workspace ID = %q, want %q", definition.WorkspaceID, workspace.ID)
	}

	prepared, err := runs.PrepareProjectRun(ctx, store, rejectingWorkspaceResolver{}, domain.ProjectRunRecord{
		RunID: "migration-run", ProjectID: result.Project.ID, ProjectRevision: result.Revision.Revision,
		AgentName: schedulerAgent.AgentName, ManagedAgentID: schedulerAgent.ManagedAgentID,
	}, nil)
	if err != nil {
		t.Fatalf("PrepareProjectRun returned error: %v", err)
	}
	if prepared.WorkspaceConfig == nil || prepared.WorkspaceConfig.ID != workspace.ID || prepared.Workspace == nil || prepared.Workspace.ID != workspace.ID {
		t.Fatalf("prepared original uploaded workspace = %#v / %#v", prepared.WorkspaceConfig, prepared.Workspace)
	}
	if data, err := os.ReadFile(filepath.Join(content.AbsRoot, "migration.txt")); err != nil || string(data) != "preserved\n" {
		t.Fatalf("uploaded workspace content = %q, err = %v", data, err)
	}
}

type rejectingWorkspaceResolver struct{}

func (rejectingWorkspaceResolver) ResolveProjectRunWorkspace(context.Context, domain.ProjectRunRecord, domain.ProjectRecord, *compose.WorkspaceSpec, *compose.WorkspaceSpec) (*domain.WorkspaceConfig, *domain.SandboxWorkspace, error) {
	return nil, nil, fmt.Errorf("ordinary workspace resolver must not handle migrated file preset")
}
