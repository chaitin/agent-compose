package agentcompose

import (
	"agent-compose/pkg/agentcompose/domain"
	"agent-compose/pkg/agentcompose/projects"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runspkg "agent-compose/pkg/agentcompose/runs"
	"agent-compose/pkg/compose"
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
		if len(id) > 80 {
			t.Fatalf("%s id too long: %q", label, id)
		}
		if !strings.Contains(id, "-reviewer-") && label != "project" {
			t.Fatalf("%s id missing readable agent name: %q", label, id)
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

func TestConfigStoreProjectStoreRevisionIdempotency(t *testing.T) {
	testConfigStoreProjectStoreRevisionIdempotency(t)
}

func TestIntegrationConfigStoreProjectStoreWorkflow(t *testing.T) {
	testConfigStoreProjectStoreRevisionIdempotency(t)
}

func testConfigStoreProjectStoreRevisionIdempotency(t *testing.T) {
	t.Helper()
	store := newTestConfigStore(t)
	ctx := context.Background()
	spec := mustNormalizeProjectStoreSpec(t, `
name: demo
agents:
  reviewer:
    provider: codex
    model: gpt-test
    image: guest:v1
    scheduler:
      triggers:
        - name: hourly
          cron: "0 * * * *"
  worker:
    provider: claude
`)
	project, err := projects.NewRecordFromSpec(spec, filepath.Join(t.TempDir(), "agent-compose.yml"))
	if err != nil {
		t.Fatalf("projects.NewRecordFromSpec returned error: %v", err)
	}
	project, err = store.UpsertProject(ctx, project)
	if err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	specJSON := mustProjectSpecJSON(t, spec)
	firstRevision, created, err := store.SaveProjectRevision(ctx, ProjectRevisionRecord{
		ProjectID: project.ID,
		SpecHash:  project.SpecHash,
		SpecJSON:  specJSON,
	})
	if err != nil {
		t.Fatalf("SaveProjectRevision returned error: %v", err)
	}
	if !created || firstRevision.Revision != 1 {
		t.Fatalf("first revision created=%v revision=%d, want created revision 1", created, firstRevision.Revision)
	}
	repeatedRevision, created, err := store.SaveProjectRevision(ctx, ProjectRevisionRecord{
		ProjectID: project.ID,
		SpecHash:  project.SpecHash,
		SpecJSON:  specJSON,
	})
	if err != nil {
		t.Fatalf("repeat SaveProjectRevision returned error: %v", err)
	}
	if created || repeatedRevision.Revision != firstRevision.Revision {
		t.Fatalf("repeat revision created=%v revision=%d, want existing revision %d", created, repeatedRevision.Revision, firstRevision.Revision)
	}

	upsertAgentsAndSchedulersForTest(t, store, ctx, project.ID, firstRevision.Revision, spec)
	upsertAgentsAndSchedulersForTest(t, store, ctx, project.ID, firstRevision.Revision, spec)
	agents, err := store.ListProjectAgents(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListProjectAgents returned error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("project agent count = %d, want 2", len(agents))
	}
	schedulers, err := store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListProjectSchedulers returned error: %v", err)
	}
	if len(schedulers) != 1 || schedulers[0].AgentName != "reviewer" || schedulers[0].TriggerCount != 1 {
		t.Fatalf("project schedulers = %#v, want one reviewer scheduler", schedulers)
	}

	changedSpec := mustNormalizeProjectStoreSpec(t, `
name: demo
agents:
  reviewer:
    provider: codex
    model: gpt-next
    image: guest:v2
    scheduler:
      triggers:
        - name: hourly
          cron: "0 * * * *"
  worker:
    provider: claude
`)
	changedProject, err := projects.NewRecordFromSpec(changedSpec, project.SourcePath)
	if err != nil {
		t.Fatalf("projects.NewRecordFromSpec changed returned error: %v", err)
	}
	changedSpecJSON := mustProjectSpecJSON(t, changedSpec)
	secondRevision, created, err := store.SaveProjectRevision(ctx, ProjectRevisionRecord{
		ProjectID: project.ID,
		SpecHash:  changedProject.SpecHash,
		SpecJSON:  changedSpecJSON,
	})
	if err != nil {
		t.Fatalf("SaveProjectRevision changed returned error: %v", err)
	}
	if !created || secondRevision.Revision != 2 {
		t.Fatalf("changed revision created=%v revision=%d, want created revision 2", created, secondRevision.Revision)
	}
	loadedProject, err := store.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetProject returned error: %v", err)
	}
	if loadedProject.CurrentRevision != 2 || loadedProject.SpecHash != changedProject.SpecHash {
		t.Fatalf("loaded project revision/hash = %d/%q, want 2/%q", loadedProject.CurrentRevision, loadedProject.SpecHash, changedProject.SpecHash)
	}
	listed, err := store.ListProjects(ctx, ProjectListOptions{Query: "demo"})
	if err != nil {
		t.Fatalf("ListProjects returned error: %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Projects) != 1 || listed.Projects[0].ID != project.ID {
		t.Fatalf("listed projects = %#v, want project %s", listed, project.ID)
	}
}

func TestConfigStoreProjectRunCreateUpdateList(t *testing.T) {
	store := newTestConfigStore(t)
	ctx := context.Background()
	spec := mustNormalizeProjectStoreSpec(t, `
name: runs
agents:
  reviewer:
    provider: codex
`)
	project, err := projects.NewRecordFromSpec(spec, filepath.Join(t.TempDir(), "agent-compose.yml"))
	if err != nil {
		t.Fatalf("projects.NewRecordFromSpec returned error: %v", err)
	}
	project, err = store.UpsertProject(ctx, project)
	if err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	revision, _, err := store.SaveProjectRevision(ctx, ProjectRevisionRecord{
		ProjectID: project.ID,
		SpecHash:  project.SpecHash,
		SpecJSON:  mustProjectSpecJSON(t, spec),
	})
	if err != nil {
		t.Fatalf("SaveProjectRevision returned error: %v", err)
	}
	runID, err := domain.StableProjectRunID(project.ID, "reviewer", domain.ProjectRunSourceManual, "request-1")
	if err != nil {
		t.Fatalf("domain.StableProjectRunID returned error: %v", err)
	}
	startedAt := time.Date(2026, 6, 10, 9, 8, 7, 123456789, time.UTC)
	run, err := store.CreateProjectRun(ctx, ProjectRunRecord{
		RunID:           runID,
		ProjectID:       project.ID,
		ProjectName:     project.Name,
		ProjectRevision: revision.Revision,
		AgentName:       "reviewer",
		Source:          domain.ProjectRunSourceManual,
		Status:          domain.ProjectRunStatusRunning,
		StartedAt:       startedAt,
		Prompt:          "review this",
		Driver:          "boxlite",
		ImageRef:        "guest:latest",
	})
	if err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}
	if run.Status != domain.ProjectRunStatusRunning || !run.StartedAt.Equal(time.UnixMilli(startedAt.UnixMilli()).UTC()) {
		t.Fatalf("created run = %#v", run)
	}
	completedAt := startedAt.Add(2 * time.Second)
	run.Status = domain.ProjectRunStatusSucceeded
	run.SessionID = "session-1"
	run.CompletedAt = completedAt
	run.DurationMs = 2000
	run.Output = "done"
	run.ResultJSON = `{"ok":true}`
	updated, err := store.UpdateProjectRun(ctx, run)
	if err != nil {
		t.Fatalf("UpdateProjectRun returned error: %v", err)
	}
	if updated.Status != domain.ProjectRunStatusSucceeded || updated.SessionID != "session-1" || updated.DurationMs != 2000 {
		t.Fatalf("updated run = %#v", updated)
	}
	loaded, err := store.GetProjectRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if loaded.Output != "done" || loaded.ResultJSON != `{"ok":true}` {
		t.Fatalf("loaded run = %#v", loaded)
	}
	runs, err := store.ListProjectRuns(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("ListProjectRuns returned error: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != runID {
		t.Fatalf("project runs = %#v, want run %s", runs, runID)
	}
}

func TestIntegrationProjectSessionRelationsUseSQLiteBeforeTags(t *testing.T) {
	store := newTestConfigStore(t)
	sessionStore := newProjectSessionTestStore(t)
	ctx := context.Background()
	spec := mustNormalizeProjectStoreSpec(t, `
name: sessions
agents:
  reviewer:
    provider: codex
`)
	project, err := projects.NewRecordFromSpec(spec, filepath.Join(t.TempDir(), "agent-compose.yml"))
	if err != nil {
		t.Fatalf("projects.NewRecordFromSpec returned error: %v", err)
	}
	project, err = store.UpsertProject(ctx, project)
	if err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	revision, _, err := store.SaveProjectRevision(ctx, ProjectRevisionRecord{
		ProjectID: project.ID,
		SpecHash:  project.SpecHash,
		SpecJSON:  mustProjectSpecJSON(t, spec),
	})
	if err != nil {
		t.Fatalf("SaveProjectRevision returned error: %v", err)
	}
	linkedSession, err := sessionStore.CreateSession(ctx, "Linked without project tags", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SessionTypeManual, nil, nil,
		[]SessionTag{{Name: "legacy", Value: "true"}})
	if err != nil {
		t.Fatalf("CreateSession linked returned error: %v", err)
	}
	tagOnlySession, err := sessionStore.CreateSession(ctx, "Tag only", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SessionTypeManual, nil, nil,
		[]SessionTag{
			{Name: "project", Value: project.ID},
			{Name: "agent", Value: "reviewer"},
			{Name: "run_id", Value: "tag-only-run"},
			{Name: "source", Value: domain.ProjectRunSourceManual},
		})
	if err != nil {
		t.Fatalf("CreateSession tag-only returned error: %v", err)
	}
	runID, err := domain.StableProjectRunID(project.ID, "reviewer", domain.ProjectRunSourceManual, "request-sqlite")
	if err != nil {
		t.Fatalf("domain.StableProjectRunID returned error: %v", err)
	}
	if _, err := store.CreateProjectRun(ctx, ProjectRunRecord{
		RunID:           runID,
		ProjectID:       project.ID,
		ProjectName:     project.Name,
		ProjectRevision: revision.Revision,
		AgentName:       "reviewer",
		Source:          domain.ProjectRunSourceManual,
		Status:          domain.ProjectRunStatusRunning,
		SessionID:       linkedSession.Summary.ID,
	}); err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}

	runs, err := store.ListProjectSessionRuns(ctx, domain.ProjectSessionRelationFilter{ProjectID: project.ID, Statuses: []string{domain.ProjectRunStatusRunning}})
	if err != nil {
		t.Fatalf("ListProjectSessionRuns returned error: %v", err)
	}
	if len(runs) != 1 || runs[0].SessionID != linkedSession.Summary.ID {
		t.Fatalf("project session runs = %#v, want linked session %s", runs, linkedSession.Summary.ID)
	}
	tagOnlyRuns, err := store.ListProjectRunsForSession(ctx, tagOnlySession.Summary.ID)
	if err != nil {
		t.Fatalf("ListProjectRunsForSession tag-only returned error: %v", err)
	}
	if len(tagOnlyRuns) != 0 {
		t.Fatalf("tag-only session returned SQLite relations: %#v", tagOnlyRuns)
	}
	statuses, err := runspkg.ListProjectSessionStatuses(ctx, store, sessionStore, domain.ProjectSessionRelationFilter{ProjectID: project.ID, Statuses: []string{domain.ProjectRunStatusRunning}})
	if err != nil {
		t.Fatalf("ListProjectSessionStatuses returned error: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Session == nil || statuses[0].Session.Summary.ID != linkedSession.Summary.ID {
		t.Fatalf("project session statuses = %#v, want loaded linked session %s", statuses, linkedSession.Summary.ID)
	}
	if statuses[0].SessionMissing {
		t.Fatalf("linked session unexpectedly marked missing")
	}
	if sessionHasTag(statuses[0].Session, "project", project.ID) {
		t.Fatalf("test setup unexpectedly added project tag to linked session")
	}
}

func mustNormalizeProjectStoreSpec(t *testing.T, raw string) *compose.NormalizedProjectSpec {
	t.Helper()
	spec, err := compose.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("compose.Parse returned error: %v", err)
	}
	normalized, err := compose.Normalize(spec, compose.NormalizeOptions{})
	if err != nil {
		t.Fatalf("compose.Normalize returned error: %v", err)
	}
	return normalized
}

