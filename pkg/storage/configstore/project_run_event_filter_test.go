package configstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// seedEventRunFilterFixture creates a project with runs whose scheduler_run_id
// values are recorded against an event tree: root event "evt-root" with a
// descendant "evt-child" and a correlation sibling "evt-sibling", plus an
// unrelated event "evt-other" and a delivery without a scheduler run.
func seedEventRunFilterFixture(t *testing.T, ctx context.Context, store *ConfigStore, projectID string) {
	t.Helper()
	for _, event := range []domain.TopicEventRecord{
		{ID: "evt-root", Topic: "webhook.github.push", Source: domain.TopicEventSourceWebhook, CorrelationID: "corr-1", PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus},
		{ID: "evt-child", Topic: "runtime.completed", Source: domain.TopicEventSourceScheduler, CorrelationID: "corr-1", ParentEventID: "evt-root", PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus},
		{ID: "evt-sibling", Topic: "runtime.finished", Source: domain.TopicEventSourceScheduler, CorrelationID: "corr-1", PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus},
		{ID: "evt-other", Topic: "webhook.github.push", Source: domain.TopicEventSourceWebhook, CorrelationID: "corr-2", PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus},
		{ID: "evt-no-runs", Topic: "webhook.github.push", Source: domain.TopicEventSourceWebhook, CorrelationID: "corr-3", PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus},
	} {
		if _, err := store.CreateEvent(ctx, event); err != nil {
			t.Fatalf("create event %s: %v", event.ID, err)
		}
	}
	deliveries := []domain.EventDelivery{
		{EventID: "evt-root", SchedulerID: "scheduler-1", TriggerID: "trigger-1", RunID: "sched-run-root", Status: domain.EventDeliveryStatusMatched},
		{EventID: "evt-child", SchedulerID: "scheduler-1", TriggerID: "trigger-1", RunID: "sched-run-child", Status: domain.EventDeliveryStatusMatched},
		{EventID: "evt-sibling", SchedulerID: "scheduler-1", TriggerID: "trigger-1", RunID: "sched-run-sibling", Status: domain.EventDeliveryStatusMatched},
		{EventID: "evt-other", SchedulerID: "scheduler-1", TriggerID: "trigger-1", RunID: "sched-run-other", Status: domain.EventDeliveryStatusMatched},
		{EventID: "evt-no-runs", SchedulerID: "scheduler-1", TriggerID: "trigger-1", Status: domain.EventDeliveryStatusMatched},
	}
	for _, delivery := range deliveries {
		if err := store.UpsertEventDelivery(ctx, delivery); err != nil {
			t.Fatalf("upsert delivery for %s: %v", delivery.EventID, err)
		}
	}
}

