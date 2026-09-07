package configstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/events"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

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
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: "/tmp/project", SourceJSON: `{"kind":"local"}`, SpecHash: "hash-0"})
	if err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	project.Name = "project-renamed"
	if project, err = store.UpsertProject(ctx, project); err != nil || project.Name != "project-renamed" {
		t.Fatalf("UpsertProject rename project=%#v err=%v", project, err)
	}
	project.SourcePath = "/tmp/project-updated"
	if project, err = store.UpsertProject(ctx, project); err != nil || project.SourcePath != "/tmp/project-updated" {
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
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-2", Name: "other-project", SourcePath: "/tmp/other", SourceJSON: `{"kind":"local"}`}); err != nil {
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
		ProjectID: project.ID, AgentName: "worker", ID: "managed-agent-1", Revision: thirdRevision.Revision,
		Provider: "codex", Model: "gpt", Image: "guest:latest", Driver: driverpkg.RuntimeDriverBoxlite, SchedulerEnabled: true, SpecJSON: `{"name":"worker"}`,
	})
	if err != nil {
		t.Fatalf("UpsertProjectAgent returned error: %v", err)
	}
	agent.Model = "gpt-updated"
	if agent, err = store.UpsertProjectAgent(ctx, agent); err != nil || agent.Model != "gpt-updated" {
		t.Fatalf("UpsertProjectAgent update agent=%#v err=%v", agent, err)
	}
	if got, err := store.GetProjectAgent(ctx, project.ID, "worker"); err != nil || got.ID != "managed-agent-1" {
		t.Fatalf("GetProjectAgent got=%#v err=%v", got, err)
	}
	if _, err := store.GetProjectAgent(ctx, project.ID, "missing-agent"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetProjectAgent missing err=%v, want not found", err)
	}
	if agents, err := store.ListProjectAgents(ctx, project.ID); err != nil || len(agents) != 1 {
		t.Fatalf("ListProjectAgents agents=%#v err=%v", agents, err)
	}
	scheduler, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{
		ProjectID: project.ID, SchedulerID: "scheduler-1", AgentName: "worker", ID: "scheduler-1", Revision: thirdRevision.Revision, Enabled: true, TriggerCount: 2, SpecJSON: `{"id":"scheduler-1"}`,
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

	run, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-1", ProjectID: project.ID, ProjectName: project.Name, ProjectRevision: thirdRevision.Revision, AgentName: "worker", AgentID: agent.ID,
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
	if events, total, err := store.ListEvents(ctx, domain.TopicEventFilter{Topic: event.Topic, CorrelationID: "corr-1", Limit: 10}); err != nil || len(events) != 2 || total != 2 || events[0].ID != child.ID || events[1].ID != event.ID {
		t.Fatalf("ListEvents events=%#v total=%d err=%v", events, total, err)
	}
	if events, total, err := store.ListEvents(ctx, domain.TopicEventFilter{Topic: event.Topic, CorrelationID: "corr-1", Offset: 1, Limit: 1}); err != nil || len(events) != 1 || total != 2 || events[0].ID != event.ID {
		t.Fatalf("ListEvents second page events=%#v total=%d err=%v", events, total, err)
	}
	if events, total, err := store.ListEvents(ctx, domain.TopicEventFilter{Topic: event.Topic, CorrelationID: "corr-1", SequenceAsc: true, Limit: 10}); err != nil || len(events) != 2 || total != 2 || events[0].ID != event.ID || events[1].ID != child.ID {
		t.Fatalf("ListEvents legacy events=%#v total=%d err=%v", events, total, err)
	}
	if events, total, err := store.ListEvents(ctx, domain.TopicEventFilter{Topic: event.Topic, AfterSequence: event.Sequence, DispatchStatus: domain.TopicEventDispatchPending, Limit: 1000}); err != nil || len(events) != 1 || total != 1 || events[0].ID != child.ID {
		t.Fatalf("ListEvents filtered events=%#v total=%d err=%v", events, total, err)
	}
	if _, _, err := store.ListEvents(ctx, domain.TopicEventFilter{}); err == nil {
		t.Fatalf("ListEvents empty filter returned nil error")
	}
	if _, _, err := store.ListEvents(ctx, domain.TopicEventFilter{Topic: "bad topic"}); err == nil {
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
	if err := store.ReleaseEventClaim(ctx, events.ReleaseEventClaimRequest{ClaimID: "claim", Status: domain.TopicEventDispatchRetrying}); err == nil {
		t.Fatalf("ReleaseEventClaim empty id returned nil error")
	}
	if err := store.ReleaseEventClaim(ctx, events.ReleaseEventClaimRequest{EventID: event.ID, ClaimID: "claim-1", Status: domain.TopicEventDispatchRetrying, LastError: "retry", NextAttemptAt: now.Add(time.Millisecond)}); err != nil {
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

	if err := store.UpsertEventDelivery(ctx, domain.EventDelivery{EventID: event.ID, SchedulerID: "scheduler-1", TriggerID: "trigger-1", RunID: "run-1", Status: domain.EventDeliveryStatusRunSucceeded}); err != nil {
		t.Fatalf("UpsertEventDelivery returned error: %v", err)
	}
	if err := store.UpsertEventDelivery(ctx, domain.EventDelivery{}); err == nil {
		t.Fatalf("UpsertEventDelivery empty delivery returned nil error")
	}
	if deliveries, err := store.ListEventDeliveries(ctx, []string{"", event.ID, event.ID}); err != nil || len(deliveries) != 1 {
		t.Fatalf("ListEventDeliveries deliveries=%#v err=%v", deliveries, err)
	}
	if err := store.AddEventSandboxLink(ctx, domain.EventSandboxLink{EventID: event.ID, SandboxID: "sandbox-1", Relation: "created", SchedulerID: "scheduler-1", RunID: "run-1", TriggerID: "trigger-1", SchedulerEventID: "scheduler-event-1"}); err != nil {
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
		ID: "github", Name: "GitHub", Enabled: true, Provider: "generic", TopicPrefix: "webhook.github.",
		TokenHash: "hash", TokenHeader: "x-github-token", SignatureType: "hmac-sha256", SignatureSecret: "secret", BodyLimitBytes: 1024,
	})
	if err != nil {
		t.Fatalf("UpsertWebhookSource returned error: %v", err)
	}
	if webhook.Name != "GitHub" || webhook.TokenHeader != "x-github-token" {
		t.Fatalf("webhook source = %#v", webhook)
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
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("second InitSchema returned error: %v", err)
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

	scheduler, err := upsertNativeTestScheduler(ctx, store, Scheduler{
		Summary:  domain.SchedulerSummary{ID: "scheduler-1", Name: "Scheduler", Enabled: true, Runtime: domain.SchedulerRuntimeScheduler, DefaultAgent: "codex"},
		Script:   "function main(){}",
		EnvItems: []domain.SandboxEnvVar{{Name: "SCHEDULER", Value: "value"}},
	})
	if err != nil {
		t.Fatalf("create native scheduler returned error: %v", err)
	}
	triggers, err := store.ReplaceSchedulerTriggers(ctx, scheduler.Summary.ID, []domain.SchedulerTrigger{
		{ID: "interval", Kind: domain.SchedulerTriggerKindInterval, IntervalMs: 1000, Enabled: true},
		{ID: "event", Kind: domain.SchedulerTriggerKindEvent, Topic: "topic", Enabled: true},
	})
	if err != nil || len(triggers) != 2 {
		t.Fatalf("ReplaceSchedulerTriggers triggers=%#v err=%v", triggers, err)
	}
	if err := store.SetSchedulerEnabled(ctx, scheduler.Summary.ID, false); err != nil {
		t.Fatalf("SetSchedulerEnabled false returned error: %v", err)
	}
	if err := store.SetSchedulerEnabled(ctx, scheduler.Summary.ID, true); err != nil {
		t.Fatalf("SetSchedulerEnabled true returned error: %v", err)
	}
	if err := store.SetSchedulerTriggerEnabled(ctx, scheduler.Summary.ID, "interval", false); err != nil {
		t.Fatalf("SetSchedulerTriggerEnabled returned error: %v", err)
	}
	if err := store.SetSchedulerTriggerEnabled(ctx, scheduler.Summary.ID, "interval", true); err != nil {
		t.Fatalf("SetSchedulerTriggerEnabled true returned error: %v", err)
	}
	if err := store.SetSchedulerTriggerEnabled(ctx, scheduler.Summary.ID, "missing", true); err == nil {
		t.Fatalf("SetSchedulerTriggerEnabled missing trigger returned nil error")
	}
	if err := store.MarkSchedulerTriggerFired(ctx, scheduler.Summary.ID, "interval", time.Now().UTC(), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("MarkSchedulerTriggerFired returned error: %v", err)
	}
	if err := store.SetSchedulerTriggerNextFireAt(ctx, scheduler.Summary.ID, "interval", time.Now().UTC().Add(2*time.Hour)); err != nil {
		t.Fatalf("SetSchedulerTriggerNextFireAt returned error: %v", err)
	}
	if err := store.SetSchedulerTriggerNextFireAt(ctx, scheduler.Summary.ID, "missing", time.Now().UTC()); err == nil {
		t.Fatal("SetSchedulerTriggerNextFireAt missing trigger returned nil error")
	}
	if err := store.SetSchedulerTriggerNextFireAt(ctx, "", "interval", time.Now().UTC()); err == nil {
		t.Fatal("SetSchedulerTriggerNextFireAt empty scheduler returned nil error")
	}
	if err := store.UpdateSchedulerLastError(ctx, scheduler.Summary.ID, "last error"); err != nil {
		t.Fatalf("UpdateSchedulerLastError returned error: %v", err)
	}
	if err := store.UpdateSchedulerLastError(ctx, "", "last error"); err == nil {
		t.Fatalf("UpdateSchedulerLastError empty id returned nil error")
	}
	if loaded, err := store.GetScheduler(ctx, scheduler.Summary.ID); err != nil || loaded.Summary.ID != scheduler.Summary.ID {
		t.Fatalf("GetScheduler loaded=%#v err=%v", loaded, err)
	}
	if summaries, err := store.ListSchedulerSummaries(ctx); err != nil || len(summaries) < 1 {
		t.Fatalf("ListSchedulerSummaries summaries=%#v err=%v", summaries, err)
	}
	if schedulers, err := store.ListSchedulers(ctx); err != nil || len(schedulers) < 1 {
		t.Fatalf("ListSchedulers schedulers=%#v err=%v", schedulers, err)
	}
	run := domain.SchedulerRunSummary{ID: "run-1", SchedulerID: scheduler.Summary.ID, TriggerID: "event", TriggerKind: domain.SchedulerTriggerKindEvent, TriggerSource: "manual", Status: domain.SchedulerRunStatusRunning, StartedAt: time.Now().UTC(), PayloadJSON: `{}`}
	if err := store.CreateSchedulerRun(ctx, run); err != nil {
		t.Fatalf("CreateSchedulerRun returned error: %v", err)
	}
	run.Status = domain.SchedulerRunStatusSucceeded
	run.CompletedAt = time.Now().UTC()
	if err := store.UpdateSchedulerRun(ctx, run); err != nil {
		t.Fatalf("UpdateSchedulerRun returned error: %v", err)
	}
	missingRun := run
	missingRun.ID = "missing"
	if err := store.UpdateSchedulerRun(ctx, missingRun); err == nil {
		t.Fatalf("UpdateSchedulerRun missing run returned nil error")
	}
	if _, err := store.GetSchedulerRun(ctx, scheduler.Summary.ID, run.ID); err != nil {
		t.Fatalf("GetSchedulerRun returned error: %v", err)
	}
	if _, err := store.GetSchedulerRun(ctx, scheduler.Summary.ID, "missing"); err == nil {
		t.Fatalf("GetSchedulerRun missing run returned nil error")
	}
	if runs, err := store.ListSchedulerRuns(ctx, scheduler.Summary.ID, 10); err != nil || len(runs) != 1 {
		t.Fatalf("ListSchedulerRuns runs=%#v err=%v", runs, err)
	}
	if runs, err := store.ListSchedulerRuns(ctx, scheduler.Summary.ID, 0); err != nil || len(runs) != 1 {
		t.Fatalf("ListSchedulerRuns default limit runs=%#v err=%v", runs, err)
	}
	if runs, err := store.ListRecentSchedulerRuns(ctx, 10); err != nil || len(runs) != 1 {
		t.Fatalf("ListRecentSchedulerRuns runs=%#v err=%v", runs, err)
	}
	if runs, err := store.ListRecentSchedulerRuns(ctx, 0); err != nil || len(runs) != 1 {
		t.Fatalf("ListRecentSchedulerRuns default limit runs=%#v err=%v", runs, err)
	}
	if err := store.AddSchedulerEvent(ctx, domain.SchedulerEvent{ID: "event-1", SchedulerID: scheduler.Summary.ID, RunID: run.ID, Type: "scheduler.test", Level: "info", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("AddSchedulerEvent returned error: %v", err)
	}
	if events, err := store.ListSchedulerEvents(ctx, scheduler.Summary.ID, 10); err != nil || len(events) != 1 {
		t.Fatalf("ListSchedulerEvents events=%#v err=%v", events, err)
	}
	if err := store.SetSchedulerState(ctx, scheduler.Summary.ID, "key", `{"ok":true}`); err != nil {
		t.Fatalf("SetSchedulerState returned error: %v", err)
	}
	if value, found, err := store.GetSchedulerState(ctx, scheduler.Summary.ID, "key"); err != nil || !found || value == "" {
		t.Fatalf("GetSchedulerState value=%q found=%v err=%v", value, found, err)
	}
	if err := store.DeleteSchedulerState(ctx, scheduler.Summary.ID, "key"); err != nil {
		t.Fatalf("DeleteSchedulerState returned error: %v", err)
	}
	if value, found, err := store.GetSchedulerState(ctx, scheduler.Summary.ID, "key"); err != nil || found || value != "" {
		t.Fatalf("GetSchedulerState deleted value=%q found=%v err=%v", value, found, err)
	}
	if err := store.SetSchedulerState(ctx, "", "key", `{}`); err == nil {
		t.Fatalf("SetSchedulerState empty scheduler returned nil error")
	}
	if err := store.UpsertSchedulerBinding(ctx, domain.SchedulerBinding{SchedulerID: scheduler.Summary.ID, SandboxID: "sandbox-1"}); err != nil {
		t.Fatalf("UpsertSchedulerBinding returned error: %v", err)
	}
	if binding, found, err := store.GetSchedulerBinding(ctx, scheduler.Summary.ID, ""); err != nil || !found || binding.SandboxID != "sandbox-1" {
		t.Fatalf("GetSchedulerBinding binding=%#v found=%v err=%v", binding, found, err)
	}
	if err := store.UpsertSchedulerBinding(ctx, domain.SchedulerBinding{SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1", SandboxID: "sandbox-2", SandboxConfigHash: "sha256:config"}); err != nil {
		t.Fatalf("UpsertSchedulerBinding trigger scope returned error: %v", err)
	}
	if binding, found, err := store.GetSchedulerBinding(ctx, scheduler.Summary.ID, "trigger-1"); err != nil || !found || binding.SandboxID != "sandbox-2" || binding.SandboxConfigHash != "sha256:config" {
		t.Fatalf("GetSchedulerBinding trigger binding=%#v found=%v err=%v", binding, found, err)
	}
	wrongExpected := domain.SchedulerBinding{SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1", SandboxID: "stale", SandboxConfigHash: "sha256:config"}
	claimed, err := store.CompareAndSwapSchedulerBinding(ctx, &wrongExpected, domain.SchedulerBinding{SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1", SandboxID: "sandbox-3", SandboxConfigHash: "sha256:new"})
	if err != nil || claimed {
		t.Fatalf("CompareAndSwapSchedulerBinding stale expected claimed=%v err=%v, want false/nil", claimed, err)
	}
	current, found, err := store.GetSchedulerBinding(ctx, scheduler.Summary.ID, "trigger-1")
	if err != nil || !found {
		t.Fatalf("GetSchedulerBinding before compare-and-swap current=%#v found=%v err=%v", current, found, err)
	}
	claimed, err = store.CompareAndSwapSchedulerBinding(ctx, &current, domain.SchedulerBinding{SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1", SandboxID: "sandbox-3", SandboxConfigHash: "sha256:new"})
	if err != nil || !claimed {
		t.Fatalf("CompareAndSwapSchedulerBinding current claimed=%v err=%v, want true/nil", claimed, err)
	}
	claimed, err = store.CompareAndSwapSchedulerBinding(ctx, nil, domain.SchedulerBinding{SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1", SandboxID: "sandbox-4", SandboxConfigHash: "sha256:other"})
	if err != nil || claimed {
		t.Fatalf("CompareAndSwapSchedulerBinding occupied insert claimed=%v err=%v, want false/nil", claimed, err)
	}
	if binding, found, err := store.GetSchedulerBinding(ctx, scheduler.Summary.ID, ""); err != nil || !found || binding.SandboxID != "sandbox-1" {
		t.Fatalf("scheduler-level binding changed: binding=%#v found=%v err=%v", binding, found, err)
	}
	if binding, found, err := store.GetSchedulerBinding(ctx, "missing", ""); err != nil || found || binding.SchedulerID != "" {
		t.Fatalf("GetSchedulerBinding missing binding=%#v found=%v err=%v", binding, found, err)
	}
	if err := store.UpsertSchedulerBinding(ctx, domain.SchedulerBinding{}); err == nil {
		t.Fatalf("UpsertSchedulerBinding empty binding returned nil error")
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

	if err := store.DeleteWorkspaceConfig(ctx, workspace.ID); err != nil {
		t.Fatalf("DeleteWorkspaceConfig returned error: %v", err)
	}
}

func testConfigStoreLLMBootstrapResolveCoverage(t *testing.T, ctx context.Context) {
	t.Helper()
	isolateConfigStoreLLMEnv(t)

	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema for LLM bootstrap returned error: %v", err)
	}
	config := &appconfig.Config{LLMAPIEndpoint: "https://config.example/v1", LLMAPIProtocol: "chat_completions", LLMAPIKey: "config-key", LLMModel: "config-model"}
	lookup := llms.DefaultLLMEnvProviderLookup(ctx, config, store)
	if endpoint := lookup("LLM_API_ENDPOINT"); endpoint != "https://config.example/v1" {
		t.Fatalf("config LLM endpoint = %q, want %q", endpoint, "https://config.example/v1")
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
	runtimeTarget, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: config, SessionID: "sandbox-1", PreferredProviderFamily: llms.ProviderFamilyOpenAI, RequestedModel: "session-model", ProviderID: "", EnvItems: []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://session.example/v1"},
			{Name: "LLM_API_KEY", Value: "session-key", Secret: true},
			{Name: "LLM_MODEL", Value: "session-model"},
		},
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
	reusedRuntimeTarget, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: config, SessionID: "sandbox-1", PreferredProviderFamily: llms.ProviderFamilyOpenAI, RequestedModel: "session-model", ProviderID: "", EnvItems: nil,
	})
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
	sessionAnthropicID, err := llms.EnsureSessionAnthropicEnvProvider(ctx, anthropicStore, llms.SessionEnvProviderQuery{SessionID: "session-2", RequestedModel: "claude-session", EnvItems: []domain.SandboxEnvVar{
		{Name: "ANTHROPIC_API_KEY", Value: "session-anthropic-key", Secret: true},
		{Name: "ANTHROPIC_MODEL", Value: "claude-session"},
	}})
	if err != nil || sessionAnthropicID == "" {
		t.Fatalf("EnsureSessionAnthropicEnvProvider id=%q err=%v", sessionAnthropicID, err)
	}
}

func isolateConfigStoreLLMEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"LLM_API_ENDPOINT",
		"LLM_API_PROTOCOL",
		"LLM_API_KEY",
		"OPENAI_API_KEY",
		"LLM_MODEL",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_ENDPOINT",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_MODEL",
		"CLAUDE_MODEL",
	} {
		t.Setenv(key, "")
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

}

func assertProjectSchema(t *testing.T, store *ConfigStore) {
	t.Helper()
	for table, columns := range map[string][]string{
		"project":          {"id", "name", "source_path", "source_json", "current_revision", "spec_hash", "created_at", "updated_at", "removed_at"},
		"project_revision": {"project_id", "revision", "spec_hash", "spec_json", "created_at"},
		"project_agent":    {"id", "project_id", "agent_name", "revision", "provider", "model", "image", "driver", "scheduler_enabled", "spec_json", "created_at", "updated_at"},
		"project_scheduler": {"id", "project_id", "scheduler_id", "agent_name", "revision", "enabled", "trigger_count", "spec_json", "last_error",
			"created_at", "updated_at"},
		"project_run": {"run_id", "project_id", "project_name", "project_revision", "agent_name", "agent_id", "source", "scheduler_id", "scheduler_run_id", "trigger_id", "status",
			"sandbox_id", "exit_code", "error", "prompt", "output", "result_json", "logs_path", "artifacts_dir", "cleanup_error", "driver", "image_ref", "started_at",
			"completed_at", "duration_ms", "created_at", "updated_at"},
		"project_run_label": {"run_id", "key", "value"},
		"scheduler_trigger": {"scheduler_id", "trigger_id", "kind", "spec_json"},
		"scheduler_run":     {"scheduler_id", "run_id", "status", "started_at"},
		"scheduler_event":   {"scheduler_id", "scheduler_run_id", "event_id", "type"},
	} {
		assertTableColumns(t, store, table, columns...)
	}
	for _, index := range []string{
		"idx_project_name",
		"idx_project_source_path",
		"idx_project_revision_hash",
		"idx_project_agent_project",
		"idx_project_scheduler_agent",
		"idx_project_run_project_status",
		"idx_project_run_agent",
		"idx_project_run_sandbox",
		"idx_project_run_scheduler",
		"idx_project_run_scheduler_run",
		"idx_scheduler_run_started",
	} {
		assertSQLiteIndexExists(t, store.db, index)
	}
	assertSQLiteIndexUnique(t, store.db, "idx_project_revision_hash", false)
}

func assertTableColumns(t *testing.T, store *ConfigStore, table string, columns ...string) {
	t.Helper()
	columnTypes, err := sqliteTableColumnTypes(context.Background(), store.db, table)
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
	assertTableColumns(t, store, "scheduler_sandbox_binding", "sandbox_id", "sandbox_config_hash")
	assertTableColumns(t, store, "scheduler_event", "linked_sandbox_id", "linked_agent_thread_id")
	assertTableDoesNotExist(t, store, "loader")
	assertTableDoesNotExist(t, store, "loader_binding")
	assertTableDoesNotExist(t, store, "loader_event")
	assertTableColumns(t, store, "event_sandbox_link", "sandbox_id")
	assertTableDoesNotExist(t, store, "event_session_link")
	assertTableColumns(t, store, "llm_facade_token", "sandbox_id")
	assertTableMissingColumns(t, store, "llm_facade_token", "session_id")
	assertSQLiteIndexExists(t, store.db, "idx_llm_facade_token_sandbox")
}

func assertTableMissingColumns(t *testing.T, store *ConfigStore, table string, columns ...string) {
	t.Helper()
	columnTypes, err := sqliteTableColumnTypes(context.Background(), store.db, table)
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
	columnTypes, err := sqliteTableColumnTypes(context.Background(), store.db, table)
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
	var unique int
	if err := db.QueryRowContext(context.Background(), `
		SELECT il."unique"
		FROM sqlite_master AS sm, pragma_index_list(sm.tbl_name) AS il
		WHERE sm.type = 'index' AND sm.name = ? AND il.name = sm.name
	`, indexName).Scan(&unique); err != nil {
		t.Fatalf("query sqlite index %s uniqueness: %v", indexName, err)
	}
	if (unique != 0) != want {
		t.Fatalf("sqlite index %s unique = %v, want %v", indexName, unique != 0, want)
	}
}

func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys%281%29")
	if err != nil {
		t.Fatalf("open SQLite test database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
