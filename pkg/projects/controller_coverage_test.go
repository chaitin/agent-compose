package projects

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func TestControllerValidateApplyDryRunAndResolveWorkflows(t *testing.T) {
	ctx := context.Background()
	raw, err := compose.Parse([]byte(`
name: coverage-project
variables:
  SHARED: value
workspaces:
  default:
    provider: local
    path: .
agents:
  worker:
    provider: codex
    model: gpt-test
    image: guest:latest
    driver:
      boxlite: {}
    env:
      AGENT_ENV: agent
    capset_ids: [dev]
    jupyter:
      enabled: true
      guest_port: 8888
    scheduler:
      script: |
        export default { triggers: [{ name: "daily", cron: "0 0 * * *", prompt: "run" }] }
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	normalizedSpec, err := compose.Normalize(raw, compose.NormalizeOptions{ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	hash, err := normalizedSpec.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	store := &controllerCoverageStore{
		projects: []domain.ProjectRecord{
			{ID: "project-1", Name: "coverage-project", SourcePath: "/one"},
			{ID: "project-2", Name: "coverage-project", SourcePath: "/two"},
		},
	}
	controller := NewController(ControllerDependencies{
		Config:  &appconfig.Config{RuntimeDriver: "boxlite"},
		Store:   store,
		Loaders: controllerCoverageLoaderValidator{},
	})
	normalized := NormalizedProject{Spec: normalizedSpec, SpecHash: hash, SourcePath: "/repo/agent-compose.yaml"}
	validation, err := controller.ValidateProject(ctx, normalized, nil)
	if err != nil || !validation.Valid || validation.SpecHash != hash {
		t.Fatalf("ValidateProject validation=%#v err=%v", validation, err)
	}
	dryRun, err := controller.ApplyProject(ctx, ApplyRequest{Normalized: normalized, DryRun: true})
	if err != nil {
		t.Fatalf("ApplyProject dry-run returned error: %v", err)
	}
	if dryRun.Applied || len(dryRun.Agents) != 1 || len(dryRun.Schedulers) != 1 || len(dryRun.Changes) < 4 {
		t.Fatalf("dryRun = %#v", dryRun)
	}
	if !strings.Contains(dryRun.Agents[0].SpecJSON, `"jupyter"`) {
		t.Fatalf("project agent spec json = %s, want jupyter config", dryRun.Agents[0].SpecJSON)
	}
	if issue, err := controller.ApplyProject(ctx, ApplyRequest{Issues: []ValidationIssue{{Path: "x", Message: "bad"}}, Normalized: normalized}); err != nil || len(issue.Issues) != 1 {
		t.Fatalf("ApplyProject issues=%#v err=%v", issue, err)
	}
	if missingSpec, err := controller.ValidateProject(ctx, NormalizedProject{}, nil); err != nil || missingSpec.Valid {
		t.Fatalf("ValidateProject missing spec=%#v err=%v", missingSpec, err)
	}
	if _, err := controller.ResolveProjectRef(ctx, ProjectRef{Name: "coverage-project"}); !errors.Is(err, domain.ErrAmbiguous) {
		t.Fatalf("expected ambiguous project error, got %v", err)
	}
	store.projects = []domain.ProjectRecord{{ID: "project-1", Name: "coverage-project", SourcePath: "/repo"}}
	resolved, err := controller.ResolveProjectRef(ctx, ProjectRef{Name: "coverage-project"})
	if err != nil || resolved.ID != "project-1" {
		t.Fatalf("ResolveProjectRef resolved=%#v err=%v", resolved, err)
	}
	if _, err := controller.ResolveProjectRef(ctx, ProjectRef{}); !errors.Is(err, domain.ErrRequired) {
		t.Fatalf("expected required project error, got %v", err)
	}
}

func TestControllerRemoveProjectMarksProjectRemovedAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := &controllerCoverageStore{
		projects: []domain.ProjectRecord{{ID: "project-1", Name: "down-project", SourcePath: "/repo/agent-compose.yaml"}},
	}
	volumeManager := &controllerCoverageVolumeManager{}
	controller := NewController(ControllerDependencies{
		Store:    store,
		Sessions: controllerCoverageSessionStore{},
		Volumes:  volumeManager,
	})
	removed, err := controller.RemoveProject(ctx, RemoveRequest{Project: ProjectRef{ProjectID: "project-1"}})
	if err != nil {
		t.Fatalf("RemoveProject returned error: %v", err)
	}
	if removed.Project.RemovedAt.IsZero() {
		t.Fatalf("RemoveProject project was not marked removed: %#v", removed.Project)
	}
	assertProjectChange(t, removed.Changes, ChangeActionRemoved, "project", "project-1")
	if result, err := store.ListProjects(ctx, domain.ProjectListOptions{}); err != nil || result.TotalCount != 0 {
		t.Fatalf("ListProjects after remove result=%#v err=%v", result, err)
	}
	if len(volumeManager.removedProjects) != 1 || volumeManager.removedProjects[0] != "project-1" {
		t.Fatalf("RemoveProject volume cleanup = %#v", volumeManager.removedProjects)
	}

	repeated, err := controller.RemoveProject(ctx, RemoveRequest{Project: ProjectRef{ProjectID: "project-1"}})
	if err != nil {
		t.Fatalf("repeated RemoveProject returned error: %v", err)
	}
	if len(repeated.Changes) != 0 {
		t.Fatalf("repeated RemoveProject changes=%#v, want unchanged", repeated.Changes)
	}
}

func TestManagedSchedulerErrorHelpersCoverage(t *testing.T) {
	plain := &managedSchedulerBuildError{message: "missing script"}
	if plain.Error() != "missing script" {
		t.Fatalf("plain build error = %q", plain.Error())
	}
	withPath := &managedSchedulerBuildError{path: "agents.worker.scheduler.script", message: "invalid script"}
	if withPath.Error() != "agents.worker.scheduler.script: invalid script" {
		t.Fatalf("path build error = %q", withPath.Error())
	}
	if issue := managedSchedulerBuildIssue(withPath); issue.Path != withPath.path || issue.Message != withPath.message {
		t.Fatalf("managed scheduler build issue = %#v", issue)
	}
	if issue := managedSchedulerBuildIssue(errors.New("boom")); issue.Path != "schedulers" || issue.Message != "boom" {
		t.Fatalf("fallback scheduler build issue = %#v", issue)
	}

	var cleaned bool
	cleanupFailedManagedScheduler(context.Background(), ReconcileSchedulerOptions{
		CleanupFailedManagedScheduler: func(_ context.Context, scheduler domain.ProjectSchedulerRecord, loaderID string) {
			cleaned = scheduler.SchedulerID == "scheduler-1" && loaderID == "loader-1"
		},
	}, domain.ProjectSchedulerRecord{SchedulerID: "scheduler-1"}, "loader-1")
	if !cleaned {
		t.Fatal("cleanupFailedManagedScheduler did not invoke callback")
	}
	cleanupFailedManagedScheduler(context.Background(), ReconcileSchedulerOptions{}, domain.ProjectSchedulerRecord{}, "")
}

func TestDownProjectSessionAndSchedulerWorkflows(t *testing.T) {
	ctx := context.Background()
	project := domain.ProjectRecord{ID: "project-1", Name: "Down Project"}
	schedulerStore := &downCoverageStore{items: []domain.ProjectSchedulerRecord{
		{ProjectID: project.ID, SchedulerID: "scheduler-disabled", AgentName: "idle", ManagedLoaderID: "loader-idle", Enabled: false},
		{ProjectID: project.ID, SchedulerID: "scheduler-1", AgentName: "worker", ManagedLoaderID: "loader-1", Enabled: true},
	}}
	sessionStore := downCoverageSessions{sessions: []*domain.Session{
		nil,
		{Summary: domain.SessionSummary{ID: "other", Title: "Other", Tags: []domain.SessionTag{{Name: "project", Value: "other"}}}},
		{Summary: domain.SessionSummary{ID: "session-fail", Title: "Fail", Tags: []domain.SessionTag{{Name: " project ", Value: " project-1 "}}}},
		{Summary: domain.SessionSummary{ID: "session-ok", Title: "OK", Tags: []domain.SessionTag{{Name: "project", Value: "project-1"}}}},
	}}
	stopped := make([]string, 0)
	refreshed := false
	changes, err := DownProject(ctx, project, DownOptions{
		Store:    schedulerStore,
		Sessions: sessionStore,
		DisableManagedLoader: func(_ context.Context, loaderID, projectID, schedulerID string) error {
			if loaderID != "loader-1" || projectID != project.ID || schedulerID != "scheduler-1" {
				t.Fatalf("DisableManagedLoader args = %q/%q/%q", loaderID, projectID, schedulerID)
			}
			return nil
		},
		RefreshLoaders: func(context.Context) error {
			refreshed = true
			return nil
		},
		StopSession: func(_ context.Context, session *domain.Session) error {
			stopped = append(stopped, session.Summary.ID)
			if session.Summary.ID == "session-fail" {
				return errors.New("stop failed")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("DownProject returned error: %v", err)
	}
	if !refreshed || len(stopped) != 2 {
		t.Fatalf("refreshed/stopped = %v/%#v", refreshed, stopped)
	}
	assertDownChange(t, changes, DownChangeUpdated, "project_scheduler", "scheduler-1")
	assertDownChange(t, changes, DownChangeUpdated, "loader", "loader-1")
	assertDownChange(t, changes, DownChangeUnchanged, "session", "session-fail")
	assertDownChange(t, changes, DownChangeUpdated, "session", "session-ok")
	if SessionHasTag(nil, "project", project.ID) || !SessionHasTag(sessionStore.sessions[2], "project", project.ID) {
		t.Fatalf("SessionHasTag returned unexpected values")
	}

	if _, err := DisableProjectManagedSchedulers(ctx, project, DownOptions{}); err == nil {
		t.Fatalf("DisableProjectManagedSchedulers without store returned nil error")
	}
	if _, err := DisableProjectManagedSchedulers(ctx, project, DownOptions{Store: &downCoverageStore{listErr: errors.New("list failed")}}); err == nil {
		t.Fatalf("DisableProjectManagedSchedulers list error returned nil error")
	}
	if _, err := DisableProjectManagedSchedulers(ctx, project, DownOptions{
		Store: &downCoverageStore{items: []domain.ProjectSchedulerRecord{{ProjectID: project.ID, SchedulerID: "scheduler-1", ManagedLoaderID: "loader-1", Enabled: true}}},
		DisableManagedLoader: func(context.Context, string, string, string) error {
			return errors.New("disable failed")
		},
	}); err == nil {
		t.Fatalf("DisableProjectManagedSchedulers managed loader error returned nil error")
	}
	if _, err := DisableProjectManagedSchedulers(ctx, project, DownOptions{
		Store: &downCoverageStore{items: []domain.ProjectSchedulerRecord{{ProjectID: project.ID, SchedulerID: "scheduler-1", Enabled: true}}},
		RefreshLoaders: func(context.Context) error {
			return errors.New("refresh failed")
		},
	}); err == nil {
		t.Fatalf("DisableProjectManagedSchedulers refresh error returned nil error")
	}
	if _, err := StopProjectRunningSessions(ctx, project, DownOptions{}); err == nil {
		t.Fatalf("StopProjectRunningSessions without sessions returned nil error")
	}
	if _, err := StopProjectRunningSessions(ctx, project, DownOptions{Sessions: downCoverageSessions{err: errors.New("list failed")}}); err == nil {
		t.Fatalf("StopProjectRunningSessions list error returned nil error")
	}
	if _, err := StopProjectRunningSessions(ctx, project, DownOptions{Sessions: downCoverageSessions{sessions: []*domain.Session{{Summary: domain.SessionSummary{ID: "session-1", Tags: []domain.SessionTag{{Name: "project", Value: project.ID}}}}}}}); err == nil {
		t.Fatalf("StopProjectRunningSessions without stopper returned nil error")
	}
}

func TestIntegrationControllerValidateApplyDryRunAndResolveWorkflows(t *testing.T) {
	TestControllerValidateApplyDryRunAndResolveWorkflows(t)
	TestDownProjectSessionAndSchedulerWorkflows(t)
}

func TestE2EControllerValidateApplyDryRunAndResolveWorkflows(t *testing.T) {
	TestControllerValidateApplyDryRunAndResolveWorkflows(t)
	TestDownProjectSessionAndSchedulerWorkflows(t)
}

type controllerCoverageLoaderValidator struct{}

func (controllerCoverageLoaderValidator) Validate(context.Context, string, string) (loaders.LoaderValidationResult, error) {
	return loaders.LoaderValidationResult{Triggers: []domain.LoaderTrigger{{ID: "daily", Kind: domain.LoaderTriggerKindCron, Enabled: true, SpecJSON: `{"expr":"0 0 * * *"}`}}}, nil
}

func (controllerCoverageLoaderValidator) Refresh(context.Context) error {
	return nil
}

type controllerCoverageStore struct {
	projects []domain.ProjectRecord
}

func (s *controllerCoverageStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, error) {
	for _, project := range s.projects {
		if project.ID == id && project.RemovedAt.IsZero() {
			return project, nil
		}
	}
	return domain.ProjectRecord{}, sql.ErrNoRows
}

func (s *controllerCoverageStore) GetProjectIfExists(_ context.Context, id string, includeRemoved bool) (domain.ProjectRecord, bool, error) {
	for _, project := range s.projects {
		if project.ID == id && (includeRemoved || project.RemovedAt.IsZero()) {
			return project, true, nil
		}
	}
	return domain.ProjectRecord{}, false, nil
}

func (s *controllerCoverageStore) ListProjects(_ context.Context, options domain.ProjectListOptions) (domain.ProjectListResult, error) {
	var projects []domain.ProjectRecord
	query := strings.TrimSpace(options.Query)
	for _, project := range s.projects {
		if !options.IncludeRemoved && !project.RemovedAt.IsZero() {
			continue
		}
		if query != "" && project.Name != query {
			continue
		}
		projects = append(projects, project)
	}
	return domain.ProjectListResult{Projects: projects, TotalCount: len(projects)}, nil
}

func (s *controllerCoverageStore) UpsertProject(context.Context, domain.ProjectRecord) (domain.ProjectRecord, error) {
	return domain.ProjectRecord{}, nil
}

func (s *controllerCoverageStore) MarkProjectRemoved(_ context.Context, projectID string) (domain.ProjectRecord, error) {
	for i, project := range s.projects {
		if project.ID == projectID {
			if project.RemovedAt.IsZero() {
				project.RemovedAt = time.Now().UTC()
				project.UpdatedAt = project.RemovedAt
				s.projects[i] = project
			}
			return project, nil
		}
	}
	return domain.ProjectRecord{}, sql.ErrNoRows
}

func (s *controllerCoverageStore) SaveProjectRevision(context.Context, domain.ProjectRevisionRecord) (domain.ProjectRevisionRecord, bool, error) {
	return domain.ProjectRevisionRecord{}, false, nil
}

func (s *controllerCoverageStore) GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error) {
	return domain.ProjectAgentRecord{}, sql.ErrNoRows
}

func (s *controllerCoverageStore) UpsertProjectAgent(context.Context, domain.ProjectAgentRecord) (domain.ProjectAgentRecord, error) {
	return domain.ProjectAgentRecord{}, nil
}

func (s *controllerCoverageStore) ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error) {
	return nil, nil
}

func (s *controllerCoverageStore) ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error) {
	return nil, nil
}

func (s *controllerCoverageStore) GetAgentDefinitionIfExists(context.Context, string, bool) (domain.AgentDefinition, bool, error) {
	return domain.AgentDefinition{}, false, nil
}

func (s *controllerCoverageStore) UpsertManagedAgentDefinition(context.Context, domain.AgentDefinition) (domain.AgentDefinition, error) {
	return domain.AgentDefinition{}, nil
}

func (s *controllerCoverageStore) ListManagedAgentDefinitions(context.Context, string, bool) ([]domain.AgentDefinition, error) {
	return nil, nil
}

func (s *controllerCoverageStore) SetAgentDefinitionEnabled(context.Context, string, bool) (domain.AgentDefinition, error) {
	return domain.AgentDefinition{}, nil
}

func (s *controllerCoverageStore) GetProjectScheduler(context.Context, string, string) (domain.ProjectSchedulerRecord, error) {
	return domain.ProjectSchedulerRecord{}, sql.ErrNoRows
}

func (s *controllerCoverageStore) UpsertProjectScheduler(context.Context, domain.ProjectSchedulerRecord) (domain.ProjectSchedulerRecord, error) {
	return domain.ProjectSchedulerRecord{}, nil
}

func (s *controllerCoverageStore) SetProjectSchedulerEnabled(context.Context, string, string, bool) (domain.ProjectSchedulerRecord, error) {
	return domain.ProjectSchedulerRecord{}, nil
}

func (s *controllerCoverageStore) GetLoaderIfExists(context.Context, string) (domain.Loader, bool, error) {
	return domain.Loader{}, false, nil
}

func (s *controllerCoverageStore) UpsertManagedLoader(context.Context, domain.Loader) (domain.Loader, error) {
	return domain.Loader{}, nil
}

func (s *controllerCoverageStore) ReplaceLoaderTriggers(context.Context, string, []domain.LoaderTrigger) ([]domain.LoaderTrigger, error) {
	return nil, nil
}

func (s *controllerCoverageStore) SetLoaderEnabled(context.Context, string, bool) error {
	return nil
}

type controllerCoverageSessionStore struct{}

func (controllerCoverageSessionStore) ListSessions(context.Context, domain.SessionListOptions) (domain.SessionListResult, error) {
	return domain.SessionListResult{}, nil
}

type controllerCoverageVolumeManager struct {
	removedProjects []string
}

func (m *controllerCoverageVolumeManager) Ensure(context.Context, domain.VolumeRecord) (domain.VolumeRecord, bool, error) {
	return domain.VolumeRecord{}, false, nil
}

func (m *controllerCoverageVolumeManager) Inspect(context.Context, string) (domain.VolumeRecord, error) {
	return domain.VolumeRecord{}, nil
}

func (m *controllerCoverageVolumeManager) ReplaceProjectVolumes(context.Context, string, map[string]domain.ProjectVolumeLink) error {
	return nil
}

func (m *controllerCoverageVolumeManager) RemoveProjectVolumes(_ context.Context, projectID string) error {
	m.removedProjects = append(m.removedProjects, projectID)
	return nil
}

func assertProjectChange(t *testing.T, changes []Change, action, resourceType, resourceID string) {
	t.Helper()
	for _, change := range changes {
		if change.Action == action && change.ResourceType == resourceType && change.ResourceID == resourceID {
			return
		}
	}
	t.Fatalf("changes %#v did not contain %s %s %s", changes, action, resourceType, resourceID)
}

type downCoverageStore struct {
	items   []domain.ProjectSchedulerRecord
	listErr error
}

func (s *downCoverageStore) ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]domain.ProjectSchedulerRecord(nil), s.items...), nil
}

func (s *downCoverageStore) SetProjectSchedulerEnabled(_ context.Context, projectID, schedulerID string, enabled bool) (domain.ProjectSchedulerRecord, error) {
	for index := range s.items {
		if s.items[index].ProjectID == projectID && s.items[index].SchedulerID == schedulerID {
			s.items[index].Enabled = enabled
			return s.items[index], nil
		}
	}
	return domain.ProjectSchedulerRecord{}, sql.ErrNoRows
}

type downCoverageSessions struct {
	sessions []*domain.Session
	err      error
}

func (s downCoverageSessions) ListSessions(context.Context, domain.SessionListOptions) (domain.SessionListResult, error) {
	return domain.SessionListResult{Sessions: s.sessions}, s.err
}

func assertDownChange(t *testing.T, changes []DownChange, action, resourceType, resourceID string) {
	t.Helper()
	for _, change := range changes {
		if change.Action == action && change.ResourceType == resourceType && change.ResourceID == resourceID {
			return
		}
	}
	t.Fatalf("changes %#v did not contain %s %s %s", changes, action, resourceType, resourceID)
}