func TestListProjectRunsByOptionsFiltersByEventID(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: "/project", SourceJSON: `{}`})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	revision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: "hash", SpecJSON: `{"agents":[]}`})
	if err != nil {
		t.Fatalf("create project revision: %v", err)
	}
	agent, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{
		ProjectID: project.ID, AgentName: "worker", ID: "agent-1", Revision: revision.Revision,
		Provider: "codex", Model: "gpt", Image: "guest:latest", Driver: driverpkg.RuntimeDriverDocker,
		SpecJSON: `{"name":"worker"}`,
	})
	if err != nil {
		t.Fatalf("create project agent: %v", err)
	}
	seedEventRunFilterFixture(t, ctx, store, project.ID)

	startedAt := time.Date(2026, time.September, 21, 10, 0, 0, 0, time.UTC)
	schedulerRuns := map[string]string{
		"run-root":   "sched-run-root",
		"run-child":  "sched-run-child",
		"run-sib":    "sched-run-sibling",
		"run-other":  "sched-run-other",
		"run-manual": "",
	}
	runIDsByScheduler := make(map[string]string, len(schedulerRuns))
	for runID, schedulerRunID := range schedulerRuns {
		if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
			RunID: runID, ProjectID: project.ID, ProjectName: project.Name, ProjectRevision: revision.Revision,
			AgentName: agent.AgentName, AgentID: agent.ID, SchedulerRunID: schedulerRunID, Source: domain.ProjectRunSourceAPI,
			Status: domain.ProjectRunStatusPending, ResultJSON: `{}`, StartedAt: startedAt,
		}); err != nil {
			t.Fatalf("create run %s: %v", runID, err)
		}
		if schedulerRunID != "" {
			runIDsByScheduler[schedulerRunID] = runID
		}
	}

	t.Run("root event resolves descendants and correlation siblings", func(t *testing.T) {
		result, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: project.ID, EventID: "evt-root", Limit: 50})
		if err != nil {
			t.Fatalf("list runs by event: %v", err)
		}
		got := make(map[string]struct{}, len(result.Runs))
		for _, run := range result.Runs {
			got[run.RunID] = struct{}{}
		}
		for _, want := range []string{"run-root", "run-child", "run-sib"} {
			if _, ok := got[want]; !ok {
				t.Fatalf("event scope missing run %s: got %v", want, result.Runs)
			}
		}
		for _, unwanted := range []string{"run-other", "run-manual"} {
			if _, ok := got[unwanted]; ok {
				t.Fatalf("event scope includes unrelated run %s", unwanted)
			}
		}
		total, _, err := store.CountProjectRuns(ctx, ProjectRunListOptions{ProjectID: project.ID, EventID: "evt-root"})
		if err != nil {
			t.Fatalf("count runs by event: %v", err)
		}
		if total != 3 {
			t.Fatalf("count by event = %d, want 3", total)
		}
	})

	t.Run("descendant event resolves the whole correlation scope", func(t *testing.T) {
		// evt-child shares the root's correlation id, so its scope includes the
		// root's and the sibling's deliveries, mirroring GetEventTrace.
		result, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: project.ID, EventID: "evt-child", Limit: 50})
		if err != nil {
			t.Fatalf("list runs by descendant event: %v", err)
		}
		got := make(map[string]struct{}, len(result.Runs))
		for _, run := range result.Runs {
			got[run.RunID] = struct{}{}
		}
		for _, want := range []string{"run-root", "run-child", "run-sib"} {
			if _, ok := got[want]; !ok {
				t.Fatalf("descendant event scope missing run %s: got %v", want, result.Runs)
			}
		}
		if _, ok := got["run-other"]; ok {
			t.Fatalf("descendant event scope includes unrelated run run-other")
		}
	})

	t.Run("event without scheduler runs filters to empty result", func(t *testing.T) {
		result, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: project.ID, EventID: "evt-no-runs", Limit: 50})
		if err != nil {
			t.Fatalf("list runs for event without deliveries: %v", err)
		}
		if len(result.Runs) != 0 {
			t.Fatalf("expected no runs, got %v", result.Runs)
		}
		total, _, err := store.CountProjectRuns(ctx, ProjectRunListOptions{ProjectID: project.ID, EventID: "evt-no-runs"})
		if err != nil {
			t.Fatalf("count runs for event without deliveries: %v", err)
		}
		if total != 0 {
			t.Fatalf("expected zero total, got %d", total)
		}
	})

	t.Run("unknown event returns not found", func(t *testing.T) {
		_, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: project.ID, EventID: "evt-missing", Limit: 50})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown event error = %v, want NotFound", err)
		}
		if _, _, err := store.CountProjectRuns(ctx, ProjectRunListOptions{ProjectID: project.ID, EventID: "evt-missing"}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown event count error = %v, want NotFound", err)
		}
	})

	t.Run("empty event id applies no filter", func(t *testing.T) {
		result, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: project.ID, Limit: 50})
		if err != nil {
			t.Fatalf("list runs without event filter: %v", err)
		}
		if len(result.Runs) != len(schedulerRuns) {
			t.Fatalf("unfiltered runs = %d, want %d", len(result.Runs), len(schedulerRuns))
		}
	})

	t.Run("event filter composes with agent filter", func(t *testing.T) {
		result, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: project.ID, EventID: "evt-root", AgentName: "nobody", Limit: 50})
		if err != nil {
			t.Fatalf("list runs with agent filter: %v", err)
		}
		if len(result.Runs) != 0 {
			t.Fatalf("expected agent filter to exclude event runs, got %v", result.Runs)
		}
	})
}

