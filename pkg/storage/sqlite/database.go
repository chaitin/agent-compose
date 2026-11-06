package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultBusyTimeout = 16 * time.Second

	// DefaultMaxOpenConns is the runtime connection limit used by Open.
	DefaultMaxOpenConns = 4
)

// Database owns the application's shared SQLite connection. All storage
// facades receive DB() and must not close it themselves.
type Database struct {
	db        *sql.DB
	closeOnce sync.Once
	closeErr  error
}

// Open opens and migrates one application database. The returned Database owns
// the connection and must be closed by its composition root.
func Open(path string, busyTimeout time.Duration) (*Database, error) {
	return OpenWithMaxOpenConns(path, busyTimeout, DefaultMaxOpenConns)
}

// OpenWithMaxOpenConns opens and migrates one application database, then applies
// the provided runtime connection limit.
func OpenWithMaxOpenConns(path string, busyTimeout time.Duration, runtimeMaxOpenConns int) (*Database, error) {
	if runtimeMaxOpenConns <= 0 {
		return nil, fmt.Errorf("SQLite maximum open connections must be positive")
	}
	if isMemoryDatabase(path) {
		runtimeMaxOpenConns = 1
	}
	db, err := sql.Open("sqlite", sqliteDSN(path, busyTimeout))
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping SQLite database: %w", err)
	}
	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	db.SetMaxOpenConns(runtimeMaxOpenConns)
	db.SetMaxIdleConns(runtimeMaxOpenConns)
	return &Database{db: db}, nil
}

func isMemoryDatabase(path string) bool {
	path = strings.TrimSpace(path)
	if path == ":memory:" {
		return true
	}
	uri, err := url.Parse(path)
	if err != nil || uri.Scheme != "file" {
		return false
	}
	return uri.Opaque == ":memory:" || uri.Path == ":memory:" || strings.EqualFold(uri.Query().Get("mode"), "memory")
}

// DB returns the shared connection. Database retains ownership; callers must
// not close the returned handle.
func (d *Database) DB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

// Close releases the database owner. It is safe to call more than once.
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

// sqliteURI resolves a database path to the SQLite file URI the driver expects.
// Relative paths must omit the host separator, otherwise the first segment is
// interpreted as a URI authority.
func sqliteURI(path string) *url.URL {
	if len(path) >= len("file:") && path[:len("file:")] == "file:" {
		if parsed, err := url.Parse(path); err == nil {
			return parsed
		}
	}
	if path == ":memory:" {
		return &url.URL{Scheme: "file", Opaque: ":memory:"}
	}
	return &url.URL{Scheme: "file", Path: path, OmitHost: !filepath.IsAbs(path)}
}

func sqliteDSN(path string, busyTimeout time.Duration) string {
	if busyTimeout <= 0 {
		busyTimeout = defaultBusyTimeout
	}
	uri := sqliteURI(path)
	query := uri.Query()
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	query.Add("_pragma", "journal_mode(WAL)")
	uri.RawQuery = query.Encode()
	return uri.String()
}
