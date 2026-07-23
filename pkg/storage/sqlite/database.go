package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const defaultBusyTimeout = 16 * time.Second

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
	return &Database{db: db}, nil
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

func sqliteDSN(path string, busyTimeout time.Duration) string {
	if busyTimeout <= 0 {
		busyTimeout = defaultBusyTimeout
	}
	var uri *url.URL
	if len(path) >= len("file:") && path[:len("file:")] == "file:" {
		if parsed, err := url.Parse(path); err == nil {
			uri = parsed
		}
	}
	if uri == nil {
		if path == ":memory:" {
			uri = &url.URL{Scheme: "file", Opaque: ":memory:"}
		} else {
			// 相对路径必须省略 host 分隔符，否则首段会被 SQLite 解释为 URI authority。
			uri = &url.URL{Scheme: "file", Path: path, OmitHost: !filepath.IsAbs(path)}
		}
	}
	query := uri.Query()
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	query.Add("_pragma", "journal_mode(WAL)")
	uri.RawQuery = query.Encode()
	return uri.String()
}
