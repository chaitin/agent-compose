package configstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/sessionstore"
)

func TestConfigStoreMigrationAndTimeParsingWorkflows(t *testing.T) {
	testConfigStoreMigrationAndTimeParsingWorkflows(t)
}

func TestIntegrationConfigStoreMigrationAndTimeParsingWorkflows(t *testing.T) {
	testConfigStoreMigrationAndTimeParsingWorkflows(t)
}

func TestE2EConfigStoreMigrationAndTimeParsingWorkflows(t *testing.T) {
	testConfigStoreMigrationAndTimeParsingWorkflows(t)
}

func TestConfigStoreProjectSchemaMigrationWorkflows(t *testing.T) {
	testConfigStoreProjectSchemaMigrationWorkflows(t)
}

func TestConfigStoreCRUDCoverageWorkflows(t *testing.T) {
	testConfigStoreCRUDCoverageWorkflows(t)
}

func TestConfigStoreTopicEventCoverageWorkflows(t *testing.T) {
	testConfigStoreTopicEventCoverageWorkflows(t)
}

func TestConfigStoreProjectCRUDCoverageWorkflows(t *testing.T) {
	testConfigStoreProjectCRUDCoverageWorkflows(t)
}

func TestConfigStoreMigratesLegacySQLiteSessionSchema(t *testing.T) {
	testConfigStoreMigratesLegacySQLiteSessionSchema(t)
}

func TestConfigStoreRecoversInterruptedLoaderBindingTriggerMigration(t *testing.T) {
	testConfigStoreRecoversInterruptedLoaderBindingTriggerMigration(t)
}

func TestIntegrationConfigStoreProjectSchemaMigrationWorkflows(t *testing.T) {
	testConfigStoreProjectSchemaMigrationWorkflows(t)
}

func TestE2EConfigStoreProjectSchemaMigrationWorkflows(t *testing.T) {
	testConfigStoreProjectSchemaMigrationWorkflows(t)
}

func TestIntegrationConfigStoreCRUDCoverageWorkflows(t *testing.T) {
	testConfigStoreCRUDCoverageWorkflows(t)
}

func TestE2EConfigStoreCRUDCoverageWorkflows(t *testing.T) {
	testConfigStoreCRUDCoverageWorkflows(t)
}

func TestIntegrationConfigStoreTopicEventCoverageWorkflows(t *testing.T) {
	testConfigStoreTopicEventCoverageWorkflows(t)
}

func TestE2EConfigStoreTopicEventCoverageWorkflows(t *testing.T) {
	testConfigStoreTopicEventCoverageWorkflows(t)
}

func TestIntegrationConfigStoreProjectCRUDCoverageWorkflows(t *testing.T) {
	testConfigStoreProjectCRUDCoverageWorkflows(t)
}

func TestE2EConfigStoreProjectCRUDCoverageWorkflows(t *testing.T) {
	testConfigStoreProjectCRUDCoverageWorkflows(t)
}

