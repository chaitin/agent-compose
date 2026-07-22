package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	"agent-compose/pkg/identity"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
)

type schedulerPruneE2EFixture struct {
	composePath string
	host        string
	store       *configstore.ConfigStore
	scheduler   domain.ProjectSchedulerRecord
	triggerID   string
	runID       string
	eventID     string
	artifactDir string
	artifactLen uint64
}

func TestE2ECLISchedulerPruneLifecycleWithInProcessDaemon(t *testing.T) {
	fixture := newSchedulerPruneE2EFixture(t)
	fixture.assertHistoryVisible(t)

	dryRun := fixture.prune(t, false)
	if !dryRun.DryRun || dryRun.Matched != fixture.wantStats() || dryRun.Removed != (composeSchedulerPruneStats{}) {
		t.Fatalf("dry-run output=%#v, want matched %#v and no removals", dryRun, fixture.wantStats())
	}
	fixture.assertPersisted(t)

	removed := fixture.prune(t, true)
	if removed.DryRun || removed.Matched != fixture.wantStats() || removed.Removed != fixture.wantStats() || removed.SkippedRuns != 0 || len(removed.Residues) != 0 {
		t.Fatalf("forced prune output=%#v, want matched and removed %#v", removed, fixture.wantStats())
	}
	fixture.assertHistoryRemoved(t)
}

func newSchedulerPruneE2EFixture(t *testing.T) schedulerPruneE2EFixture {
	t.Helper()
	useTestDockerImage(t, "guest:v1")
	composePath := writeComposeFile(t, t.TempDir(), `
name: scheduler-prune-test
agents:
  reviewer:
    provider: codex
    image: guest:v1
    driver:
      docker: {}
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
          prompt: review nightly
`)
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", nil)
	t.Cleanup(cancel)
	server := httptestServer(t, app)

	stdout, stderr, _, exitCode := executeCLICommand("up", "--host", server.URL, "--file", composePath, "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("up code/stdout/stderr=%d/%q/%q", exitCode, stdout, stderr)
	}

	ctx := context.Background()
	store := do.MustInvoke[*configstore.ConfigStore](app.DI)
	projects, err := store.ListProjects(ctx, domain.ProjectListOptions{Query: "scheduler-prune-test", Limit: 10})
	if err != nil || len(projects.Projects) != 1 {
		t.Fatalf("list project result=%#v err=%v", projects, err)
	}
	project := projects.Projects[0]
	schedulers, err := store.ListProjectSchedulers(ctx, project.ID)
	if err != nil || len(schedulers) != 1 || schedulers[0].ManagedLoaderID == "" {
		t.Fatalf("list project schedulers=%#v err=%v", schedulers, err)
	}
	triggerID, err := domain.StableManagedTriggerID(project.ID, "reviewer", "", "nightly", 0)
	if err != nil {
		t.Fatalf("stable trigger id: %v", err)
	}
	fixture := schedulerPruneE2EFixture{
		composePath: composePath,
		host:        server.URL,
		store:       store,
		scheduler:   schedulers[0],
		triggerID:   triggerID,
		runID:       identity.NewRandomID(identity.ResourceRun),
		eventID:     "evt_scheduler_prune_e2e",
	}
	fixture.seedHistory(t, app.Config.DataRoot)
	return fixture
}

func httptestServer(t *testing.T, app *DaemonApp) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(app.Echo)
	t.Cleanup(server.Close)
	return server
}

