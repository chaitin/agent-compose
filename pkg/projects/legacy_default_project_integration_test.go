package projects_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/images"
	"agent-compose/pkg/internal/testutil"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/workspaces"
)

func TestIntegrationLegacyDefaultProjectAdoptsLoaderHistoryAtCurrentRevision(t *testing.T) {
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
		ID:         "legacy-file-workspace",
		Name:       "news-source",
		Type:       "file",
		ConfigJSON: workspaces.DefaultFileConfigJSON(config, "legacy-file-workspace"),
	})
	if err != nil {
		t.Fatalf("create legacy workspace: %v", err)
	}
	agent, err := store.CreateAgentDefinition(ctx, domain.AgentDefinition{
		ID:          "legacy-agent-source",
		Name:        "ai资讯推送",
		Description: "每日推送最新的 AI 资讯",
		Enabled:     true,
		Provider:    "codex",
		Driver:      driverpkg.RuntimeDriverDocker,
		GuestImage:  "guest:latest",
		WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatalf("create legacy agent: %v", err)
	}
	const script = `
scheduler.interval("hourly", function hourly() {}, 3600000);
scheduler.on("news.ready", "on-news", function onNews() {});
`
	loader, err := store.CreateLoader(ctx, domain.Loader{
		Summary: domain.LoaderSummary{
			ID:            "legacy-loader-source",
			Name:          "资讯推送任务",
			Description:   "每小时检查并推送资讯",
			Enabled:       false,
			Runtime:       domain.LoaderRuntimeScheduler,
			AgentID:       agent.ID,
			WorkspaceID:   workspace.ID,
			Driver:        driverpkg.RuntimeDriverDocker,
			GuestImage:    "guest:latest",
			DefaultAgent:  "codex",
			SandboxPolicy: domain.LoaderSandboxPolicySticky,
		},
		Script: script,
	})
	if err != nil {
		t.Fatalf("create legacy loader: %v", err)
	}
	engine := &loaders.QJSLoaderEngine{}
	validation, err := engine.Validate(ctx, loader.Summary.Runtime, loader.Script)
	if err != nil {
		t.Fatalf("validate legacy loader: %v", err)
	}
	if _, err := store.ReplaceLoaderTriggers(ctx, loader.Summary.ID, validation.Triggers); err != nil {
		t.Fatalf("create legacy triggers: %v", err)
	}
	if err := store.SetLoaderTriggerEnabled(ctx, loader.Summary.ID, "on-news", false); err != nil {
		t.Fatalf("disable legacy trigger: %v", err)
	}
	startedAt := time.Now().UTC().Add(-time.Hour)
	if err := store.CreateLoaderRun(ctx, domain.LoaderRunSummary{
		ID: "legacy-run", LoaderID: loader.Summary.ID, TriggerID: "hourly", TriggerKind: domain.LoaderTriggerKindInterval,
		Status: domain.LoaderRunStatusSucceeded, StartedAt: startedAt, CompletedAt: startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("create legacy run: %v", err)
	}
	if err := store.AddLoaderEvent(ctx, domain.LoaderEvent{
		ID: "legacy-event", LoaderID: loader.Summary.ID, RunID: "legacy-run", Type: "loader.completed", CreatedAt: startedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("create legacy event: %v", err)
	}

	controller := projects.NewController(projects.ControllerDependencies{
		Config:  config,
		Store:   store,
		Images:  legacyProjectImageBackend{},
		Loaders: legacyProjectLoaderValidator{engine: engine},
		Volumes: legacyProjectVolumeManager{},
	})
	result, err := controller.SyncLegacyDefaultProject(ctx)
	if err != nil {
		t.Fatalf("SyncLegacyDefaultProject returned error: %v", err)
	}
	if !result.Applied || len(result.Issues) != 0 {
		t.Fatalf("sync result = %#v", result)
	}
	if len(result.Agents) != 1 || !strings.HasPrefix(result.Agents[0].AgentName, "legacy-agent-") {
		t.Fatalf("projected Chinese agents = %#v", result.Agents)
	}
	if result.Project.SourcePath != "" || result.RevisionSpec == nil || len(result.RevisionSpec.Workspaces) != 1 || len(result.RevisionSpec.Agents) != 1 {
		t.Fatalf("projected file workspace project = %#v, spec = %#v", result.Project, result.RevisionSpec)
	}
	projectedAgent := result.RevisionSpec.Agents[0]
	if projectedAgent.DisplayName != "ai资讯推送" || projectedAgent.Description != "每日推送最新的 AI 资讯" {
		t.Fatalf("projected agent metadata = %#v", projectedAgent)
	}
	if projectedAgent.Scheduler == nil || projectedAgent.Scheduler.DisplayName != "资讯推送任务" || projectedAgent.Scheduler.Description != "每小时检查并推送资讯" {
		t.Fatalf("projected scheduler metadata = %#v", projectedAgent.Scheduler)
	}
	if !strings.Contains(result.Agents[0].SpecJSON, `"display_name":"ai资讯推送"`) || !strings.Contains(result.Agents[0].SpecJSON, `"description":"每日推送最新的 AI 资讯"`) {
		t.Fatalf("persisted project agent spec = %s", result.Agents[0].SpecJSON)
	}
	if projectedAgent.Workspace == nil || projectedAgent.Workspace.Name != "news-source" {
		t.Fatalf("projected agent workspace = %#v", projectedAgent.Workspace)
	}
	projectedWorkspace := result.RevisionSpec.Workspaces["news-source"]
	wantWorkspacePath := filepath.ToSlash(filepath.Join("workspaces", workspace.ID, workspaces.FileWorkspaceContentDirName))
	if projectedWorkspace.Provider != "file" || projectedWorkspace.Path != wantWorkspacePath {
		t.Fatalf("projected workspace = %#v, want path %q", projectedWorkspace, wantWorkspacePath)
	}

	projectID, err := domain.StableProjectID(projects.LegacyDefaultProjectName, "")
	if err != nil {
		t.Fatalf("stable project id: %v", err)
	}
	project, err := store.GetProject(ctx, projectID)
	if err != nil {
		t.Fatalf("load legacy project: %v", err)
	}
	resolved, err := controller.ResolveProjectRef(ctx, projects.ProjectRef{Name: result.Project.Name, SourcePath: result.Project.SourcePath})
	if err != nil || resolved.ID != project.ID {
		t.Fatalf("resolve returned legacy project ref = %#v, err = %v", resolved, err)
	}
	managedAgent, err := store.GetAgentDefinition(ctx, result.Agents[0].ManagedAgentID)
	if err != nil || managedAgent.WorkspaceID != workspace.ID {
		t.Fatalf("managed legacy agent workspace = %#v, err = %v", managedAgent, err)
	}
	page, err := store.ListProjectSchedulersPage(ctx, "", "", 10)
	if err != nil {
		t.Fatalf("list project schedulers page: %v", err)
	}
	if len(page) != 1 || page[0].Revision != project.CurrentRevision || page[0].ManagedLoaderID != loader.Summary.ID || page[0].Enabled || page[0].RunCount != 1 || !strings.Contains(page[0].SpecJSON, `"display_name":"资讯推送任务"`) || !strings.Contains(page[0].SpecJSON, `"description":"每小时检查并推送资讯"`) {
		t.Fatalf("project scheduler page = %#v, project = %#v", page, project)
	}

	adopted, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("load adopted loader: %v", err)
	}
	if adopted.Summary.ManagedProjectID != project.ID || adopted.Summary.ManagedRevision != project.CurrentRevision || adopted.Summary.ManagedAgentName != page[0].AgentName || adopted.Summary.Enabled || adopted.Summary.WorkspaceID != workspace.ID || adopted.Summary.Name != "资讯推送任务" || adopted.Summary.Description != "每小时检查并推送资讯" {
		t.Fatalf("adopted loader = %#v", adopted.Summary)
	}
	triggerEnabled := make(map[string]bool, len(adopted.Triggers))
	for _, trigger := range adopted.Triggers {
		triggerEnabled[trigger.ID] = trigger.Enabled
	}
	if len(triggerEnabled) != 2 || !triggerEnabled["hourly"] || triggerEnabled["on-news"] {
		t.Fatalf("adopted trigger states = %#v", triggerEnabled)
	}
	if runs, err := store.ListLoaderRuns(ctx, loader.Summary.ID, 10); err != nil || len(runs) != 1 || runs[0].ID != "legacy-run" {
		t.Fatalf("adopted loader runs = %#v, err = %v", runs, err)
	}
	if events, err := store.ListLoaderEvents(ctx, loader.Summary.ID, 10); err != nil || len(events) != 1 || events[0].ID != "legacy-event" {
		t.Fatalf("adopted loader events = %#v, err = %v", events, err)
	}

	emulateLegacyRevisionWithoutPresentation(t, ctx, store, project, result.RevisionSpec, result.Agents[0])
	upgraded, err := controller.SyncLegacyDefaultProject(ctx)
	if err != nil || !upgraded.Applied || upgraded.Revision.Revision != project.CurrentRevision+1 {
		t.Fatalf("presentation metadata upgrade = %#v, err = %v", upgraded, err)
	}
	if len(upgraded.RevisionSpec.Agents) != 1 || upgraded.RevisionSpec.Agents[0].DisplayName != "ai资讯推送" || upgraded.RevisionSpec.Agents[0].Description != "每日推送最新的 AI 资讯" || upgraded.RevisionSpec.Agents[0].Scheduler == nil || upgraded.RevisionSpec.Agents[0].Scheduler.DisplayName != "资讯推送任务" || upgraded.RevisionSpec.Agents[0].Scheduler.Description != "每小时检查并推送资讯" {
		t.Fatalf("upgraded presentation metadata = %#v", upgraded.RevisionSpec.Agents)
	}
	project, err = store.GetProject(ctx, projectID)
	if err != nil {
		t.Fatalf("load upgraded legacy project: %v", err)
	}
	page, err = store.ListProjectSchedulersPage(ctx, "", "", 10)
	if err != nil || len(page) != 1 || page[0].Revision != project.CurrentRevision || page[0].ManagedLoaderID != loader.Summary.ID || page[0].RunCount != 1 || !strings.Contains(page[0].SpecJSON, `"display_name":"资讯推送任务"`) {
		t.Fatalf("scheduler page after presentation upgrade = %#v, err = %v", page, err)
	}
	managedAgent, err = store.GetAgentDefinition(ctx, result.Agents[0].ManagedAgentID)
	if err != nil || managedAgent.Name != result.Agents[0].AgentName || managedAgent.ManagedAgentName != result.Agents[0].AgentName || managedAgent.Description != "每日推送最新的 AI 资讯" {
		t.Fatalf("managed agent after presentation upgrade = %#v, err = %v", managedAgent, err)
	}
	repeated, err := controller.SyncLegacyDefaultProject(ctx)
	if err != nil || !repeated.Applied || repeated.Revision.Revision != project.CurrentRevision {
		t.Fatalf("repeated upgraded sync = %#v, err = %v", repeated, err)
	}

	editedSpec := *repeated.RevisionSpec
	editedSpec.Agents = append([]compose.NormalizedAgentSpec(nil), repeated.RevisionSpec.Agents...)
	editedScheduler := *editedSpec.Agents[0].Scheduler
	editedScheduler.Description = "Edited through v2 project apply"
	editedSpec.Agents[0].Scheduler = &editedScheduler
	editedHash, err := editedSpec.Hash()
	if err != nil {
		t.Fatalf("hash edited synthetic project: %v", err)
	}
	edited, err := controller.ApplyProject(ctx, projects.ApplyRequest{Normalized: projects.NormalizedProject{Spec: &editedSpec, SpecHash: editedHash}})
	if err != nil || !edited.Applied {
		t.Fatalf("apply edited synthetic project = %#v, err = %v", edited, err)
	}
	page, err = store.ListProjectSchedulersPage(ctx, "", "", 10)
	if err != nil || len(page) != 1 || page[0].ManagedLoaderID != loader.Summary.ID {
		t.Fatalf("edited scheduler lost adopted loader identity: %#v, err = %v", page, err)
	}
	editedLoader, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil || editedLoader.Summary.Description != editedScheduler.Description {
		t.Fatalf("edited adopted loader = %#v, err = %v", editedLoader.Summary, err)
	}
}

func emulateLegacyRevisionWithoutPresentation(t *testing.T, ctx context.Context, store *configstore.ConfigStore, project domain.ProjectRecord, spec *compose.NormalizedProjectSpec, agent domain.ProjectAgentRecord) {
	t.Helper()
	legacySpec := *spec
	legacySpec.Agents = append([]compose.NormalizedAgentSpec(nil), spec.Agents...)
	for index := range legacySpec.Agents {
		legacySpec.Agents[index].DisplayName = ""
		legacySpec.Agents[index].Description = ""
		if legacySpec.Agents[index].Scheduler != nil {
			scheduler := *legacySpec.Agents[index].Scheduler
			scheduler.DisplayName = ""
			scheduler.Description = ""
			legacySpec.Agents[index].Scheduler = &scheduler
		}
	}
	legacyHash, err := legacySpec.Hash()
	if err != nil {
		t.Fatalf("hash legacy presentation-free spec: %v", err)
	}
	legacyJSON, err := legacySpec.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("marshal legacy presentation-free spec: %v", err)
	}
	legacyAgent, err := projects.NewAgentRecordFromSpec(project.ID, project.CurrentRevision, legacySpec.Agents[0])
	if err != nil {
		t.Fatalf("build legacy presentation-free agent: %v", err)
	}
	legacyScheduler, ok, err := projects.NewSchedulerRecordFromSpec(project.ID, project.CurrentRevision, legacySpec.Agents[0])
	if err != nil || !ok {
		t.Fatalf("build legacy presentation-free scheduler: %#v/%v/%v", legacyScheduler, ok, err)
	}

	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin legacy presentation downgrade: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE project SET spec_hash = ? WHERE id = ?`, legacyHash, project.ID); err != nil {
		t.Fatalf("downgrade project hash: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE project_revision SET spec_hash = ?, spec_json = ? WHERE project_id = ? AND revision = ?`, legacyHash, string(legacyJSON), project.ID, project.CurrentRevision); err != nil {
		t.Fatalf("downgrade project revision: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE project_agent SET spec_json = ? WHERE project_id = ? AND agent_name = ?`, legacyAgent.SpecJSON, project.ID, agent.AgentName); err != nil {
		t.Fatalf("downgrade project agent spec: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE project_scheduler SET spec_json = ? WHERE project_id = ? AND agent_name = ?`, legacyScheduler.SpecJSON, project.ID, agent.AgentName); err != nil {
		t.Fatalf("downgrade project scheduler spec: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_definition SET name = ?, description = '' WHERE id = ?`, agent.AgentName, agent.ManagedAgentID); err != nil {
		t.Fatalf("downgrade managed agent presentation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit legacy presentation downgrade: %v", err)
	}
}

type legacyProjectLoaderValidator struct {
	engine *loaders.QJSLoaderEngine
}

func (v legacyProjectLoaderValidator) Validate(ctx context.Context, runtime, script string) (loaders.LoaderValidationResult, error) {
	return v.engine.Validate(ctx, runtime, script)
}

func (legacyProjectLoaderValidator) Refresh(context.Context) error { return nil }

type legacyProjectImageBackend struct{}

func (legacyProjectImageBackend) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (legacyProjectImageBackend) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	return images.PullResult{}, nil
}

func (legacyProjectImageBackend) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{}, nil
}

func (legacyProjectImageBackend) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

type legacyProjectVolumeManager struct{}

func (legacyProjectVolumeManager) Ensure(context.Context, domain.VolumeRecord) (domain.VolumeRecord, bool, error) {
	return domain.VolumeRecord{}, false, nil
}

func (legacyProjectVolumeManager) Inspect(context.Context, string) (domain.VolumeRecord, error) {
	return domain.VolumeRecord{}, nil
}

func (legacyProjectVolumeManager) ReplaceProjectVolumes(context.Context, string, map[string]domain.ProjectVolumeLink) error {
	return nil
}

func (legacyProjectVolumeManager) RemoveProjectVolumes(context.Context, string) error { return nil }
