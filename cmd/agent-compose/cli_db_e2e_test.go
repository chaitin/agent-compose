package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storage/pgcopy"
	"github.com/chaitin/agent-compose/pkg/storage/postgres"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

// TestE2ECLIDatabasePostgresWorkflow walks the operator workflow for the
// PostgreSQL backend through the public CLI: inspect an empty schema, migrate
// it, copy an offline SQLite database into another schema, and read the copied
// rows back.
func TestE2ECLIDatabasePostgresWorkflow(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set AGENT_COMPOSE_TEST_POSTGRES_DSN to run the PostgreSQL database CLI workflow")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	admin := openDatabaseTestAdmin(t, ctx, dsn)
	schema := fmt.Sprintf("ac_cli_e2e_%d", time.Now().UnixNano())
	copySchema := schema + "_copy"
	textCopySchema := schema + "_text_copy"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, name := range []string{textCopySchema, copySchema, schema} {
			if _, err := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+name+" CASCADE"); err != nil {
				t.Logf("drop schema %s: %v", name, err)
			}
		}
		_ = admin.Close()
	})
	for _, name := range []string{schema, copySchema, textCopySchema} {
		if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+name); err != nil {
			t.Fatalf("create schema %s: %v", name, err)
		}
	}

	t.Setenv("DATA_ROOT", t.TempDir())
	t.Setenv("DB_DRIVER", "postgres")
	t.Setenv("DB_URL", postgresURLWithSearchPath(dsn, schema))

	status := runDatabaseStatusJSON(t, "db", "status", "--json")
	if status.Driver != "postgres" || status.Applied != 0 || status.Pending != 2 || status.Latest != 21 {
		t.Fatalf("db status before migrate = %+v", status)
	}
	if status.SQLitePath != "" || status.Target == "" {
		t.Fatalf("db status paths = %+v", status)
	}

	migrateOut, migrateErr, _, migrateCode := executeCLICommand("db", "migrate")
	if migrateCode != 0 || migrateErr != "" {
		t.Fatalf("db migrate code/stderr = %d / %q", migrateCode, migrateErr)
	}
	if !strings.Contains(migrateOut, "database migrated: driver=postgres applied=21 latest=21") {
		t.Fatalf("db migrate output = %q", migrateOut)
	}

	statusOut, statusErr, _, statusCode := executeCLICommand("db", "status")
	if statusCode != 0 || statusErr != "" {
		t.Fatalf("db status text code/stderr = %d / %q", statusCode, statusErr)
	}
	if !strings.Contains(statusOut, "driver: postgres") || !strings.Contains(statusOut, "schema: applied=21 latest=21 pending=0") {
		t.Fatalf("db status text = %q", statusOut)
	}

	status = runDatabaseStatusJSON(t, "db", "status", "--json")
	if status.Applied != status.Latest || status.Pending != 0 || len(status.Migrations) == 0 || !status.Migrations[0].Applied {
		t.Fatalf("db status after migrate = %+v", status)
	}

	source := seedDatabaseCopySource(t, ctx)
	copyOut, copyErr, _, copyCode := executeCLICommand("db", "copy",
		"--source", source,
		"--to", postgresURLWithSearchPath(dsn, copySchema),
		"--batch-size", "2",
		"--json")
	if copyCode != 0 || copyErr != "" {
		t.Fatalf("db copy code/stderr = %d / %q", copyCode, copyErr)
	}
	var copied pgcopy.Result
	if err := json.Unmarshal([]byte(copyOut), &copied); err != nil {
		t.Fatalf("decode db copy output: %v\n%s", err, copyOut)
	}
	if len(copied.Tables) == 0 || copied.EventSequence != 1 {
		t.Fatalf("db copy result = %+v", copied)
	}

	target, err := postgres.Open(ctx, postgres.Config{URL: postgresURLWithSearchPath(dsn, copySchema), ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("open copy target: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })
	store := configstore.FromDBWithDialect(target.DB(), dialect.Postgres())
	project, err := store.GetProject(ctx, "project-cli-copy")
	if err != nil {
		t.Fatalf("read copied project: %v", err)
	}
	if project.Name != "cli-copy-project" {
		t.Fatalf("copied project name = %q", project.Name)
	}
	volume, err := store.GetVolume(ctx, "volume-cli-copy")
	if err != nil {
		t.Fatalf("read copied volume: %v", err)
	}
	if volume.Name != "cli-copy-volume" {
		t.Fatalf("copied volume name = %q", volume.Name)
	}

	copyURL := postgresURLWithSearchPath(dsn, copySchema)
	_, nonEmptyErr, _, nonEmptyCode := executeCLICommand("db", "copy", "--source", source, "--to", copyURL, "--json")
	if nonEmptyCode == 0 || !strings.Contains(nonEmptyErr, "not empty") {
		t.Fatalf("db copy into non-empty target code/stderr = %d / %q", nonEmptyCode, nonEmptyErr)
	}

	_, missingErr, _, missingCode := executeCLICommand("db", "copy",
		"--source", filepath.Join(t.TempDir(), "missing.db"),
		"--to", postgresURLWithSearchPath(dsn, textCopySchema))
	if missingCode == 0 || !strings.Contains(missingErr, "read SQLite source") {
		t.Fatalf("db copy with missing source code/stderr = %d / %q", missingCode, missingErr)
	}

	textOut, textErr, _, textCode := executeCLICommand("db", "copy",
		"--source", source,
		"--to", postgresURLWithSearchPath(dsn, textCopySchema))
	if textCode != 0 || textErr != "" {
		t.Fatalf("db copy text code/stderr = %d / %q", textCode, textErr)
	}
	if !strings.Contains(textOut, "copied 30 tables, 3 rows; event sequence=1") {
		t.Fatalf("db copy text = %q", textOut)
	}
}

// TestE2ECLIDatabaseSQLiteWorkflow covers the default backend: an operator can
// inspect the data.db before it exists, migrate it, and inspect it again.
func TestE2ECLIDatabaseSQLiteWorkflow(t *testing.T) {
	t.Setenv("DATA_ROOT", t.TempDir())
	t.Setenv("DB_DRIVER", "")
	t.Setenv("DB_URL", "")

	status := runDatabaseStatusJSON(t, "db", "status", "--json")
	if status.Driver != "sqlite" || status.Applied != 0 || status.Pending != 1 || status.SQLitePath == "" {
		t.Fatalf("db status before migrate = %+v", status)
	}

	migrateOut, migrateErr, _, migrateCode := executeCLICommand("db", "migrate", "--json")
	if migrateCode != 0 || migrateErr != "" {
		t.Fatalf("db migrate code/stderr = %d / %q", migrateCode, migrateErr)
	}
	var migrated dbStatusOutput
	if err := json.Unmarshal([]byte(migrateOut), &migrated); err != nil {
		t.Fatalf("decode db migrate output: %v\n%s", err, migrateOut)
	}
	if migrated.Applied != migrated.Latest || migrated.Pending != 0 {
		t.Fatalf("db migrate result = %+v", migrated)
	}

	statusOut, statusErr, _, statusCode := executeCLICommand("db", "status")
	if statusCode != 0 || statusErr != "" {
		t.Fatalf("db status text code/stderr = %d / %q", statusCode, statusErr)
	}
	if !strings.Contains(statusOut, "driver: sqlite") || !strings.Contains(statusOut, "pending=0") {
		t.Fatalf("db status text = %q", statusOut)
	}
}

func runDatabaseStatusJSON(t *testing.T, args ...string) dbStatusOutput {
	t.Helper()
	stdout, stderr, _, code := executeCLICommand(args...)
	if code != 0 || stderr != "" {
		t.Fatalf("%v code/stderr = %d / %q", args, code, stderr)
	}
	var status dbStatusOutput
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("decode %v output: %v\n%s", args, err, stdout)
	}
	return status
}

// seedDatabaseCopySource builds a migrated SQLite data.db with one project and
// one volume, the smallest row set that proves copy preservation end to end.
func seedDatabaseCopySource(t *testing.T, ctx context.Context) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := storagesqlite.Open(path, 5*time.Second)
	if err != nil {
		t.Fatalf("open SQLite source: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = db.Close()
		}
	})
	store := configstore.FromDB(db.DB())
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: "project-cli-copy", Name: "cli-copy-project", SourcePath: "/tmp/cli-copy", SourceJSON: `{}`,
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := store.CreateVolume(ctx, domain.VolumeRecord{
		ID: "volume-cli-copy", Name: "cli-copy-volume", Driver: domain.VolumeDriverLocal,
	}); err != nil {
		t.Fatalf("seed volume: %v", err)
	}
	if _, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "event-cli-copy", Topic: "cli.copy", Source: domain.TopicEventSourceWebhook,
		CorrelationID: "corr-cli-copy", PayloadHash: "hash", PayloadJSON: `{}`,
		DispatchStatus: domain.TopicEventDispatchPending,
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close SQLite source: %v", err)
	}
	closed = true
	return path
}

func openDatabaseTestAdmin(t *testing.T, ctx context.Context, dsn string) *sql.DB {
	t.Helper()
	db, err := postgres.Connect(ctx, postgres.Config{URL: dsn, ConnectTimeout: 5 * time.Second, MaxOpenConns: 2, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("connect to PostgreSQL: %v", err)
	}
	return db
}

func postgresURLWithSearchPath(dsn, schema string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "search_path=" + schema
}
