package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestProjectRunEventsAreOrderedIdempotentAndCascade(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	columns, err := sqliteTableColumnTypes(ctx, store.db, "project_run_event")
	if err != nil {
		t.Fatalf("inspect run event schema: %v", err)
	}
	if _, exists := columns["idempotency_key"]; exists {
		t.Fatalf("project_run_event still contains idempotency_key: %#v", columns)
	}
	runColumns, err := sqliteTableColumnTypes(ctx, store.db, "project_run")
	if err != nil {
		t.Fatalf("inspect project run schema: %v", err)
	}
	if _, exists := runColumns["history_available"]; exists {
		t.Fatalf("project_run still contains history_available: %#v", runColumns)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-events", Name: "events"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	createdRun, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{RunID: "run-events", ProjectID: project.ID, SandboxID: "sandbox-events", Status: domain.ProjectRunStatusRunning})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if available, err := store.HasProjectRunEvents(ctx, createdRun.RunID); err != nil || available {
		t.Fatalf("new run history availability = %v, err = %v; want false", available, err)
	}
	first, created, err := store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{ID: "event-prompt", RunID: "run-events", Kind: domain.ProjectRunEventKindUserMessage, Text: "hello"})
	if err != nil || !created || first.Sequence != 1 {
		t.Fatalf("append first=%#v created=%v err=%v", first, created, err)
	}
	if available, err := store.HasProjectRunEvents(ctx, createdRun.RunID); err != nil || !available {
		t.Fatalf("run history availability = %v, err = %v; want true", available, err)
	}
	repeated, created, err := store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{ID: "event-prompt", RunID: "run-events", Kind: domain.ProjectRunEventKindUserMessage, Text: "changed"})
	if err != nil || created || repeated.ID != first.ID || repeated.Text != "hello" {
		t.Fatalf("repeat=%#v created=%v err=%v", repeated, created, err)
	}
	if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{RunID: "run-other-events", ProjectID: project.ID, Status: domain.ProjectRunStatusRunning}); err != nil {
		t.Fatalf("create other run: %v", err)
	}
	if _, _, err := store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{ID: first.ID, RunID: "run-other-events", Kind: domain.ProjectRunEventKindUserMessage, Text: "other"}); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("cross-run event id conflict = %v, want original unique constraint", err)
	}
	second, created, err := store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{ID: "event-terminal", RunID: "run-events", Kind: domain.ProjectRunEventKindStatus})
	if err != nil || !created || second.Sequence != 2 {
		t.Fatalf("append second=%#v created=%v err=%v", second, created, err)
	}
	events, err := store.ListProjectRunEvents(ctx, "run-events", 1, 10)
	if err != nil || len(events) != 1 || events[0].Sequence != 2 {
		t.Fatalf("list=%#v err=%v", events, err)
	}
	sandboxEvents, err := store.ListProjectRunEventsForSandbox(ctx, "sandbox-events", time.Time{}, "", 0, 1)
	if err != nil || len(sandboxEvents) != 1 || sandboxEvents[0].ID != first.ID {
		t.Fatalf("list sandbox first page=%#v err=%v", sandboxEvents, err)
	}
	runIDs, err := store.ListProjectRunEventRunIDsForSandbox(ctx, "sandbox-events")
	if err != nil || len(runIDs) != 1 || runIDs[0] != createdRun.RunID {
		t.Fatalf("sandbox history run ids = %#v, err = %v", runIDs, err)
	}
	sandboxEvents, err = store.ListProjectRunEventsForSandbox(ctx, "sandbox-events", first.CreatedAt, first.RunID, first.Sequence, 10)
	if err != nil || len(sandboxEvents) != 1 || sandboxEvents[0].ID != second.ID {
		t.Fatalf("list sandbox second page=%#v err=%v", sandboxEvents, err)
	}
	if _, _, err := store.AppendProjectRunEvents(ctx, []domain.ProjectRunEventRecord{{ID: "event-rolled-back", RunID: "run-events", Kind: domain.ProjectRunEventKindAgentMessage}, {RunID: "run-events", Kind: domain.ProjectRunEventKindStatus}}); err == nil {
		t.Fatal("invalid atomic batch returned nil error")
	}
	events, err = store.ListProjectRunEvents(ctx, "run-events", 2, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("atomic batch was not rolled back: %#v err=%v", events, err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE UNIQUE INDEX test_project_run_event_kind ON project_run_event(run_id, kind)`); err != nil {
		t.Fatalf("create constraint test index: %v", err)
	}
	if _, _, err := store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{ID: "event-different-status", RunID: "run-events", Kind: domain.ProjectRunEventKindStatus}); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("non-idempotency constraint error = %v, want original unique constraint", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM project_run WHERE run_id = ?`, "run-events"); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	events, err = store.ListProjectRunEvents(ctx, "run-events", 0, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("events survived run delete: %#v err=%v", events, err)
	}
}