func (f *schedulerPruneE2EFixture) seedHistory(t *testing.T, dataRoot string) {
	t.Helper()
	ctx := context.Background()
	completedAt := time.Now().UTC().Add(-time.Hour)
	f.artifactDir = filepath.Join(dataRoot, "loaders", f.scheduler.ManagedLoaderID, "runs", f.runID)
	if err := f.store.CreateLoaderRun(ctx, domain.LoaderRunSummary{
		ID: f.runID, LoaderID: f.scheduler.ManagedLoaderID, TriggerID: f.triggerID,
		TriggerKind: domain.LoaderTriggerKindCron, TriggerSource: "manual", Status: domain.LoaderRunStatusSucceeded,
		StartedAt: completedAt.Add(-time.Minute), CompletedAt: completedAt, DurationMs: 60_000,
		ResultJSON: `{"ok":true}`, PayloadJSON: `{"source":"e2e"}`, ArtifactsDir: f.artifactDir,
	}); err != nil {
		t.Fatalf("create loader run: %v", err)
	}
	if err := f.store.AddLoaderEvent(ctx, domain.LoaderEvent{
		ID: "loader-event-scheduler-prune-e2e", LoaderID: f.scheduler.ManagedLoaderID, RunID: f.runID, TriggerID: f.triggerID,
		Type: "loader.run.completed", Level: "info", Message: "scheduler prune e2e completed", CreatedAt: completedAt,
	}); err != nil {
		t.Fatalf("add loader event: %v", err)
	}
	if _, err := f.store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: f.eventID, Topic: "scheduler.prune.e2e", Source: domain.TopicEventSourceSystem,
		DispatchStatus: domain.TopicEventDispatchPublishedToBus, PayloadJSON: `{}`, CreatedAt: completedAt,
	}); err != nil {
		t.Fatalf("create topic event: %v", err)
	}
	if err := f.store.UpsertEventDelivery(ctx, domain.EventDelivery{
		EventID: f.eventID, LoaderID: f.scheduler.ManagedLoaderID, TriggerID: f.triggerID, RunID: f.runID,
		Status: domain.EventDeliveryStatusRunSucceeded, CreatedAt: completedAt, UpdatedAt: completedAt,
	}); err != nil {
		t.Fatalf("add event delivery: %v", err)
	}
	if err := f.store.AddEventSandboxLink(ctx, domain.EventSandboxLink{
		EventID: f.eventID, SandboxID: identity.NewRandomID(identity.ResourceSandbox), Relation: "used",
		LoaderID: f.scheduler.ManagedLoaderID, RunID: f.runID, TriggerID: f.triggerID, CreatedAt: completedAt,
	}); err != nil {
		t.Fatalf("add event sandbox link: %v", err)
	}
	artifact := []byte("{\"ok\":true}\n")
	if err := os.MkdirAll(f.artifactDir, 0o700); err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.artifactDir, "payload.json"), artifact, 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	f.artifactLen = uint64(len(artifact))
}

func (f schedulerPruneE2EFixture) prune(t *testing.T, force bool) composeSchedulerPruneOutput {
	t.Helper()
	args := []string{"scheduler", "prune", "--scheduler", "reviewer", "--trigger", "nightly", "--json", "--host", f.host, "--file", f.composePath}
	if force {
		args = append(args, "--force")
	}
	stdout, stderr, _, exitCode := executeCLICommand(args...)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("scheduler prune force=%v code/stdout/stderr=%d/%q/%q", force, exitCode, stdout, stderr)
	}
	var output composeSchedulerPruneOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode scheduler prune output: %v\n%s", err, stdout)
	}
	return output
}

func (f schedulerPruneE2EFixture) wantStats() composeSchedulerPruneStats {
	return composeSchedulerPruneStats{
		Runs: 1, LoaderEvents: 1, EventDeliveries: 1, EventSandboxLinks: 1,
		ArtifactDirs: 1, ArtifactBytes: f.artifactLen,
	}
}

func (f schedulerPruneE2EFixture) assertHistoryVisible(t *testing.T) {
	t.Helper()
	runs := f.schedulerRuns(t)
	if len(runs.Runs) != 1 || runs.Runs[0].RunID != f.runID || runs.Runs[0].TriggerID != f.triggerID || runs.Runs[0].Status != domain.LoaderRunStatusSucceeded {
		t.Fatalf("scheduler runs=%#v", runs.Runs)
	}
	logs := f.schedulerLogs(t)
	if len(logs.Events) != 1 || logs.Events[0].RunID != f.runID || logs.Events[0].Message != "scheduler prune e2e completed" {
		t.Fatalf("scheduler logs=%#v", logs.Events)
	}
	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "inspect", f.runID, "--json", "--host", f.host, "--file", f.composePath)
	var inspected composeSchedulerInspectOutput
	if exitCode != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &inspected) != nil || inspected.Resource != "run" || inspected.Run == nil || inspected.Run.RunID != f.runID {
		t.Fatalf("scheduler inspect code/stdout/stderr=%d/%q/%q", exitCode, stdout, stderr)
	}
}