func testConfigStoreMigratesLegacySQLiteSessionSchema(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db := newMemoryDB(t)
	legacySchema := []string{
		`CREATE TABLE loader (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', runtime TEXT NOT NULL DEFAULT 'scheduler',
			script TEXT NOT NULL, workspace_id TEXT NOT NULL DEFAULT '', agent_id TEXT NOT NULL DEFAULT '', driver TEXT NOT NULL DEFAULT '',
			guest_image TEXT NOT NULL DEFAULT '', default_agent TEXT NOT NULL DEFAULT 'codex', session_policy TEXT NOT NULL DEFAULT 'sticky',
			concurrency_policy TEXT NOT NULL DEFAULT 'skip', capset_ids TEXT NOT NULL DEFAULT '[]', env_json TEXT NOT NULL DEFAULT '[]',
			volumes_json TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, last_error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE loader_binding(loader_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE loader_event(
			loader_id TEXT NOT NULL, event_id TEXT NOT NULL, run_id TEXT NOT NULL DEFAULT '', trigger_id TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL, level TEXT NOT NULL DEFAULT 'info', message TEXT NOT NULL DEFAULT '', payload_json TEXT NOT NULL DEFAULT '',
			linked_session_id TEXT NOT NULL DEFAULT '', linked_cell_id TEXT NOT NULL DEFAULT '', linked_agent_session_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL, PRIMARY KEY(loader_id, event_id)
		)`,
		`CREATE TABLE llm_facade_token(
			token_hash TEXT PRIMARY KEY, session_id TEXT NOT NULL, token_fingerprint TEXT NOT NULL, model TEXT NOT NULL DEFAULT '',
			provider_id TEXT NOT NULL DEFAULT '', wire_api TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', run_id TEXT NOT NULL DEFAULT '',
			issued_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE event_session_link(
			event_id TEXT NOT NULL, session_id TEXT NOT NULL, relation TEXT NOT NULL, loader_id TEXT NOT NULL DEFAULT '',
			run_id TEXT NOT NULL DEFAULT '', trigger_id TEXT NOT NULL DEFAULT '', loader_event_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
			PRIMARY KEY(event_id, session_id, relation, run_id)
		)`,
	}
	for _, stmt := range legacySchema {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("create legacy fixture: %v", err)
		}
	}
	rawToken := "legacy-raw-token"
	hash, fingerprint := llms.HashFacadeToken(rawToken)
	fixtures := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO loader(id, name, script, session_policy, created_at, updated_at) VALUES('loader-1', 'legacy', 'return 1', 'ephemeral', 1, 1)`, nil},
		{`INSERT INTO loader_binding(loader_id, session_id, created_at, updated_at) VALUES('loader-1', 'sandbox-1', 1, 1)`, nil},
		{`INSERT INTO loader_event(loader_id, event_id, type, linked_session_id, linked_agent_session_id, created_at) VALUES('loader-1', 'event-1', 'legacy', 'sandbox-1', 'thread-1', 1)`, nil},
		{`INSERT INTO llm_facade_token(token_hash, session_id, token_fingerprint, issued_at, expires_at) VALUES(?, 'sandbox-1', ?, 1, 0)`, []any{hash, fingerprint}},
		{`INSERT INTO event_session_link(event_id, session_id, relation, created_at) VALUES('topic-event-1', 'sandbox-1', 'created', 1)`, nil},
	}
	for _, fixture := range fixtures {
		if _, err := db.ExecContext(ctx, fixture.query, fixture.args...); err != nil {
			t.Fatalf("insert legacy fixture: %v", err)
		}
	}
	store := FromDB(db)
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema legacy migration returned error: %v", err)
	}
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("second initSchema legacy migration returned error: %v", err)
	}
	assertTableColumns(t, store, "loader", "sandbox_policy")
	assertTableMissingColumns(t, store, "loader", "session_policy")
	assertTableColumns(t, store, "loader_binding", "trigger_id", "sandbox_id")
	assertTableColumns(t, store, "loader_event", "linked_sandbox_id", "linked_agent_thread_id")
	assertTableColumns(t, store, "llm_facade_token", "sandbox_id")
	if binding, found, err := store.GetLoaderBinding(ctx, "loader-1", ""); err != nil || !found || binding.SandboxID != "sandbox-1" {
		t.Fatalf("migrated binding=%#v found=%v err=%v", binding, found, err)
	}
	if events, err := store.ListLoaderEvents(ctx, "loader-1", 10); err != nil || len(events) != 1 || events[0].LinkedSandboxID != "sandbox-1" || events[0].LinkedAgentThreadID != "thread-1" {
		t.Fatalf("migrated loader events=%#v err=%v", events, err)
	}
	if token, err := store.GetLLMFacadeToken(ctx, rawToken); err != nil || token.SandboxID != "sandbox-1" {
		t.Fatalf("migrated token=%#v err=%v", token, err)
	}
	if links, err := store.ListEventSandboxLinks(ctx, []string{"topic-event-1"}); err != nil || len(links) != 1 || links[0].SandboxID != "sandbox-1" {
		t.Fatalf("migrated event links=%#v err=%v", links, err)
	}
	assertTableColumns(t, store, "event_session_link", "session_id")
}

func testConfigStoreRecoversInterruptedLoaderBindingTriggerMigration(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db := newMemoryDB(t)
	if _, err := db.ExecContext(ctx, `CREATE TABLE loader_binding_legacy(
		loader_id TEXT PRIMARY KEY,
		sandbox_id TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create interrupted migration fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO loader_binding_legacy(loader_id, sandbox_id, created_at, updated_at)
		VALUES('loader-1', 'sandbox-1', 1, 2)`); err != nil {
		t.Fatalf("insert interrupted migration fixture: %v", err)
	}
	store := FromDB(db)
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema interrupted migration recovery returned error: %v", err)
	}
	assertTableColumns(t, store, "loader_binding", "trigger_id", "sandbox_id")
	assertTableDoesNotExist(t, store, "loader_binding_legacy")
	if binding, found, err := store.GetLoaderBinding(ctx, "loader-1", ""); err != nil || !found || binding.SandboxID != "sandbox-1" {
		t.Fatalf("recovered binding=%#v found=%v err=%v", binding, found, err)
	}
}

func testConfigStoreProjectCRUDCoverageWorkflows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema returned error: %v", err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{}); err == nil {
		t.Fatalf("UpsertProject empty project returned nil error")
	}
	if _, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: "missing-project", SpecHash: "hash", SpecJSON: `{"ok":true}`}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SaveProjectRevision missing project err=%v, want not found", err)
	}
	if _, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: "missing-project", SpecHash: "hash", SpecJSON: `{bad json`}); err == nil {
		t.Fatalf("SaveProjectRevision invalid JSON returned nil error")
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "Project", SourcePath: "/tmp/project", SourceJSON: `{"kind":"local"}`, SpecHash: "hash-0"})
	if err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	project.Name = "Project Updated"
	if project, err = store.UpsertProject(ctx, project); err != nil || project.Name != "Project Updated" {
		t.Fatalf("UpsertProject update project=%#v err=%v", project, err)
	}
	revision, created, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: "hash-1", SpecJSON: `{"agents":[]}`})
	if err != nil || !created || revision.Revision != 1 {
		t.Fatalf("SaveProjectRevision revision=%#v created=%v err=%v", revision, created, err)
	}
	if existing, created, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: "hash-1", SpecJSON: `{"agents":[]}`}); err != nil || created || existing.Revision != revision.Revision {
		t.Fatalf("SaveProjectRevision existing=%#v created=%v err=%v", existing, created, err)
	}
	secondRevision, created, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: "hash-2", SpecJSON: `{"agents":[{"driver":"boxlite"}]}`})
	if err != nil || !created || secondRevision.Revision != 2 {
		t.Fatalf("SaveProjectRevision secondRevision=%#v created=%v err=%v", secondRevision, created, err)
	}
	thirdRevision, created, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: "hash-1", SpecJSON: `{"agents":[]}`})
	if err != nil || !created || thirdRevision.Revision != 3 {
		t.Fatalf("SaveProjectRevision repeated hash thirdRevision=%#v created=%v err=%v", thirdRevision, created, err)
	}
	if got, err := store.GetProject(ctx, project.ID); err != nil || got.CurrentRevision != thirdRevision.Revision {
		t.Fatalf("GetProject got=%#v err=%v", got, err)
	}
	if got, err := store.GetProjectRevision(ctx, project.ID, revision.Revision); err != nil || got.SpecHash != "hash-1" {
		t.Fatalf("GetProjectRevision got=%#v err=%v", got, err)
	}
	if got, err := store.GetProjectRevision(ctx, project.ID, thirdRevision.Revision); err != nil || got.SpecHash != "hash-1" {
		t.Fatalf("GetProjectRevision repeated hash got=%#v err=%v", got, err)
	}
	if result, err := store.ListProjects(ctx, domain.ProjectListOptions{Query: "updated", Limit: 10}); err != nil || result.TotalCount != 1 {
		t.Fatalf("ListProjects result=%#v err=%v", result, err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-2", Name: "Other Project", SourcePath: "/tmp/other", SourceJSON: `{"kind":"local"}`}); err != nil {
		t.Fatalf("UpsertProject second project returned error: %v", err)
	}
	if result, err := store.ListProjects(ctx, domain.ProjectListOptions{Limit: 1, Offset: -5}); err != nil || result.TotalCount != 2 || len(result.Projects) != 1 || !result.HasMore || result.NextOffset != 1 {
		t.Fatalf("ListProjects paged result=%#v err=%v", result, err)
	}
	if result, err := store.ListProjects(ctx, domain.ProjectListOptions{Query: "not-present", Limit: 500, Offset: 5}); err != nil || result.TotalCount != 0 || len(result.Projects) != 0 || result.NextOffset != 0 {
		t.Fatalf("ListProjects empty result=%#v err=%v", result, err)
	}
	if _, err := store.GetProject(ctx, "missing-project"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetProject missing err=%v, want not found", err)
	}
	if _, err := store.GetProjectRevision(ctx, project.ID, 999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetProjectRevision missing err=%v, want not found", err)
	}
	if got, found, err := store.GetProjectIfExists(ctx, project.ID, false); err != nil || !found || got.ID != project.ID {
		t.Fatalf("GetProjectIfExists got=%#v found=%v err=%v", got, found, err)
	}
	if _, found, err := store.GetProjectIfExists(ctx, "missing-project", false); err != nil || found {
		t.Fatalf("GetProjectIfExists missing found=%v err=%v", found, err)
	}

	agent, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{
		ProjectID: project.ID, AgentName: "worker", ManagedAgentID: "managed-agent-1", Revision: thirdRevision.Revision,
		Provider: "codex", Model: "gpt", Image: "guest:latest", Driver: driverpkg.RuntimeDriverBoxlite, SchedulerEnabled: true, SpecJSON: `{"name":"worker"}`,
	})
	if err != nil {
		t.Fatalf("UpsertProjectAgent returned error: %v", err)
	}
	agent.Model = "gpt-updated"
	if agent, err = store.UpsertProjectAgent(ctx, agent); err != nil || agent.Model != "gpt-updated" {
		t.Fatalf("UpsertProjectAgent update agent=%#v err=%v", agent, err)
	}
	if got, err := store.GetProjectAgent(ctx, project.ID, "worker"); err != nil || got.ManagedAgentID != "managed-agent-1" {
		t.Fatalf("GetProjectAgent got=%#v err=%v", got, err)
	}
	if _, err := store.GetProjectAgent(ctx, project.ID, "missing-agent"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetProjectAgent missing err=%v, want not found", err)
	}
	if agents, err := store.ListProjectAgents(ctx, project.ID); err != nil || len(agents) != 1 {
		t.Fatalf("ListProjectAgents agents=%#v err=%v", agents, err)
	}
	scheduler, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{
		ProjectID: project.ID, SchedulerID: "scheduler-1", AgentName: "worker", ManagedLoaderID: "loader-1", Revision: thirdRevision.Revision, Enabled: true, TriggerCount: 2, SpecJSON: `{"id":"scheduler-1"}`,
	})
	if err != nil {
		t.Fatalf("UpsertProjectScheduler returned error: %v", err)
	}
	scheduler.TriggerCount = 3
	if scheduler, err = store.UpsertProjectScheduler(ctx, scheduler); err != nil || scheduler.TriggerCount != 3 {
		t.Fatalf("UpsertProjectScheduler update scheduler=%#v err=%v", scheduler, err)
	}
	if scheduler, err = store.SetProjectSchedulerEnabled(ctx, project.ID, scheduler.SchedulerID, false); err != nil || scheduler.Enabled {
		t.Fatalf("SetProjectSchedulerEnabled scheduler=%#v err=%v", scheduler, err)
	}
	if _, err := store.SetProjectSchedulerEnabled(ctx, "", scheduler.SchedulerID, true); err == nil {
		t.Fatalf("SetProjectSchedulerEnabled empty project returned nil error")
	}
	if _, err := store.SetProjectSchedulerEnabled(ctx, project.ID, "missing-scheduler", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetProjectSchedulerEnabled missing err=%v, want not found", err)
	}
	if got, err := store.GetProjectScheduler(ctx, project.ID, scheduler.SchedulerID); err != nil || got.SchedulerID != scheduler.SchedulerID {
		t.Fatalf("GetProjectScheduler got=%#v err=%v", got, err)
	}
	if _, err := store.GetProjectScheduler(ctx, project.ID, "missing-scheduler"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetProjectScheduler missing err=%v, want not found", err)
	}
	if schedulers, err := store.ListProjectSchedulers(ctx, project.ID); err != nil || len(schedulers) != 1 {
		t.Fatalf("ListProjectSchedulers schedulers=%#v err=%v", schedulers, err)
	}

	managedAgent, err := store.UpsertManagedAgentDefinition(ctx, domain.AgentDefinition{ID: "managed-agent-1", Name: "Managed", Enabled: true, Provider: "codex", ManagedProjectID: project.ID, ManagedAgentName: "worker", Driver: driverpkg.RuntimeDriverBoxlite, GuestImage: "guest:latest"})
	if err != nil {
		t.Fatalf("UpsertManagedAgentDefinition returned error: %v", err)
	}
	if got, err := store.GetManagedAgentDefinition(ctx, managedAgent.ID); err != nil || got.ManagedProjectID != project.ID {
		t.Fatalf("GetManagedAgentDefinition got=%#v err=%v", got, err)
	}
	run, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-1", ProjectID: project.ID, ProjectName: project.Name, ProjectRevision: thirdRevision.Revision, AgentName: "worker", ManagedAgentID: managedAgent.ID,
		Source: domain.ProjectRunSourceAPI, SchedulerID: scheduler.SchedulerID, TriggerID: "trigger-1", Status: domain.ProjectRunStatusPending, Prompt: "prompt", ResultJSON: "{}",
	})
	if err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}
	run.Status = domain.ProjectRunStatusRunning
	run.SandboxID = "sandbox-1"
	run.StartedAt = time.Now().UTC()
	if run, err = store.UpdateProjectRun(ctx, run); err != nil || run.SandboxID != "sandbox-1" {
		t.Fatalf("UpdateProjectRun run=%#v err=%v", run, err)
	}
	if _, err := store.UpdateProjectRun(ctx, domain.ProjectRunRecord{RunID: "missing-run", ProjectID: project.ID, ResultJSON: "{}"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateProjectRun missing err=%v, want not found", err)
	}
	if got, err := store.GetProjectRun(ctx, run.RunID); err != nil || got.RunID != run.RunID {
		t.Fatalf("GetProjectRun got=%#v err=%v", got, err)
	}
	if _, err := store.GetProjectRun(ctx, "missing-run"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetProjectRun missing err=%v, want not found", err)
	}
	if runs, err := store.ListProjectRuns(ctx, project.ID, 10); err != nil || len(runs) != 1 {
		t.Fatalf("ListProjectRuns runs=%#v err=%v", runs, err)
	}
	if runs, err := store.ListProjectRunsByOptions(ctx, domain.ProjectRunListOptions{Limit: 500, Offset: -1}); err != nil || len(runs) != 1 {
		t.Fatalf("ListProjectRunsByOptions unfiltered runs=%#v err=%v", runs, err)
	}
	if runs, err := store.ListProjectRunsByOptions(ctx, domain.ProjectRunListOptions{ProjectID: project.ID, AgentName: "worker", SandboxID: "sandbox-1", SchedulerID: scheduler.SchedulerID, Status: domain.ProjectRunStatusRunning, Source: domain.ProjectRunSourceAPI, Limit: 10}); err != nil || len(runs) != 1 {
		t.Fatalf("ListProjectRunsByOptions runs=%#v err=%v", runs, err)
	}
	if runs, err := store.ListProjectSandboxRuns(ctx, domain.ProjectSandboxRelationFilter{ProjectID: project.ID, AgentName: "worker", SandboxID: "sandbox-1", Statuses: []string{domain.ProjectRunStatusRunning}, Limit: 10}); err != nil || len(runs) != 1 {
		t.Fatalf("ListProjectSandboxRuns runs=%#v err=%v", runs, err)
	}
	if runs, err := store.ListProjectRunsForSandbox(ctx, "sandbox-1"); err != nil || len(runs) != 1 {
		t.Fatalf("ListProjectRunsForSandbox runs=%#v err=%v", runs, err)
	}
	if _, err := store.MarkProjectRemoved(ctx, ""); err == nil {
		t.Fatalf("MarkProjectRemoved empty project returned nil error")
	}
	if _, err := store.MarkProjectRemoved(ctx, "missing-project"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("MarkProjectRemoved missing err=%v, want not found", err)
	}
	removed, err := store.MarkProjectRemoved(ctx, project.ID)
	if err != nil || removed.RemovedAt.IsZero() {
		t.Fatalf("MarkProjectRemoved removed=%#v err=%v", removed, err)
	}
	if removedAgain, err := store.MarkProjectRemoved(ctx, project.ID); err != nil || removedAgain.RemovedAt.IsZero() {
		t.Fatalf("MarkProjectRemoved already removed=%#v err=%v", removedAgain, err)
	}
	if _, err := store.GetProject(ctx, project.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetProject after remove err=%v, want not found", err)
	}
	if result, err := store.ListProjects(ctx, domain.ProjectListOptions{Query: "updated", Limit: 10}); err != nil || result.TotalCount != 0 {
		t.Fatalf("ListProjects after remove result=%#v err=%v", result, err)
	}
	if result, err := store.ListProjects(ctx, domain.ProjectListOptions{Query: "updated", IncludeRemoved: true, Limit: 10}); err != nil || result.TotalCount != 1 || result.Projects[0].RemovedAt.IsZero() {
		t.Fatalf("ListProjects include removed result=%#v err=%v", result, err)
	}
	reactivated, err := store.UpsertProject(ctx, project)
	if err != nil || !reactivated.RemovedAt.IsZero() {
		t.Fatalf("UpsertProject reactivated=%#v err=%v", reactivated, err)
	}
	if placeholders(0) != "" || placeholders(3) != "?,?,?" {
		t.Fatalf("placeholders returned unexpected values")
	}
}

func testConfigStoreTopicEventCoverageWorkflows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema returned error: %v", err)
	}
	now := time.Now().UTC()
	event, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID:             "event-1",
		Topic:          "webhook.github.push",
		Source:         domain.TopicEventSourceWebhook,
		Provider:       "github",
		Intent:         "push",
		CorrelationID:  "corr-1",
		IdempotencyKey: "idem-1",
		PayloadJSON:    `{"branch":"main"}`,
		DispatchStatus: domain.TopicEventDispatchPending,
		CreatedAt:      now,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}
	if event.Sequence == 0 {
		t.Fatalf("expected event sequence")
	}
	duplicate, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID:             "event-duplicate",
		Topic:          event.Topic,
		Source:         domain.TopicEventSourceWebhook,
		CorrelationID:  "corr-1",
		IdempotencyKey: event.IdempotencyKey,
		PayloadJSON:    event.PayloadJSON,
		DispatchStatus: domain.TopicEventDispatchPending,
	})
	if err != nil || duplicate.ID != event.ID {
		t.Fatalf("idempotent CreateEvent duplicate=%#v err=%v", duplicate, err)
	}
	if _, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID:             "event-conflict",
		Topic:          event.Topic,
		Source:         domain.TopicEventSourceWebhook,
		CorrelationID:  "corr-1",
		IdempotencyKey: event.IdempotencyKey,
		PayloadJSON:    `{"branch":"other"}`,
		DispatchStatus: domain.TopicEventDispatchPending,
	}); err == nil {
		t.Fatalf("CreateEvent idempotency conflict returned nil error")
	}
	child, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID:             "event-child",
		Topic:          event.Topic,
		Source:         domain.TopicEventSourceSystem,
		CorrelationID:  "corr-1",
		ParentEventID:  event.ID,
		PayloadJSON:    `{}`,
		DispatchStatus: domain.TopicEventDispatchPending,
	})
	if err != nil {
		t.Fatalf("CreateEvent child returned error: %v", err)
	}
	if got, err := store.GetEvent(ctx, event.ID); err != nil || got.ID != event.ID {
		t.Fatalf("GetEvent got=%#v err=%v", got, err)
	}
	if _, err := store.GetEvent(ctx, ""); err == nil {
		t.Fatalf("GetEvent empty id returned nil error")
	}
	if _, err := store.GetEvent(ctx, "missing"); err == nil {
		t.Fatalf("GetEvent missing id returned nil error")
	}
	if got, found, err := store.FindEventByIdempotencyKey(ctx, event.Topic, event.IdempotencyKey); err != nil || !found || got.ID != event.ID {
		t.Fatalf("FindEventByIdempotencyKey got=%#v found=%v err=%v", got, found, err)
	}
	if got, found, err := store.FindEventByIdempotencyKey(ctx, "", event.IdempotencyKey); err != nil || found || got.ID != "" {
		t.Fatalf("FindEventByIdempotencyKey empty topic got=%#v found=%v err=%v", got, found, err)
	}
	if pending, err := store.ListPendingEvents(ctx, 10); err != nil || len(pending) != 2 {
		t.Fatalf("ListPendingEvents pending=%#v err=%v", pending, err)
	}
	if events, err := store.ListEvents(ctx, domain.TopicEventFilter{Topic: event.Topic, CorrelationID: "corr-1", Limit: 10}); err != nil || len(events) != 2 {
		t.Fatalf("ListEvents events=%#v err=%v", events, err)
	}
	if events, err := store.ListEvents(ctx, domain.TopicEventFilter{Topic: event.Topic, AfterSequence: event.Sequence, DispatchStatus: domain.TopicEventDispatchPending, Limit: 1000}); err != nil || len(events) != 1 {
		t.Fatalf("ListEvents filtered events=%#v err=%v", events, err)
	}
	if _, err := store.ListEvents(ctx, domain.TopicEventFilter{}); err == nil {
		t.Fatalf("ListEvents empty filter returned nil error")
	}
	if _, err := store.ListEvents(ctx, domain.TopicEventFilter{Topic: "bad topic"}); err == nil {
		t.Fatalf("ListEvents invalid topic returned nil error")
	}
	if err := store.UpdateEventPayload(ctx, event.ID, `{"branch":"dev"}`); err != nil {
		t.Fatalf("UpdateEventPayload returned error: %v", err)
	}
	if err := store.UpdateEventPayload(ctx, "", `{}`); err == nil {
		t.Fatalf("UpdateEventPayload empty id returned nil error")
	}
	if err := store.UpdateEventPayload(ctx, event.ID, " "); err == nil {
		t.Fatalf("UpdateEventPayload empty payload returned nil error")
	}
	if err := store.UpdateEventPayload(ctx, "missing", `{}`); err == nil {
		t.Fatalf("UpdateEventPayload missing event returned nil error")
	}
	dispatchable, err := store.ListDispatchableEvents(ctx, now, 10)
	if err != nil || len(dispatchable) != 2 {
		t.Fatalf("ListDispatchableEvents events=%#v err=%v", dispatchable, err)
	}
	if _, err := store.ClaimEvent(ctx, "", "claim", now, now.Add(time.Minute)); err == nil {
		t.Fatalf("ClaimEvent empty id returned nil error")
	}
	claimed, err := store.ClaimEvent(ctx, event.ID, "claim-1", now, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("ClaimEvent claimed=%v err=%v", claimed, err)
	}
	if claimed, err := store.ClaimEvent(ctx, event.ID, "claim-ignored", now, now.Add(time.Minute)); err != nil || claimed {
		t.Fatalf("ClaimEvent active claim claimed=%v err=%v", claimed, err)
	}
	if err := store.ReleaseEventClaim(ctx, "", "claim", domain.TopicEventDispatchRetrying, "", time.Time{}); err == nil {
		t.Fatalf("ReleaseEventClaim empty id returned nil error")
	}
	if err := store.ReleaseEventClaim(ctx, event.ID, "claim-1", domain.TopicEventDispatchRetrying, "retry", now.Add(time.Millisecond)); err != nil {
		t.Fatalf("ReleaseEventClaim returned error: %v", err)
	}
	claimed, err = store.ClaimEvent(ctx, event.ID, "claim-2", now.Add(time.Second), now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("ClaimEvent second claimed=%v err=%v", claimed, err)
	}
	if err := store.MarkEventPublished(ctx, event.ID, "claim-2", now); err != nil {
		t.Fatalf("MarkEventPublished returned error: %v", err)
	}
	if err := store.MarkEventPublished(ctx, "missing", "claim-missing", time.Time{}); err == nil {
		t.Fatalf("MarkEventPublished missing event returned nil error")
	}
	if err := store.MarkEventPublished(ctx, event.ID, "wrong-claim", time.Time{}); err != nil {
		t.Fatalf("MarkEventPublished stale claim returned error: %v", err)
	}
	claimed, err = store.ClaimEvent(ctx, child.ID, "claim-child", now, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("ClaimEvent child claimed=%v err=%v", claimed, err)
	}
	if err := store.MarkEventNoSubscriber(ctx, "", "claim-child", time.Time{}); err == nil {
		t.Fatalf("MarkEventNoSubscriber empty id returned nil error")
	}
	if err := store.MarkEventNoSubscriber(ctx, child.ID, "claim-child", now); err != nil {
		t.Fatalf("MarkEventNoSubscriber returned error: %v", err)
	}
	if err := store.MarkEventNoSubscriber(ctx, "missing", "claim-missing", time.Time{}); err == nil {
		t.Fatalf("MarkEventNoSubscriber missing event returned nil error")
	}
	if ids, err := store.ListDescendantEventIDs(ctx, event.ID, 10); err != nil || len(ids) != 2 {
		t.Fatalf("ListDescendantEventIDs ids=%#v err=%v", ids, err)
	}

	if err := store.UpsertEventDelivery(ctx, domain.EventDelivery{EventID: event.ID, LoaderID: "loader-1", TriggerID: "trigger-1", RunID: "run-1", Status: domain.EventDeliveryStatusRunSucceeded}); err != nil {
		t.Fatalf("UpsertEventDelivery returned error: %v", err)
	}
	if err := store.UpsertEventDelivery(ctx, domain.EventDelivery{}); err == nil {
		t.Fatalf("UpsertEventDelivery empty delivery returned nil error")
	}
	if deliveries, err := store.ListEventDeliveries(ctx, []string{"", event.ID, event.ID}); err != nil || len(deliveries) != 1 {
		t.Fatalf("ListEventDeliveries deliveries=%#v err=%v", deliveries, err)
	}
	if err := store.AddEventSandboxLink(ctx, domain.EventSandboxLink{EventID: event.ID, SandboxID: "sandbox-1", Relation: "created", LoaderID: "loader-1", RunID: "run-1", TriggerID: "trigger-1", LoaderEventID: "loader-event-1"}); err != nil {
		t.Fatalf("AddEventSandboxLink returned error: %v", err)
	}
	if err := store.AddEventSandboxLink(ctx, domain.EventSandboxLink{}); err == nil {
		t.Fatalf("AddEventSandboxLink empty link returned nil error")
	}
	if links, err := store.ListEventSandboxLinks(ctx, []string{event.ID}); err != nil || len(links) != 1 || links[0].SandboxID != "sandbox-1" {
		t.Fatalf("ListEventSandboxLinks links=%#v err=%v", links, err)
	}
	if links, err := store.ListEventSandboxLinks(ctx, nil); err != nil || len(links) != 0 {
		t.Fatalf("ListEventSandboxLinks empty links=%#v err=%v", links, err)
	}
	if deliveries, err := store.ListEventDeliveries(ctx, nil); err != nil || len(deliveries) != 0 {
		t.Fatalf("ListEventDeliveries empty deliveries=%#v err=%v", deliveries, err)
	}

	webhook, err := store.UpsertWebhookSource(ctx, domain.WebhookSource{
		ID: "github", Name: "GitHub", Enabled: true, Provider: "github", TopicPrefix: "webhook.github.",
		TokenHash: "hash", TokenHeader: "x-github-token", SignatureType: "hmac-sha256", SignatureSecret: "secret", BodyLimitBytes: 1024,
	})
	if err != nil {
		t.Fatalf("UpsertWebhookSource returned error: %v", err)
	}
	if webhook.Name != "GitHub" || webhook.TokenHeader != "x-github-token" {
		t.Fatalf("webhook source = %#v", webhook)
	}
	if !WebhookSourceTopicMatches("webhook.github.push", "webhook.github.") || WebhookSourceTopicMatches("", "webhook.github.") {
		t.Fatalf("WebhookSourceTopicMatches returned unexpected values")
	}
	if enabled, err := store.ListEnabledWebhookSourcesForTopic(ctx, "webhook.github.push"); err != nil || len(enabled) != 1 {
		t.Fatalf("ListEnabledWebhookSourcesForTopic enabled=%#v err=%v", enabled, err)
	}
	if sources, err := store.ListWebhookSources(ctx); err != nil || len(sources) != 1 {
		t.Fatalf("ListWebhookSources sources=%#v err=%v", sources, err)
	}
	if got, found, err := store.GetWebhookSource(ctx, webhook.ID); err != nil || !found || got.ID != webhook.ID || got.TokenHeader != "x-github-token" {
		t.Fatalf("GetWebhookSource got=%#v found=%v err=%v", got, found, err)
	}
	if _, err := store.UpsertWebhookSource(ctx, domain.WebhookSource{ID: "bad", TopicPrefix: "webhook.bad.", TokenHeader: "Bad Header"}); err == nil {
		t.Fatalf("UpsertWebhookSource with invalid token header returned nil error")
	}
	if err := store.DeleteWebhookSource(ctx, webhook.ID); err != nil {
		t.Fatalf("DeleteWebhookSource returned error: %v", err)
	}
	if _, found, err := store.GetWebhookSource(ctx, webhook.ID); err != nil || found {
		t.Fatalf("GetWebhookSource deleted found=%v err=%v", found, err)
	}
}

func testConfigStoreCRUDCoverageWorkflows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema returned error: %v", err)
	}
	assertSandboxNamedSQLiteSchema(t, store)
	if store.DB() == nil {
		t.Fatalf("DB returned nil")
	}
	if _, err := store.TableColumnTypes(ctx, "workspace_config"); err != nil {
		t.Fatalf("TableColumnTypes returned error: %v", err)
	}
	if err := store.EnsureGlobalEnvSchema(ctx); err != nil {
		t.Fatalf("EnsureGlobalEnvSchema returned error: %v", err)
	}
	if err := store.EnsureWorkspaceConfigSchema(ctx); err != nil {
		t.Fatalf("EnsureWorkspaceConfigSchema returned error: %v", err)
	}
	if err := store.EnsureAgentDefinitionSchema(ctx); err != nil {
		t.Fatalf("EnsureAgentDefinitionSchema returned error: %v", err)
	}
	if err := store.EnsureCapabilityGatewaySchema(ctx); err != nil {
		t.Fatalf("EnsureCapabilityGatewaySchema returned error: %v", err)
	}
	if err := store.EnsureLoaderSchema(ctx); err != nil {
		t.Fatalf("EnsureLoaderSchema returned error: %v", err)
	}
	if err := store.EnsureProjectSchema(ctx); err != nil {
		t.Fatalf("EnsureProjectSchema returned error: %v", err)
	}
	if err := store.EnsureEventSchema(ctx); err != nil {
		t.Fatalf("EnsureEventSchema returned error: %v", err)
	}

	if saved, err := store.SaveCapabilityGateway(ctx, domain.CapabilityGatewaySettings{Addr: "http://octobus", Token: "token"}); err != nil || saved.Addr == "" {
		t.Fatalf("SaveCapabilityGateway saved=%#v err=%v", saved, err)
	}
	if gateway, err := store.GetCapabilityGateway(ctx); err != nil || gateway.Token != "token" {
		t.Fatalf("GetCapabilityGateway gateway=%#v err=%v", gateway, err)
	}

	if _, err := store.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{{Name: "A", Value: "1"}, {Name: "SECRET", Value: "2", Secret: true}}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	if env, err := store.ListGlobalEnv(ctx); err != nil || len(env) != 2 {
		t.Fatalf("ListGlobalEnv env=%#v err=%v", env, err)
	}

	workspace, err := store.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{ID: "workspace-1", Name: "Workspace", Type: "file", ConfigJSON: `{"path":"/tmp/work"}`, Comment: "comment"})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	workspace.Name = "Workspace Updated"
	if _, err := store.UpdateWorkspaceConfig(ctx, workspace); err != nil {
		t.Fatalf("UpdateWorkspaceConfig returned error: %v", err)
	}
	if items, err := store.ListWorkspaceConfigs(ctx); err != nil || len(items) != 1 {
		t.Fatalf("ListWorkspaceConfigs items=%#v err=%v", items, err)
	}

	agent, err := store.CreateAgentDefinition(ctx, domain.AgentDefinition{
		ID: "agent-1", Name: "Agent", Enabled: true, Provider: "codex", Model: "gpt", SystemPrompt: "prompt",
		Driver: driverpkg.RuntimeDriverBoxlite, GuestImage: "guest:latest", WorkspaceID: workspace.ID,
		EnvItems: []domain.SandboxEnvVar{{Name: "TOKEN", Value: "secret", Secret: true}}, CapsetIDs: []string{"dev"},
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	if !AgentMatchesQuery(agent, "agent") || !AgentMatchesQuery(agent, "codex") {
		t.Fatalf("AgentMatchesQuery failed")
	}
	agent.Description = "updated"
	if _, err := store.UpdateAgentDefinition(ctx, agent); err != nil {
		t.Fatalf("UpdateAgentDefinition returned error: %v", err)
	}
	if _, err := store.GetAgentDefinitionIncludingDeleted(ctx, agent.ID); err != nil {
		t.Fatalf("GetAgentDefinitionIncludingDeleted returned error: %v", err)
	}
	if result, err := store.ListAgentDefinitions(ctx, domain.AgentDefinitionListOptions{Query: "agent", IncludeDisabled: true, Limit: 10}); err != nil || result.TotalCount != 1 {
		t.Fatalf("ListAgentDefinitions result=%#v err=%v", result, err)
	}
	managedAgent, err := store.UpsertManagedAgentDefinition(ctx, domain.AgentDefinition{
		ID: "managed-agent-1", Name: "Managed", Enabled: true, Provider: "codex", ManagedProjectID: "project-1", ManagedAgentName: "worker", ManagedProjectRevision: 1,
	})
	if err != nil {
		t.Fatalf("UpsertManagedAgentDefinition returned error: %v", err)
	}
	if managedAgents, err := store.ListManagedAgentDefinitions(ctx, "project-1", true); err != nil || len(managedAgents) != 1 || managedAgents[0].ID != managedAgent.ID {
		t.Fatalf("ListManagedAgentDefinitions agents=%#v err=%v", managedAgents, err)
	}
	if _, err := store.SetAgentDefinitionEnabled(ctx, agent.ID, false); err != nil {
		t.Fatalf("SetAgentDefinitionEnabled returned error: %v", err)
	}

	loader, err := store.CreateLoader(ctx, Loader{
		Summary:  domain.LoaderSummary{ID: "loader-1", Name: "Loader", Enabled: true, Runtime: domain.LoaderRuntimeScheduler, DefaultAgent: "agent-1", AgentID: agent.ID},
		Script:   "function main(){}",
		EnvItems: []domain.SandboxEnvVar{{Name: "LOADER", Value: "value"}},
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	triggers, err := store.ReplaceLoaderTriggers(ctx, loader.Summary.ID, []domain.LoaderTrigger{
		{ID: "interval", Kind: domain.LoaderTriggerKindInterval, IntervalMs: 1000, Enabled: true},
		{ID: "event", Kind: domain.LoaderTriggerKindEvent, Topic: "topic", Enabled: true},
	})
	if err != nil || len(triggers) != 2 {
		t.Fatalf("ReplaceLoaderTriggers triggers=%#v err=%v", triggers, err)
	}
	if err := store.SetLoaderEnabled(ctx, loader.Summary.ID, false); err != nil {
		t.Fatalf("SetLoaderEnabled false returned error: %v", err)
	}
	if err := store.SetLoaderEnabled(ctx, loader.Summary.ID, true); err != nil {
		t.Fatalf("SetLoaderEnabled true returned error: %v", err)
	}
	if err := store.SetLoaderTriggerEnabled(ctx, loader.Summary.ID, "interval", false); err != nil {
		t.Fatalf("SetLoaderTriggerEnabled returned error: %v", err)
	}
	if err := store.SetLoaderTriggerEnabled(ctx, loader.Summary.ID, "interval", true); err != nil {
		t.Fatalf("SetLoaderTriggerEnabled true returned error: %v", err)
	}
	if err := store.SetLoaderTriggerEnabled(ctx, loader.Summary.ID, "missing", true); err == nil {
		t.Fatalf("SetLoaderTriggerEnabled missing trigger returned nil error")
	}
	if err := store.MarkLoaderTriggerFired(ctx, loader.Summary.ID, "interval", time.Now().UTC(), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("MarkLoaderTriggerFired returned error: %v", err)
	}
	if err := store.UpdateLoaderLastError(ctx, loader.Summary.ID, "last error"); err != nil {
		t.Fatalf("UpdateLoaderLastError returned error: %v", err)
	}
	if err := store.UpdateLoaderLastError(ctx, "", "last error"); err == nil {
		t.Fatalf("UpdateLoaderLastError empty id returned nil error")
	}
	loader.Summary.Description = "updated"
	if _, err := store.UpdateLoader(ctx, loader); err != nil {
		t.Fatalf("UpdateLoader returned error: %v", err)
	}
	if _, err := store.UpsertManagedLoader(ctx, Loader{
		Summary: domain.LoaderSummary{ID: "managed-loader-1", Name: "Managed Loader", Enabled: true, Runtime: domain.LoaderRuntimeScheduler, DefaultAgent: "codex", ManagedProjectID: "project-1", ManagedAgentName: "worker", ManagedSchedulerID: "sched-1"},
		Script:  "function main(){}",
	}); err != nil {
		t.Fatalf("UpsertManagedLoader returned error: %v", err)
	}
	if managedLoaders, err := store.ListManagedLoaders(ctx, "project-1"); err != nil || len(managedLoaders) != 1 {
		t.Fatalf("ListManagedLoaders loaders=%#v err=%v", managedLoaders, err)
	}
	if _, found, err := store.GetLoaderIfExists(ctx, loader.Summary.ID); err != nil || !found {
		t.Fatalf("GetLoaderIfExists found=%v err=%v", found, err)
	}
	if summaries, err := store.ListLoaderSummaries(ctx); err != nil || len(summaries) < 1 {
		t.Fatalf("ListLoaderSummaries summaries=%#v err=%v", summaries, err)
	}
	if loaders, err := store.ListLoaders(ctx); err != nil || len(loaders) < 1 {
		t.Fatalf("ListLoaders loaders=%#v err=%v", loaders, err)
	}
	run := domain.LoaderRunSummary{ID: "run-1", LoaderID: loader.Summary.ID, TriggerID: "event", TriggerKind: domain.LoaderTriggerKindEvent, TriggerSource: "manual", Status: domain.LoaderRunStatusRunning, StartedAt: time.Now().UTC(), PayloadJSON: `{}`}
	if err := store.CreateLoaderRun(ctx, run); err != nil {
		t.Fatalf("CreateLoaderRun returned error: %v", err)
	}
	run.Status = domain.LoaderRunStatusSucceeded
	run.CompletedAt = time.Now().UTC()
	if err := store.UpdateLoaderRun(ctx, run); err != nil {
		t.Fatalf("UpdateLoaderRun returned error: %v", err)
	}
	missingRun := run
	missingRun.ID = "missing"
	if err := store.UpdateLoaderRun(ctx, missingRun); err == nil {
		t.Fatalf("UpdateLoaderRun missing run returned nil error")
	}
	if _, err := store.GetLoaderRun(ctx, loader.Summary.ID, run.ID); err != nil {
		t.Fatalf("GetLoaderRun returned error: %v", err)
	}
	if _, err := store.GetLoaderRun(ctx, loader.Summary.ID, "missing"); err == nil {
		t.Fatalf("GetLoaderRun missing run returned nil error")
	}
	if runs, err := store.ListLoaderRuns(ctx, loader.Summary.ID, 10); err != nil || len(runs) != 1 {
		t.Fatalf("ListLoaderRuns runs=%#v err=%v", runs, err)
	}
	if runs, err := store.ListLoaderRuns(ctx, loader.Summary.ID, 0); err != nil || len(runs) != 1 {
		t.Fatalf("ListLoaderRuns default limit runs=%#v err=%v", runs, err)
	}
	if runs, err := store.ListRecentLoaderRuns(ctx, 10); err != nil || len(runs) != 1 {
		t.Fatalf("ListRecentLoaderRuns runs=%#v err=%v", runs, err)
	}
	if runs, err := store.ListRecentLoaderRuns(ctx, 0); err != nil || len(runs) != 1 {
		t.Fatalf("ListRecentLoaderRuns default limit runs=%#v err=%v", runs, err)
	}
	if err := store.AddLoaderEvent(ctx, domain.LoaderEvent{ID: "event-1", LoaderID: loader.Summary.ID, RunID: run.ID, Type: "loader.test", Level: "info", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("AddLoaderEvent returned error: %v", err)
	}
	if events, err := store.ListLoaderEvents(ctx, loader.Summary.ID, 10); err != nil || len(events) != 1 {
		t.Fatalf("ListLoaderEvents events=%#v err=%v", events, err)
	}
	if err := store.SetLoaderState(ctx, loader.Summary.ID, "key", `{"ok":true}`); err != nil {
		t.Fatalf("SetLoaderState returned error: %v", err)
	}
	if value, found, err := store.GetLoaderState(ctx, loader.Summary.ID, "key"); err != nil || !found || value == "" {
		t.Fatalf("GetLoaderState value=%q found=%v err=%v", value, found, err)
	}
	if err := store.DeleteLoaderState(ctx, loader.Summary.ID, "key"); err != nil {
		t.Fatalf("DeleteLoaderState returned error: %v", err)
	}
	if value, found, err := store.GetLoaderState(ctx, loader.Summary.ID, "key"); err != nil || found || value != "" {
		t.Fatalf("GetLoaderState deleted value=%q found=%v err=%v", value, found, err)
	}
	if err := store.SetLoaderState(ctx, "", "key", `{}`); err == nil {
		t.Fatalf("SetLoaderState empty loader returned nil error")
	}
	if err := store.UpsertLoaderBinding(ctx, domain.LoaderBinding{LoaderID: loader.Summary.ID, SandboxID: "sandbox-1"}); err != nil {
		t.Fatalf("UpsertLoaderBinding returned error: %v", err)
	}
	if binding, found, err := store.GetLoaderBinding(ctx, loader.Summary.ID, ""); err != nil || !found || binding.SandboxID != "sandbox-1" {
		t.Fatalf("GetLoaderBinding binding=%#v found=%v err=%v", binding, found, err)
	}
	if err := store.UpsertLoaderBinding(ctx, domain.LoaderBinding{LoaderID: loader.Summary.ID, TriggerID: "trigger-1", SandboxID: "sandbox-2"}); err != nil {
		t.Fatalf("UpsertLoaderBinding trigger scope returned error: %v", err)
	}
	if binding, found, err := store.GetLoaderBinding(ctx, loader.Summary.ID, "trigger-1"); err != nil || !found || binding.SandboxID != "sandbox-2" {
		t.Fatalf("GetLoaderBinding trigger binding=%#v found=%v err=%v", binding, found, err)
	}
	if binding, found, err := store.GetLoaderBinding(ctx, loader.Summary.ID, ""); err != nil || !found || binding.SandboxID != "sandbox-1" {
		t.Fatalf("loader-level binding changed: binding=%#v found=%v err=%v", binding, found, err)
	}
	if binding, found, err := store.GetLoaderBinding(ctx, "missing", ""); err != nil || found || binding.LoaderID != "" {
		t.Fatalf("GetLoaderBinding missing binding=%#v found=%v err=%v", binding, found, err)
	}
	if err := store.UpsertLoaderBinding(ctx, domain.LoaderBinding{}); err == nil {
		t.Fatalf("UpsertLoaderBinding empty binding returned nil error")
	}
	if disabled, err := store.DisableLoadersByDefaultAgent(ctx, agent.ID); err != nil || disabled < 1 {
		t.Fatalf("DisableLoadersByDefaultAgent disabled=%d err=%v", disabled, err)
	}

	if has, err := store.HasLLMProviders(ctx); err != nil || has {
		t.Fatalf("HasLLMProviders before seed has=%v err=%v", has, err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{ID: "provider-1", Name: "OpenAI", ProviderType: llms.ProviderFamilyOpenAI, BaseURL: "https://api.openai.com/v1", APIKey: "key", Enabled: true}, llms.Model{ID: "model-1", Name: "gpt", Enabled: true, DefaultModel: true}); err != nil {
		t.Fatalf("UpsertDefaultLLMConfig returned error: %v", err)
	}
	if providers, err := store.ListEnabledLLMProviders(ctx); err != nil || len(providers) != 1 {
		t.Fatalf("ListEnabledLLMProviders providers=%#v err=%v", providers, err)
	}
	if models, err := store.ListEnabledLLMModels(ctx); err != nil || len(models) != 1 {
		t.Fatalf("ListEnabledLLMModels models=%#v err=%v", models, err)
	}
	if _, ok, err := store.LLMProviderModelWireAPI(ctx, "provider-1", "model-1"); err != nil || !ok {
		t.Fatalf("LLMProviderModelWireAPI ok=%v err=%v", ok, err)
	}
	rawToken := "raw-token"
	hash, fingerprint := llms.HashFacadeToken(rawToken)
	if err := store.SaveLLMFacadeToken(ctx, llms.FacadeToken{SandboxID: "sandbox-1", TokenHash: hash, TokenFingerprint: fingerprint, Model: "model-1", ProviderID: "provider-1"}); err != nil {
		t.Fatalf("SaveLLMFacadeToken returned error: %v", err)
	}
	if token, err := store.GetLLMFacadeToken(ctx, rawToken); err != nil || token.SandboxID != "sandbox-1" {
		t.Fatalf("GetLLMFacadeToken token=%#v err=%v", token, err)
	}
	if err := store.RevokeLLMFacadeTokensForSandbox(ctx, "sandbox-1"); err != nil {
		t.Fatalf("RevokeLLMFacadeTokensForSandbox returned error: %v", err)
	}
	if err := store.DeleteLLMFacadeToken(ctx, rawToken); err != nil {
		t.Fatalf("DeleteLLMFacadeToken returned error: %v", err)
	}
	testConfigStoreLLMBootstrapResolveCoverage(t, ctx)

	if err := store.DeleteLoader(ctx, loader.Summary.ID); err != nil {
		t.Fatalf("DeleteLoader returned error: %v", err)
	}
	if err := store.DeleteAgentDefinition(ctx, agent.ID); err != nil {
		t.Fatalf("DeleteAgentDefinition returned error: %v", err)
	}
	if _, err := store.GetAgentDefinitionIncludingDeleted(ctx, agent.ID); err != nil {
		t.Fatalf("GetAgentDefinitionIncludingDeleted after delete returned error: %v", err)
	}
	if err := store.DeleteWorkspaceConfig(ctx, workspace.ID); err != nil {
		t.Fatalf("DeleteWorkspaceConfig returned error: %v", err)
	}
}

func testConfigStoreLLMBootstrapResolveCoverage(t *testing.T, ctx context.Context) {
	t.Helper()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema for LLM bootstrap returned error: %v", err)
	}
	config := &appconfig.Config{LLMAPIEndpoint: "https://config.example/v1", LLMAPIProtocol: "chat_completions", LLMAPIKey: "config-key", LLMModel: "config-model"}
	if lookup := llms.DefaultLLMEnvProviderLookup(ctx, config, store); lookup("LLM_API_ENDPOINT") != "https://config.example/v1" {
		t.Fatalf("config LLM lookup failed")
	}
	if _, err := store.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global.example/v1"},
		{Name: "LLM_API_PROTOCOL", Value: "chat_completions"},
		{Name: "LLM_API_KEY", Value: "global-key", Secret: true},
		{Name: "LLM_MODEL", Value: "global-model"},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv for LLM returned error: %v", err)
	}
	target, err := llms.ResolveLLMTarget(ctx, config, store, "")
	if err != nil {
		t.Fatalf("ResolveLLMTarget returned error: %v", err)
	}
	if target.Provider.ID != llms.ProviderIDDefaultOpenAI || target.Provider.APIKey != "global-key" || target.Model.ID != "global-model" || target.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("OpenAI resolved target = %#v", target)
	}
	runtimeTarget, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, config, store, "sandbox-1", llms.ProviderFamilyOpenAI, "session-model", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://session.example/v1"},
		{Name: "LLM_API_KEY", Value: "session-key", Secret: true},
		{Name: "LLM_MODEL", Value: "session-model"},
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv OpenAI returned error: %v", err)
	}
	if runtimeTarget.Provider.ID == target.Provider.ID || runtimeTarget.Provider.Scope != llms.ProviderScopeSessionEnv || runtimeTarget.Model.ID != "session-model" {
		t.Fatalf("session OpenAI target = %#v", runtimeTarget)
	}
	if llms.HasEnabledLLMProviderID(ctx, store, runtimeTarget.Provider.ID) != true {
		t.Fatalf("expected session provider to be enabled")
	}
	reusedRuntimeTarget, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, config, store, "sandbox-1", llms.ProviderFamilyOpenAI, "session-model", "", nil)
	if err != nil || reusedRuntimeTarget.Provider.ID != runtimeTarget.Provider.ID {
		t.Fatalf("reused session OpenAI target=%#v err=%v", reusedRuntimeTarget, err)
	}
	if _, err := llms.ResolveRuntimeLLMTarget(ctx, config, store, "missing-model", "missing-provider"); err == nil {
		t.Fatalf("expected missing runtime LLM target error")
	}

	anthropicStore := FromDB(newMemoryDB(t))
	if err := anthropicStore.initSchema(ctx); err != nil {
		t.Fatalf("initSchema for Anthropic returned error: %v", err)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "https://anthropic.example")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("ANTHROPIC_MODEL", "claude-test")
	anthropicTarget, err := llms.ResolveLLMTargetForProviderFamily(ctx, &appconfig.Config{}, anthropicStore, llms.ProviderFamilyAnthropic, "")
	if err != nil {
		t.Fatalf("ResolveLLMTargetForProviderFamily Anthropic returned error: %v", err)
	}
	if anthropicTarget.Provider.ProviderType != llms.ProviderFamilyAnthropic || anthropicTarget.WireAPI != llms.APIProtocolMessages || anthropicTarget.Model.ID != "claude-test" {
		t.Fatalf("Anthropic target = %#v", anthropicTarget)
	}
	sessionAnthropicID, err := llms.EnsureSessionAnthropicEnvProvider(ctx, anthropicStore, "session-2", "claude-session", []domain.SandboxEnvVar{
		{Name: "ANTHROPIC_API_KEY", Value: "session-anthropic-key", Secret: true},
		{Name: "ANTHROPIC_MODEL", Value: "claude-session"},
	})
	if err != nil || sessionAnthropicID == "" {
		t.Fatalf("EnsureSessionAnthropicEnvProvider id=%q err=%v", sessionAnthropicID, err)
	}
}

func testConfigStoreMigrationAndTimeParsingWorkflows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db := newMemoryDB(t)
	store := FromDB(db)

	if _, err := store.tableColumnTypes(ctx, " "); err == nil {
		t.Fatalf("empty table name returned nil error")
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE global_env (
		name TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		secret INTEGER NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy global env: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO global_env(name, value, secret, updated_at)
		VALUES ('A', 'one', 1, '2026-06-02T09:00:00Z')`); err != nil {
		t.Fatalf("insert legacy global env: %v", err)
	}
	if err := store.rebuildGlobalEnvTable(ctx); err != nil {
		t.Fatalf("rebuildGlobalEnvTable returned error: %v", err)
	}
	columns, err := store.tableColumnTypes(ctx, "global_env")
	if err != nil {
		t.Fatalf("tableColumnTypes returned error: %v", err)
	}
	if !IsIntegerColumnType(columns["updated_at"]) {
		t.Fatalf("updated_at column type = %q, want integer", columns["updated_at"])
	}
	items, err := store.ListGlobalEnv(ctx)
	if err != nil {
		t.Fatalf("ListGlobalEnv returned error: %v", err)
	}
	if len(items) != 1 || items[0].Name != "A" || !items[0].Secret {
		t.Fatalf("global env items = %#v", items)
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE workspace_config (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		config_json TEXT NOT NULL,
		comment TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy workspace config: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workspace_config(id, name, type, config_json, comment, created_at, updated_at)
		VALUES ('ws-1', 'Workspace', 'file', '{}', 'legacy', '2026-06-02T09:00:00.000Z', '2026-06-02T09:01:00Z')`); err != nil {
		t.Fatalf("insert legacy workspace config: %v", err)
	}
	if err := store.rebuildWorkspaceConfigTable(ctx); err != nil {
		t.Fatalf("rebuildWorkspaceConfigTable returned error: %v", err)
	}
	workspace, err := store.GetWorkspaceConfig(ctx, "ws-1")
	if err != nil {
		t.Fatalf("GetWorkspaceConfig returned error: %v", err)
	}
	if workspace.Name != "Workspace" || workspace.Type != "file" || workspace.CreatedAt.IsZero() || workspace.UpdatedAt.IsZero() {
		t.Fatalf("workspace = %#v", workspace)
	}

	if !ParseStoredLoaderTriggerTime(int(1000)).Equal(time.Unix(1000, 0).UTC()) {
		t.Fatalf("ParseStoredLoaderTriggerTime int failed")
	}
	if !ParseStoredLoaderTriggerTime(float64(1000)).Equal(time.Unix(1000, 0).UTC()) {
		t.Fatalf("ParseStoredLoaderTriggerTime float failed")
	}
	if !ParseStoredLoaderTriggerTime([]byte("1000")).Equal(time.Unix(1000, 0).UTC()) {
		t.Fatalf("ParseStoredLoaderTriggerTime bytes failed")
	}
	if !ParseStoredLoaderTriggerTime(" ").IsZero() || !ParseStoredLoaderTriggerTime(struct{}{}).IsZero() {
		t.Fatalf("ParseStoredLoaderTriggerTime empty/default failed")
	}
	if !ParseStoredTime("2026-06-02T09:00:00.000Z").Equal(time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("ParseStoredTime custom layout failed")
	}
	if !strings.Contains(NormalizeSQLiteTimestampExpr("updated_at"), "updated_at") {
		t.Fatalf("NormalizeSQLiteTimestampExpr missing column name")
	}
	if BoolToInt(true) != 1 || BoolToInt(false) != 0 {
		t.Fatalf("BoolToInt returned unexpected values")
	}
}

func testConfigStoreProjectSchemaMigrationWorkflows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db := newMemoryDB(t)
	store := FromDB(db)

	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema on empty db returned error: %v", err)
	}
	assertProjectSchema(t, store)
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("second initSchema on empty db returned error: %v", err)
	}
	assertProjectSchema(t, store)

	existingDB := newMemoryDB(t)
	configDB := FromDB(existingDB)
	for _, ensure := range []func(context.Context) error{
		configDB.ensureGlobalEnvSchema,
		configDB.ensureCapabilityGatewaySchema,
		configDB.ensureWorkspaceConfigSchema,
		configDB.ensureLoaderSchema,
		configDB.ensureAgentDefinitionSchema,
		configDB.ensureEventSchema,
	} {
		if err := ensure(ctx); err != nil {
			t.Fatalf("prepare existing schema returned error: %v", err)
		}
	}

	agent, err := configDB.CreateAgentDefinition(ctx, domain.AgentDefinition{
		ID:           "agent-existing",
		Name:         "Existing Agent",
		Provider:     "codex",
		Model:        "gpt-test",
		Driver:       driverpkg.RuntimeDriverBoxlite,
		GuestImage:   "guest:latest",
		SystemPrompt: "keep me",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	loader, err := configDB.CreateLoader(ctx, Loader{
		Summary: domain.LoaderSummary{
			ID:           "loader-existing",
			Name:         "Existing Loader",
			Runtime:      domain.LoaderRuntimeScheduler,
			Enabled:      true,
			DefaultAgent: "codex",
		},
		Script: `{"steps":[]}`,
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	if _, err := configDB.db.ExecContext(ctx, `INSERT INTO loader_run(
		loader_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at
	) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		loader.Summary.ID, "run-existing", "manual", domain.LoaderTriggerKindEvent, "legacy", domain.LoaderRunStatusRunning, time.Now().UTC().UnixMilli()); err != nil {
		t.Fatalf("insert existing loader run: %v", err)
	}

	sessionStore, err := sessionstore.NewWithConfig(&appconfig.Config{
		SandboxRoot:          filepath.Join(t.TempDir(), "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		JupyterProxyBasePath: "/agent-compose/session",
		JupyterGuestPort:     8888,
	})
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := sessionStore.CreateSandbox(ctx, "Legacy Session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, []domain.SandboxTag{{Name: "legacy", Value: "true"}})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	if err := configDB.initSchema(ctx); err != nil {
		t.Fatalf("initSchema on existing db returned error: %v", err)
	}
	assertProjectSchema(t, configDB)

	loadedAgent, err := configDB.GetAgentDefinition(ctx, agent.ID)
	if err != nil {
		t.Fatalf("GetAgentDefinition after migration returned error: %v", err)
	}
	if loadedAgent.Name != agent.Name || loadedAgent.Provider != agent.Provider || loadedAgent.Model != agent.Model {
		t.Fatalf("loaded agent after migration = %#v, want %#v", loadedAgent, agent)
	}
	loadedLoader, err := configDB.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader after migration returned error: %v", err)
	}
	if loadedLoader.Summary.Name != loader.Summary.Name || loadedLoader.Summary.RunCount != 1 {
		t.Fatalf("loaded loader after migration = %#v", loadedLoader)
	}
	run, err := configDB.GetLoaderRun(ctx, loader.Summary.ID, "run-existing")
	if err != nil {
		t.Fatalf("GetLoaderRun after migration returned error: %v", err)
	}
	if run.Status != domain.LoaderRunStatusRunning || run.TriggerSource != "legacy" {
		t.Fatalf("loader run after migration = %#v", run)
	}
	loadedSession, err := sessionStore.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession after config migration returned error: %v", err)
	}
	if loadedSession.Summary.Title != "Legacy Session" || len(loadedSession.Summary.Tags) != 1 {
		t.Fatalf("loaded session after config migration = %#v", loadedSession)
	}
}

