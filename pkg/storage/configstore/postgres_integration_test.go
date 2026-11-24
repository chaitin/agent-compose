package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storage/postgres"
)

const postgresStoreTestDSNEnv = "AGENT_COMPOSE_TEST_POSTGRES_DSN"

func TestPostgresConfigStoreIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := openPostgresStore(t, ctx)

	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "p-conc", Name: "concurrent", SourcePath: "/tmp/concurrent", SourceJSON: `{}`})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	agent, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ProjectID: project.ID, AgentName: "worker", Revision: 1, SpecJSON: `{}`})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	run, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-conc", ProjectID: project.ID, AgentName: "worker", AgentID: agent.ID,
		Status: domain.ProjectRunStatusRunning,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	assertConcurrentProjectRevisions(t, ctx, store, project.ID)
	assertConcurrentRunEvents(t, ctx, store, run.RunID)
	assertConcurrentEvents(t, ctx, store)
	assertSchedulerRuns(t, ctx, store, project.ID)

	// Search uses the backend-specific contains expression.
	results, err := store.ListProjects(ctx, domain.ProjectListOptions{Query: "concurr"})
	if err != nil {
		t.Fatalf("search projects: %v", err)
	}
	if results.TotalCount != 1 || len(results.Projects) != 1 || results.Projects[0].ID != project.ID {
		t.Fatalf("search result = %+v, want project %s", results, project.ID)
	}

	// A primary-key collision must surface as ErrAlreadyExists on PostgreSQL.
	volume := domain.VolumeRecord{ID: "vol-1", Name: "shared", Driver: domain.VolumeDriverLocal}
	if _, err := store.CreateVolume(ctx, volume); err != nil {
		t.Fatalf("create volume: %v", err)
	}
	if _, err := store.CreateVolume(ctx, volume); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate volume error = %v, want ErrAlreadyExists", err)
	}
}

func assertConcurrentProjectRevisions(t *testing.T, ctx context.Context, store *ConfigStore, projectID string) {
	t.Helper()
	const workers = 8
	revisions := make([]int64, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			revision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{
				ProjectID: projectID,
				SpecHash:  fmt.Sprintf("hash-%d", index),
				SpecJSON:  `{"agents":[]}`,
			})
			revisions[index] = revision.Revision
			errs[index] = err
		}()
	}
	close(start)
	wg.Wait()
	seen := make(map[int64]bool, workers)
	for index, err := range errs {
		if err != nil {
			t.Fatalf("worker %d revision error: %v", index, err)
		}
		if revisions[index] == 0 || seen[revisions[index]] {
			t.Fatalf("revision %d repeated or zero: %v", revisions[index], revisions)
		}
		seen[revisions[index]] = true
	}
}

func assertConcurrentRunEvents(t *testing.T, ctx context.Context, store *ConfigStore, runID string) {
	t.Helper()
	const workers = 20
	errorsByWorker := make([]error, workers)
	sequenceByWorker := make([]uint64, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			event, created, err := store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{
				ID:    fmt.Sprintf("evt-%02d", index),
				RunID: runID,
				Kind:  domain.ProjectRunEventKindUserMessage,
				Text:  fmt.Sprintf("message %d", index),
			})
			sequenceByWorker[index] = uint64(event.Sequence)
			if err == nil && !created {
				err = fmt.Errorf("event was not created")
			}
			errorsByWorker[index] = err
		}()
	}
	close(start)
	wg.Wait()
	seen := make(map[uint64]bool, workers)
	for index, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("worker %d event error: %v", index, err)
		}
		if sequenceByWorker[index] == 0 || seen[sequenceByWorker[index]] {
			t.Fatalf("sequence %d repeated or zero: %v", sequenceByWorker[index], sequenceByWorker)
		}
		seen[sequenceByWorker[index]] = true
	}
	events, err := store.ListProjectRunEvents(ctx, runID, 0, 100)
	if err != nil {
		t.Fatalf("list run events: %v", err)
	}
	if len(events) != workers {
		t.Fatalf("run event count = %d, want %d", len(events), workers)
	}
}

func assertConcurrentEvents(t *testing.T, ctx context.Context, store *ConfigStore) {
	t.Helper()
	const workers = 20
	errorsByWorker := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.CreateEvent(ctx, domain.TopicEventRecord{
				ID:             fmt.Sprintf("topic-event-%02d", index),
				Topic:          "test.concurrent",
				Source:         domain.TopicEventSourceWebhook,
				CorrelationID:  fmt.Sprintf("corr-%d", index),
				PayloadHash:    "hash",
				PayloadJSON:    `{}`,
				DispatchStatus: domain.TopicEventDispatchPending,
			})
			errorsByWorker[index] = err
		}()
	}
	close(start)
	wg.Wait()
	for index, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("worker %d topic event error: %v", index, err)
		}
	}
}

