package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storage/postgres"
)

func TestOpenSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	owner, err := Open(context.Background(), Config{
		Driver:             DriverSQLite,
		SQLitePath:         path,
		SQLiteBusyTimeout:  5 * time.Second,
		SQLiteMaxOpenConns: 2,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if owner.Dialect().Name() != dialect.NameSQLite {
		t.Fatalf("Dialect().Name() = %q", owner.Dialect().Name())
	}
	if err := owner.DB().Ping(); err != nil {
		t.Fatalf("ping SQLite: %v", err)
	}
}

func TestOpenRejectsUnknownDriver(t *testing.T) {
	if _, err := Open(context.Background(), Config{Driver: Driver("mysql")}); err == nil {
		t.Fatal("Open() error = nil, want unsupported driver error")
	}
}

func TestOpenPostgresRejectsSingleConnectionWithInstanceLock(t *testing.T) {
	_, err := Open(context.Background(), Config{
		Driver:              DriverPostgres,
		AcquirePostgresLock: true,
		Postgres:            postgres.Config{URL: "postgres://db.example.com/agent_compose", MaxOpenConns: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "DB_MAX_OPEN_CONNS") {
		t.Fatalf("Open() error = %v, want DB_MAX_OPEN_CONNS guidance", err)
	}
}