func assertProjectSchema(t *testing.T, store *ConfigStore) {
	t.Helper()
	for table, columns := range map[string][]string{
		"project":          {"id", "name", "source_path", "source_json", "current_revision", "spec_hash", "created_at", "updated_at", "removed_at"},
		"project_revision": {"project_id", "revision", "spec_hash", "spec_json", "created_at"},
		"project_agent":    {"project_id", "agent_name", "managed_agent_id", "revision", "provider", "model", "image", "driver", "scheduler_enabled", "spec_json", "created_at", "updated_at"},
		"project_scheduler": {"project_id", "scheduler_id", "agent_name", "managed_loader_id", "revision", "enabled", "trigger_count", "spec_json",
			"created_at", "updated_at"},
		"project_run": {"run_id", "project_id", "project_name", "project_revision", "agent_name", "managed_agent_id", "source", "scheduler_id", "trigger_id", "status",
			"sandbox_id", "exit_code", "error", "prompt", "output", "result_json", "logs_path", "artifacts_dir", "cleanup_error", "driver", "image_ref", "started_at",
			"completed_at", "duration_ms", "created_at", "updated_at"},
		"agent_definition": {"managed_project_id", "managed_project_revision", "managed_agent_name"},
		"loader":           {"managed_project_id", "managed_project_revision", "managed_agent_name", "managed_scheduler_id"},
	} {
		assertTableColumns(t, store, table, columns...)
	}
	for _, index := range []string{
		"idx_project_name",
		"idx_project_source_path",
		"idx_project_revision_hash",
		"idx_project_agent_managed_agent",
		"idx_project_scheduler_agent",
		"idx_project_scheduler_managed_loader",
		"idx_project_run_project_status",
		"idx_project_run_agent",
		"idx_project_run_sandbox",
		"idx_project_run_scheduler",
		"idx_agent_definition_managed_project",
		"idx_loader_managed_project",
	} {
		assertSQLiteIndexExists(t, store.db, index)
	}
	assertSQLiteIndexUnique(t, store.db, "idx_project_revision_hash", false)
}

