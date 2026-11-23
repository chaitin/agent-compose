package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const defaultApplicationName = "agent-compose"

// Config is the PostgreSQL connection and pool configuration. It is built by
// the composition root from the daemon's environment; the storage layer does
// not read environment variables.
type Config struct {
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnectTimeout  time.Duration
	ApplicationName string
}

// Database owns the application's PostgreSQL connection pool. All storage
// facades receive DB() and must not close it themselves.
type Database struct {
	db        *sql.DB
	closeOnce sync.Once
	closeErr  error
}

// Connect opens a PostgreSQL pool and verifies connectivity without applying
// migrations. It is used by read-only tooling such as `db status`. The caller
// owns the returned pool.
func Connect(ctx context.Context, config Config) (*sql.DB, error) {
	if strings.TrimSpace(config.URL) == "" {
		return nil, fmt.Errorf("PostgreSQL URL is required")
	}
	parsed, err := parsePostgresConfig(config.URL)
	if err != nil {
		return nil, err
	}
	if config.ConnectTimeout > 0 {
		parsed.ConnectTimeout = config.ConnectTimeout
	}
	applicationName := strings.TrimSpace(config.ApplicationName)
	if applicationName == "" {
		applicationName = defaultApplicationName
	}
	if parsed.RuntimeParams == nil {
		parsed.RuntimeParams = make(map[string]string)
	}
	if _, exists := parsed.RuntimeParams["application_name"]; !exists {
		parsed.RuntimeParams["application_name"] = applicationName
	}

	db := sql.OpenDB(newConnectorFromConfig(*parsed))
	if config.MaxOpenConns > 0 {
		db.SetMaxOpenConns(config.MaxOpenConns)
	}
	if config.MaxIdleConns >= 0 {
		db.SetMaxIdleConns(config.MaxIdleConns)
	}
	if config.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(config.ConnMaxLifetime)
	}

	connectCtx := ctx
	cancel := func() {}
	if config.ConnectTimeout > 0 {
		connectCtx, cancel = context.WithTimeout(ctx, config.ConnectTimeout)
	}
	defer cancel()
	if err := db.PingContext(connectCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return db, nil
}

// Open connects to PostgreSQL, verifies connectivity, and applies pending
// migrations. The caller must Close the returned owner. Open does not acquire
// the single-instance lock; the composition root does that explicitly.
func Open(ctx context.Context, config Config) (*Database, error) {
	db, err := Connect(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Database{db: db}, nil
}

func (d *Database) DB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

func (d *Database) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	d.closeOnce.Do(func() { d.closeErr = d.db.Close() })
	return d.closeErr
}

// Shutdown adapts Close to the samber/do Shutdowner interface.
func (d *Database) Shutdown() error {
	return d.Close()
}

// ErrInstanceLocked means another daemon instance holds the PostgreSQL
// database. Multi-instance operation is not supported yet, so callers must
// refuse to start instead of risking the other instance's runs and sandboxes.
var ErrInstanceLocked = errors.New("another agent-compose instance is using this PostgreSQL database; multi-instance operation is not supported")

// InstanceLock is a session-scoped PostgreSQL advisory lock held for the
// lifetime of the daemon. Losing the underlying connection releases it.
type InstanceLock struct {
	conn      *sql.Conn
	key       int64
	closeOnce sync.Once
	closeErr  error
}

// AcquireInstanceLock takes the single-daemon advisory lock. The returned lock
// must be released with Close during shutdown.
func AcquireInstanceLock(ctx context.Context, db *sql.DB) (*InstanceLock, error) {
	if db == nil {
		return nil, fmt.Errorf("PostgreSQL database is required")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire PostgreSQL instance lock connection: %w", err)
	}
	var database string
	if err := conn.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read PostgreSQL database name: %w", err)
	}
	key := advisoryKey("agent-compose:instance:" + database)
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("acquire PostgreSQL instance lock: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, ErrInstanceLocked
	}
	return &InstanceLock{conn: conn, key: key}, nil
}

func (l *InstanceLock) Close() error {
	if l == nil || l.conn == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released bool
		if err := l.conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, l.key).Scan(&released); err != nil {
			l.closeErr = fmt.Errorf("release PostgreSQL instance lock: %w", err)
		} else if !released {
			l.closeErr = fmt.Errorf("release PostgreSQL instance lock: lock was not held")
		}
		if err := l.conn.Close(); err != nil {
			l.closeErr = errors.Join(l.closeErr, fmt.Errorf("close PostgreSQL instance lock connection: %w", err))
		}
	})
	return l.closeErr
}
