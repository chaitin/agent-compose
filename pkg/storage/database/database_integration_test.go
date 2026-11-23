package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storage/postgres"
)

func TestPostgresDatabaseOwnerIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set AGENT_COMPOSE_TEST_POSTGRES_DSN to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("ac_database_%d", time.Now().UnixNano())
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
	config := Config{
		Driver: DriverPostgres,
		Postgres: postgres.Config{
			URL:            dsn + separator + "search_path=" + schema,
			MaxOpenConns:   4,
			MaxIdleConns:   2,
			ConnectTimeout: 5 * time.Second,
		},
		AcquirePostgresLock: true,
	}

	first, err := Open(ctx, config)
	if err != nil {
		t.Fatalf("open first owner: %v", err)
	}
	if first.Dialect().Name() != dialect.NamePostgres {
		t.Fatalf("dialect = %q", first.Dialect().Name())
	}
	if err := first.DB().PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, err := Open(ctx, config); !errors.Is(err, postgres.ErrInstanceLocked) {
		t.Fatalf("second owner error = %v, want ErrInstanceLocked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first owner: %v", err)
	}
	second, err := Open(ctx, config)
	if err != nil {
		t.Fatalf("open after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second owner: %v", err)
	}
}
