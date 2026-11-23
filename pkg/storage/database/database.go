// Package database selects and owns the daemon's application database.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storage/postgres"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

// Driver selects the application database backend.
type Driver string

const (
	DriverSQLite   Driver = "sqlite"
	DriverPostgres Driver = "postgres"
)

// Config is the composition-level database configuration. The daemon maps its
// environment-derived config into this type; storage packages never read the
// environment.
type Config struct {
	Driver Driver

	SQLitePath         string
	SQLiteBusyTimeout  time.Duration
	SQLiteMaxOpenConns int

	Postgres postgres.Config
	// AcquirePostgresLock holds the single-daemon advisory lock for the
	// lifetime of the returned Database. The daemon sets it; tests that need
	// several isolated owners over one server do not.
	AcquirePostgresLock bool
}

// Database owns the application connection pool and its backend dialect.
type Database struct {
	db      *sql.DB
	dialect dialect.Dialect

	lock      interface{ Close() error }
	closeOnce sync.Once
	closeErr  error
}

// Open opens, migrates, and owns the selected backend.
func Open(ctx context.Context, config Config) (*Database, error) {
	switch config.Driver {
	case DriverSQLite, "":
		sqliteDB, err := storagesqlite.OpenWithMaxOpenConns(
			config.SQLitePath,
			config.SQLiteBusyTimeout,
			config.SQLiteMaxOpenConns,
		)
		if err != nil {
			return nil, err
		}
		return &Database{db: sqliteDB.DB(), dialect: dialect.SQLite(), lock: sqliteDB}, nil
	case DriverPostgres:
		if config.AcquirePostgresLock && config.Postgres.MaxOpenConns == 1 {
			return nil, fmt.Errorf("PostgreSQL mode requires DB_MAX_OPEN_CONNS of at least 2 because one connection holds the single-instance lock")
		}
		postgresDB, err := postgres.Open(ctx, config.Postgres)
		if err != nil {
			return nil, err
		}
		var lock *postgres.InstanceLock
		if config.AcquirePostgresLock {
			lock, err = postgres.AcquireInstanceLock(ctx, postgresDB.DB())
			if err != nil {
				_ = postgresDB.Close()
				return nil, err
			}
		}
		return &Database{db: postgresDB.DB(), dialect: dialect.Postgres(), lock: compositeCloser{lock: lock, database: postgresDB}}, nil
	default:
		return nil, fmt.Errorf("unsupported database driver %q", config.Driver)
	}
}

func (d *Database) DB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

func (d *Database) Dialect() dialect.Dialect {
	if d == nil {
		return dialect.SQLite()
	}
	return dialect.OrSQLite(d.dialect)
}

func (d *Database) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		if d.lock != nil {
			d.closeErr = d.lock.Close()
		}
	})
	return d.closeErr
}

// Shutdown adapts Close to the samber/do Shutdowner interface.
func (d *Database) Shutdown() error {
	return d.Close()
}

// compositeCloser releases the instance lock before closing the database.
type compositeCloser struct {
	lock     *postgres.InstanceLock
	database *postgres.Database
}

func (c compositeCloser) Close() error {
	if c.lock != nil {
		if err := c.lock.Close(); err != nil {
			_ = c.database.Close()
			return err
		}
	}
	return c.database.Close()
}
