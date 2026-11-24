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
// lifetime of the daemon. Monitor reacquires it after a connection loss; if
// another daemon takes it first, the caller must stop serving.
type InstanceLock struct {
	db        *sql.DB
	mu        sync.Mutex
	conn      *sql.Conn
	key       int64
	closed    chan struct{}
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
	acquired, err := tryInstanceLock(ctx, conn, key)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !acquired {
		_ = conn.Close()
		return nil, ErrInstanceLocked
	}
	return &InstanceLock{db: db, conn: conn, key: key, closed: make(chan struct{})}, nil
}

// Monitor keeps the advisory lock alive. A dropped PostgreSQL session releases
// the lock; Monitor first tries to reacquire it for retryWindow so a brief
// database restart does not require a daemon restart. If another instance took
// the lock, or the database stays unavailable past the window, Monitor returns
// an error and the daemon must stop.
func (l *InstanceLock) Monitor(ctx context.Context, interval, retryWindow time.Duration) error {
	if l == nil {
		return nil
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if retryWindow <= 0 {
		retryWindow = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-l.closed:
			return nil
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, interval)
		err := l.ping(pingCtx)
		cancel()
		if err == nil {
			continue
		}
		if err := l.reacquire(ctx, retryWindow); err != nil {
			return err
		}
	}
}

func (l *InstanceLock) ping(ctx context.Context) error {
	l.mu.Lock()
	conn := l.conn
	l.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("PostgreSQL instance lock connection is not held")
	}
	return conn.PingContext(ctx)
}

func (l *InstanceLock) reacquire(ctx context.Context, retryWindow time.Duration) error {
	l.mu.Lock()
	old := l.conn
	l.conn = nil
	l.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	// The retry window must bound the operations that can block, not just the
	// pause between attempts: pool acquisition and the advisory-lock query can
	// hang while another daemon or a saturated pool holds the database.
	retryCtx, cancel := context.WithTimeout(ctx, retryWindow)
	defer cancel()
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-l.closed:
			return nil
		default:
		}
		if retryCtx.Err() != nil {
			return fmt.Errorf("PostgreSQL instance lock could not be reacquired within %s: %w", retryWindow, lastErr)
		}
		conn, err := l.db.Conn(retryCtx)
		if err == nil {
			acquired, lockErr := tryInstanceLock(retryCtx, conn, l.key)
			if lockErr == nil && acquired {
				l.mu.Lock()
				select {
				case <-l.closed:
					l.mu.Unlock()
					_ = conn.Close()
					return nil
				default:
				}
				l.conn = conn
				l.mu.Unlock()
				return nil
			}
			_ = conn.Close()
			if lockErr != nil {
				err = lockErr
			} else {
				err = ErrInstanceLocked
			}
		}
		lastErr = err
		if errors.Is(err, ErrInstanceLocked) {
			return fmt.Errorf("PostgreSQL instance lock was taken by another daemon while reconnecting: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-l.closed:
			return nil
		case <-retryCtx.Done():
			return fmt.Errorf("PostgreSQL instance lock could not be reacquired within %s: %w", retryWindow, lastErr)
		case <-time.After(time.Second):
		}
	}
}

func tryInstanceLock(ctx context.Context, conn *sql.Conn, key int64) (bool, error) {
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
		return false, fmt.Errorf("acquire PostgreSQL instance lock: %w", err)
	}
	return acquired, nil
}

func (l *InstanceLock) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		close(l.closed)
		l.mu.Lock()
		conn := l.conn
		l.conn = nil
		l.mu.Unlock()
		if conn == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, l.key).Scan(&released); err != nil {
			l.closeErr = fmt.Errorf("release PostgreSQL instance lock: %w", err)
		} else if !released {
			l.closeErr = fmt.Errorf("release PostgreSQL instance lock: lock was not held")
		}
		if err := conn.Close(); err != nil {
			l.closeErr = errors.Join(l.closeErr, fmt.Errorf("close PostgreSQL instance lock connection: %w", err))
		}
	})
	return l.closeErr
}
