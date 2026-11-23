// Package database selects and owns the daemon's application database.
package database

import (
	"context"
	"database/sql"
	"errors"
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

	sqlite   *storagesqlite.Database
	postgres *postgres.Database
	lock     *postgres.InstanceLock

	lockLost      chan error
	monitorCancel context.CancelFunc
	monitorDone   chan struct{}

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
		return &Database{db: sqliteDB.DB(), dialect: dialect.SQLite(), sqlite: sqliteDB}, nil
	case DriverPostgres:
		if config.AcquirePostgresLock && config.Postgres.MaxOpenConns == 1 {
			return nil, fmt.Errorf("PostgreSQL mode requires DB_MAX_OPEN_CONNS of at least 2 because one connection holds the single-instance lock")
		}
		postgresDB, err := postgres.Open(ctx, config.Postgres)
		if err != nil {
			return nil, err
		}
		owner := &Database{
			db:       postgresDB.DB(),
			dialect:  dialect.Postgres(),
			postgres: postgresDB,
		}
		if config.AcquirePostgresLock {
			lock, err := postgres.AcquireInstanceLock(ctx, postgresDB.DB())
			if err != nil {
				_ = postgresDB.Close()
				return nil, err
			}
			owner.lock = lock
			owner.lockLost = make(chan error, 1)
			monitorCtx, cancel := context.WithCancel(ctx)
			owner.monitorCancel = cancel
			owner.monitorDone = make(chan struct{})
			go func() {
				defer close(owner.monitorDone)
				if err := lock.Monitor(monitorCtx, 5*time.Second, 30*time.Second); err != nil {
					select {
					case owner.lockLost <- err:
					default:
					}
				}
			}()
		}
		return owner, nil
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

// LockLost reports a lost single-instance lock. A nil channel means the
// backend has no instance lock (SQLite) or no lock was requested.
func (d *Database) LockLost() <-chan error {
	if d == nil {
		return nil
	}
	return d.lockLost
}

func (d *Database) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		if d.monitorCancel != nil {
			d.monitorCancel()
		}
		if d.monitorDone != nil {
			select {
			case <-d.monitorDone:
			case <-time.After(5 * time.Second):
				d.closeErr = fmt.Errorf("timed out stopping PostgreSQL instance lock monitor")
			}
		}
		if d.lock != nil {
			d.closeErr = errors.Join(d.closeErr, d.lock.Close())
		}
		if d.postgres != nil {
			d.closeErr = errors.Join(d.closeErr, d.postgres.Close())
		}
		if d.sqlite != nil {
			d.closeErr = errors.Join(d.closeErr, d.sqlite.Close())
		}
	})
	return d.closeErr
}

// Shutdown adapts Close to the samber/do Shutdowner interface.
func (d *Database) Shutdown() error {
	return d.Close()
}