func openPostgresStore(t *testing.T, ctx context.Context) *ConfigStore {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(postgresStoreTestDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", postgresStoreTestDSNEnv)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("ac_store_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
	})
	db, err := postgres.Open(ctx, postgres.Config{
		URL:            postgresTestSearchPath(dsn, schema),
		MaxOpenConns:   10,
		MaxIdleConns:   5,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open postgres database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return FromDBWithDialect(db.DB(), dialect.Postgres())
}

func postgresTestSearchPath(dsn, schema string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "search_path=" + schema
}

func assertSchedulerRuns(t *testing.T, ctx context.Context, store *ConfigStore, projectID string) {
	t.Helper()
	scheduler, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{
		ProjectID: projectID, SchedulerID: "scheduler-pg", AgentName: "worker",
		Revision: 1, Enabled: true, SpecJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("create scheduler: %v", err)
	}
	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.CreateSchedulerRun(ctx, domain.SchedulerRunSummary{
		ID: "scheduler-run-pg", SchedulerID: scheduler.SchedulerID,
		TriggerID: "trigger-pg", TriggerKind: "cron", Status: domain.SchedulerRunStatusRunning,
		StartedAt: startedAt, PayloadJSON: `{}`,
	}); err != nil {
		t.Fatalf("create scheduler run: %v", err)
	}
	interrupted, err := store.ListInterruptedSchedulerRuns(ctx, startedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("list interrupted scheduler runs: %v", err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != "scheduler-run-pg" {
		t.Fatalf("interrupted scheduler runs = %+v", interrupted)
	}
	completedAt := startedAt.Add(time.Second)
	if err := store.UpdateSchedulerRun(ctx, domain.SchedulerRunSummary{
		ID: "scheduler-run-pg", SchedulerID: scheduler.SchedulerID,
		TriggerID: "trigger-pg", TriggerKind: "cron", Status: domain.SchedulerRunStatusSucceeded,
		StartedAt: startedAt, CompletedAt: completedAt, DurationMs: 1000, ResultJSON: `{"ok":true}`,
	}); err != nil {
		t.Fatalf("update scheduler run: %v", err)
	}
	runs, err := store.ListSchedulerRuns(ctx, scheduler.SchedulerID, 10)
	if err != nil {
		t.Fatalf("list scheduler runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != domain.SchedulerRunStatusSucceeded || runs[0].DurationMs != 1000 {
		t.Fatalf("scheduler runs = %+v", runs)
	}
	if err := store.AddSchedulerEvent(ctx, domain.SchedulerEvent{
		ID: "scheduler-event-pg", SchedulerID: scheduler.SchedulerID, RunID: "scheduler-run-pg",
		Type: "scheduler.run.succeeded", Level: "info", Message: "done", CreatedAt: completedAt,
	}); err != nil {
		t.Fatalf("add scheduler event: %v", err)
	}
}

// TestPostgresEventTraceIntegration pins the recursive descendant query on
// PostgreSQL. SQLite accepts ORDER BY/LIMIT inside a recursive CTE, PostgreSQL
// rejects it, so the walk must be limited by the outer SELECT.
func TestPostgresEventTraceIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := openPostgresStore(t, ctx)

	root, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "event-trace-root", Topic: "trace.test", Source: domain.TopicEventSourceWebhook,
		CorrelationID: "trace-corr", PayloadHash: "hash", PayloadJSON: `{}`,
		DispatchStatus: domain.TopicEventDispatchPending,
	})
	if err != nil {
		t.Fatalf("create root event: %v", err)
	}
	child, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "event-trace-child", Topic: "trace.test", Source: domain.TopicEventSourceWebhook,
		CorrelationID: "trace-corr", PayloadHash: "hash", PayloadJSON: `{}`,
		DispatchStatus: domain.TopicEventDispatchPending, ParentEventID: root.ID,
	})
	if err != nil {
		t.Fatalf("create child event: %v", err)
	}
	grandchild, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "event-trace-grandchild", Topic: "trace.test", Source: domain.TopicEventSourceWebhook,
		CorrelationID: "trace-corr", PayloadHash: "hash", PayloadJSON: `{}`,
		DispatchStatus: domain.TopicEventDispatchPending, ParentEventID: child.ID,
	})
	if err != nil {
		t.Fatalf("create grandchild event: %v", err)
	}

	for index, eventID := range []string{child.ID, grandchild.ID} {
		if err := store.UpsertEventDelivery(ctx, domain.EventDelivery{
			EventID: eventID, SchedulerID: fmt.Sprintf("scheduler-trace-%d", index),
			TriggerID: "trigger-trace", RunID: fmt.Sprintf("run-trace-%d", index),
			Status: domain.EventDeliveryStatusRunSucceeded,
		}); err != nil {
			t.Fatalf("create delivery for %s: %v", eventID, err)
		}
	}

	trace, err := store.GetEventTrace(ctx, root.ID, domain.MaxEventScopeEvents)
	if err != nil {
		t.Fatalf("get event trace: %v", err)
	}
	traced := make(map[string]bool, len(trace.Runs))
	for _, run := range trace.Runs {
		traced[run.Delivery.EventID] = true
	}
	for _, id := range []string{child.ID, grandchild.ID} {
		if !traced[id] {
			t.Fatalf("trace runs %+v do not include descendant %s", trace.Runs, id)
		}
	}
	if trace.DescendantsTruncated {
		t.Fatalf("trace unexpectedly truncated: %+v", trace)
	}
	// ListRuns shares the same event-scope walk as GetEventTrace.
	if _, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{
		ProjectID: "no-such-project", EventID: root.ID, Limit: 10,
	}); err != nil {
		t.Fatalf("list runs by event scope: %v", err)
	}

	truncated, err := store.GetEventTrace(ctx, root.ID, 1)
	if err != nil {
		t.Fatalf("get truncated event trace: %v", err)
	}
	if !truncated.DescendantsTruncated {
		t.Fatalf("trace with limit 1 was not truncated: %+v", truncated)
	}
}