// TestListProjectRunsByOptionsEventScopeTruncation seeds more events than the
// scope cap and asserts the list and count both report the truncated flag.
func TestListProjectRunsByOptionsEventScopeTruncation(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	root, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "evt-overflow-root", Topic: "webhook.github.push", Source: domain.TopicEventSourceWebhook,
		CorrelationID: "corr-overflow", PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus,
	})
	if err != nil {
		t.Fatalf("create root event: %v", err)
	}
	// MaxEventScopeEvents plus one descendant forces the descendant walk to
	// truncate: root + 1000 children fit, the last child falls outside.
	for i := 0; i <= domain.MaxEventScopeEvents; i++ {
		if _, err := store.CreateEvent(ctx, domain.TopicEventRecord{
			ID: fmt.Sprintf("evt-overflow-child-%04d", i), Topic: "runtime.completed", Source: domain.TopicEventSourceScheduler,
			CorrelationID: root.CorrelationID, ParentEventID: root.ID,
			PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus,
		}); err != nil {
			t.Fatalf("create overflow child %d: %v", i, err)
		}
	}
	if err := store.UpsertEventDelivery(ctx, domain.EventDelivery{
		EventID: root.ID, SchedulerID: "scheduler-overflow", TriggerID: "trigger-overflow", RunID: "sched-run-overflow", Status: domain.EventDeliveryStatusMatched,
	}); err != nil {
		t.Fatalf("upsert delivery: %v", err)
	}
	if err := store.UpsertEventDelivery(ctx, domain.EventDelivery{
		EventID: "evt-overflow-child-1000", SchedulerID: "scheduler-overflow", TriggerID: "trigger-overflow", RunID: "sched-run-overflow-last", Status: domain.EventDeliveryStatusMatched,
	}); err != nil {
		t.Fatalf("upsert last-child delivery: %v", err)
	}

	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-overflow", Name: "project-overflow", SourcePath: "/project-overflow", SourceJSON: `{}`})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	revision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: "hash-overflow", SpecJSON: `{"agents":[]}`})
	if err != nil {
		t.Fatalf("create project revision: %v", err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{
		ProjectID: project.ID, AgentName: "worker", ID: "agent-overflow", Revision: revision.Revision,
		Provider: "codex", Model: "gpt", Image: "guest:latest", Driver: driverpkg.RuntimeDriverDocker,
		SpecJSON: `{"name":"worker"}`,
	}); err != nil {
		t.Fatalf("create project agent: %v", err)
	}

	_, err = store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-overflow", ProjectID: project.ID, ProjectName: project.Name, ProjectRevision: revision.Revision,
		AgentName: "worker", AgentID: "agent-overflow", SchedulerRunID: "sched-run-overflow",
		Source: domain.ProjectRunSourceAPI, Status: domain.ProjectRunStatusPending, ResultJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("create in-scope run: %v", err)
	}
	_, err = store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-overflow-last", ProjectID: project.ID, ProjectName: project.Name, ProjectRevision: revision.Revision,
		AgentName: "worker", AgentID: "agent-overflow", SchedulerRunID: "sched-run-overflow-last",
		Source: domain.ProjectRunSourceAPI, Status: domain.ProjectRunStatusPending, ResultJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("create out-of-scope run: %v", err)
	}

	runs, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{EventID: root.ID, Limit: 50})
	if err != nil {
		t.Fatalf("list runs by overflow event: %v", err)
	}
	if len(runs.Runs) != 1 || runs.Runs[0].SchedulerRunID != "sched-run-overflow" {
		t.Fatalf("truncated list = %#v, want only sched-run-overflow", runs.Runs)
	}
	if !runs.EventScopeTruncated {
		t.Fatalf("list event_scope_truncated = false, want true")
	}
	total, truncated, err := store.CountProjectRuns(ctx, ProjectRunListOptions{EventID: root.ID})
	if err != nil {
		t.Fatalf("count runs by overflow event: %v", err)
	}
	if total != 1 || !truncated {
		t.Fatalf("count = %d truncated=%v, want 1 true", total, truncated)
	}
}