func assertTableColumns(t *testing.T, store *ConfigStore, table string, columns ...string) {
	t.Helper()
	columnTypes, err := store.tableColumnTypes(context.Background(), table)
	if err != nil {
		t.Fatalf("tableColumnTypes(%s) returned error: %v", table, err)
	}
	if len(columnTypes) == 0 {
		t.Fatalf("table %s does not exist or has no columns", table)
	}
	for _, column := range columns {
		if _, ok := columnTypes[column]; !ok {
			t.Fatalf("table %s missing column %s; columns=%v", table, column, columnTypes)
		}
	}
}

func assertSandboxNamedSQLiteSchema(t *testing.T, store *ConfigStore) {
	t.Helper()
	assertTableColumns(t, store, "loader", "sandbox_policy")
	assertTableMissingColumns(t, store, "loader", "session_policy")
	assertTableColumns(t, store, "loader_binding", "sandbox_id")
	assertTableMissingColumns(t, store, "loader_binding", "session_id")
	assertTableColumns(t, store, "loader_event", "linked_sandbox_id", "linked_agent_thread_id")
	assertTableMissingColumns(t, store, "loader_event", "linked_session_id", "linked_agent_session_id")
	assertTableColumns(t, store, "event_sandbox_link", "sandbox_id")
	assertTableDoesNotExist(t, store, "event_session_link")
	assertTableColumns(t, store, "llm_facade_token", "sandbox_id")
	assertTableMissingColumns(t, store, "llm_facade_token", "session_id")
	assertSQLiteIndexExists(t, store.db, "idx_llm_facade_token_sandbox")
}

