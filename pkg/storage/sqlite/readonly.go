package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"
)

// OpenReadOnly opens an existing application database for read-only queries.
// Unlike Open it never migrates and never writes: the DSN opens the file with
// mode=ro and enables PRAGMA query_only as a second guard. A live daemon keeps
// the WAL shared-memory file in place, so read-only connections can observe a
// consistent snapshot without blocking the writer.
//
// The returned Database owns the connection; callers must Close it.
func OpenReadOnly(path string) (*Database, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("SQLite database path is required")
	}
	if isMemoryDatabase(path) {
		return nil, fmt.Errorf("read-only SQLite access requires a file database")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database read-only: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("open SQLite database read-only: %q is a directory", path)
	}
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path, defaultBusyTimeout))
	if err != nil {
		return nil, fmt.Errorf("open SQLite database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open SQLite database read-only: %w", err)
	}
	return &Database{db: db}, nil
}

func sqliteReadOnlyDSN(path string, busyTimeout time.Duration) string {
	if busyTimeout <= 0 {
		busyTimeout = defaultBusyTimeout
	}
	uri := sqliteURI(path)
	query := uri.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	uri.RawQuery = query.Encode()
	return uri.String()
}
