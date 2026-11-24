package pgcopy

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storage/postgres"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

const copyTestDSNEnv = "AGENT_COMPOSE_TEST_POSTGRES_DSN"

func TestPostgresCopyIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(copyTestDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", copyTestDSNEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	sourcePath := seedSQLiteSource(t, ctx)
	target := openCopyTarget(t, ctx, dsn)

	result, err := Copy(ctx, sourcePath, target.DB(), Options{})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if result.EventSequence != 1 {
		t.Fatalf("event sequence = %d, want 1", result.EventSequence)
	}
	counts := make(map[string]int64, len(result.Tables))
	for _, table := range result.Tables {
		counts[table.Table] = table.Rows
	}
	for table, want := range map[string]int64{"project": 1, "project_agent": 1, "project_run": 1, "project_run_event": 2, "event": 1, "volumes": 1} {
		if counts[table] != want {
			t.Fatalf("table %s copied %d rows, want %d (%v)", table, counts[table], want, counts)
		}
	}

	store := configstore.FromDBWithDialect(target.DB(), dialect.Postgres())
	project, err := store.GetProject(ctx, "project-copy")
	if err != nil {
		t.Fatalf("read copied project: %v", err)
	}
	if project.Name != "copy-project" {
		t.Fatalf("project name = %q", project.Name)
	}
	events, err := store.ListProjectRunEvents(ctx, "run-copy", 0, 10)
	if err != nil {
		t.Fatalf("read copied run events: %v", err)
	}
	if len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 || events[0].Text != "hello" {
		t.Fatalf("copied run events = %+v", events)
	}
	event, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "event-next", Topic: "copy.test", Source: domain.TopicEventSourceWebhook,
		CorrelationID: "corr-next", PayloadHash: "hash", PayloadJSON: `{}`,
		DispatchStatus: domain.TopicEventDispatchPending,
	})
	if err != nil {
		t.Fatalf("create event after copy: %v", err)
	}
	if event.Sequence != 2 {
		t.Fatalf("new event sequence = %d, want 2", event.Sequence)
	}

	if _, err := Copy(ctx, sourcePath, target.DB(), Options{}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("second copy error = %v, want non-empty target error", err)
	}
}

func seedSQLiteSource(t *testing.T, ctx context.Context) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := storagesqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	store := configstore.FromDB(db.DB())
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-copy", Name: "copy-project", SourcePath: "/tmp/copy", SourceJSON: `{}`})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	agent, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ProjectID: project.ID, AgentName: "worker", Revision: 1, SpecJSON: `{}`})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	run, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-copy", ProjectID: project.ID, AgentName: "worker", AgentID: agent.ID,
		Status: domain.ProjectRunStatusSucceeded,
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	for index, text := range []string{"hello", "done"} {
		kind := domain.ProjectRunEventKindUserMessage
		if index == 1 {
			kind = domain.ProjectRunEventKindStatus
		}
		if _, _, err := store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{
			ID: fmt.Sprintf("run-event-%d", index), RunID: run.RunID, Kind: kind, Text: text,
		}); err != nil {
			t.Fatalf("seed run event: %v", err)
		}
	}
	if _, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "event-copy", Topic: "copy.test", Source: domain.TopicEventSourceWebhook,
		CorrelationID: "corr-copy", PayloadHash: "hash", PayloadJSON: `{}`,
		DispatchStatus: domain.TopicEventDispatchPending,
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := store.CreateVolume(ctx, domain.VolumeRecord{ID: "volume-copy", Name: "copy-volume", Driver: domain.VolumeDriverLocal}); err != nil {
		t.Fatalf("seed volume: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}
	return path
}

func openCopyTarget(t *testing.T, ctx context.Context, dsn string) *postgres.Database {
	t.Helper()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("ac_copy_%d", time.Now().UnixNano())
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
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	target, err := postgres.Open(ctx, postgres.Config{
		URL:            dsn + separator + "search_path=" + schema,
		MaxOpenConns:   6,
		MaxIdleConns:   3,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open target database: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })
	return target
}

// TestPostgresCopyPreservesDeletedEventSequenceIntegration covers SQLite's
// AUTOINCREMENT high-water mark: deleting the newest events must not let the
// PostgreSQL copy reissue sequence values clients have already seen.
func TestPostgresCopyPreservesDeletedEventSequenceIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(copyTestDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", copyTestDSNEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	for _, test := range []struct {
		name      string
		deleteAll bool
		wantSeq   int64
	}{
		{name: "trailing events deleted", wantSeq: 3},
		{name: "all events deleted", deleteAll: true, wantSeq: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			sourcePath := seedSQLiteSourceWithDeletedEvents(t, ctx, test.deleteAll)
			target := openCopyTarget(t, ctx, dsn)
			result, err := Copy(ctx, sourcePath, target.DB(), Options{})
			if err != nil {
				t.Fatalf("copy: %v", err)
			}
			if result.EventSequence != test.wantSeq {
				t.Fatalf("event sequence = %d, want %d", result.EventSequence, test.wantSeq)
			}
			store := configstore.FromDBWithDialect(target.DB(), dialect.Postgres())
			event, err := store.CreateEvent(ctx, domain.TopicEventRecord{
				ID: "event-after-copy", Topic: "copy.test", Source: domain.TopicEventSourceWebhook,
				CorrelationID: "corr-after-copy", PayloadHash: "hash", PayloadJSON: `{}`,
				DispatchStatus: domain.TopicEventDispatchPending,
			})
			if err != nil {
				t.Fatalf("create event after copy: %v", err)
			}
			if event.Sequence != test.wantSeq+1 {
				t.Fatalf("next event sequence = %d, want %d", event.Sequence, test.wantSeq+1)
			}
		})
	}
}

func seedSQLiteSourceWithDeletedEvents(t *testing.T, ctx context.Context, deleteAll bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := storagesqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	store := configstore.FromDB(db.DB())
	for index := 1; index <= 3; index++ {
		if _, err := store.CreateEvent(ctx, domain.TopicEventRecord{
			ID: fmt.Sprintf("deleted-event-%d", index), Topic: "copy.test",
			Source: domain.TopicEventSourceWebhook, CorrelationID: fmt.Sprintf("corr-%d", index),
			PayloadHash: "hash", PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPending,
		}); err != nil {
			t.Fatalf("seed event %d: %v", index, err)
		}
	}
	query := `DELETE FROM event WHERE sequence > 1`
	if deleteAll {
		query = `DELETE FROM event`
	}
	if _, err := db.DB().ExecContext(ctx, query); err != nil {
		t.Fatalf("delete seeded events: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}
	return path
}