func assertTableMissingColumns(t *testing.T, store *ConfigStore, table string, columns ...string) {
	t.Helper()
	columnTypes, err := store.tableColumnTypes(context.Background(), table)
	if err != nil {
		t.Fatalf("tableColumnTypes(%s) returned error: %v", table, err)
	}
	for _, column := range columns {
		if _, ok := columnTypes[column]; ok {
			t.Fatalf("table %s unexpectedly has column %s; columns=%v", table, column, columnTypes)
		}
	}
}

func assertTableDoesNotExist(t *testing.T, store *ConfigStore, table string) {
	t.Helper()
	columnTypes, err := store.tableColumnTypes(context.Background(), table)
	if err != nil {
		t.Fatalf("tableColumnTypes(%s) returned error: %v", table, err)
	}
	if len(columnTypes) != 0 {
		t.Fatalf("table %s unexpectedly exists; columns=%v", table, columnTypes)
	}
}

func assertSQLiteIndexExists(t *testing.T, db *sql.DB, indexName string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&count); err != nil {
		t.Fatalf("query sqlite index %s: %v", indexName, err)
	}
	if count != 1 {
		t.Fatalf("sqlite index %s count = %d, want 1", indexName, count)
	}
}

func assertSQLiteIndexUnique(t *testing.T, db *sql.DB, indexName string, want bool) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `PRAGMA index_list('project_revision')`)
	if err != nil {
		t.Fatalf("query sqlite index list: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var seq int
		var name string
		var unique int
		var origin string
		var partial int
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan sqlite index list: %v", err)
		}
		if name == indexName {
			if (unique != 0) != want {
				t.Fatalf("sqlite index %s unique = %v, want %v", indexName, unique != 0, want)
			}
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite index list: %v", err)
	}
	t.Fatalf("sqlite index %s not found", indexName)
}