func (f schedulerPruneE2EFixture) assertPersisted(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.store.GetLoaderRun(ctx, f.scheduler.ManagedLoaderID, f.runID); err != nil {
		t.Fatalf("loader run missing after dry-run: %v", err)
	}
	if _, err := os.Stat(f.artifactDir); err != nil {
		t.Fatalf("artifact missing after dry-run: %v", err)
	}
	f.assertRelationCounts(t, 1)
}

func (f schedulerPruneE2EFixture) assertHistoryRemoved(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.store.GetLoaderRun(ctx, f.scheduler.ManagedLoaderID, f.runID); err == nil {
		t.Fatal("loader run still exists after forced prune")
	}
	if _, err := os.Stat(f.artifactDir); !os.IsNotExist(err) {
		t.Fatalf("artifact dir stat after forced prune=%v, want not exist", err)
	}
	f.assertRelationCounts(t, 0)
	if _, err := f.store.GetEvent(ctx, f.eventID); err != nil {
		t.Fatalf("topic event was removed with scheduler run history: %v", err)
	}
	if runs := f.schedulerRuns(t); len(runs.Runs) != 0 {
		t.Fatalf("scheduler runs after prune=%#v, want empty", runs.Runs)
	}
	if logs := f.schedulerLogs(t); strings.Contains(mustJSON(t, logs), f.runID) {
		t.Fatalf("scheduler logs still contain pruned run %s: %#v", f.runID, logs.Events)
	}
	for _, args := range [][]string{{"scheduler", "logs", f.runID}, {"scheduler", "inspect", f.runID}} {
		stdout, stderr, _, exitCode := executeCLICommand(append(args, "--host", f.host, "--file", f.composePath)...)
		if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "not found") {
			t.Fatalf("pruned history command %v code/stdout/stderr=%d/%q/%q", args, exitCode, stdout, stderr)
		}
	}
}

func (f schedulerPruneE2EFixture) assertRelationCounts(t *testing.T, want int) {
	t.Helper()
	ctx := context.Background()
	events, err := f.store.ListLoaderEventsPage(ctx, loaders.LoaderEventPageFilter{
		LoaderIDs: []string{f.scheduler.ManagedLoaderID}, RunID: f.runID, Limit: 10,
	})
	if err != nil || len(events) != want {
		t.Fatalf("loader events=%#v err=%v, want %d", events, err, want)
	}
	deliveries, err := f.store.ListEventDeliveries(ctx, []string{f.eventID})
	if err != nil || len(deliveries) != want {
		t.Fatalf("event deliveries=%#v err=%v, want %d", deliveries, err, want)
	}
	links, err := f.store.ListEventSandboxLinks(ctx, []string{f.eventID})
	if err != nil || len(links) != want {
		t.Fatalf("event sandbox links=%#v err=%v, want %d", links, err, want)
	}
}

func (f schedulerPruneE2EFixture) schedulerRuns(t *testing.T) composeSchedulerRunsOutput {
	t.Helper()
	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "runs", "reviewer", "--json", "--host", f.host, "--file", f.composePath)
	var output composeSchedulerRunsOutput
	if exitCode != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &output) != nil {
		t.Fatalf("scheduler runs code/stdout/stderr=%d/%q/%q", exitCode, stdout, stderr)
	}
	return output
}

func (f schedulerPruneE2EFixture) schedulerLogs(t *testing.T) composeSchedulerLogsOutput {
	t.Helper()
	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "logs", "--scheduler", "reviewer", "--trigger", "nightly", "--json", "--host", f.host, "--file", f.composePath)
	var output composeSchedulerLogsOutput
	if exitCode != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &output) != nil {
		t.Fatalf("scheduler logs code/stdout/stderr=%d/%q/%q", exitCode, stdout, stderr)
	}
	return output
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test value: %v", err)
	}
	return string(data)
}