func mustProjectSpecJSON(t *testing.T, spec *compose.NormalizedProjectSpec) string {
	t.Helper()
	data, err := spec.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("canonical project spec json is invalid: %s", data)
	}
	return string(data)
}

func upsertAgentsAndSchedulersForTest(t *testing.T, store *ConfigStore, ctx context.Context, projectID string, revision int64, spec *compose.NormalizedProjectSpec) {
	t.Helper()
	for _, agentSpec := range spec.Agents {
		agent, err := projects.NewAgentRecordFromSpec(projectID, revision, agentSpec)
		if err != nil {
			t.Fatalf("projects.NewAgentRecordFromSpec(%s) returned error: %v", agentSpec.Name, err)
		}
		if _, err := store.UpsertProjectAgent(ctx, agent); err != nil {
			t.Fatalf("UpsertProjectAgent(%s) returned error: %v", agent.AgentName, err)
		}
		scheduler, ok, err := projects.NewSchedulerRecordFromSpec(projectID, revision, agentSpec)
		if err != nil {
			t.Fatalf("projects.NewSchedulerRecordFromSpec(%s) returned error: %v", agentSpec.Name, err)
		}
		if ok {
			if _, err := store.UpsertProjectScheduler(ctx, scheduler); err != nil {
				t.Fatalf("UpsertProjectScheduler(%s) returned error: %v", scheduler.SchedulerID, err)
			}
		}
	}
}

func newProjectSessionTestStore(t *testing.T) *Store {
	t.Helper()
	return &Store{config: &appconfig.Config{
		SessionRoot:          filepath.Join(t.TempDir(), "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		JupyterProxyBasePath: "/agent-compose/session",
		JupyterGuestPort:     8888,
	}}
}

func sessionHasTag(session *Session, name, value string) bool {
	if session == nil {
		return false
	}
	for _, tag := range session.Summary.Tags {
		if strings.TrimSpace(tag.Name) == name && strings.TrimSpace(tag.Value) == value {
			return true
		}
	}
	return false
}