func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestConfigStoreExportedSchemaHelpers(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	if _, err := db.ExecContext(ctx, `CREATE TABLE helper_columns (name TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create helper table: %v", err)
	}
	if err := EnsureColumn(ctx, db, "helper_columns", "value", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("EnsureColumn add returned error: %v", err)
	}
	if err := EnsureColumn(ctx, db, "helper_columns", "value", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("EnsureColumn existing returned error: %v", err)
	}
	types, err := TableColumnTypes(ctx, db, "helper_columns")
	if err != nil {
		t.Fatalf("TableColumnTypes returned error: %v", err)
	}
	if types["value"] != "TEXT" {
		t.Fatalf("column types = %#v", types)
	}

	store := FromDB(db)
	if err := store.InitCoreSchema(ctx); err != nil {
		t.Fatalf("InitCoreSchema returned error: %v", err)
	}
	if _, err := store.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{{Name: "A", Value: "1", Secret: true}}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	if _, err := store.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{ID: "workspace-1", Name: "Workspace", Type: "file", ConfigJSON: `{}`}); err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	if err := store.RebuildGlobalEnvTable(ctx); err != nil {
		t.Fatalf("RebuildGlobalEnvTable returned error: %v", err)
	}
	if err := store.RebuildWorkspaceConfigTable(ctx); err != nil {
		t.Fatalf("RebuildWorkspaceConfigTable returned error: %v", err)
	}
	env, err := store.ListGlobalEnv(ctx)
	if err != nil || len(env) != 1 || env[0].Name != "A" || !env[0].Secret {
		t.Fatalf("global env after rebuild = %#v err=%v", env, err)
	}
	workspace, err := store.GetWorkspaceConfig(ctx, "workspace-1")
	if err != nil || workspace.Name != "Workspace" {
		t.Fatalf("workspace after rebuild = %#v err=%v", workspace, err)
	}
}
