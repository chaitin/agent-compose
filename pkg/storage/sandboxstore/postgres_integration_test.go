package sandboxstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storage/postgres"
)

const postgresSandboxTestDSNEnv = "AGENT_COMPOSE_TEST_POSTGRES_DSN"

func TestPostgresSandboxStoreIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db := openPostgresSandboxDatabase(t, ctx)

	// The cache's health check and schema validation must work on PostgreSQL.
	idx, needsRebuild, err := openSandboxCacheDBWithDialect(ctx, db.DB(), dialect.Postgres())
	if err != nil {
		t.Fatalf("open sandbox cache: %v", err)
	}
	if !needsRebuild {
		t.Fatal("empty sandbox projection should need a rebuild")
	}
	now := time.Now().UTC()
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID: "sandbox-pg", ShortID: "spg", Title: "Needle Sandbox",
			TriggerSource: "cli", Driver: "docker", VMStatus: "running",
			CreatedAt: now, UpdatedAt: now,
		},
		WorkspaceID: "workspace-1",
		Workspace:   &domain.SandboxWorkspace{ID: "workspace-2", Name: "Workspace Needle", Type: "file"},
	}
	if err := idx.Reconcile(ctx, sandbox, "project-pg"); err != nil {
		t.Fatalf("reconcile sandbox: %v", err)
	}
	page, total, err := idx.list(ctx, domain.SandboxListOptions{TitleQuery: "needle"}, func(id string) string {
		return "/tmp/" + id
	})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if total != 1 || len(page) != 1 || page[0].Summary.ID != sandbox.Summary.ID {
		t.Fatalf("list result total=%d page=%+v", total, page)
	}
	if _, _, err := idx.list(ctx, domain.SandboxListOptions{WorkspaceQuery: "needle"}, func(id string) string { return id }); err != nil {
		t.Fatalf("workspace query: %v", err)
	}
	if err := idx.Delete(ctx, sandbox.Summary.ID); err != nil {
		t.Fatalf("delete sandbox row: %v", err)
	}

	// A full store construction exercises quick check, schema validation, the
	// projection invalidate/rebuild path, and the empty-filesystem reconcile.
	config := &appconfig.Config{SandboxRoot: t.TempDir(), DbAddr: "postgres"}
	store, err := NewWithDatabaseWithDialect(config, db.DB(), dialect.Postgres())
	if err != nil {
		t.Fatalf("new sandbox store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close sandbox store: %v", err)
		}
	})
	result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list sandboxes from store: %v", err)
	}
	if len(result.Sandboxes) != 0 {
		t.Fatalf("empty root listed %d sandboxes", len(result.Sandboxes))
	}
}

func openPostgresSandboxDatabase(t *testing.T, ctx context.Context) *postgres.Database {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(postgresSandboxTestDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", postgresSandboxTestDSNEnv)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("ac_sandbox_%d", time.Now().UnixNano())
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
	db, err := postgres.Open(ctx, postgres.Config{
		URL:            dsn + separator + "search_path=" + schema,
		MaxOpenConns:   6,
		MaxIdleConns:   3,
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open postgres database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