func TestProjectRunAndEventsCommitAtomically(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-atomic", Name: "atomic"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_run_event BEFORE INSERT ON project_run_event BEGIN SELECT RAISE(ABORT, 'forced event failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	run := domain.ProjectRunRecord{RunID: "run-atomic", ProjectID: "project-atomic", Status: domain.ProjectRunStatusRunning}
	event := domain.ProjectRunEventRecord{ID: "event-initial-prompt", RunID: run.RunID, Kind: domain.ProjectRunEventKindUserMessage, Text: "hello"}
	if _, err := store.CreateProjectRunWithEvents(ctx, run, []domain.ProjectRunEventRecord{event}); err == nil {
		t.Fatal("create with failing event returned nil error")
	}
	if _, err := store.GetProjectRun(ctx, run.RunID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("run survived failed event insert: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_run_event`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	created, err := store.CreateProjectRunWithEvents(ctx, run, nil)
	if err != nil {
		t.Fatalf("create without event: %v", err)
	}
	if _, err := store.CreateProjectRunWithEvents(ctx, run, []domain.ProjectRunEventRecord{event}); err != nil {
		t.Fatalf("retry did not backfill event: %v", err)
	}
	events, err := store.ListProjectRunEvents(ctx, run.RunID, 0, 10)
	if err != nil || len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("backfilled events = %#v, err = %v", events, err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_run_event BEFORE INSERT ON project_run_event BEGIN SELECT RAISE(ABORT, 'forced event failure'); END`); err != nil {
		t.Fatalf("recreate failure trigger: %v", err)
	}
	terminal := created
	terminal.Status = domain.ProjectRunStatusSucceeded
	terminal.Output = "done"
	terminalEvent := domain.ProjectRunEventRecord{ID: "event-terminal-status", RunID: run.RunID, Kind: domain.ProjectRunEventKindStatus}
	if _, err := store.UpdateProjectRunWithEvents(ctx, terminal, []domain.ProjectRunEventRecord{terminalEvent}); err == nil {
		t.Fatal("update with failing event returned nil error")
	}
	unchanged, err := store.GetProjectRun(ctx, run.RunID)
	if err != nil || unchanged.Status != domain.ProjectRunStatusRunning || unchanged.Output != "" {
		t.Fatalf("run update was not rolled back: %#v, err = %v", unchanged, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_run_event`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	updated, err := store.UpdateProjectRunWithEvents(ctx, terminal, []domain.ProjectRunEventRecord{terminalEvent})
	if err != nil || updated.Status != domain.ProjectRunStatusSucceeded {
		t.Fatalf("atomic terminal update = %#v, err = %v", updated, err)
	}
	events, err = store.ListProjectRunEvents(ctx, run.RunID, 0, 10)
	if err != nil || len(events) != 2 || events[1].ID != terminalEvent.ID {
		t.Fatalf("committed events = %#v, err = %v", events, err)
	}
}

func TestCreateProjectRunWithEventsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-idempotent", Name: "idempotent"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	run := domain.ProjectRunRecord{
		RunID:     "run-idempotent",
		ProjectID: "project-idempotent",
		AgentName: "worker",
		Prompt:    "hello",
		Status:    domain.ProjectRunStatusRunning,
	}
	events := []domain.ProjectRunEventRecord{{
		ID:    "event-idempotent-initial-prompt",
		RunID: run.RunID,
		Kind:  domain.ProjectRunEventKindUserMessage,
		Text:  run.Prompt,
		Agent: run.AgentName,
	}}
	first, err := store.CreateProjectRunWithEvents(ctx, run, events)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := store.CreateProjectRunWithEvents(ctx, run, events)
	if err != nil {
		t.Fatalf("idempotent create: %v", err)
	}
	if second.RunID != first.RunID || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("idempotent create returned a different run: first=%#v second=%#v", first, second)
	}
	storedEvents, err := store.ListProjectRunEvents(ctx, run.RunID, 0, 10)
	if err != nil {
		t.Fatalf("list run events: %v", err)
	}
	if len(storedEvents) != 1 || storedEvents[0].ID != events[0].ID || storedEvents[0].Text != run.Prompt {
		t.Fatalf("events after idempotent create = %#v", storedEvents)
	}
}

func TestProjectRunEventSequencesAreAtomicAcrossConnections(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "run-events.db") + "?_pragma=busy_timeout%285000%29"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	store := FromDB(db)
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-concurrent-events", Name: "events"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{RunID: "run-concurrent-events", ProjectID: project.ID, Status: domain.ProjectRunStatusRunning}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	const count = 24
	sequences := make(chan uint64, count)
	errorsCh := make(chan error, count)
	var wg sync.WaitGroup
	for index := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event, created, appendErr := store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{
				ID: fmt.Sprintf("event-%02d", index), RunID: "run-concurrent-events", Kind: domain.ProjectRunEventKindStatus,
			})
			if appendErr != nil {
				errorsCh <- appendErr
				return
			}
			if !created {
				errorsCh <- fmt.Errorf("event-%02d was not created", index)
				return
			}
			sequences <- event.Sequence
		}()
	}
	wg.Wait()
	close(errorsCh)
	close(sequences)
	for appendErr := range errorsCh {
		t.Errorf("concurrent append: %v", appendErr)
	}
	ordered := make([]int, 0, count)
	for sequence := range sequences {
		ordered = append(ordered, int(sequence))
	}
	sort.Ints(ordered)
	if len(ordered) != count {
		t.Fatalf("sequence count = %d, want %d", len(ordered), count)
	}
	for index, sequence := range ordered {
		if sequence != index+1 {
			t.Fatalf("sequences = %v, want contiguous 1..%d", ordered, count)
		}
	}
}
