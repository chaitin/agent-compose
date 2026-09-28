package projects

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func TestControllerValidateApplyDryRunAndResolveWorkflows(t *testing.T) {
	ctx := context.Background()
	raw, err := compose.Parse([]byte(`
name: coverage-project
variables:
  SHARED: value
workspaces:
  default:
    provider: file
    path: .
agents:
  worker:
    provider: codex
    model: gpt-test
    image: guest:latest
    driver:
      docker: {}
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
		projects: []domain.ProjectRecord{{ID: "project-1", Name: "coverage-project", SourcePath: "/one"}},
	}
	controller := NewController(ControllerDependencies{
		Config:     &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
		Store:      store,
		Schedulers: controllerCoverageSchedulerValidator{},
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
	if dryRun.Applied || dryRun.Project.ID != "project-1" || len(dryRun.Agents) != 1 || len(dryRun.Schedulers) != 1 || len(dryRun.Changes) < 4 {
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
	store.projects = append(store.projects, domain.ProjectRecord{ID: "project-2", Name: "coverage-project", SourcePath: "/two"})
	if _, err := controller.ResolveProjectRef(ctx, ProjectRefByName("coverage-project")); !errors.Is(err, domain.ErrAmbiguous) {
		t.Fatalf("expected ambiguous project error, got %v", err)
	}
	store.projects = []domain.ProjectRecord{{ID: "project-1", Name: "coverage-project", SourcePath: "/repo"}}
	resolved, err := controller.ResolveProjectRef(ctx, ProjectRefByName("coverage-project"))
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
		Store:     store,
		Sandboxes: controllerCoverageSessionStore{},
		Volumes:   volumeManager,
	})
	removed, err := controller.RemoveProject(ctx, RemoveRequest{Project: ProjectRefByID("project-1")})
	if err != nil {
		t.Fatalf("RemoveProject returned error: %v", err)
	}
	if removed.Project.RemovedAt.IsZero() {
		t.Fatalf("RemoveProject project was not marked removed: %#v", removed.Project)
	}
	assertProjectChange(t, removed.Changes, changeExpectation{
		Action:       ChangeActionRemoved,
		ResourceType: "project",
		ResourceID:   "project-1",
	})
	if result, err := store.ListProjects(ctx, domain.ProjectListOptions{}); err != nil || result.TotalCount != 0 {
		t.Fatalf("ListProjects after remove result=%#v err=%v", result, err)
	}
	if len(volumeManager.removedProjects) != 1 || volumeManager.removedProjects[0] != "project-1" {
		t.Fatalf("RemoveProject volume cleanup = %#v", volumeManager.removedProjects)
	}

	repeated, err := controller.RemoveProject(ctx, RemoveRequest{Project: ProjectRefByID("project-1")})
	if err != nil {
		t.Fatalf("repeated RemoveProject returned error: %v", err)
	}
	if len(repeated.Changes) != 0 {
		t.Fatalf("repeated RemoveProject changes=%#v, want unchanged", repeated.Changes)
	}
}

func TestControllerRemoveProjectKeepsProjectActiveWhenSandboxStopFails(t *testing.T) {
	project := domain.ProjectRecord{ID: "project-partial", Name: "partial-down"}
	store := &controllerCoverageStore{projects: []domain.ProjectRecord{project}}
	volumeManager := &controllerCoverageVolumeManager{}
	controller := NewController(ControllerDependencies{
		Store: store,
		Sandboxes: &downCoverageSessions{sessions: []*domain.Sandbox{{Summary: domain.SandboxSummary{
			ID: "sandbox-failed", Tags: []domain.SandboxTag{{Name: "project", Value: project.ID}},
		}}}},
		Volumes: volumeManager,
		StopSandbox: func(context.Context, *domain.Sandbox) error {
			return errors.New("stop failed")
		},
	})
	result, err := controller.RemoveProject(context.Background(), RemoveRequest{Project: ProjectRefByID(project.ID)})
	if err != nil {
		t.Fatalf("RemoveProject returned error: %v", err)
	}
	if !result.Project.RemovedAt.IsZero() {
		t.Fatalf("partially stopped project was marked removed: %#v", result.Project)
	}
	if len(volumeManager.removedProjects) != 0 {
		t.Fatalf("partial down removed volume links: %#v", volumeManager.removedProjects)
	}
	failureReported := false
	for _, change := range result.Changes {
		if change.ResourceType == "sandbox" && change.Action == ChangeActionUnchanged && strings.Contains(change.Message, "failed to stop") {
			failureReported = true
		}
	}
	if !failureReported {
		t.Fatalf("partial down changes did not report failure: %#v", result.Changes)
	}
}

func TestControllerRemoveProjectRetriesVolumeCleanupAfterRemoval(t *testing.T) {
	project := domain.ProjectRecord{ID: "project-volume-retry", Name: "volume-retry"}
	store := &controllerCoverageStore{projects: []domain.ProjectRecord{project}}
	volumeManager := &controllerCoverageVolumeManager{removeErr: errors.New("volume cleanup failed")}
	controller := NewController(ControllerDependencies{
		Store: store, Sandboxes: controllerCoverageSessionStore{}, Volumes: volumeManager,
	})

	first, err := controller.RemoveProject(context.Background(), RemoveRequest{Project: ProjectRefByID(project.ID)})
	if err == nil || first.Project.RemovedAt.IsZero() {
		t.Fatalf("first down result=%#v err=%v, want removed project and cleanup error", first, err)
	}
	volumeManager.removeErr = nil
	second, err := controller.RemoveProject(context.Background(), RemoveRequest{Project: ProjectRefByID(project.ID)})
	if err != nil {
		t.Fatalf("retry down returned error: %v", err)
	}
	if len(volumeManager.removedProjects) != 2 {
		t.Fatalf("volume cleanup calls = %#v, want two attempts", volumeManager.removedProjects)
	}
	if !second.Project.RemovedAt.Equal(first.Project.RemovedAt) {
		t.Fatalf("retry changed removed time from %v to %v", first.Project.RemovedAt, second.Project.RemovedAt)
	}
}

func TestManagedSchedulerErrorHelpersCoverage(t *testing.T) {
	plain := &projectSchedulerBuildError{message: "missing script"}
	if plain.Error() != "missing script" {
		t.Fatalf("plain build error = %q", plain.Error())
	}
	withPath := &projectSchedulerBuildError{path: "agents.worker.scheduler.script", message: "invalid script"}
	if withPath.Error() != "agents.worker.scheduler.script: invalid script" {
		t.Fatalf("path build error = %q", withPath.Error())
	}
	if issue := projectSchedulerBuildIssue(withPath); issue.Path != withPath.path || issue.Message != withPath.message {
		t.Fatalf("managed scheduler build issue = %#v", issue)
	}
	if issue := projectSchedulerBuildIssue(errors.New("boom")); issue.Path != "schedulers" || issue.Message != "boom" {
		t.Fatalf("fallback scheduler build issue = %#v", issue)
	}

	var cleaned bool
	cleanupFailedScheduler(context.Background(), ReconcileSchedulerOptions{
		CleanupFailedScheduler: func(_ context.Context, scheduler domain.ProjectSchedulerRecord, schedulerID string) {
			cleaned = scheduler.SchedulerID == "scheduler-1" && schedulerID == "scheduler-1"
		},
	}, domain.ProjectSchedulerRecord{SchedulerID: "scheduler-1"}, "scheduler-1")
	if !cleaned {
		t.Fatal("cleanupFailedScheduler did not invoke callback")
	}
	cleanupFailedScheduler(context.Background(), ReconcileSchedulerOptions{}, domain.ProjectSchedulerRecord{}, "")
}

func TestDownProjectSandboxAndSchedulerWorkflows(t *testing.T) {
	ctx := context.Background()
	project := domain.ProjectRecord{ID: "project-1", Name: "Down Project"}
	schedulerStore := &downCoverageStore{items: []domain.ProjectSchedulerRecord{
		{ProjectID: project.ID, SchedulerID: "scheduler-disabled", AgentName: "idle", ID: "scheduler-disabled", Enabled: false},
		{ProjectID: project.ID, SchedulerID: "scheduler-1", AgentName: "worker", ID: "scheduler-1", Enabled: true},
	}}
	sessionStore := &downCoverageSessions{sessions: []*domain.Sandbox{
		nil,
		{Summary: domain.SandboxSummary{ID: "other", Title: "Other", Tags: []domain.SandboxTag{{Name: "project", Value: "other"}}}},
		{Summary: domain.SandboxSummary{ID: "session-fail", Title: "Fail", Tags: []domain.SandboxTag{{Name: " project ", Value: " project-1 "}}}},
		{Summary: domain.SandboxSummary{ID: "session-ok", Title: "OK", Tags: []domain.SandboxTag{{Name: "project", Value: "project-1"}}}},
		{Summary: domain.SandboxSummary{ID: "session-legacy", Title: "Legacy", Tags: []domain.SandboxTag{{Name: "project_id", Value: "project-1"}}}},
		{Summary: domain.SandboxSummary{ID: "session-conflict", Title: "Conflict", Tags: []domain.SandboxTag{{Name: "project", Value: "other"}, {Name: "project_id", Value: "project-1"}}}},
	}}
	stopped := make([]string, 0)
	refreshed := false
	changes, err := DownProject(ctx, project, DownOptions{
		Store:     schedulerStore,
		Sandboxes: sessionStore,
		RefreshSchedulers: func(context.Context) error {
			refreshed = true
			return nil
		},
		StopSandbox: func(_ context.Context, session *domain.Sandbox) error {
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
	if !refreshed || len(stopped) != 3 {
		t.Fatalf("refreshed/stopped = %v/%#v", refreshed, stopped)
	}
	if len(sessionStore.listOptions) != 1 || sessionStore.listOptions[0].ProjectID != "" || sessionStore.listOptions[0].VMStatus != domain.VMStatusRunning {
		t.Fatalf("sandbox list options = %#v, want all running sandboxes without project prefilter", sessionStore.listOptions)
	}
	assertDownChange(t, changes, changeExpectation{
		Action:       DownChangeUpdated,
		ResourceType: "scheduler",
		ResourceID:   "scheduler-1",
	})
	assertDownChange(t, changes, changeExpectation{
		Action:       DownChangeUnchanged,
		ResourceType: "sandbox",
		ResourceID:   "session-fail",
	})
	assertDownChange(t, changes, changeExpectation{
		Action:       DownChangeUpdated,
		ResourceType: "sandbox",
		ResourceID:   "session-ok",
	})
	assertDownChange(t, changes, changeExpectation{
		Action:       DownChangeUpdated,
		ResourceType: "sandbox",
		ResourceID:   "session-legacy",
	})
	if !DownChangesHaveFailures(changes) || DownChangesHaveFailures(nil) {
		t.Fatalf("DownChangesHaveFailures(%#v) returned unexpected result", changes)
	}
	if SandboxHasTag(nil, "project", project.ID) || !SandboxHasTag(sessionStore.sessions[2], "project", project.ID) {
		t.Fatalf("SandboxHasTag returned unexpected values")
	}

	if _, err := DisableProjectSchedulers(ctx, project, DownOptions{}); err == nil {
		t.Fatalf("DisableProjectSchedulers without store returned nil error")
	}
	if _, err := DisableProjectSchedulers(ctx, project, DownOptions{Store: &downCoverageStore{listErr: errors.New("list failed")}}); err == nil {
		t.Fatalf("DisableProjectSchedulers list error returned nil error")
	}
	if _, err := DisableProjectSchedulers(ctx, project, DownOptions{
		Store: &downCoverageStore{items: []domain.ProjectSchedulerRecord{{ProjectID: project.ID, SchedulerID: "scheduler-1", Enabled: true}}},
		RefreshSchedulers: func(context.Context) error {
			return errors.New("refresh failed")
		},
	}); err == nil {
		t.Fatalf("DisableProjectSchedulers refresh error returned nil error")
	}
	if _, err := StopProjectRunningSandboxes(ctx, project, DownOptions{}); err == nil {
		t.Fatalf("StopProjectRunningSandboxes without sandboxes returned nil error")
	}
	if _, err := StopProjectRunningSandboxes(ctx, project, DownOptions{Sandboxes: &downCoverageSessions{err: errors.New("list failed")}}); err == nil {
		t.Fatalf("StopProjectRunningSandboxes list error returned nil error")
	}
	if _, err := StopProjectRunningSandboxes(ctx, project, DownOptions{Sandboxes: &downCoverageSessions{sessions: []*domain.Sandbox{{Summary: domain.SandboxSummary{ID: "session-1", Tags: []domain.SandboxTag{{Name: "project", Value: project.ID}}}}}}}); err == nil {
		t.Fatalf("StopProjectRunningSandboxes without stopper returned nil error")
	}
}

func TestIntegrationControllerValidateApplyDryRunAndResolveWorkflows(t *testing.T) {
	TestControllerValidateApplyDryRunAndResolveWorkflows(t)
	TestControllerRejectsUncompiledDriversBeforePersistence(t)
	TestDownProjectSandboxAndSchedulerWorkflows(t)
}

func TestE2EControllerValidateApplyDryRunAndResolveWorkflows(t *testing.T) {
	TestControllerValidateApplyDryRunAndResolveWorkflows(t)
	TestControllerRejectsUncompiledDriversBeforePersistence(t)
	TestDownProjectSandboxAndSchedulerWorkflows(t)
}

type controllerCoverageSchedulerValidator struct{}

func (controllerCoverageSchedulerValidator) Validate(context.Context, string, string) (schedulers.SchedulerValidationResult, error) {
	return schedulers.SchedulerValidationResult{Triggers: []domain.SchedulerTrigger{{ID: "daily", Kind: domain.SchedulerTriggerKindCron, Enabled: true, SpecJSON: `{"expr":"0 0 * * *"}`}}}, nil
}

func (controllerCoverageSchedulerValidator) Refresh(context.Context) error {
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

func (s *controllerCoverageStore) SetProjectApplyTrustedHeaders(context.Context, string, []domain.TrustedHeader) error {
	return nil
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

func (s *controllerCoverageStore) GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error) {
	return domain.ProjectRevisionRecord{}, sql.ErrNoRows
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

func (s *controllerCoverageStore) GetScheduler(context.Context, string) (domain.Scheduler, error) {
	return domain.Scheduler{}, sql.ErrNoRows
}

func (s *controllerCoverageStore) ReplaceSchedulerTriggers(context.Context, string, []domain.SchedulerTrigger) ([]domain.SchedulerTrigger, error) {
	return nil, nil
}

type controllerCoverageSessionStore struct{}

func (controllerCoverageSessionStore) ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error) {
	return domain.SandboxListResult{}, nil
}

type controllerCoverageVolumeManager struct {
	removedProjects []string
	removeErr       error
}

func (m *controllerCoverageVolumeManager) Ensure(context.Context, domain.VolumeRecord) (domain.VolumeRecord, bool, error) {
	return domain.VolumeRecord{}, false, nil
}

func (m *controllerCoverageVolumeManager) Inspect(context.Context, string) (domain.VolumeRecord, error) {
	return domain.VolumeRecord{}, nil
}

func (m *controllerCoverageVolumeManager) ListProjectVolumes(context.Context, string) (map[string]domain.VolumeRecord, error) {
	return nil, nil
}

func (m *controllerCoverageVolumeManager) ReplaceProjectVolumes(context.Context, string, map[string]domain.ProjectVolumeLink) error {
	return nil
}

func (m *controllerCoverageVolumeManager) RemoveProjectVolumes(_ context.Context, projectID string) error {
	m.removedProjects = append(m.removedProjects, projectID)
	return m.removeErr
}

type changeExpectation struct {
	Action       string
	ResourceType string
	ResourceID   string
}

func assertProjectChange(t *testing.T, changes []Change, want changeExpectation) {
	t.Helper()
	for _, change := range changes {
		if change.Action == want.Action && change.ResourceType == want.ResourceType && change.ResourceID == want.ResourceID {
			return
		}
	}
	t.Fatalf("changes %#v did not contain %s %s %s", changes, want.Action, want.ResourceType, want.ResourceID)
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
	sessions    []*domain.Sandbox
	err         error
	listOptions []domain.SandboxListOptions
}

func (s *downCoverageSessions) ListSandboxes(_ context.Context, options domain.SandboxListOptions) (domain.SandboxListResult, error) {
	s.listOptions = append(s.listOptions, options)
	return domain.SandboxListResult{Sandboxes: s.sessions}, s.err
}

func assertDownChange(t *testing.T, changes []DownChange, want changeExpectation) {
	t.Helper()
	for _, change := range changes {
		if change.Action == want.Action && change.ResourceType == want.ResourceType && change.ResourceID == want.ResourceID {
			return
		}
	}
	t.Fatalf("changes %#v did not contain %s %s %s", changes, want.Action, want.ResourceType, want.ResourceID)
}
