package configstore

import (
	"context"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// TestLoadLegacySchedulerWithoutPrompt reproduces the upgrade path described in
// the "require a prompt on declarative scheduler triggers" change: a revision
// persisted before prompt became required stores a trigger with no prompt key
// at all (the empty prompt was dropped by omitempty). Loading that scheduler
// must still succeed by restoring the old compiler's "Run agent <name>."
// fallback at the persisted-data boundary, otherwise scheduler startup fails
// and the whole project stops firing.
func TestLoadLegacySchedulerWithoutPrompt(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const legacySpecJSON = `{"name":"legacy","agents":[{"name":"worker","enabled":true,"scheduler":{"enabled":true,"sandbox_policy":"new","concurrency_policy":"skip","triggers":[{"name":"nightly","kind":"cron","cron":"0 * * * *"}]}}]}`
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "legacy", Name: "legacy"}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	revision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{
		ProjectID: "legacy", SpecHash: "legacy-hash", SpecJSON: legacySpecJSON,
	})
	if err != nil {
		t.Fatalf("save legacy revision: %v", err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{
		ID: "legacy-agent", ProjectID: "legacy", AgentName: "worker", Revision: revision.Revision, SpecJSON: "{}",
	}); err != nil {
		t.Fatalf("upsert legacy agent row: %v", err)
	}
	if _, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{
		ID: "legacy-scheduler", ProjectID: "legacy", SchedulerID: "legacy-scheduler", AgentName: "worker",
		Revision: revision.Revision, Enabled: true, TriggerCount: 1, SpecJSON: "{}",
	}); err != nil {
		t.Fatalf("upsert legacy scheduler row: %v", err)
	}

	loaded, err := store.GetScheduler(ctx, "legacy-scheduler")
	if err != nil {
		t.Fatalf("GetScheduler legacy revision returned error: %v", err)
	}
	if !strings.Contains(loaded.Script, "Run agent worker.") {
		t.Fatalf("loaded scheduler script = %q, want the restored fallback prompt", loaded.Script)
	}

	summaries, err := store.ListSchedulerSummaries(ctx)
	if err != nil {
		t.Fatalf("ListSchedulerSummaries returned error: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("ListSchedulerSummaries = %d summaries, want 1", len(summaries))
	}

	schedulers, err := store.ListSchedulers(ctx)
	if err != nil {
		t.Fatalf("ListSchedulers returned error: %v", err)
	}
	if len(schedulers) != 1 {
		t.Fatalf("ListSchedulers = %d schedulers, want 1", len(schedulers))
	}
}

// TestLegacySchedulerReapplyWithPromptUpdatesRevision covers the recovery path:
// after the operator adds a prompt and re-applies, the new revision carries an
// explicit prompt and reconcile must be able to load the old definition (via
// the restored fallback) and then update the scheduler row's revision pointer.
func TestLegacySchedulerReapplyWithPromptUpdatesRevision(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const legacySpecJSON = `{"name":"legacy","agents":[{"name":"worker","enabled":true,"scheduler":{"enabled":true,"sandbox_policy":"new","concurrency_policy":"skip","triggers":[{"name":"nightly","kind":"cron","cron":"0 * * * *"}]}}]}`
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "legacy", Name: "legacy"}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	legacyRevision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{
		ProjectID: "legacy", SpecHash: "legacy-hash", SpecJSON: legacySpecJSON,
	})
	if err != nil {
		t.Fatalf("save legacy revision: %v", err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{
		ID: "legacy-agent", ProjectID: "legacy", AgentName: "worker", Revision: legacyRevision.Revision, SpecJSON: "{}",
	}); err != nil {
		t.Fatalf("upsert legacy agent row: %v", err)
	}
	if _, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{
		ID: "legacy-scheduler", ProjectID: "legacy", SchedulerID: "legacy-scheduler", AgentName: "worker",
		Revision: legacyRevision.Revision, Enabled: true, TriggerCount: 1, SpecJSON: "{}",
	}); err != nil {
		t.Fatalf("upsert legacy scheduler row: %v", err)
	}

	// The old definition must still load before the new revision is reconciled;
	// this is the step that failed before the fallback was restored.
	if _, err := store.GetScheduler(ctx, "legacy-scheduler"); err != nil {
		t.Fatalf("load legacy definition before reapply: %v", err)
	}

	const fixedSpecJSON = `{"name":"legacy","agents":[{"name":"worker","enabled":true,"scheduler":{"enabled":true,"sandbox_policy":"new","concurrency_policy":"skip","triggers":[{"name":"nightly","kind":"cron","cron":"0 * * * *","prompt":"Summarize the day."}]}}]}`
	fixedRevision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{
		ProjectID: "legacy", SpecHash: "fixed-hash", SpecJSON: fixedSpecJSON,
	})
	if err != nil {
		t.Fatalf("save fixed revision: %v", err)
	}
	if _, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{
		ID: "legacy-scheduler", ProjectID: "legacy", SchedulerID: "legacy-scheduler", AgentName: "worker",
		Revision: fixedRevision.Revision, Enabled: true, TriggerCount: 1, SpecJSON: "{}",
	}); err != nil {
		t.Fatalf("reapply scheduler row with fixed revision: %v", err)
	}

	loaded, err := store.GetScheduler(ctx, "legacy-scheduler")
	if err != nil {
		t.Fatalf("GetScheduler fixed revision returned error: %v", err)
	}
	if !strings.Contains(loaded.Script, "Summarize the day.") {
		t.Fatalf("loaded scheduler script = %q, want the declared prompt", loaded.Script)
	}
}
